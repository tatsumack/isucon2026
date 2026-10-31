package isureal

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

type getImageRequest struct {
	ImageID string `param:"imageId" json:"-"`
	Exp     string `query:"exp" json:"-"`
	Sig     string `query:"sig" json:"-"`
}

func getImageHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		c.Response().Header().Set("Cache-Control", "no-store")
		var input getImageRequest
		if err := c.Bind(&input); err != nil {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		query, err := url.ParseQuery(c.Request().URL.RawQuery)
		if err != nil || len(query["exp"]) != 1 || len(query["sig"]) != 1 {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		exp, err := strconv.ParseInt(input.Exp, 10, 64)
		if err != nil || strconv.FormatInt(exp, 10) != input.Exp || exp <= time.Now().Unix() {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		sig, err := base64.RawURLEncoding.Strict().DecodeString(input.Sig)
		if err != nil || len(sig) != sha256.Size || base64.RawURLEncoding.EncodeToString(sig) != input.Sig {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}

		// 署名鍵と削除状態を1クエリで取る。削除状態は毎回確認する。即時反映も5秒以内の許容範囲に含まれる。
		// 画像本体は署名を検証してから取る。
		ctx := c.Request().Context()
		var object struct {
			SecretKeyHash string `db:"secret_key_hash"`
			MediaType     string `db:"media_type"`
			Servable      bool   `db:"servable"`
		}
		err = h.DB.GetContext(ctx, &object, `
   SELECT u.secret_key_hash, i.media_type, (
     u.profile_image_id = i.id
     OR EXISTS (
       SELECT 1 FROM posts p
       WHERE p.image_id = i.id AND p.author_id = u.id AND p.deleted_at IS NULL
     )
     OR EXISTS (
       SELECT 1 FROM realmojis r
       JOIN posts p ON p.id = r.post_id AND p.deleted_at IS NULL
       JOIN users author ON author.id = p.author_id AND author.status = 'active'
       WHERE r.image_id = i.id AND r.reactor_id = u.id AND r.replaced_at IS NULL AND r.deleted_at IS NULL
     )
   ) IS TRUE AS servable
   FROM image_objects i JOIN users u ON u.id = i.owner_user_id
   WHERE i.id = $1 AND u.status = 'active'
  `, input.ImageID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		if err != nil {
			return err
		}
		key, err := imageSigningKey(object.SecretKeyHash)
		if err != nil {
			return err
		}
		if !hmac.Equal(sig, imageSignature(key, input.ImageID, exp)) {
			c.Set("abuse", "invalid_signature")
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		if !object.Servable {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}

		// メモリのキャッシュに無く、ファイルがこのサーバにあれば、nginx に返させる。Content-Type と Cache-Control はこの応答のものが使われる
		// キャッシュにある画像 (asia1 で保存した直後の投稿画像など) は、ディスクを読まないようアプリから返す (#23313)
		if _, cached := imageDataCache.get(input.ImageID); !cached {
			if path, ok := h.Images.AccelPath(input.ImageID); ok {
				if exp <= time.Now().Unix() {
					return NewAPIError(http.StatusForbidden, "image_forbidden")
				}
				c.Response().Header().Set(echo.HeaderContentType, object.MediaType)
				c.Response().Header().Set("X-Accel-Redirect", path)
				return c.NoContent(http.StatusOK)
			}
		}

		data, _, err := h.Images.Load(ctx, input.ImageID)
		if errors.Is(err, errImageNotFound) {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		if err != nil {
			return err
		}
		if exp <= time.Now().Unix() {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		return c.Blob(http.StatusOK, object.MediaType, data)
	}
}

// 画像本体のバイト列を画像IDごとに保持する (image_store.go の ImageStore.Load から使う)。画像IDはサーバが採番するUUIDで、
// 同じIDの中身が書き換わることはないため、無効化しない。配信してよいかは毎回DBで確認する。
// /initialize で消えた画像のエントリが残っても、DBの確認で403になるため配信されない。
// asia1 では保存した画像もここに入れる (ImageStore.Save)。投稿の画像は直後に友達や他リージョンから読まれるが、
// asia1 はページキャッシュが小さく (約300MB)、ファイルに書いた画像がすぐ追い出されてディスクから読み直していた (#19113)
var imageDataCache = newByteCache(256 << 20)

type cachedImage struct {
	data []byte
	// 初期データの画像か。asia1 の内部 API が europe1 / america1 に伝える (image_store.go)
	initial bool
}

type byteCache struct {
	mu      sync.RWMutex
	entries map[string]cachedImage
	// 入れた順のキー。古いものから追い出す
	order []string
	size  int
	limit int
}

func newByteCache(limit int) *byteCache {
	return &byteCache{entries: map[string]cachedImage{}, limit: limit}
}

func (c *byteCache) get(key string) (cachedImage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	image, ok := c.entries[key]
	return image, ok
}

// put は上限を超えるとき、古いエントリから追い出して空きを作る。
// 投稿の画像は書いた直後ほどよく読まれるため、新しいものを残す。
func (c *byteCache) put(key string, image cachedImage) {
	if len(image.data) > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		return
	}
	for len(c.order) > 0 && c.size+len(image.data) > c.limit {
		evictKey := c.order[0]
		c.order = c.order[1:]
		c.size -= len(c.entries[evictKey].data)
		delete(c.entries, evictKey)
	}
	c.entries[key] = image
	c.order = append(c.order, key)
	c.size += len(image.data)
}
