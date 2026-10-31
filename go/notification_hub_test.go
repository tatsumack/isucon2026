package isureal

import (
	"sync"
	"testing"
	"time"
)

func TestNotificationHubCurrentStateAndGeneration(t *testing.T) {
	str := func(v string) *string { return &v }
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                     string
		status, region           *string
		generation               int64
		id                       *string
		disconnect, notification bool
	}{
		{"active", str("active"), str("asia"), 3, str("notification"), false, true},
		{"moved", str("active"), str("europe"), 3, str("notification"), true, false},
		{"deleted", str("deleted"), str("asia"), 3, str("notification"), true, false},
		{"missing", nil, nil, 3, nil, true, false},
		{"reset_other_node", str("active"), str("asia"), 4, str("notification"), true, false},
		{"no_event", str("active"), str("asia"), 3, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := &NotificationHub{subscriptions: make(map[int64]*notificationSubscription)}
			events, cancel := hub.Subscribe("user", "asia", at, 3)
			defer cancel()
			hub.deliverRows(hub.snapshot(), []notificationPollRow{{SubscriptionID: 1, Generation: tc.generation, UserStatus: tc.status, UserRegion: tc.region, ID: tc.id, PostingWindowID: str("window"), Region: str("europe"), CreatedAt: &at}})
			select {
			case got := <-events:
				if got.Disconnect != tc.disconnect || (got.Notification != nil) != tc.notification {
					t.Fatalf("unexpected event: %#v", got)
				}
				if got.Notification != nil && got.Notification.Region != "europe" {
					t.Fatal("historical recipient can receive another region's window")
				}
			default:
				if tc.disconnect || tc.notification {
					t.Fatal("missing event")
				}
			}
		})
	}
}

func TestNotificationHubResetDisconnectsAndForgets(t *testing.T) {
	hub := &NotificationHub{subscriptions: make(map[int64]*notificationSubscription)}
	events, cancel := hub.Subscribe("user", "asia", time.Now(), 1)
	hub.Reset()
	if len(hub.snapshot()) != 0 {
		t.Fatal("reset retained subscriptions")
	}
	select {
	case got := <-events:
		if !got.Disconnect {
			t.Fatal("reset did not disconnect")
		}
	default:
		t.Fatal("reset did not notify")
	}
	cancel()
	// Reset is idempotent, and IDs are not reused across reset.
	hub.Reset()
	_, cancel = hub.Subscribe("user", "asia", time.Now(), 2)
	defer cancel()
	if hub.nextID != 2 {
		t.Fatal("subscription ID reused")
	}
}

func TestNotificationHubResetReplacesQueuedAndRejectsInflight(t *testing.T) {
	hub := &NotificationHub{subscriptions: make(map[int64]*notificationSubscription)}
	events, cancel := hub.Subscribe("user", "asia", time.Now(), 1)
	defer cancel()
	old := hub.snapshot()[1]
	hub.deliver(old, notificationEvent{Notification: &getNotificationsResponse{ID: "queued"}})
	hub.Reset()
	if got := <-events; !got.Disconnect || got.Notification != nil {
		t.Fatalf("queued notification survived reset: %#v", got)
	}
	hub.deliver(old, notificationEvent{Notification: &getNotificationsResponse{ID: "inflight"}})
	select {
	case got := <-events:
		t.Fatalf("retired poll delivered: %#v", got)
	default:
	}
	_, cancel2 := hub.Subscribe("user", "asia", time.Now(), 2)
	defer cancel2()
	// 同一idを誤って再利用しても古いpointerを受け付けない。
	hub.mu.Lock()
	hub.subscriptions[old.id] = hub.subscriptions[2]
	hub.mu.Unlock()
	hub.deliver(old, notificationEvent{Notification: &getNotificationsResponse{ID: "old-pointer"}})
	select {
	case got := <-events:
		t.Fatalf("old pointer delivered: %#v", got)
	default:
	}
}

func TestNotificationHubUnsubscribeRejectsInflight(t *testing.T) {
	hub := &NotificationHub{subscriptions: make(map[int64]*notificationSubscription)}
	events, cancel := hub.Subscribe("user", "asia", time.Now(), 1)
	old := hub.snapshot()[1]
	cancel()
	hub.deliver(old, notificationEvent{Notification: &getNotificationsResponse{ID: "inflight"}})
	select {
	case got := <-events:
		t.Fatalf("unsubscribed poll delivered: %#v", got)
	default:
	}
}

func TestNotificationHubConcurrentResetDelivery(t *testing.T) {
	for range 100 {
		hub := &NotificationHub{subscriptions: make(map[int64]*notificationSubscription)}
		events, cancel := hub.Subscribe("user", "asia", time.Now(), 1)
		old := hub.snapshot()[1]
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 100 {
				hub.deliver(old, notificationEvent{Notification: &getNotificationsResponse{ID: "racing"}})
			}
		}()
		go func() { defer wg.Done(); hub.Reset() }()
		wg.Wait()
		if got := <-events; !got.Disconnect || got.Notification != nil {
			t.Fatalf("notification survived reset: %#v", got)
		}
		cancel()
	}
}
