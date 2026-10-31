package isureal

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

const (
	admissionCritical = iota
	admissionProfile
	admissionMemories
	admissionExport
	admissionOther
	admissionGroups
)

var admissionNames = [admissionGroups]string{"critical", "profile", "memories", "export", "other"}

type admissionWaiter struct {
	ready   chan struct{}
	granted bool
	started time.Time
}

type admissionQueue struct {
	mu                     sync.Mutex
	limit, total, next     int
	active, reserved, caps [admissionGroups]int
	queues                 [admissionGroups][]*admissionWaiter
	admitted, canceled     [admissionGroups]int
	wait                   [admissionGroups]time.Duration
}

// 同時実行上限はsplitの通常枠+overflowと同じ。予約数は優先配分の目安で、
// 待ち手がいなければ他グループに貸す。実行中の処理は横取りしない。
// export/profile/Memoriesの上限で、遅い処理だけによる全枠占有を防ぐ。
func newAdmissionQueue(limit int) *admissionQueue {
	q := &admissionQueue{limit: limit + max(1, limit/8)}
	q.reserved = [admissionGroups]int{max(1, limit*3/8), max(1, limit/8), max(1, limit/8), max(1, limit/16), max(1, limit*5/16)}
	q.caps = [admissionGroups]int{q.limit, max(1, limit/4), max(1, limit/4), max(1, limit/16), q.limit}
	return q
}

func admissionGroup(method, path, feed string) int {
	switch method + " " + path {
	case "GET /api/posting-window/current", "GET /api/posts/me/current", "POST /api/posts":
		return admissionCritical
	case "GET /api/feeds/:feed":
		if feed == "friends" {
			return admissionCritical
		}
	case "PUT /api/me/profile-image":
		return admissionProfile
	case "GET /api/memories", "GET /api/memories/:date":
		return admissionMemories
	case "GET /api/me/export":
		return admissionExport
	}
	return admissionOther
}

// 呼び出し元がmuを保持する。予約数に満たないグループを先に選び、
// 次に余剰枠をround-robinで貸す。各グループ内はFIFO。
func (q *admissionQueue) dispatch() {
	for q.total < q.limit {
		selected := -1
		for pass := 0; pass < 2 && selected < 0; pass++ {
			for offset := 0; offset < admissionGroups; offset++ {
				group := (q.next + offset) % admissionGroups
				if len(q.queues[group]) == 0 || q.active[group] >= q.caps[group] {
					continue
				}
				if pass == 0 && q.active[group] >= q.reserved[group] {
					continue
				}
				selected = group
				break
			}
		}
		if selected < 0 {
			return
		}
		group := selected
		w := q.queues[group][0]
		q.queues[group][0] = nil
		q.queues[group] = q.queues[group][1:]
		q.active[group]++
		q.total++
		q.next = (group + 1) % admissionGroups
		q.admitted[group]++
		q.wait[group] += time.Since(w.started)
		w.granted = true
		close(w.ready)
	}
}

func (q *admissionQueue) acquire(ctx context.Context, group int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w := &admissionWaiter{ready: make(chan struct{}), started: time.Now()}
	q.mu.Lock()
	q.queues[group] = append(q.queues[group], w)
	q.dispatch()
	q.mu.Unlock()
	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		q.mu.Lock()
		q.canceled[group]++
		if w.granted {
			q.active[group]--
			q.total--
		} else {
			for i, queued := range q.queues[group] {
				if queued == w {
					copy(q.queues[group][i:], q.queues[group][i+1:])
					last := len(q.queues[group]) - 1
					q.queues[group][last] = nil
					q.queues[group] = q.queues[group][:last]
					break
				}
			}
		}
		q.dispatch()
		q.mu.Unlock()
		return ctx.Err()
	}
}

func (q *admissionQueue) release(group int) {
	q.mu.Lock()
	q.active[group]--
	q.total--
	q.dispatch()
	q.mu.Unlock()
}

// waitは同じ集計期間に入場した要求だけの合計/件数。開始と完了を別の期間で
// 数える旧ログのavg_waitと異なり、分母と分子の対象を揃える。
func (q *admissionQueue) report() {
	for range time.Tick(priorityStatsInterval) {
		q.mu.Lock()
		active, admitted, canceled, wait := q.active, q.admitted, q.canceled, q.wait
		var queued [admissionGroups]int
		for g := range admissionGroups {
			queued[g] = len(q.queues[g])
		}
		q.admitted = [admissionGroups]int{}
		q.canceled = [admissionGroups]int{}
		q.wait = [admissionGroups]time.Duration{}
		q.mu.Unlock()
		for g, name := range admissionNames {
			if admitted[g] == 0 && queued[g] == 0 && canceled[g] == 0 {
				continue
			}
			var avg time.Duration
			if admitted[g] > 0 {
				avg = wait[g] / time.Duration(admitted[g])
			}
			log.Printf("priority_adaptive: group=%s active=%d queued=%d admitted=%d canceled=%d avg_wait=%s", name, active[g], queued[g], admitted[g], canceled[g], avg.Round(time.Millisecond))
		}
	}
}

func adaptivePriorityMiddleware(config *Config) echo.MiddlewareFunc {
	q := newAdmissionQueue(config.PriorityLimit)
	log.Printf("priority_adaptive: limit=%d reserved=%v caps=%v groups=%v", q.limit, q.reserved, q.caps, admissionNames)
	go q.report()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			route := c.RouteInfo()
			if isTrustedPeer(c.Request(), config) || priorityExemptRoutes[route.Method+" "+route.Path] {
				return next(c)
			}
			group := admissionGroup(route.Method, route.Path, c.Param("feed"))
			ctx := c.Request().Context()
			if err := q.acquire(ctx, group); err != nil {
				return err
			}
			defer q.release(group)
			if err := ctx.Err(); err != nil {
				return err
			}
			return next(c)
		}
	}
}
