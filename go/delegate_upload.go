package isureal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// europe1 / america1 は、投稿と RealMoji の本文を asia1 へ転送する。そのとき画像のコピーを手元に残し、
// asia1 が作った画像 ID で imageDataCache に入れる。同じリージョンの友達がその画像を見るとき、asia1 から取り寄せずに返せる。
// #24003 では、europe で投稿された画像の europe での取得 1,025件のうち 570件が 0.5秒以上かかっていた (asia1 からの取り寄せ)。
//
// 入れるのは asia1 が 201 を返したときだけ。asia1 は本文の最初の image のファイルをそのまま保存するので (post_api.go の
// bindCreatePostRequest、realmoji_api.go の Bind)、同じ部分を取り出せば中身は同じになる。
// 配信してよいかは今までどおり画像の取得のたびに DB で確認するので、キャッシュに入れても閲覧制限は変わらない。
// 画像 ID は asia1 が採番する UUID で、同じ ID の中身が変わることはないため無効化しない (imageDataCache と同じ)。

var uploadRoutes = map[string]bool{
	"POST /api/posts":                   true,
	"POST /api/posts/:postId/realmojis": true,
}

// 取り出す本文の大きさの上限。nginx の client_max_body_size (32m) と揃える
const maxCapturedUploadSize = 32 << 20

type capturedUploadKey struct{}

// withCapturedUpload は、アップロードの本文から画像を取り出して context に入れる。
// 本文は読んだ分を戻すので、転送する内容は変わらない。取り出せなければ何もしない。
func withCapturedUpload(r *http.Request) *http.Request {
	if r.Body == nil || r.ContentLength > maxCapturedUploadSize {
		return r
	}
	size := int64(bytes.MinRead)
	if r.ContentLength > 0 {
		size += r.ContentLength
	}
	buf := bytes.NewBuffer(make([]byte, 0, size))
	_, err := buf.ReadFrom(io.LimitReader(r.Body, maxCapturedUploadSize+1))
	body := buf.Bytes()
	// 読み切れなかった残りがあっても、読んだ分の後ろにつないで、そのまま転送する
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
	if err != nil || len(body) > maxCapturedUploadSize {
		return r
	}
	image, ok := uploadedImage(body, r.Header.Get("Content-Type"))
	if !ok {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), capturedUploadKey{}, image))
}

// uploadedImage は、multipart の本文から、最初の image という名前のファイルを返す。
func uploadedImage(body []byte, contentType string) ([]byte, bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, false
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			return nil, false
		}
		if part.FormName() == "image" && part.FileName() != "" {
			image, err := io.ReadAll(part)
			if err != nil || len(image) == 0 {
				return nil, false
			}
			return image, true
		}
	}
}

// cacheUploadedImage は ReverseProxy.ModifyResponse から呼ぶ。asia1 が 201 を返したら、応答の imageUrl の画像 ID でキャッシュに入れる。
func cacheUploadedImage(resp *http.Response) error {
	image, ok := resp.Request.Context().Value(capturedUploadKey{}).([]byte)
	if !ok || resp.StatusCode != http.StatusCreated || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return err
	}
	var response struct {
		ImageURL string `json:"imageUrl"`
	}
	if json.Unmarshal(body, &response) != nil {
		return nil
	}
	if id := imageIDFromURL(response.ImageURL); id != "" {
		imageDataCache.put(id, cachedImage{data: image})
	}
	return nil
}

// imageIDFromURL は、signedImageURL が作る /api/images/<id>?exp=...&sig=... から画像 ID を取り出す。
func imageIDFromURL(imageURL string) string {
	u, err := url.Parse(imageURL)
	if err != nil {
		return ""
	}
	id, ok := strings.CutPrefix(u.Path, "/api/images/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}
