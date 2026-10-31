package isureal

import (
	"bytes"
	"io"
)

// readAllSized uses size only as an allocation hint, never as a read limit.
// Unknown or excessive sizes use ReadAll to bound speculative allocation.
func readAllSized(r io.Reader, size int64) ([]byte, error) {
	if size <= 0 || size > 32<<20 {
		return io.ReadAll(r)
	}
	var buf bytes.Buffer
	// ReadFrom needs spare room for the final read even at EOF.
	buf.Grow(int(size) + bytes.MinRead)
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}
