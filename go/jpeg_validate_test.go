package isureal

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
)

func encodeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestValidJPEGMatchesFullDecode(t *testing.T) {
	ok := encodeTestJPEG(t, 300, 200)
	big := encodeTestJPEG(t, 600, 100)
	cases := map[string]struct {
		data []byte
		max  int
	}{
		"valid":            {ok, 512},
		"too large":        {big, 512},
		"empty":            {nil, 512},
		"not jpeg":         {[]byte("GIF89a......"), 512},
		"truncated header": {ok[:20], 512},
		"truncated scan":   {ok[:len(ok)/2], 512},
		"missing EOI":      {ok[:len(ok)-2], 512},
		"trailing bytes":   {append(append([]byte{}, ok...), 0, 1, 2), 512},
	}
	for name, tc := range cases {
		img, err := jpeg.Decode(bytes.NewReader(tc.data))
		want := err == nil && img.Bounds().Dx() <= tc.max && img.Bounds().Dy() <= tc.max
		if got := validJPEG(tc.data, tc.max); got != want {
			t.Errorf("%s: validJPEG = %v, full decode = %v (err=%v)", name, got, want, err)
		}
	}
}
