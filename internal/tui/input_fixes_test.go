package tui

// Regression tests for the TUI core (2026-09-23): the equalizer on a short frame and
// with its tunnel down, the view flags and the logs request across a lost
// link, the palette switch, the help page, the sleep timer and the volume
// keys. Each test here failed before its fix.

import (
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// frameRowWith is the first line of a frame that contains sub, with the
// frame's border and padding trimmed ("" when no line does).
func frameRowWith(frame, sub string) string {
	for ln := range strings.SplitSeq(frame, "\n") {
		if strings.Contains(ln, sub) {
			return strings.TrimSpace(strings.Trim(ln, "┃"))
		}
	}
	return ""
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

// eqSpecIndex is the tunnel.Specs index of a wire code.
func eqSpecIndex(code string) int {
	return slices.IndexFunc(tunnel.Specs, func(sp tunnel.Spec) bool { return sp.Code == code })
}

// ---- equalizer ---------------------------------------------------------------

// The nine slider rows need 17 terminal rows. At 15, Balance and Max volume
// were cut off, yet ↑ from the first row wraps the focus onto Max volume and
// ← then lowered the output cap with nothing on screen.
func TestEQFocusedRowStaysDrawnOnAShortFrame(t *testing.T) {
	m, st, _ := makeModel(t)
	m.eqcmds = make(chan workers.EQCommand, 8)
	st.PreloadEQ(map[string]int{"MXV": 100})
	m.rows, m.cols = 15, 80
	m.key(kr('2'))
	m.key(ke(kUp)) // wraps from EQ to the last row
	if code := m.eqSpec().Code; code != "MXV" {
		t.Fatalf("setup: focus on %s", code)
	}
	if out := clean(m.viewContent()); !eqRowDrawn(out, "Max volume") {
		t.Errorf("focus is on Max volume but the row is not drawn:\n%s", out)
	}
	// one row up the window stays put: both rows are already on screen
	m.key(ke(kUp))
	out := clean(m.viewContent())
	for _, label := range []string{"Balance", "Max volume"} {
		if !eqRowDrawn(out, label) {
			t.Errorf("%s is not drawn after ↑ from Max volume:\n%s", label, out)
		}
	}
	// a frame tall enough for all nine shows them all again
	m.rows = 40
	out = clean(m.viewContent())
	for _, code := range eqDisplay {
		if !eqRowDrawn(out, eqLabel[code]) {
			t.Errorf("a tall frame lacks the %s row", eqLabel[code])
		}
	}
}

// At 80×14 with the tunnel live: ↓ walks the focus through every row, each is
// drawn when focused, and ← on Max volume changes what is on screen.
func TestEQFocusedRowIsVisibleAt80x14(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.eqcmds = make(chan workers.EQCommand, 8)
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
		if label := eqLabel[m.eqSpec().Code]; !eqRowDrawn(clean(m.viewContent()), label) {
			t.Errorf("focus on %s, but its row is not drawn at 80x14", label)
		}
	}
	if code := m.eqSpec().Code; code != "MXV" {
		t.Fatalf("setup: focus on %s", code)
	}
	m.key(ke(kLeft))
	if c := <-m.eqcmds; c.Code != "MXV" || c.Val != 95 {
		t.Errorf("← on Max volume queued %+v, want MXV 95", c)
	}
}

// README: a dead :2018 tunnel "only marks the equalizer read-only", and the
// view's banner says the values are the last known. ←/→ used to paint the
// change and queue it; the worker dropped it after EQCommandDeadline, and
// nothing reverted the painted value.
func TestDeadTunnelLeavesTheEqualizerReadOnly(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.eqcmds = make(chan workers.EQCommand, 8)
	st.PreloadEQ(map[string]int{"TRE": 0, "EQE": 0}) // cached; the tunnel never came up
	m.rows, m.cols = 40, 100
	m.setView(viewEQ)
	for m.eqSpec().Code != "TRE" {
		m.key(ke(kDown))
	}
	m.key(ke(kRight))
	out := clean(m.viewContent())
	if row := frameRowWith(out, "Treble"); !strings.HasSuffix(row, " 0") {
		t.Errorf("tunnel down, yet the Treble row now reads %q under %q", row, frameRowWith(out, "tunnel is down"))
	}
	m.eqFocus = 0 // the EQ switch: enter is refused too
	m.key(ke(kEnter))
	if n := len(m.eqcmds); n != 0 {
		t.Errorf("%d writes queued into a dead tunnel", n)
	}
	if v, _ := st.EQValue("EQE"); v != 0 {
		t.Errorf("EQE painted as %d with the tunnel down", v)
	}
	if !strings.Contains(m.notice, "equalizer read-only") || !m.noticeWarn {
		t.Errorf("notice = %q (warn=%v), want the read-only warning", m.notice, m.noticeWarn)
	}
	// the tunnel back, the same key goes through
	st.ApplyTunnel("EQE", 0)
	m.key(ke(kEnter))
	if c := <-m.eqcmds; c.Code != "EQE" || c.Val != 1 {
		t.Errorf("enter with the tunnel up queued %+v, want EQE 1", c)
	}
}

// The preset selector drew names left to right and stopped at the row's end;
// at the narrowest frame that is not the mini line the current preset (Vocal,
// the sixth) was never drawn, so nothing on the row was lit.
func TestEQCurrentPresetIsDrawnAtTheNarrowestFrame(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
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
	m.sty = newTheme()
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

// ---- the link: view flags and the logs request -------------------------------

// The view keep-alives (94 0 every 3 s off the player, 90 1 in the
// diagnostics) were queued while the ssh link was down; the command worker
// dropped each one stale after 4 s and noted "command not delivered" in
// State's one error slot, which replaced the connection's own reason on the
// diagnostics error line and the connecting screen.
func TestKeepAlivesWhileDisconnectedAreNotLostCommands(t *testing.T) {
	t.Setenv("LP10_SSH", filepath.Join(t.TempDir(), "missing-ssh")) // never connects
	t.Setenv("LP10_TUNNEL_ADDR", "127.0.0.1:0")
	t.Setenv("LP10_LSSDP_HOST", "")
	t.Setenv("LP10_ZC_ADDR", "")
	t.Setenv("LP10_OTA_URL", "")
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	st := protocol.NewState()
	cfg := config.Config{Host: "127.0.0.1", Name: "LP10", VolStep: 2}
	rt := workers.StartRuntime(st, cfg)
	defer rt.Close(100 * time.Millisecond)
	m := newModel(st, cfg, rt.Commands, rt.EQCommands)
	m.premutePath = ""
	m.rows, m.cols = 40, 120
	m.key(kr('5')) // open the diagnostics to see why it will not connect
	end := time.Now().Add(7 * time.Second)
	next := time.Now()
	for time.Now().Before(end) {
		if !time.Now().Before(next) {
			m.dispatch(logicMsg{})
			next = next.Add(logicInterval)
		}
		if e := st.Snap().Error; e == "command not delivered" {
			line, _ := diagErrLine(st.Snap(), time.Now(), 116)
			t.Fatalf("nothing was typed, yet the error slot says %q; the diagnostics show %q", e, stripANSI(line))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// While the link is down no view flag goes out. The loop starts every
// connection with stats off and the player visible, so the connect's tick
// sends each flag the view needs once, the re-assert carries on while
// connected, and a later outage starts the same over.
func TestViewFlagsWaitForTheLinkAndFollowAReconnect(t *testing.T) {
	m, st, collect := modelWith(protocol.NewState()) // connecting…
	m.rows, m.cols = 40, 120
	m.setView(viewDiag)
	ticks := func(n int) []string {
		for range n {
			m.dispatch(logicMsg{})
		}
		var out []string
		for _, c := range collect() {
			out = append(out, fmt.Sprintf("%d %s", c.Mid, c.Data))
		}
		return out
	}
	if got := ticks(2 * StatsReassertTicks); len(got) != 0 {
		t.Fatalf("sent %v into a dead link", got)
	}
	protocol.ApplyRecord(st, playingRecord())
	if got := ticks(1); !slices.Equal(got, []string{"90 1", "94 0"}) {
		t.Fatalf("the connect's tick sent %v, want [90 1 94 0]", got)
	}
	if got := ticks(2 * StatsReassertTicks); !slices.Contains(got, "90 1") || !slices.Contains(got, "94 0") {
		t.Errorf("connected, the flags should be re-asserted; sent %v", got)
	}
	st.Disconnect()
	if got := ticks(2 * StatsReassertTicks); len(got) != 0 {
		t.Fatalf("sent %v while the link was down again", got)
	}
	protocol.ApplyRecord(st, playingRecord())
	if got := ticks(1); !slices.Equal(got, []string{"90 1", "94 0"}) {
		t.Errorf("the reconnect's tick sent %v, want [90 1 94 0]", got)
	}
	// back on the player while the link is down: nothing goes out then, and
	// the new loop's defaults already match (stats off, player visible), so
	// the reconnect sends nothing either
	st.Disconnect()
	ticks(1)
	m.key(ke(kEsc))
	if got := ticks(3); len(got) != 0 {
		t.Errorf("closing the diagnostics with the link down sent %v", got)
	}
	protocol.ApplyRecord(st, playingRecord())
	if got := ticks(1); len(got) != 0 {
		t.Errorf("the reconnect's tick on the player sent %v, want nothing", got)
	}
}

// Follow mode refetches the open log every 10 s, but not into a dead link.
func TestLogFollowWaitsForTheLink(t *testing.T) {
	m, st, collect := makeModel(t)
	m.rows, m.cols = 40, 120
	m.dispatch(logicMsg{}) // the connect's tick
	m.setView(viewLogs)
	m.key(kr('F'))
	collect()
	st.Disconnect()
	for range 250 {
		m.dispatch(logicMsg{})
	}
	for _, c := range collect() {
		if c.Mid == 93 {
			t.Fatalf("follow refetched into a dead link: %+v", c)
		}
	}
}

// The logs are asked for once per run (logAsked). A request made while the
// link was down was dropped stale by the command worker, and the pane kept
// saying "asking the device…" after the box came back; reopening the view
// did not ask again, only r recovered it.
func TestLogsRequestLostWhileDisconnectedIsRetried(t *testing.T) {
	m, st, collect := modelWith(protocol.NewState()) // connecting…
	m.rows, m.cols = 40, 120
	m.key(kr('4'))
	collect()                                 // the 93 1 goes into the dead link and is dropped stale (4 s)
	protocol.ApplyRecord(st, playingRecord()) // the box comes back
	for range 50 {
		m.dispatch(logicMsg{})
	}
	m.key(ke(kEsc))
	m.key(kr('4')) // reopen
	for range 50 {
		m.dispatch(logicMsg{})
	}
	var asked []string
	for _, c := range collect() {
		if c.Mid == 93 {
			asked = append(asked, c.Data)
		}
	}
	if out := clean(m.viewContent()); strings.Contains(out, "asking the device…") && len(asked) == 0 {
		t.Error("connected again, the logs pane still says \"asking the device…\" and nothing re-asks")
	}
	if !slices.Equal(asked, []string{"1"}) {
		t.Errorf("re-asked %v, want the device log once", asked)
	}
	// once answered, another connect asks nothing again
	protocol.ApplyRecord(st, protocol.Record{"l": {" Sep 23 10:00:00:000000 I/tag[1]: up"}})
	st.Disconnect()
	m.dispatch(logicMsg{})
	protocol.ApplyRecord(st, playingRecord())
	m.dispatch(logicMsg{})
	for _, c := range collect() {
		if c.Mid == 93 {
			t.Errorf("an answered log was asked for again on reconnect: %+v", c)
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
	m.rows, m.cols = 40, 120
	m.dispatch(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.viewContent() // the frame before the terminal's answer
	if m.volBlk == nil {
		t.Fatal("setup: the full player should draw the rail")
	}
	m.Update(light)
	if m.themeDark {
		t.Fatal("setup: a white background should switch to the light palette")
	}
	m.viewContent()
	if want := m.buildVolRail(st.Snap(), len(m.volBlk)-1); !slices.Equal(m.volBlk, want) {
		t.Errorf("after the light answer the rail kept the dark palette:\n got %q\nwant %q", m.volBlk[len(m.volBlk)-2], want[len(want)-2])
	}
	fresh, _, _ := makeModel(t)
	fresh.rows, fresh.cols = 40, 120
	fresh.dispatch(light)
	fresh.viewContent()
	if !slices.Equal(m.volBlk, fresh.volBlk) {
		t.Error("the rail differs from one drawn light from the start")
	}
}

// The palette switch dropped the album tint (amb = nil) but kept ambKey, so
// refreshAmbient took the cover as already resolved: the seek bar and the
// cover frame lost the album colour for the rest of the track.
func TestThemeAnswerKeepsTheAlbumTint(t *testing.T) {
	for _, mode := range []string{"kitty", "halfblock"} {
		t.Run(mode, func(t *testing.T) {
			m, st, _ := makeModel(t)
			m.rows, m.cols = 40, 120
			m.cfg.Art, m.cfg.ArtMode = true, mode
			m.viewContent()
			m.sty.trueColor = true // a truecolor terminal, so the half-block cover is drawn
			st.SetArt(st.Snap().CoverURL, image.NewRGBA(image.Rect(0, 0, 40, 40)), color.RGBA{R: 210, G: 30, B: 30, A: 255}, true)
			m.viewContent()
			if m.amb == nil {
				t.Fatal("setup: a coloured cover should tint the player")
			}
			m.Update(tea.BackgroundColorMsg{Color: color.White})
			m.sty.trueColor = true // the rebuilt palette probed the test's stdout again
			m.viewContent()
			if m.amb == nil {
				t.Error("after the light answer the album tint is gone for the rest of the track")
			}
		})
	}
}

// ---- help page -----------------------------------------------------------------

// The help page is 37 rows and did not scroll: at 80×24 (and at the 25-row
// full size) the services, logs, diagnostics and everywhere groups were cut
// off and no key reached them. It now scrolls like the diagnostics, the
// footer saying how much is off-screen, and opens at its top.
func TestHelpScrollsToEveryGroupAt80x24(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 24, 80
	body := func() string { // the frame below the header and the notice line
		return strings.Join(strings.Split(clean(m.viewContent()), "\n")[3:], "\n")
	}
	m.key(kr('?'))
	first := body()
	if !strings.Contains(first, "more rows below") {
		t.Fatalf("a too-short help page should offer to scroll:\n%s", first)
	}
	seen := map[string]bool{}
	for range 40 {
		frame := clean(m.viewContent())
		if n := strings.Count(frame, "\n") + 1; n != 24 {
			t.Fatalf("the frame is %d rows, want 24", n)
		}
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
	if up := body(); !strings.Contains(up, "· 1 below") {
		t.Errorf("↑ from the bottom should scroll back one row:\n%s", up)
	}
	for range 10 {
		m.key(ke(kLeft)) // a page at a time, clamped at the top
	}
	if body() != first {
		t.Error("← past the top should come back to the first row")
	}
	// it opens at its top, whatever the diagnostics' scroll was
	m.key(kr('5'))
	for range 5 {
		m.key(ke(kDown))
	}
	m.key(kr('?'))
	if body() != first {
		t.Error("the help page should open at its top")
	}
	// too narrow for the scroll count and the way out together: the count stays
	m.cols = MiniCols
	if foot := frameRowWith(clean(m.viewContent()), "more rows below"); foot == "" || strings.Contains(foot, "back to the player") {
		t.Errorf("narrow help footer = %q, want the scroll count alone", foot)
	}
}

// The help page says the playback keys work from every view that does not use
// the letter, but on the help page itself they were ignored.
func TestHelpPageTakesThePlaybackKeys(t *testing.T) {
	m, _, collect := makeModel(t)
	m.key(kr('?'))
	collect()
	m.key(kr(' '))
	m.key(kr('n'))
	if got := collect(); len(got) != 2 || got[0].Data != "PAUSE" || got[1].Data != "NEXT" {
		t.Errorf("space n on the help page sent %+v, want [40 PAUSE] [40 NEXT]", got)
	}
	if m.view != viewHelp {
		t.Errorf("a playback key closed the help page (view %s)", viewNames[m.view])
	}
}

// The help page and the footer's second page said t flips "remaining ⇄
// elapsed"; it flips the right-hand time between remaining and the TOTAL (the
// README's wording) — elapsed is always on the left.
func TestTSwitchesRemainingAndTotal(t *testing.T) {
	m, st, _ := makeModel(t)
	s := st.Snap()
	if got, want := m.fmtRight(s.Track.TotalTime, s.Pos), "-"+FmtMs(s.Track.TotalTime-s.Pos); got != want {
		t.Errorf("right-hand time = %q, want the remaining %q", got, want)
	}
	m.key(kr('t'))
	if got, want := m.fmtRight(s.Track.TotalTime, s.Pos), FmtMs(s.Track.TotalTime); got != want {
		t.Errorf("after t the right-hand time is %q, want the total %q", got, want)
	}
	for _, g := range helpGroups {
		for _, k := range g.keys {
			if k[0] == "t" && (strings.Contains(k[1], "elapsed") || !strings.Contains(k[1], "remaining ⇄ total")) {
				t.Errorf("help page: t %q", k[1])
			}
		}
	}
	for _, h := range append(slices.Clone(playerHints), playerHintsRare...) {
		if strings.Contains(h, "elapsed") {
			t.Errorf("footer hint says elapsed: %q", h)
		}
	}
}

// ---- notices -------------------------------------------------------------------

// The sixth b (past 90 min) turns the timer off and puts night mode back, but
// the notice still said "night mode on".
func TestBedtimeOffNoticeSaysNightModeIsBack(t *testing.T) {
	m, st, _ := makeModel(t)
	protocol.ApplyRecord(st, protocol.Record{"n": {"values=off"}})
	for range len(sleepPresets) {
		m.key(kr('b'))
	}
	if !strings.HasSuffix(m.notice, "night mode on") {
		t.Errorf("armed: notice = %q", m.notice)
	}
	m.key(kr('b'))
	if s := st.Snap(); s.Night {
		t.Fatal("setup: night mode should be back off")
	}
	if want := "bedtime · sleep timer off · night mode off"; m.notice != want {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}
}

// With every source reported off, the idle line read "start something on no
// streaming service is switched on · 3 opens the services".
func TestIdleLineWithNoSourceOn(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{
		"c": {"spotify.eng=", "spotify.cfg=none", "airplay=off", "dlna=off", "bt=off", "tidal=off", "qobuz=off", "usb=off", "cast=off"},
		"t": {"MID-Read:51 Data:2 Length:1"}, "v": {"MID-Read:64 Data:30 Length:2"},
	})
	m, _, _ := modelWith(st)
	for _, size := range [][2]int{{40, 140}, {20, 80}} { // the idle clock, then the compact player
		m.rows, m.cols = size[0], size[1]
		out := clean(m.viewContent())
		if strings.Contains(out, "start something on no streaming service") || !strings.Contains(out, "no streaming service is switched on · 3 opens the services") {
			t.Errorf("%dx%d: idle line is garbled:\n%s", size[1], size[0], out)
		}
	}
}

// LP10_OTA_URL="" switches the vendor check off (its worker returns at once),
// but u still raised the request and the update line said "checking…" for the
// rest of the run.
func TestUpdateCheckSwitchedOffDoesNotHang(t *testing.T) {
	t.Setenv("LP10_OTA_URL", "")
	m, st, _ := makeModel(t)
	applyFixtureRecords(st, "device_record.txt")
	m.rows, m.cols = 60, 160
	m.key(kr('5'))
	m.key(kr('u'))
	if out := clean(m.viewContent()); strings.Contains(out, "checking…") {
		t.Error("with the check switched off, u leaves the update line on \"checking…\" forever")
	}
	if !strings.Contains(m.notice, "update check is off") {
		t.Errorf("notice = %q, want the check named off", m.notice)
	}
	// switched on, u raises the request (no OTA worker runs here, so nothing
	// leaves the test)
	t.Setenv("LP10_OTA_URL", "http://127.0.0.1:9/")
	m.key(kr('u'))
	if !st.DiagnosticView(time.Now()).OTAPending {
		t.Error("with the check on, u should raise the request")
	}
}

// ---- volume --------------------------------------------------------------------

// Volume keys before the device's first volume read of the run computed from
// the cached snapshot: cached 40, the phone set 70 meanwhile, ↑ during
// "connecting…" sent 64 42 and the room dropped from 70 to 42 on connect.
func TestVolumeKeysWaitForTheLiveVolume(t *testing.T) {
	st := protocol.NewState()
	st.Preload(nil, 0, 40) // the last run's snapshot
	m, _, collect := modelWith(st)
	for _, ev := range []keyEvent{ke(kUp), ke(kDown), kr('+'), kr('-'), kr('m')} {
		m.key(ev)
	}
	if got := collect(); len(got) != 0 {
		t.Fatalf("sent %+v before the device reported its volume", got)
	}
	if v := st.Snap().Vol; v != 40 {
		t.Errorf("the cached volume moved to %d", v)
	}
	if !strings.Contains(m.notice, "volume not read yet") {
		t.Errorf("notice = %q, want why the key did nothing", m.notice)
	}
	if !m.flash["mute"].IsZero() {
		t.Error("a refused mute flashed its button")
	}
	protocol.ApplyRecord(st, protocol.Record{"v": {"MID-Read:64 Data:70 Length:2"}}) // the live read: the phone's 70
	m.key(ke(kUp))
	if got := collect(); len(got) != 1 || got[0].Mid != 64 || got[0].Data != "72" {
		t.Errorf("↑ after the live read sent %+v, want [64 72]", got)
	}
	// a later outage keeps the keys: the volume in hand is this run's own
	m.dispatch(logicMsg{})
	st.Disconnect()
	m.key(kr('-'))
	if got := collect(); len(got) != 1 || got[0].Data != "70" {
		t.Errorf("- during a later outage sent %+v, want [64 70]", got)
	}
}

// ---- sleep timer ---------------------------------------------------------------

// A deadline the link kept from firing that ran out long before the link came
// back (an outage through the night) no longer pauses the morning's playback
// on reconnect: the timer is cancelled with a notice, and a bedtime arming
// still puts night mode back. A short blip still ends in the pause.
func TestSleepTooLateOnReconnectIsCancelled(t *testing.T) {
	m, st, collect := makeModel(t)
	protocol.ApplyRecord(st, protocol.Record{"n": {"  : values=off"}}) // baseline: night off
	m.key(kr('b'))                                                     // bedtime: timer armed, night on
	collect()
	st.Disconnect()
	m.sleepAt = time.Now().Add(-8 * time.Hour) // it ran out overnight, the link still down
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Fatalf("sent %+v while the link was down", got)
	}
	protocol.ApplyRecord(st, playingRecord()) // morning: the link is back and music plays
	m.dispatch(logicMsg{})
	got := collect()
	if slices.ContainsFunc(got, func(c protocol.Command) bool { return c.Mid == 40 }) {
		t.Errorf("the overnight timer paused the morning's playback: %+v", got)
	}
	if !slices.ContainsFunc(got, func(c protocol.Command) bool { return c.Mid == 91 && c.Data == "0" }) {
		t.Errorf("sent %+v, want night mode put back (91 0)", got)
	}
	if s := st.Snap(); !m.sleepAt.IsZero() || m.bedtime || s.Night || s.Playing != 0 {
		t.Errorf("after the reconnect: timer %v, bedtime %v, night %v, playing %d", m.sleepAt, m.bedtime, s.Night, s.Playing)
	}
	if want := "sleep timer cancelled · it ran out 8h ago"; m.notice != want {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}

	// nine minutes late (a blip): it still pauses
	m.key(kr('s'))
	st.Disconnect()
	m.sleepAt = time.Now().Add(-9 * time.Minute)
	m.dispatch(logicMsg{})
	protocol.ApplyRecord(st, playingRecord())
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 1 || got[0].Mid != 40 || got[0].Data != "PAUSE" {
		t.Errorf("a blip past the deadline sent %+v, want [40 PAUSE]", got)
	}
}
