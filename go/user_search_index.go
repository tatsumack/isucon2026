package isureal

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

// UserSearchIndex は、ユーザー検索 (GET /api/users) の絞り込みを asia1 のメモリで行うための索引。
// 検索は europe1 / america1 からも asia1 へ転送されるため (delegate.go)、索引を持つのは asia1 だけ。
//
// 持つのは絞り込みと並び順に使う値 (username, 正規化した username / 表示名, 退会済みか) だけ。
// 応答に使う値 (bio, account_type, プロフィール画像など) はヒットした最大51件を DB から主キーで読む。
// そのため、管理者の認定 (どのサーバでも動く) やプロフィール画像の変更では索引を更新しなくてよい。
//
// 索引の更新箇所:
//   - 起動時と /api/initialize: DB から全件を読み直す (asia1 以外で初期化されたら /internal/user-search/reset で依頼する)
//   - 登録 (asia1 へ転送される): コミット後に追加する
//   - 表示名の変更 (asia1 へ転送される): コミット後に正規化した表示名を差し替える
//   - 退会 (転送されない): 検索で DB から読んだときに status='active' で見つからなければ、その場で索引から外す
//
// 並び順は SQL の ORDER BY username, id と同じバイト順。DB の照合順序は C.UTF-8 (2026-10-31 に確認)。
type UserSearchIndex struct {
	db        *sqlx.DB
	originURL string
	client    *http.Client

	mu      sync.RWMutex
	entries []*userSearchEntry // (username, id) の昇順
	byID    map[string]*userSearchEntry
}

type userSearchEntry struct {
	id                string
	username          string
	searchUsername    string
	searchDisplayName string
}

func NewUserSearchIndex(config *Config, db *sqlx.DB) *UserSearchIndex {
	index := &UserSearchIndex{db: db, originURL: config.DelegateURL, client: &http.Client{}, byID: map[string]*userSearchEntry{}}
	if index.isOrigin() {
		if err := index.load(context.Background()); err != nil {
			panic("failed to load user search index: " + err.Error())
		}
	}
	return index
}

func (index *UserSearchIndex) isOrigin() bool {
	return index.originURL == ""
}

func compareUserSearchEntry(a, b *userSearchEntry) int {
	if c := strings.Compare(a.username, b.username); c != 0 {
		return c
	}
	return strings.Compare(a.id, b.id)
}

func (index *UserSearchIndex) load(ctx context.Context) error {
	var users []struct {
		ID          string `db:"id"`
		Username    string `db:"username"`
		DisplayName string `db:"display_name"`
	}
	if err := index.db.SelectContext(ctx, &users, `SELECT id, username, display_name FROM users WHERE status='active'`); err != nil {
		return err
	}
	entries := make([]*userSearchEntry, len(users))
	byID := make(map[string]*userSearchEntry, len(users))
	for i, user := range users {
		entry := &userSearchEntry{id: user.ID, username: user.Username, searchUsername: normalizeSearchText(user.Username), searchDisplayName: normalizeSearchText(user.DisplayName)}
		entries[i] = entry
		byID[user.ID] = entry
	}
	slices.SortFunc(entries, compareUserSearchEntry)
	index.mu.Lock()
	index.entries = entries
	index.byID = byID
	index.mu.Unlock()
	return nil
}

// Reset は /api/initialize の後に索引を DB から作り直す。asia1 以外では asia1 に依頼する。
func (index *UserSearchIndex) Reset(ctx context.Context) error {
	if index.isOrigin() {
		return index.load(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, index.originURL+"/internal/user-search/reset", nil)
	if err != nil {
		return err
	}
	res, err := index.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("reset user search index on origin: status %d", res.StatusCode)
	}
	return nil
}

// Add は登録したユーザーを索引に入れる。
func (index *UserSearchIndex) Add(id, username, displayName string) {
	if !index.isOrigin() {
		return
	}
	entry := &userSearchEntry{id: id, username: username, searchUsername: normalizeSearchText(username), searchDisplayName: normalizeSearchText(displayName)}
	index.mu.Lock()
	defer index.mu.Unlock()
	if _, ok := index.byID[id]; ok {
		return
	}
	pos, _ := slices.BinarySearchFunc(index.entries, entry, compareUserSearchEntry)
	index.entries = slices.Insert(index.entries, pos, entry)
	index.byID[id] = entry
}

// UpdateDisplayName は表示名の変更を索引に反映する。
func (index *UserSearchIndex) UpdateDisplayName(id, displayName string) {
	if !index.isOrigin() {
		return
	}
	normalized := normalizeSearchText(displayName)
	index.mu.Lock()
	defer index.mu.Unlock()
	if entry, ok := index.byID[id]; ok {
		entry.searchDisplayName = normalized
	}
}

// markDeleted は退会済みのユーザーを索引から外す。
func (index *UserSearchIndex) markDeleted(ids []string) {
	index.mu.Lock()
	defer index.mu.Unlock()
	for _, id := range ids {
		entry, ok := index.byID[id]
		if !ok {
			continue
		}
		delete(index.byID, id)
		pos, found := slices.BinarySearchFunc(index.entries, entry, compareUserSearchEntry)
		if found {
			index.entries = slices.Delete(index.entries, pos, pos+1)
		}
	}
}

// candidates は、条件に合うユーザーの ID を (username, id) の順に最大 limit 件返す。
// 条件は SQL 版と同じ: viewer 以外、(username,id) > (beforeUsername,beforeID)、username か表示名の部分一致、ブロック関係なし。
func (index *UserSearchIndex) candidates(viewer, beforeUsername, beforeID, query string, blocked map[string]bool, limit int) []string {
	index.mu.RLock()
	defer index.mu.RUnlock()
	start := 0
	if beforeUsername != "" {
		start, _ = slices.BinarySearchFunc(index.entries, &userSearchEntry{username: beforeUsername, id: beforeID}, compareUserSearchEntry)
		// (username,id) が一致するものは含めない
		for start < len(index.entries) && index.entries[start].username == beforeUsername && index.entries[start].id == beforeID {
			start++
		}
	}
	ids := make([]string, 0, limit)
	for _, entry := range index.entries[start:] {
		if entry.id == viewer || blocked[entry.id] {
			continue
		}
		if !strings.Contains(entry.searchUsername, query) && !strings.Contains(entry.searchDisplayName, query) {
			continue
		}
		ids = append(ids, entry.id)
		if len(ids) == limit {
			break
		}
	}
	return ids
}

// Search は SQL 版のユーザー検索と同じ結果を (username, id) の順に最大 limit 件返す。
func (index *UserSearchIndex) Search(ctx context.Context, viewer, beforeUsername, beforeID, query string, limit int) ([]User, error) {
	var blockedIDs []string
	if err := index.db.SelectContext(ctx, &blockedIDs, `
  SELECT blocked_id FROM block_relations WHERE blocker_id = $1
  UNION
  SELECT blocker_id FROM block_relations WHERE blocked_id = $1`, viewer); err != nil {
		return nil, err
	}
	blocked := make(map[string]bool, len(blockedIDs))
	for _, id := range blockedIDs {
		blocked[id] = true
	}
	normalized := normalizeSearchText(query)
	for {
		ids := index.candidates(viewer, beforeUsername, beforeID, normalized, blocked, limit)
		if len(ids) == 0 {
			return []User{}, nil
		}
		var users []User
		if err := index.db.SelectContext(ctx, &users, `SELECT `+strings.Join(userColumns, ", ")+` FROM users WHERE id = ANY($1) AND status='active'`, ids); err != nil {
			return nil, err
		}
		if len(users) == len(ids) {
			order := make(map[string]int, len(ids))
			for i, id := range ids {
				order[id] = i
			}
			slices.SortFunc(users, func(a, b User) int { return order[a.ID] - order[b.ID] })
			return users, nil
		}
		// 退会したユーザーが含まれていた。索引から外して数え直す。
		found := make(map[string]bool, len(users))
		for _, user := range users {
			found[user.ID] = true
		}
		var missing []string
		for _, id := range ids {
			if !found[id] {
				missing = append(missing, id)
			}
		}
		index.markDeleted(missing)
	}
}

func resetInternalUserSearchHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := h.UserSearch.Reset(c.Request().Context()); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
