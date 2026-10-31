package isureal

import (
	"context"
	"sync"
)

const sessionCacheLimit = 8192

// SessionCache は認証の成功だけを保持する。認証に必要な変更はすべて asia1 で実行する。
// 海外のSSEはDBを直接確認するため、非同期のキャッシュ同期やTTLに依存しない。
// DBが正本で、再起動時は空から開始する。HTTPリクエストには値のコピーを渡す。
type SessionCache struct {
	mu           sync.Mutex
	entries      map[string]Session
	byUser       map[string]string
	changed      map[string]uint64
	writing      map[string]int
	version      uint64
	resetVersion uint64
	resetting    int
	limit        int
}

func newSessionCache(config *Config) *SessionCache {
	if !config.SessionCacheEnabled || config.Region != "asia" || config.DelegateURL != "" {
		return nil
	}
	return &SessionCache{
		entries: make(map[string]Session), byUser: make(map[string]string),
		changed: make(map[string]uint64), writing: make(map[string]int), limit: sessionCacheLimit,
	}
}

// DB読み取り中に対象ユーザーが変更された場合、その結果は要求に返すだけでキャッシュしない。
// 別ユーザーの変更は読み込みを妨げない。不存在・DBエラーはキャッシュしない。
func (s *SessionCache) lookup(ctx context.Context, tokenHash string, load func(context.Context, string) (*Session, error)) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return load(ctx, tokenHash)
	}
	s.mu.Lock()
	if entry, ok := s.entries[tokenHash]; ok && s.resetting == 0 && s.writing[entry.UserID] == 0 {
		s.mu.Unlock()
		return &entry, nil
	}
	version := s.version
	s.mu.Unlock()
	entry, err := load(ctx, tokenHash)
	if err != nil || entry == nil {
		return entry, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetting != 0 || s.resetVersion > version || s.writing[entry.UserID] != 0 || s.changed[entry.UserID] > version {
		return entry, nil
	}
	// 有効なセッションは1ユーザー1つ。失効トークンをメモリに蓄積させない。
	s.evictUser(entry.UserID)
	if len(s.entries) >= s.limit {
		for _, old := range s.entries {
			s.evictUser(old.UserID)
			break
		}
	}
	copy := *entry
	copy.Token = "" // 平文のトークンは保持しない
	s.entries[tokenHash] = copy
	s.byUser[entry.UserID] = tokenHash
	return entry, nil
}

// 呼び出し側はBEGINより前に呼び、COMMIT/ROLLBACKを終えた後に戻り値を呼ぶ。
// 更新開始時に古いエントリを除去し、更新中のキャッシュ登録も止める。
// 同じユーザーの変更が重なっても、すべて終わるまでDBで確認する。
func (s *SessionCache) beginMutation(userID string) func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	s.version++
	s.changed[userID] = s.version
	s.writing[userID]++
	s.evictUser(userID)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.version++
		s.changed[userID] = s.version
		s.writing[userID]--
		if s.writing[userID] == 0 {
			delete(s.writing, userID)
		}
		s.evictUser(userID)
	}
}

func (s *SessionCache) evictUser(userID string) {
	if tokenHash, ok := s.byUser[userID]; ok {
		delete(s.entries, tokenHash)
		delete(s.byUser, userID)
	}
}

// initialize前後で全消去し、リセットをまたいで完了したDB読み取りの再登録も拒否する。
func (s *SessionCache) beginReset() func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	s.resetting++
	s.clear()
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.resetting--
		s.clear()
	}
}

func (s *SessionCache) clear() {
	s.version++
	s.resetVersion = s.version
	clear(s.entries)
	clear(s.byUser)
	clear(s.changed)
	// 更新中のカウンターは保持する。initializeと並行した処理の終了時に消える。
}
