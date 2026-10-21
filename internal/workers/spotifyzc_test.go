package workers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// runZC runs zcWorker on a fresh State until until holds (or 5 s pass) and
// returns the diagnostics at that moment.
func runZC(t *testing.T, cfg config.Config, until func(d protocol.DiagnosticSnapshot) bool) protocol.DiagnosticSnapshot {
	t.Helper()
	st := protocol.NewState()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { zcWorker(ctx, control, st, cfg); close(done) }()
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

// zcServer is a stand-in for the engine's getInfo endpoint, counting its hits.
func zcServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const zcLive = `{"status":101,"statusString":"OK","libraryVersion":"3.203.239-g1d6bd565","activeUser":"lu\u001bcas","remoteName":"Living"}`

// LP10_ZC_ADDR fixes the endpoint (skipping mDNS): its answer is recorded
// control-stripped with the override's port.
func TestZCWorkerFixedAddress(t *testing.T) {
	srv, _ := zcServer(t, zcLive)
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	t.Setenv("LP10_ZC_ADDR", addr)
	d := runZC(t, config.Config{Host: "ignored"}, func(d protocol.DiagnosticSnapshot) bool { return d.SpotifyZC != nil })
	if d.SpotifyZC == nil || d.SpotifyZC.ActiveUser != "lucas" || d.SpotifyZC.StatusString != "OK" || d.SpotifyZC.LibraryVersion != "3.203.239-g1d6bd565" {
		t.Fatalf("ZeroConf info = %+v, want the (control-stripped) answer", d.SpotifyZC)
	}
	if d.ZCPort != atoiOrZero(port) || d.ZCPort == 0 {
		t.Errorf("port = %d, want %s", d.ZCPort, port)
	}
	if d.ZCOKAt.IsZero() || d.ZCProbeAt.IsZero() {
		t.Error("probe/answer times should be stamped")
	}
}

// Set-but-empty LP10_ZC_ADDR disables the worker outright (the hermetic e2e
// contract), as does no host at all: it returns at once, without a probe.
func TestZCWorkerDisabled(t *testing.T) {
	t.Setenv("LP10_ZC_ADDR", "")
	for _, cfg := range []config.Config{{Host: "Living.local"}, {}} {
		st := protocol.NewState()
		done := make(chan struct{})
		go func() {
			zcWorker(context.Background(), newRunControl(), st, cfg)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a disabled worker should return at once")
		}
		if !st.DiagnosticView().ZCProbeAt.IsZero() {
			t.Error("a disabled worker probed")
		}
		os.Unsetenv("LP10_ZC_ADDR") // the second round: no override, no host (t.Setenv restores it)
	}
}

// Without the override the endpoint comes from mDNS: found, it is probed on
// the advertised port (the lookup asked for the configured host and its
// address); found but dead, the miss keeps the port and records no answer.
func TestZCWorkerFindsOverMDNS(t *testing.T) {
	t.Parallel()
	srv, _ := zcServer(t, zcLive)
	ip, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	const host = "198.51.100.3"
	advertiseZC(t, host, func(h string, got net.IP) (discovery.SpotifyEndpoint, bool) {
		if h != host || !got.Equal(net.ParseIP(host)) {
			t.Errorf("find asked for %q %v, want %q and its address", h, got, host)
		}
		return discovery.SpotifyEndpoint{Name: "Living", Host: "Living.local", Port: atoiOrZero(port), IP: net.ParseIP(ip)}, true
	})
	d := runZC(t, config.Config{Host: host}, func(d protocol.DiagnosticSnapshot) bool { return d.SpotifyZC != nil })
	if d.SpotifyZC == nil || d.SpotifyZC.ActiveUser != "lucas" || d.ZCPort != atoiOrZero(port) {
		t.Fatalf("found path: %+v port=%d", d.SpotifyZC, d.ZCPort)
	}

	srv.Close()
	d = runZC(t, config.Config{Host: host}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCPort != atoiOrZero(port) {
		t.Errorf("dead-endpoint path: %+v port=%d", d.SpotifyZC, d.ZCPort)
	}
}

// A host nothing advertises: a not-found probe, port 0.
func TestZCWorkerNotAdvertised(t *testing.T) {
	t.Parallel()
	d := runZC(t, config.Config{Host: "198.51.100.1"}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCPort != 0 || d.ZCProbeAt.IsZero() {
		t.Errorf("not advertised: %+v port=%d probeAt=%v", d.SpotifyZC, d.ZCPort, d.ZCProbeAt)
	}
}
