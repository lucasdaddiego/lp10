package workers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// otaServer answers like the vendor manifest: 1001 for the current build,
// 1000 + an offered version for an older one, and whatever `reply` overrides.
func otaServer(t *testing.T, hits *atomic.Int32, reply func(build string) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body struct {
			Device map[string]string `json:"device"`
		}
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || json.Unmarshal(raw, &body) != nil || body.Device["model"] != "LP10" || body.Device["brand"] != "arylic" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		code, text := reply(body.Device["fwVersion"])
		w.WriteHeader(code)
		_, _ = w.Write([]byte(text))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOTACheckVerdicts(t *testing.T) {
	var hits atomic.Int32
	srv := otaServer(t, &hits, func(build string) (int, string) {
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
		}
		return 200, `not json`
	})
	ctx := context.Background()
	if v := otaCheck(ctx, srv.URL, "AR241CE_8530"); !v.UpToDate || v.Err != "" || v.Asked != "AR241CE_8530" || v.At.IsZero() {
		t.Errorf("current build: %+v", v)
	}
	if v := otaCheck(ctx, srv.URL, "AR241CE_9243"); v.UpToDate || v.Offered != "AR241CE_8530" || v.Err != "" {
		t.Errorf("older build: %+v", v)
	}
	if v := otaCheck(ctx, srv.URL, "AR241CE_0001"); v.Offered != "a newer build" {
		t.Errorf("offer without a version: %+v", v)
	}
	if v := otaCheck(ctx, srv.URL, "AR241CE_0002"); v.Err != "vendor said: Unknown model" {
		t.Errorf("vendor error: %+v", v)
	}
	if v := otaCheck(ctx, srv.URL, "AR241CE_0003"); v.Err != "unexpected vendor reply" {
		t.Errorf("bare vendor error: %+v", v)
	}
	if v := otaCheck(ctx, srv.URL, "AR241CE_0004"); v.Err != "unexpected vendor reply" {
		t.Errorf("http 500: %+v", v)
	}
	if v := otaCheck(ctx, srv.URL, "AR241CE_0005"); v.Err != "unexpected vendor reply" {
		t.Errorf("non-JSON: %+v", v)
	}
	// nothing leaves for a build that is missing or not build-shaped
	before := hits.Load()
	if v := otaCheck(ctx, srv.URL, ""); v.Err != "firmware not read yet" {
		t.Errorf("no build: %+v", v)
	}
	if v := otaCheck(ctx, srv.URL, "AR241CE_8530; drop"); v.Err != "unrecognised firmware string" {
		t.Errorf("odd build: %+v", v)
	}
	if hits.Load() != before {
		t.Error("a request left for an unusable build")
	}
	if v := otaCheck(ctx, "http://127.0.0.1:1/v1", "AR241CE_8530"); v.Err != "vendor unreachable" {
		t.Errorf("dead vendor: %+v", v)
	}
	if v := otaCheck(ctx, "::not a url", "AR241CE_8530"); v.Err != "bad manifest url" {
		t.Errorf("bad url: %+v", v)
	}
}

func runOTA(t *testing.T, st *protocol.State, until func(d protocol.DiagnosticSnapshot) bool) protocol.DiagnosticSnapshot {
	t.Helper()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { otaWorker(ctx, control, st); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !until(st.DiagnosticView(time.Now())) {
		time.Sleep(20 * time.Millisecond)
	}
	d := st.DiagnosticView(time.Now())
	control.stop.Set()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	return d
}

// The worker only ever asks on a request, serves a repeat request from a
// fresh verdict without a second trip, and asks again when the build changed.
func TestOTAWorkerOnDemandAndFresh(t *testing.T) {
	var hits atomic.Int32
	srv := otaServer(t, &hits, func(build string) (int, string) {
		if build == "AR241CE_8530" {
			return 200, `{"errorCode":1001,"errorString":"No update available"}`
		}
		return 200, `{"errorCode":1000,"errorString":"SUCCESS","version":"AR241CE_8530"}`
	})
	t.Setenv("LP10_OTA_URL", srv.URL)
	st := protocol.NewState()
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_8530.23.2"})
	// no request: nothing happens
	d := runOTA(t, st, func(d protocol.DiagnosticSnapshot) bool { return false })
	if d.OTA != nil || hits.Load() != 0 {
		t.Fatalf("unrequested check: %+v hits=%d", d.OTA, hits.Load())
	}
	st.RequestOTA()
	if !st.OTAPending() {
		t.Fatal("request not pending")
	}
	d = runOTA(t, st, func(d protocol.DiagnosticSnapshot) bool { return d.OTA != nil })
	if d.OTA == nil || !d.OTA.UpToDate || d.OTA.Asked != "AR241CE_8530" || d.OTAPending || hits.Load() != 1 {
		t.Fatalf("first check: %+v pending=%v hits=%d", d.OTA, d.OTAPending, hits.Load())
	}
	// A second request within the fresh window is answered from the last
	// verdict; the worker is a new instance here, so exercise the reuse inside
	// one run: two requests back to back.
	st2 := protocol.NewState()
	st2.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_8530.23.2"})
	hits.Store(0)
	st2.RequestOTA()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { otaWorker(ctx, control, st2); close(done) }()
	wait := func(cond func(protocol.DiagnosticSnapshot) bool) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && !cond(st2.DiagnosticView(time.Now())) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait(func(d protocol.DiagnosticSnapshot) bool { return d.OTA != nil })
	first := st2.DiagnosticView(time.Now()).OTA
	st2.RequestOTA()
	wait(func(d protocol.DiagnosticSnapshot) bool { return !d.OTAPending })
	if hits.Load() != 1 {
		t.Errorf("a repeat request within the fresh window went to the vendor: hits=%d", hits.Load())
	}
	if again := st2.DiagnosticView(time.Now()).OTA; again == nil || first == nil || !again.At.Equal(first.At) {
		t.Errorf("repeat verdict = %+v, want the first one (%+v) re-served", again, first)
	}
	// the build changing (the ssh stream now says an older build) forces a new ask
	protocol.ApplyRecord(st2, protocol.Record{"s": {"100 0.5 0.4 0.3 137000 215000 2 AR241CE_9243.16 Linux-5.15.137"}})
	st2.RequestOTA()
	wait(func(d protocol.DiagnosticSnapshot) bool { return d.OTA != nil && d.OTA.Asked == "AR241CE_9243" })
	if v := st2.DiagnosticView(time.Now()).OTA; v == nil || v.Offered != "AR241CE_8530" || hits.Load() != 2 {
		t.Errorf("changed build: %+v hits=%d", v, hits.Load())
	}
	control.stop.Set()
	cancel()
	<-done
}

func TestOTAWorkerDisabledAndNoFirmware(t *testing.T) {
	t.Setenv("LP10_OTA_URL", "")
	st := protocol.NewState()
	st.RequestOTA()
	control := newRunControl()
	done := make(chan struct{})
	go func() { otaWorker(context.Background(), control, st); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a disabled worker should return at once")
	}
	if !st.OTAPending() {
		t.Error("a disabled worker consumed the request")
	}
	// enabled but the firmware is unknown yet: a verdict that says so, no request out
	var hits atomic.Int32
	srv := otaServer(t, &hits, func(string) (int, string) { return 200, `{"errorCode":1001}` })
	t.Setenv("LP10_OTA_URL", srv.URL)
	st = protocol.NewState()
	st.RequestOTA()
	d := runOTA(t, st, func(d protocol.DiagnosticSnapshot) bool { return d.OTA != nil })
	if d.OTA == nil || d.OTA.Err != "firmware not read yet" || hits.Load() != 0 {
		t.Errorf("unknown firmware: %+v hits=%d", d.OTA, hits.Load())
	}
	if u, ok := otaURL(); !ok || u != srv.URL {
		t.Errorf("otaURL override = %q %v", u, ok)
	}
}
