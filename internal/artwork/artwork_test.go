package artwork

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// solid builds a w×h image filled with c.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestCanonicalCover(t *testing.T) {
	cases := []struct{ in, want string }{
		// a 300px thumbnail is upgraded to the 640px master
		{"https://i.scdn.co/image/ab67616d00001e02abcdef0123456789abcdef01",
			"https://i.scdn.co/image/ab67616d0000b273abcdef0123456789abcdef01"},
		// a 64px thumbnail likewise
		{"https://i.scdn.co/image/ab67616d00004851deadbeefdeadbeefdeadbeef",
			"https://i.scdn.co/image/ab67616d0000b273deadbeefdeadbeefdeadbeef"},
		// already the master -> unchanged
		{"https://i.scdn.co/image/ab67616d0000b273cafecafecafecafecafecafe",
			"https://i.scdn.co/image/ab67616d0000b273cafecafecafecafecafecafe"},
		// non-Spotify URL -> untouched
		{"https://example.com/cover.jpg", "https://example.com/cover.jpg"},
		// artist image (different tag) -> untouched
		{"https://i.scdn.co/image/ab6761610000e5ebabcdef", "https://i.scdn.co/image/ab6761610000e5ebabcdef"},
	}
	for _, c := range cases {
		if got := canonicalCover(c.in); got != c.want {
			t.Errorf("canonicalCover(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHalfBlockDimensions(t *testing.T) {
	img := solid(8, 8, color.RGBA{10, 20, 30, 255})
	lines := HalfBlock(img, 5, 3)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, ln := range lines {
		if n := strings.Count(ln, "▀"); n != 5 {
			t.Errorf("line %d: %d half-blocks, want 5", i, n)
		}
		if !strings.HasSuffix(ln, "\x1b[0m") {
			t.Errorf("line %d: not reset-terminated", i)
		}
	}
}

// TestHalfBlockColors checks the top pixel becomes the foreground and the
// bottom pixel the background, by box-averaging a 2×2 quadrant into one cell.
func TestHalfBlockColors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255}) // top-left  red
	img.Set(1, 0, color.RGBA{0, 255, 0, 255}) // top-right green
	img.Set(0, 1, color.RGBA{0, 0, 255, 255}) // bot-left  blue
	img.Set(1, 1, color.RGBA{255, 255, 255, 255})

	lines := HalfBlock(img, 1, 1)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	// top row averaged: (red+green)/2 = (127,127,0); bottom: (blue+white)/2 = (127,127,255)
	if !strings.Contains(lines[0], "38;2;127;127;0") {
		t.Errorf("foreground not top-row average: %q", lines[0])
	}
	if !strings.Contains(lines[0], "48;2;127;127;255") {
		t.Errorf("background not bottom-row average: %q", lines[0])
	}
}

func TestHalfBlockGuards(t *testing.T) {
	if HalfBlock(nil, 4, 4) != nil {
		t.Error("nil image should render nothing")
	}
	if HalfBlock(solid(4, 4, color.RGBA{}), 0, 4) != nil {
		t.Error("zero width should render nothing")
	}
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestGetFetchAndCache(t *testing.T) {
	want := solid(3, 3, color.RGBA{1, 2, 3, 255})
	body := pngBytes(t, want)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(body)
	}))
	dir := t.TempDir()

	img, err := Get(context.Background(), srv.URL, dir)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if got := img.Bounds().Dx(); got != 3 {
		t.Errorf("decoded width %d, want 3", got)
	}
	if hits != 1 {
		t.Fatalf("expected 1 network hit, got %d", hits)
	}
	// the cache file should now exist
	if _, err := os.Stat(cacheFile(dir, srv.URL)); err != nil {
		t.Errorf("cache not written: %v", err)
	}

	// second call serves from cache even with the server down
	srv.Close()
	if _, err := Get(context.Background(), srv.URL, dir); err != nil {
		t.Errorf("cached Get after server close: %v", err)
	}
	if hits != 1 {
		t.Errorf("cache miss: network was hit %d times", hits)
	}
}

func TestGetHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := Get(context.Background(), srv.URL, t.TempDir()); err == nil {
		t.Error("expected error on 404")
	}
}

func TestGetCorruptCacheRefetches(t *testing.T) {
	want := solid(2, 2, color.RGBA{9, 9, 9, 255})
	body := pngBytes(t, want)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	// seed a garbage cache entry; Get must ignore it and refetch a valid image
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o600); err == nil {
		os.WriteFile(cacheFile(dir, srv.URL), []byte("not an image"), 0o600)
	}
	img, err := Get(context.Background(), srv.URL, dir)
	if err != nil || img == nil {
		t.Fatalf("refetch on corrupt cache: img=%v err=%v", img, err)
	}
}

// Dominant returns a vivid hue close to a strongly-coloured cover, and reports
// ok=false for a greyscale one (so the UI keeps its default accent).
func TestDominant(t *testing.T) {
	if _, ok := Dominant(nil); ok {
		t.Error("nil image should report no dominant colour")
	}

	// a saturated red field -> red wins (R clearly the largest channel)
	c, ok := Dominant(solid(40, 40, color.RGBA{210, 30, 30, 255}))
	if !ok {
		t.Fatal("a red cover should yield a dominant colour")
	}
	if !(c.R > c.G+40 && c.R > c.B+40) {
		t.Errorf("red cover -> %v, want red-dominant", c)
	}

	// pure grey carries no hue -> no tint
	if _, ok := Dominant(solid(40, 40, color.RGBA{128, 128, 128, 255})); ok {
		t.Error("a grey cover should report no usable hue")
	}

	// a small vivid splash on a near-grey field still wins on saturation weight
	img := solid(40, 40, color.RGBA{120, 122, 124, 255})
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{30, 60, 220, 255}) // blue corner
		}
	}
	if c, ok := Dominant(img); !ok || c.B <= c.R || c.B <= c.G {
		t.Errorf("blue splash should win on saturation: %v ok=%v", c, ok)
	}
}

// Ghost dims and desaturates: a vivid cover becomes darker and far less colourful.
func TestGhost(t *testing.T) {
	g := Ghost(solid(8, 8, color.RGBA{220, 40, 40, 255}))
	r, gg, b, _ := g.At(0, 0).RGBA()
	r8, g8, b8 := int(r>>8), int(gg>>8), int(b>>8)
	if r8 >= 220 {
		t.Errorf("ghost not dimmed: R=%d, want < 220", r8)
	}
	max, min := r8, r8
	for _, c := range []int{g8, b8} {
		if c > max {
			max = c
		}
		if c < min {
			min = c
		}
	}
	if max-min > 120 { // source spread was 180; ghost must be much greyer
		t.Errorf("ghost not desaturated: channel spread %d, want <= 120", max-min)
	}
}
