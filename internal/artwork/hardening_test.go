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
		if _, err := Get(context.Background(), u, t.TempDir()); !errors.Is(err, ErrUndecodable) {
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
	if _, err := Get(context.Background(), srv.URL, t.TempDir()); !errors.Is(err, ErrUndecodable) {
		t.Errorf("non-image body: err=%v, want ErrUndecodable (so the worker won't retry it)", err)
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
	if img, err := Get(context.Background(), srv.URL, t.TempDir()); err != nil || img == nil {
		t.Fatalf("gif cover: img=%v err=%v", img, err)
	}
}

func TestPruneCache(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, "f"+strconv.Itoa(i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Unix(int64(i), 0) // f0 oldest ... f4 newest
		os.Chtimes(p, ts, ts)
	}
	PruneCache(dir, 2) // keep the 2 newest (f3, f4)
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("kept %d files, want 2", len(ents))
	}
	for _, keep := range []string{"f3", "f4"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s should have survived pruning", keep)
		}
	}
	PruneCache(dir, 0) // no-op guard
	PruneCache("", 5)  // no-op guard
}
