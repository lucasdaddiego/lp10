package artwork

import (
	"image"
	"image/color"
	"testing"
)

func benchCover(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 3), uint8(y * 5), uint8((x + y) * 2), 0xff})
		}
	}
	return img
}

func BenchmarkHalfBlock(b *testing.B) {
	img := benchCover(640, 640)
	b.ReportAllocs()
	for b.Loop() {
		sink = HalfBlock(img, 30, 16)
	}
}

func BenchmarkDominant(b *testing.B) {
	img := benchCover(640, 640)
	b.ReportAllocs()
	for b.Loop() {
		sinkC, sinkB = Dominant(img)
	}
}

func BenchmarkGhost(b *testing.B) {
	img := benchCover(640, 640)
	b.ReportAllocs()
	for b.Loop() {
		sinkImg = Ghost(img)
	}
}

func BenchmarkDownscale(b *testing.B) {
	img := benchCover(640, 640)
	b.ReportAllocs()
	for b.Loop() {
		sinkImg = downscale(img, 48, 48)
	}
}

func BenchmarkKittyImage(b *testing.B) {
	img := benchCover(640, 640)
	b.ReportAllocs()
	for b.Loop() {
		sinkS, sink = KittyImage(img, 30, 16, 1981, 300, 320)
	}
}

func BenchmarkRGBToHSL(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkF, _, _ = RGBToHSL(0x40, 0xa0, 0xe0)
	}
}

var (
	sink    []string
	sinkS   string
	sinkC   color.RGBA
	sinkB   bool
	sinkF   float64
	sinkImg image.Image
)
