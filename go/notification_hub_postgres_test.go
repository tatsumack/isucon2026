package isureal

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// 専用テストDBのDSNを明示した場合だけ実行する。恒久表には触らず、接続専用TEMP表を使う。
// 実際のadvisory lock競合/MVCCの並行検証はこの単一接続テストでは行わない。
func TestNotificationHubPostgres(t *testing.T) {
	dsn := os.Getenv("ISUREAL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ISUREAL_TEST_POSTGRES_DSN未指定: 実PostgreSQLテストは未実行")
	}
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.Exec(`
 CREATE TEMP TABLE users(id text PRIMARY KEY,region text,status text);
 CREATE TEMP TABLE posting_windows(id text PRIMARY KEY,region text,starts_at timestamptz,ended_at timestamptz,cut_epoch bigint);
 CREATE TEMP TABLE notifications(id text,user_id text,posting_window_id text,created_at timestamptz);
 CREATE TEMP TABLE membership(user_id text,region text,epoch bigint,present boolean,PRIMARY KEY(user_id,region,epoch));
 CREATE TEMP TABLE notification_history_state(id boolean,ready boolean,generation bigint);
 INSERT INTO notification_history_state VALUES(true,true,9);
 INSERT INTO users VALUES('user','europe','active');
 INSERT INTO posting_windows VALUES('window','asia','2026-10-31T00:00:00Z',NULL,3);
 INSERT INTO membership VALUES('user','asia',0,true),('user','asia',4,false);
 `)
	if err != nil {
		t.Fatal(err)
	}
	hub := &NotificationHub{db: db}
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	subscriptions := map[int64]*notificationSubscription{1: {id: 1, userID: "user", region: "europe", connectedAt: at.Add(-time.Second), generation: 9}}
	fetch := func() notificationPollRow {
		t.Helper()
		rows, err := hub.fetch(context.Background(), subscriptions)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("rows=%v", rows)
		}
		return rows[0]
	}
	got := fetch()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("isureal:event-notification:v1:4:user:window")))
	variant := "89ab"[(int(sha256.Sum256([]byte("isureal:event-notification:v1:4:user:window"))[1])&15)&3]
	want := notificationIDPrefix(at) + hash[:3] + "-" + string(variant) + hash[4:7] + "-" + hash[7:19]
	if got.ID == nil || *got.ID != want {
		t.Fatalf("ID=%v want=%s", got.ID, want)
	}
	parsed, err := uuid.Parse(*got.ID)
	if err != nil || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("bad UUID %v %v", parsed, err)
	}
	if got.Region == nil || *got.Region != "asia" || got.Generation != 9 {
		t.Fatalf("historical cross-region recipient lost: %#v", got)
	}
	// 同epoch最終falseを先に除外してはならない。
	if _, err = db.Exec(`INSERT INTO membership VALUES('user','asia',3,false)`); err != nil {
		t.Fatal(err)
	}
	if got = fetch(); got.ID != nil {
		t.Fatalf("false predecessor generated notification: %v", *got.ID)
	}
	// legacyはmembershipより優先しID/created_atを保存する。
	if _, err = db.Exec(`INSERT INTO notifications VALUES('legacy','user','window','2026-10-31T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if got = fetch(); got.ID == nil || *got.ID != "legacy" {
		t.Fatalf("legacy lost: %#v", got)
	}
	subscriptions[1].connectedAt = at
	if got = fetch(); got.ID != nil {
		t.Fatal("equal-time notification must not be returned")
	}
}
