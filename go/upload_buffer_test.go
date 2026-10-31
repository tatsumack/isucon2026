package isureal

import (
	"bytes"
	"testing"
)

func TestReadUploadLargeHintAndMismatchedLength(t *testing.T) {
	data := bytes.Repeat([]byte{0x12, 0x34}, (9<<20)/2)
	for _, hint := range []int64{int64(len(data)), int64(len(data)) - 1024, int64(len(data)) + 1024, -1} {
		buf, err := readUpload(bytes.NewReader(data), hint)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf.Bytes(), data) {
			t.Fatalf("hint %d changed/truncated body", hint)
		}
		if hint == int64(len(data)) && buf.Cap() >= 2*len(data) {
			t.Fatalf("large body grew geometrically: size=%d cap=%d", len(data), buf.Cap())
		}
		releaseUpload(buf)
	}
}
