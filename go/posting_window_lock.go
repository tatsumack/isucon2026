package isureal

import (
	"context"
	"database/sql"
	"slices"

	"github.com/jmoiron/sqlx"
)

// lockPostingWindowRegionsShared lets changes to the set of active users run
// concurrently, while still serializing them with opening a posting window.
// Sorting is required when a user moves between regions so concurrent moves
// cannot deadlock by taking the same pair of advisory locks in opposite orders.
func lockPostingWindowRegionsShared(ctx context.Context, tx *sqlx.Tx, regions ...string) error {
	regions = normalizePostingWindowRegions(regions)
	for _, region := range regions {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('posting_window:' || $1::text))`, region); err != nil {
			return err
		}
	}
	return nil
}

// lockPostingWindowRegionExclusive waits for in-flight user changes and blocks
// new ones until the window and its notification recipients are committed.
func lockPostingWindowRegionExclusive(ctx context.Context, tx *sqlx.Tx, region string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('posting_window:' || $1::text))`, region)
	return err
}

func normalizePostingWindowRegions(regions []string) []string {
	regions = slices.Clone(regions)
	slices.Sort(regions)
	return slices.Compact(regions)
}

// recordMembershipState は、対象 user の row lock（新規登録なら INSERT）と
// region の shared advisory lock を保持する READ COMMITTED transaction で呼ぶ。
// epoch を読む INSERT SELECT は lock 取得と必ず別 statement にする。待機前の snapshot では
// admin が進めた世代を読み逃し、確定済み cut の対象を後から変えてしまう。
// region_state を FOR UPDATE しないので、異なる user の登録を直列化しない。
func recordMembershipState(ctx context.Context, tx *sqlx.Tx, userID, region string, present bool) error {
	// shared lock取得とは別statementなので、待機終了後のfresh snapshotでepochを読む。
	// SELECTとUPSERTを一緒にし、追加のDB往復は1回に留める。
	result, err := tx.ExecContext(ctx, `
  INSERT INTO membership (user_id, region, epoch, present)
  SELECT $1, region, current_epoch, $3 FROM region_state WHERE region = $2
  ON CONFLICT (user_id, region, epoch) DO UPDATE SET present = EXCLUDED.present
 `, userID, region, present)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}
