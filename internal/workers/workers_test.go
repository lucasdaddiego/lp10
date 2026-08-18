package workers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
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
	h.wg.Go(func() { streamWorker(h.st, cfg, opts.snapshot, h.procs, h.control) })
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

func TestEofReconnects(t *testing.T) {
	h := newHarness(t)
	st := h.start("eof", startOpts{})
	if !waitFor(func() bool { return st.RawAttempts() >= 3 }, 6*time.Second) {
		t.Fatalf("attempts = %d, want >= 3", st.RawAttempts())
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
