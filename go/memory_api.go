package isureal

import (
	"context"
	"net/http"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type getMemoriesRequest struct {
	Month          string     `query:"month"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type memoryPostResponse struct {
	createPostResponse
	RealMojis []replaceRealMojiResponse `json:"realmojis"`
	Comments  []createCommentResponse   `json:"comments"`
}

type getMemoriesResponse struct {
	Items   []memoryPostResponse `json:"items"`
	HasMore bool                 `json:"hasMore"`
}

type getMemoriesByDateRequest struct {
	Date           string     `param:"date"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type getMemoriesByDateResponse getMemoriesResponse

func getMemoriesHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getMemoriesRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		start, err := time.Parse("2006-01", input.Month)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_memory_period")
		}
		response, err := listMemories(c.Request().Context(), h.DB, h.Signer, GetSession(c).UserID, start, start.AddDate(0, 1, 0), input.BeforePostedAt, input.BeforeID)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, response)
	}
}

func getMemoriesByDateHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getMemoriesByDateRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		start, err := time.Parse(time.DateOnly, input.Date)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_memory_period")
		}
		response, err := listMemories(c.Request().Context(), h.DB, h.Signer, GetSession(c).UserID, start, start.AddDate(0, 0, 1), input.BeforePostedAt, input.BeforeID)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, getMemoriesByDateResponse(response))
	}
}

func listMemories(ctx context.Context, db sqlx.QueryerContext, signer *ImageSigner, userID string, start, end time.Time, beforeAt *time.Time, beforeID string) (getMemoriesResponse, error) {
	var response getMemoriesResponse
	if (beforeAt == nil) != (beforeID == "") {
		return response, NewAPIError(http.StatusBadRequest, "invalid_pagination")
	}
	posts, err := selectPostsWithAuthor(ctx, db, `
  AND p.author_id = $1 AND w.local_date >= $2 AND w.local_date < $3
  AND ($4::timestamptz IS NULL OR (p.posted_at, p.id) < ($4, $5))
  ORDER BY p.posted_at DESC, p.id DESC LIMIT 51
 `, userID, start, end, beforeAt, beforeID)
	if err != nil {
		return response, err
	}
	response.HasMore = len(posts) > 50
	if response.HasMore {
		posts = posts[:50]
	}
	reactions, comments, err := selectMemoryInteractions(ctx, db, posts)
	if err != nil {
		return response, err
	}

	// 応答で返す署名を、組み立てる前にまとめて依頼する (image_signature_remote.go)。件数の上限は下の組み立てと同じ
	var batch imageSignatureBatch
	for _, post := range posts {
		batch.add(post.Author, post.ImageID)
		batch.addProfileImage(post.Author)
	}
	reactionCounts := make(map[string]int, len(posts))
	for _, reaction := range reactions {
		if reactionCounts[reaction.PostID] < 50 {
			reactionCounts[reaction.PostID]++
			batch.addProfileImage(reaction.Reactor)
			batch.add(reaction.Reactor, reaction.ImageID)
		}
	}
	commentCounts := make(map[string]int, len(posts))
	for _, comment := range comments {
		if commentCounts[comment.PostID] < 50 {
			commentCounts[comment.PostID]++
			batch.addProfileImage(comment.Author)
		}
	}
	signer.Prime(ctx, &batch)

	items := make([]memoryPostResponse, 0, len(posts))
	itemByPostID := make(map[string]*memoryPostResponse, len(posts))
	for _, post := range posts {
		items = append(items, memoryPostResponse{
			createPostResponse: newPostResponse(post.Post, post.Author),
			RealMojis:          make([]replaceRealMojiResponse, 0),
			Comments:           make([]createCommentResponse, 0),
		})
	}
	for i := range items {
		itemByPostID[items[i].ID] = &items[i]
	}
	for _, reaction := range reactions {
		item := itemByPostID[reaction.PostID]
		if len(item.RealMojis) < 50 {
			item.RealMojis = append(item.RealMojis, newRealMojiResponse(reaction.RealMoji, reaction.Reactor))
		}
	}
	for _, comment := range comments {
		item := itemByPostID[comment.PostID]
		if len(item.Comments) < 50 {
			item.Comments = append(item.Comments, newCommentResponse(comment.Comment, comment.Author))
		}
	}
	response.Items = items
	return response, nil
}

func selectMemoryInteractions(ctx context.Context, db sqlx.QueryerContext, posts []PostWithAuthor) ([]RealMojiWithReactor, []CommentWithAuthor, error) {
	if len(posts) == 0 {
		return nil, nil, nil
	}
	postIDs := make([]string, len(posts))
	for i := range posts {
		postIDs[i] = posts[i].ID
	}

	query, args, err := sqlx.In(`AND r.post_id IN (?) ORDER BY r.created_at DESC, r.id DESC`, postIDs)
	if err != nil {
		return nil, nil, err
	}
	reactions, err := selectCurrentRealMojis(ctx, db, sqlx.Rebind(sqlx.DOLLAR, query), args...)
	if err != nil {
		return nil, nil, err
	}

	query, args, err = sqlx.In(`AND c.post_id IN (?) ORDER BY c.created_at, c.id`, postIDs)
	if err != nil {
		return nil, nil, err
	}
	comments, err := selectCommentsWithAuthor(ctx, db, sqlx.Rebind(sqlx.DOLLAR, query), args...)
	if err != nil {
		return nil, nil, err
	}
	return reactions, comments, nil
}
