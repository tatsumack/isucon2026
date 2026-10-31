package isureal

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const imageURLLifetime = 24 * time.Hour
const imageSignatureIterations = 1000

// 同じ期限の署名を使い回せるよう、期限を時間単位に切り捨てる。期限は常に発行から23時間以上先になる。
// 署名は1回に約0.4ms (asia1) かかり、期限が1分ごとに変わると、Memories や export で同じ画像の署名を何度も計算していた。
const imageURLExpirationStep = time.Hour

func imageSigningKey(secretKeyHash string) ([]byte, error) {
	key, err := hex.DecodeString(secretKeyHash)
	if err != nil || len(key) != sha256.Size {
		return nil, fmt.Errorf("invalid image signing key")
	}
	return key, nil
}

type imageSignatureCacheEntry struct {
	exp int64
	sig []byte
}

// 署名は (鍵, 画像ID, 期限) だけで決まる純粋な計算なので、(鍵, 画像ID) ごとに直近に発行した期限の結果を保持する。
// 入力が同じなら結果も同じため、無効化は不要 (initialize後も誤った結果にならない)。
var imageSignatureCache sync.Map

// 検証用。発行時のキャッシュに同じ期限があれば使い、なければ計算する (古い期限で発行用のエントリを上書きしない)。
func imageSignature(key []byte, imageID string, exp int64) []byte {
	if sig, ok := cachedImageSignature(key, imageID, exp); ok {
		return sig
	}
	return computeImageSignature(key, imageID, exp)
}

func issueImageSignature(key []byte, imageID string, exp int64) []byte {
	if sig, ok := cachedImageSignature(key, imageID, exp); ok {
		return sig
	}
	sig := computeImageSignature(key, imageID, exp)
	storeImageSignature(key, imageID, exp, sig)
	return sig
}

func storeImageSignature(key []byte, imageID string, exp int64, sig []byte) {
	imageSignatureCache.Store(string(key)+":"+imageID, imageSignatureCacheEntry{exp: exp, sig: sig})
}

func cachedImageSignature(key []byte, imageID string, exp int64) ([]byte, bool) {
	cached, ok := imageSignatureCache.Load(string(key) + ":" + imageID)
	if !ok {
		return nil, false
	}
	entry := cached.(imageSignatureCacheEntry)
	return entry.sig, entry.exp == exp
}

// HMAC-SHA256 を imageSignatureIterations 回繰り返す。結果は crypto/hmac で繰り返した場合と同じ。
// crypto/hmac は1回ごとに出力の確保と状態の作り直しをするため、鍵を混ぜた直後 (ipad / opad) の状態を
// 1度だけ作って毎回戻し、出力は同じバッファに書き込む。
func computeImageSignature(key []byte, imageID string, exp int64) []byte {
	if len(key) > sha256.BlockSize {
		sum := sha256.Sum256(key)
		key = sum[:]
	}
	var ipad, opad [sha256.BlockSize]byte
	copy(ipad[:], key)
	copy(opad[:], key)
	for i := range ipad {
		ipad[i] ^= 0x36
		opad[i] ^= 0x5c
	}
	inner, outer := sha256.New(), sha256.New()
	inner.Write(ipad[:])
	outer.Write(opad[:])
	innerState, err := inner.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		panic(err)
	}
	outerState, err := outer.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		panic(err)
	}
	innerRestore := inner.(encoding.BinaryUnmarshaler)
	outerRestore := outer.(encoding.BinaryUnmarshaler)

	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	var buf [sha256.Size]byte
	for range imageSignatureIterations {
		innerRestore.UnmarshalBinary(innerState)
		inner.Write(value)
		sum := inner.Sum(buf[:0])
		outerRestore.UnmarshalBinary(outerState)
		outer.Write(sum)
		value = outer.Sum(buf[:0])
	}
	return bytes.Clone(value)
}

func imageURLExpiration(now time.Time) int64 {
	return now.Add(imageURLLifetime).Truncate(imageURLExpirationStep).Unix()
}

func signedImageURL(user User, imageID string) string {
	exp := imageURLExpiration(time.Now())
	query := url.Values{
		"exp": {strconv.FormatInt(exp, 10)},
		"sig": {base64.RawURLEncoding.EncodeToString(issueImageSignature(user.ImageKey, imageID, exp))},
	}
	return "/api/images/" + url.PathEscape(imageID) + "?" + query.Encode()
}
