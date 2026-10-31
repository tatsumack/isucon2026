package isureal

import (
	"context"
	"io"
	"os"
	"sync"
)

var exportCopyBuffers = sync.Pool{New: func() any {
	b := make([]byte, 64<<10)
	return &b
}}

// export の SQL で確認済みの画像だけを渡す。初期画像は new/ の存在確認を省略。
// 大量の過去画像を通常の画像キャッシュへ入れず、ファイルから ZIP writer へ直接コピーする。
// ZIP全体はハンドラー内で保持するので、読み取り失敗時に途中の200応答を返すことはない。
func (s *ImageStore) writeExportImage(ctx context.Context, dst io.Writer, id string, initial bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cached, ok := imageDataCache.get(id); ok {
		_, err := dst.Write(cached.data)
		return err
	}
	kind := "new"
	if initial {
		kind = "initial"
	}
	f, err := os.Open(s.path(kind, id))
	if err == nil {
		defer f.Close()
		info, statErr := f.Stat()
		if statErr == nil && info.Size() > 0 {
			buffer := exportCopyBuffers.Get().(*[]byte)
			defer exportCopyBuffers.Put(buffer)
			// os.File.WriteTo に迂回せず、共有バッファを使う。
			_, err := io.CopyBuffer(dst, struct{ io.Reader }{f}, *buffer)
			return err
		}
	}
	// 未抽出の初期画像・ローカルに無い画像は、従来どおりDB/転送元から取得する。
	data, _, err := s.LoadWithoutCaching(ctx, id)
	if err != nil {
		return err
	}
	_, err = dst.Write(data)
	return err
}
