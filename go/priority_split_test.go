package isureal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func TestSplitPriorityIsolationAndBoundedOverflow(t *testing.T) {
	e := echo.New()
	e.Use(PriorityMiddleware(&HandlerContext{Config: &Config{
		TrustedPeers: []string{testPeer}, PriorityScope: "split",
		PriorityLimit: 3, PriorityMaxWait: 20 * time.Millisecond,
	}}))
	entered := make(chan string, 20)
	release := make(chan struct{})
	defer close(release)
	handler := func(c *echo.Context) error {
		entered <- c.Request().URL.Path
		if c.QueryParam("block") == "1" {
			<-release
		}
		return c.NoContent(http.StatusNoContent)
	}
	e.GET("/api/memories", handler)
	e.GET("/api/feeds/:feed", handler)
	e.GET("/api/images/:id", handler)
	e.POST("/api/admin/posting-window", handler)
	start := func(target string) {
		go servePriority(e, http.MethodGet, target+"?block=1", "203.0.113.1")
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request failed to enter: " + target)
		}
	}
	// heavyを埋めても投稿経路と画像には専用枠が残る。
	start("/api/memories")
	start("/api/feeds/friends")
	start("/api/images/a")
	// 4本目は待ち時間経過後に共通追加枠を取る。
	start("/api/memories")
	// 通常3枠+追加1枠が埋まっている。タイマーが過ぎても5本目は実行しない。
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/feeds/friends", nil).WithContext(ctx)
	req.RemoteAddr = "203.0.113.1:1234"
	done := make(chan struct{})
	go func() { e.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-entered:
		t.Fatal("overflow bypassed the concurrency bound")
	case <-time.After(80 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not exit")
	}
	select {
	case <-entered:
		t.Fatal("canceled waiter entered handler")
	default:
	}
	// 全枠が埋まっても管理者と海外の委譲を待たせない。
	for _, tc := range []struct{ method, path, peer string }{
		{http.MethodPost, "/api/admin/posting-window", "203.0.113.1"},
		{http.MethodGet, "/api/feeds/friends", testPeer},
	} {
		bypassDone := make(chan struct{})
		go func() { servePriority(e, tc.method, tc.path, tc.peer); close(bypassDone) }()
		select {
		case <-bypassDone:
		case <-time.After(time.Second):
			t.Fatal("exempt request was blocked")
		}
		<-entered
	}
}

func TestSplitPriorityReleasesSlotAfterHandler(t *testing.T) {
	e := echo.New()
	e.Use(PriorityMiddleware(&HandlerContext{Config: &Config{
		TrustedPeers: []string{testPeer}, PriorityScope: "split", PriorityLimit: 3, PriorityMaxWait: time.Hour,
	}}))
	e.GET("/api/feeds/:feed", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req := httptest.NewRequest(http.MethodGet, "/api/feeds/friends", nil).WithContext(ctx)
		req.RemoteAddr = "203.0.113.1:1234"
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, req)
		cancel()
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("slot leaked: status %d", recorder.Code)
		}
	}
}

func TestSplitPriorityLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config Config
		want   []int
	}{
		{"LIMITだけなら従来どおり (32 -> 16 / 4 / 12)", Config{PriorityLimit: 32}, []int{16, 4, 12}},
		{"LIMIT 3 でも各枠は最低1", Config{PriorityLimit: 3}, []int{1, 1, 1}},
		{"3つとも指定", Config{PriorityLimit: 32, PriorityCriticalLimit: 16, PriorityHeavyLimit: 8, PriorityOtherLimit: 8}, []int{16, 8, 8}},
		{"heavy だけ指定すると、その他は残り", Config{PriorityLimit: 32, PriorityHeavyLimit: 8}, []int{16, 8, 8}},
		{"その他だけ指定すると、heavy は LIMIT/8", Config{PriorityLimit: 32, PriorityOtherLimit: 12}, []int{16, 4, 12}},
		{"枠の合計は LIMIT を超えてよい", Config{PriorityLimit: 32, PriorityHeavyLimit: 8, PriorityOtherLimit: 12}, []int{16, 8, 12}},
	} {
		got := splitPriorityLimits(&tc.config)
		if len(got) != 3 || got[0] != tc.want[0] || got[1] != tc.want[1] || got[2] != tc.want[2] {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProfileImagePriorityGroup(t *testing.T) {
	for name, want := range map[string]struct {
		group  int
		exempt bool
	}{
		"heavy": {1, false}, "": {1, false}, "unknown": {1, false},
		"critical": {0, false}, "other": {2, false}, "exempt": {0, true},
	} {
		group, exempt := profileImagePriorityGroup(name)
		if group != want.group || exempt != want.exempt {
			t.Errorf("%q: got (%d, %v), want (%d, %v)", name, group, exempt, want.group, want.exempt)
		}
	}
}
