package isureal

import (
	"bytes"
	"image/jpeg"
)

// validJPEG は画像全体を展開せずに、JPEG として読めることと縦横が maxSize 以下であることを確認する。
// jpeg.Decode は1枚で数十msかかるため、ヘッダ (DecodeConfig) とマーカーの並びだけを見る。
// 画像データの中身の破損は検出できないため、不正な画像で BAN を判定するプロフィール画像には使わない。
func validJPEG(data []byte, maxSize int) bool {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width > maxSize || cfg.Height > maxSize {
		return false
	}
	// SOS (スキャン開始) まで各セグメントをたどり、その後ろに EOI (終了) があることを確認する。
	// 画像データ内の 0xFF はバイトスタッフィング (FF 00) されるため、FF D9 は EOI にしか現れない。
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return false
		}
		marker := data[i+1]
		if marker == 0xFF {
			i++
			continue
		}
		if marker == 0xDA {
			return bytes.Contains(data[i:], []byte{0xFF, 0xD9})
		}
		i += 2 + int(data[i+2])<<8 + int(data[i+3])
	}
	return false
}
