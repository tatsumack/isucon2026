package isureal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func testSessionCache() *SessionCache {
	return newSessionCache(&Config{Region: "asia", SessionCacheEnabled: true})
}

func TestSessionCacheCopyAndMisses(t *testing.T) {
	c := testSessionCache()
	calls := 0
	load := func(context.Context, string) (*Session, error) {
		calls++
		return &Session{UserID: "u", TokenHash: "h", Region: "asia", Token: "secret"}, nil
	}
	first, _ := c.lookup(context.Background(), "h", load)
	first.Region = "europe"
	second, _ := c.lookup(context.Background(), "h", load)
	if calls != 1 || second.Region != "asia" || second.Token != "" {
		t.Fatalf("cache copy: calls=%d session=%+v", calls, second)
	}
	second.Region = "america"
	third, _ := c.lookup(context.Background(), "h", load)
	if third.Region != "asia" {
		t.Fatal("hit exposed shared state")
	}
	for _, loadErr := range []error{nil, errors.New("database unavailable")} {
		calls = 0
		miss := func(context.Context, string) (*Session, error) { calls++; return nil, loadErr }
		for i := 0; i < 2; i++ {
			_, err := c.lookup(context.Background(), "missing", miss)
			if !errors.Is(err, loadErr) {
				t.Fatal(err)
			}
		}
		if calls != 2 {
			t.Fatal("negative result cached")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.lookup(ctx, "h", load); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSessionCacheInvalidation(t *testing.T) {
	for _, change := range []string{"logout", "replacement", "region", "deletion"} {
		t.Run(change, func(t *testing.T) {
			c := testSessionCache()
			state := &Session{UserID: "u", TokenHash: "old", Region: "asia"}
			calls := 0
			load := func(_ context.Context, hash string) (*Session, error) {
				calls++
				if state == nil || state.TokenHash != hash {
					return nil, nil
				}
				copy := *state
				return &copy, nil
			}
			c.lookup(context.Background(), "old", load)
			finish := c.beginMutation("u")
			switch change {
			case "region":
				state = &Session{UserID: "u", TokenHash: "old", Region: "europe"}
			case "replacement":
				state = &Session{UserID: "u", TokenHash: "new", Region: "asia"}
			default:
				state = nil
			}
			finish()
			got, err := c.lookup(context.Background(), "old", load)
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if change == "region" {
				if got == nil || got.Region != "europe" {
					t.Fatal("stale region")
				}
			} else if got != nil {
				t.Fatal("invalid token accepted")
			}
			if change == "replacement" {
				got, _ = c.lookup(context.Background(), "new", load)
				if got == nil {
					t.Fatal("replacement token rejected")
				}
			}
		})
	}
}

func TestSessionCacheConcurrentFill(t *testing.T) {
	for _, action := range []string{"mutation", "reset", "other-user"} {
		t.Run(action, func(t *testing.T) {
			c := testSessionCache()
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				c.lookup(context.Background(), "h", func(context.Context, string) (*Session, error) {
					close(started)
					<-release
					return &Session{UserID: "u", TokenHash: "h", Region: "asia"}, nil
				})
			}()
			<-started
			switch action {
			case "mutation":
				c.beginMutation("u")()
			case "reset":
				c.beginReset()()
			case "other-user":
				c.beginMutation("other")()
			}
			close(release)
			<-done
			calls := 0
			got, _ := c.lookup(context.Background(), "h", func(context.Context, string) (*Session, error) { calls++; return nil, nil })
			if action == "other-user" {
				if calls != 0 || got == nil {
					t.Fatal("unrelated write prevented fill")
				}
			} else if calls != 1 || got != nil {
				t.Fatal("stale result repopulated cache")
			}
		})
	}
}

func TestSessionCacheOverlappingMutationsAndReset(t *testing.T) {
	c := testSessionCache()
	calls := 0
	load := func(context.Context, string) (*Session, error) {
		calls++
		return &Session{UserID: "u", TokenHash: "h"}, nil
	}
	a, b := c.beginMutation("u"), c.beginMutation("u")
	reset := c.beginReset()
	c.lookup(context.Background(), "h", load)
	reset()
	a()
	c.lookup(context.Background(), "h", load)
	c.lookup(context.Background(), "h", load)
	if calls != 3 {
		t.Fatal("cached while write pending")
	}
	b()
	c.lookup(context.Background(), "h", load)
	c.lookup(context.Background(), "h", load)
	if calls != 4 {
		t.Fatalf("cache did not recover: %d", calls)
	}
	c.beginReset()()
	c.lookup(context.Background(), "h", load)
	if calls != 5 {
		t.Fatal("reset retained cache")
	}
}

func TestSessionCacheCapacityAndConfiguration(t *testing.T) {
	c := testSessionCache()
	c.limit = 2
	for i := 0; i < 10; i++ {
		key := fmt.Sprint(i)
		c.lookup(context.Background(), key, func(context.Context, string) (*Session, error) { return &Session{UserID: key, TokenHash: key}, nil })
	}
	if len(c.entries) != 2 || len(c.byUser) != 2 {
		t.Fatal("capacity exceeded")
	}
	for user, hash := range c.byUser {
		if c.entries[hash].UserID != user {
			t.Fatal("eviction index inconsistent")
		}
	}
	for _, config := range []*Config{{Region: "asia"}, {Region: "europe", SessionCacheEnabled: true}, {Region: "asia", DelegateURL: "http://origin", SessionCacheEnabled: true}} {
		disabled := newSessionCache(config)
		if disabled != nil {
			t.Fatal("cache enabled outside origin")
		}
		disabled.beginMutation("u")()
		disabled.beginReset()()
		calls := 0
		for i := 0; i < 2; i++ {
			disabled.lookup(context.Background(), "h", func(context.Context, string) (*Session, error) { calls++; return &Session{UserID: "u"}, nil })
		}
		if calls != 2 {
			t.Fatal("disabled cache did not use DB")
		}
	}
}

func TestSessionMiddlewareCacheHit(t *testing.T) {
	cache := testSessionCache()
	hashBytes := sha256.Sum256([]byte("token"))
	hash := hex.EncodeToString(hashBytes[:])
	cache.lookup(context.Background(), hash, func(context.Context, string) (*Session, error) {
		return &Session{UserID: "u", TokenHash: hash, Region: "asia"}, nil
	})
	e := echo.New()
	e.HTTPErrorHandler = APIErrorHandler
	e.GET("/", func(c *echo.Context) error {
		if GetSession(c).UserID != "u" {
			t.Error("wrong session")
		}
		return c.NoContent(http.StatusNoContent)
	}, SessionMiddleware(&HandlerContext{Sessions: cache}, true))
	for _, tc := range []struct {
		auth   string
		status int
	}{{"Bearer token", 204}, {"", 401}, {"Basic token", 401}} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", tc.auth)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("auth=%q: status %d", tc.auth, rec.Code)
		}
	}
}
