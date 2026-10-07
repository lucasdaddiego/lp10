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

// Max volume is written no lower than 30, the device's documented floor:
// holding ← used to send MXV:25 … MXV:0, and a cap the MCU took that low
// would leave the room silent with no phone or remote able to raise it. A
// cap the device holds below the floor still shows as it is: ← on it sends
// nothing, → lifts it to the floor, and at the floor ← sends nothing.
func TestEQMaxVolumeFloor(t *testing.T) {
	m, st, collect := eqModel(t)
	st.ApplyTunnel("MXV", 0)
	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max volume is the last display slot
	m.key(ke(kLeft))
	if v, _ := st.EQValue("MXV"); v != 0 {
		t.Errorf("MXV=%d after ← on 0, want 0 (shown as the device holds it)", v)
	}
	if got := wire(collect()); len(got) != 0 {
		t.Errorf("← on a cap of 0 queued %v, want nothing", got)
	}
	m.key(ke(kRight))
	if got := wire(collect()); !slices.Equal(got, []string{"MXV:30"}) {
		t.Errorf("→ on a cap of 0 queued %v, want [MXV:30]", got)
	}
	if v, _ := st.EQValue("MXV"); v != 30 {
		t.Errorf("MXV=%d after →, want 30 (the value sent)", v)
	}
	m.key(ke(kLeft))
	if got := wire(collect()); len(got) != 0 {
		t.Errorf("← at the floor queued %v, want nothing", got)
	}
	m.key(ke(kRight))
	if got := wire(collect()); !slices.Equal(got, []string{"MXV:35"}) {
		t.Errorf("→ at the floor queued %v, want [MXV:35]", got)
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

func TestCov_toneStrBalStrPresetName(t *testing.T) {
	if toneStr(0) != "0" || toneStr(3) != "+3" || toneStr(-6) != "-6" {
		t.Errorf("toneStr wrong: %q %q %q", toneStr(0), toneStr(3), toneStr(-6))
	}
	if balStr(0) != "0" || balStr(-20) != "L20" || balStr(35) != "R35" {
		t.Errorf("balStr wrong: %q %q %q", balStr(0), balStr(-20), balStr(35))
	}
	names := []string{"Flat", "", "Pop"}
	for idx, want := range map[int]string{0: "Flat", 1: "preset 1", 2: "Pop", 3: "preset 3", -1: "preset -1"} {
		if got := presetName(names, idx); got != want {
			t.Errorf("presetName(%d) = %q, want %q", idx, got, want)
		}
	}
	if plural(1) != "" || plural(0) != "s" || plural(2) != "s" {
		t.Error("plural wrong")
	}
	if btoi(true) != 1 || btoi(false) != 0 {
		t.Error("btoi wrong")
	}
}

func TestCov_eqToggleNoop(t *testing.T) {
	m, st, collect := modelWith(protocol.NewState())
	// eqToggleFocused is a no-op on a ranged control
	st.ApplyTunnel("TRE", 3)
	m.view, m.eqFocus = viewEQ, 2 // TRE (ranged)
	m.eqToggleFocused()
	if nv, _ := st.EQValue("TRE"); nv != 3 {
		t.Error("eqToggleFocused on a ranged control must be a no-op")
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("a ranged enter sent %v", wire(got))
	}
}

func TestCov_eqSliderRow(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	const w = 60
	mxv, eqe, bas := eqSpecIndex("MXV"), eqSpecIndex("EQE"), eqSpecIndex("BAS")
	// toggle ON and OFF
	if got := stripANSI(m.eqSliderRow(eqe, map[string]int{"EQE": 1}, false, w)); !strings.Contains(got, "● on") {
		t.Errorf("toggle on = %q", got)
	}
	if got := stripANSI(m.eqSliderRow(eqe, map[string]int{"EQE": 0}, false, w)); !strings.Contains(got, "○ off") {
		t.Errorf("toggle off = %q", got)
	}
	// ranged unknown value -> "—"
	if got := stripANSI(m.eqSliderRow(mxv, map[string]int{}, false, w)); !strings.HasSuffix(got, "—") {
		t.Errorf("ranged unknown = %q", got)
	}
	// ranged tone +/- and a non-negative-min ranged (MXV) accent knob, focused
	if got := stripANSI(m.eqSliderRow(bas, map[string]int{"BAS": 5}, true, w)); !strings.HasSuffix(got, "+5") {
		t.Errorf("ranged +5 = %q", got)
	}
	if got := stripANSI(m.eqSliderRow(bas, map[string]int{"BAS": -5}, true, w)); !strings.HasSuffix(got, "-5") {
		t.Errorf("ranged -5 = %q", got)
	}
	if got := stripANSI(m.eqSliderRow(mxv, map[string]int{"MXV": 50}, true, w)); !strings.HasSuffix(got, "50") {
		t.Errorf("ranged MXV = %q", got)
	}
	// every row is exactly w wide, whatever the value
	for _, row := range []string{
		m.eqSliderRow(bas, map[string]int{"BAS": 1000}, false, w),  // over the max: the knob clamps
		m.eqSliderRow(bas, map[string]int{"BAS": -1000}, false, w), // under the min
		m.eqSliderRow(bas, map[string]int{"BAS": 0}, true, w),
		m.eqSliderRow(eqe, map[string]int{"EQE": 1}, true, w),
		m.eqSliderRow(eqe, map[string]int{}, false, w),
	} {
		if got := DispW(stripANSI(row)); got != w {
			t.Errorf("row width = %d, want %d: %q", got, w, stripANSI(row))
		}
	}
	// trackW < 1 (very narrow) still renders without panicking
	_ = m.eqSliderRow(bas, map[string]int{"BAS": 0}, false, 5)
	_ = m.eqSliderRow(eqe, map[string]int{"EQE": 1}, false, 9)

	// the warm (boost) and cool (cut) focused knobs emit different styling
	warm := m.eqSliderRow(bas, map[string]int{"BAS": 5}, true, w)
	cool := m.eqSliderRow(bas, map[string]int{"BAS": -5}, true, w)
	if warm == cool {
		t.Error("a boosted (warm) and cut (cool) knob should differ in styling")
	}
}

// eqSpecIndex is the tunnel.Specs index of a wire code.
func eqSpecIndex(code string) int {
	return slices.IndexFunc(tunnel.Specs, func(sp tunnel.Spec) bool { return sp.Code == code })
}

// eqRowDrawn reports whether a frame draws the slider row of the control
// labelled label (its label column, not a note that merely names it).
func eqRowDrawn(frame, label string) bool {
	for ln := range strings.SplitSeq(frame, "\n") {
		if strings.HasPrefix(strings.TrimSpace(strings.Trim(ln, "┃")), padDisp(label, sliderLabelW)) {
			return true
		}
	}
	return false
}

// The nine slider rows need 17 terminal rows. At 15, Balance and Max volume
// were cut off, yet ↑ from the first row wraps the focus onto Max volume and
// ← then lowered the output cap with nothing on screen.
func TestEQFocusedRowStaysDrawnOnAShortFrame(t *testing.T) {
	m, st, _ := makeModel(t)
	st.PreloadEQ(map[string]int{"MXV": 100})
	m.rows, m.cols = 15, 80
	m.key(kr('2'))
	m.key(ke(kUp)) // wraps from EQ to the last row
	if code := m.eqSpec().Code; code != "MXV" {
		t.Fatalf("setup: focus on %s", code)
	}
	if out := clean(render(t, m)); !eqRowDrawn(out, "Max volume") {
		t.Errorf("focus is on Max volume but the row is not drawn:\n%s", out)
	}
	// one row up the window stays put: both rows are already on screen
	m.key(ke(kUp))
	out := clean(render(t, m))
	for _, label := range []string{"Balance", "Max volume"} {
		if !eqRowDrawn(out, label) {
			t.Errorf("%s is not drawn after ↑ from Max volume:\n%s", label, out)
		}
	}
	// a frame tall enough for all nine shows them all again
	m.rows = 40
	out = clean(render(t, m))
	for _, code := range eqDisplay {
		if !eqRowDrawn(out, eqLabel[code]) {
			t.Errorf("a tall frame lacks the %s row", eqLabel[code])
		}
	}
	if m.eqScroll != 0 {
		t.Errorf("a frame with room for every row keeps the window at 0, got %d", m.eqScroll)
	}
}

// At 80×14 with the tunnel live: ↓ walks the focus through every row, each is
// drawn when focused, and ← on Max volume changes what is on screen.
func TestEQFocusedRowIsVisibleAt80x14(t *testing.T) {
	m, st, collect := makeModel(t)
	for _, sp := range tunnel.Specs {
		st.ApplyTunnel(sp.Code, max(sp.Min, 0))
	}
	st.ApplyTunnel("MXV", 100)
	m.rows, m.cols = 14, 80
	m.setView(viewEQ)
	for d := range eqOrder {
		if d > 0 {
			m.key(ke(kDown))
		}
		if label := eqLabel[m.eqSpec().Code]; !eqRowDrawn(clean(render(t, m)), label) {
			t.Errorf("focus on %s, but its row is not drawn at 80x14", label)
		}
	}
	if code := m.eqSpec().Code; code != "MXV" {
		t.Fatalf("setup: focus on %s", code)
	}
	m.key(ke(kLeft))
	if got := wire(collect()); !slices.Equal(got, []string{"MXV:95"}) {
		t.Errorf("← on Max volume queued %v, want [MXV:95]", got)
	}
	// a frame with room for a single row still shows the focused one
	m.eqScroll = 0
	if rows := m.eqWindow(m.eqSliders(74), 0); len(rows) != 1 || !strings.HasPrefix(stripANSI(rows[0]), "Max volume") {
		t.Errorf("a zero-room window = %q, want the focused row alone", rows)
	}
}

// A dead :2018 tunnel "only marks the equalizer read-only", and the view's
// banner says the values are the last known. ←/→ used to paint the change
// and queue it; the worker dropped it after its deadline, and nothing
// reverted the painted value.
func TestDeadTunnelLeavesTheEqualizerReadOnly(t *testing.T) {
	st := protocol.NewState()
	st.PreloadEQ(map[string]int{"TRE": 0, "EQE": 0}) // cached; the tunnel never came up
	m, _, collect := modelWith(st)
	m.rows, m.cols = 40, 100
	m.setView(viewEQ)
	for m.eqSpec().Code != "TRE" {
		m.key(ke(kDown))
	}
	m.key(ke(kRight))
	out := clean(render(t, m))
	if row := frameRowWith(out, "Treble"); !strings.HasSuffix(row, " 0") {
		t.Errorf("tunnel down, yet the Treble row now reads %q", row)
	}
	if !strings.Contains(out, "the :2018 control tunnel is down — values are the last known until it returns") {
		t.Errorf("the read-only banner is missing:\n%s", out)
	}
	m.eqFocus = 0 // the EQ switch: enter is refused too
	m.key(ke(kEnter))
	if got := collect(); len(got) != 0 {
		t.Errorf("%v queued into a dead tunnel", wire(got))
	}
	if v, _ := st.EQValue("EQE"); v != 0 {
		t.Errorf("EQE painted as %d with the tunnel down", v)
	}
	if !strings.Contains(m.notice, "equalizer read-only") || !m.noticeWarn {
		t.Errorf("notice = %q (warn=%v), want the read-only warning", m.notice, m.noticeWarn)
	}
	// the tunnel back, the same key goes through
	connect(st)
	m.key(ke(kEnter))
	if got := wire(collect()); !slices.Equal(got, []string{"EQE:1"}) {
		t.Errorf("enter with the tunnel up queued %v, want [EQE:1]", got)
	}
	if out := clean(render(t, m)); strings.Contains(out, "control tunnel is down") {
		t.Error("the banner should leave with the tunnel back")
	}
}

// A switch the device has not reported rendered "○ off", where the ranged
// rows say "—" for the same state.
func TestEQUnknownSwitchIsNotOff(t *testing.T) {
	m, st, _ := makeModel(t)
	_, vals := st.EQView() // nothing reported yet
	row := stripANSI(m.eqSliderRow(eqSpecIndex("EQE"), vals, false, 80))
	if strings.Contains(row, "off") || !strings.Contains(row, "—") {
		t.Errorf("unreported EQ switch renders as %q, want —", strings.TrimSpace(row))
	}
	if DispW(row) != 80 {
		t.Errorf("row width = %d, want 80", DispW(row))
	}
	for v, want := range map[int]string{0: "○ off", 1: "● on"} {
		if row := stripANSI(m.eqSliderRow(eqSpecIndex("EQE"), map[string]int{"EQE": v}, false, 80)); !strings.Contains(row, want) {
			t.Errorf("EQE=%d renders %q, want %q", v, strings.TrimSpace(row), want)
		}
	}
}
