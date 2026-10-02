package workers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
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
	return st, d
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

// advertiseServer makes the mDNS stand-in find srv for host.
func advertiseServer(t *testing.T, host string, srv *httptest.Server) {
	t.Helper()
	ip, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	advertiseZC(t, host, func(string, net.IP) (discovery.SpotifyEndpoint, bool) {
		return discovery.SpotifyEndpoint{Name: "Living", Host: "Living.local", Port: atoiOrZero(port), IP: net.ParseIP(ip)}, true
	})
}

const zcOK = `{"status":101,"statusString":"OK","remoteName":"Living"}`

// LP10_ZC_ADDR fixes the endpoint (skipping mDNS): its answer is recorded
// control-stripped with the override's port; a dead fixed target keeps that
// port and records no answer.
func TestZCWorkerFixedAddress(t *testing.T) {
	srv, _ := zcServer(t, `{"status":101,"statusString":"OK","libraryVersion":"3.203.239-g1d6bd565","activeUser":"lu\u001bcas","remoteName":"Living"}`)
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	t.Setenv("LP10_ZC_ADDR", addr)
	_, d := runZC(t, config.Config{Host: "ignored"}, func(d protocol.DiagnosticSnapshot) bool { return d.SpotifyZC != nil })
	if d.SpotifyZC == nil || d.SpotifyZC.ActiveUser != "lucas" || d.SpotifyZC.StatusString != "OK" || d.SpotifyZC.LibraryVersion != "3.203.239-g1d6bd565" {
		t.Fatalf("ZeroConf info = %+v, want the (control-stripped) answer", d.SpotifyZC)
	}
	if d.ZCPort != atoiOrZero(port) || d.ZCPort == 0 {
		t.Errorf("port = %d, want %s", d.ZCPort, port)
	}
	if d.ZCOKAt.IsZero() || d.ZCProbeAt.IsZero() {
		t.Error("probe/answer times should be stamped")
	}

	srv.Close()
	_, d = runZC(t, config.Config{}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCProbeAt.IsZero() || d.ZCPort == 0 {
		t.Errorf("dead target: %+v port=%d probeAt=%v", d.SpotifyZC, d.ZCPort, d.ZCProbeAt)
	}
}

func TestZCWorkerDisabledAndBadPort(t *testing.T) {
	// set-but-empty disables the worker outright (the hermetic e2e contract)
	t.Setenv("LP10_ZC_ADDR", "")
	st := protocol.NewState()
	done := make(chan struct{})
	go func() {
		zcWorker(context.Background(), newRunControl(), st, config.Config{Host: "Living.local"})
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
	// a bad override port reports 0 rather than failing the worker
	t.Setenv("LP10_ZC_ADDR", "127.0.0.1:notaport")
	_, d := runZC(t, config.Config{}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCPort != 0 || d.ZCProbeAt.IsZero() {
		t.Errorf("bad override port: %+v port=%d", d.SpotifyZC, d.ZCPort)
	}
}

// No host configured (and no override): the worker does not run. A host
// nothing advertises: a not-found probe, port 0.
func TestZCWorkerNoHostAndNotAdvertised(t *testing.T) {
	t.Parallel()
	if host, fixed, ok := zcTarget(config.Config{}); ok || host != "" || fixed != "" {
		t.Errorf("zcTarget(empty host) = %q %q %v", host, fixed, ok)
	}
	if host, fixed, ok := zcTarget(config.Config{Host: "Living.local"}); !ok || host != "Living.local" || fixed != "" {
		t.Errorf("zcTarget(host) = %q %q %v", host, fixed, ok)
	}
	_, d := runZC(t, config.Config{Host: "198.51.100.1"}, func(d protocol.DiagnosticSnapshot) bool { return !d.ZCProbeAt.IsZero() })
	if d.SpotifyZC != nil || d.ZCPort != 0 || d.ZCProbeAt.IsZero() {
		t.Errorf("not advertised: %+v port=%d probeAt=%v", d.SpotifyZC, d.ZCPort, d.ZCProbeAt)
	}
}

func TestZCResolveAndAtoi(t *testing.T) {
	t.Parallel()
	if ip := zcResolve(context.Background(), "192.168.0.13"); ip == nil || ip.String() != "192.168.0.13" {
		t.Errorf("literal IP resolved to %v", ip)
	}
	if ip := zcResolve(context.Background(), "::1"); ip != nil {
		t.Errorf("an IPv6 literal gave %v, want nil (the A records are IPv4)", ip)
	}
	if ip := zcResolve(context.Background(), "localhost"); ip != nil && !ip.IsLoopback() {
		t.Errorf("localhost resolved to %v", ip)
	}
	// "" fails in the resolver itself: no lookup leaves the machine
	if ip := zcResolve(context.Background(), ""); ip != nil {
		t.Errorf("an unresolvable host gave %v", ip)
	}
	for s, want := range map[string]int{"9096": 9096, "0": 0, "": 0, "x1": 0, "70000": 0, "65535": 65535} {
		if got := atoiOrZero(s); got != want {
			t.Errorf("atoiOrZero(%q) = %d, want %d", s, got, want)
		}
	}
}

// A panic in the probe — here the mDNS lookup, which parses whatever the LAN
// answers — must cost a noted error, not the program: a panic escaping the
// worker's goroutine would kill lp10 with the terminal still in raw mode.
func TestZCWorkerSurvivesAProbePanic(t *testing.T) {
	t.Parallel()
	advertiseZC(t, "198.51.100.2", func(string, net.IP) (discovery.SpotifyEndpoint, bool) {
		panic("mdns parse boom")
	})
	st := protocol.NewState()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { zcWorker(ctx, control, st, config.Config{Host: "198.51.100.2"}); close(done) }()
	noted := waitFor(func() bool { return st.Snap().Error == "zeroconf worker: mdns parse boom" }, 5*time.Second)
	control.stop.Set()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the worker did not stop after the panic")
	}
	if !noted {
		t.Errorf("error = %q, want the probe's panic noted", st.Snap().Error)
	}
}

// Without the override the endpoint comes from mDNS: found → probed on the
// advertised port; not found → a port-0 miss; a probe miss → re-found next
// time (the engine may have restarted on another port).
func TestZCWorkerFindsThenRefindsAfterMiss(t *testing.T) {
	t.Parallel()
	srv, _ := zcServer(t, zcOK)
	ip, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	const host = "198.51.100.3"
	var finds atomic.Int32
	var found atomic.Bool
	found.Store(true)
	advertiseZC(t, host, func(h string, got net.IP) (discovery.SpotifyEndpoint, bool) {
		finds.Add(1)
		if h != host || !got.Equal(net.ParseIP(host)) {
			t.Errorf("find asked for %q %v, want %q and its address", h, got, host)
		}
		if !found.Load() {
			return discovery.SpotifyEndpoint{}, false
		}
		return discovery.SpotifyEndpoint{Name: "Living", Host: "Living.local", Port: atoiOrZero(port), IP: net.ParseIP(ip)}, true
	})

	_, d := runZC(t, config.Config{Host: host}, func(d protocol.DiagnosticSnapshot) bool { return d.SpotifyZC != nil })
	if d.SpotifyZC == nil || d.SpotifyZC.StatusString != "OK" || d.ZCPort != atoiOrZero(port) || finds.Load() != 1 {
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

// startZC runs zcWorker on st and stops it when the test ends.
func startZC(t *testing.T, st *protocol.State, host string) {
	t.Helper()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { zcWorker(ctx, control, st, config.Config{Host: host}); close(done) }()
	t.Cleanup(func() {
		control.stop.Set()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("the ZeroConf worker did not stop")
		}
	})
}

// Stop alone ends a waiting worker at its next wake, without a probe.
func TestZCWorkerStopBeforeFirstProbe(t *testing.T) {
	t.Parallel()
	srv, hits := zcServer(t, zcOK)
	advertiseServer(t, "198.51.100.4", srv)
	control := newRunControl()
	done := make(chan struct{})
	go func() {
		zcWorker(context.Background(), control, protocol.NewState(), config.Config{Host: "198.51.100.4"})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // into the first-probe lag
	control.stop.Set()
	select {
	case <-done:
	case <-time.After(zcFirstProbeLag + 2*time.Second):
		t.Fatal("a stopped worker kept running")
	}
	if hits.Load() != 0 {
		t.Error("a stopped worker probed")
	}
}

// The first probe runs even when connected with nobody looking (see the
// LSSDP twin): the engine's state is part of the connect summary.
func TestZCWorkerFirstProbeRunsWhenConnectedAndQuiet(t *testing.T) {
	t.Parallel()
	srv, hits := zcServer(t, zcOK)
	advertiseServer(t, "198.51.100.5", srv)
	st := protocol.NewState()
	st.Received() // connected
	st.SetProbeQuiet(true)
	startZC(t, st, "198.51.100.5")
	eventually(t, "the first probe", zcFirstProbeLag+2*time.Second, func() bool { return st.DiagnosticView().SpotifyZC != nil })
	if hits.Load() != 1 {
		t.Errorf("probes = %d, want 1", hits.Load())
	}
}

// After the first probe, a connected worker with nobody looking skips its
// rounds. Slow by necessity (~12 s): the round after the first comes
// zcDisconnected (10 s) later, and only that round can be skipped.
func TestZCWorkerQuietSkipsLaterProbes(t *testing.T) {
	t.Parallel()
	srv, hits := zcServer(t, zcOK)
	advertiseServer(t, "198.51.100.6", srv)
	st := protocol.NewState() // disconnected: the first probe runs, then the 10 s cadence
	st.SetProbeQuiet(true)
	startZC(t, st, "198.51.100.6")
	eventually(t, "the first probe", zcFirstProbeLag+2*time.Second, func() bool { return st.DiagnosticView().SpotifyZC != nil })
	first := time.Now()
	time.Sleep(200 * time.Millisecond) // let the worker pick its cadence while still disconnected
	st.Received()                      // the tunnel connects
	for time.Since(first) < zcDisconnected+time.Second {
		if hits.Load() != 1 {
			t.Fatalf("a quiet, connected worker probed again (%d probes)", hits.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
