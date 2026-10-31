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
	events      chan notificationEvent
}

type notificationPollRow struct {
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
	for _, row := range rows {
		subscription := subscriptions[row.SubscriptionID]
		if subscription == nil {
			continue
		}
		if row.UserRegion == nil || row.UserStatus == nil || *row.UserStatus != "active" || *row.UserRegion != subscription.region {
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
	if len(subscriptions) == 0 {
		return nil, nil
	}
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
       n.id,
       n.posting_window_id,
       n.region,
       n.created_at
FROM subscribers s
LEFT JOIN users u ON u.id = s.user_id
LEFT JOIN LATERAL (
  SELECT notifications.id,
         notifications.posting_window_id,
         posting_windows.region,
         notifications.created_at
  FROM notifications
  JOIN posting_windows ON posting_windows.id = notifications.posting_window_id
  WHERE notifications.user_id = s.user_id
    AND notifications.created_at > s.connected_at
    AND posting_windows.ended_at IS NULL
  UNION ALL
  SELECT compact_notification_id(s.user_id, w.id) AS id,
         w.id AS posting_window_id,
         w.region,
         w.starts_at AS created_at
  FROM posting_windows w
  JOIN notification_recipient_snapshots r ON r.posting_window_id = w.id
  WHERE w.ended_at IS NULL
    AND w.starts_at > s.connected_at
    AND r.recipients ? s.user_id
    -- Legacy IDs are authoritative, even if a snapshot also covers this pair.
    AND NOT EXISTS (SELECT 1 FROM notifications existing
                    WHERE existing.user_id = s.user_id AND existing.posting_window_id = w.id)
  ORDER BY created_at, id
  LIMIT 1
) n ON true
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
