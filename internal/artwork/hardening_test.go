package artwork

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Non-http(s) schemes must be rejected up front (no local-file disclosure, no
// arbitrary-protocol fetch) and surface as a deterministic ErrUndecodable.
func TestGetRejectsNonHTTPScheme(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://h/x", "gopher://h", "data:image/png;base64,AAAA", "://nope"} {
		if _, err := Get(context.Background(), u, t.TempDir(), ""); !errors.Is(err, ErrUndecodable) {
			t.Errorf("scheme %q: err=%v, want ErrUndecodable", u, err)
		}
	}
}

// tinyPNGHeader builds a minimal valid PNG signature + IHDR declaring w×h, so
// DecodeConfig reports those dimensions without a full image — enough to test
// the decompression-bomb guard cheaply.
func tinyPNGHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // bit depth 8, color type 2 (truecolor)
	chunk := append([]byte("IHDR"), ihdr...)
	binary.Write(&b, binary.BigEndian, uint32(13))
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestDecodeRejectsOversized(t *testing.T) {
	if _, err := decode(tinyPNGHeader(60000, 60000)); !errors.Is(err, ErrUndecodable) {
		t.Errorf("3.6-gigapixel header: err=%v, want ErrUndecodable", err)
	}
	if _, err := decode(tinyPNGHeader(^uint32(0), ^uint32(0))); !errors.Is(err, ErrUndecodable) {
		t.Errorf("overflowing-dimensions header: err=%v, want ErrUndecodable", err)
	}
	// a normal small image still decodes
	if _, err := decode(pngBytes(t, solid(8, 8, color.RGBA{1, 2, 3, 255}))); err != nil {
		t.Errorf("small image rejected: %v", err)
	}
}

func TestGetUndecodableBytesAreTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("definitely not an image"))
	}))
	defer srv.Close()
	if _, err := Get(context.Background(), srv.URL, t.TempDir(), allowedHost(t, srv.URL)); !errors.Is(err, ErrUndecodable) {
		t.Errorf("non-image body: err=%v, want ErrUndecodable (so the worker won't retry it)", err)
	}
}

func TestGetRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(maxArtBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if _, err := Get(context.Background(), srv.URL, t.TempDir(), allowedHost(t, srv.URL)); !errors.Is(err, ErrUndecodable) {
		t.Errorf("oversized response: err=%v, want ErrUndecodable", err)
	}
}

func TestGetDecodesGIF(t *testing.T) {
	var buf bytes.Buffer
	if err := gif.Encode(&buf, solid(4, 4, color.RGBA{9, 9, 9, 255}), nil); err != nil {
		t.Fatalf("gif encode: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	defer srv.Close()
	if img, err := Get(context.Background(), srv.URL, t.TempDir(), allowedHost(t, srv.URL)); err != nil || img == nil {
		t.Fatalf("gif cover: img=%v err=%v", img, err)
	}
}

func TestPruneCache(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		p := filepath.Join(dir, "f"+strconv.Itoa(i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Unix(int64(i), 0) // f0 oldest ... f4 newest
		os.Chtimes(p, ts, ts)
	}
	PruneCache(dir, 2, 1<<20) // keep the 2 newest (f3, f4)
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("kept %d files, want 2", len(ents))
	}
	// no-op guards: unguarded, a zero keep or budget would empty the dir
	PruneCache(dir, 0, 1<<20)
	PruneCache(dir, 2, 0)
	PruneCache("", 5, 1<<20)
	for _, keep := range []string{"f3", "f4"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s should have survived pruning", keep)
		}
	}
}

// agedCovers writes one cover-named file per size into dir, oldest first (each
// modified a second after the one before), and returns their paths.
func agedCovers(t *testing.T, dir string, sizes ...int) []string {
	t.Helper()
	paths := make([]string, len(sizes))
	for i, n := range sizes {
		paths[i] = cacheFile(dir, "https://i.scdn.co/image/"+strconv.Itoa(i))
		if err := os.WriteFile(paths[i], make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Unix(int64(1000+i), 0)
		if err := os.Chtimes(paths[i], ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

// The cache keeps its newest covers while BOTH the count and the byte total
// fit, and removes the rest: the first cover that does not fit and every older
// one, so an old small cover never outlives a newer one.
func TestPruneCacheByCountAndBytes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sizes    []int // oldest first
		keep     int
		maxBytes int64
		kept     int // how many of the newest survive
	}{
		{"bytes bind with few files", []int{10, 10, 10, 10}, 256, 25, 2},
		{"count binds with small files", []int{1, 1, 1, 1, 1}, 2, 1 << 20, 2},
		{"the budget itself fits", []int{10, 10, 10}, 256, 20, 2},
		{"count binds before bytes", []int{4, 4, 4, 4}, 3, 100, 3},
		{"bytes bind before count", []int{8, 8, 8, 8}, 3, 20, 2},
		{"a newer big cover cuts the small older ones", []int{1, 1, 30, 5, 5}, 4, 20, 2},
		{"within both, nothing goes", []int{3, 3}, 256, 100, 2},
		{"a newest cover over the budget goes too", []int{1, 50}, 256, 20, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := agedCovers(t, dir, tc.sizes...)
			PruneCache(dir, tc.keep, tc.maxBytes)
			cut := len(paths) - tc.kept
			for i, p := range paths {
				_, err := os.Stat(p)
				if gone := errors.Is(err, os.ErrNotExist); gone != (i < cut) {
					t.Errorf("cover %d of %d (%d bytes): removed=%v, want %v", i, len(paths), tc.sizes[i], gone, i < cut)
				}
			}
		})
	}
}

// A save still being written (atomicfile's dot-prefixed temporary sibling) is
// neither counted nor removed — counted, this fresh one would break both
// limits as the newest entry and take every cover with it — while an hour-old
// one, what a crash mid-write left behind, is removed.
func TestPruneCacheLeavesTempFilesAlone(t *testing.T) {
	dir := t.TempDir()
	covers := agedCovers(t, dir, 10, 10)
	fresh := filepath.Join(dir, "."+filepath.Base(covers[0])+".tmp-123456") // os.CreateTemp's "*" is digits
	stale := filepath.Join(dir, "."+filepath.Base(covers[1])+".tmp-654321")
	for _, p := range []string{fresh, stale} {
		if err := os.WriteFile(p, make([]byte, 50), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTempAfter)
	os.Chtimes(stale, old, old)
	PruneCache(dir, 2, 25)
	for _, p := range append(covers, fresh) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have survived: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("an hour-old temporary save should be removed, stat: %v", err)
	}
}
