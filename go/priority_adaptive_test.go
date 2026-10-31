package isureal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func enqueueAdmission(q *admissionQueue, group int) *admissionWaiter {
	w := &admissionWaiter{ready: make(chan struct{}), started: time.Now()}
	q.queues[group] = append(q.queues[group], w)
	return w
}

func TestAdmissionBorrowAndReturn(t *testing.T) {
	q := newAdmissionQueue(32)
	// 投稿経路が空いている間は、その他の処理が36枠すべてを使える。
	for range 36 {
		if err := q.acquire(context.Background(), admissionOther); err != nil {
			t.Fatal(err)
		}
	}
	q.mu.Lock()
	other := enqueueAdmission(q, admissionOther)
	critical := enqueueAdmission(q, admissionCritical)
	profile := enqueueAdmission(q, admissionProfile)
	memories := enqueueAdmission(q, admissionMemories)
	export := enqueueAdmission(q, admissionExport)
	q.dispatch()
	if other.granted || critical.granted {
		t.Fatal("exceeded global limit")
	}
	q.mu.Unlock()
	// 貸した枠が戻ったときは予約未達のグループに先に渡す。
	for range 4 {
		q.release(admissionOther)
	}
	if !critical.granted || !profile.granted || !memories.granted || !export.granted || other.granted {
		t.Fatal("reservations did not regain released capacity")
	}
	for range 32 {
		q.release(admissionOther)
	}
	for _, g := range []int{admissionCritical, admissionProfile, admissionMemories, admissionExport} {
		q.release(g)
	}
	q.release(admissionOther)
	if q.total != 0 {
		t.Fatalf("leaked slots: %d", q.total)
	}
}

func TestAdmissionHeavyIsolationAndFIFO(t *testing.T) {
	q := newAdmissionQueue(32)
	first := enqueueAdmission(q, admissionExport)
	second := enqueueAdmission(q, admissionExport)
	third := enqueueAdmission(q, admissionExport)
	fourth := enqueueAdmission(q, admissionExport)
	for range 20 {
		enqueueAdmission(q, admissionProfile)
		enqueueAdmission(q, admissionMemories)
	}
	q.dispatch()
	if !first.granted || !second.granted || third.granted || fourth.granted {
		t.Fatal("export cap or FIFO broken")
	}
	if q.active[admissionProfile] != 8 || q.active[admissionMemories] != 8 {
		t.Fatal("profile and Memories must have independent caps")
	}
	// exportが詰まっても他グループの要求を処理する。
	if err := q.acquire(context.Background(), admissionCritical); err != nil {
		t.Fatal(err)
	}
	q.release(admissionCritical)
	q.release(admissionExport)
	if !third.granted || fourth.granted {
		t.Fatal("export queue is not FIFO")
	}
}

func waitAdmissionQueued(t *testing.T, q *admissionQueue, group int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		n := len(q.queues[group])
		q.mu.Unlock()
		if n > 0 {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("request did not queue")
}

func TestAdmissionCancelAndGrantRace(t *testing.T) {
	for range 100 {
		q := newAdmissionQueue(1)
		for range q.limit {
			if err := q.acquire(context.Background(), admissionOther); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			err := q.acquire(ctx, admissionProfile)
			if err == nil {
				q.release(admissionProfile)
			}
			done <- err
		}()
		waitAdmissionQueued(t, q, admissionProfile)
		var wg sync.WaitGroup
		wg.Go(cancel)
		wg.Go(func() { q.release(admissionOther) })
		wg.Wait()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("canceled waiter stuck")
		}
		for i := 1; i < q.limit; i++ {
			q.release(admissionOther)
		}
		q.mu.Lock()
		if q.total != 0 || q.active != [admissionGroups]int{} || len(q.queues[admissionProfile]) != 0 {
			t.Fatal("cancel leaked a slot or waiter")
		}
		q.mu.Unlock()
	}
}

func TestAdmissionCanceledWaiterRemoved(t *testing.T) {
	q := newAdmissionQueue(1)
	if err := q.acquire(context.Background(), admissionExport); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.acquire(ctx, admissionExport) }()
	waitAdmissionQueued(t, q, admissionExport)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel stuck")
	}
	q.release(admissionExport)
	if q.total != 0 || len(q.queues[admissionExport]) != 0 {
		t.Fatal("canceled request retained")
	}
}

func TestAdaptiveHTTPIsolationAndExemptions(t *testing.T) {
	e := echo.New()
	e.Use(PriorityMiddleware(&HandlerContext{Config: &Config{TrustedPeers: []string{testPeer}, PriorityScope: "adaptive", PriorityLimit: 32}}))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	e.GET("/api/me/export", func(c *echo.Context) error { entered <- struct{}{}; <-release; return c.NoContent(204) })
	handler := func(c *echo.Context) error { return c.NoContent(204) }
	e.PUT("/api/me/profile-image", handler)
	e.GET("/api/memories", handler)
	e.GET("/api/feeds/:feed", handler)
	e.POST("/api/admin/posting-window", handler)
	for range 2 {
		wg.Go(func() { servePriority(e, "GET", "/api/me/export", "203.0.113.1") })
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("export did not enter")
		}
	}

	for _, tc := range []struct{ method, path, ip string }{{"PUT", "/api/me/profile-image", "203.0.113.1"}, {"GET", "/api/memories", "203.0.113.1"}, {"GET", "/api/feeds/friends", "203.0.113.1"}, {"POST", "/api/admin/posting-window", "203.0.113.1"}, {"GET", "/api/memories", testPeer}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = tc.ip + ":1234"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", tc.path, rec.Code)
		}
	}
}

func TestAdmissionRouteGroups(t *testing.T) {
	for _, tc := range []struct {
		method, path, feed string
		group              int
	}{
		{"POST", "/api/posts", "", admissionCritical},
		{"GET", "/api/posting-window/current", "", admissionCritical},
		{"GET", "/api/posts/me/current", "", admissionCritical},
		{"GET", "/api/feeds/:feed", "friends", admissionCritical},
		{"GET", "/api/feeds/:feed", "public", admissionOther},
		{"PUT", "/api/me/profile-image", "", admissionProfile},
		{"GET", "/api/memories", "", admissionMemories},
		{"GET", "/api/memories/:date", "", admissionMemories},
		{"GET", "/api/me/export", "", admissionExport},
		{"POST", "/api/friend-requests", "", admissionOther},
		{"GET", "/api/images/:imageId", "", admissionOther},
	} {
		if got := admissionGroup(tc.method, tc.path, tc.feed); got != tc.group {
			t.Fatalf("%+v: group %d", tc, got)
		}
	}
}

func TestAdaptiveExemptWhenAllSlotsBorrowed(t *testing.T) {
	e := echo.New()
	e.Use(PriorityMiddleware(&HandlerContext{Config: &Config{TrustedPeers: []string{testPeer}, PriorityScope: "adaptive", PriorityLimit: 1}}))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	e.GET("/api/users", func(c *echo.Context) error {
		if c.QueryParam("block") == "1" {
			entered <- struct{}{}
			<-release
		}
		return c.NoContent(204)
	})
	e.POST("/api/admin/posting-window", func(c *echo.Context) error { return c.NoContent(204) })
	for range 2 {
		wg.Go(func() { servePriority(e, "GET", "/api/users?block=1", "203.0.113.1") })
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("borrow failed")
		}
	}
	for _, tc := range []struct{ method, path, ip string }{{"POST", "/api/admin/posting-window", "203.0.113.1"}, {"GET", "/api/users", testPeer}} {
		done := make(chan struct{})
		wg.Go(func() { servePriority(e, tc.method, tc.path, tc.ip); close(done) })
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("exempt request queued")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/api/users", nil).WithContext(ctx)
	req.RemoteAddr = "203.0.113.1:1234"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code == 204 {
		t.Fatal("canceled request executed handler")
	}
}
