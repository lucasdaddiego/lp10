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

func runZC(t *testing.T, cfg config.Config, until func(d protocol.DiagnosticSnapshot) bool) (*protocol.State, protocol.DiagnosticSnapshot) {
	t.Helper()
	st := protocol.NewState()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { zcWorker(ctx, control, st, cfg); close(done) }()
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
	return st, d
}

func TestZCWorkerRecordsAnswerAndSilence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":101,"statusString":"OK","libraryVersion":"3.203.239-g1d6bd565","activeUser":"lu\u001bcas","remoteName":"Living"}`))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	t.Setenv("LP10_ZC_ADDR", addr)
	_, d := runZC(t, config.Config{Host: "ignored"}, func(d protocol.DiagnosticSnapshot) bool { return d.SpotifyZC != nil })
	if d.SpotifyZC == nil || d.SpotifyZC.ActiveUser != "lucas" || d.SpotifyZC.LibraryVersion != "3.203.239-g1d6bd565" ||
		d.SpotifyZC.Status != 101 || d.SpotifyZC.RemoteName != "Living" {
		t.Fatalf("ZeroConf info = %+v, want the (control-stripped) answer", d.SpotifyZC)
	}
	if d.ZCPort != atoiOrZero(port) || d.ZCPort == 0 {
		t.Errorf("port = %d, want %s", d.ZCPort, port)
	}
	if d.ZCOKAt.IsZero() || d.ZCProbeAt.IsZero() {
		t.Error("probe/answer times should be stamped")
	}

	// a dead fixed target: probe stamped, port known, no answer
	srv.Close()
	_, d = runZC(t, config.Config{}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCProbeAt.IsZero() || d.ZCPort == 0 {
		t.Errorf("dead target: %+v port=%d probeAt=%v", d.SpotifyZC, d.ZCPort, d.ZCProbeAt)
	}
}

func TestZCWorkerDisabledAndUnfindable(t *testing.T) {
	// set-but-empty disables the worker outright (the hermetic e2e contract)
	t.Setenv("LP10_ZC_ADDR", "")
	st := protocol.NewState()
	control := newRunControl()
	done := make(chan struct{})
	go func() { zcWorker(context.Background(), control, st, config.Config{Host: "Living.local"}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a disabled worker should return at once")
	}
	if !st.DiagnosticView(time.Now()).ZCProbeAt.IsZero() {
		t.Error("a disabled worker probed")
	}
	// no host configured: same
	host, fixed, ok := zcTarget(config.Config{})
	if ok || host != "" || fixed != "" {
		t.Errorf("zcTarget(empty host) = %q %q %v", host, fixed, ok)
	}
	// an unset override with a host resolves to nothing on the LAN here — the
	// loop runs the mDNS path (a short timeout) and stamps a not-found probe
	t.Setenv("LP10_ZC_ADDR", "127.0.0.1:1") // keep the real LAN out of the test
	// a bad override port reports 0 rather than failing the worker
	t.Setenv("LP10_ZC_ADDR", "127.0.0.1:notaport")
	_, d := runZC(t, config.Config{}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCPort != 0 {
		t.Errorf("bad override port: %+v port=%d", d.SpotifyZC, d.ZCPort)
	}
}

func TestZCResolveAndAtoi(t *testing.T) {
	if ip := zcResolve(context.Background(), "192.168.0.13"); ip == nil || ip.String() != "192.168.0.13" {
		t.Errorf("literal IP resolved to %v", ip)
	}
	if ip := zcResolve(context.Background(), "localhost"); ip != nil && !ip.IsLoopback() {
		t.Errorf("localhost resolved to %v", ip)
	}
	if ip := zcResolve(context.Background(), "definitely-not-a-host.invalid"); ip != nil {
		t.Errorf("an unresolvable host gave %v", ip)
	}
	for s, want := range map[string]int{"9096": 9096, "0": 0, "": 0, "x1": 0, "70000": 0, "65535": 65535} {
		if got := atoiOrZero(s); got != want {
			t.Errorf("atoiOrZero(%q) = %d, want %d", s, got, want)
		}
	}
}

// Without the override the endpoint comes from mDNS: found → probed on the
// advertised port; not found → a port-0 miss; a probe miss → re-found next
// time (the engine may have restarted on another port).
func TestZCWorkerFindsThenRefindsAfterMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":101,"statusString":"OK","remoteName":"Living"}`))
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	ip := net.ParseIP(host).To4()
	var finds atomic.Int32
	found := atomic.Bool{}
	found.Store(true)
	orig := zcFind
	zcFind = func(h string, got net.IP, _ time.Duration) (discovery.SpotifyEndpoint, bool) {
		finds.Add(1)
		if h != host || !got.Equal(ip) {
			t.Errorf("find asked for %q %v, want %q %v", h, got, host, ip)
		}
		if !found.Load() {
			return discovery.SpotifyEndpoint{}, false
		}
		return discovery.SpotifyEndpoint{Name: "Living", Host: "Living.local", Port: atoiOrZero(port), IP: ip}, true
	}
	defer func() { zcFind = orig }()
	os.Unsetenv("LP10_ZC_ADDR")
	t.Setenv("LP10_ZC_ADDR", "x") // register the cleanup, then really unset it
	os.Unsetenv("LP10_ZC_ADDR")

	_, d := runZC(t, config.Config{Host: host}, func(d protocol.DiagnosticSnapshot) bool { return d.SpotifyZC != nil })
	if d.SpotifyZC == nil || d.SpotifyZC.RemoteName != "Living" || d.ZCPort != atoiOrZero(port) || finds.Load() != 1 {
		t.Fatalf("found path: %+v port=%d finds=%d", d.SpotifyZC, d.ZCPort, finds.Load())
	}

	// nothing advertised: a miss with port 0
	found.Store(false)
	_, d = runZC(t, config.Config{Host: host}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCPort != 0 {
		t.Errorf("not-found path: %+v port=%d", d.SpotifyZC, d.ZCPort)
	}

	// found, but the endpoint is dead: the miss keeps the port and the next
	// probe looks the endpoint up again rather than trusting the stale address
	found.Store(true)
	srv.Close()
	before := finds.Load()
	_, d = runZC(t, config.Config{Host: host}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCPort != atoiOrZero(port) || finds.Load() != before+1 {
		t.Errorf("dead-endpoint path: %+v port=%d finds=%d", d.SpotifyZC, d.ZCPort, finds.Load())
	}
}
