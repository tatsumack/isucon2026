package isureal

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type openPostingWindowRequest struct {
	Region        string `json:"region"`
	LocalDate     string `json:"localDate"`
	OnTimeSeconds *int64 `json:"onTimeSeconds"`
}

type openPostingWindowResponse struct {
	ID             string    `json:"id"`
	Region         string    `json:"region"`
	LocalDate      string    `json:"localDate"`
	StartsAt       time.Time `json:"startsAt"`
	OnTimeDeadline time.Time `json:"onTimeDeadline"`
}

func openPostingWindowHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input openPostingWindowRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		region := input.Region
		localDate, err := time.Parse(time.DateOnly, input.LocalDate)
		if !validRegion(region) || err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_posting_window")
		}
		seconds := int64(24 * 60 * 60)
		if input.OnTimeSeconds != nil {
			seconds = *input.OnTimeSeconds
		}
		ctx := c.Request().Context()
		tx, err := h.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// 登録・リージョン変更・退会と直列化し、通知対象のactiveユーザーを確定する。
		if err := lockPostingWindowRegionExclusive(ctx, tx, region); err != nil {
			return err
		}
		var conflict bool
		if err := tx.GetContext(ctx, &conflict, `SELECT EXISTS(SELECT 1 FROM posting_windows WHERE region = $1 AND local_date >= $2)`, region, localDate); err != nil {
			return err
		}
		if conflict {
			return NewAPIError(http.StatusConflict, "posting_window_conflict")
		}
		var startsAt time.Time
		if err := tx.GetContext(ctx, &startsAt, `SELECT clock_timestamp()`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE posting_windows SET ended_at = $2 WHERE region = $1 AND ended_at IS NULL`, region, startsAt); err != nil {
			return err
		}
		// 排他lock取得後の別statementで世代を読む。legacyへ切戻しても世代を進める。
		var epoch int64
		if err := tx.GetContext(ctx, &epoch, `SELECT current_epoch FROM region_state WHERE region = $1`, region); err != nil {
			return err
		}
		var cutEpoch *int64
		if h.Config.EventOnlyNotifications {
			var ready bool
			if err := tx.GetContext(ctx, &ready, `SELECT ready FROM notification_history_state WHERE id = true`); err != nil {
				return err
			}
			if !ready {
				return NewAPIError(http.StatusServiceUnavailable, "notification_history_not_initialized")
			}
			cutEpoch = &epoch
		}
		windowID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
   INSERT INTO posting_windows (id, region, local_date, starts_at, on_time_deadline, cut_epoch)
   VALUES ($1, $2, $3, $4, $5, $6)
  `, windowID, region, localDate, startsAt, startsAt.Add(time.Duration(seconds)*time.Second), cutEpoch); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE region_state SET current_epoch = current_epoch + 1 WHERE region = $1`, region); err != nil {
			return err
		}
		// flagは新windowの書込方式だけを選ぶ。reader/history追跡は常に両方式に対応。
		// event方式ではrecipientの走査も通知の人数分INSERTも実行しない。
		if !h.Config.EventOnlyNotifications {
			if _, err := tx.ExecContext(ctx, `
   INSERT INTO notifications (id, user_id, posting_window_id, created_at)
   SELECT $4 || substr(gen_random_uuid()::text, 16), id, $2, $3 FROM users WHERE region = $1 AND status = 'active'
  `, region, windowID, startsAt, notificationIDPrefix(startsAt)); err != nil {
				return err
			}
		}
		window, err := getPostingWindow(ctx, tx, windowID)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, openPostingWindowResponse{
			ID: window.ID, Region: window.Region, LocalDate: window.LocalDate.Format(time.DateOnly),
			StartsAt: window.StartsAt, OnTimeDeadline: window.OnTimeDeadline,
		})
	}
}

// notificationIDPrefix は、UUID v7 の先頭15文字 (48ビットのミリ秒時刻とバージョンの7) を返す。
func notificationIDPrefix(t time.Time) string {
	ms := uint64(t.UnixMilli())
	return fmt.Sprintf("%08x-%04x-7", ms>>16, ms&0xffff)
}
