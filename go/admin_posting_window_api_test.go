package isureal

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// SQL の `$4 || substr(gen_random_uuid()::text, 16)` と同じ組み立てで、UUID v7 になることを確かめる。
func TestNotificationIDPrefixMakesUUIDv7(t *testing.T) {
	startsAt := time.Date(2026, 10, 31, 3, 47, 20, 730_719_000, time.UTC)
	prev := ""
	for i := range 1000 {
		at := startsAt.Add(time.Duration(i) * 7 * time.Millisecond)
		id := notificationIDPrefix(at) + uuid.NewString()[15:]
		parsed, err := uuid.Parse(id)
		if err != nil {
			t.Fatalf("uuid.Parse(%q): %v", id, err)
		}
		if parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
			t.Fatalf("%q: version %d, variant %v", id, parsed.Version(), parsed.Variant())
		}
		if sec, nsec := parsed.Time().UnixTime(); time.Unix(sec, nsec).UnixMilli() != at.UnixMilli() {
			t.Fatalf("%q: time %v, want %v", id, time.Unix(sec, nsec).UTC(), at)
		}
		if len(id) != 36 || id <= prev {
			t.Fatalf("%q is not after %q", id, prev)
		}
		prev = id
	}
}

func TestOpenPostingWindowEventAndLegacyModes(t *testing.T) {
	for _, eventOnly := range []bool{true, false} {
		t.Run(map[bool]string{true: "event", false: "legacy"}[eventOnly], func(t *testing.T) {
			at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
			steps := []membershipSQLStep{
				{sql: "SELECT pg_advisory_xact_lock(", args: []any{"asia"}},
				{sql: "SELECT EXISTS", columns: []string{"exists"}, values: [][]driver.Value{{false}}},
				{sql: "SELECT clock_timestamp()", columns: []string{"clock_timestamp"}, values: [][]driver.Value{{at}}},
				{sql: "UPDATE posting_windows SET ended_at", args: []any{"asia", at}},
				membershipEpoch("asia", 12),
			}
			if eventOnly {
				steps = append(steps, membershipSQLStep{sql: "SELECT ready", columns: []string{"ready"}, values: [][]driver.Value{{true}}})
			}
			steps = append(steps, membershipSQLStep{sql: "INSERT INTO posting_windows", check: func(args []driver.NamedValue) {
				if len(args) != 6 {
					t.Fatalf("args=%v", args)
				}
				if eventOnly && args[5].Value != int64(12) {
					t.Fatalf("cut=%v", args[5].Value)
				}
				if !eventOnly && args[5].Value != nil {
					t.Fatalf("legacy cut=%v", args[5].Value)
				}
			}}, membershipSQLStep{sql: "UPDATE region_state SET current_epoch = current_epoch + 1", args: []any{"asia"}})
			if !eventOnly {
				steps = append(steps, membershipSQLStep{sql: "INSERT INTO notifications", args: []any{"asia", nil, at, notificationIDPrefix(at)}})
			}
			steps = append(steps, membershipSQLStep{sql: "SELECT id, region, local_date, starts_at", columns: []string{"id", "region", "local_date", "starts_at", "on_time_deadline"}, values: [][]driver.Value{{"window", "asia", at, at, at.Add(time.Hour)}}})
			db, state := newMembershipTestDB(t, steps)
			err := openPostingWindowHandler(&HandlerContext{DB: db, Config: &Config{EventOnlyNotifications: eventOnly}})(membershipContext("POST", `{"region":"asia","localDate":"2026-10-31"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !state.committed || state.rolledBack {
				t.Fatalf("commit=%v rollback=%v", state.committed, state.rolledBack)
			}
		})
	}
}

func TestOpenPostingWindowConflictAndUnreadyRollback(t *testing.T) {
	for _, conflict := range []bool{true, false} {
		t.Run(map[bool]string{true: "conflict", false: "baseline_not_ready"}[conflict], func(t *testing.T) {
			at := time.Now()
			steps := []membershipSQLStep{{sql: "SELECT pg_advisory_xact_lock("}, {sql: "SELECT EXISTS", columns: []string{"exists"}, values: [][]driver.Value{{conflict}}}}
			if !conflict {
				steps = append(steps, membershipSQLStep{sql: "SELECT clock_timestamp()", columns: []string{"clock_timestamp"}, values: [][]driver.Value{{at}}}, membershipSQLStep{sql: "UPDATE posting_windows SET ended_at"}, membershipEpoch("asia", 7), membershipSQLStep{sql: "SELECT ready", columns: []string{"ready"}, values: [][]driver.Value{{false}}})
			}
			db, state := newMembershipTestDB(t, steps)
			err := openPostingWindowHandler(&HandlerContext{DB: db, Config: &Config{EventOnlyNotifications: true}})(membershipContext("POST", `{"region":"asia","localDate":"2026-10-31"}`))
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected APIError, got %v", err)
			}
			if state.committed || !state.rolledBack {
				t.Fatalf("commit=%v rollback=%v", state.committed, state.rolledBack)
			}
		})
	}
}
