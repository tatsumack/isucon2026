package isureal

import (
	"context"
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

// lockPostingWindowRegionExclusive serializes opening posting windows in the
// same region. Notifications are no longer stored as rows; SSE builds them from
// the open window, so the shared locks no longer protect a recipient set.
func lockPostingWindowRegionExclusive(ctx context.Context, tx *sqlx.Tx, region string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('posting_window:' || $1::text))`, region)
	return err
}

func normalizePostingWindowRegions(regions []string) []string {
	regions = slices.Clone(regions)
	slices.Sort(regions)
	return slices.Compact(regions)
}
