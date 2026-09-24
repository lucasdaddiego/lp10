package workers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/fixtures"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/testutil"
	"github.com/lucasdaddiego/lp10/internal/transport"
)

func waitFor(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pred()
}

// testPingHost is the ping_host every spawning test runs with: an IPv4
// literal, so the laptop-side lookup at each spawn (transport.PingTarget) puts
// no DNS query on the wire.
const testPingHost = "192.0.2.1"

type startOpts struct {
	fastFatal bool
	watchdog  *struct{ silent, connect, dataless time.Duration }
	ssh       string
	snapshot  string
}

type harness struct {
	t       *testing.T
	st      *protocol.State
	procs   *processSlot
	control *runControl
	fakeSSH string
	tmp     string
	wg      sync.WaitGroup
	restore func() // package-var restore, run after the worker join (see newHarness)
}

func newHarness(t *testing.T) *harness {
	testutil.Isolate(t)
	tmp := t.TempDir()
	t.Setenv("LP10_FAKE_DIR", tmp)
	h := &harness{
		t: t, st: protocol.NewState(), procs: newProcessSlot(), control: newRunControl(),
		fakeSSH: testutil.FakeSSH(t), tmp: tmp,
	}
	t.Cleanup(func() {
		h.control.stop.Set()
		if proc, _ := h.procs.current(); proc != nil {
			if proc.Cmd.Process != nil {
				proc.Cmd.Process.Kill()
			}
			proc.waitTimeout(3 * time.Second)
		}
		// Join the worker goroutines: a worker leaked past its test races with
		// the next test's package-var writes (classify, backoffResetAfter).
		done := make(chan struct{})
		go func() { h.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("harness workers did not exit after stop")
		}
		// Restore package vars ONLY after the join: a worker still calling
		// classify() would otherwise race this write (t.Cleanup is LIFO, so a
		// restore registered in start() would run before this join).
		if h.restore != nil {
			h.restore()
		}
	})
	return h
}

func (h *harness) start(scenario string, opts startOpts) *protocol.State {
	ssh := h.fakeSSH
	if opts.ssh != "" {
		ssh = opts.ssh
	}
	h.t.Setenv("LP10_SSH", ssh)
	h.t.Setenv("LP10_FAKE_SCENARIO", scenario)
	if opts.fastFatal {
		orig := classify
		classify = func(s string) *transport.TransportError {
			if e := orig(s); e != nil {
				e.Cadence = 200 * time.Millisecond
				return e
			}
			return nil
		}
		h.restore = func() { classify = orig } // run post-join, see newHarness
	}
	cfg := config.Load()
	cfg.PingHost = testPingHost
	h.wg.Go(func() { streamWorker(context.Background(), h.st, cfg, opts.snapshot, h.procs, h.control) })
	if opts.watchdog != nil {
		w := opts.watchdog
		if w.dataless == 0 {
			w.dataless = DatalessAfter
		}
		h.wg.Go(func() { watchdog(h.st, h.procs, h.control, w.silent, w.connect, w.dataless) })
	}
	return h.st
}

// ---- integration: stream/command/watchdog against the fake transport --------

func TestNormalStreamConnectsAndParses(t *testing.T) {
	h := newHarness(t)
	st := h.start("normal", startOpts{})
	if !waitFor(func() bool { return st.Snap().Connected }, 6*time.Second) {
		t.Fatal("never connected")
	}
	s := st.Snap()
	if s.Track == nil || s.Track.TrackName != "De Música Ligera" || s.Vol != 44 || s.Playing != 0 {
		t.Errorf("snap = %+v", s)
	}
}

func TestGarbageStreamStillParses(t *testing.T) {
	h := newHarness(t)
	st := h.start("garbage", startOpts{})
	if !waitFor(func() bool { return st.Snap().Track != nil }, 6*time.Second) {
		t.Fatal("track never parsed")
	}
	// raw pos 31000 only arrives via post-noise heartbeats, proving the parser
	// survives mid-stream garbage (not just the leading record's 30000).
	if !waitFor(func() bool { return st.RawPos() >= 31000 }, 6*time.Second) {
		t.Fatal("post-garbage heartbeat never parsed")
	}
}

// A session that logs in and ends on its own reconnects on the backoff; when
// that repeats — a loop that dies right after the login, every session — the
// stall streak holds the logins apart exactly as it does for watchdog kills:
// each one is a login the box's sshd counts toward its lockout.
func TestEofReconnects(t *testing.T) {
	h := newHarness(t)
	origHold, origMax := stallHold, stallHoldMax
	stallHold, stallHoldMax = 700*time.Millisecond, 1400*time.Millisecond
	h.restore = func() { stallHold, stallHoldMax = origHold, origMax } // post-join, see newHarness
	st := h.start("eof", startOpts{})
	if !waitFor(func() bool { return st.RawAttempts() >= 2 }, 6*time.Second) {
		t.Fatalf("attempts = %d, want a reconnect after the first EOF", st.RawAttempts())
	}
	if !waitFor(func() bool { return strings.HasPrefix(st.Snap().Error, "2 short sessions in a row") }, 6*time.Second) {
		t.Fatalf("note = %q, want the hold after two short logins", st.Snap().Error)
	}
	noted := st.Snap().ErrorAt
	if !waitFor(func() bool { return st.RawAttempts() >= 3 }, 6*time.Second) {
		t.Fatalf("attempts = %d, want the third login after the hold", st.RawAttempts())
	}
	if gap := time.Since(noted); gap < stallHold {
		t.Errorf("the third login came %v after the note, want at least the %v hold", gap, stallHold)
	}
}

func TestSilentStreamTripsWatchdogAndRecycles(t *testing.T) {
	h := newHarness(t)
	st := h.start("silent", startOpts{watchdog: &struct{ silent, connect, dataless time.Duration }{
		silent: 300 * time.Millisecond, connect: 2 * time.Second}})
	if !waitFor(func() bool { return st.RawAttempts() >= 2 }, 8*time.Second) {
		t.Fatalf("attempts = %d, want >= 2", st.RawAttempts())
	}
}

func TestDatalessFromBirthRecycles(t *testing.T) {
	h := newHarness(t)
	st := h.start("dataless", startOpts{watchdog: &struct{ silent, connect, dataless time.Duration }{
		silent: 10 * time.Second, connect: 10 * time.Second, dataless: 400 * time.Millisecond}})
	if !waitFor(func() bool { return st.RawAttempts() >= 2 }, 8*time.Second) {
		t.Fatalf("attempts = %d, want >= 2", st.RawAttempts())
	}
}

func TestSnapshotPersistsDuringStream(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.tmp, "snap.json")
	h.start("normal", startOpts{snapshot: path})
	// The device sends its one-shot @@i block before the first PlayView, so the
	// very first persisted snapshot can be track-less; wait for the track itself.
	if !waitFor(func() bool {
		snap := config.LoadSnapshot(path)
		if snap == nil {
			return false
		}
		return snap.Track != nil && snap.Track.TrackName == "De Música Ligera"
	}, 8*time.Second) {
		t.Fatalf("track snapshot never persisted: %v", config.LoadSnapshot(path))
	}
}

func TestSpawnFailureIsNotedAndRetried(t *testing.T) {
	h := newHarness(t)
	st := h.start("normal", startOpts{ssh: filepath.Join(h.tmp, "missing-ssh")})
	if !waitFor(func() bool { return strings.Contains(st.Snap().Error, "cannot start ssh") }, 6*time.Second) {
		t.Fatalf("error = %q, want 'cannot start ssh'", st.Snap().Error)
	}
}

func TestUnclassifiedSSHStderrIsSurfaced(t *testing.T) {
	h := newHarness(t)
	fail := filepath.Join(h.tmp, "failing-ssh")
	os.WriteFile(fail, []byte("#!/bin/sh\necho 'ssh: Could not resolve hostname nope' >&2\nexit 255\n"), 0o755)
	st := h.start("normal", startOpts{ssh: fail})
	if !waitFor(func() bool { return strings.Contains(st.Snap().Error, "Could not resolve hostname") }, 6*time.Second) {
		t.Fatalf("error = %q", st.Snap().Error)
	}
	if st.Snap().Fatal {
		t.Error("a transient stderr must not be fatal")
	}
}

func TestAuthfailIsFatalWithRemediation(t *testing.T) {
	h := newHarness(t)
	st := h.start("authfail", startOpts{fastFatal: true})
	if !waitFor(func() bool { return st.Snap().Fatal }, 6*time.Second) {
		t.Fatal("never went fatal")
	}
	e := st.Snap().Error
	if !strings.Contains(e, "password rejected") || !strings.Contains(e, transport.StoreHint) {
		t.Errorf("error = %q", e)
	}
}

func TestKeychainLockedIsDistinct(t *testing.T) {
	h := newHarness(t)
	st := h.start("keychain-locked", startOpts{fastFatal: true})
	if !waitFor(func() bool { return st.Snap().Fatal }, 6*time.Second) {
		t.Fatal("never went fatal")
	}
	e := st.Snap().Error
	if !strings.Contains(e, "is locked") || strings.Contains(e, "password rejected") {
		t.Errorf("error = %q", e)
	}
}

func TestHealClearsFatalError(t *testing.T) {
	h := newHarness(t)
	t.Setenv("LP10_FAKE_HEAL_AFTER", "1")
	st := h.start("heal", startOpts{fastFatal: true})
	if !waitFor(func() bool { return st.Snap().Fatal }, 6*time.Second) {
		t.Fatal("first attempt should be fatal")
	}
	if !waitFor(func() bool { return st.Snap().Connected }, 15*time.Second) {
		t.Fatal("never healed to connected")
	}
	s := st.Snap()
	if s.Fatal || s.Error != "" {
		t.Errorf("after heal: fatal=%v error=%q", s.Fatal, s.Error)
	}
}

func TestCommandsReachDeviceAndTeardownIsClean(t *testing.T) {
	h := newHarness(t)
	log := filepath.Join(h.tmp, "cmdlog")
	t.Setenv("LP10_FAKE_CMDLOG", log)
	st := h.start("normal", startOpts{})
	cmds := make(chan *protocol.Command, 64)
	go commandWorker(st, h.procs, h.control, cmds, CommandDeadline)
	if !waitFor(func() bool { return st.Snap().Connected }, 6*time.Second) {
		t.Fatal("never connected")
	}
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: time.Now()}
	cmds <- &protocol.Command{Mid: 64, Data: "30", TS: time.Now()}
	if !waitFor(func() bool { return logContains(log, "40 NEXT") && logContains(log, "64 30") }, 6*time.Second) {
		t.Fatalf("cmdlog = %q", readFile(log))
	}
	proc, _ := h.procs.current()
	teardown(st, h.procs, h.control, cmds, DrainTimeout, "")
	if proc != nil && !proc.waitTimeout(3*time.Second) {
		t.Error("child not reaped after teardown")
	}
}

func TestFailedSendsDeliverInOrderAfterReconnect(t *testing.T) {
	h := newHarness(t)
	log := filepath.Join(h.tmp, "ordlog")
	t.Setenv("LP10_FAKE_CMDLOG", log)
	cmds := make(chan *protocol.Command, 64)
	go commandWorker(h.st, h.procs, h.control, cmds, 15*time.Second)
	now := time.Now()
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: now}
	cmds <- &protocol.Command{Mid: 40, Data: "PREV", TS: now}
	time.Sleep(500 * time.Millisecond)
	h.start("normal", startOpts{})
	if !waitFor(func() bool { return logContains(log, "40 NEXT") && logContains(log, "40 PREV") }, 8*time.Second) {
		t.Fatalf("cmdlog = %q", readFile(log))
	}
	txt := readFile(log)
	if strings.Index(txt, "40 NEXT") >= strings.Index(txt, "40 PREV") {
		t.Errorf("order not preserved: %q", txt)
	}
}

func TestStaleCommandsDropVisibly(t *testing.T) {
	h := newHarness(t) // no stream started: command stays queued and ages out
	cmds := make(chan *protocol.Command, 64)
	go commandWorker(h.st, h.procs, h.control, cmds, 200*time.Millisecond)
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: time.Now().Add(-time.Second)}
	if !waitFor(func() bool { return h.st.Snap().Error == "command not delivered" }, 6*time.Second) {
		t.Fatalf("error = %q", h.st.Snap().Error)
	}
}

func TestFreshCommandNotAgedByOlderPendingOne(t *testing.T) {
	h := newHarness(t)
	log := filepath.Join(h.tmp, "agelog")
	t.Setenv("LP10_FAKE_CMDLOG", log)
	cmds := make(chan *protocol.Command, 64)
	go commandWorker(h.st, h.procs, h.control, cmds, 4*time.Second)
	now := time.Now()
	cmds <- &protocol.Command{Mid: 64, Data: "10", TS: now.Add(-3700 * time.Millisecond)} // old but not stale
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: now}                             // fresh
	time.Sleep(1 * time.Second)                                                           // old one expires while pending
	h.start("normal", startOpts{})
	if !waitFor(func() bool { return logContains(log, "40 NEXT") }, 8*time.Second) {
		t.Fatalf("fresh command never delivered; cmdlog = %q", readFile(log))
	}
}

// ---- targeted unit tests ----------------------------------------------------

func TestSelfSnapReturnsSubsetOfState(t *testing.T) {
	snap := selfSnap(protocol.NewState())
	if snap.Track != nil || snap.Pos != 0 || snap.Playing != 2 || snap.Vol != 0 {
		t.Errorf("cached player state = %+v", snap)
	}
	if snap.EQ == nil {
		t.Error("cached EQ map should be initialized")
	}
}

func TestTeardownSetsStop(t *testing.T) {
	st := protocol.NewState()
	control := newRunControl()
	teardown(st, newProcessSlot(), control, make(chan *protocol.Command, 1), 100*time.Millisecond, "")
	if !control.stop.IsSet() {
		t.Error("teardown should set stop")
	}
}

func TestCommandWorkerExitsOnStop(t *testing.T) {
	st := protocol.NewState()
	control := newRunControl()
	control.stop.Set()
	done := make(chan struct{})
	go func() {
		commandWorker(st, newProcessSlot(), control, make(chan *protocol.Command), CommandDeadline)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("command worker did not exit on stop")
	}
}

func TestCommandWorkerDrainsOnSentinel(t *testing.T) {
	st := protocol.NewState()
	control := newRunControl()
	cmds := make(chan *protocol.Command, 1)
	cmds <- nil // drain sentinel
	go commandWorker(st, newProcessSlot(), control, cmds, CommandDeadline)
	if !control.drained.Wait(2 * time.Second) {
		t.Error("drained should be set after the sentinel")
	}
}

func TestWatchdogExitsOnStop(t *testing.T) {
	st := protocol.NewState()
	control := newRunControl()
	control.stop.Set()
	done := make(chan struct{})
	go func() {
		watchdog(st, newProcessSlot(), control, SilentAfter, ConnectWindow, DatalessAfter)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("watchdog did not exit on stop")
	}
}

func TestProcessWaitTimeout(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if !(&process{Done: done}).waitTimeout(time.Second) {
		t.Error("a closed Done channel should report an exited process")
	}
	if (&process{Done: make(chan struct{})}).waitTimeout(10 * time.Millisecond) {
		t.Error("a live process should time out")
	}
}

func TestRunSignalWait(t *testing.T) {
	alreadySet := newRunSignal()
	alreadySet.Set()
	alreadySet.Set() // idempotent
	if !alreadySet.IsSet() || !alreadySet.Wait(time.Second) {
		t.Error("a set signal should remain set and return immediately")
	}

	concurrent := newRunSignal()
	go func() {
		time.Sleep(10 * time.Millisecond)
		concurrent.Set()
	}()
	if !concurrent.Wait(2 * time.Second) {
		t.Error("Wait should observe a concurrent Set")
	}

	if newRunSignal().Wait(10 * time.Millisecond) {
		t.Error("an unset signal should time out")
	}
}

func TestProcessSlotRejectsStaleClear(t *testing.T) {
	st := protocol.NewState()
	procs := newProcessSlot()
	first := &process{}
	second := &process{}
	procs.start(st, first)
	procs.start(st, second)

	if procs.clear(first) {
		t.Error("a late cleanup must not clear a newer process")
	}
	if current, _ := procs.current(); current != second {
		t.Errorf("current process = %p, want newer process %p", current, second)
	}
	if !procs.clear(second) {
		t.Error("the current process should clear")
	}
	if current, spawned := procs.current(); current != nil || !spawned.IsZero() {
		t.Errorf("cleared slot = (%p, %v), want nil and zero time", current, spawned)
	}
	if attempts := st.RawAttempts(); attempts != 2 {
		t.Errorf("connection attempts = %d, want 2", attempts)
	}
}

func fakeProc(t *testing.T, name string, args ...string) *process {
	t.Helper()
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &process{Cmd: cmd, Stdin: stdin, Done: make(chan struct{})}
	go func() { cmd.Wait(); close(p.Done) }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	return p
}

func TestWatchdogKillsWedgedProcess(t *testing.T) {
	st := protocol.NewState()
	procs := newProcessSlot()
	control := newRunControl()
	proc := fakeProc(t, "sleep", "60")
	procs.start(st, proc)
	go watchdog(st, procs, control, SilentAfter, 300*time.Millisecond, DatalessAfter)
	defer control.stop.Set()
	if !proc.waitTimeout(3 * time.Second) {
		t.Error("watchdog should have killed the connecting-but-silent process")
	}
}

func TestWatchdogNoProcessIsHarmless(t *testing.T) {
	st := protocol.NewState()
	control := newRunControl()
	go watchdog(st, newProcessSlot(), control, 100*time.Millisecond, 500*time.Millisecond, time.Second)
	time.Sleep(150 * time.Millisecond)
	control.stop.Set() // must not have panicked with a nil proc
}

func TestReapClosesStdinAndClears(t *testing.T) {
	st := protocol.NewState()
	procs := newProcessSlot()
	proc := fakeProc(t, "cat") // cat exits on stdin EOF
	procs.start(st, proc)
	reap(st, procs, proc)
	if current, _ := procs.current(); current != nil {
		t.Error("reap should clear the process slot")
	}
	if !proc.waitTimeout(2 * time.Second) {
		t.Error("cat should exit once its stdin is closed")
	}
}

func TestReapHandlesNilStdin(t *testing.T) {
	st := protocol.NewState()
	cmd := exec.Command("true")
	cmd.Run()
	proc := &process{Cmd: cmd, Done: make(chan struct{})}
	close(proc.Done)
	procs := newProcessSlot()
	procs.start(st, proc)
	reap(st, procs, proc) // nil stdin + already-exited: must not panic
}

func logContains(path, sub string) bool { return strings.Contains(readFile(path), sub) }

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// TestStreamBackoffResetNeedsSustainedSession: a session that emits one record
// and exits (fakessh "eof") must not reset the reconnect backoff — resetting on
// the first record produced constant-cadence ssh churn against the device's
// lockout-prone sshd. Only a session older than backoffResetAfter resets.
func TestStreamBackoffResetNeedsSustainedSession(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("LP10_SSH", testutil.FakeSSH(t))
	t.Setenv("LP10_FAKE_SCENARIO", "eof")

	// Young session: escalation continues (1600ms doubles and caps).
	st := protocol.NewState()
	if next := streamOnce(st, config.Config{}, 1600*time.Millisecond, newRunControl()); next != MaxBackoff {
		t.Errorf("young session: backoff=%v want %v", next, MaxBackoff)
	}
	if s := st.Snap(); s.Track == nil {
		t.Fatal("the eof scenario should have delivered one playing record")
	}

	// With the sustain threshold shortened to zero, the same session resets.
	orig := backoffResetAfter
	backoffResetAfter = 0
	defer func() { backoffResetAfter = orig }()
	if next := streamOnce(protocol.NewState(), config.Config{}, 1600*time.Millisecond, newRunControl()); next != 2*InitialBackoff {
		t.Errorf("sustained session: backoff=%v want %v", next, 2*InitialBackoff)
	}
}

// The loop's internet ping never gets a name: the stream worker resolves
// ping_host on the laptop at each spawn and hands the loop the address, so the
// box's resolvers can never stall a stats tick past the watchdog.
func TestStreamWorkerHandsTheLoopAnAddress(t *testing.T) {
	testutil.Isolate(t)
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	// a fake ssh that keeps the loop (its last argument) and exits: a clean EOF
	ssh := filepath.Join(dir, "record-ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nfor a; do l=$a; done\nprintf '%s' \"$l\" > "+loop+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_SSH", ssh)
	for host, want := range map[string]string{
		"localhost": "ph='127.0.0.1';", // a name, resolved HERE (from the hosts file)
		"192.0.2.7": "ph='192.0.2.7';", // an address is its own answer
	} {
		os.Remove(loop)
		st := protocol.NewState()
		control := newRunControl()
		done := make(chan struct{})
		go func() {
			streamWorker(context.Background(), st, config.Config{Host: "127.0.0.1", PingHost: host}, "", newProcessSlot(), control)
			close(done)
		}()
		spawned := waitFor(func() bool { return strings.Contains(readFile(loop), "ph=") }, 5*time.Second)
		control.stop.Set()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("stream worker did not exit after stop")
		}
		if !spawned {
			t.Fatalf("ping_host %q: the fake ssh never received a loop", host)
		}
		if got := readFile(loop); !strings.Contains(got, want) {
			i := strings.Index(got, "ph=")
			t.Errorf("ping_host %q reached the loop as %q, want %s", host, got[i:min(len(got), i+24)], want)
		}
	}
}

// Three fatal verdicts in a row stretch the retry cadence sixfold: the sshd
// lockout looks exactly like a rejected password, and hammering it every
// cadence only keeps it locked. The harness cadence is 200 ms, so the fourth
// attempt must wait ~1.2 s where the first three came ~200 ms apart.
func TestFatalCadenceEscalatesAfterThreeHits(t *testing.T) {
	h := newHarness(t)
	st := h.start("authfail", startOpts{fastFatal: true})
	if !waitFor(func() bool { return st.RawAttempts() >= 3 }, 6*time.Second) {
		t.Fatal("never reached the third attempt")
	}
	t0 := time.Now()
	if !waitFor(func() bool { return st.RawAttempts() >= 4 }, 10*time.Second) {
		t.Fatal("never reached the fourth attempt")
	}
	if el := time.Since(t0); el < 900*time.Millisecond {
		t.Errorf("fourth attempt came after %v, want the stretched cadence (~1.2 s)", el)
	}
}

// ---- lockout insurance: the stall streak ------------------------------------

// The rule at its real values: the first short stall reconnects on the normal
// backoff, the second holds the next spawn 30 s, and each further one doubles
// the hold, up to 2 minutes.
func TestStallStreakEscalatesToTheCap(t *testing.T) {
	var s stallStreak
	for i, want := range []time.Duration{0, 30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute} {
		if got := s.after(true, 10*time.Second); got != want {
			t.Errorf("stall %d in a row: hold %v, want %v", i+1, got, want)
		}
	}
}

// One stall in a long healthy session is not a pattern: a session that had
// delivered for 2 minutes reconnects on the normal backoff even mid-streak,
// and the next streak starts from scratch. A second less still counts as short.
func TestStallStreakSparesALongSession(t *testing.T) {
	var s stallStreak
	s.after(true, 0)
	s.after(true, 0)
	if got := s.after(true, stallResetAfter-time.Second); got != time.Minute {
		t.Fatalf("a stall just short of stallResetAfter: hold %v, want the streak's 1m0s", got)
	}
	if got := s.after(true, stallResetAfter); got != 0 {
		t.Errorf("a stall after 2 minutes of delivery: hold %v, want 0 (the normal backoff)", got)
	}
	for i, want := range []time.Duration{0, 30 * time.Second} {
		if got := s.after(true, 0); got != want {
			t.Errorf("stall %d of the next streak: hold %v, want %v", i+1, got, want)
		}
	}
}

// A session that delivers for 2 minutes ends the streak however it ends. A
// shorter one that ends any other way (a clean EOF, a refused login) neither
// extends nor breaks it, so such endings between the stalls cannot dodge the
// hold.
func TestStallStreakResetsOnATwoMinuteSession(t *testing.T) {
	var s stallStreak
	s.after(true, 0)
	s.after(true, 0) // the streak holds 30 s now
	if got := s.after(false, 30*time.Second); got != 0 {
		t.Errorf("a short session that ended on its own: hold %v, want 0", got)
	}
	if got := s.after(true, 0); got != time.Minute {
		t.Errorf("the stall after a short clean ending: hold %v, want 1m0s (the streak kept)", got)
	}
	s.after(false, 2*time.Minute) // healthy for 2 minutes, then a clean end
	for i, want := range []time.Duration{0, 30 * time.Second} {
		if got := s.after(true, 0); got != want {
			t.Errorf("stall %d after a 2-minute session: hold %v, want %v", i+1, got, want)
		}
	}
}

func TestStallNoteSaysWhyAndHowLong(t *testing.T) {
	for _, tc := range []struct {
		n    int
		wait time.Duration
		want string
	}{
		{2, 30 * time.Second, "2 short sessions in a row · waiting 30 s — the box's sshd locks out rapid logins"},
		{4, 2 * time.Minute, "4 short sessions in a row · waiting 120 s — the box's sshd locks out rapid logins"},
		{3, 1400 * time.Millisecond, "3 short sessions in a row · waiting 2 s — the box's sshd locks out rapid logins"},
	} {
		if got := stallNote(tc.n, tc.wait); got != tc.want {
			t.Errorf("stallNote(%d, %v) = %q, want %q", tc.n, tc.wait, got, tc.want)
		}
	}
}

// The watchdog marks what it kills, and the stream worker holds off: a box
// that stalls every session (fake "silent": one record, then nothing) gets its
// first stall back on the normal backoff — the first note comes with two
// attempts made — then holds that grow, each announced by ONE note that stands
// for the whole wait. The holds are shortened; the rule's real values are
// pinned above.
func TestStallKillsHoldTheNextSpawn(t *testing.T) {
	h := newHarness(t)
	origHold, origMax := stallHold, stallHoldMax
	stallHold, stallHoldMax = 700*time.Millisecond, 1400*time.Millisecond
	h.restore = func() { stallHold, stallHoldMax = origHold, origMax } // post-join, see newHarness
	st := h.start("silent", startOpts{watchdog: &struct{ silent, connect, dataless time.Duration }{
		silent: 300 * time.Millisecond, connect: 5 * time.Second}})
	for _, step := range []struct {
		made int // attempts made when the note comes
		note string
		hold time.Duration
	}{
		{2, "2 short sessions in a row · waiting 1 s", 700 * time.Millisecond},
		{3, "3 short sessions in a row · waiting 2 s", 1400 * time.Millisecond},
	} {
		if !waitFor(func() bool { return strings.HasPrefix(st.Snap().Error, step.note) }, 8*time.Second) {
			t.Fatalf("note = %q, want %q…", st.Snap().Error, step.note)
		}
		noted := st.Snap().ErrorAt
		if n := st.RawAttempts(); n != step.made {
			t.Fatalf("%q came with %d attempts made, want %d", step.note, n, step.made)
		}
		if !waitFor(func() bool { return st.RawAttempts() > step.made }, 8*time.Second) {
			t.Fatalf("no respawn after the %v hold", step.hold)
		}
		if gap := time.Since(noted); gap < step.hold {
			t.Errorf("attempt %d came %v after the note, want at least the %v hold", step.made+1, gap, step.hold)
		}
		if at := st.Snap().ErrorAt; !at.Equal(noted) {
			t.Errorf("the note was rewritten during the hold (%v, then %v), want once per wait", noted, at)
		}
	}
}

// The hold stays interruptible: quit during a 30 s hold (the real value) ends
// the stream worker at once, not when the hold runs out.
func TestStallHoldEndsOnQuit(t *testing.T) {
	h := newHarness(t)
	st := h.start("silent", startOpts{watchdog: &struct{ silent, connect, dataless time.Duration }{
		silent: 300 * time.Millisecond, connect: 5 * time.Second}})
	if !waitFor(func() bool { return strings.Contains(st.Snap().Error, "waiting 30 s") }, 8*time.Second) {
		t.Fatalf("note = %q, want the 30 s hold", st.Snap().Error)
	}
	h.control.stop.Set()
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream worker sat out the hold after quit")
	}
}

// stallSSH writes a fake ssh for a session that logs in, delivers — a playing
// record, then beats heartbeats 50 ms apart — and then goes silent until it is
// killed. exec leaves the one process that holds stdout for the kill to hit.
func stallSSH(t *testing.T, beats int) string {
	t.Helper()
	dir := t.TempDir()
	play, beat := filepath.Join(dir, "play"), filepath.Join(dir, "beat")
	for p, fixture := range map[string]string{play: "playing_record.txt", beat: "heartbeat_record.txt"} {
		if err := os.WriteFile(p, []byte(fixtures.Get(fixture)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ssh := filepath.Join(dir, "stall-ssh")
	script := fmt.Sprintf("#!/bin/sh\ncat '%s'\ni=0\nwhile [ $i -lt %d ]; do sleep 0.05; cat '%s'; i=$((i+1)); done\nexec sleep 60\n", play, beats, beat)
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ssh
}

// stallSession runs one stream lifecycle (from InitialBackoff) against $LP10_SSH
// under its own watchdog, carrying stalls, and returns the next backoff and the
// session's state. The watchdog is joined before it returns.
func stallSession(stalls *stallStreak, silent, dataless time.Duration) (time.Duration, *protocol.State) {
	st, procs, dog := protocol.NewState(), newProcessSlot(), newRunControl()
	done := make(chan struct{})
	go func() { watchdog(st, procs, dog, silent, 10*time.Second, dataless); close(done) }()
	defer func() { dog.stop.Set(); <-done }()
	return streamOnceWithSnapshot(st, config.Config{}, "", InitialBackoff, stalls, "", procs, newRunControl()), st
}

// How long a stalled session delivered decides whether it counts, measured
// from its first data record to its last: this session's ~0.4 s of heartbeats
// clears a 150 ms stallResetAfter, so its stall — mid-streak — respawns on the
// normal backoff and ends the streak; against a minute the same session is
// short, extends the streak and holds the next spawn.
func TestStallStreakMeasuresDelivery(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("LP10_SSH", stallSSH(t, 8))
	origReset, origHold := stallResetAfter, stallHold
	defer func() { stallResetAfter, stallHold = origReset, origHold }()
	stallHold = 500 * time.Millisecond

	stallResetAfter = 150 * time.Millisecond
	var stalls stallStreak
	stalls.after(true, 0)
	stalls.after(true, 0) // two stalls in a row: the next short one would hold 1 s
	next, st := stallSession(&stalls, 300*time.Millisecond, 10*time.Second)
	if e := st.Snap().Error; next != 2*InitialBackoff || stalls.n != 0 || e != "" {
		t.Errorf("a long session's stall: next=%v streak=%d note=%q, want %v, 0, none", next, stalls.n, e, 2*InitialBackoff)
	}

	stallResetAfter = time.Minute
	stalls.after(true, 0) // one stall: this session is the second in a row
	next, st = stallSession(&stalls, 300*time.Millisecond, 10*time.Second)
	s := st.Snap()
	if next != 2*InitialBackoff || stalls.n != 2 || !strings.HasPrefix(s.Error, "2 short sessions in a row") {
		t.Errorf("a short session's stall: next=%v streak=%d note=%q, want %v, 2, the hold's note", next, stalls.n, s.Error, 2*InitialBackoff)
	}
	if held := time.Since(s.ErrorAt); held < stallHold {
		t.Errorf("returned %v after the note, want at least the %v hold", held, stallHold)
	}
}

// A record of any kind proves the login, not only player data: a loop whose
// LUCI reads wedge sends empty frames until the watchdog's dataless kill, the
// same every session, and that stall is held off like any other.
func TestStallStreakCountsADatalessWedge(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("LP10_SSH", testutil.FakeSSH(t))
	t.Setenv("LP10_FAKE_SCENARIO", "dataless")
	origHold := stallHold
	defer func() { stallHold = origHold }()
	stallHold = 300 * time.Millisecond

	var stalls stallStreak
	stalls.after(true, 0) // one stall: this wedge is the second in a row
	_, st := stallSession(&stalls, 10*time.Second, 400*time.Millisecond)
	if e := st.Snap().Error; stalls.n != 2 || !strings.HasPrefix(e, "2 short sessions in a row") {
		t.Errorf("a dataless wedge: streak=%d note=%q, want 2 and the hold's note", stalls.n, e)
	}
}

// A watchdog kill before the first record counts too: ssh's own ConnectTimeout
// and a refused password end a session long before the connect window, so a
// session still there to be killed had logged in and its loop printed nothing —
// a loop that hangs at startup every session paces logins like any stall.
func TestStallStreakCountsAKillBeforeTheFirstRecord(t *testing.T) {
	testutil.Isolate(t)
	ssh := filepath.Join(t.TempDir(), "mute-ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_SSH", ssh)
	origHold := stallHold
	defer func() { stallHold = origHold }()
	stallHold = 300 * time.Millisecond

	var stalls stallStreak
	stalls.after(true, 0) // one short login: this mute one is the second in a row
	st, procs, dog := protocol.NewState(), newProcessSlot(), newRunControl()
	done := make(chan struct{})
	go func() { watchdog(st, procs, dog, 10*time.Second, 300*time.Millisecond, 10*time.Second); close(done) }()
	streamOnceWithSnapshot(st, config.Config{}, "", InitialBackoff, &stalls, "", procs, newRunControl())
	dog.stop.Set()
	<-done
	if e := st.Snap().Error; stalls.n != 2 || !strings.HasPrefix(e, "2 short sessions in a row") {
		t.Errorf("a kill before the first record: streak=%d note=%q, want 2 and the hold's note", stalls.n, e)
	}
}
