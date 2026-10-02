package tui

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// eqModel is a connected model whose equalizer has read MXV 40, EQ off and
// the first preset — what the tunnel's seed queries bring on connect.
func eqModel(t *testing.T) (*model, *protocol.State, func() []workers.Command) {
	t.Helper()
	st := protocol.NewState()
	connect(st)
	st.ApplyTunnel("MXV", 40)
	st.ApplyTunnel("EQE", 0)
	st.ApplyTunnel("EQS", 0)
	m, _, collect := modelWith(st)
	m.rows, m.cols = 24, 80
	return m, st, collect
}

func TestEQPaneFocusAdjustToggle(t *testing.T) {
	m, st, collect := eqModel(t)

	// 'e' opens the equalizer; the first display slot is the EQ enable (EQE, a toggle).
	m.key(kr('e'))
	if m.view != viewEQ || m.eqFocus != 0 {
		t.Fatalf("after e: view=%d focus=%d", m.view, m.eqFocus)
	}
	if m.eqSpec().Code != "EQE" {
		t.Fatalf("display slot 0 is %s, want EQE", m.eqSpec().Code)
	}

	// enter flips the EQ enable 0 -> 1, optimistic + queued.
	m.key(ke(kEnter))
	if v, _ := st.EQValue("EQE"); v != 1 {
		t.Errorf("EQE=%d want 1", v)
	}
	cmds := collect()
	if got := wire(cmds); !slices.Equal(got, []string{"EQE:1"}) || cmds[0].TS.IsZero() {
		t.Errorf("queued %v (ts %v), want [EQE:1] stamped", got, cmds[0].TS)
	}
	st.ApplyTunnel("EQE", 0) // the echo of a poll answered before the write: held off
	if v, _ := st.EQValue("EQE"); v != 1 {
		t.Error("the EQ echo hold should keep a stale readback from undoing the change")
	}

	// Max volume (MXV) is the last display slot; right nudges its slider (+step=5): 40 -> 45.
	m.eqFocus = len(eqOrder) - 1
	if m.eqSpec().Code != "MXV" {
		t.Fatalf("last display slot is %s, want MXV", m.eqSpec().Code)
	}
	m.key(ke(kRight))
	if v, _ := st.EQValue("MXV"); v != 45 {
		t.Errorf("MXV=%d want 45", v)
	}
	if got := wire(collect()); !slices.Equal(got, []string{"MXV:45"}) {
		t.Errorf("queued %v, want [MXV:45]", got)
	}

	// esc steps back to the player rather than quitting.
	if m.key(ke(kEsc)) {
		t.Error("esc in the equalizer should not quit")
	}
	if m.view != viewPlayer {
		t.Error("esc should return to the player")
	}
	// e toggles: a second e from the equalizer goes home too
	m.key(kr('e'))
	m.key(kr('E'))
	if m.view != viewPlayer {
		t.Error("E should close the equalizer it opened")
	}
}

// tab walks the three numbered views in order and wraps; esc goes straight
// home; help is behind ? alone and outside the cycle.
func TestTabSwitchesView(t *testing.T) {
	m, _, _ := eqModel(t)
	for _, want := range []view{viewEQ, viewDiag, viewPlayer, viewEQ} {
		m.key(ke(kTab))
		if m.view != want {
			t.Fatalf("tab reached view %s, want %s", viewNames[m.view], viewNames[want])
		}
	}
	m.key(ke(kEsc))
	if m.view != viewPlayer {
		t.Fatalf("esc should return to the player, got %s", viewNames[m.view])
	}
	m.key(kr('?'))
	if m.view != viewHelp {
		t.Fatal("? should open the help page")
	}
	m.key(kr('?'))
	if m.view != viewPlayer {
		t.Fatal("? again should close it")
	}
}

func TestEQClampsAtMin(t *testing.T) {
	m, st, collect := eqModel(t)
	st.ApplyTunnel("MXV", 0)
	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max volume is the last display slot
	m.key(ke(kLeft))             // already 0 -> clamps
	if v, _ := st.EQValue("MXV"); v != 0 {
		t.Errorf("MXV=%d want 0 (clamped)", v)
	}
	if got := wire(collect()); !slices.Equal(got, []string{"MXV:0"}) {
		t.Errorf("queued %v, want [MXV:0]", got)
	}
}

// Inbound tunnel values are raw (deliberately unclamped), so the nudge math
// must not wrap: MaxInt + step would clamp to Min and send MXV:0 — a hard 0%
// cap on the speaker's output.
func TestEQAdjustOverflowSaturates(t *testing.T) {
	m, st, collect := eqModel(t)
	st.ApplyTunnel("MXV", math.MaxInt)
	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max volume
	m.key(ke(kRight))
	if got := wire(collect()); !slices.Equal(got, []string{"MXV:100"}) {
		t.Errorf("queued %v, want [MXV:100] (saturated at Max, not wrapped to Min)", got)
	}
	st.ApplyTunnel("BAS", math.MinInt)
	m.eqFocus = slices.Index(eqDisplay, "BAS")
	m.key(ke(kLeft))
	if got := wire(collect()); !slices.Equal(got, []string{"BAS:-10"}) {
		t.Errorf("queued %v, want [BAS:-10] (saturated at Min, not wrapped to Max)", got)
	}
}

// Leaving a view consumes the rest of the same event batch: a paste that
// begins with esc while the diagnostics are up must only return to the player
// — the 'n' would otherwise skip the track and the 'q' would quit the app.
func TestDiagCloseSwallowsRestOfBatch(t *testing.T) {
	m, _, collect := makeModel(t)
	m.view = viewDiag
	if m.dispatchKeys(append([]keyEvent{ke(kEsc)}, runeEvents("nq")...)) {
		t.Fatal("a swallowed batch must not quit")
	}
	if m.view == viewDiag {
		t.Error("the first event should close the view")
	}
	if cmds := collect(); len(cmds) != 0 {
		t.Errorf("events after the view closed leaked through: %v", wire(cmds))
	}
	if !m.dispatchKeys(runeEvents("q")) {
		t.Error("q on the player should still quit")
	}
}

func TestEQDisplayOrder(t *testing.T) {
	// EQ enable + its preset, tone, deep bass, balance, then the rarely-touched
	// output cap (Max volume) last.
	want := []string{"EQE", "EQS", "TRE", "MID", "BAS", "VBS", "VBI", "BAL", "MXV"}
	if len(eqOrder) != len(want) {
		t.Fatalf("eqOrder len=%d want %d", len(eqOrder), len(want))
	}
	for d, code := range want {
		if got := tunnel.Specs[eqOrder[d]].Code; got != code {
			t.Errorf("display slot %d = %s, want %s", d, got, code)
		}
		if eqLabel[code] == "" || len(eqAbout[code]) == 0 {
			t.Errorf("%s has no label or note", code)
		}
	}
}

func TestDashboardRenders(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 24, 80
	out := clean(render(t, m))
	// the equalizer lives in its own view: nothing of it on the player
	for _, absent := range []string{"Treble", "EQ off", "Max volume"} {
		if strings.Contains(out, absent) {
			t.Errorf("dashboard render carries %q", absent)
		}
	}
	if !strings.Contains(out, "pause") {
		t.Error("dashboard render lacks the transport")
	}
}

func TestEQUnknownValuesQueryNotSet(t *testing.T) {
	// Nothing reported yet (no snapshot, tunnel not seeded): nudging or toggling
	// must never send a value — fabricating a 0 baseline would hard-cap the
	// speaker's output when the focused control is MXV. Instead each keypress
	// re-queries the control so a lost seed reply self-heals.
	st := protocol.NewState()
	connect(st)
	m, _, collect := modelWith(st)
	m.rows, m.cols = 24, 80

	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max volume: slider shows "—"
	m.key(ke(kLeft))
	m.eqFocus = 0 // EQE toggle, also unknown
	m.key(ke(kEnter))
	m.eqFocus = 1 // EQS preset choice, also unknown
	m.key(ke(kEnter))
	m.key(ke(kRight))

	cmds := collect()
	if got := wire(cmds); !slices.Equal(got, []string{"MXV?", "EQE?", "EQS?", "EQS?"}) {
		t.Errorf("queued %v, want a query per keypress", got)
	}
	for _, c := range cmds {
		if c.TS.IsZero() {
			t.Errorf("query %+v carries no enqueue time", c)
		}
	}
	if _, known := st.EQValue("MXV"); known {
		t.Error("MXV must stay unknown (no optimistic local write)")
	}

	// Once the device reports a value the nudge works again.
	st.ApplyTunnel("MXV", 40)
	m.eqFocus = len(eqOrder) - 1
	m.key(ke(kLeft))
	if got := wire(collect()); !slices.Equal(got, []string{"MXV:35"}) {
		t.Errorf("queued %v, want [MXV:35]", got)
	}
}

func TestEQPaneInertAtMiniSize(t *testing.T) {
	// At mini size the equalizer isn't drawn, so its keys must not drive it: a
	// view held from before a shrink is dropped, tab/e can't re-enter it, and
	// arrows act on the player (never nudging the invisible Max volume cap).
	m, st, collect := eqModel(t)
	m.view = viewEQ        // as if open before the shrink
	m.rows, m.cols = 8, 40 // below MiniRows/MiniCols

	m.key(ke(kTab)) // must not step into (or within) the views
	if m.view != viewPlayer {
		t.Fatalf("tab at mini size: view=%d want viewPlayer", m.view)
	}
	for _, r := range "e23i?" {
		m.key(kr(r))
		if m.view != viewPlayer {
			t.Fatalf("%c at mini size: view=%s want player", r, viewNames[m.view])
		}
	}
	m.eqFocus = len(eqOrder) - 1 // Max volume, were the view up
	m.key(ke(kLeft))             // must be a player action, not an EQ nudge
	m.key(ke(kRight))
	if got := collect(); len(got) != 0 {
		t.Errorf("%v sent at mini size, want nothing", wire(got))
	}
	if v, _ := st.EQValue("MXV"); v != 40 {
		t.Errorf("MXV changed to %d at mini size, want 40 (untouched)", v)
	}

	// Back at full size the equalizer works again.
	m.rows, m.cols = 24, 80
	m.key(kr('e'))
	if m.view != viewEQ {
		t.Fatal("e at full size should open the equalizer")
	}
}

// The equalizer view: a section head, the nine slider rows, the focused
// control's note, the footer hint; every row inside the frame.
func TestEQViewRendersEveryControl(t *testing.T) {
	m, st, _ := eqModel(t)
	st.SetEQPresets([]string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"})
	st.ApplyTunnel("BAS", 3)
	st.ApplyTunnel("BAL", -10)
	m.rows, m.cols = 40, 100
	m.setView(viewEQ)
	m.eqFocus = slices.Index(eqDisplay, "BAS")
	out := clean(render(t, m))
	for _, code := range eqDisplay {
		if !eqRowDrawn(out, eqLabel[code]) {
			t.Errorf("the %s row is missing", eqLabel[code])
		}
	}
	for _, want := range []string{"── equalizer ─", "── Bass ─", "Bass in dB, −10 to +10", "+3", "L10", "Flat", eqHint} {
		if !strings.Contains(out, want) {
			t.Errorf("equalizer view lacks %q:\n%s", want, out)
		}
	}
}
