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
