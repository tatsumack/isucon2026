package isureal

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

const testPeer = "192.168.2.10"

func newPriorityTestServer(t *testing.T, scope string, release <-chan struct{}) *echo.Echo {
	t.Helper()
	h := &HandlerContext{Config: &Config{
		TrustedPeers:    []string{testPeer},
		PriorityLimit:   1,
		PriorityMaxWait: 200 * time.Millisecond,
		PriorityScope:   scope,
	}}
	e := echo.New()
	e.Use(PriorityMiddleware(h))
	block := func(c *echo.Context) error {
		if c.QueryParam("block") == "1" {
			<-release
		}
		return c.NoContent(http.StatusNoContent)
	}
	e.GET("/api/memories", block)
	e.GET("/api/feeds/:feed", block)
	e.POST("/api/admin/posting-window", block)
	return e
}

func servePriority(e *echo.Echo, method, target, remoteIP string) time.Duration {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remoteIP + ":12345"
	start := time.Now()
	e.ServeHTTP(httptest.NewRecorder(), req)
	return time.Since(start)
}

// 1本目が枠を埋めている間に、2本目がどれだけ待つか
func elapsedWhileSlotTaken(t *testing.T, scope, method, target, remoteIP string) time.Duration {
	t.Helper()
	release := make(chan struct{})
	e := newPriorityTestServer(t, scope, release)
	var wg sync.WaitGroup
	wg.Go(func() { servePriority(e, http.MethodGet, "/api/memories?block=1", "203.0.113.1") })
	time.Sleep(50 * time.Millisecond) // 1本目が枠を取るのを待つ
	elapsed := servePriority(e, method, target, remoteIP)
	close(release)
	wg.Wait()
	return elapsed
}

func TestPriorityLocalHeavyWaitsThenProceeds(t *testing.T) {
	elapsed := elapsedWhileSlotTaken(t, "heavy", http.MethodGet, "/api/memories", "203.0.113.2")
	if elapsed < 150*time.Millisecond || elapsed > time.Second {
		t.Fatalf("local heavy request should wait about PriorityMaxWait, waited %s", elapsed)
	}
}

func TestPriorityDelegatedDoesNotWait(t *testing.T) {
	elapsed := elapsedWhileSlotTaken(t, "heavy", http.MethodGet, "/api/memories", testPeer)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("delegated request should not wait, waited %s", elapsed)
	}
}

func TestPriorityScope(t *testing.T) {
	cases := []struct {
		scope, method, target string
		wait                  bool
	}{
		{"heavy", http.MethodGet, "/api/feeds/friends", false},
		{"all", http.MethodGet, "/api/feeds/friends", true},
		{"all", http.MethodPost, "/api/admin/posting-window", false},
	}
	for _, c := range cases {
		elapsed := elapsedWhileSlotTaken(t, c.scope, c.method, c.target, "203.0.113.2")
		if waited := elapsed >= 150*time.Millisecond; waited != c.wait {
			t.Errorf("scope=%s %s %s: waited=%v (%s), want %v", c.scope, c.method, c.target, waited, elapsed, c.wait)
		}
	}
}

func TestPriorityDisabledOnDelegatingServer(t *testing.T) {
	h := &HandlerContext{Config: &Config{DelegateURL: "http://192.168.1.10:8080", PriorityLimit: 1, PriorityMaxWait: time.Second}}
	e := echo.New()
	e.Use(PriorityMiddleware(h))
	release := make(chan struct{})
	e.GET("/api/memories", func(c *echo.Context) error {
		if c.QueryParam("block") == "1" {
			<-release
		}
		return c.NoContent(http.StatusNoContent)
	})
	var wg sync.WaitGroup
	wg.Go(func() { servePriority(e, http.MethodGet, "/api/memories?block=1", "203.0.113.1") })
	time.Sleep(50 * time.Millisecond)
	if elapsed := servePriority(e, http.MethodGet, "/api/memories", "203.0.113.2"); elapsed > 100*time.Millisecond {
		t.Fatalf("europe1 / america1 should not limit, waited %s", elapsed)
	}
	close(release)
	wg.Wait()
}
