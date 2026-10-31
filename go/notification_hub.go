package isureal

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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
	generation  int64
	events      chan notificationEvent
}

type notificationPollRow struct {
	Generation      int64      `db:"generation"`
	SubscriptionID  int64      `db:"subscription_id"`
	UserRegion      *string    `db:"user_region"`
	UserStatus      *string    `db:"user_status"`
	ID              *string    `db:"id"`
	PostingWindowID *string    `db:"posting_window_id"`
	Region          *string    `db:"region"`
	CreatedAt       *time.Time `db:"created_at"`
}

type NotificationHub struct {
	db *sqlx.DB

	mu            sync.Mutex
	nextID        int64
	subscriptions map[int64]*notificationSubscription
}

func NewNotificationHub(db *sqlx.DB) *NotificationHub {
	hub := &NotificationHub{
		db:            db,
		subscriptions: make(map[int64]*notificationSubscription),
	}
	go hub.run()
	return hub
}

func (h *NotificationHub) Subscribe(userID, region string, connectedAt time.Time, generation int64) (<-chan notificationEvent, func()) {
	h.mu.Lock()
	h.nextID++
	subscription := &notificationSubscription{
		id:          h.nextID,
		userID:      userID,
		region:      region,
		connectedAt: connectedAt,
		generation:  generation,
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
	for range ticker.C {
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
	h.deliverRows(subscriptions, rows)
}

func (h *NotificationHub) deliverRows(subscriptions map[int64]*notificationSubscription, rows []notificationPollRow) {
	for _, row := range rows {
		subscription := subscriptions[row.SubscriptionID]
		if subscription == nil {
			continue
		}
		if row.Generation != subscription.generation || row.UserRegion == nil || row.UserStatus == nil || *row.UserStatus != "active" || *row.UserRegion != subscription.region {
			h.deliver(subscription, notificationEvent{Disconnect: true})
			continue
		}
		if row.ID == nil {
			continue
		}
		h.deliver(subscription, notificationEvent{Notification: &getNotificationsResponse{
			ID:              *row.ID,
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
	query := fmt.Sprintf(notificationPollQuery, strings.Join(values, ","))

	var rows []notificationPollRow
	if err := h.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *NotificationHub) deliver(subscription *notificationSubscription, event notificationEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// poll開始後のunsubscribe/Resetで退役した購読へ配送しない。
	if h.subscriptions[subscription.id] != subscription {
		return
	}
	select {
	case subscription.events <- event:
	default:
	}
}

// Resetはinitializeの前後にローカル購読を閉じる。別ノードの購読はDBのgenerationで失効する。
func (h *NotificationHub) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, subscription := range h.subscriptions {
		// 既にqueueに入った通知も破棄し、Disconnectへ置き換える。
		select {
		case <-subscription.events:
		default:
		}
		subscription.events <- notificationEvent{Disconnect: true}
	}
	clear(h.subscriptions)
}

// 全regionのactive windowと現在userを同一snapshotで読む。session regionはpollで照合する。
// cutのpredecessorを選んだ後にpresentを判定する（falseを先に除くと退会/移動が復活する）。
// legacy IDとcreated_atは常に優先し、旧windowには仮想通知を遡及生成しない。
const notificationPollQuery = `
WITH subscribers(subscription_id, user_id, connected_at) AS (VALUES %s),
active_windows AS MATERIALIZED (
  SELECT id, region, starts_at, cut_epoch
  FROM posting_windows WHERE ended_at IS NULL
)
SELECT s.subscription_id,
       hs.generation,
       u.region AS user_region,
       u.status AS user_status,
       n.id, n.posting_window_id, n.region, n.created_at
FROM subscribers s
CROSS JOIN notification_history_state hs
LEFT JOIN users u ON u.id = s.user_id
LEFT JOIN LATERAL (
  SELECT COALESCE(legacy.id,
           substr(parts.ts, 1, 8) || '-' || substr(parts.ts, 9, 4) || '-7' ||
           substr(parts.hash, 1, 3) || '-' ||
           substr('89ab', ((strpos('0123456789abcdef', substr(parts.hash, 4, 1)) - 1) & 3) + 1, 1) ||
           substr(parts.hash, 5, 3) || '-' || substr(parts.hash, 8, 12)) AS id,
         w.id AS posting_window_id,
         w.region,
         COALESCE(legacy.created_at, w.starts_at) AS created_at
  FROM active_windows w
  LEFT JOIN notifications legacy
    ON legacy.user_id = s.user_id AND legacy.posting_window_id = w.id
  LEFT JOIN LATERAL (
    SELECT m.present
    FROM membership m
    WHERE m.user_id = s.user_id AND m.region = w.region AND m.epoch <= w.cut_epoch
    ORDER BY m.epoch DESC LIMIT 1
  ) member ON w.cut_epoch IS NOT NULL AND legacy.id IS NULL
  CROSS JOIN LATERAL (
    SELECT lpad(to_hex(floor(extract(epoch FROM w.starts_at) * 1000)::bigint), 12, '0') AS ts,
           encode(sha256(convert_to('isureal:event-notification:v1:' || length(s.user_id)::text || ':' || s.user_id || ':' || w.id, 'UTF8')), 'hex') AS hash
  ) parts
  WHERE (legacy.id IS NOT NULL OR (w.cut_epoch IS NOT NULL AND member.present))
    AND COALESCE(legacy.created_at, w.starts_at) > s.connected_at
  ORDER BY created_at, id
  LIMIT 1
) n ON true
WHERE hs.id = true
`
