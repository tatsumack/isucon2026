package isureal

import (
	"bytes"
	"io"
	"sync"
)

// アップロードされた画像を読むバッファを使い回す。io.ReadAll や multipart の解析は、
// 1枚ごとに確保と拡張 (確保した領域の0埋めとコピー) を繰り返すため、CPU を多く使っていた。
// バッファの中身は、ハンドラが返る前に使い終わる (ファイルへの書き込みと DB への INSERT はどちらも同期で、スライスを持ち続けない)。
var uploadBufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// 不正なリクエストで確保した大きなバッファは使い回さない
const maxPooledUploadBuffer = 8 << 20

// readUpload は r を使い回しのバッファに読む。sizeHint (Content-Length など) が正なら先に確保して、読みながら拡張しない。
// 使い終わったら releaseUpload でバッファを返す。
func readUpload(r io.Reader, sizeHint int64) (*bytes.Buffer, error) {
	buf := uploadBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	if sizeHint > 0 && sizeHint <= maxPooledUploadBuffer {
		// ReadFrom は読む前に最低 512 バイトの空きを求めるので、その分も確保する
		buf.Grow(int(sizeHint) + bytes.MinRead)
	}
	if _, err := buf.ReadFrom(r); err != nil {
		releaseUpload(buf)
		return nil, err
	}
	return buf, nil
}

func releaseUpload(buf *bytes.Buffer) {
	if buf.Cap() > maxPooledUploadBuffer {
		return
	}
	uploadBufferPool.Put(buf)
}
