package artwork

import (
	"image"
	"image/color"
	"testing"
)

// rgbaOf underpins every pixel loop in this package: the loops read its Pix
// bytes directly instead of calling img.At(). This pins the two properties that
// makes that safe — a zero-based *image.RGBA passes through untouched (so no
// copy is paid on the common path), and any other source converts to the same
// pixels the At() path would have produced.
//
// The tolerance is 0 for every 8-bit-per-channel format; a YCbCr source (JPEG
// covers) is allowed 1/255 because image/draw uses the standard 8-bit
// color.YCbCrToRGB conversion where color.YCbCr.RGBA() carried a 16-bit one.
func TestRGBAOfMatchesAtAndAvoidsCopies(t *testing.T) {
	const w, h = 24, 18
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	gray := image.NewGray(image.Rect(0, 0, w, h))
	offset := image.NewRGBA(image.Rect(7, 11, 7+w, 11+h))
	ycbcr := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := range h {
		for x := range w {
			c := color.RGBA{uint8(x*7 + y), uint8(y*5 + 30), uint8((x*y)%251 + 3), 0xff}
			rgba.Set(x, y, c)
			nrgba.Set(x, y, color.NRGBA{c.R, c.G, c.B, uint8(40 + (x+y)%216)})
			gray.Set(x, y, color.Gray{uint8(x*3 + y*2)})
			offset.Set(7+x, 11+y, c)
		}
	}
	for i := range ycbcr.Y {
		ycbcr.Y[i] = uint8(i * 3)
	}
	for i := range ycbcr.Cb {
		ycbcr.Cb[i], ycbcr.Cr[i] = uint8(i*5+20), uint8(i*7+60)
	}

	if rgbaOf(rgba) != rgba {
		t.Error("a zero-based *image.RGBA should pass through without a copy")
	}
	if got := rgbaOf(offset); got == any(offset) || got.Rect.Min != (image.Point{}) {
		t.Error("a non-zero-origin source should be converted to zero-based")
	}

	for _, c := range []struct {
		name string
		src  image.Image
		tol  int
	}{{"RGBA", rgba, 0}, {"NRGBA", nrgba, 0}, {"Gray", gray, 0}, {"offset", offset, 0}, {"YCbCr", ycbcr, 1}} {
		conv := rgbaOf(c.src)
		b := c.src.Bounds()
		for y := range h {
			for x := range w {
				wr, wg, wb, _ := c.src.At(b.Min.X+x, b.Min.Y+y).RGBA()
				i := conv.PixOffset(x, y)
				for ch, want := range [3]uint32{wr >> 8, wg >> 8, wb >> 8} {
					if d := int(conv.Pix[i+ch]) - int(want); d > c.tol || d < -c.tol {
						t.Fatalf("%s at (%d,%d) channel %d: got %d want %d (tol %d)",
							c.name, x, y, ch, conv.Pix[i+ch], want, c.tol)
					}
				}
			}
		}
	}
}
