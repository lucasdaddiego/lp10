package artwork

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestKittyDiacriticsTable(t *testing.T) {
	if len(kittyDiacritics) != 297 {
		t.Fatalf("diacritic table length %d, want 297", len(kittyDiacritics))
	}
	if kittyDiacritics[0] != 0x0305 || kittyDiacritics[len(kittyDiacritics)-1] != 0x1D244 {
		t.Errorf("table endpoints wrong: [0]=%U last=%U", kittyDiacritics[0], kittyDiacritics[len(kittyDiacritics)-1])
	}
}

func TestKittyImageStructure(t *testing.T) {
	// Use the production id (>255): it must ride a 24-bit RGB foreground, not a
	// 256-color index (which only addresses 0–255). 1981 = 0x0007BD -> 0;7;189.
	const id = 1981
	transmit, lines := KittyImage(solid(40, 40, color.RGBA{200, 50, 50, 255}), 12, 6, id, 0, 0)
	if len(lines) != 6 {
		t.Fatalf("got %d placeholder lines, want 6", len(lines))
	}
	if !strings.HasPrefix(transmit, "\x1b_G") || !strings.HasSuffix(transmit, "\x1b\\") {
		t.Fatalf("transmit not APC-framed")
	}
	for _, want := range []string{"a=T", "U=1", "i=1981", "c=12", "r=6", "f=100", "q=2"} {
		if !strings.Contains(transmit, want) {
			t.Errorf("transmit missing control key %q", want)
		}
	}
	for i, ln := range lines {
		if n := strings.Count(ln, string(rune(KittyPlaceholder))); n != 12 {
			t.Errorf("line %d: %d placeholder cells, want 12", i, n)
		}
		if !strings.Contains(ln, "\x1b[38;2;0;7;189m") {
			t.Errorf("line %d: image id not in a 24-bit foreground (got %q)", i, ln)
		}
		if strings.Contains(ln, "\x1b[38;5;") {
			t.Errorf("line %d: id must not use the 256-color form (can't address ids >255)", i)
		}
		if !strings.HasSuffix(ln, "\x1b[39m") {
			t.Errorf("line %d: foreground not reset", i)
		}
	}
	// Each cell is placeholder + ROW diacritic + COLUMN diacritic. Assert the exact
	// per-cell ordering so a swapped/constant row diacritic can't pass via the
	// column diacritic (which alone would also match a bare Contains check).
	ph := string(rune(KittyPlaceholder))
	row1col0 := ph + string(kittyDiacritics[1]) + string(kittyDiacritics[0])
	if !strings.Contains(lines[1], row1col0) {
		t.Error("row 1, col 0 cell not encoded as placeholder+diacritic[1]+diacritic[0]")
	}
	row0col1 := ph + string(kittyDiacritics[0]) + string(kittyDiacritics[1])
	if !strings.Contains(lines[0], row0col1) {
		t.Error("row 0, col 1 cell not encoded as placeholder+diacritic[0]+diacritic[1]")
	}
}

func TestKittyImagePayloadIsPNG(t *testing.T) {
	transmit, _ := KittyImage(solid(8, 8, color.RGBA{10, 20, 30, 255}), 4, 4, 1, 0, 0)
	raw, err := base64.StdEncoding.DecodeString(extractKittyPayload(transmit))
	if err != nil {
		t.Fatalf("payload not valid base64: %v", err)
	}
	if len(raw) < 8 || string(raw[1:4]) != "PNG" {
		t.Errorf("payload is not a PNG (got % x)", raw[:min(8, len(raw))])
	}
}

func TestKittyImageGuards(t *testing.T) {
	if tr, ls := KittyImage(nil, 12, 6, 1, 0, 0); tr != "" || ls != nil {
		t.Error("nil image should render nothing")
	}
	if tr, ls := KittyImage(solid(4, 4, color.RGBA{}), len(kittyDiacritics)+1, 1, 1, 0, 0); tr != "" || ls != nil {
		t.Error("a box wider than the diacritic table should render nothing")
	}
}

// decodeKittyPNG pulls the transmitted PNG back out and decodes its config, so a
// test can assert the image's pixel dimensions.
func decodeKittyPNG(t *testing.T, transmit string) image.Config {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(extractKittyPayload(transmit))
	if err != nil {
		t.Fatalf("payload not base64: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("payload not a decodable image: %v", err)
	}
	return cfg
}

// With a known footprint the transmitted PNG must be sized to it exactly (an
// undersized source is enlarged, an oversized one reduced), so a virtual
// placement — drawn at native pixels — fills its cell box with no gap or clip.
func TestKittyImageSizedToFootprint(t *testing.T) {
	// source smaller than the footprint -> upscaled to fill it
	up, _ := KittyImage(solid(40, 40, color.RGBA{200, 50, 50, 255}), 12, 6, 1, 360, 300)
	if c := decodeKittyPNG(t, up); c.Width != 360 || c.Height != 300 {
		t.Errorf("upscaled PNG = %dx%d, want 360x300", c.Width, c.Height)
	}
	// source larger than the footprint -> downscaled to it
	down, _ := KittyImage(solid(800, 800, color.RGBA{20, 200, 90, 255}), 12, 6, 1, 240, 240)
	if c := decodeKittyPNG(t, down); c.Width != 240 || c.Height != 240 {
		t.Errorf("downscaled PNG = %dx%d, want 240x240", c.Width, c.Height)
	}
	// a giant footprint is capped so the payload can't blow up
	big, _ := KittyImage(solid(40, 40, color.RGBA{}), 12, 6, 1, 9000, 9000)
	if c := decodeKittyPNG(t, big); c.Width > kittyMaxPx || c.Height > kittyMaxPx {
		t.Errorf("footprint not capped: %dx%d (max %d)", c.Width, c.Height, kittyMaxPx)
	}
	// unknown cell size (0) -> longest-edge fallback enlarges to kittyMinPx
	fb, _ := KittyImage(solid(40, 40, color.RGBA{}), 12, 6, 1, 0, 0)
	if c := decodeKittyPNG(t, fb); c.Width != kittyMinPx || c.Height != kittyMinPx {
		t.Errorf("fallback PNG = %dx%d, want %d square", c.Width, c.Height, kittyMinPx)
	}
}

// resample must enlarge without crashing and preserve a flat colour (a solid
// source stays solid after bilinear interpolation).
func TestResamplePreservesSolid(t *testing.T) {
	out := resample(solid(10, 10, color.RGBA{120, 60, 200, 255}), 47, 33)
	if b := out.Bounds(); b.Dx() != 47 || b.Dy() != 33 {
		t.Fatalf("resample size = %dx%d, want 47x33", b.Dx(), b.Dy())
	}
	for _, p := range [][2]int{{0, 0}, {23, 16}, {46, 32}} {
		r, g, b, _ := out.At(p[0], p[1]).RGBA()
		if r>>8 != 120 || g>>8 != 60 || b>>8 != 200 {
			t.Errorf("at %v got %d,%d,%d, want 120,60,200", p, r>>8, g>>8, b>>8)
		}
	}
}

// extractKittyPayload concatenates the base64 payloads across all transmit
// chunks (each APC is "\x1b_G<keys>;<payload>\x1b\\").
func extractKittyPayload(transmit string) string {
	var sb strings.Builder
	for _, chunk := range strings.Split(transmit, "\x1b\\") {
		if i := strings.IndexByte(chunk, ';'); i >= 0 {
			sb.WriteString(chunk[i+1:])
		}
	}
	return sb.String()
}
