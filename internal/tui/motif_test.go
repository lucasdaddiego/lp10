package tui

import (
	"math"
	"regexp"
	"testing"
)

// refMotifRGB is the ORIGINAL motif colour math — pure math.Sin, no separable
// precompute, no LUT — kept as the reference the optimized motifBlock is pinned
// against. It returns the (r,g,b) for one cell exactly as the pre-optimization
// implementation painted it.
func refMotifRGB(x, y, frame int) (uint8, uint8, uint8) {
	ph := float64(frame) * 0.16
	const warpAmp = 0.9
	fx, fy := float64(x), float64(y)
	wx := math.Sin(fy*0.30+ph*0.6) + 0.5*math.Sin(fx*0.22-ph*0.35)
	wy := math.Sin(fx*0.27-ph*0.5) + 0.5*math.Sin(fy*0.24+ph*0.45)
	ax, ay := fx+warpAmp*wx, fy+warpAmp*wy
	v := math.Sin(ax*0.55+ph) + math.Sin(ay*0.75-ph*0.8) + math.Sin((ax+ay)*0.42+ph*1.3)
	n := (v + 3) / 6
	hue := math.Mod(ph*57.29578+
		74*math.Sin(ax*0.20+ay*0.16+ph*0.5)+
		24*math.Sin((ax-ay)*0.17-ph*0.35)+360, 360)
	return hslRGB(hue, 0.70+0.18*n, 0.20+0.40*n)
}

var reSGRColor = regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+)m`)

// The fastSin/separable rewrite of motifBlock may differ from the math.Sin
// reference by AT MOST one quantization step per channel (the LUT's ~3e-7
// error is three orders of magnitude below 1/255 — a boundary-rounding cell is
// the only way to see even ±1). Anything larger means the LUT or the
// separable precompute broke the field.
func TestMotifMatchesMathSin(t *testing.T) {
	th := newTheme()
	atoi := func(s string) int {
		n := 0
		for _, c := range s {
			n = n*10 + int(c-'0')
		}
		return n
	}
	for _, frame := range []int{0, 3, 100, 997, 12345} {
		for _, dim := range [][2]int{{30, 16}, {41, 7}} {
			w, h := dim[0], dim[1]
			lines := th.motifBlock(w, h, frame)
			for y, ln := range lines {
				cells := reSGRColor.FindAllStringSubmatch(ln, -1)
				if len(cells) != w {
					t.Fatalf("frame %d %dx%d row %d: %d colour cells, want %d", frame, w, h, y, len(cells), w)
				}
				for x, c := range cells {
					wr, wg, wb := refMotifRGB(x, y, frame)
					gr, gg, gb := atoi(c[1]), atoi(c[2]), atoi(c[3])
					if d := max(absInt(gr-int(wr)), absInt(gg-int(wg)), absInt(gb-int(wb))); d > 1 {
						t.Fatalf("frame %d cell (%d,%d): got %d,%d,%d want %d,%d,%d (Δ%d)",
							frame, x, y, gr, gg, gb, wr, wg, wb, d)
					}
				}
			}
		}
	}
}

// fastSin itself must track math.Sin to LUT precision over many periods,
// including negative arguments (the motif's phase terms go negative).
func TestFastSinAccuracy(t *testing.T) {
	const tol = 5e-7 // interpolation bound ≈2.9e-7, with headroom
	for x := -50.0; x < 50; x += 0.0137 {
		if d := math.Abs(fastSin(x) - math.Sin(x)); d > tol {
			t.Fatalf("fastSin(%v) off by %g (tol %g)", x, d, tol)
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
