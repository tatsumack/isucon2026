package isureal

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type deleteFriendshipRequest struct {
	UserID string `param:"userId" json:"-"`
}

func deleteFriendshipHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deleteFriendshipRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		tx, err := beginRelationshipTx(ctx, h.DB, actor, input.UserID)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `DELETE FROM friendships WHERE user_low_id=$1 AND user_high_id=$2`, min(actor, input.UserID), max(actor, input.UserID))
		if err := requireRelationshipChange(result, err, http.StatusConflict, "relationship_conflict"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

type followRequest struct {
	UserID string `param:"userId" json:"-"`
}

func followHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input followRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		tx, err := beginRelationshipTx(ctx, h.DB, actor, input.UserID)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// 認定公開アカウントか・ブロック関係・既にフォロー済みかを1回の往復で確認する。
		var target struct {
			AccountType string `db:"account_type"`
			Blocked     bool   `db:"blocked"`
			Following   bool   `db:"following"`
		}
		if err := tx.GetContext(ctx, &target, `
   SELECT u.account_type,
     EXISTS(SELECT 1 FROM block_relations WHERE (blocker_id=$1 AND blocked_id=u.id) OR (blocker_id=u.id AND blocked_id=$1)) AS blocked,
     EXISTS(SELECT 1 FROM follows WHERE follower_id=$1 AND followee_id=u.id) AS following
   FROM users u WHERE u.id=$2 AND u.status='active'
  `, actor, input.UserID); err != nil {
			return err
		}
		if target.AccountType != "certified_public" || target.Blocked || target.Following {
			return NewAPIError(http.StatusConflict, "relationship_conflict")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO follows (id, follower_id, followee_id) VALUES ($1,$2,$3)`, uuid.NewString(), actor, input.UserID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

type unfollowRequest struct {
	UserID string `param:"userId" json:"-"`
}

func unfollowHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input unfollowRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		tx, err := beginRelationshipTx(ctx, h.DB, actor, input.UserID)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `DELETE FROM follows WHERE follower_id=$1 AND followee_id=$2`, actor, input.UserID)
		if err := requireRelationshipChange(result, err, http.StatusConflict, "relationship_conflict"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

type blockRequest struct {
	UserID string `param:"userId" json:"-"`
}

func blockHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input blockRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		tx, err := beginRelationshipTx(ctx, h.DB, actor, input.UserID)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// ブロックの登録と、友達・フォロー・保留中の友達申請の解除を1回の往復で行う。
		// 既にブロック済みなら何も登録されないので 409 を返し、ロールバックで解除も取り消す。
		var inserted int
		if err := tx.GetContext(ctx, &inserted, `
   WITH blocked AS (
     INSERT INTO block_relations (id, blocker_id, blocked_id) VALUES ($1,$2,$3)
     ON CONFLICT (blocker_id, blocked_id) DO NOTHING
     RETURNING id
   ), unfriend AS (
     DELETE FROM friendships WHERE user_low_id=$4 AND user_high_id=$5
   ), unfollow AS (
     DELETE FROM follows WHERE (follower_id=$2 AND followee_id=$3) OR (follower_id=$3 AND followee_id=$2)
   ), cancel AS (
     UPDATE friend_requests SET status='cancelled', updated_at=clock_timestamp()
     WHERE status='pending' AND ((requester_id=$2 AND recipient_id=$3) OR (requester_id=$3 AND recipient_id=$2))
   )
   SELECT count(*) FROM blocked
  `, uuid.NewString(), actor, input.UserID, min(actor, input.UserID), max(actor, input.UserID)); err != nil {
			return err
		}
		if inserted == 0 {
			return NewAPIError(http.StatusConflict, "relationship_conflict")
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

type unblockRequest struct {
	UserID string `param:"userId" json:"-"`
}

func unblockHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input unblockRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		tx, err := beginRelationshipTx(ctx, h.DB, actor, input.UserID)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `DELETE FROM block_relations WHERE blocker_id=$1 AND blocked_id=$2`, actor, input.UserID)
		if err := requireRelationshipChange(result, err, http.StatusForbidden, "relationship_action_forbidden"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
