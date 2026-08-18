// Hostile-input fuzzing for the cover-image decode path: the bytes come from
// an arbitrary HTTP endpoint named by the device, so decode must reject
// dimension bombs and never panic, and the downstream rasterizers must digest
// whatever it accepts.

package artwork

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func fuzzSeedImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := range 6 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{uint8(x * 32), uint8(y * 42), 0x80, 0xff})
		}
	}
	return img
}

func FuzzDecode(f *testing.F) {
	img := fuzzSeedImage()
	var pngBuf, jpgBuf, gifBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	_ = jpeg.Encode(&jpgBuf, img, nil)
	_ = gif.Encode(&gifBuf, img, nil)
	f.Add(pngBuf.Bytes())
	f.Add(jpgBuf.Bytes())
	f.Add(gifBuf.Bytes())
	f.Add(pngBuf.Bytes()[:len(pngBuf.Bytes())/2]) // truncated
	f.Add([]byte("GIF89a"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		img, err := decode(raw)
		if err != nil {
			if img != nil {
				t.Fatal("decode returned an image alongside an error")
			}
			return
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		if w <= 0 || h <= 0 || w > maxArtPixels/h {
			t.Fatalf("decode accepted out-of-cap dimensions %dx%d", w, h)
		}
		if w*h > 1<<16 {
			return // keep iterations cheap; the cap invariant above still ran
		}
		// downstream consumers of an accepted image must hold up too
		lines := HalfBlock(img, 12, 6)
		if len(lines) != 6 {
			t.Fatalf("HalfBlock returned %d lines", len(lines))
		}
		_, _ = Dominant(img)
		_ = Ghost(img)
	})
}
