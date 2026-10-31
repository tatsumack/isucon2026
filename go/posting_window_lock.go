package isureal

import (
	"context"
	"slices"

	"github.com/jmoiron/sqlx"
)

// lockPostingWindowRegions serializes changes to the set of active users in a
// region with opening a posting window for that region. Sorting is required
// when a user moves between regions so concurrent moves cannot deadlock by
// taking the same pair of advisory locks in opposite orders.
func lockPostingWindowRegions(ctx context.Context, tx *sqlx.Tx, regions ...string) error {
	regions = normalizePostingWindowRegions(regions)
	for _, region := range regions {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('posting_window:' || $1::text))`, region); err != nil {
			return err
		}
	}
	return nil
}

func normalizePostingWindowRegions(regions []string) []string {
	regions = slices.Clone(regions)
	slices.Sort(regions)
	return slices.Compact(regions)
}
