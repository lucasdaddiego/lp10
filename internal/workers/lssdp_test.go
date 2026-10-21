package workers

import (
	"context"
	"net"
	"os"
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

// Set-but-empty LP10_LSSDP_HOST disables the worker outright (the hermetic
// e2e contract), as does no host at all: it returns at once, without a probe.
func TestLSSDPWorkerDisabled(t *testing.T) {
	t.Setenv("LP10_LSSDP_HOST", "")
	for _, cfg := range []config.Config{{Host: "1.2.3.4"}, {}} {
		st := protocol.NewState()
		done := make(chan struct{})
		go func() {
			lssdpWorker(context.Background(), newRunControl(), st, cfg)
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
		os.Unsetenv("LP10_LSSDP_HOST") // the second round: no override, no host (t.Setenv restores it)
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

// startProbeLoop runs probeLoop on st with a millisecond cadence (the LSSDP
// and ZeroConf workers pass their seconds; the loop is the same) and stops it
// when the test ends.
func startProbeLoop(t *testing.T, st *protocol.State, probe func()) {
	t.Helper()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cad := probeCadence{first: 10 * time.Millisecond, connected: 20 * time.Millisecond, disconnected: 20 * time.Millisecond}
	go func() { probeLoop(ctx, control, st, "probe", cad, probe); close(done) }()
	t.Cleanup(func() {
		control.stop.Set()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("the probe loop did not stop")
		}
	})
}

// The first probe runs even when connected with nobody looking (its answer is
// the connect summary's and the update check's); after it, a connected loop
// with nobody looking asks nothing and polls the flag instead; the moment a
// view wants the answer, the next probe fires within probeQuietPoll and the
// cadence resumes.
func TestProbeLoopQuietCadence(t *testing.T) {
	t.Parallel()
	st := protocol.NewState()
	st.Received() // connected
	st.SetProbeQuiet(true)
	var hits atomic.Int32
	startProbeLoop(t, st, func() { hits.Add(1) })
	eventually(t, "the first probe", 2*time.Second, func() bool { return hits.Load() == 1 })
	for start := time.Now(); time.Since(start) < 300*time.Millisecond; time.Sleep(20 * time.Millisecond) {
		if hits.Load() != 1 {
			t.Fatalf("a quiet, connected loop probed again (%d probes)", hits.Load())
		}
	}
	st.SetProbeQuiet(false)
	eventually(t, "a probe once a view wants it", probeQuietPoll+time.Second, func() bool { return hits.Load() == 2 })
	eventually(t, "the cadence resumed", time.Second, func() bool { return hits.Load() >= 4 })
}

// A panic in the probe (the mDNS and LSSDP parsers read whatever the LAN
// answers) must cost a noted error, not the program: a panic escaping the
// worker's goroutine would kill lp10 with the terminal still in raw mode.
func TestProbeLoopSurvivesAProbePanic(t *testing.T) {
	t.Parallel()
	st := protocol.NewState()
	startProbeLoop(t, st, func() { panic("mdns parse boom") })
	eventually(t, "the panic noted", 2*time.Second, func() bool { return st.Snap().Error == "probe: mdns parse boom" })
}
