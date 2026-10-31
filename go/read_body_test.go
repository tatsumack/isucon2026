package isureal

import (
	"bytes"
	"testing"
)

func TestReadAllSizedMatchesReadAll(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 50_000)
	cases := map[string]int64{
		"exact":     int64(len(data)),
		"too small": 100,
		"too large": int64(len(data)) * 2,
		"no hint":   0,
		"unknown":   -1,
		"over cap":  maxReadSizeHint + 1,
	}
	for name, hint := range cases {
		got, err := readAllSized(bytes.NewReader(data), hint)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%s: got %d bytes, want %d", name, len(got), len(data))
		}
	}
}

func TestReadAllSizedDoesNotGrowWithExactHint(t *testing.T) {
	data := bytes.Repeat([]byte{1}, 430_000)
	got, err := readAllSized(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if cap(got) != len(data)+bytes.MinRead {
		t.Fatalf("buffer was reallocated: cap %d", cap(got))
	}
}
