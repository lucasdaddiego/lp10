package workers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// fakeLSSDP answers every M-SEARCH on a loopback UDP port with a canned reply
// (or stays silent), returning the host:port to probe.
func fakeLSSDP(t *testing.T, reply string) string {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if reply != "" && n > 0 {
				c.WriteToUDP([]byte(reply), from)
			}
		}
	}()
	return c.LocalAddr().String()
}

const cannedLSSDP = "HTTP/1.1 200 OK\r\nPORT:7777\r\nFWVERSION:AR241CE_9243.16.2\r\nDeviceName:Living\x1b[31m\r\nState:S\r\nNETMODE:ETH0\r\nSOURCE_LIST:LS8::01000030\r\nUSN:aa:bb\r\n\r\n"

func TestLSSDPWorkerRecordsAnswerAndSilence(t *testing.T) {
	addr := fakeLSSDP(t, cannedLSSDP)
	host, port, _ := net.SplitHostPort(addr)
	_ = port
	// ProbeLSSDP dials :1800 on the host, so the fake must sit on that port —
	// not possible on loopback without privileges; drive the worker's target
	// through the env override with the full host:port instead.
	t.Setenv("LP10_LSSDP_HOST", addr)
	st := protocol.NewState()
	control := newRunControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { lssdpWorker(ctx, control, st, config.Config{Host: host}); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st.Snap().LSSDPAlive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	d := st.DiagnosticView(time.Now())
	if d.LSSDP == nil || d.LSSDP.Name != "Living[31m" || d.LSSDP.FW != "AR241CE_9243.16.2" || d.LSSDP.State != "S" || d.LSSDP.NetMode != "ETH0" {
		t.Fatalf("LSSDP info = %+v, want the canned (control-stripped) answer", d.LSSDP)
	}
	if d.LSSDPOKAt.IsZero() || d.LSSDPProbeAt.IsZero() {
		t.Error("probe/answer times should be stamped")
	}
	control.stop.Set()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}

	// a silent target: probe stamped, no answer
	silent := fakeLSSDP(t, "")
	t.Setenv("LP10_LSSDP_HOST", silent)
	st2 := protocol.NewState()
	control2 := newRunControl()
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { lssdpWorker(ctx2, control2, st2, config.Config{}); close(done2) }()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !st2.DiagnosticView(time.Now()).LSSDPProbeAt.IsZero() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := st2.DiagnosticView(time.Now()); d.LSSDP != nil || d.LSSDPProbeAt.IsZero() || st2.Snap().LSSDPAlive {
		t.Errorf("silent target: %+v alive=%v", d.LSSDP, st2.Snap().LSSDPAlive)
	}
	control2.stop.Set()
	cancel2()
	<-done2

	// empty override disables the worker outright
	t.Setenv("LP10_LSSDP_HOST", "")
	if h, ok := lssdpHost(config.Config{Host: "1.2.3.4"}); ok || h != "" {
		t.Errorf("empty override should disable: %q %v", h, ok)
	}
	if h, ok := lssdpHost(config.Config{}); ok || h != "" {
		t.Errorf("no host should disable: %q %v", h, ok)
	}
}
