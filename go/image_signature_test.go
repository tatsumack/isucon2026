package isureal

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"strconv"
	"testing"
)

// 変更前の実装 (crypto/hmac をそのまま繰り返す)。computeImageSignature と結果が一致することの確認に使う。
func referenceImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	mac := hmac.New(sha256.New, key)
	for range imageSignatureIterations {
		mac.Reset()
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func TestComputeImageSignatureMatchesHMAC(t *testing.T) {
	for _, keyLen := range []int{0, 1, 32, 63, 64, 65, 100} {
		for i := range 5 {
			key := make([]byte, keyLen)
			rand.Read(key)
			imageID := rand.Text()
			exp := int64(1793415600 + i*60)
			got := computeImageSignature(key, imageID, exp)
			want := referenceImageSignature(key, imageID, exp)
			if !bytes.Equal(got, want) {
				t.Fatalf("keyLen=%d imageID=%s exp=%d: got %x, want %x", keyLen, imageID, exp, got, want)
			}
		}
	}
}

func BenchmarkComputeImageSignature(b *testing.B) {
	key := make([]byte, 32)
	rand.Read(key)
	for b.Loop() {
		computeImageSignature(key, "0f8fad5b-d9cb-469f-a165-70867728950e", 1793415600)
	}
}

func BenchmarkReferenceImageSignature(b *testing.B) {
	key := make([]byte, 32)
	rand.Read(key)
	for b.Loop() {
		referenceImageSignature(key, "0f8fad5b-d9cb-469f-a165-70867728950e", 1793415600)
	}
}
