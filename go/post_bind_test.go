package isureal

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func newMultipartPostRequest(t *testing.T, build func(w *multipart.Writer)) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	build(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/posts", &body)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	return req
}

func bindForTest(t *testing.T, req *http.Request) (createPostRequest, error) {
	t.Helper()
	c := echo.New().NewContext(req, httptest.NewRecorder())
	var input createPostRequest
	err := bindCreatePostRequest(c, &input)
	if input.Image != nil {
		t.Cleanup(func() { releaseUpload(input.Image) })
	}
	return input, err
}

func statusOf(err error) int {
	var coder echo.HTTPStatusCoder
	if errors.As(err, &coder) {
		return coder.StatusCode()
	}
	return 0
}

func TestBindCreatePostRequest(t *testing.T) {
	image := []byte("\xff\xd8 image bytes \xff\xd9")
	req := newMultipartPostRequest(t, func(w *multipart.Writer) {
		w.WriteField("caption", "hello")
		fw, _ := w.CreateFormFile("image", "a.jpg")
		fw.Write(image)
		fw, _ = w.CreateFormFile("image", "b.jpg")
		fw.Write([]byte("second"))
		w.WriteField("caption", "ignored")
		w.WriteField("visibility", "public")
		w.WriteField("location_kind", "point")
		w.WriteField("latitude", "35.5")
		w.WriteField("longitude", "")
		w.WriteField("locality", "")
	})
	input, err := bindForTest(t, req)
	if err != nil {
		t.Fatal(err)
	}
	if input.Image == nil || !bytes.Equal(input.Image.Bytes(), image) {
		t.Fatalf("image = %v", input.Image)
	}
	if input.Caption != "hello" || input.Visibility != "public" || input.LocationKind != "point" {
		t.Fatalf("fields = %+v", input)
	}
	if input.Latitude == nil || *input.Latitude != 35.5 {
		t.Fatalf("latitude = %v", input.Latitude)
	}
	if input.Longitude == nil || *input.Longitude != 0 {
		t.Fatalf("longitude = %v", input.Longitude)
	}
	if input.Locality == nil || *input.Locality != "" {
		t.Fatalf("locality = %v", input.Locality)
	}
}

func TestBindCreatePostRequestAbsentFields(t *testing.T) {
	req := newMultipartPostRequest(t, func(w *multipart.Writer) {
		// ファイル名が無い image はファイルとみなさない (c.Bind と同じ)
		w.WriteField("image", "not a file")
		w.WriteField("caption", "hello")
	})
	input, err := bindForTest(t, req)
	if err != nil {
		t.Fatal(err)
	}
	if input.Image != nil || input.Latitude != nil || input.Longitude != nil || input.Locality != nil {
		t.Fatalf("input = %+v", input)
	}
}

// 変更前の c.Bind と同じステータスになることを確かめる
type legacyCreatePostRequest struct {
	Image        *multipart.FileHeader `form:"image"`
	Caption      string                `form:"caption"`
	Visibility   string                `form:"visibility"`
	LocationKind string                `form:"location_kind"`
	Latitude     *float64              `form:"latitude"`
	Longitude    *float64              `form:"longitude"`
	Locality     *string               `form:"locality"`
}

func TestBindCreatePostRequestMatchesEchoBind(t *testing.T) {
	cases := map[string]func() *http.Request{
		"bad float": func() *http.Request {
			return newMultipartPostRequest(t, func(w *multipart.Writer) { w.WriteField("latitude", "abc") })
		},
		"no boundary": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/posts", bytes.NewReader([]byte("x")))
			r.Header.Set(echo.HeaderContentType, "multipart/form-data")
			return r
		},
		"garbage": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/posts", bytes.NewReader([]byte("garbage")))
			r.Header.Set(echo.HeaderContentType, "multipart/form-data; boundary=xyz")
			return r
		},
		"truncated": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/posts", bytes.NewReader([]byte("--xyz\r\nContent-Disposition: form-data; name=\"image\"; filename=\"a.jpg\"\r\n\r\nabc")))
			r.Header.Set(echo.HeaderContentType, "multipart/form-data; boundary=xyz")
			return r
		},
		"text/plain": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/posts", bytes.NewReader([]byte("x")))
			r.Header.Set(echo.HeaderContentType, "text/plain")
			return r
		},
		"urlencoded": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/posts", bytes.NewReader([]byte("caption=a")))
			r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
			return r
		},
		"empty": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/posts", nil)
			r.Header.Set(echo.HeaderContentType, "text/plain")
			return r
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			var legacy legacyCreatePostRequest
			legacyErr := echo.New().NewContext(build(), httptest.NewRecorder()).Bind(&legacy)
			input, err := bindForTest(t, build())
			if statusOf(err) != statusOf(legacyErr) || (err == nil) != (legacyErr == nil) {
				t.Fatalf("err = %v, c.Bind = %v", err, legacyErr)
			}
			if err == nil && (input.Image == nil) != (legacy.Image == nil) {
				t.Fatalf("image = %v, c.Bind = %v", input.Image, legacy.Image)
			}
		})
	}
}

func benchmarkPostBody(b *testing.B) ([]byte, string) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("caption", "hello")
	fw, _ := w.CreateFormFile("image", "a.jpg")
	fw.Write(bytes.Repeat([]byte{0x42}, 300<<10))
	w.WriteField("visibility", "public")
	w.Close()
	return body.Bytes(), w.FormDataContentType()
}

func BenchmarkBindCreatePostRequest(b *testing.B) {
	data, contentType := benchmarkPostBody(b)
	e := echo.New()
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/api/posts", bytes.NewReader(data))
		req.Header.Set(echo.HeaderContentType, contentType)
		var input createPostRequest
		if err := bindCreatePostRequest(e.NewContext(req, nil), &input); err != nil {
			b.Fatal(err)
		}
		releaseUpload(input.Image)
	}
}

func BenchmarkLegacyBindAndReadAll(b *testing.B) {
	data, contentType := benchmarkPostBody(b)
	e := echo.New()
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/api/posts", bytes.NewReader(data))
		req.Header.Set(echo.HeaderContentType, contentType)
		var input legacyCreatePostRequest
		if err := e.NewContext(req, nil).Bind(&input); err != nil {
			b.Fatal(err)
		}
		f, _ := input.Image.Open()
		io.ReadAll(f)
		f.Close()
	}
}
