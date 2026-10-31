package isureal

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type createPostRequest struct {
	// 最初の image のファイルのパート。無ければ nil。使い終わったら releaseUpload で返す
	Image        *bytes.Buffer
	Caption      string
	Visibility   string
	LocationKind string
	Latitude     *float64
	Longitude    *float64
	Locality     *string
}

// bindCreatePostRequest は multipart のパートを順に読み、画像は使い回しのバッファへ1回だけ読む。
// c.Bind (ParseMultipartForm) は画像をメモリ上のバッファへ読み、さらに Open + io.ReadAll で読み直していた。
// ステータスは c.Bind と同じにする: 本文が空なら値なし、multipart の形が壊れていれば 400、知らない Content-Type は 415。
// フィールドの扱いも c.Bind と同じ: 同じ名前は最初の値を使い、空の数値は 0、ファイル名の無い image はファイルとみなさない。
func bindCreatePostRequest(c *echo.Context, input *createPostRequest) error {
	req := c.Request()
	if req.ContentLength == 0 {
		return nil
	}
	base, _, _ := strings.Cut(req.Header.Get(echo.HeaderContentType), ";")
	switch strings.TrimSpace(base) {
	case echo.MIMEMultipartForm:
	case echo.MIMEApplicationJSON, echo.MIMEApplicationXML, echo.MIMETextXML, echo.MIMEApplicationForm:
		// c.Bind でも image のファイルは入らず、invalid_post になる
		return nil
	default:
		return &echo.HTTPError{Code: http.StatusUnsupportedMediaType}
	}
	reader, err := req.MultipartReader()
	if err != nil {
		return echo.ErrBadRequest.Wrap(err)
	}
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		// ラップされた EOF (境界が1つも無い本文) は壊れた本文として 400 にする (ParseMultipartForm と同じ比べ方)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return echo.ErrBadRequest.Wrap(err)
		}
		name := part.FormName()
		if name == "image" && part.FileName() != "" {
			if input.Image == nil {
				// 本文のほとんどは画像なので、Content-Length の分を先に確保する
				if input.Image, err = readUpload(part, req.ContentLength); err != nil {
					return echo.ErrBadRequest.Wrap(err)
				}
			}
			continue
		}
		if part.FileName() != "" || seen[name] {
			continue
		}
		seen[name] = true
		value, err := io.ReadAll(part)
		if err != nil {
			return echo.ErrBadRequest.Wrap(err)
		}
		switch name {
		case "caption":
			input.Caption = string(value)
		case "visibility":
			input.Visibility = string(value)
		case "location_kind":
			input.LocationKind = string(value)
		case "latitude", "longitude":
			v := 0.0
			if len(value) != 0 {
				if v, err = strconv.ParseFloat(string(value), 64); err != nil {
					return echo.ErrBadRequest.Wrap(err)
				}
			}
			if name == "latitude" {
				input.Latitude = &v
			} else {
				input.Longitude = &v
			}
		case "locality":
			locality := string(value)
			input.Locality = &locality
		}
	}
}

type postAuthorResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type createPostResponse struct {
	ID              string             `json:"id"`
	Author          postAuthorResponse `json:"author"`
	PostingWindowID string             `json:"postingWindowId"`
	ImageURL        string             `json:"imageUrl"`
	Caption         string             `json:"caption"`
	Visibility      string             `json:"visibility"`
	Location        PostLocation       `json:"location"`
	PostedAt        time.Time          `json:"postedAt"`
	OnTime          bool               `json:"onTime"`
	LateSeconds     int64              `json:"lateSeconds"`
}

func createPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createPostRequest
		err := bindCreatePostRequest(c, &input)
		if input.Image != nil {
			defer releaseUpload(input.Image)
		}
		if err != nil {
			return err
		}
		if input.Image == nil || !validUserText(input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		switch input.Visibility {
		case "friends", "friends_of_friends", "public":
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		location := PostLocation{
			Kind: input.LocationKind, Latitude: input.Latitude,
			Longitude: input.Longitude, Locality: input.Locality,
		}
		if !location.valid() {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		imageData := input.Image.Bytes()
		if !validJPEG(imageData, 4096) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}

		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		// 画像本体はファイルに置き、行の data は空にする (image_store.go)。投稿できない場合は行がロールバックで消え、
		// 行が無いファイルは配信されない。ファイルの fsync を待つ間に DB の接続を持たないよう、トランザクションの前に書く。
		imageID := uuid.NewString()
		if err := h.Images.Save(imageID, imageData); err != nil {
			return err
		}
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// 画像の行の保存はロックを取る前に済ませ、ロックを持つ時間を短くする。
		if _, err := tx.ExecContext(ctx, `INSERT INTO image_objects (id, owner_user_id, data, media_type, created_at) VALUES ($1, $2, ''::bytea, 'image/jpeg', $3)`, imageID, userID, time.Now().UTC()); err != nil {
			return err
		}
		var lockedUserID string
		if err := tx.GetContext(ctx, &lockedUserID, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
			return err
		}
		user, err := getUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		var window struct {
			ID        string    `db:"id"`
			LocalDate time.Time `db:"local_date"`
			StartsAt  time.Time `db:"starts_at"`
		}
		// 投稿同士は並行に進め、投稿期間の終了 (UPDATE posting_windows SET ended_at) とだけ排他にする。
		// FOR KEY SHARE では ended_at の更新を止められないため FOR SHARE を使う。
		// ロック待ちの間に期間が終了した場合は行が返らないので、新しい期間を取り直す。
		err = tx.GetContext(ctx, &window, `SELECT id, local_date, starts_at FROM posting_windows WHERE region = $1 AND ended_at IS NULL FOR SHARE`, user.Region)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.GetContext(ctx, &window, `SELECT id, local_date, starts_at FROM posting_windows WHERE region = $1 AND ended_at IS NULL FOR SHARE`, user.Region)
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NewAPIError(http.StatusConflict, "post_slot_unavailable")
			}
			return err
		}
		var counts struct {
			Active  int `db:"active"`
			Deleted int `db:"deleted"`
		}
		if err := tx.GetContext(ctx, &counts, `
   SELECT count(*) FILTER (WHERE deleted_at IS NULL) AS active,
          count(*) FILTER (WHERE deleted_at IS NOT NULL) AS deleted
   FROM posts p JOIN posting_windows w ON w.id = p.posting_window_id
   WHERE p.author_id = $1 AND w.local_date = $2
  `, userID, window.LocalDate); err != nil {
			return err
		}
		postedAt := time.Now().UTC()
		if counts.Active != 0 || counts.Deleted >= 2 || postedAt.Before(window.StartsAt) {
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		}
		postID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
   INSERT INTO posts (id, author_id, posting_window_id, image_id, caption, visibility,
                      location_kind, latitude, longitude, locality, posted_at)
   VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
  `, postID, userID, window.ID, imageID, input.Caption, input.Visibility,
			location.Kind, location.Latitude, location.Longitude, location.Locality, postedAt); err != nil {
			return err
		}
		post, err := getPost(ctx, tx, postID)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, newPostResponse(post, user))
	}
}

func newPostResponse(post Post, user User) createPostResponse {
	response := createPostResponse{
		ID: post.ID,
		Author: postAuthorResponse{
			ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
			ProfileImageURL: newGetMeResponse(user).ProfileImageURL,
			Bio:             user.Bio, AccountType: user.AccountType,
		},
		PostingWindowID: post.PostingWindowID, ImageURL: signedImageURL(user, post.ImageID),
		Caption: post.Caption, Visibility: post.Visibility, Location: post.PostLocation,
		PostedAt: post.PostedAt, OnTime: !post.PostedAt.After(post.OnTimeDeadline),
	}
	if !response.OnTime {
		response.LateSeconds = int64(post.PostedAt.Sub(post.OnTimeDeadline) / time.Second)
	}
	return response
}

type getMyCurrentPostResponse createPostResponse

func getMyCurrentPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		var postID string
		err := h.DB.GetContext(ctx, &postID, `
   SELECT p.id FROM posts p
   JOIN posting_windows w ON w.id = p.posting_window_id
   JOIN users u ON u.id = p.author_id
   JOIN posting_windows current ON current.region = u.region AND current.ended_at IS NULL
                              AND current.local_date = w.local_date
   WHERE p.author_id = $1 AND p.deleted_at IS NULL
  `, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		post, err := getPost(ctx, h.DB, postID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		user, err := getUser(ctx, h.DB, userID)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, getMyCurrentPostResponse(newPostResponse(post, user)))
	}
}

type getPostRequest struct {
	PostID string `param:"postId"`
}

type getPostResponse createPostResponse

func getPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getPostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		post, err := getPostWithAuthor(ctx, h.DB, input.PostID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		viewer, err := loadViewerContext(ctx, h.DB, GetSession(c).UserID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err != nil || !viewer.canView(post.Post) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		return c.JSON(http.StatusOK, getPostResponse(newPostResponse(post.Post, post.Author)))
	}
}

type updatePostRequest struct {
	PostID     string  `param:"postId" json:"-"`
	Caption    *string `json:"caption"`
	Visibility *string `json:"visibility"`
}

type updatePostResponse createPostResponse

func updatePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updatePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Caption == nil && input.Visibility == nil {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Caption != nil && !validUserText(*input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Visibility != nil {
			switch *input.Visibility {
			case "friends", "friends_of_friends", "public":
			default:
				return NewAPIError(http.StatusBadRequest, "invalid_post")
			}
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var lockedUserID string
		if err := tx.GetContext(ctx, &lockedUserID, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
			return err
		}
		post, err := getPost(ctx, tx, input.PostID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		if post.AuthorID != userID {
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		}
		window, err := getPostingWindow(ctx, tx, post.PostingWindowID)
		if err != nil {
			return err
		}
		if window.EndedAt != nil {
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		}
		if input.Caption != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE posts SET caption = $2 WHERE id = $1`, post.ID, *input.Caption); err != nil {
				return err
			}
		}
		if input.Visibility != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE posts SET visibility = $2 WHERE id = $1`, post.ID, *input.Visibility); err != nil {
				return err
			}
		}
		post, err = getPost(ctx, tx, post.ID)
		if err != nil {
			return err
		}
		user, err := getUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, updatePostResponse(newPostResponse(post, user)))
	}
}

type deletePostRequest struct {
	PostID string `param:"postId" json:"-"`
}

func deletePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deletePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var lockedUserID string
		if err := tx.GetContext(ctx, &lockedUserID, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
			return err
		}
		post, err := getPost(ctx, tx, input.PostID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		if post.AuthorID != userID {
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		}
		window, err := getPostingWindow(ctx, tx, post.PostingWindowID)
		if err != nil {
			return err
		}
		var deletions int
		if err := tx.GetContext(ctx, &deletions, `
   SELECT count(*) FROM posts p JOIN posting_windows w ON w.id = p.posting_window_id
   WHERE p.author_id = $1 AND w.local_date = $2 AND p.deleted_at IS NOT NULL
  `, userID, window.LocalDate); err != nil {
			return err
		}
		if deletions >= 2 {
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE posts SET deleted_at = clock_timestamp() WHERE id = $1`, post.ID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
