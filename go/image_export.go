package isureal

import (
	"context"
	"io"
)

// エクスポートで読んだ画像本体を、画像IDごとに持つ。imageDataCache (投稿直後の画像用) とは分け、追い出し合わないようにする。
// 同じユーザーが続けてエクスポートすることが多い (#33924 で 233回中 144回が同じユーザーの2回目以降、間隔の中央値 2.9s)。
// 画像IDはサーバが採番するUUIDで中身が変わらないため、無効化しない。
// 削除した投稿の画像は、エクスポートの SQL (deleted_at IS NULL) で対象から外れるため返らない。
// /initialize (ImageStore.Reset) で空にする。
var exportImageCache = newByteCache(256 << 20)

// export の SQL で確認済みの画像だけを渡す。初期画像は new/ の存在確認を省略。
// 大量の過去画像を通常の画像キャッシュ (imageDataCache) へ入れず、エクスポート用のキャッシュに入れる。
// ZIP全体はハンドラー内で保持するので、読み取り失敗時に途中の200応答を返すことはない。
func (s *ImageStore) writeExportImage(ctx context.Context, dst io.Writer, id string, initial bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cached, ok := imageDataCache.get(id); ok {
		_, err := dst.Write(cached.data)
		return err
	}
	if cached, ok := exportImageCache.get(id); ok {
		_, err := dst.Write(cached.data)
		return err
	}
	kind := "new"
	if initial {
		kind = "initial"
	}
	data, ok := readImageFile(s.path(kind, id))
	if !ok {
		// 未抽出の初期画像・ローカルに無い画像は、従来どおりDB/転送元から取得する。
		var err error
		if data, _, err = s.LoadWithoutCaching(ctx, id); err != nil {
			return err
		}
	}
	exportImageCache.put(id, cachedImage{data: data})
	_, err := dst.Write(data)
	return err
}
