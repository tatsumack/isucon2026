package isureal

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"
)

type exportPostResponse struct {
	createPostResponse
	ImagePath string `json:"imagePath"`
}

// 画像本体はこの件数ずつDBから取り、ZIPに書いてから次を取る。
// 全画像をメモリに載せず、レスポンスを送っている間もDB接続をつかみ続けない。
const exportImageBatchSize = 20

func exportPostsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		author, err := getUser(ctx, h.DB, userID)
		if err != nil {
			return err
		}
		var posts []Post
		if err := h.DB.SelectContext(ctx, &posts, `
   SELECT `+postColumns+`
   FROM posts p
   JOIN posting_windows w ON w.id = p.posting_window_id
   JOIN image_objects i ON i.id = p.image_id
   WHERE p.author_id = $1 AND p.deleted_at IS NULL
   ORDER BY p.posted_at, p.id
  `, userID); err != nil {
			return err
		}

		// ここから先はレスポンスを送り始めるため、エラーになっても500を返せない (接続を切る)。
		c.Response().Header().Set(echo.HeaderContentType, "application/zip")
		c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="isureal-posts.zip"`)
		c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
		c.Response().WriteHeader(http.StatusOK)
		writer := zip.NewWriter(c.Response())
		defer writer.Close()
		items := make([]exportPostResponse, 0, len(posts))
		for start := 0; start < len(posts); start += exportImageBatchSize {
			batch := posts[start:min(start+exportImageBatchSize, len(posts))]
			imageIDs := make([]string, len(batch))
			for i, post := range batch {
				imageIDs[i] = post.ImageID
			}
			var images []struct {
				ID   string `db:"id"`
				Data []byte `db:"data"`
			}
			if err := h.DB.SelectContext(ctx, &images, `SELECT id, data FROM image_objects WHERE id = ANY($1)`, imageIDs); err != nil {
				return err
			}
			dataByID := make(map[string][]byte, len(images))
			for _, image := range images {
				if _, ok := dataByID[image.ID]; !ok {
					dataByID[image.ID] = image.Data
				}
			}
			for _, post := range batch {
				data, ok := dataByID[post.ImageID]
				if !ok {
					// 一覧を取った後に画像が消えた。JOINで除外されていた場合と同じく含めない。
					continue
				}
				imagePath := fmt.Sprintf("images/%06d.jpg", len(items)+1)
				image, err := writer.CreateHeader(&zip.FileHeader{Name: imagePath, Method: zip.Store})
				if err != nil {
					return err
				}
				if _, err := image.Write(data); err != nil {
					return err
				}
				items = append(items, exportPostResponse{createPostResponse: newPostResponse(post, author), ImagePath: imagePath})
			}
		}
		manifest, err := writer.Create("posts.json")
		if err != nil {
			return err
		}
		if err := json.NewEncoder(manifest).Encode(items); err != nil {
			return err
		}
		return writer.Close()
	}
}
