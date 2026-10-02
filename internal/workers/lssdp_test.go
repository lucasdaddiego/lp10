package workers

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// fakeLSSDP answers every M-SEARCH on a loopback UDP port with a canned reply
// (or stays silent), returning the host:port to probe and a count of the
// probes it received.
func fakeLSSDP(t *testing.T, reply string) (string, *atomic.Int32) {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	t.Cleanup(func() { c.Close() })
	var hits atomic.Int32
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			hits.Add(1)
			if reply != "" && n > 0 {
				c.WriteToUDP([]byte(reply), from)
			}
		}
	}()
	return c.LocalAddr().String(), &hits
}

const cannedLSSDP = "HTTP/1.1 200 OK\r\nPORT:7777\r\nFWVERSION:AR241CE_8530.23.2\r\nDeviceName:Living\x1b[31m\r\nState:S\r\nNETMODE:ETH0\r\nSOURCE_LIST:LS8::01000030\r\nUSN:aa:bb\r\n\r\n"

// startLSSDP runs lssdpWorker on st and returns a func that stops it and
// waits. ProbeLSSDP honours an explicit host:port, so the configured host
// points the worker at a fake without the process-wide env override, and the
// tests run in parallel.
func startLSSDP(t *testing.T, st *protocol.State, host string) func() {
	t.Helper()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { lssdpWorker(ctx, control, st, config.Config{Host: host}); close(done) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		control.stop.Set()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("the LSSDP worker did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestLSSDPWorkerRecordsAnswer(t *testing.T) {
	t.Parallel()
	addr, _ := fakeLSSDP(t, cannedLSSDP)
	st := protocol.NewState()
	stop := startLSSDP(t, st, addr)
	eventually(t, "the LSSDP answer", 5*time.Second, func() bool { return st.Snap().LSSDPAlive })
	stop()
	d := st.DiagnosticView()
	if d.LSSDP == nil || d.LSSDP.FW != "AR241CE_8530.23.2" || d.LSSDP.State != "S" || d.LSSDP.NetMode != "ETH0" {
		t.Fatalf("LSSDP info = %+v, want the canned (control-stripped) answer", d.LSSDP)
	}
	if d.LSSDPOKAt.IsZero() || d.LSSDPProbeAt.IsZero() {
		t.Error("probe/answer times should be stamped")
	}
}

// A silent target: the probe is stamped, nothing is recorded as alive.
func TestLSSDPWorkerRecordsSilence(t *testing.T) {
	t.Parallel()
	addr, _ := fakeLSSDP(t, "")
	st := protocol.NewState()
	stop := startLSSDP(t, st, addr)
	eventually(t, "a probe stamped", 5*time.Second, func() bool { return !st.DiagnosticView().LSSDPProbeAt.IsZero() })
	stop()
	if d := st.DiagnosticView(); d.LSSDP != nil || st.Snap().LSSDPAlive {
		t.Errorf("silent target: %+v alive=%v", d.LSSDP, st.Snap().LSSDPAlive)
	}
}

// LP10_LSSDP_HOST beats the configured host; set-but-empty disables the
// worker outright (the hermetic e2e contract), as does no host at all.
func TestLSSDPHostOverride(t *testing.T) {
	if h, ok := lssdpHost(config.Config{Host: "1.2.3.4"}); !ok || h != "1.2.3.4" {
		t.Errorf("no override: %q %v", h, ok)
	}
	if h, ok := lssdpHost(config.Config{}); ok || h != "" {
		t.Errorf("no host should disable: %q %v", h, ok)
	}
	t.Setenv("LP10_LSSDP_HOST", "127.0.0.1:9")
	if h, ok := lssdpHost(config.Config{Host: "1.2.3.4"}); !ok || h != "127.0.0.1:9" {
		t.Errorf("override: %q %v", h, ok)
	}
	t.Setenv("LP10_LSSDP_HOST", "")
	if h, ok := lssdpHost(config.Config{Host: "1.2.3.4"}); ok || h != "" {
		t.Errorf("empty override should disable: %q %v", h, ok)
	}
	st := protocol.NewState()
	done := make(chan struct{})
	go func() {
		lssdpWorker(context.Background(), newRunControl(), st, config.Config{Host: "1.2.3.4"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a disabled worker should return at once")
	}
	if !st.DiagnosticView().LSSDPProbeAt.IsZero() {
		t.Error("a disabled worker probed")
	}
}

// A shutdown with a probe in flight must not sit out the probe's budget: the
// runtime's ctx closes the socket under the read. A silent target keeps every
// probe waiting its full 1.5 s.
func TestLSSDPWorkerStopsMidProbe(t *testing.T) {
	t.Parallel()
	addr, hits := fakeLSSDP(t, "")
	stop := startLSSDP(t, protocol.NewState(), addr)
	eventually(t, "the first probe in flight", 5*time.Second, func() bool { return hits.Load() == 1 })
	start := time.Now()
	stop()
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Errorf("worker took %v to stop with a probe in flight", el)
	}
}

// Stop alone (the runtime sets it before cancelling ctx) ends a waiting
// worker at its next wake, without a probe.
func TestLSSDPWorkerStopBeforeFirstProbe(t *testing.T) {
	t.Parallel()
	addr, hits := fakeLSSDP(t, cannedLSSDP)
	control := newRunControl()
	done := make(chan struct{})
	go func() {
		lssdpWorker(context.Background(), control, protocol.NewState(), config.Config{Host: addr})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // into the first-probe lag
	control.stop.Set()
	select {
	case <-done:
	case <-time.After(lssdpFirstProbeLag + 2*time.Second):
		t.Fatal("a stopped worker kept running")
	}
	if hits.Load() != 0 {
		t.Error("a stopped worker probed")
	}
}

// The first probe runs even when the tunnel is already up and nobody looks at
// the answer: its firmware build is the connect summary's and the update
// check's, and the tunnel connects before the probe is due.
func TestLSSDPWorkerFirstProbeRunsWhenConnectedAndQuiet(t *testing.T) {
	t.Parallel()
	addr, hits := fakeLSSDP(t, cannedLSSDP)
	st := protocol.NewState()
	st.Received() // connected
	st.SetProbeQuiet(true)
	startLSSDP(t, st, addr)
	eventually(t, "the first probe", lssdpFirstProbeLag+2*time.Second, func() bool { return st.Snap().LSSDPAlive })
	if hits.Load() != 1 {
		t.Errorf("probes = %d, want 1", hits.Load())
	}
}

// After the first probe, a connected worker with nobody looking asks nothing
// and polls the flag instead; the moment a view wants the answer, the next
// probe fires within the quiet poll. Slow by necessity (~8 s): the round after
// the first comes lssdpDisconnected (5 s) later, and only that round can be
// skipped.
func TestLSSDPWorkerQuietSkipsLaterProbes(t *testing.T) {
	t.Parallel()
	addr, hits := fakeLSSDP(t, cannedLSSDP)
	st := protocol.NewState() // disconnected: the first probe runs, then the 5 s cadence
	st.SetProbeQuiet(true)
	startLSSDP(t, st, addr)
	eventually(t, "the first probe", lssdpFirstProbeLag+2*time.Second, func() bool { return st.Snap().LSSDPAlive })
	first := time.Now()
	time.Sleep(200 * time.Millisecond) // let the worker pick its cadence while still disconnected
	st.Received()                      // the tunnel connects
	for time.Since(first) < lssdpDisconnected+time.Second {
		if hits.Load() != 1 {
			t.Fatalf("a quiet, connected worker probed again (%d probes)", hits.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	st.SetProbeQuiet(false)
	eventually(t, "a probe once a view wants it", probeQuietPoll+time.Second, func() bool { return hits.Load() == 2 })
}
