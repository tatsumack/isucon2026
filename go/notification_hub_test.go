package isureal

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// notificationID が、投稿期間の開始時刻を持つ UUID v7 になり、同じ組では同じIDになることを確かめる。
func TestNotificationIDMakesUUIDv7(t *testing.T) {
	startsAt := time.Date(2026, 10, 31, 3, 47, 20, 730_719_000, time.UTC)
	prev := ""
	for i := range 1000 {
		at := startsAt.Add(time.Duration(i) * 7 * time.Millisecond)
		windowID, userID := uuid.NewString(), uuid.NewString()
		id := notificationID(windowID, userID, at)
		if again := notificationID(windowID, userID, at); again != id {
			t.Fatalf("notificationID is not stable: %q, %q", id, again)
		}
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

func TestNotificationWakeCoalescesAndListenerStops(t *testing.T) {
	hub := &NotificationHub{wake: make(chan struct{}, 1)}
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { hub.wakePoll() })
	}
	wg.Wait()
	if len(hub.wake) != 1 {
		t.Fatal("wake hints were not coalesced")
	}
	<-hub.wake
	hub.wakePoll()
	if len(hub.wake) != 1 {
		t.Fatal("next event could not wake poller")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { hub.listen(ctx, "invalid DSN"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("listener failed to stop on cancellation")
	}
}
