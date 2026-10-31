package isureal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

// asia1 の CPU には SHA-NI がなく、署名の計算が europe1 / america1 の約3.3倍かかる (bench-results/7477)。
// asia1 では、Memories・エクスポート・投稿の応答で返す署名を ISUREAL_SIGNING_URLS のサーバにまとめて計算させ、
// 結果を imageSignatureCache に入れる。signedImageURL はそのキャッシュを使うので、応答を組み立てる処理は変えない。
// 依頼に失敗したときは何もしない (signedImageURL がこれまでどおり asia1 で計算する)。
type ImageSigner struct {
	urls   []string
	client *http.Client
}

// 1回の依頼の待ち時間の上限。asia1 から europe1 / america1 への往復は約100ms。
const remoteImageSignatureTimeout = time.Second

func NewImageSigner(config *Config) *ImageSigner {
	return &ImageSigner{
		urls: config.SigningURLs,
		client: &http.Client{
			// 往復ごとに接続を張ると、TCP の接続だけで1往復 (約100ms) 増えるため、接続を使い回す。
			Transport: &http.Transport{
				MaxIdleConns:        1024,
				MaxIdleConnsPerHost: 1024,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

type imageSignatureTarget struct {
	Key     []byte `json:"key"`
	ImageID string `json:"imageId"`
}

// imageSignatureBatch は、1つの応答で返す署名の (鍵, 画像ID) を重複なく集める。
type imageSignatureBatch struct {
	seen    map[string]struct{}
	targets []imageSignatureTarget
}

func (b *imageSignatureBatch) add(user User, imageID string) {
	b.addKey(user.ImageKey, imageID)
}

func (b *imageSignatureBatch) addKey(key []byte, imageID string) {
	if b.seen == nil {
		b.seen = make(map[string]struct{})
	}
	cacheKey := string(key) + ":" + imageID
	if _, ok := b.seen[cacheKey]; ok {
		return
	}
	b.seen[cacheKey] = struct{}{}
	b.targets = append(b.targets, imageSignatureTarget{Key: key, ImageID: imageID})
}

// addProfileImage は、newGetMeResponse と同じ条件でプロフィール画像の署名を加える。
func (b *imageSignatureBatch) addProfileImage(user User) {
	if user.ProfileImageID != nil {
		b.add(user, *user.ProfileImageID)
	}
}

type imageSignaturesRequest struct {
	Exp     int64                  `json:"exp"`
	Targets []imageSignatureTarget `json:"targets"`
}

type imageSignaturesResponse struct {
	Signatures [][]byte `json:"signatures"`
}

// Prime は、batch のうちキャッシュにない署名を依頼先に計算させ、キャッシュに入れる。
// 依頼先が複数あるときは、等分して並行に依頼する。
func (s *ImageSigner) Prime(ctx context.Context, batch *imageSignatureBatch) {
	if s == nil || len(s.urls) == 0 {
		return
	}
	exp := imageURLExpiration(time.Now())
	pending := make([]imageSignatureTarget, 0, len(batch.targets))
	for _, target := range batch.targets {
		if _, ok := cachedImageSignature(target.Key, target.ImageID, exp); !ok {
			pending = append(pending, target)
		}
	}
	if len(pending) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remoteImageSignatureTimeout)
	defer cancel()
	chunkSize := (len(pending) + len(s.urls) - 1) / len(s.urls)
	var wg sync.WaitGroup
	for i, url := range s.urls {
		start := i * chunkSize
		if start >= len(pending) {
			break
		}
		chunk := pending[start:min(start+chunkSize, len(pending))]
		wg.Go(func() {
			if err := s.request(ctx, url, exp, chunk); err != nil {
				log.Printf("remote image signature failed: %v", err)
			}
		})
	}
	wg.Wait()
}

func (s *ImageSigner) request(ctx context.Context, url string, exp int64, targets []imageSignatureTarget) error {
	body, err := json.Marshal(imageSignaturesRequest{Exp: exp, Targets: targets})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/internal/image-signatures", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d from %s", res.StatusCode, url)
	}
	var response imageSignaturesResponse
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return err
	}
	if len(response.Signatures) != len(targets) {
		return fmt.Errorf("got %d signatures for %d targets from %s", len(response.Signatures), len(targets), url)
	}
	for i, target := range targets {
		storeImageSignature(target.Key, target.ImageID, exp, response.Signatures[i])
	}
	return nil
}

// asia1 から署名の計算を受ける内部 API。/internal/ に置き、nginx からは届かない。
func postInternalImageSignaturesHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input imageSignaturesRequest
		if err := json.NewDecoder(c.Request().Body).Decode(&input); err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_request")
		}
		response := imageSignaturesResponse{Signatures: make([][]byte, len(input.Targets))}
		for i, target := range input.Targets {
			if len(target.Key) == 0 {
				return NewAPIError(http.StatusBadRequest, "invalid_request")
			}
			response.Signatures[i] = issueImageSignature(target.Key, target.ImageID, input.Exp)
		}
		return c.JSON(http.StatusOK, response)
	}
}
