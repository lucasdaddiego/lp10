package workers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// otaServer answers like the vendor manifest, through reply, and counts its
// hits per asked build (hits("") is the total) — so parallel tests sharing one
// server (LP10_OTA_URL is process-wide) each count only their own builds.
func otaServer(t *testing.T, reply func(build string) (int, string)) (*httptest.Server, func(build string) int) {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Device map[string]string `json:"device"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		counts[""]++
		counts[body.Device["fwVersion"]]++
		mu.Unlock()
		if r.Method != http.MethodPost || json.Unmarshal(raw, &body) != nil || body.Device["model"] != "LP10" || body.Device["brand"] != "arylic" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		code, text := reply(body.Device["fwVersion"])
		w.WriteHeader(code)
		_, _ = w.Write([]byte(text))
	}))
	t.Cleanup(srv.Close)
	return srv, func(build string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[build]
	}
}

func TestOTACheckVerdicts(t *testing.T) {
	t.Parallel()
	srv, hits := otaServer(t, func(build string) (int, string) {
		switch build {
		case "AR241CE_8530":
			return 200, `{"errorCode":1001,"errorString":"No update available"}`
		case "AR241CE_9243":
			return 200, `{"errorCode":1000,"errorString":"SUCCESS","url":"https://cdn/x.swu","version":"AR241CE_8530","otapackage":"https://cdn/x.swu","mcuOnlyUpdate":false}`
		case "AR241CE_0001":
			return 200, `{"errorCode":1000,"errorString":"SUCCESS"}`
		case "AR241CE_0002":
			return 200, `{"errorCode":2000,"errorString":"Unknown model"}`
		case "AR241CE_0003":
			return 200, `{"errorCode":2000}`
		case "AR241CE_0004":
			return 500, `oops`
		case "AR241CE_0006":
			return 200, `{"errorCode":1000,"errorString":"SUCCESS","url":"http://cdn/x.swu","version":"AR241CE_8530"}`
		}
		return 200, `not json`
	})
	ctx := context.Background()
	if v := OTACheck(ctx, srv.URL, "AR241CE_8530"); !v.UpToDate || v.Err != "" || v.Asked != "AR241CE_8530" || v.At.IsZero() {
		t.Errorf("current build: %+v", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_9243"); v.UpToDate || v.Offered != "AR241CE_8530" || v.Err != "" || v.PackageURL != "https://cdn/x.swu" {
		t.Errorf("older build: %+v", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_0006"); v.Offered != "AR241CE_8530" || v.PackageURL != "" {
		t.Errorf("an offer naming a plain-http package: %+v, want the offer without the URL", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_0001"); v.Offered != "a newer build" {
		t.Errorf("offer without a version: %+v", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_0002"); v.Err != "vendor said: Unknown model" {
		t.Errorf("vendor error: %+v", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_0003"); v.Err != "unexpected vendor reply" {
		t.Errorf("bare vendor error: %+v", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_0004"); v.Err != "unexpected vendor reply" {
		t.Errorf("http 500: %+v", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_0005"); v.Err != "unexpected vendor reply" {
		t.Errorf("non-JSON: %+v", v)
	}
	// nothing leaves for a build that is missing or not build-shaped
	before := hits("")
	if v := OTACheck(ctx, srv.URL, ""); v.Err != "unrecognised firmware string" {
		t.Errorf("no build: %+v", v)
	}
	if v := OTACheck(ctx, srv.URL, "AR241CE_8530; drop"); v.Err != "unrecognised firmware string" {
		t.Errorf("odd build: %+v", v)
	}
	if hits("") != before {
		t.Error("a request left for an unusable build")
	}
	if v := OTACheck(ctx, "http://127.0.0.1:1/v1", "AR241CE_8530"); v.Err != "vendor unreachable" {
		t.Errorf("dead vendor: %+v", v)
	}
	if v := OTACheck(ctx, "::not a url", "AR241CE_8530"); v.Err != "bad manifest url" {
		t.Errorf("bad url: %+v", v)
	}
	// a reply cut short mid-body is a transport failure, not a verdict
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errorCode":`))
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				c.Close()
			}
		}
	}))
	defer cut.Close()
	if v := OTACheck(ctx, cut.URL, "AR241CE_8530"); v.Err != "vendor unreachable" {
		t.Errorf("truncated reply: %+v", v)
	}
}

// The vendor offers a bundle to one deviceId five times, then answers "no
// update" to that id: every check carries an id of its own, so the sixth and
// later ones still see the offer.
func TestOTACheckFreshDeviceID(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Device map[string]string `json:"device"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := body.Device["deviceId"]
		mu.Lock()
		asked[id]++
		n := asked[id]
		mu.Unlock()
		if n > 5 {
			_, _ = w.Write([]byte(`{"errorCode":1001,"errorString":"No update available"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errorCode":1000,"errorString":"SUCCESS","url":"https://cdn/x.swu","version":"AR241CP_8747"}`))
	}))
	defer srv.Close()
	for i := range 8 {
		if v := OTACheck(context.Background(), srv.URL, "AR241CP_1"); v.Offered != "AR241CP_8747" {
			t.Fatalf("check %d: %+v, want the offer", i+1, v)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 8 {
		t.Errorf("8 checks sent %d distinct deviceIds: %v", len(asked), asked)
	}
	for id := range asked {
		if !strings.HasPrefix(id, "lp10-") || len(id) != len("lp10-00000000") {
			t.Errorf("deviceId %q, want lp10-<8 hex>", id)
		}
	}
}

// runOTA runs otaWorker on st until until holds (or 5 s pass) and returns
// the diagnostics at that moment.
func runOTA(t *testing.T, st *protocol.State, until func(d protocol.DiagnosticSnapshot) bool) protocol.DiagnosticSnapshot {
	t.Helper()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { otaWorker(ctx, control, st); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !until(st.DiagnosticView()) {
		time.Sleep(20 * time.Millisecond)
	}
	d := st.DiagnosticView()
	control.stop.Set()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	return d
}

// TestOTAWorker runs the worker scenarios in parallel against one vendor
// stand-in: the worker reads LP10_OTA_URL as it starts, and the env is
// process-wide. Each subtest asks about builds no other one does.
func TestOTAWorker(t *testing.T) {
	srv, hits := otaServer(t, func(build string) (int, string) {
		switch build {
		case "AR241CE_8530", "AR241CE_7777":
			return 200, `{"errorCode":1001,"errorString":"No update available"}`
		}
		return 200, `{"errorCode":1000,"errorString":"SUCCESS","version":"AR241CE_8530"}`
	})
	t.Setenv("LP10_OTA_URL", srv.URL)

	// The worker only ever asks on a request, serves a repeat request from a
	// fresh verdict without a second trip, and asks again when the build
	// changed.
	t.Run("on demand, then from the fresh verdict", func(t *testing.T) {
		t.Parallel()
		st := protocol.NewState()
		st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_8530.23.2"})
		start := time.Now()
		d := runOTA(t, st, func(protocol.DiagnosticSnapshot) bool { return time.Since(start) > 3*otaPoll })
		if d.OTA != nil || hits("AR241CE_8530") != 0 {
			t.Fatalf("unrequested check: %+v hits=%d", d.OTA, hits("AR241CE_8530"))
		}
		st.RequestOTA()
		if !st.DiagnosticView().OTAPending {
			t.Fatal("request not pending")
		}
		d = runOTA(t, st, func(d protocol.DiagnosticSnapshot) bool { return d.OTA != nil })
		if d.OTA == nil || !d.OTA.UpToDate || d.OTA.Asked != "AR241CE_8530" || d.OTAPending || hits("AR241CE_8530") != 1 {
			t.Fatalf("first check: %+v pending=%v hits=%d", d.OTA, d.OTAPending, hits("AR241CE_8530"))
		}

		// The reuse lives inside one worker run: two requests back to back.
		st2 := protocol.NewState()
		st2.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_8530.23.2"})
		st2.RequestOTA()
		control := newRunControl()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { otaWorker(ctx, control, st2); close(done) }()
		defer func() {
			control.stop.Set()
			cancel()
			<-done
		}()
		eventually(t, "the first verdict", 5*time.Second, func() bool { return st2.DiagnosticView().OTA != nil })
		first := st2.DiagnosticView().OTA
		st2.RequestOTA()
		eventually(t, "the repeat request answered", 5*time.Second, func() bool { return !st2.DiagnosticView().OTAPending })
		if hits("AR241CE_8530") != 2 {
			t.Errorf("a repeat request within the fresh window went to the vendor: hits=%d", hits("AR241CE_8530"))
		}
		if again := st2.DiagnosticView().OTA; again == nil || !again.At.Equal(first.At) {
			t.Errorf("repeat verdict = %+v, want the first one (%+v) re-served", again, first)
		}
		// the box now names an older build: that forces a new ask
		st2.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_9243.16"})
		st2.RequestOTA()
		eventually(t, "the changed build's verdict", 5*time.Second, func() bool {
			v := st2.DiagnosticView().OTA
			return v != nil && v.Asked == "AR241CE_9243"
		})
		if v := st2.DiagnosticView().OTA; v.Offered != "AR241CE_8530" || hits("AR241CE_9243") != 1 {
			t.Errorf("changed build: %+v hits=%d", v, hits("AR241CE_9243"))
		}
	})

	// Enabled but the firmware is unknown yet: the request is held (the
	// overlay keeps "checking…"), nothing goes out — and the check runs the
	// moment a build lands, from the same request.
	t.Run("held until the build is known", func(t *testing.T) {
		t.Parallel()
		st := protocol.NewState()
		st.RequestOTA()
		control := newRunControl()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { otaWorker(ctx, control, st); close(done) }()
		defer func() {
			control.stop.Set()
			cancel()
			<-done
		}()
		time.Sleep(3 * otaPoll)
		if d := st.DiagnosticView(); d.OTA != nil || !d.OTAPending || hits("AR241CE_7777") != 0 {
			t.Errorf("unknown firmware: verdict %+v pending=%v hits=%d, want the request held", d.OTA, d.OTAPending, hits("AR241CE_7777"))
		}
		st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_7777.29.2"})
		eventually(t, "the verdict once the build landed", 5*time.Second, func() bool { return st.DiagnosticView().OTA != nil })
		if d := st.DiagnosticView(); !d.OTA.UpToDate || d.OTAPending || hits("AR241CE_7777") != 1 {
			t.Errorf("build landed: verdict %+v pending=%v hits=%d, want one check", d.OTA, d.OTAPending, hits("AR241CE_7777"))
		}
	})
}

// Set-but-empty LP10_OTA_URL disables the worker: it returns at once and
// leaves the request pending. Unset, the checks go to the vendor manifest.
func TestOTAWorkerDisabledAndURL(t *testing.T) {
	t.Setenv("LP10_OTA_URL", "")
	st := protocol.NewState()
	st.RequestOTA()
	done := make(chan struct{})
	go func() { otaWorker(context.Background(), newRunControl(), st); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a disabled worker should return at once")
	}
	if !st.DiagnosticView().OTAPending {
		t.Error("a disabled worker consumed the request")
	}
	if u, ok := ManifestURL(); ok || u != "" {
		t.Errorf("ManifestURL disabled = %q %v", u, ok)
	}
	t.Setenv("LP10_OTA_URL", "http://127.0.0.1:9/v1")
	if u, ok := ManifestURL(); !ok || u != "http://127.0.0.1:9/v1" {
		t.Errorf("ManifestURL override = %q %v", u, ok)
	}
	os.Unsetenv("LP10_OTA_URL") // t.Setenv restores the TestMain value afterwards
	if u, ok := ManifestURL(); !ok || u != otaManifestURL {
		t.Errorf("ManifestURL default = %q %v, want %q", u, ok, otaManifestURL)
	}
}
