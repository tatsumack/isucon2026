package isureal

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"
)

type exportPostResponse struct {
	createPostResponse
	ImagePath string `json:"imagePath"`
}

func exportPostsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		author, err := getUser(ctx, h.DB, userID)
		if err != nil {
			return err
		}
		// 画像本体は image_objects ではなくファイルから読む (image_store.go)。
		// image_objects との JOIN はしない。image_objects は created_at で約36個に分かれていて、id だけの JOIN は投稿1件ごとに全パーティションを引く
		// (ベンチ #26166 で1回 109ms、DB 時間の13.3%)。JOIN で落ちる投稿は無い:
		//   - 投稿の作成 (post_api.go) は、同じトランザクションで image_objects に行を入れる
		//   - image_objects の行を消すのはプロフィール画像の変更・削除だけで、投稿の画像とは id が別 (初期データで重なりは0件)
		//   - 初期データで、画像の行が無い投稿は0件 (2026-10-31 に asia2 で確認)
		// 初期データの画像かどうかは、画像の行の代わりに投稿の created_at で決める。投稿と画像の行は同じときに作られる。
		// 外れても writeExportImage が両方のディレクトリを探し直すので、結果は変わらない (遅くなるだけ)
		var posts []struct {
			Post
			InitialImage bool `db:"initial_image"`
		}
		if err := h.DB.SelectContext(ctx, &posts, `
   SELECT `+postColumns+`, p.created_at <= (SELECT created_at FROM initial_data_boundary) AS initial_image
   FROM posts p
   JOIN posting_windows w ON w.id = p.posting_window_id
   WHERE p.author_id = $1 AND p.deleted_at IS NULL
   ORDER BY p.posted_at, p.id
  `, userID); err != nil {
			return err
		}
		// 応答で返す署名を、組み立てる前にまとめて依頼する (image_signature_remote.go)
		var batch imageSignatureBatch
		batch.addProfileImage(author)
		for _, post := range posts {
			batch.add(author, post.ImageID)
		}
		h.Signer.Prime(ctx, &batch)

		var body bytes.Buffer
		writer := zip.NewWriter(&body)
		defer writer.Close()
		items := make([]exportPostResponse, 0, len(posts))
		for _, post := range posts {
			imagePath := fmt.Sprintf("images/%06d.jpg", len(items)+1)
			image, err := writer.CreateHeader(&zip.FileHeader{Name: imagePath, Method: zip.Store})
			if err != nil {
				return err
			}
			// エクスポートは過去の画像をまとめて読むため、画像取得用のメモリのキャッシュに入れない (あれば使う)
			if err := h.Images.writeExportImage(ctx, image, post.ImageID, post.InitialImage); err != nil {
				return err
			}
			items = append(items, exportPostResponse{createPostResponse: newPostResponse(post.Post, author), ImagePath: imagePath})
		}
		manifest, err := writer.Create("posts.json")
		if err != nil {
			return err
		}
		if err := json.NewEncoder(manifest).Encode(items); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="isureal-posts.zip"`)
		c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
		return c.Blob(http.StatusOK, "application/zip", body.Bytes())
	}
}
