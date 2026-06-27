package workers

// coverage_test.go targets the statement-coverage gaps left by workers_test.go,
// tunnel_test.go, and art_test.go. It only adds tests (TestCov_*) and a couple of
// local helpers; it never touches production code or the other _test.go files.
// Helpers reused from the sibling test files (same package): waitFor, fakeProc,
// waitUntil, readUntilContains.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/transport"
)

// covExitSSH writes a fake ssh that exits 0 immediately with no output (clean
// EOF, no records), so streamOnce runs its read-loop-empty -> reap -> classify
// tail deterministically.
func covExitSSH(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "exit0-ssh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// covPanicWriter is a live stdin whose Write panics, to exercise commandOnce's
// recover() handler (fault injection in the test only; prod is untouched).
type covPanicWriter struct{}

func (covPanicWriter) Write([]byte) (int, error) { panic("write boom") }
func (covPanicWriter) Close() error              { return nil }

// ---- pure-ish helpers -------------------------------------------------------

func TestCov_WaitBackoff(t *testing.T) {
	st := protocol.NewState() // Stop NOT set: the wait elapses and the value doubles
	if got := waitBackoff(st, time.Millisecond); got != 2*time.Millisecond {
		t.Errorf("doubling: got %v want 2ms", got)
	}
	// 1600ms*2 = 3200ms > MaxBackoff(3s) -> capped. (~1.6s wait, Stop unset.)
	if got := waitBackoff(st, 1600*time.Millisecond); got != MaxBackoff {
		t.Errorf("cap: got %v want %v", got, MaxBackoff)
	}
	// Stop set: Wait returns immediately, backoff is returned unchanged.
	stopped := protocol.NewState()
	stopped.Stop.Set()
	if got := waitBackoff(stopped, 1234*time.Millisecond); got != 1234*time.Millisecond {
		t.Errorf("stop set: got %v want 1234ms (unchanged)", got)
	}
}

func TestCov_TunnelAddr(t *testing.T) {
	t.Setenv("LP10_TUNNEL_ADDR", "1.2.3.4:9999")
	if a := tunnelAddr(config.Config{Host: "ignored"}); a != "1.2.3.4:9999" {
		t.Errorf("override: got %q", a)
	}
	t.Setenv("LP10_TUNNEL_ADDR", "") // unset -> host:2018
	if a := tunnelAddr(config.Config{Host: "dev.local"}); a != "dev.local:2018" {
		t.Errorf("default: got %q want dev.local:2018", a)
	}
}

func TestCov_Clip160(t *testing.T) {
	long := strings.Repeat("z", 200)
	if got := clip160(long); len(got) != 160 || got != long[:160] {
		t.Errorf("clip: len=%d want 160", len(got))
	}
	if got := clip160("short"); got != "short" {
		t.Errorf("short string mutated: %q", got)
	}
}

func TestCov_LaterTime(t *testing.T) {
	early := time.Now()
	late := early.Add(time.Second)
	if got := laterTime(early, late); !got.Equal(late) { // b after a -> b
		t.Errorf("b-later: got %v want %v", got, late)
	}
	if got := laterTime(late, early); !got.Equal(late) { // b not after a -> a
		t.Errorf("a-later: got %v want %v", got, late)
	}
}

func TestCov_BoundedLines(t *testing.T) {
	// A line with no trailing newline is returned, then EOF reports (",", false).
	next := boundedLines(strings.NewReader("hello"))
	if s, ok := next(); !ok || s != "hello" {
		t.Fatalf("no-newline line = %q,%v want hello,true", s, ok)
	}
	if s, ok := next(); ok || s != "" {
		t.Fatalf("at EOF = %q,%v want '',false", s, ok)
	}

	// Two newline-terminated lines, then EOF.
	next = boundedLines(strings.NewReader("a\nb\n"))
	if s, ok := next(); !ok || s != "a\n" {
		t.Fatalf("line1 = %q,%v", s, ok)
	}
	if s, ok := next(); !ok || s != "b\n" {
		t.Fatalf("line2 = %q,%v", s, ok)
	}
	if _, ok := next(); ok {
		t.Fatal("expected EOF after two lines")
	}

	// A newline-free run longer than maxLine is split into <=maxLine chunks,
	// losslessly (the first chunk is exactly maxLine).
	long := strings.Repeat("a", maxLine+4464)
	next = boundedLines(strings.NewReader(long))
	first, ok := next()
	if !ok || len(first) != maxLine {
		t.Fatalf("first chunk len=%d want %d", len(first), maxLine)
	}
	total := first
	for {
		s, more := next()
		if !more {
			break
		}
		total += s
	}
	if total != long {
		t.Fatalf("reassembled %d bytes, want %d", len(total), len(long))
	}
}

// ---- the :2018 control tunnel ----------------------------------------------

// TestCov_TunnelRoundTripFloodReconnect drives a frame in, a command out, a
// separator-free flood (dropped, framing survives), then a server-side close
// that forces a reconnect.
func TestCov_TunnelRoundTripFloodReconnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("LP10_TUNNEL_ADDR", ln.Addr().String())

	st := protocol.NewState()
	eqcmds := make(chan EQCommand, 8)
	go TunnelWorker(st, config.Config{Host: "unused"}, eqcmds)
	defer st.Stop.Set()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept1: %v", err)
	}

	// Device broadcast -> State reflects it, link marked live.
	if _, err := conn.Write([]byte("MXV:50;BAS:3;")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "MXV applied", func() bool { v, ok := st.EQValue("MXV"); return ok && v == 50 })
	if v, ok := st.EQValue("BAS"); !ok || v != 3 {
		t.Errorf("BAS=%d,%v want 3", v, ok)
	}
	if c, _ := st.EQView(); !c {
		t.Error("tunnel should be marked connected")
	}

	// Queued command reaches the device, clamped, as CODE:VAL;.
	eqcmds <- EQCommand{Code: "BAS", Val: 99} // clamps to +10
	if got := readUntilContains(t, conn, "BAS:10;"); !strings.Contains(got, "BAS:10;") {
		t.Errorf("server got %q, want it to contain BAS:10;", got)
	}

	// A separator-free flood beyond tunnelCarryMax is dropped, but framing
	// survives: the following frame (after a ';' to flush the junk carry) parses.
	if _, err := conn.Write([]byte(strings.Repeat("X", 30000))); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(";TRE:5;")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "TRE after flood", func() bool { v, ok := st.EQValue("TRE"); return ok && v == 5 })

	// Closing the device end forces a reconnect: the worker re-dials, we accept.
	conn.Close()
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
	conn2, err := ln.Accept()
	if err != nil {
		t.Fatalf("reconnect accept: %v", err)
	}
	defer conn2.Close()
	waitUntil(t, "reconnected", func() bool { c, _ := st.EQView(); return c })
}

// TestCov_TunnelDialFailure points the worker at a closed port: the dial is
// refused and the tunnel is marked disconnected.
func TestCov_TunnelDialFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens now -> dial refused
	t.Setenv("LP10_TUNNEL_ADDR", addr)

	st := protocol.NewState()
	st.SetEQConnected(true) // a stale "connected" the dial-failure path must clear
	go TunnelWorker(st, config.Config{Host: "unused"}, make(chan EQCommand))
	defer st.Stop.Set()

	if !waitFor(func() bool { c, _ := st.EQView(); return !c }, 3*time.Second) {
		t.Error("a refused dial should mark the tunnel disconnected")
	}
}

// TestCov_TunnelStopDuringSeed sets Stop while the worker is mid-seed, exercising
// the seed loop's Stop check and the Stop-set return.
func TestCov_TunnelStopDuringSeed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("LP10_TUNNEL_ADDR", ln.Addr().String())

	st := protocol.NewState()
	done := make(chan struct{})
	go func() { TunnelWorker(st, config.Config{Host: "unused"}, make(chan EQCommand)); close(done) }()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()

	st.Stop.Set() // interrupt the on-connect seed loop -> worker exits

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("worker should exit when stop fires during seeding")
	}
}

// ---- command worker ---------------------------------------------------------

// TestCov_CommandFlushWithPending: an undeliverable command (no live proc)
// followed by the drain sentinel -> the sentinel is consumed in the drain loop
// and the flush reports the still-pending command as undelivered.
func TestCov_CommandFlushWithPending(t *testing.T) {
	st := protocol.NewState() // no proc -> nothing can be written
	cmds := make(chan *protocol.Command, 4)
	cmds <- &protocol.Command{Mid: 64, Data: "30", TS: time.Now()} // fresh, undeliverable
	cmds <- nil                                                    // drain sentinel, picked up in the drain loop
	go CommandWorker(st, cmds, CommandDeadline)

	if !st.Drained.Wait(3 * time.Second) {
		t.Fatal("worker should drain on the sentinel")
	}
	if e := st.Snap().Error; e != "command not delivered" {
		t.Errorf("error=%q want 'command not delivered' (pending at flush)", e)
	}
}

// TestCov_CommandFailedSendStopExits: a fresh command fails to send (no proc),
// and Stop fires during the post-failure 200ms wait, breaking the worker.
func TestCov_CommandFailedSendStopExits(t *testing.T) {
	st := protocol.NewState() // no proc -> send fails
	cmds := make(chan *protocol.Command, 4)
	done := make(chan struct{})
	go func() { CommandWorker(st, cmds, CommandDeadline); close(done) }()

	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: time.Now()}
	time.Sleep(60 * time.Millisecond) // worker is now in the post-failure 200ms wait
	st.Stop.Set()                     // wakes that wait -> commandOnce returns true

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("worker should exit when stop fires during the failed-send wait")
	}
}

// TestCov_CommandWorkerRecoversPanic injects a panicking stdin so commandOnce's
// recover() handler runs (notes the error, does not die). The channel is empty
// afterwards, so the worker simply spins until Stop.
func TestCov_CommandWorkerRecoversPanic(t *testing.T) {
	st := protocol.NewState()
	st.StartProc(&protocol.Proc{Stdin: covPanicWriter{}, Done: make(chan struct{})}) // live (spawnedAt=now)
	cmds := make(chan *protocol.Command, 4)
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: time.Now()}
	go CommandWorker(st, cmds, CommandDeadline)
	defer st.Stop.Set()

	if !waitFor(func() bool { return strings.Contains(st.Snap().Error, "command worker") }, 4*time.Second) {
		t.Fatalf("recover note not set; error=%q", st.Snap().Error)
	}
}

// ---- watchdog ---------------------------------------------------------------

// TestCov_WatchdogNilTickThenSilentAfterData covers the no-proc early return
// (a tick with sproc nil) and the post-data silent kill (got=true -> silentAfter).
func TestCov_WatchdogNilTickThenSilentAfterData(t *testing.T) {
	st := protocol.NewState()
	// silentAfter tiny, connect/dataless large: only the post-data silent path kills.
	go Watchdog(st, 100*time.Millisecond, 5*time.Second, 5*time.Second)
	defer st.Stop.Set()

	time.Sleep(700 * time.Millisecond) // one tick runs with no proc (early return)

	proc := fakeProc(t, "sleep", "30")
	st.StartProc(proc)
	protocol.ApplyRecord(st, protocol.Record{"v": {"Data:44"}}) // gotRecord=true, lastData/lastRx=now
	if !proc.WaitTimeout(3 * time.Second) {
		t.Error("watchdog should kill a proc that went silent after delivering data")
	}
}

// ---- stream worker / streamOnce / reap --------------------------------------

// TestCov_ReapKillsUnresponsiveChild: a child that ignores its closed stdin is
// killed by reap after the grace wait.
func TestCov_ReapKillsUnresponsiveChild(t *testing.T) {
	st := protocol.NewState()
	proc := fakeProc(t, "sleep", "30") // ignores stdin close
	st.StartProc(proc)

	reap(st, proc) // closes stdin (ignored) -> WaitTimeout grace -> Kill

	if st.Sproc() != nil {
		t.Error("reap should clear sproc")
	}
	if !proc.WaitTimeout(2 * time.Second) {
		t.Error("reap should have killed the unresponsive child")
	}
}

// TestCov_StreamOnceCreateTempFailure points TMPDIR at a missing dir so the
// stderr temp file can't be created; streamOnce notes it and returns the backoff
// unchanged. Stop is pre-set so the 3s wait returns immediately.
func TestCov_StreamOnceCreateTempFailure(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no-such-subdir"))
	st := protocol.NewState()
	st.Stop.Set() // so the post-failure 3s wait is instant

	next := streamOnce(st, config.Config{}, InitialBackoff)
	if next != InitialBackoff {
		t.Errorf("backoff=%v want unchanged %v", next, InitialBackoff)
	}
	if e := st.Snap().Error; !strings.Contains(e, "cannot start ssh") {
		t.Errorf("error=%q want it to contain 'cannot start ssh'", e)
	}
}

// TestCov_StreamOnceBackoffCaps connects to an instantly-exiting ssh (clean EOF,
// no records) and checks the trailing backoff doubles and caps at MaxBackoff.
func TestCov_StreamOnceBackoffCaps(t *testing.T) {
	t.Setenv("LP10_SSH", covExitSSH(t))
	st := protocol.NewState() // Stop NOT set: the trailing backoff wait elapses
	// 1600ms*2 = 3200ms > MaxBackoff -> capped (~1.6s wait).
	if next := streamOnce(st, config.Config{}, 1600*time.Millisecond); next != MaxBackoff {
		t.Errorf("backoff=%v want capped %v", next, MaxBackoff)
	}
}

// TestCov_StreamWorkerRecoversPanic makes classify panic (the one injectable hook
// inside streamOnce) so StreamWorker's recover() handler runs and notes it.
func TestCov_StreamWorkerRecoversPanic(t *testing.T) {
	t.Setenv("LP10_SSH", covExitSSH(t))
	orig := classify
	classify = func(string) *transport.TransportError { panic("classify boom") }

	st := protocol.NewState()
	done := make(chan struct{})
	go func() { StreamWorker(st, config.Config{}); close(done) }()
	defer func() {
		st.Stop.Set()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("stream worker did not exit after stop")
		}
		classify = orig // restore only after the worker goroutine has exited (no race)
	}()

	if !waitFor(func() bool { return strings.Contains(st.Snap().Error, "stream worker") }, 6*time.Second) {
		t.Fatalf("recover note not set; error=%q", st.Snap().Error)
	}
}

// ---- teardown ---------------------------------------------------------------

// TestCov_TeardownSigtermLadder: a child that ignores its closed stdin but dies
// on SIGTERM exercises the first wait + SIGTERM rung.
func TestCov_TeardownSigtermLadder(t *testing.T) {
	st := protocol.NewState()
	st.StartProc(fakeProc(t, "sleep", "30")) // ignores stdin close; dies on SIGTERM
	proc := st.Sproc()

	Teardown(st, make(chan *protocol.Command, 1), 50*time.Millisecond)

	if !proc.WaitTimeout(2 * time.Second) {
		t.Error("sleep should be terminated by the SIGTERM rung")
	}
	if !st.Stop.IsSet() {
		t.Error("teardown must set stop")
	}
}

// TestCov_TeardownKillLadder: a child that ignores both its closed stdin and
// SIGTERM forces the final SIGKILL rung.
func TestCov_TeardownKillLadder(t *testing.T) {
	st := protocol.NewState()
	st.StartProc(fakeProc(t, "sh", "-c", "trap '' TERM; while true; do sleep 1; done"))
	proc := st.Sproc()

	Teardown(st, make(chan *protocol.Command, 1), 50*time.Millisecond)

	if !proc.WaitTimeout(2 * time.Second) {
		t.Error("a TERM-trapping process should be SIGKILLed by the final rung")
	}
}

// ---- art worker -------------------------------------------------------------

// TestCov_ArtTransientFailureBacksOff: a 5xx (not ErrUndecodable) is a transient
// failure -> backed off, not retried within the delay, no art set.
func TestCov_ArtTransientFailureBacksOff(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := protocol.NewState()
	st.Preload(protocol.Track{"TrackName": "x", "CoverArtUrl": srv.URL}, 0, 50)
	go ArtWorker(st, config.Config{Art: true, ArtMode: "auto"})
	defer st.Stop.Set()

	if !waitFor(func() bool { return atomic.LoadInt32(&hits) >= 1 }, 3*time.Second) {
		t.Fatal("transient endpoint never hit")
	}
	time.Sleep(2*artPoll + 100*time.Millisecond) // would re-fetch if not backed off
	if st.Snap().Art != nil {
		t.Error("a failing cover must not set art")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("hits=%d want 1 (backed off after transient failure)", n)
	}
}

// TestCov_ArtUndecodableGivesUp: a 200 with non-image bytes is undecodable, a
// permanent failure -> the url is marked loaded and never retried.
func TestCov_ArtUndecodableGivesUp(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("this is definitely not an image"))
	}))
	defer srv.Close()

	st := protocol.NewState()
	st.Preload(protocol.Track{"TrackName": "x", "CoverArtUrl": srv.URL}, 0, 50)
	go ArtWorker(st, config.Config{Art: true, ArtMode: "auto"})
	defer st.Stop.Set()

	if !waitFor(func() bool { return atomic.LoadInt32(&hits) >= 1 }, 3*time.Second) {
		t.Fatal("undecodable endpoint never hit")
	}
	time.Sleep(2*artPoll + 100*time.Millisecond) // would re-fetch if not given up on
	if st.Snap().Art != nil {
		t.Error("an undecodable cover must not set art")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("hits=%d want 1 (undecodable never retried)", n)
	}
}
