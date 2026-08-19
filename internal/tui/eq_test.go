package tui

import (
	"math"
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

func eqModel(t *testing.T) (*model, *protocol.State, chan workers.EQCommand) {
	t.Helper()
	st := protocol.NewState()
	st.ApplyTunnel("MXV", 40)
	st.ApplyTunnel("EQS", 0)
	eqcmds := make(chan workers.EQCommand, 16)
	m := newModel(st, defaultCfg(), make(chan *protocol.Command, 8), eqcmds)
	m.rows, m.cols = 24, 80
	return m, st, eqcmds
}

func TestEQPaneFocusAdjustToggle(t *testing.T) {
	m, st, eqcmds := eqModel(t)

	// 'e' focuses the EQ pane; first display slot is the EQ switch (EQS, a toggle).
	m.key(kr('e'))
	if m.pane != paneEQ || m.eqFocus != 0 {
		t.Fatalf("after e: pane=%d focus=%d", m.pane, m.eqFocus)
	}
	if m.eqSpec().Code != "EQS" {
		t.Fatalf("display slot 0 is %s, want EQS", m.eqSpec().Code)
	}

	// enter flips the EQ toggle 0 -> 1, optimistic + queued.
	m.key(ke(kEnter))
	if v, _ := st.EQValue("EQS"); v != 1 {
		t.Errorf("EQS=%d want 1", v)
	}
	if cmd := <-eqcmds; cmd.Code != "EQS" || cmd.Val != 1 || cmd.TS.IsZero() {
		t.Errorf("queued cmd=%+v want {EQS 1}", cmd)
	}

	// Max Vol (MXV) is the last display slot; right nudges its slider (+step=5): 40 -> 45.
	m.eqFocus = len(eqOrder) - 1
	if m.eqSpec().Code != "MXV" {
		t.Fatalf("last display slot is %s, want MXV", m.eqSpec().Code)
	}
	m.key(ke(kRight))
	if v, _ := st.EQValue("MXV"); v != 45 {
		t.Errorf("MXV=%d want 45", v)
	}
	if cmd := <-eqcmds; cmd.Code != "MXV" || cmd.Val != 45 {
		t.Errorf("queued cmd=%+v want {MXV 45}", cmd)
	}

	// esc steps back to the player rather than quitting.
	if m.key(ke(kEsc)) {
		t.Error("esc in EQ pane should not drain/quit")
	}
	if m.pane != paneNow {
		t.Error("esc should return focus to the now-playing pane")
	}
}

func TestTabSwitchesPane(t *testing.T) {
	m, _, _ := eqModel(t)
	m.key(ke(kTab))
	if m.pane != paneEQ {
		t.Fatalf("tab should switch to EQ pane, got %d", m.pane)
	}
	m.key(ke(kTab))
	if m.pane != paneNow {
		t.Fatalf("tab should switch back, got %d", m.pane)
	}
}

func TestEQClampsAtMin(t *testing.T) {
	m, st, eqcmds := eqModel(t)
	st.ApplyTunnel("MXV", 0)
	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max Vol is the last display slot
	m.key(ke(kLeft))             // already 0 -> clamps
	if v, _ := st.EQValue("MXV"); v != 0 {
		t.Errorf("MXV=%d want 0 (clamped)", v)
	}
	if cmd := <-eqcmds; cmd.Val != 0 {
		t.Errorf("queued val=%d want 0", cmd.Val)
	}
}

// Inbound tunnel values are raw (deliberately unclamped), so the nudge math
// must not wrap: MaxInt + step would clamp to Min and send MXV:0 — a hard 0%
// cap on the speaker's output.
func TestEQAdjustOverflowSaturates(t *testing.T) {
	m, st, eqcmds := eqModel(t)
	st.ApplyTunnel("MXV", math.MaxInt)
	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max Vol
	m.key(ke(kRight))
	if cmd := <-eqcmds; cmd.Val != 100 {
		t.Errorf("queued val=%d want 100 (saturated at Max, not wrapped to Min)", cmd.Val)
	}
	st.ApplyTunnel("BAS", math.MinInt)
	m.eqFocus = 3 // Bass
	m.key(ke(kLeft))
	if cmd := <-eqcmds; cmd.Val != -10 {
		t.Errorf("queued val=%d want -10 (saturated at Min, not wrapped to Max)", cmd.Val)
	}
}

// Closing the diag overlay consumes the rest of the same event batch: pasting
// "nq" with the overlay open must only dismiss it — the 'n' would otherwise
// skip the track and the 'q' would quit the app.
func TestDiagCloseSwallowsRestOfBatch(t *testing.T) {
	m, _, collect := makeModel(t)
	m.diag = true
	if m.dispatchKeys(runeEvents("nq")) {
		t.Fatal("a swallowed batch must not quit")
	}
	if m.diag {
		t.Error("the first event should close the overlay")
	}
	if cmds := collect(); len(cmds) != 0 {
		t.Errorf("events after the overlay close leaked through: %v", cmds)
	}
	if !m.dispatchKeys(runeEvents("q")) {
		t.Error("q outside the overlay should still quit")
	}
}

func TestEQDisplayOrder(t *testing.T) {
	// EQ + tone, then deep bass, then the rarely-touched output cap (Max Vol) last.
	want := []string{"EQS", "TRE", "MID", "BAS", "VBS", "VBI", "MXV"}
	if len(eqOrder) != len(want) {
		t.Fatalf("eqOrder len=%d want %d", len(eqOrder), len(want))
	}
	for d, code := range want {
		if got := tunnel.Specs[eqOrder[d]].Code; got != code {
			t.Errorf("display slot %d = %s, want %s", d, got, code)
		}
	}
}

func TestDashboardRenders(t *testing.T) {
	m, _, _ := eqModel(t)
	protocol.ApplyRecord(m.st, playingRecord())
	out := m.viewContent()
	for _, want := range []string{"equalizer", "Max", "Treble", "Mid", "Bass"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard render missing %q", want)
		}
	}
}

func TestEQUnknownValuesQueryNotSet(t *testing.T) {
	// Nothing reported yet (no snapshot, tunnel not seeded): nudging or toggling
	// must never send a value — fabricating a 0 baseline would hard-cap the
	// speaker's output when the focused control is MXV. Instead each keypress
	// re-queries the control so a lost seed reply self-heals.
	st := protocol.NewState()
	eqcmds := make(chan workers.EQCommand, 16)
	m := newModel(st, defaultCfg(), make(chan *protocol.Command, 8), eqcmds)
	m.rows, m.cols = 24, 80

	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max Vol: slider shows "—"
	m.key(ke(kLeft))
	m.eqFocus = 0 // EQS toggle, also unknown
	m.key(ke(kEnter))

	for _, want := range []string{"MXV", "EQS"} {
		cmd := <-eqcmds
		if !cmd.Query || cmd.Code != want || cmd.TS.IsZero() {
			t.Errorf("queued cmd=%+v want a %s query", cmd, want)
		}
	}
	if n := len(eqcmds); n != 0 {
		t.Fatalf("%d extra commands queued from unknown values, want 0", n)
	}
	if _, known := st.EQValue("MXV"); known {
		t.Error("MXV must stay unknown (no optimistic local write)")
	}

	// Once the device reports a value the nudge works again.
	st.ApplyTunnel("MXV", 40)
	m.eqFocus = len(eqOrder) - 1
	m.key(ke(kLeft))
	if cmd := <-eqcmds; cmd.Code != "MXV" || cmd.Val != 35 || cmd.Query {
		t.Errorf("queued cmd=%+v want {MXV 35}", cmd)
	}
}

func TestEQPaneInertAtMiniSize(t *testing.T) {
	// At mini size the EQ pane isn't drawn, so its keys must not drive it: a pane
	// focus held from before a shrink is dropped, tab/e can't re-enter it, and
	// arrows act on the player (never nudging the invisible Max Vol cap).
	m, st, eqcmds := eqModel(t)
	st.ApplyTunnel("MXV", 40) // known, so a leaked nudge WOULD send
	m.pane = paneEQ           // as if focused before the shrink
	m.rows, m.cols = 8, 40    // below MiniRows/MiniCols

	m.key(ke(kTab)) // must not toggle into (or within) the EQ pane
	if m.pane != paneNow {
		t.Fatalf("tab at mini size: pane=%d want paneNow", m.pane)
	}
	m.key(kr('e')) // must not focus EQ
	if m.pane != paneNow {
		t.Fatalf("e at mini size: pane=%d want paneNow", m.pane)
	}
	m.eqFocus = len(eqOrder) - 1 // Max Vol, were the pane active
	m.key(ke(kLeft))             // must be a player action, not an EQ nudge
	m.key(ke(kRight))
	if n := len(eqcmds); n != 0 {
		t.Errorf("%d EQ commands sent at mini size, want 0", n)
	}
	if v, _ := st.EQValue("MXV"); v != 40 {
		t.Errorf("MXV changed to %d at mini size, want 40 (untouched)", v)
	}

	// Back at full size the EQ pane works again.
	m.rows, m.cols = 24, 80
	m.key(kr('e'))
	if m.pane != paneEQ {
		t.Fatal("e at full size should focus the EQ pane")
	}
}
