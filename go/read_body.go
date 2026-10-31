package isureal

import (
	"bytes"
	"io"
)

// 大きさの見込みとして信じる上限。nginx の client_max_body_size (32m) と揃える。
const maxReadSizeHint = 32 << 20

// readAllSized は、大きさの見込み (multipart のファイルの Size や Content-Length) の分だけ先に確保してから読む。
// io.ReadAll は 512 バイトから倍々に確保し直すため、数百KBの画像では確保とゼロ埋め・コピーが何度も起きる (#18039 の asia1 で memclr 8.3%)。
// 見込みが無い・上限を超える場合は io.ReadAll と同じ。見込みより大きくても、足りない分は bytes.Buffer が伸ばすので結果は変わらない。
func readAllSized(r io.Reader, sizeHint int64) ([]byte, error) {
	if sizeHint <= 0 || sizeHint > maxReadSizeHint {
		return io.ReadAll(r)
	}
	// ReadFrom は読むたびに MinRead バイトの空きを求めるので、その分も足しておく (足りないと最後に倍に確保し直す)
	buf := bytes.NewBuffer(make([]byte, 0, sizeHint+bytes.MinRead))
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
