package isureal

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func TestCompactNotificationsOptIn(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		t.Setenv("ISUREAL_COMPACT_NOTIFICATIONS", value)
		if got := ConfigFromEnv().CompactNotifications; got != (value == "true") {
			t.Fatalf("value %q: enabled=%v", value, got)
		}
	}
}

// Opt in only with a dedicated local test database. Each test uses an isolated
// schema and never invokes initialize or touches the database's public schema.
func compactTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("ISUREAL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ISUREAL_TEST_DATABASE_URL to a dedicated local PostgreSQL test DB")
	}
	db, err := sqlx.Connect("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "compact_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	// Configure every pooled connection through PostgreSQL's startup parameter.
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	scoped, err := sqlx.Connect("pgx", dsn+separator+"search_path="+schema)
	if err != nil {
		db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { scoped.Close(); db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); db.Close() })
	scoped.MustExec(`CREATE TABLE users (id varchar(36) PRIMARY KEY, region text NOT NULL, status text NOT NULL);
 CREATE TABLE posting_windows (id varchar(36) PRIMARY KEY, region text NOT NULL, starts_at timestamptz NOT NULL, ended_at timestamptz);
 CREATE TABLE notifications (id varchar(36) PRIMARY KEY, user_id varchar(36) REFERENCES users(id), posting_window_id varchar(36) REFERENCES posting_windows(id), created_at timestamptz NOT NULL, UNIQUE(user_id,posting_window_id));`)
	migration, err := os.ReadFile("../sql/005-compact-notifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	scoped.MustExec(string(migration))
	return scoped
}

func testCompactWindow(t *testing.T, db *sqlx.DB, id, region string, at time.Time, compact bool) {
	t.Helper()
	tx, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tx.MustExec(`LOCK TABLE notification_recipient_snapshots IN ACCESS SHARE MODE`)
	if err := lockPostingWindowRegionExclusive(context.Background(), tx, region); err != nil {
		t.Fatal(err)
	}
	tx.MustExec(`INSERT INTO posting_windows(id,region,starts_at) VALUES($1,$2,$3)`, id, region, at)
	if err := createWindowNotifications(context.Background(), tx, compact, region, id, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactNotificationsIntegration(t *testing.T) {
	db := compactTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	db.MustExec(`INSERT INTO users VALUES ('not-a-UUID','asia','active'),('deleted','asia','deleted'),('other','europe','active')`)
	testCompactWindow(t, db, "window-a", "asia", at, true)
	var count int
	db.Get(&count, `SELECT count(*) FROM notifications`)
	if count != 0 {
		t.Fatal("eager notifications generated")
	}
	// No subscriber existed when the complete set was captured.
	db.MustExec(`UPDATE users SET region='europe' WHERE id='not-a-UUID'`)
	testCompactWindow(t, db, "window-e", "europe", at, true)
	db.MustExec(`UPDATE users SET region='asia' WHERE id='not-a-UUID'; INSERT INTO users VALUES ('late','asia','active')`)
	subscriptions := map[int64]*notificationSubscription{
		1: {id: 1, userID: "not-a-UUID", region: "asia", connectedAt: at.Add(-time.Second)},
		2: {id: 2, userID: "late", region: "asia", connectedAt: at.Add(-time.Second)},
		3: {id: 3, userID: "not-a-UUID", region: "asia", connectedAt: at},
		4: {id: 4, userID: "deleted", region: "asia", connectedAt: at.Add(-time.Second)},
	}
	hub := &NotificationHub{db: db}
	rows, err := hub.fetch(ctx, subscriptions)
	if err != nil {
		t.Fatal(err)
	}
	var firstID string
	for _, row := range rows {
		if row.SubscriptionID == 1 {
			if row.ID == nil {
				t.Fatal("missing recipient")
			}
			firstID = *row.ID
		} else if row.ID != nil {
			t.Fatalf("unexpected notification for subscription %d", row.SubscriptionID)
		}
	}
	var expected string
	if err := db.Get(&expected, `SELECT compact_notification_id('not-a-UUID',id) AS id FROM posting_windows ORDER BY id LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if firstID != expected {
		t.Fatalf("all-region equal-time order got %s want %s", firstID, expected)
	}
	parsed, err := uuid.Parse(firstID)
	if err != nil || parsed.Version() != 8 || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("not UUIDv8: %s", firstID)
	}
	restarted := &NotificationHub{db: db}
	rows, err = restarted.fetch(ctx, map[int64]*notificationSubscription{1: subscriptions[1]})
	if err != nil || len(rows) != 1 || rows[0].ID == nil || *rows[0].ID != firstID {
		t.Fatalf("restart: %v %v", rows, err)
	}
	db.Get(&count, `SELECT count(*) FROM notifications`)
	if count != 0 {
		t.Fatal("reader performed writes")
	}
	// Existing IDs take precedence over the derived name, without duplication.
	db.MustExec(`INSERT INTO notifications VALUES ('legacy-id','not-a-UUID','window-a',$1)`, at)
	db.MustExec(`UPDATE posting_windows SET ended_at=$1 WHERE id='window-e'`, at)
	rows, err = hub.fetch(ctx, map[int64]*notificationSubscription{1: subscriptions[1]})
	if err != nil || len(rows) != 1 || rows[0].ID == nil || *rows[0].ID != "legacy-id" {
		t.Fatalf("legacy preservation: %v %v", rows, err)
	}
	db.MustExec(`TRUNCATE notification_recipient_snapshots; DELETE FROM notifications; DELETE FROM posting_windows`)
	rows, err = hub.fetch(ctx, map[int64]*notificationSubscription{1: subscriptions[1]})
	if err != nil || rows[0].ID != nil {
		t.Fatalf("reset: %v %v", rows, err)
	}
	// Writer flag off preserves eager storage; the same reader supports both.
	testCompactWindow(t, db, "compact-window", "asia", at, true)
	testCompactWindow(t, db, "eager-window", "europe", at, false)
	db.Get(&count, `SELECT count(*) FROM notifications WHERE posting_window_id='eager-window'`)
	if count != 1 {
		t.Fatalf("eager fallback: %d", count)
	}
}

func TestCompactParallelReadersIntegration(t *testing.T) {
	db := compactTestDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	db.MustExec(`INSERT INTO users VALUES ('user','asia','active')`)
	testCompactWindow(t, db, "window", "asia", at, true)
	sub := map[int64]*notificationSubscription{1: {id: 1, userID: "user", region: "asia", connectedAt: at.Add(-time.Second)}}
	const workers = 8
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := &NotificationHub{db: db}
			rows, err := h.fetch(context.Background(), sub)
			if err != nil {
				errs <- err
				return
			}
			if len(rows) != 1 || rows[0].ID == nil {
				errs <- context.Canceled
				return
			}
			ids <- *rows[0].ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var winner string
	for id := range ids {
		if winner == "" {
			winner = id
		}
		if id != winner {
			t.Errorf("unstable ID %s != %s", id, winner)
		}
	}
	var count int
	db.Get(&count, `SELECT count(*) FROM notifications`)
	if count != 0 {
		t.Fatalf("reader writes: %d", count)
	}
}

func TestCompactDisconnectAndRollbackIntegration(t *testing.T) {
	db := compactTestDB(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	db.MustExec(`INSERT INTO users VALUES ('user','asia','active')`)
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.MustExec(`INSERT INTO posting_windows(id,region,starts_at) VALUES ('aborted','asia',$1)`, at)
	if err := createWindowNotifications(ctx, tx, true, "asia", "aborted", at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	db.Get(&count, `SELECT count(*) FROM notification_recipient_snapshots`)
	if count != 0 {
		t.Fatal("aborted snapshot persisted")
	}
	testCompactWindow(t, db, "window", "asia", at, true)
	for _, change := range []string{`UPDATE users SET region='europe' WHERE id='user'`, `UPDATE users SET region='asia',status='deleted' WHERE id='user'`} {
		db.MustExec(change)
		sub := &notificationSubscription{id: 1, userID: "user", region: "asia", connectedAt: at.Add(-time.Second), events: make(chan notificationEvent, 1)}
		h := &NotificationHub{db: db, subscriptions: map[int64]*notificationSubscription{1: sub}}
		h.poll(ctx)
		select {
		case event := <-sub.events:
			if !event.Disconnect || event.Notification != nil || event.Err != nil {
				t.Fatalf("expected disconnect: %+v", event)
			}
		default:
			t.Fatal("no disconnect")
		}
	}
}

func TestCompactExactIDsIntegration(t *testing.T) {
	db := compactTestDB(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	users := []string{"UPPER-case", "upper-case", "a\"b", "a\\b", "日本語", "not-a-uuid"}
	for _, id := range users {
		db.MustExec(`INSERT INTO users VALUES ($1,'asia','active')`, id)
	}
	testCompactWindow(t, db, "window", "asia", at, true)
	seen := map[string]bool{}
	for i, id := range users {
		h := &NotificationHub{db: db}
		sub := map[int64]*notificationSubscription{int64(i): {id: int64(i), userID: id, region: "asia", connectedAt: at.Add(-time.Second)}}
		rows, err := h.fetch(ctx, sub)
		if err != nil || len(rows) != 1 || rows[0].ID == nil {
			t.Fatalf("exact ID %q: %v %v", id, rows, err)
		}
		if seen[*rows[0].ID] {
			t.Fatalf("duplicate derived ID for %q", id)
		}
		seen[*rows[0].ID] = true
	}
}

// Values independently checked against the actual PostgreSQL function by the
// standalone SQL fixture. This unit test verifies the Go UUID parser contract;
// it is not a substitute for executing the opt-in database integration tests.
func TestCompactNotificationUUIDShape(t *testing.T) {
	for _, value := range []string{"a52597ed-4f23-802f-b2a6-0c095b0da22d", "155bea1f-72b8-8fbf-9902-89f068e3c453"} {
		id, err := uuid.Parse(value)
		if err != nil || id.String() != value || id.Version() != 8 || id.Variant() != uuid.RFC4122 {
			t.Fatalf("invalid virtual UUID %q: %v", value, err)
		}
	}
}
