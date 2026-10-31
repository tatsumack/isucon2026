package isureal

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestExportImageStreamingZIPAndCacheIsolation(t *testing.T) {
	store := &ImageStore{dir: t.TempDir()}
	data := bytes.Repeat([]byte("jpeg bytes unchanged"), 10000)
	for _, initial := range []bool{true, false} {
		id := uuid.NewString()
		kind := "new"
		if initial {
			kind = "initial"
		}
		if err := writeFileAtomic(store.path(kind, id), data, false); err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		zw := zip.NewWriter(&body)
		entry, err := zw.CreateHeader(&zip.FileHeader{Name: "images/000001.jpg", Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.writeExportImage(context.Background(), entry, id, initial); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(body.Bytes()), int64(body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := zr.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("ZIP content/CRC mismatch: %v", err)
		}
		if _, ok := imageDataCache.get(id); ok {
			t.Fatal("export polluted image cache")
		}
	}
}

func TestExportImageFallbackCacheAndCancellation(t *testing.T) {
	data := []byte("fallback image")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer server.Close()
	store := &ImageStore{dir: t.TempDir(), originURL: server.URL, client: server.Client()}
	for _, empty := range []bool{false, true} {
		id := uuid.NewString()
		if empty {
			path := store.path("initial", id)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0644); err != nil {
				t.Fatal(err)
			}
		}
		var dst bytes.Buffer
		if err := store.writeExportImage(context.Background(), &dst, id, true); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(dst.Bytes(), data) {
			t.Fatal("missing/empty file fallback changed image")
		}
	}
	id := uuid.NewString()
	imageDataCache.put(id, cachedImage{data: data})
	var dst bytes.Buffer
	if err := store.writeExportImage(context.Background(), &dst, id, true); err != nil || !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("cache fallback: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst.Reset()
	if err := store.writeExportImage(ctx, &dst, id, true); !errors.Is(err, context.Canceled) || dst.Len() != 0 {
		t.Fatalf("canceled export wrote data: %v", err)
	}
}
