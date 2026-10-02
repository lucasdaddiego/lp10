package tui

// Regression tests for the TUI core: the equalizer on a short frame and with
// its tunnel down, the palette switch, the help page, the update check, the
// sleep timer and the volume keys. Each test here failed before its fix.

import (
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

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

// ---- equalizer ---------------------------------------------------------------

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

// The preset selector drew names left to right and stopped at the row's end;
// at the narrowest frame that is not the mini line the current preset (Vocal,
// the sixth) was never drawn, so nothing on the row was lit.
func TestEQCurrentPresetIsDrawnAtTheNarrowestFrame(t *testing.T) {
	m, st, _ := makeModel(t)
	st.SetEQPresets([]string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"})
	st.ApplyTunnel("EQS", 5)
	_, vals := st.EQView()
	W := MiniCols - 6 // the narrowest frame that is not the mini line
	row := stripANSI(m.eqSliderRow(eqSpecIndex("EQS"), vals, false, W))
	if !strings.Contains(row, "Vocal") {
		t.Errorf("W=%d: the current preset is not on the row: %q", W, row)
	}
	if DispW(row) != W {
		t.Errorf("row width = %d, want %d: %q", DispW(row), W, row)
	}
	// a current preset that fits from the first name leaves the list there
	st.ApplyTunnel("EQS", 1)
	_, vals = st.EQView()
	if row := stripANSI(m.eqSliderRow(eqSpecIndex("EQS"), vals, false, W)); !strings.Contains(row, "Flat · Classical") {
		t.Errorf("W=%d, Classical current: %q, want the list from Flat", W, row)
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

// ---- theme --------------------------------------------------------------------

// theme = auto draws the first frame dark: the terminal has not answered yet.
// The light answer rebuilt the palette, but the volume rail, cached by volume,
// mute and height alone, kept the dark colours until the volume changed.
func TestThemeAnswerRepaintsTheVolumeRail(t *testing.T) {
	light := tea.BackgroundColorMsg{Color: color.White}
	m, st, _ := makeModel(t)
	m.dispatch(tea.WindowSizeMsg{Width: 120, Height: 40})
	render(t, m) // the frame before the terminal's answer
	if m.volBlk == nil {
		t.Fatal("setup: the full player should draw the rail")
	}
	m.Update(light)
	if m.themeDark {
		t.Fatal("setup: a white background should switch to the light palette")
	}
	render(t, m)
	if want := m.buildVolRail(st.Snap(), len(m.volBlk)-1); !slices.Equal(m.volBlk, want) {
		t.Errorf("after the light answer the rail kept the dark palette:\n got %q\nwant %q", m.volBlk[len(m.volBlk)-2], want[len(want)-2])
	}
	fresh, _, _ := makeModel(t)
	fresh.dispatch(tea.WindowSizeMsg{Width: 120, Height: 40})
	fresh.dispatch(light)
	render(t, fresh)
	if !slices.Equal(m.volBlk, fresh.volBlk) {
		t.Error("the rail differs from one drawn light from the start")
	}
}

// ---- help page -----------------------------------------------------------------

// The help page is taller than 80×24 and must scroll: at 80×24 (and at the
// 25-row full size) the lower groups were cut off and no key reached them. It
// scrolls like the diagnostics, the footer saying how much is off-screen, and
// opens at its top.
func TestHelpScrollsToEveryGroupAt80x24(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 24, 80
	body := func() string { // the frame below the header and the notice line
		return strings.Join(strings.Split(clean(render(t, m)), "\n")[3:], "\n")
	}
	m.key(kr('?'))
	first := body()
	if !strings.Contains(first, "more rows below") {
		t.Fatalf("a too-short help page should offer to scroll:\n%s", first)
	}
	seen := map[string]bool{}
	for range 40 {
		frame := clean(render(t, m))
		for _, g := range helpGroups {
			if strings.Contains(frame, "── "+g.title+" ─") {
				seen[g.title] = true
			}
		}
		m.key(ke(kDown))
	}
	for _, g := range helpGroups {
		if !seen[g.title] {
			t.Errorf("scrolling never reached the %q group", g.title)
		}
	}
	if bottom := body(); !strings.Contains(bottom, "rows above") || strings.Contains(bottom, "below") {
		t.Errorf("scrolling past the end should clamp at the bottom:\n%s", bottom)
	}
	m.key(ke(kUp))
	if up := body(); !strings.Contains(up, "above · 1 below") {
		t.Errorf("↑ from the bottom should scroll back one row:\n%s", up)
	}
	for range 10 {
		m.key(ke(kLeft)) // a page at a time, clamped at the top
	}
	if body() != first {
		t.Error("← past the top should come back to the first row")
	}
	m.key(ke(kRight))
	if m.diagScroll != m.diagPage() {
		t.Errorf("→ should page down by %d, scrolled %d", m.diagPage(), m.diagScroll)
	}
	// it opens at its top, whatever the diagnostics' scroll was
	m.key(kr('3'))
	for range 5 {
		m.key(ke(kDown))
	}
	m.key(kr('?'))
	if body() != first {
		t.Error("the help page should open at its top")
	}
	// too narrow for the scroll count and the way out together: the count stays
	m.cols = MiniCols
	if foot := frameRowWith(clean(render(t, m)), "more rows below"); foot == "" || strings.Contains(foot, "back to the player") {
		t.Errorf("narrow help footer = %q, want the scroll count alone", foot)
	}
	// tall enough: no scroll count, the way out alone
	m.rows = 60
	if foot := footerOf(clean(render(t, m))); foot != "esc · q · ? back to the player" {
		t.Errorf("tall help footer = %q", foot)
	}
}

// The help page says the playback keys work from every view that does not use
// the letter; on the help page itself they reach the device and the page stays.
func TestHelpPageTakesThePlaybackKeys(t *testing.T) {
	m, _, collect := makeModel(t)
	m.key(kr('?'))
	collect()
	m.key(kr(' '))
	m.key(kr('n'))
	if got := wire(collect()); !slices.Equal(got, []string{"POP", "NXT"}) {
		t.Errorf("space n on the help page sent %v, want [POP NXT]", got)
	}
	if m.view != viewHelp {
		t.Errorf("a playback key closed the help page (view %s)", viewNames[m.view])
	}
}

// ---- the update check ------------------------------------------------------------

// LP10_OTA_URL="" switches the vendor check off (its worker never starts),
// but u still raised the request and the vendor line said "checking…" for the
// rest of the run.
func TestUpdateCheckSwitchedOffDoesNotHang(t *testing.T) {
	t.Setenv("LP10_OTA_URL", "")
	m, st, _ := makeModel(t)
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2", State: "S", NetMode: "ETH0"})
	m.rows, m.cols = 60, 160
	m.key(kr('3'))
	m.key(kr('u'))
	if out := clean(render(t, m)); strings.Contains(out, "checking…") {
		t.Error("with the check switched off, u leaves the vendor line on \"checking…\" forever")
	}
	if !strings.Contains(m.notice, "update check is off") {
		t.Errorf("notice = %q, want the check named off", m.notice)
	}
	// switched on, u raises the request (no OTA worker runs here, so nothing
	// leaves the test)
	t.Setenv("LP10_OTA_URL", "http://127.0.0.1:9/")
	m.key(kr('U'))
	if !st.DiagnosticView().OTAPending {
		t.Error("with the check on, U should raise the request")
	}
	if m.view != viewDiag {
		t.Error("u must not leave the diagnostics")
	}
}

// ---- volume --------------------------------------------------------------------

// Volume keys before the device's first volume read of the run computed from
// the cached snapshot: cached 40, the phone set 70 meanwhile, ↑ during
// "connecting…" sent VOL:42 and the room dropped from 70 to 42 on connect. The
// mute waits on the same read (the status reply carries both).
func TestVolumeKeysWaitForTheLiveVolume(t *testing.T) {
	st := protocol.NewState()
	st.Preload(40) // the last run's snapshot
	m, _, collect := modelWith(st)
	for _, ev := range []keyEvent{ke(kUp), ke(kDown), kr('+'), kr('-'), kr('m')} {
		m.key(ev)
	}
	m.do("volup")
	if got := collect(); len(got) != 0 {
		t.Fatalf("sent %v before the device reported its volume", wire(got))
	}
	if s := st.Snap(); s.Vol != 40 || s.Muted {
		t.Errorf("the cached volume moved to %d (muted %v)", s.Vol, s.Muted)
	}
	if !strings.Contains(m.notice, "volume not read yet") {
		t.Errorf("notice = %q, want why the key did nothing", m.notice)
	}
	if !m.flash["mute"].IsZero() || !m.flash["volup"].IsZero() {
		t.Error("a refused key flashed its button")
	}
	// the live read: the phone's 70 (a connected tunnel, first status reply)
	connect(st)
	st.ApplyStatus("NET", false, 70, true)
	m.key(ke(kUp))
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:72"}) {
		t.Errorf("↑ after the live read sent %v, want [VOL:72]", got)
	}
	m.key(kr('m'))
	if got := wire(collect()); !slices.Equal(got, []string{"MUT:1"}) {
		t.Errorf("m after the live read sent %v, want [MUT:1]", got)
	}
	// a later outage keeps the keys: the volume in hand is this run's own
	m.dispatch(logicMsg{})
	st.Disconnect()
	m.key(kr('-'))
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:70"}) {
		t.Errorf("- during a later outage sent %v, want [VOL:70]", got)
	}
	// a VOL push alone (no status) makes the level live too
	st2 := protocol.NewState()
	st2.Preload(40)
	m2, _, collect2 := modelWith(st2)
	st2.ApplyVolume(55)
	m2.key(kr('+'))
	if got := wire(collect2()); !slices.Equal(got, []string{"VOL:57"}) {
		t.Errorf("+ after a VOL push sent %v, want [VOL:57]", got)
	}
}

// ---- sleep timer ---------------------------------------------------------------

// A deadline the link kept from firing that ran out long before the link came
// back (an outage through the night) no longer pauses the morning's playback
// on reconnect: the timer is cancelled with a notice. A short blip still ends
// in the pause.
func TestSleepTooLateOnReconnectIsCancelled(t *testing.T) {
	m, st, collect := makeModel(t)
	m.key(kr('s'))
	collect()
	st.Disconnect()
	m.sleepAt = time.Now().Add(-8 * time.Hour) // it ran out overnight, the link still down
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Fatalf("sent %v while the link was down", wire(got))
	}
	connect(st) // morning: the link is back and music plays
	m.dispatch(logicMsg{})
	if got := wire(collect()); slices.Contains(got, "POP") {
		t.Errorf("the overnight timer paused the morning's playback: %v", got)
	}
	if !m.sleepAt.IsZero() || !st.Snap().Playing {
		t.Errorf("after the reconnect: timer %v, playing %v", m.sleepAt, st.Snap().Playing)
	}
	if want := "sleep timer cancelled · it ran out 8h ago"; m.notice != want {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}

	// nine minutes late (a blip): it still pauses
	m.key(kr('s'))
	st.Disconnect()
	m.sleepAt = time.Now().Add(-9 * time.Minute)
	m.dispatch(logicMsg{})
	connect(st)
	st.ApplyStatus("NET", false, 44, true) // the new link's seed: still playing
	m.dispatch(logicMsg{})
	if got := wire(collect()); !slices.Equal(got, []string{"POP"}) {
		t.Errorf("a blip past the deadline sent %v, want [POP]", got)
	}
	if st.Snap().Playing {
		t.Error("the late-but-close timer should pause")
	}
}
