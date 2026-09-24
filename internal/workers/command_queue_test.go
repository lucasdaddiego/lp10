package workers

// Regression tests for the command queue (2026-09-23): what an expired view
// flag may say, and a held volume key.

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// While no session is up, the TUI keeps re-sending its view flag (94 0 every
// StatsReassertTicks ≈ 3 s off the player; 90 1 in the diagnostics). Each one
// expires in the queue after CommandDeadline and posted "command not delivered",
// which overwrote the real reason the stream worker noted ("connection
// refused", "no route to the device") although the user typed nothing.
// Scaled 1:10 here: a 400 ms deadline, a keep-alive every 300 ms.
func TestKeepaliveExpiryDoesNotMaskConnectError(t *testing.T) {
	st := protocol.NewState()
	st.Note("ssh: connect to host 192.0.2.13 port 22: Connection refused")
	control := newRunControl()
	defer control.stop.Set()
	cmds := make(chan *protocol.Command, 16)
	go commandWorker(st, newProcessSlot(), control, cmds, 400*time.Millisecond)
	for range 5 {
		cmds <- &protocol.Command{Mid: 94, Data: "0", TS: time.Now()}
		time.Sleep(300 * time.Millisecond)
	}
	if e := st.Snap().Error; e == "command not delivered" {
		t.Errorf("a view keep-alive nobody typed replaced the connection error with %q", e)
	}
	// A lost command the user DID type still says so.
	cmds <- &protocol.Command{Mid: 40, Data: "PAUSE", TS: time.Now()}
	if !waitFor(func() bool { return st.Snap().Error == "command not delivered" }, 3*time.Second) {
		t.Errorf("an expired PAUSE left the error at %q, want \"command not delivered\"", st.Snap().Error)
	}
}

// lineRecorder is a live stdin that keeps every line written to it.
type lineRecorder struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *lineRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lineRecorder) Close() error { return nil }

func (w *lineRecorder) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.FieldsFunc(w.buf.String(), func(r rune) bool { return r == '\n' })
}

// A volume key held at a 30 ms repeat must not reach the box as one set per
// repeat: each set costs the loop a LUCI_local run (~40 ms), so the sets queue
// on the box, the loop's burst drain never reaches @@E, and the watchdog kills
// the session. Volume levels are absolute, so the worker writes only the
// newest a few times a second — and the level the key stopped at always lands.
func TestHeldVolumeKeyCoalesces(t *testing.T) {
	st := protocol.NewState()
	procs := newProcessSlot()
	stdin := &lineRecorder{}
	procs.start(st, &process{Stdin: stdin, Done: make(chan struct{})}) // young-spawn grace: writable
	control := newRunControl()
	defer control.stop.Set()
	cmds := make(chan *protocol.Command, 64)
	go commandWorker(st, procs, control, cmds, CommandDeadline)

	const presses = 34 // ~1 s of a held key
	start := time.Now()
	for n := range presses {
		cmds <- &protocol.Command{Mid: 64, Data: strconv.Itoa(n + 1), TS: time.Now()}
		time.Sleep(30 * time.Millisecond)
	}
	held := time.Since(start)
	last := "64 " + strconv.Itoa(presses)
	if !waitFor(func() bool { l := stdin.lines(); return len(l) > 0 && l[len(l)-1] == last }, 2*time.Second) {
		t.Fatalf("the level the key stopped at never reached the box: %q", stdin.lines())
	}
	if n, most := len(stdin.lines()), int(held/volumePace)+2; n > most {
		t.Errorf("%d presses over %v reached the box as %d volume sets, want at most %d (the newest level every %v)",
			presses, held.Round(time.Millisecond), n, most, volumePace)
	}
}
