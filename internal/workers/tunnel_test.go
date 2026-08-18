package workers

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// TestTunnelWorkerRoundTrip stands up a fake :2018 server and checks the worker
// applies device broadcasts to State and writes queued commands as CODE:VAL;.
func TestTunnelWorkerRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("LP10_TUNNEL_ADDR", ln.Addr().String())

	st := protocol.NewState()
	control := newRunControl()
	eqcmds := make(chan EQCommand, 8)
	go tunnelWorker(context.Background(), control, st, config.Config{Host: "unused"}, eqcmds)
	defer control.stop.Set()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()

	// The device broadcasts current values -> State reflects them, link is live.
	if _, err := conn.Write([]byte("MXV:42;BAS:-6;")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "MXV applied", func() bool {
		v, ok := st.EQValue("MXV")
		return ok && v == 42
	})
	if v, ok := st.EQValue("BAS"); !ok || v != -6 {
		t.Errorf("BAS=%d,%v want -6", v, ok)
	}
	if connected, _ := st.EQView(); !connected {
		t.Error("tunnel not marked connected")
	}

	// A queued command is written to the device, clamped, as CODE:VAL;.
	eqcmds <- EQCommand{Code: "BAS", Val: 99} // clamps to +10
	got := readUntilContains(t, conn, "BAS:10;")
	if !strings.Contains(got, "BAS:10;") {
		t.Errorf("server received %q, want it to contain BAS:10;", got)
	}
}

// TestTunnelWorkerEchoHold checks a locally-set value isn't clobbered by the
// device's broadcast echo arriving within the hold window.
func TestTunnelWorkerEchoHold(t *testing.T) {
	st := protocol.NewState()
	st.SetEQLocal("MXV", 30)  // user just set 30, hold armed
	st.ApplyTunnel("MXV", 99) // stale echo within hold -> ignored
	if v, _ := st.EQValue("MXV"); v != 30 {
		t.Errorf("MXV=%d want 30 (echo should be held off)", v)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func readUntilContains(t *testing.T, conn net.Conn, want string) string {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var buf []byte
	tmp := make([]byte, 256)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if strings.Contains(string(buf), want) {
				return string(buf)
			}
		}
		if err != nil {
			return string(buf)
		}
	}
}

// TestTunnelSend: a failed write hands the command back for the next connection;
// stale intent notes and drops; unknown codes never reach the wire.
func TestTunnelSend(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close() // a locally-closed conn fails its next Write deterministically

	st := protocol.NewState()
	carry, dead := tunnelSend(st, conn, EQCommand{Code: "BAS", Val: 5, TS: time.Now()})
	if carry == nil || !dead || carry.Code != "BAS" || carry.Val != 5 {
		t.Errorf("failed write = (%+v, %v), want the command carried and dead=true", carry, dead)
	}

	st2 := protocol.NewState()
	carry, dead = tunnelSend(st2, conn, EQCommand{
		Code: "BAS", Val: 5, TS: time.Now().Add(-EQCommandDeadline - time.Second),
	})
	if carry != nil || dead {
		t.Errorf("stale command = (%+v, %v), want dropped without killing the conn", carry, dead)
	}
	if e := st2.Snap().Error; !strings.Contains(e, "command not delivered") {
		t.Errorf("stale note = %q, want 'command not delivered'", e)
	}

	if carry, dead = tunnelSend(st, conn, EQCommand{Code: "NOPE", Val: 1, TS: time.Now()}); carry != nil || dead {
		t.Errorf("unknown code = (%+v, %v), want silently dropped", carry, dead)
	}
}

// TestTunnelCarryStaleDroppedWhileDown: a carried command ages out visibly even
// while the tunnel stays unreachable, instead of applying minutes later.
func TestTunnelCarryStaleDroppedWhileDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listening -> dial fails fast
	t.Setenv("LP10_TUNNEL_ADDR", addr)

	st := protocol.NewState()
	control := newRunControl()
	control.stop.Set() // waits return immediately
	stale := &EQCommand{Code: "MXV", Val: 40, TS: time.Now().Add(-EQCommandDeadline - time.Second)}
	_, carry := tunnelOnceContext(context.Background(), control, st, config.Config{Host: "unused"}, make(chan EQCommand), InitialBackoff, stale)
	if carry != nil {
		t.Errorf("carry = %+v, want dropped as stale", carry)
	}
	if e := st.Snap().Error; !strings.Contains(e, "command not delivered") {
		t.Errorf("note = %q, want 'command not delivered'", e)
	}
}

// TestTunnelCarryDeliveredOnNextConnection: a command carried from a dead
// connection is written (clamped) once the next connection is up.
func TestTunnelCarryDeliveredOnNextConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("LP10_TUNNEL_ADDR", ln.Addr().String())

	st := protocol.NewState()
	control := newRunControl()
	done := make(chan *EQCommand, 1)
	go func() {
		_, carry := tunnelOnceContext(context.Background(), control, st, config.Config{Host: "unused"},
			make(chan EQCommand), InitialBackoff, &EQCommand{Code: "BAS", Val: 99, TS: time.Now()})
		done <- carry
	}()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()
	if got := readUntilContains(t, conn, "BAS:10;"); !strings.Contains(got, "BAS:10;") {
		t.Errorf("server received %q, want the carried command (clamped) after the seed", got)
	}

	control.stop.Set()
	if carry := <-done; carry != nil {
		t.Errorf("carry = %+v, want consumed after delivery", carry)
	}
}
