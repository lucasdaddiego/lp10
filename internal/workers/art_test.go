package workers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// allowedHost returns raw's hostname, passed as the artWorker's cfg.Host so a
// test's loopback httptest server is exempt from the artwork SSRF block.
func allowedHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Hostname()
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A successful fetch decodes the cover into State and is not repeated for the
// same url (dedup), so the device endpoint is hit exactly once.
func TestArtWorkerLoadsAndDedups(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	body := smallPNG(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(body)
	}))
	defer srv.Close()

	st := protocol.NewState()
	control := newRunControl()
	st.Preload(&protocol.Track{TrackName: "x", CoverArtURL: srv.URL}, 0, 50)
	go artWorker(context.Background(), control, st, config.Config{Art: true, ArtMode: "auto", Host: allowedHost(t, srv.URL)})
	defer control.stop.Set()

	if !waitFor(func() bool { return st.Snap().Art != nil }, 3*time.Second) {
		t.Fatal("art never loaded")
	}
	// Let the poll loop run several more cycles (artPoll): the same url must not
	// refetch — loadedURL gates it — so the endpoint stays hit exactly once. The
	// old 50ms wait spanned zero polls (artPoll is 700ms), so it proved nothing.
	time.Sleep(2*artPoll + 100*time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Errorf("endpoint hit %d times, want 1 (dedup by url)", n)
	}
}

// A cover larger than any placement shows is bounded once, in the worker:
// State — and so every render — gets at most artMaxEdge pixels on the long
// side, aspect kept, instead of the full decode (up to 16 MP) rebuilt on the
// render goroutine at every footprint change.
func TestArtWorkerBoundsLargeCovers(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	big := image.NewRGBA(image.Rect(0, 0, 2*artMaxEdge, 64))
	for i := range big.Pix {
		big.Pix[i] = 0x80
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	st := protocol.NewState()
	control := newRunControl()
	st.Preload(&protocol.Track{TrackName: "x", CoverArtURL: srv.URL}, 0, 50)
	go artWorker(context.Background(), control, st, config.Config{Art: true, ArtMode: "auto", Host: allowedHost(t, srv.URL)})
	defer control.stop.Set()

	if !waitFor(func() bool { return st.Snap().Art != nil }, 5*time.Second) {
		t.Fatal("art never loaded")
	}
	if b := st.Snap().Art.Bounds(); b.Dx() != artMaxEdge || b.Dy() != 32 {
		t.Errorf("State got a %dx%d cover from a %dx64 source, want %dx32", b.Dx(), b.Dy(), 2*artMaxEdge, artMaxEdge)
	}
}

// boundArt averages each k×k block — including a short last block — from any
// image type at any origin, and passes a cover that already fits through as is.
func TestBoundArtAveragesBlocks(t *testing.T) {
	fits := image.NewRGBA(image.Rect(0, 0, artMaxEdge, 10))
	if got := boundArt(fits); got != image.Image(fits) {
		t.Error("a cover within artMaxEdge was copied, want it passed through")
	}
	// 2049 wide at a non-zero origin: k=2, so 1025 columns, the last one a
	// single source column. Columns alternate 100/200, so a full block is 150.
	g := image.NewGray(image.Rect(10, 10, 10+artMaxEdge+1, 12))
	for y := 10; y < 12; y++ {
		for x := 10; x < 10+artMaxEdge+1; x++ {
			g.SetGray(x, y, color.Gray{Y: uint8(100 + 100*((x-10)%2))})
		}
	}
	got, ok := boundArt(g).(*image.RGBA)
	if !ok || got.Bounds() != image.Rect(0, 0, 1025, 1) {
		t.Fatalf("bounded %v source = %T %v, want a 1025x1 RGBA", g.Bounds(), got, got.Bounds())
	}
	if c := got.RGBAAt(0, 0); c != (color.RGBA{150, 150, 150, 255}) {
		t.Errorf("a full block averaged to %v, want grey 150", c)
	}
	if c := got.RGBAAt(1024, 0); c != (color.RGBA{100, 100, 100, 255}) {
		t.Errorf("the short last block averaged to %v, want grey 100", c)
	}
}

// The worker bounds the cover cache by bytes as well as by count: at startup a
// cache far under artCacheKeep files still drops what passes artCacheBytes.
// The two newest covers fill the budget exactly, so the oldest, a single byte,
// is the one to go — any other budget keeps it or cuts a newer one. The files
// are sparse: nothing near 64 MB touches the disk.
func TestArtWorkerPrunesCacheByBytes(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	dir := config.ArtCacheDir()
	var covers []string
	for i, size := range []int64{1, artCacheBytes / 2, artCacheBytes / 2} { // oldest first
		p := filepath.Join(dir, fmt.Sprintf("%040x", i))
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, size); err != nil {
			t.Fatal(err)
		}
		ts := time.Unix(int64(1000+i), 0)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
		covers = append(covers, p)
	}
	control := newRunControl()
	control.stop.Set() // the startup prune runs; the poll loop then exits at once
	artWorker(context.Background(), control, protocol.NewState(), config.Config{Art: true, ArtMode: "auto"})
	for i, p := range covers {
		_, err := os.Stat(p)
		if gone := errors.Is(err, os.ErrNotExist); gone != (i == 0) {
			t.Errorf("cover %d: removed=%v, want %v", i, gone, i == 0)
		}
	}
}

// art disabled, or art_mode "off", returns immediately and fetches nothing.
func TestArtWorkerDisabledFetchesNothing(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	for _, cfg := range []config.Config{{Art: false}, {Art: true, ArtMode: "off"}} {
		st := protocol.NewState()
		st.Preload(&protocol.Track{CoverArtURL: srv.URL}, 0, 50)
		artWorker(context.Background(), newRunControl(), st, cfg) // must return synchronously, not loop
		if st.Snap().Art != nil {
			t.Errorf("cfg %+v: art set despite being disabled", cfg)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("disabled worker fetched %d times, want 0", n)
	}
}
