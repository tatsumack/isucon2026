package isureal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"golang.org/x/sync/singleflight"
)

// 投稿と RealMoji の画像本体は、DB ではなく asia1 (転送先) のディスクに置く。
// DB の image_objects には行 (所有者・種類・作成時刻) を残し、data は空にする。配信してよいかの確認は今までどおり DB で行う。
// プロフィール画像は各リージョンのサーバで書かれるため、DB に置いたままにする。
//
// ディレクトリ (ImageDir) の構成:
//   - new/<id[:2]>/<id>: ベンチ中に書かれた画像。asia1 だけに置き、/initialize で消す
//   - initial/<id[:2]>/<id>: 初期データの画像。cmd/extract-images で DB から書き出す。足りない分は読んだときに埋める
//
// europe1 / america1 は、手元に無い画像を asia1 の内部 API (/internal/images/:id) から取る。
// 取ってきた画像のうち、初期データのものだけを自分の initial/ に保存する。初期データの画像は /initialize でも変わらないため、消す必要がない。
// ベンチ中の画像はメモリのキャッシュ (imageDataCache) にだけ持つ。
//
// image_objects の行を消すのはプロフィール画像の変更・削除 (profile_image_api.go) だけなので、ファイルを消す処理は要らない。
// 行が消えた画像は、DB の確認で 403 になるため配信されない。

const imageInitialHeader = "X-Isureal-Image-Initial"

var errImageNotFound = errors.New("image not found")

type ImageStore struct {
	dir string
	db  *sqlx.DB
	// asia1 の URL。空なら、このサーバが画像の置き場 (asia1)
	originURL string
	client    *http.Client
	fetches   singleflight.Group
}

func NewImageStore(config *Config, db *sqlx.DB) *ImageStore {
	return &ImageStore{
		dir:       config.ImageDir,
		db:        db,
		originURL: config.DelegateURL,
		client: &http.Client{Transport: &http.Transport{
			// delegate.go と同じく、リージョン間の往復ごとに接続を張らないよう使い回す
			MaxIdleConns:        1024,
			MaxIdleConnsPerHost: 1024,
			IdleConnTimeout:     90 * time.Second,
		}},
	}
}

func (s *ImageStore) isOrigin() bool {
	return s.originURL == ""
}

func (s *ImageStore) path(kind, id string) string {
	shard := id
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return filepath.Join(s.dir, kind, shard, filepath.Base(id))
}

// Save は新しい画像を new/ に書き、fsync してから返す。DB の行は呼び出し側が data を空にして INSERT する。
// 投稿と RealMoji の作成は asia1 へ転送されるため、asia1 以外で呼ばれることはない。
func (s *ImageStore) Save(id string, data []byte) error {
	if !s.isOrigin() {
		return fmt.Errorf("image writes must run on the origin server")
	}
	return writeFileAtomic(s.path("new", id), data, true)
}

// imageAccelPrefix は nginx の internal location (etc/nginx/sites-available/isureal.conf)。ImageDir を指す
const imageAccelPrefix = "/_images/"

// AccelPath は画像のファイルがこのサーバにあれば、nginx に渡すパス (X-Accel-Redirect の値) を返す。
// 探す順と、大きさ0のファイルを無いものと扱うのは load と同じ。
func (s *ImageStore) AccelPath(id string) (string, bool) {
	for _, kind := range []string{"new", "initial"} {
		path := s.path(kind, id)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			rel, err := filepath.Rel(s.dir, path)
			if err != nil {
				return "", false
			}
			return imageAccelPrefix + filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// Load は画像本体と、初期データの画像かどうかを返す。見つからなければ errImageNotFound を返す。
func (s *ImageStore) Load(ctx context.Context, id string) ([]byte, bool, error) {
	if cached, ok := imageDataCache.get(id); ok {
		return cached.data, cached.initial, nil
	}
	data, initial, err := s.load(ctx, id)
	if err != nil {
		return nil, false, err
	}
	imageDataCache.put(id, cachedImage{data: data, initial: initial})
	return data, initial, nil
}

func (s *ImageStore) load(ctx context.Context, id string) ([]byte, bool, error) {
	if data, ok := readImageFile(s.path("new", id)); ok {
		return data, false, nil
	}
	if data, ok := readImageFile(s.path("initial", id)); ok {
		return data, true, nil
	}
	if !s.isOrigin() {
		// 同じ画像を同時に取りに行かない (投稿直後に友達が一斉に見るため)
		// 最初に来たリクエストが切断されても、相乗りしている他のリクエストを失敗させない
		fetchCtx := context.WithoutCancel(ctx)
		v, err, _ := s.fetches.Do(id, func() (any, error) {
			// 直前に終わった取得がディスクに書いていれば、それを使う
			if data, ok := readImageFile(s.path("initial", id)); ok {
				return cachedImage{data: data, initial: true}, nil
			}
			data, initial, err := s.fetchFromOrigin(fetchCtx, id)
			if err != nil {
				return nil, err
			}
			if initial {
				// 異常停止で中身が途中で切れたファイルを配信しないよう、fsync してから置く
				_ = writeFileAtomic(s.path("initial", id), data, true)
			}
			return cachedImage{data: data, initial: initial}, nil
		})
		if err != nil {
			return nil, false, err
		}
		fetched := v.(cachedImage)
		return fetched.data, fetched.initial, nil
	}
	// ファイルが無いのは、書き出し前の初期データの画像かプロフィール画像。DB から読む
	var row struct {
		Data    []byte `db:"data"`
		Initial bool   `db:"initial"`
	}
	err := s.db.GetContext(ctx, &row, `
   SELECT data, created_at <= (SELECT created_at FROM initial_data_boundary) AS initial
   FROM image_objects WHERE id = $1
  `, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(row.Data) == 0) {
		return nil, false, errImageNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if row.Initial {
		_ = writeFileAtomic(s.path("initial", id), row.Data, true)
	}
	return row.Data, row.Initial, nil
}

func (s *ImageStore) fetchFromOrigin(ctx context.Context, id string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.originURL+"/internal/images/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, false, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, false, errImageNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetch image from origin: status %d", res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, errImageNotFound
	}
	return data, res.Header.Get(imageInitialHeader) == "1", nil
}

// Reset は /initialize から呼ぶ。asia1 では new/ を空にし、それ以外のサーバでは asia1 に依頼する。
// 古いディレクトリは名前を変えてから裏で消すので、初期化の時間はほとんど増えない。
func (s *ImageStore) Reset(ctx context.Context) error {
	if !s.isOrigin() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.originURL+"/internal/images/reset", nil)
		if err != nil {
			return err
		}
		res, err := s.client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			return fmt.Errorf("reset images on origin: status %d", res.StatusCode)
		}
		return nil
	}
	newDir := filepath.Join(s.dir, "new")
	trash := filepath.Join(s.dir, "trash-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Rename(newDir, trash); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	go os.RemoveAll(trash)
	return os.MkdirAll(newDir, 0o755)
}

// 大きさ0のファイルは、書き込み中に止まった跡として無いものと扱う
func readImageFile(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// writeFileAtomic は一時ファイルに書いてから名前を変える。読み手が書きかけのファイルを見ることはない。
// durable のときは fsync してから名前を変え、ディレクトリも fsync する (再起動後もデータが残るように)。
func writeFileAtomic(path string, data []byte, durable bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	// CreateTemp は 0600 で作る。nginx (www-data) が X-Accel-Redirect で読めるようにする
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if durable {
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if durable {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	return nil
}

// 内部 API。europe1 / america1 (ISUREAL_TRUSTED_PEERS) からだけ受け付ける。
// 署名を確かめずに画像を返すため、呼び出し側のサーバが署名と配信してよいかを確かめてから呼ぶ。
// nginx は /api/ だけをアプリへ渡すので、外部から nginx 経由では届かない。
func trustedPeerOnly(h *HandlerContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !isTrustedPeer(c.Request(), h.Config) {
				return NewAPIError(http.StatusForbidden, "forbidden")
			}
			return next(c)
		}
	}
}

func getInternalImageHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		id := c.Param("imageId")
		data, initial, err := h.Images.Load(c.Request().Context(), id)
		if errors.Is(err, errImageNotFound) {
			return c.NoContent(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		if initial {
			c.Response().Header().Set(imageInitialHeader, "1")
		}
		return c.Blob(http.StatusOK, "application/octet-stream", data)
	}
}

func resetInternalImagesHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := h.Images.Reset(c.Request().Context()); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
