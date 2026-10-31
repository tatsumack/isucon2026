package isureal

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v5"
)

// asia1 で、europe1 / america1 から転送されたリクエストを、asia のユーザーから直接来たリクエストより優先する。
// asia1 の CPU が張り付くと、転送された europe / america の API が遅れて、得点が1.5倍のリージョンの人数が止まる (#18039、#20097)。
// 直接来たリクエストのうち対象の API だけ、同時に処理する数を ISUREAL_PRIORITY_LIMIT 本に抑える。転送されたリクエストは待たせない。
// 返す結果は変えず、処理を始める順番だけを変える。
//
// 調整用の env (asia1 の etc/env/asia1.sh で設定する)
//   - ISUREAL_PRIORITY_LIMIT: 同時に処理する数の上限。0 (既定) なら何もしない
//   - ISUREAL_PRIORITY_MAX_WAIT: 待つ時間の上限 (既定 1s)。過ぎたら上限を無視して処理する (タイムアウトさせない)
//   - ISUREAL_PRIORITY_SCOPE: heavy (既定) は priorityHeavyRoutes だけ、all は priorityExemptRoutes 以外すべて

// 「投稿 → 友達フィード」の流れに入らない重い API (#20097 の asia1 の pprof: export 10.8%、プロフィール画像 9.0%、Memories 5.1%)
var priorityHeavyRoutes = map[string]bool{
	"GET /api/me/export":        true,
	"PUT /api/me/profile-image": true,
	"GET /api/memories":         true,
	"GET /api/memories/:date":   true,
}

// all でも待たせないもの。投稿期間の作成が遅れると全リージョンに響く (#19579 でタイムアウト)。SSE は接続を長く持つ
var priorityExemptRoutes = map[string]bool{
	"POST /api/initialize":                          true,
	"GET /api/notifications":                        true,
	"POST /api/admin/auth/login":                    true,
	"POST /api/admin/auth/logout":                   true,
	"POST /api/admin/posting-window":                true,
	"PUT /api/admin/users/:userId/certification":    true,
	"DELETE /api/admin/users/:userId/certification": true,
}

const priorityStatsInterval = 10 * time.Second

type priorityStats struct {
	limited atomic.Int64 // 上限の対象になったリクエスト
	waited  atomic.Int64 // 空きを待ったリクエスト
	gaveUp  atomic.Int64 // 待つ時間の上限を過ぎて、上限を無視して処理したリクエスト
	waitNS  atomic.Int64 // 待った時間の合計
}

func PriorityMiddleware(h *HandlerContext) echo.MiddlewareFunc {
	config := h.Config
	// 転送を受けるのは asia1 だけ (europe1 / america1 は DelegateURL を持ち、TrustedPeers を持たない)
	if config.PriorityLimit <= 0 || config.DelegateURL != "" || len(config.TrustedPeers) == 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	slots := make(chan struct{}, config.PriorityLimit)
	stats := &priorityStats{}
	go stats.report()
	limited := func(route string) bool {
		if config.PriorityScope == "all" {
			return !priorityExemptRoutes[route]
		}
		return priorityHeavyRoutes[route]
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			route := c.RouteInfo()
			if isTrustedPeer(c.Request(), config) || !limited(route.Method+" "+route.Path) {
				return next(c)
			}
			stats.limited.Add(1)
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				return next(c)
			default:
			}
			stats.waited.Add(1)
			start := time.Now()
			timer := time.NewTimer(config.PriorityMaxWait)
			defer timer.Stop()
			select {
			case slots <- struct{}{}:
				stats.waitNS.Add(int64(time.Since(start)))
				defer func() { <-slots }()
			case <-timer.C:
				stats.waitNS.Add(int64(time.Since(start)))
				stats.gaveUp.Add(1)
			case <-c.Request().Context().Done():
				return c.Request().Context().Err()
			}
			return next(c)
		}
	}
}

// 調整の目安にするため、10秒ごとに件数を1行出す (動きが無ければ出さない)
func (s *priorityStats) report() {
	for range time.Tick(priorityStatsInterval) {
		limited := s.limited.Swap(0)
		if limited == 0 {
			continue
		}
		waited, gaveUp, waitNS := s.waited.Swap(0), s.gaveUp.Swap(0), s.waitNS.Swap(0)
		avgWait := time.Duration(0)
		if waited > 0 {
			avgWait = time.Duration(waitNS / waited)
		}
		log.Printf("priority: limited=%d waited=%d gave_up=%d avg_wait=%s", limited, waited, gaveUp, avgWait.Round(time.Millisecond))
	}
}
