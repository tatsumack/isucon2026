package isureal

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"
)

func deleteAccountHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var lockedUser struct {
			ID     string `db:"id"`
			Region string `db:"region"`
		}
		err = tx.GetContext(ctx, &lockedUser, `SELECT id, region FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE`, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "account_not_found")
		}
		if err != nil {
			return err
		}
		// 投稿期間開始時の通知対象と、退会の前後関係を確定する。
		if err := lockPostingWindowRegionsShared(ctx, tx, lockedUser.Region); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET status='deleted', deleted_at=clock_timestamp(), updated_at=clock_timestamp() WHERE id=$1`, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE login_sessions SET active=false, invalidated_at=clock_timestamp(), reason='account_deletion' WHERE user_id=$1 AND active=true`, userID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
