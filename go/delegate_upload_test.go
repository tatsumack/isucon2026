package isureal

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func multipartUpload(t *testing.T, image []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("caption", "hello"); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("image", "a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(image)
	// 2つ目の image は使われない (asia1 も最初の1つだけを保存する)
	second, err := w.CreateFormFile("image", "b.jpg")
	if err != nil {
		t.Fatal(err)
	}
	second.Write([]byte("second"))
	if err := w.WriteField("visibility", "friends"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return &body, w.FormDataContentType()
}

func TestImageIDFromURL(t *testing.T) {
	cases := map[string]string{
		"/api/images/0b6f2c1e-1111-4222-8333-944445555666?exp=1&sig=abc": "0b6f2c1e-1111-4222-8333-944445555666",
		"/api/images/abc":       "abc",
		"/api/images/":          "",
		"/api/posts/abc":        "",
		"/api/images/a/b?exp=1": "",
		"":                      "",
	}
	for in, want := range cases {
		if got := imageIDFromURL(in); got != want {
			t.Errorf("imageIDFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUploadedImageTakesFirstImageFile(t *testing.T) {
	image := bytes.Repeat([]byte{0xFF, 0xD8, 1, 2, 3}, 1000)
	body, contentType := multipartUpload(t, image)
	got, ok := uploadedImage(body.Bytes(), contentType)
	if !ok || !bytes.Equal(got, image) {
		t.Fatalf("uploadedImage: ok=%v, got %d bytes, want %d", ok, len(got), len(image))
	}
	if _, ok := uploadedImage([]byte(`{"a":1}`), "application/json"); ok {
		t.Fatal("non-multipart body should not be captured")
	}
}

// 転送先 (asia1 の代わり) が受け取った本文と、手元のキャッシュに入った画像を確かめる
func TestDelegateCachesUploadedImage(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		imageID  string
		wantHold bool
	}{
		{"created", http.StatusCreated, "11111111-2222-4333-8444-555555555555", true},
		{"rejected", http.StatusBadRequest, "66666666-2222-4333-8444-555555555555", false},
	}
	for _, c := range cases {
		image := bytes.Repeat([]byte(c.name), 5000)
		body, contentType := multipartUpload(t, image)
		sent := append([]byte(nil), body.Bytes()...)
		var received []byte
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(c.status)
			io.WriteString(w, `{"id":"p","imageUrl":"/api/images/`+c.imageID+`?exp=1&sig=x"}`)
		}))
		h := &HandlerContext{Config: &Config{Region: "europe", DelegateURL: origin.URL}}
		e := echo.New()
		e.POST("/api/posts", func(c *echo.Context) error { return nil }, DelegateMiddleware(h))
		req := httptest.NewRequest(http.MethodPost, "/api/posts", body)
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		origin.Close()

		if rec.Code != c.status {
			t.Fatalf("%s: status %d, want %d", c.name, rec.Code, c.status)
		}
		if !bytes.Equal(received, sent) {
			t.Fatalf("%s: forwarded body changed (%d bytes, want %d)", c.name, len(received), len(sent))
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte(c.imageID)) {
			t.Fatalf("%s: response body was not passed through: %s", c.name, rec.Body.String())
		}
		cached, ok := imageDataCache.get(c.imageID)
		if ok != c.wantHold || (ok && !bytes.Equal(cached.data, image)) {
			t.Fatalf("%s: cached=%v (%d bytes), want %v", c.name, ok, len(cached.data), c.wantHold)
		}
		if ok && cached.initial {
			t.Fatalf("%s: uploaded image must not be marked as initial", c.name)
		}
	}
}
