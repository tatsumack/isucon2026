package isureal

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestReadAllSizedPreservesBodyAndErrors(t *testing.T) {
	body := bytes.Repeat([]byte("image"), 2000)
	for _, size := range []int64{-1, 0, 1, int64(len(body)), int64(len(body) + 1000), 1 << 40} {
		got, err := readAllSized(iotest.OneByteReader(bytes.NewReader(body)), size)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("size %d: len=%d err=%v", size, len(got), err)
		}
	}
	sentinel := errors.New("read failed")
	got, err := readAllSized(io.MultiReader(bytes.NewReader(body), iotest.ErrReader(sentinel)), int64(len(body)))
	if !errors.Is(err, sentinel) || !bytes.Equal(got, body) {
		t.Fatalf("lost partial body or error: len=%d err=%v", len(got), err)
	}
}
