package workers

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10go/internal/config"
	"github.com/lucasdaddiego/lp10go/internal/protocol"
)

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
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(body)
	}))
	defer srv.Close()

	st := protocol.NewState()
	st.Preload(protocol.Track{"TrackName": "x", "CoverArtUrl": srv.URL}, 0, 50)
	go ArtWorker(st, config.Config{Art: true, ArtMode: "auto"})
	defer st.Stop.Set()

	if !waitFor(func() bool { return st.Snap().Art != nil }, 3*time.Second) {
		t.Fatal("art never loaded")
	}
	// Let the poll loop run several more cycles (artPoll): the same url must not
	// refetch — loadedURL gates it — so the endpoint stays hit exactly once. The
	// old 50ms wait spanned zero polls (artPoll is 700ms), so it proved nothing.
	time.Sleep(2*artPoll + 100*time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("endpoint hit %d times, want 1 (dedup by url)", n)
	}
}

// art disabled, or art_mode "off", returns immediately and fetches nothing.
func TestArtWorkerDisabledFetchesNothing(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	for _, cfg := range []config.Config{{Art: false}, {Art: true, ArtMode: "off"}} {
		st := protocol.NewState()
		st.Preload(protocol.Track{"CoverArtUrl": srv.URL}, 0, 50)
		ArtWorker(st, cfg) // must return synchronously, not loop
		if st.Snap().Art != nil {
			t.Errorf("cfg %+v: art set despite being disabled", cfg)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("disabled worker fetched %d times, want 0", n)
	}
}
