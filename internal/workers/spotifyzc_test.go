package workers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
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
