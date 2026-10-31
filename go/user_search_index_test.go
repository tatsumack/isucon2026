package isureal

import (
	"slices"
	"testing"
)

func newTestUserSearchIndex(users [][3]string) *UserSearchIndex {
	index := &UserSearchIndex{byID: map[string]*userSearchEntry{}}
	for _, u := range users {
		index.Add(u[0], u[1], u[2])
	}
	return index
}

func TestUserSearchIndexCandidates(t *testing.T) {
	index := newTestUserSearchIndex([][3]string{
		{"id-c", "carol", "Carol"},
		{"id-a", "alice", "Alice"},
		{"id-b", "bob", "ALICE fan"},
		{"id-d", "al.ice", "x"},
		{"id-e", "al_ice", "y"},
	})
	// 並びは (username, id) のバイト順: al.ice < al_ice < alice < bob < carol
	got := index.candidates("", "", "", "al", nil, 10)
	if want := []string{"id-d", "id-e", "id-a", "id-b"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// 表示名の大文字小文字を畳んで一致する
	got = index.candidates("", "", "", normalizeSearchText("ALICE"), nil, 10)
	if want := []string{"id-a", "id-b"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// 自分・ブロック相手・カーソル以前を除く
	got = index.candidates("id-a", "al.ice", "id-d", "al", map[string]bool{"id-x": true}, 10)
	if want := []string{"id-e", "id-b"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	got = index.candidates("", "", "", "i", map[string]bool{"id-e": true}, 2)
	if want := []string{"id-d", "id-a"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// 表示名の変更と退会
	index.UpdateDisplayName("id-c", "Alicia")
	index.markDeleted([]string{"id-d"})
	got = index.candidates("", "", "", "ali", nil, 10)
	if want := []string{"id-a", "id-b", "id-c"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
