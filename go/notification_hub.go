package isureal

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jmoiron/sqlx"
)

const notificationPollInterval = 500 * time.Millisecond

type notificationEvent struct {
	Notification *getNotificationsResponse
	Disconnect   bool
	Err          error
}

type notificationSubscription struct {
	id          int64
	userID      string
	region      string
	connectedAt time.Time
	events      chan notificationEvent
}

type notificationPollRow struct {
	SubscriptionID  int64      `db:"subscription_id"`
	UserRegion      *string    `db:"user_region"`
	UserStatus      *string    `db:"user_status"`
	PostingWindowID *string    `db:"posting_window_id"`
	Region          *string    `db:"region"`
	CreatedAt       *time.Time `db:"created_at"`
}

type NotificationHub struct {
	db   *sqlx.DB
	wake chan struct{}

	mu            sync.Mutex
	nextID        int64
	subscriptions map[int64]*notificationSubscription
}

func NewNotificationHub(db *sqlx.DB) *NotificationHub {
	hub := &NotificationHub{
		db:            db,
		wake:          make(chan struct{}, 1),
		subscriptions: make(map[int64]*notificationSubscription),
	}
	go hub.run()
	return hub
}

func (h *NotificationHub) Subscribe(userID, region string, connectedAt time.Time) (<-chan notificationEvent, func()) {
	h.mu.Lock()
	h.nextID++
	subscription := &notificationSubscription{
		id:          h.nextID,
		userID:      userID,
		region:      region,
		connectedAt: connectedAt,
		events:      make(chan notificationEvent, 1),
	}
	h.subscriptions[subscription.id] = subscription
	h.mu.Unlock()

	return subscription.events, func() {
		h.mu.Lock()
		delete(h.subscriptions, subscription.id)
		h.mu.Unlock()
	}
}

func (h *NotificationHub) run() {
	ticker := time.NewTicker(notificationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-h.wake:
		}
		h.poll(context.Background())
	}
}

func (h *NotificationHub) poll(ctx context.Context) {
	subscriptions := h.snapshot()
	if len(subscriptions) == 0 {
		return
	}

	rows, err := h.fetch(ctx, subscriptions)
	if err != nil {
		for _, subscription := range subscriptions {
			h.deliver(subscription, notificationEvent{Err: err})
		}
		return
	}
	for _, row := range rows {
		subscription := subscriptions[row.SubscriptionID]
		if subscription == nil {
			continue
		}
		if row.UserRegion == nil || row.UserStatus == nil || *row.UserStatus != "active" || *row.UserRegion != subscription.region {
			h.deliver(subscription, notificationEvent{Disconnect: true})
			continue
		}
		if row.PostingWindowID == nil {
			continue
		}
		h.deliver(subscription, notificationEvent{Notification: &getNotificationsResponse{
			ID:              notificationID(*row.PostingWindowID, subscription.userID, *row.CreatedAt),
			PostingWindowID: *row.PostingWindowID,
			Region:          *row.Region,
			CreatedAt:       *row.CreatedAt,
		}})
	}
}

func (h *NotificationHub) snapshot() map[int64]*notificationSubscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	subscriptions := make(map[int64]*notificationSubscription, len(h.subscriptions))
	for id, subscription := range h.subscriptions {
		subscriptions[id] = subscription
	}
	return subscriptions
}

func (h *NotificationHub) fetch(ctx context.Context, subscriptions map[int64]*notificationSubscription) ([]notificationPollRow, error) {
	values := make([]string, 0, len(subscriptions))
	args := make([]any, 0, len(subscriptions)*4)
	for _, subscription := range subscriptions {
		base := len(args)
		values = append(values, fmt.Sprintf("($%d::bigint, $%d::text, $%d::timestamptz)", base+1, base+2, base+3))
		args = append(args, subscription.id, subscription.userID, subscription.connectedAt)
	}
	query := `
WITH subscribers(subscription_id, user_id, connected_at) AS (VALUES ` + strings.Join(values, ",") + `)
SELECT s.subscription_id,
       u.region AS user_region,
       u.status AS user_status,
       w.id AS posting_window_id,
       w.region,
       w.starts_at AS created_at
FROM subscribers s
LEFT JOIN users u ON u.id = s.user_id
LEFT JOIN posting_windows w
  ON w.region = u.region
 AND w.ended_at IS NULL
 AND w.starts_at > s.connected_at
`
	var rows []notificationPollRow
	if err := h.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *NotificationHub) deliver(subscription *notificationSubscription, event notificationEvent) {
	select {
	case subscription.events <- event:
	default:
	}
}

// notificationID は、投稿期間とユーザーから通知IDを決める。通知の行を持たないので、同じ組には毎回同じIDを返す。
// 先頭は投稿期間の開始時刻を入れたUUID v7の形 (notificationIDPrefix) で、後半は投稿期間IDとユーザーIDのSHA-1から作る。
func notificationID(postingWindowID, userID string, startsAt time.Time) string {
	return notificationIDPrefix(startsAt) + uuid.NewSHA1(uuid.Nil, []byte(postingWindowID+":"+userID)).String()[15:]
}

// notificationIDPrefix は、UUID v7 の先頭15文字 (48ビットのミリ秒時刻とバージョンの7) を返す。
func notificationIDPrefix(t time.Time) string {
	ms := uint64(t.UnixMilli())
	return fmt.Sprintf("%08x-%04x-7", ms>>16, ms&0xffff)
}

// 通知は起床のヒントだけ。対象者・接続時刻・終了済み期間の判定は必ず既存の SQL で行う。
// NOTIFY はトランザクションの commit 後に届く。接続断で失っても500msのpollで補う。
func (h *NotificationHub) wakePoll() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *NotificationHub) listen(ctx context.Context, dsn string) {
	for ctx.Err() == nil {
		err := h.listenOnce(ctx, dsn)
		if ctx.Err() != nil {
			return
		}
		log.Printf("notification LISTEN reconnect: %v", err)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (h *NotificationHub) listenOnce(ctx context.Context, dsn string) error {
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(connectCtx, dsn)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err := conn.Exec(connectCtx, "LISTEN isureal_posting_window"); err != nil {
		return err
	}
	h.wakePoll() // 再接続までに commit された期間も拾う
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		h.wakePoll()
	}
}
