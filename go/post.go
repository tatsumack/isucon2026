package isureal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type PostLocation struct {
	Kind      string   `json:"kind" db:"location_kind"`
	Latitude  *float64 `json:"latitude,omitempty" db:"latitude"`
	Longitude *float64 `json:"longitude,omitempty" db:"longitude"`
	Locality  *string  `json:"locality,omitempty" db:"locality"`
}

func (location PostLocation) valid() bool {
	switch location.Kind {
	case "none":
		return location.Latitude == nil && location.Longitude == nil && location.Locality == nil
	case "exact":
		return location.Latitude != nil && location.Longitude != nil && location.Locality == nil &&
			*location.Latitude >= -90 && *location.Latitude <= 90 && *location.Longitude >= -180 && *location.Longitude <= 180
	case "approximate":
		return location.Latitude == nil && location.Longitude == nil && location.Locality != nil && validUserText(*location.Locality, 1, 100)
	default:
		return false
	}
}

type Post struct {
	ID              string `db:"id"`
	AuthorID        string `db:"author_id"`
	PostingWindowID string `db:"posting_window_id"`
	ImageID         string `db:"image_id"`
	Caption         string `db:"caption"`
	Visibility      string `db:"visibility"`
	PostLocation
	PostedAt       time.Time  `db:"posted_at"`
	OnTimeDeadline time.Time  `db:"on_time_deadline"`
	WindowEndedAt  *time.Time `db:"window_ended_at"`
}

const postColumns = `p.id, p.author_id, p.posting_window_id, p.image_id, p.caption, p.visibility,
  p.location_kind, p.latitude, p.longitude, p.locality, p.posted_at,
  w.on_time_deadline, w.ended_at AS window_ended_at`

func getPost(ctx context.Context, db sqlx.QueryerContext, postID string) (Post, error) {
	var post Post
	err := sqlx.GetContext(ctx, db, &post, `
  SELECT `+postColumns+`
  FROM posts p JOIN posting_windows w ON w.id = p.posting_window_id
  WHERE p.id = $1 AND p.deleted_at IS NULL
 `, postID)
	return post, err
}

type PostWithAuthor struct {
	Post
	Author User `db:"author"`
}

func selectPostsWithAuthor(ctx context.Context, db sqlx.QueryerContext, conditions string, args ...any) ([]PostWithAuthor, error) {
	var posts []PostWithAuthor
	if err := sqlx.SelectContext(ctx, db, &posts, `
  SELECT `+postColumns+`, `+userColumnsAs("u", "author")+`
  FROM posts p
  JOIN posting_windows w ON w.id = p.posting_window_id
  JOIN users u ON u.id = p.author_id AND u.status = 'active'
  WHERE p.deleted_at IS NULL `+conditions, args...); err != nil {
		return nil, err
	}
	for i := range posts {
		if err := posts[i].Author.setImageKey(); err != nil {
			return nil, err
		}
	}
	return posts, nil
}

func getPostWithAuthor(ctx context.Context, db sqlx.QueryerContext, postID string) (PostWithAuthor, error) {
	posts, err := selectPostsWithAuthor(ctx, db, `AND p.id = $1`, postID)
	if err != nil {
		return PostWithAuthor{}, err
	}
	if len(posts) == 0 {
		return PostWithAuthor{}, sql.ErrNoRows
	}
	return posts[0], nil
}

type viewerContext struct {
	User
	HasPostedToday   bool
	Friends          map[string]bool
	FriendsOfFriends map[string]bool
	MutualFriends    map[string]int
	Blocked          map[string]bool
}

// 本人・当日投稿済みか・ブロック・友達・友達の友達を1回のクエリで取得する。
// europe / america からは DB への往復1回ごとに約100ms かかるため、以前の5往復を1往復にまとめている。
func loadViewerContext(ctx context.Context, db sqlx.QueryerContext, viewerID string) (viewerContext, error) {
	return loadViewerContextWithRelations(ctx, db, viewerID, true, true)
}

// friends/public feeds do not need a two-hop graph. Other callers retain the
// complete context for visibility checks and recommendation scoring.
func loadViewerContextWithRelations(ctx context.Context, db sqlx.QueryerContext, viewerID string, needFriends, needFriendsOfFriends bool) (viewerContext, error) {
	viewer := viewerContext{Friends: map[string]bool{}, FriendsOfFriends: map[string]bool{}, MutualFriends: map[string]int{}}
	var row struct {
		User
		HasPostedToday bool   `db:"has_posted_today"`
		BlockedIDs     []byte `db:"blocked_ids"`
		FriendIDs      []byte `db:"friend_ids"`
		// 友達の友達の候補。友達1人を介するごとに1要素 (同じIDが複数回出る)。
		FriendOfFriendIDs []byte `db:"friend_of_friend_ids"`
	}
	err := sqlx.GetContext(ctx, db, &row, `
  WITH me AS (
    SELECT `+strings.Join(userColumns, ", ")+` FROM users WHERE id = $1 AND status = 'active'
  ), friends AS (
    SELECT CASE WHEN user_low_id = $1 THEN user_high_id ELSE user_low_id END AS id
    FROM friendships WHERE user_low_id = $1 OR user_high_id = $1
  )
  SELECT me.*,
    EXISTS(
      SELECT 1 FROM posts p
      JOIN posting_windows posted ON posted.id = p.posting_window_id
      JOIN posting_windows current ON current.region = me.region AND current.ended_at IS NULL
                                  AND current.local_date = posted.local_date
      WHERE p.author_id = me.id AND p.deleted_at IS NULL
    ) AS has_posted_today,
    COALESCE((
      SELECT json_agg(CASE WHEN blocker_id = $1 THEN blocked_id ELSE blocker_id END)
      FROM block_relations WHERE blocker_id = $1 OR blocked_id = $1
    ), '[]') AS blocked_ids,
    CASE WHEN $2 THEN COALESCE((SELECT json_agg(id) FROM friends), '[]') ELSE '[]'::json END AS friend_ids,
    CASE WHEN $3 THEN COALESCE((
      SELECT json_agg(other_id) FROM (
        SELECT fr.user_high_id AS other_id FROM friendships fr WHERE fr.user_low_id IN (SELECT id FROM friends)
        UNION ALL
        SELECT fr.user_low_id FROM friendships fr WHERE fr.user_high_id IN (SELECT id FROM friends)
      ) others
    ), '[]') ELSE '[]'::json END AS friend_of_friend_ids
  FROM me
 `, viewerID, needFriends, needFriendsOfFriends)
	if err != nil {
		return viewer, err
	}
	viewer.User = row.User
	if err := viewer.User.setImageKey(); err != nil {
		return viewer, err
	}
	viewer.HasPostedToday = row.HasPostedToday
	var blockedIDs, friendIDs, friendOfFriendIDs []string
	for _, field := range []struct {
		raw []byte
		ids *[]string
	}{{row.BlockedIDs, &blockedIDs}, {row.FriendIDs, &friendIDs}, {row.FriendOfFriendIDs, &friendOfFriendIDs}} {
		if err := json.Unmarshal(field.raw, field.ids); err != nil {
			return viewer, err
		}
	}
	viewer.Blocked = make(map[string]bool, len(blockedIDs))
	for _, id := range blockedIDs {
		viewer.Blocked[id] = true
	}
	for _, id := range friendIDs {
		viewer.Friends[id] = true
	}
	for _, id := range friendOfFriendIDs {
		viewer.FriendsOfFriends[id] = true
		if id != viewer.ID {
			viewer.MutualFriends[id]++
		}
	}
	return viewer, nil
}

func (viewer viewerContext) canView(post Post) bool {
	if post.AuthorID == viewer.ID {
		return true
	}
	if post.WindowEndedAt != nil || !viewer.HasPostedToday || viewer.Blocked[post.AuthorID] {
		return false
	}
	switch post.Visibility {
	case "public":
		return true
	case "friends":
		return viewer.Friends[post.AuthorID]
	case "friends_of_friends":
		return viewer.Friends[post.AuthorID] || viewer.FriendsOfFriends[post.AuthorID]
	default:
		return false
	}
}

// 投稿を閲覧できるかを返す。呼び出し元で本人の情報を使えるよう、読み込んだ viewer も返す (許可されないときは使わないこと)。
func canViewPost(ctx context.Context, db sqlx.QueryerContext, viewerID, postID string) (bool, viewerContext, error) {
	post, err := getPostWithAuthor(ctx, db, postID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, viewerContext{}, nil
	}
	if err != nil {
		return false, viewerContext{}, err
	}
	viewer, err := loadViewerContext(ctx, db, viewerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, viewer, nil
	}
	if err != nil {
		return false, viewer, err
	}
	return viewer.canView(post.Post), viewer, nil
}

func getFriendIDs(ctx context.Context, db sqlx.QueryerContext, userID string) ([]string, error) {
	var friendships []struct {
		UserLowID  string `db:"user_low_id"`
		UserHighID string `db:"user_high_id"`
	}
	if err := sqlx.SelectContext(ctx, db, &friendships, `SELECT user_low_id, user_high_id FROM friendships WHERE user_low_id = $1 OR user_high_id = $1`, userID); err != nil {
		return nil, err
	}
	var ids []string
	for _, friendship := range friendships {
		if friendship.UserLowID == userID {
			ids = append(ids, friendship.UserHighID)
		} else {
			ids = append(ids, friendship.UserLowID)
		}
	}
	return ids, nil
}

func getBlockedUserIDs(ctx context.Context, db sqlx.QueryerContext, userID string) (map[string]bool, error) {
	var ids []string
	if err := sqlx.SelectContext(ctx, db, &ids, `
  SELECT CASE WHEN blocker_id = $1 THEN blocked_id ELSE blocker_id END
  FROM block_relations WHERE blocker_id = $1 OR blocked_id = $1
 `, userID); err != nil {
		return nil, err
	}
	blocked := make(map[string]bool, len(ids))
	for _, id := range ids {
		blocked[id] = true
	}
	return blocked, nil
}
