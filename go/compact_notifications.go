package isureal

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

func createWindowNotifications(ctx context.Context, tx *sqlx.Tx, compact bool, region, windowID string, startsAt time.Time) error {
	if compact {
		// The caller holds the region's exclusive membership lock until commit.
		// Object keys preserve arbitrary existing user IDs exactly, without UUID casts.
		_, err := tx.ExecContext(ctx, `INSERT INTO notification_recipient_snapshots (posting_window_id, recipients)
   SELECT $2, COALESCE(jsonb_object_agg(id, true), '{}'::jsonb)
   FROM users WHERE region = $1 AND status = 'active'`, region, windowID)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO notifications (id, user_id, posting_window_id, created_at)
   SELECT gen_random_uuid()::text, id, $2, $3 FROM users WHERE region = $1 AND status = 'active'`, region, windowID, startsAt)
	return err
}
