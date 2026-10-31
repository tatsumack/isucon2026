package isureal

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

// 依頼先で計算してキャッシュに入れた署名が、asia1 で計算した場合と同じになることを確かめる。
func TestImageSignerPrimeMatchesLocal(t *testing.T) {
	config := &Config{TrustedPeers: []string{"127.0.0.1"}}
	e := echo.New()
	e.POST("/internal/image-signatures", postInternalImageSignaturesHandler(&HandlerContext{Config: config}), trustedPeerOnly(&HandlerContext{Config: config}))

	var batch imageSignatureBatch
	users := make([]User, 3)
	for i := range users {
		users[i].ImageKey = make([]byte, 32)
		rand.Read(users[i].ImageKey)
		profileImageID := rand.Text()
		users[i].ProfileImageID = &profileImageID
		batch.add(users[i], rand.Text())
		batch.addProfileImage(users[i])
		batch.addProfileImage(users[i])
	}
	if len(batch.targets) != 6 {
		t.Fatalf("got %d targets, want 6 (duplicates removed)", len(batch.targets))
	}

	// 同じプロセスの中では、依頼先の処理も同じキャッシュに書き込む。依頼元が受け取った結果を入れたことを確かめるため、
	// 依頼先は応答を返す前に、自分が書き込んだキャッシュを消す。
	newServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, r)
			for _, target := range batch.targets {
				imageSignatureCache.Delete(string(target.Key) + ":" + target.ImageID)
			}
			w.WriteHeader(recorder.Code)
			w.Write(recorder.Body.Bytes())
		}))
	}
	server1, server2 := newServer(), newServer()
	defer server1.Close()
	defer server2.Close()

	signer := NewImageSigner(&Config{SigningURLs: []string{server1.URL, server2.URL}})
	signer.Prime(context.Background(), &batch)

	exp := imageURLExpiration(time.Now())
	for _, target := range batch.targets {
		got, ok := cachedImageSignature(target.Key, target.ImageID, exp)
		if !ok {
			t.Fatalf("imageID=%s: not primed (or the minute changed during the test)", target.ImageID)
		}
		if want := referenceImageSignature(target.Key, target.ImageID, exp); !bytes.Equal(got, want) {
			t.Fatalf("imageID=%s: got %x, want %x", target.ImageID, got, want)
		}
	}
}

// 依頼先に届かないときは、キャッシュに何も入れない (signedImageURL が asia1 で計算する)。
func TestImageSignerPrimeIgnoresFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	var batch imageSignatureBatch
	user := User{ImageKey: make([]byte, 32)}
	rand.Read(user.ImageKey)
	batch.add(user, rand.Text())
	NewImageSigner(&Config{SigningURLs: []string{server.URL}}).Prime(context.Background(), &batch)

	if _, ok := imageSignatureCache.Load(string(user.ImageKey) + ":" + batch.targets[0].ImageID); ok {
		t.Fatal("cache was filled despite the failure")
	}
}
