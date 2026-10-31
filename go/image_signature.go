package isureal

import (
	"crypto/hmac"
	"crypto/sha256"
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

// 同じ期限の署名を使い回せるよう、期限を分単位に切り捨てる。期限は常に発行から23時間59分以上先になる。
const imageURLExpirationStep = time.Minute

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
	imageSignatureCache.Store(string(key)+":"+imageID, imageSignatureCacheEntry{exp: exp, sig: sig})
	return sig
}

func cachedImageSignature(key []byte, imageID string, exp int64) ([]byte, bool) {
	cached, ok := imageSignatureCache.Load(string(key) + ":" + imageID)
	if !ok {
		return nil, false
	}
	entry := cached.(imageSignatureCacheEntry)
	return entry.sig, entry.exp == exp
}

func computeImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	mac := hmac.New(sha256.New, key)
	for range imageSignatureIterations {
		mac.Reset()
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func signedImageURL(user User, imageID string) string {
	exp := time.Now().Add(imageURLLifetime).Truncate(imageURLExpirationStep).Unix()
	query := url.Values{
		"exp": {strconv.FormatInt(exp, 10)},
		"sig": {base64.RawURLEncoding.EncodeToString(issueImageSignature(user.ImageKey, imageID, exp))},
	}
	return "/api/images/" + url.PathEscape(imageID) + "?" + query.Encode()
}
