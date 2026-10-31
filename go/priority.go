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
//   - ISUREAL_PRIORITY_MAX_WAIT: 既定 1s。heavy/all は超過後に上限を無視、split は共通追加枠を待つ
//   - ISUREAL_PRIORITY_SCOPE: heavy (既定) は重い API、all は除外 API 以外、split は投稿経路・重い API・その他に枠を分ける

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
	if config.PriorityScope == "split" && config.PriorityLimit >= 3 {
		return splitPriorityMiddleware(config)
	}
	slots := make(chan struct{}, config.PriorityLimit)
	stats := &priorityStats{}
	go stats.report()
	limited := func(route string) bool {
		if config.PriorityScope == "all" || config.PriorityScope == "split" {
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
func (s *priorityStats) report() { s.reportGroup("shared") }

func (s *priorityStats) reportGroup(group string) {
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
		log.Printf("priority: group=%s limited=%d waited=%d gave_up=%d avg_wait=%s", group, limited, waited, gaveUp, avgWait.Round(time.Millisecond))
	}
}

// splitPriorityLimits は split の枠 (投稿の経路 / heavy / その他) を返す。
// ISUREAL_PRIORITY_CRITICAL / _HEAVY / _OTHER で個別に指定でき、0 (未指定) のものは
// ISUREAL_PRIORITY_LIMIT から決める (投稿の経路は 1/2、heavy は 1/8、その他は残り。どれも最低1)。
func splitPriorityLimits(config *Config) []int {
	critical := config.PriorityCriticalLimit
	if critical <= 0 {
		critical = max(1, config.PriorityLimit/2)
	}
	heavy := config.PriorityHeavyLimit
	if heavy <= 0 {
		heavy = max(1, config.PriorityLimit/8)
	}
	other := config.PriorityOtherLimit
	if other <= 0 {
		other = max(1, config.PriorityLimit-critical-heavy)
	}
	return []int{critical, heavy, other}
}

// profileImagePriorityGroup は、split で PUT /api/me/profile-image を入れる枠 (0 投稿の経路 / 1 heavy / 2 その他) を返す。
// exempt なら枠に並ばない。未知の値は heavy (従来どおり)。新規ユーザーの入口にあり、処理は軽い (CPU 約3.6ms) ため、
// heavy の Memories や export の待ちに巻き込まれないよう、別の枠に出せるようにする。
func profileImagePriorityGroup(name string) (group int, exempt bool) {
	switch name {
	case "critical":
		return 0, false
	case "other":
		return 2, false
	case "exempt":
		return 0, true
	}
	return 1, false
}

// split は遅い画像転送や export が投稿経路の枠を埋めないようにする。
// 32枠なら投稿16 / heavy4 / その他12。海外からの委譲と管理者/SSEは従来どおり対象外。
// MaxWait 後も無制限には実行せず、全グループ共通の追加枠 (32枠なら4) に入れる。
// 追加枠も満杯なら通常枠か追加枠が空くまで待つ。切断された待機リクエストは実行しない。
func splitPriorityMiddleware(config *Config) echo.MiddlewareFunc {
	limits := splitPriorityLimits(config)
	log.Printf("priority: split limits critical=%d heavy=%d other=%d overflow=%d profile_image=%s", limits[0], limits[1], limits[2], max(1, config.PriorityLimit/8), config.PriorityProfileImageGroup)
	slots := make([]chan struct{}, len(limits))
	stats := make([]*priorityStats, len(limits))
	for i, limit := range limits {
		slots[i] = make(chan struct{}, limit)
		stats[i] = &priorityStats{}
		go stats[i].reportGroup([]string{"critical", "heavy", "other"}[i])
	}
	overflow := make(chan struct{}, max(1, config.PriorityLimit/8))
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			route := c.RouteInfo()
			key := route.Method + " " + route.Path
			if isTrustedPeer(c.Request(), config) || priorityExemptRoutes[key] {
				return next(c)
			}
			group := 2
			if key == "PUT /api/me/profile-image" {
				g, exempt := profileImagePriorityGroup(config.PriorityProfileImageGroup)
				if exempt {
					return next(c)
				}
				group = g
			} else {
				switch key {
				case "GET /api/posting-window/current", "GET /api/posts/me/current", "POST /api/posts":
					group = 0
				case "GET /api/feeds/:feed":
					if c.Param("feed") == "friends" {
						group = 0
					}
				default:
					if priorityHeavyRoutes[key] {
						group = 1
					}
				}
			}
			stat := stats[group]
			stat.limited.Add(1)
			ctx := c.Request().Context()
			if err := ctx.Err(); err != nil {
				return err
			}
			acquired := slots[group]
			select {
			case acquired <- struct{}{}:
			default:
				stat.waited.Add(1)
				start := time.Now()
				timer := time.NewTimer(config.PriorityMaxWait)
				defer timer.Stop()
				select {
				case acquired <- struct{}{}:
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					select {
					case acquired <- struct{}{}:
					case overflow <- struct{}{}:
						acquired = overflow
						stat.gaveUp.Add(1)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				stat.waitNS.Add(int64(time.Since(start)))
			}
			defer func() { <-acquired }()
			if err := ctx.Err(); err != nil {
				return err
			}
			return next(c)
		}
	}
}
