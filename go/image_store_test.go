package isureal

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestImageStoreOriginSaveLoadReset(t *testing.T) {
	origin := &ImageStore{dir: t.TempDir()}
	id := "0123abcd-new"
	if err := origin.Save(id, []byte("new image")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(origin.path("initial", "4567abcd-initial"), []byte("initial image"), false); err != nil {
		t.Fatal(err)
	}

	data, initial, err := origin.load(context.Background(), id)
	if err != nil || string(data) != "new image" || initial {
		t.Fatalf("load new = %q, %v, %v", data, initial, err)
	}
	data, initial, err = origin.load(context.Background(), "4567abcd-initial")
	if err != nil || string(data) != "initial image" || !initial {
		t.Fatalf("load initial = %q, %v, %v", data, initial, err)
	}

	// /initialize ではベンチ中の画像だけを消し、初期データの画像は残す
	if err := origin.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := readImageFile(origin.path("new", id)); ok {
		t.Fatal("new image remains after reset")
	}
	if _, ok := readImageFile(origin.path("initial", "4567abcd-initial")); !ok {
		t.Fatal("initial image removed by reset")
	}
	if err := origin.Save(id, []byte("again")); err != nil {
		t.Fatalf("save after reset: %v", err)
	}
}

func TestReadImageFileTreatsEmptyAsMissing(t *testing.T) {
	path := t.TempDir() + "/empty"
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readImageFile(path); ok {
		t.Fatal("empty file must be treated as missing")
	}
}

// nginx (www-data) が読めるパーミッションで書き、X-Accel-Redirect のパスは new/ を先に探す
func TestImageStoreAccelPath(t *testing.T) {
	store := &ImageStore{dir: t.TempDir()}
	if err := store.Save("0123abcd-new", []byte("new image")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.path("new", "0123abcd-new"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	if path, ok := store.AccelPath("0123abcd-new"); !ok || path != "/_images/new/01/0123abcd-new" {
		t.Fatalf("AccelPath new = %q, %v", path, ok)
	}
	if err := writeFileAtomic(store.path("initial", "4567abcd-initial"), []byte("initial image"), false); err != nil {
		t.Fatal(err)
	}
	if path, ok := store.AccelPath("4567abcd-initial"); !ok || path != "/_images/initial/45/4567abcd-initial" {
		t.Fatalf("AccelPath initial = %q, %v", path, ok)
	}
	if err := writeFileAtomic(store.path("initial", "89abcdef-empty"), nil, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.AccelPath("89abcdef-empty"); ok {
		t.Fatal("empty file must be treated as missing")
	}
	if _, ok := store.AccelPath("missing"); ok {
		t.Fatal("missing file must not be redirected")
	}
}

func TestImageStoreSaveRejectedOutsideOrigin(t *testing.T) {
	store := &ImageStore{dir: t.TempDir(), originURL: "http://origin.invalid"}
	if err := store.Save("abcd", []byte("x")); err == nil {
		t.Fatal("save must fail outside the origin")
	}
}

func TestImageStoreFetchFromOrigin(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release
		switch r.URL.Path {
		case "/internal/images/aa-initial":
			w.Header().Set(imageInitialHeader, "1")
			w.Write([]byte("initial image"))
		case "/internal/images/bb-new":
			w.Write([]byte("new image"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	store := &ImageStore{dir: t.TempDir(), originURL: server.URL, client: server.Client()}

	// 同じ画像を同時に読んでも、asia1 へは1回だけ取りに行く
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, initial, err := store.load(context.Background(), "aa-initial")
			if err != nil || string(data) != "initial image" || !initial {
				t.Errorf("load = %q, %v, %v", data, initial, err)
			}
		}()
	}
	// 1件目が asia1 で止まっている間に、残りが相乗りする。遅れて来たものは、ディスクに書かれた画像を読む
	for requests.Load() == 0 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if n := requests.Load(); n != 1 {
		t.Fatalf("origin requests = %d, want 1", n)
	}

	// 初期データの画像だけをディスクに残す。ベンチ中の画像は /initialize で消えるため残さない
	if data, ok := readImageFile(store.path("initial", "aa-initial")); !ok || !bytes.Equal(data, []byte("initial image")) {
		t.Fatalf("initial image is not cached on disk: %q", data)
	}
	data, initial, err := store.load(context.Background(), "bb-new")
	if err != nil || string(data) != "new image" || initial {
		t.Fatalf("load new = %q, %v, %v", data, initial, err)
	}
	if _, ok := readImageFile(store.path("initial", "bb-new")); ok {
		t.Fatal("new image must not be cached on disk")
	}
	if _, ok := readImageFile(store.path("new", "bb-new")); ok {
		t.Fatal("new image must not be cached on disk")
	}

	if _, _, err := store.load(context.Background(), "cc-missing"); !errors.Is(err, errImageNotFound) {
		t.Fatalf("missing image err = %v, want errImageNotFound", err)
	}
}

func TestTrustedPeerOnlyRejectsOthers(t *testing.T) {
	config := &Config{TrustedPeers: []string{"192.168.2.10"}}
	for _, tc := range []struct {
		remote string
		want   bool
	}{
		{"192.168.2.10:51234", true},
		{"127.0.0.1:51234", false},
		{"192.168.9.9:51234", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/internal/images/x", nil)
		r.RemoteAddr = tc.remote
		if got := isTrustedPeer(r, config); got != tc.want {
			t.Errorf("isTrustedPeer(%s) = %v, want %v", tc.remote, got, tc.want)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/internal/images/x", nil)
	r.RemoteAddr = "192.168.2.10:1"
	if isTrustedPeer(r, &Config{}) {
		t.Error("no peer is trusted without ISUREAL_TRUSTED_PEERS")
	}
}

func TestByteCacheEvictsOldestFirst(t *testing.T) {
	c := newByteCache(10)
	c.put("a", cachedImage{data: []byte("aaaa")})
	c.put("b", cachedImage{data: []byte("bbbb")})
	c.put("c", cachedImage{data: []byte("cccc")})
	if _, ok := c.get("a"); ok {
		t.Fatal("oldest entry must be evicted first")
	}
	for _, key := range []string{"b", "c"} {
		if _, ok := c.get(key); !ok {
			t.Fatalf("%s must remain", key)
		}
	}
	if c.size != 8 || len(c.order) != 2 {
		t.Fatalf("size = %d, order = %v", c.size, c.order)
	}
}

func TestImageStoreSaveFillsCache(t *testing.T) {
	origin := &ImageStore{dir: t.TempDir()}
	id := "89abcdef-cached"
	data := make([]byte, 5, 64)
	copy(data, "saved")
	if err := origin.Save(id, data); err != nil {
		t.Fatal(err)
	}
	cached, ok := imageDataCache.get(id)
	if !ok || string(cached.data) != "saved" || cached.initial || cap(cached.data) >= cap(data) {
		t.Fatalf("cache after save = %q (cap %d), %v, %v", cached.data, cap(cached.data), cached.initial, ok)
	}
}
