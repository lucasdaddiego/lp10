package tui

import (
	"image/color"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

// ============================================================================
// Pure display/formatting helpers — called directly, every branch.
// ============================================================================

func TestCov_firstSegToneStrBalStrPresetName(t *testing.T) {
	if firstSeg("29-1d316f0c-10", '-') != "29" {
		t.Error("firstSeg with sep wrong")
	}
	if firstSeg("nosep", '-') != "nosep" {
		t.Error("firstSeg without sep should pass through")
	}
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

func TestCov_padHelpers(t *testing.T) {
	if got := padDisp("ab", 5); got != "ab   " {
		t.Errorf("padDisp(ab,5) = %q", got)
	}
	if got := padDisp("abcde", 3); got != "abcde" {
		t.Errorf("padDisp wide no-op = %q", got)
	}
	if got := padVis("ab", 5); got != "ab   " {
		t.Errorf("padVis(ab,5) = %q", got)
	}
	if got := padVis("abcde", 3); got != "abcde" {
		t.Errorf("padVis wide no-op = %q", got)
	}
	if got := labelGap("ab", 5); got != "   " {
		t.Errorf("labelGap(ab,5) = %q (len %d)", got, len(got))
	}
	if got := labelGap("abcdef", 3); got != "" {
		t.Errorf("labelGap overflow should be empty, got %q", got)
	}
}

func TestCov_betweenSplitCcell(t *testing.T) {
	if got := between("L", 1, "R", 1, 10); got != "L"+strings.Repeat(" ", 8)+"R" {
		t.Errorf("between normal = %q", got)
	}
	// gap < 1 clamps to a single space
	if got := between("L", 5, "R", 5, 8); got != "L R" {
		t.Errorf("between clamp = %q, want %q", got, "L R")
	}
	if got := splitWidth(10, 3); len(got) != 3 || got[0] != 4 || got[1] != 3 || got[2] != 3 {
		t.Errorf("splitWidth(10,3) = %v, want [4 3 3]", got)
	}
	if got := splitWidth(9, 3); got[0] != 3 || got[1] != 3 || got[2] != 3 {
		t.Errorf("splitWidth(9,3) = %v, want [3 3 3]", got)
	}
	if w := lipgloss.Width(ccell("x", 5)); w != 5 {
		t.Errorf("ccell width = %d, want 5", w)
	}
}

func TestCov_dispWindowEdges(t *testing.T) {
	// content entirely before the window: a,b,c skipped, "de" taken
	if got := dispWindow("abcdef", 3, 2); got != "de" {
		t.Errorf("dispWindow(abcdef,3,2) = %q, want de", got)
	}
	// a double-width rune straddling each edge renders its visible cells as spaces
	got := dispWindow("漢字漢字", 1, 4)
	if DispW(got) != 4 {
		t.Errorf("dispWindow straddle width = %d, want 4", DispW(got))
	}
	if !strings.Contains(got, "字") {
		t.Errorf("dispWindow straddle should keep the fully-inside 字: %q", got)
	}
}

func TestCov_glyphsAndDetectAmb(t *testing.T) {
	// glyphs(2) is the ASCII fallback set (CJK locale / ambiguous-wide terminal).
	if g := glyphs(2); g["play"] != ">" || g["note"] != "*" || g["ell"] != "..." {
		t.Errorf("glyphs(2) ASCII fallback wrong: %v", g["play"])
	}
	if g := glyphs(1); g["play"] != "▶" || g["note"] != "♪" {
		t.Errorf("glyphs(1) unicode set wrong: %v", g["play"])
	}
	// every glyph has a fallback, and every fallback is pure ASCII
	one, two := glyphs(1), glyphs(2)
	for k := range one {
		fb, ok := two[k]
		if !ok {
			t.Errorf("glyph %q has no ASCII fallback", k)
		}
		for _, r := range fb {
			if r > 0x7e {
				t.Errorf("fallback for %q is not ASCII: %q", k, fb)
			}
		}
	}

	// detectAmb: CJK locales -> 2, everything else -> 1, with LC_ALL > LC_CTYPE > LANG.
	for _, lang := range []string{"ja_JP.UTF-8", "ko_KR.UTF-8", "zh_CN.UTF-8"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("LC_ALL", "")
			t.Setenv("LC_CTYPE", "")
			t.Setenv("LANG", lang)
			if got := detectAmb(); got != 2 {
				t.Errorf("detectAmb(%s) = %d, want 2", lang, got)
			}
		})
	}
	t.Run("default", func(t *testing.T) {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", "en_US.UTF-8")
		if got := detectAmb(); got != 1 {
			t.Errorf("detectAmb(en) = %d, want 1", got)
		}
	})
	t.Run("unset", func(t *testing.T) {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", "C")
		if got := detectAmb(); got != 1 {
			t.Errorf("detectAmb(C) = %d, want 1", got)
		}
	})
	t.Run("lc_all_precedence", func(t *testing.T) {
		t.Setenv("LC_ALL", "ja_JP.UTF-8")
		t.Setenv("LC_CTYPE", "en_US.UTF-8")
		t.Setenv("LANG", "en_US.UTF-8")
		if got := detectAmb(); got != 2 {
			t.Errorf("detectAmb(LC_ALL=ja) = %d, want 2 (LC_ALL wins)", got)
		}
	})
}

// ============================================================================
// theme.go pure helpers
// ============================================================================

func TestCov_clampFClampRangeTo8(t *testing.T) {
	if clampF(-0.5) != 0 || clampF(0.5) != 0.5 || clampF(1.5) != 1 {
		t.Error("clampF wrong")
	}
	if clampRange(0.1, 0.35, 0.85) != 0.35 {
		t.Error("clampRange below")
	}
	if clampRange(0.5, 0.35, 0.85) != 0.5 {
		t.Error("clampRange in")
	}
	if clampRange(0.99, 0.35, 0.85) != 0.85 {
		t.Error("clampRange above")
	}
	if to8(-0.1) != 0 || to8(0.5) != 128 || to8(2) != 255 {
		t.Errorf("to8 wrong: %d %d %d", to8(-0.1), to8(0.5), to8(2))
	}
}

func TestCov_rampIdx(t *testing.T) {
	const n = 5 // the fill ramp's length
	chk := func(pos, span, want int) {
		if got := rampIdx(n, pos, span); got != want {
			t.Errorf("rampIdx(pos=%d span=%d) = %d, want %d", pos, span, got, want)
		}
	}
	chk(0, 1, 0)   // span<=1 -> ratio 0
	chk(2, 5, 2)   // middle
	chk(10, 2, 4)  // i>=len -> clamp to last
	chk(-10, 5, 0) // negative pos -> i<0 -> clamp to first
}

func TestCov_hslRGBAllArms(t *testing.T) {
	// every hue feeds a different switch arm (int(hp) 0..5) and yields a
	// distinct colour at fixed s/l
	seen := map[[3]uint8]bool{}
	for _, h := range []float64{30, 90, 150, 210, 270, 330} {
		r, g, b := hslRGB(h, 0.7, 0.5)
		seen[[3]uint8{r, g, b}] = true
	}
	if len(seen) != 6 {
		t.Errorf("6 hues produced %d distinct colours, want 6", len(seen))
	}
	// a negative hue wraps (hp<0 correction) and equals its +360 equivalent
	r1, g1, b1 := hslRGB(-30, 1, 0.5)
	r2, g2, b2 := hslRGB(330, 1, 0.5)
	if r1 != r2 || g1 != g2 || b1 != b2 {
		t.Error("hslRGB(-30) should equal hslRGB(330)")
	}
	// anchor: pure red
	if r, g, b := hslRGB(0, 1, 0.5); r != 255 || g != 0 || b != 0 {
		t.Errorf("hslRGB(0,1,0.5) = %d,%d,%d, want 255,0,0", r, g, b)
	}
}

func TestCov_writeDecAndStylePen(t *testing.T) {
	for _, v := range []uint8{0, 7, 42, 100, 255} {
		var b strings.Builder
		writeDec(&b, v)
		if got, want := b.String(), strconv.Itoa(int(v)); got != want {
			t.Errorf("writeDec(%d) = %q, want %q", v, got, want)
		}
	}
	// an empty painted line needs no reset
	var b strings.Builder
	paintEndLine(&b)
	if b.Len() != 0 {
		t.Error("paintEndLine on an empty line should write nothing")
	}
	// an unstyled style flattens to an empty pen that renders text as is
	if p := stylePen(lipgloss.NewStyle()); p.render("x") != "x" {
		t.Errorf("unstyled pen renders %q", p.render("x"))
	}
}

// ============================================================================
// friendlyError — every mapped case
// ============================================================================

// friendlyError condenses a raw dial error to a calm, actionable line; an
// error it does not recognise passes through unchanged.
func TestCov_friendlyErrorAllCases(t *testing.T) {
	const (
		host    = "can't find the device — are you on the home network?"
		route   = "no route to the device — check the network"
		refused = "the device refused the connection on :2018"
		timeout = "connection timed out — the device may be off or away"
	)
	cases := map[string]string{
		"cannot reach :2018: dial tcp: lookup lp10.local: no such host": host,
		"Could not resolve hostname x":                                  host,
		"dial tcp 192.0.2.13:2018: connect: no route to host":           route,
		"dial tcp: connect: network is unreachable":                     route,
		"dial tcp 192.0.2.13:2018: connect: Connection refused":         refused,
		"dial tcp 192.0.2.13:2018: i/o timeout":                         timeout,
		"Operation timed out":                                           timeout,
		"connected, but no answer for 6s":                               timeout,
		"command not delivered":                                         "command not delivered",
		"":                                                              "",
	}
	for in, want := range cases {
		if got := friendlyError(in); got != want {
			t.Errorf("friendlyError(%q) = %q, want %q", in, got, want)
		}
	}
}

// ============================================================================
// translate / translateAll — every key class
// ============================================================================

func TestCov_translateEveryType(t *testing.T) {
	cases := []struct {
		key  tea.Key
		want keyKind
	}{
		{tea.Key{Code: tea.KeyEnter}, kEnter},
		{tea.Key{Code: tea.KeyEscape}, kEsc},
		{tea.Key{Code: tea.KeyBackspace}, kOther}, // unmapped: no UI action bound
		{tea.Key{Code: tea.KeyLeft}, kLeft},
		{tea.Key{Code: tea.KeyRight}, kRight},
		{tea.Key{Code: tea.KeyUp}, kUp},
		{tea.Key{Code: tea.KeyDown}, kDown},
		{tea.Key{Code: tea.KeyTab}, kTab},
		{tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}, kTab}, // v2: shift+tab is KeyTab + ModShift; same step
		{tea.Key{Code: tea.KeySpace, Text: " "}, kRune},
		{tea.Key{Code: 'a', Mod: tea.ModCtrl}, kOther}, // modified rune: unmapped
	}
	for _, c := range cases {
		if got := translate(c.key); got.kind != c.want {
			t.Errorf("translate(%v).kind = %d, want %d", c.key, got.kind, c.want)
		}
	}
	if ev := translate(tea.Key{Code: tea.KeySpace, Text: " "}); ev.r != ' ' {
		t.Errorf("space rune = %q, want space", ev.r)
	}
	// a printable key carries its rune in Text
	if ev := translate(tea.Key{Code: 'm', Text: "m"}); ev.kind != kRune || ev.r != 'm' {
		t.Errorf("single rune = %+v", ev)
	}
	// multi-rune Text expands 1:1 via translateAll (legacy coalesced input)
	evs := translateAll(tea.KeyPressMsg{Code: 'a', Text: "abc"})
	if len(evs) != 3 || evs[2].r != 'c' {
		t.Errorf("translateAll multi-rune = %+v", evs)
	}
	// bracketed paste takes the same expansion via runeEvents
	if evs := runeEvents("mn"); len(evs) != 2 || evs[0].r != 'm' || evs[1].r != 'n' {
		t.Errorf("runeEvents = %+v", evs)
	}
}

// Under the Kitty keyboard protocol a shifted printable arrives as its BASE
// code + ModShift with the shifted character in Text ('?' = '/'+shift), and
// CapsLock rides as a modifier bit on ordinary letters. Those are text input —
// '?', '+', '_', 'Q' must keep working when the terminal upgrades the wire
// encoding (ultraviolet's own MatchString masks exactly Shift|CapsLock).
func TestCov_translateKittyShiftedPrintables(t *testing.T) {
	cases := []struct {
		key  tea.Key
		want rune
	}{
		{tea.Key{Code: '/', Text: "?", Mod: tea.ModShift}, '?'},
		{tea.Key{Code: '=', Text: "+", Mod: tea.ModShift}, '+'},
		{tea.Key{Code: '-', Text: "_", Mod: tea.ModShift}, '_'},
		{tea.Key{Code: 'q', Text: "Q", Mod: tea.ModShift}, 'Q'},
		{tea.Key{Code: 'q', Text: "Q", Mod: tea.ModCapsLock}, 'Q'},
		{tea.Key{Code: 's', Text: "S", Mod: tea.ModShift}, 'S'},
		{tea.Key{Code: 'm', Text: "m"}, 'm'}, // plain, for symmetry
	}
	for _, c := range cases {
		if ev := translate(c.key); ev.kind != kRune || ev.r != c.want {
			t.Errorf("translate(%+v) = %+v, want rune %q", c.key, ev, c.want)
		}
	}
	// ctrl/alt-modified printables stay unmapped (they are chords, not text)
	for _, k := range []tea.Key{
		{Code: 'q', Text: "q", Mod: tea.ModCtrl},
		{Code: 'q', Text: "q", Mod: tea.ModAlt},
		{Code: 'q', Text: "q", Mod: tea.ModShift | tea.ModCtrl},
	} {
		if ev := translate(k); ev.kind != kOther {
			t.Errorf("translate(%+v) = %+v, want kOther", k, ev)
		}
	}
	// shifted multi-rune text still expands via translateAll
	if evs := translateAll(tea.KeyPressMsg{Code: '=', Text: "++", Mod: tea.ModShift}); len(evs) != 2 || evs[1].r != '+' {
		t.Errorf("shifted multi-rune = %+v", evs)
	}
}

// ============================================================================
// key dispatch — rune keys and view-specific directionals/enter
// ============================================================================

func TestCov_KeyRunes(t *testing.T) {
	m, _, collect := makeModel(t)
	for _, r := range " np=+-_" {
		m.key(kr(r))
	}
	if got := wire(collect()); !slices.Equal(got, []string{"POP", "NXT", "PRE", "VOL:46", "VOL:48", "VOL:46", "VOL:44"}) {
		t.Errorf("space n p = + - _ sent %v", got)
	}
	// e opens the equalizer
	m.key(kr('e'))
	if m.view != viewEQ {
		t.Error("e should open the equalizer")
	}
	// an unmapped rune is a no-op
	if m.key(kr('z')) {
		t.Error("an unmapped rune should not quit")
	}
	// Q backs out of a view first, then quits from the player
	if m.key(kr('Q')) || m.view != viewPlayer {
		t.Error("Q in a view should return to the player, not quit")
	}
	if !m.key(kr('Q')) {
		t.Error("Q should quit")
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("view keys sent %v", wire(got))
	}
}

func TestCov_KeyViews(t *testing.T) {
	m, st, _ := makeModel(t)

	// equalizer: up/down move the control selection, left/right adjust, enter toggles
	st.ApplyTunnel("EQE", 0) // toggling needs a known value
	st.ApplyTunnel("TRE", 0)
	m.setView(viewEQ)
	m.eqFocus = 2 // TRE (ranged)
	m.key(ke(kUp))
	if m.eqFocus != 1 {
		t.Errorf("EQ up: eqFocus = %d, want 1", m.eqFocus)
	}
	m.key(ke(kDown))
	if m.eqFocus != 2 {
		t.Errorf("EQ down: eqFocus = %d, want 2", m.eqFocus)
	}
	m.key(ke(kRight))
	m.key(ke(kLeft))
	m.eqFocus = 0 // EQE (toggle)
	m.key(ke(kEnter))
	if v, _ := st.EQValue("EQE"); v != 1 {
		t.Error("enter on the EQ switch should toggle it")
	}
	// a key the equalizer does not claim falls through to playback
	if m.eqKey(kr('x')) {
		t.Error("eqKey should not claim a rune")
	}

	// shift+tab also steps the views (it folds into kTab at translate)
	p := m.view
	m.key(translate(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if m.view == p {
		t.Error("shift+tab should step the views")
	}

	// player: up/down adjust the volume
	m.view = viewPlayer
	m.key(ke(kUp))
	if v := st.Snap().Vol; v != 46 {
		t.Errorf("player up: vol = %d, want 46", v)
	}
	m.key(ke(kDown))
	if v := st.Snap().Vol; v != 44 {
		t.Errorf("player down: vol = %d, want 44", v)
	}
	if m.playerKey(kr('x')) {
		t.Error("playerKey should not claim a rune")
	}
	if m.scrollKey(ke(kEnter)) {
		t.Error("scrollKey should not claim enter")
	}
	// the playback keys ignore non-rune events
	m.playbackKey(ke(kEnter))
}

// ============================================================================
// Update — every message branch
// ============================================================================

func TestCov_UpdateMessages(t *testing.T) {
	// WindowSizeMsg sets rows/cols
	m, _, _ := makeModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.cols != 100 || m.rows != 40 {
		t.Errorf("WindowSizeMsg: %dx%d, want 100x40", m.cols, m.rows)
	}

	// logicMsg advances the marquee, refreshes the title and reschedules
	scroll := m.scroll
	m.curTitle = ""
	if _, cmd := m.Update(logicMsg{}); cmd == nil || m.scroll != scroll+1 {
		t.Error("logicMsg should advance scroll and reschedule")
	}
	if m.curTitle == "" {
		t.Error("logicMsg should recompute the window title")
	}

	// frameMsg with the search figure live while disconnected advances the frame
	d, _, _ := modelWith(protocol.NewState())
	d.searchLive = true
	f := d.frame
	if _, cmd := d.Update(frameMsg{}); cmd == nil {
		t.Error("frameMsg should reschedule")
	}
	if d.frame != f+1 {
		t.Errorf("search frame %d -> %d, want +1", f, d.frame)
	}

	// a bracketed paste drives the hotkeys ('2' opens the equalizer)
	if _, _ = m.Update(tea.PasteMsg{Content: "2"}); m.view != viewEQ {
		t.Error("PasteMsg should dispatch its runes as keys")
	}

	// Ctrl-C marks interrupted and quits
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil || !m.interrupted {
		t.Error("ctrl-c should set interrupted and quit")
	}

	// a plain key press that does not quit returns no command
	if _, cmd := m.Update(tea.KeyPressMsg{Code: '1', Text: "1"}); cmd != nil {
		t.Error("a view key should return no command")
	}

	// the View carries the alt screen and the window title
	v := m.View()
	if !v.AltScreen || v.WindowTitle != m.curTitle {
		t.Errorf("View: alt=%v title=%q, want the alt screen and %q", v.AltScreen, v.WindowTitle, m.curTitle)
	}
}

// The terminal's background answer picks the palette (theme = auto).
func TestCov_UpdateBackgroundColor(t *testing.T) {
	m, _, _ := makeModel(t)
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.bgDark == nil || *m.bgDark || m.themeDark {
		t.Error("a white background should select the light palette")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if !m.themeDark {
		t.Error("a black background should select the dark palette again")
	}
}

// ============================================================================
// controller methods
// ============================================================================

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

func TestCov_computeTitle(t *testing.T) {
	// idle -> the device name, with non-printable runes filtered out
	m, _, _ := modelWith(protocol.NewState())
	m.cfg.Name = "ab\x01cd"
	if got := m.computeTitle(m.st.Snap()); got != "abcd" {
		t.Errorf("computeTitle(idle, ctrl char) = %q, want abcd", got)
	}
	// over-long names are capped at MaxTitleLength
	m.cfg.Name = strings.Repeat("x", MaxTitleLength+10)
	if got := m.computeTitle(m.st.Snap()); len([]rune(got)) != MaxTitleLength {
		t.Errorf("computeTitle long name len = %d, want %d", len([]rune(got)), MaxTitleLength)
	}
	// a track title is "♪ Name — Artist"
	mp, _, _ := makeModel(t)
	if got := mp.computeTitle(mp.st.Snap()); got != GL["note"]+" De Música Ligera — Soda Stereo" {
		t.Errorf("computeTitle(track) = %q", got)
	}
	// playing without a title: the device name, not a guess
	mu, _, _ := modelWith(untitledState())
	if got := mu.computeTitle(mu.st.Snap()); got != defaultCfg().Name {
		t.Errorf("computeTitle(untitled) = %q, want the device name", got)
	}
}

// ============================================================================
// Init / ticks / cellPixelSize
// ============================================================================

func TestCov_InitTicksCellPixel(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.Init() == nil {
		t.Error("Init should return a batched command")
	}
	m.cfg.Theme = "dark" // a fixed theme asks the terminal nothing
	if m.Init() == nil {
		t.Error("Init with a fixed theme still starts the ticks")
	}
	// the tick commands sleep their interval then yield the right message type
	if _, ok := logicTick()().(logicMsg); !ok {
		t.Error("logicTick cmd should yield a logicMsg")
	}
	if _, ok := frameTick(time.Millisecond)().(frameMsg); !ok {
		t.Error("frameTick cmd should yield a frameMsg")
	}
	// cellPixelSize: no tty in tests -> (0,0); just exercise it without panicking
	if w, h := cellPixelSize(); w != 0 || h != 0 {
		t.Logf("cellPixelSize returned %dx%d (a real tty?)", w, h)
	}
}

// ============================================================================
// stack / frameBody / centreRows / joinCols — direct composition
// ============================================================================

func TestCov_stack(t *testing.T) {
	if stack(nil, nil, nil, 0) != nil {
		t.Error("stack(h<=0) should be nil")
	}
	out := stack([]string{"H"}, []string{"M"}, []string{"F"}, 3)
	if len(out) != 3 || out[0] != "H" || out[2] != "F" {
		t.Errorf("stack normal = %v", out)
	}
	// middle overflows the region and is trimmed from the bottom
	out = stack([]string{"H"}, []string{"m1", "m2", "m3"}, []string{"F"}, 3)
	if len(out) != 3 || out[0] != "H" || out[2] != "F" || out[1] != "m1" {
		t.Errorf("stack overflow-trim = %v", out)
	}
	// top + bottom exceed h: the region clamps to 0, tail still pins to the bottom
	out = stack([]string{"a", "b"}, []string{"m"}, []string{"y", "z"}, 3)
	if len(out) != 3 || out[2] != "z" {
		t.Errorf("stack region<0 = %v", out)
	}
}

func TestCov_frameBody(t *testing.T) {
	if frameBody(nil, nil, 0, false) != nil {
		t.Error("frameBody(h<=0) should be nil")
	}
	// tail >= h: only the last h tail lines survive
	out := frameBody([]string{"c"}, []string{"t1", "t2", "t3"}, 2, false)
	if len(out) != 2 || out[0] != "t2" || out[1] != "t3" {
		t.Errorf("frameBody tail>=h = %v", out)
	}
	// content overflows the room above the tail -> trimmed from the bottom
	out = frameBody([]string{"c1", "c2", "c3"}, []string{"F"}, 3, false)
	if len(out) != 3 || out[0] != "c1" || out[2] != "F" {
		t.Errorf("frameBody content-trim = %v", out)
	}
	// centred content
	out = frameBody([]string{"c"}, []string{"F"}, 4, true)
	if len(out) != 4 || out[3] != "F" || out[1] != "c" {
		t.Errorf("frameBody centred = %v", out)
	}
}

func TestCov_centreRows(t *testing.T) {
	if got := centreRows(nil, 3); got != nil {
		t.Errorf("centreRows(empty) = %v", got)
	}
	col := []string{"ab", "cd", "ef"}
	if got := centreRows(col, 2); !slices.Equal(got, col) {
		t.Errorf("a taller column comes back as is, got %v", got)
	}
	got := centreRows([]string{"ab"}, 4)
	if !slices.Equal(got, []string{"  ", "ab", "  ", "  "}) {
		t.Errorf("centreRows(1 in 4) = %q", got)
	}
}

// ============================================================================
// render helpers: sourceStyle, fullSourceLine, controlsRow, metaLines,
// footerRow EQ hint, eqSliderRow, clipStyled
// ============================================================================

func TestCov_sourceStyle(t *testing.T) {
	st := newTheme()
	cases := map[string]string{
		"Spotify":   "#1db954",
		"TIDAL":     "#4fd4d4",
		"AirPlay":   "#cfd6df",
		"Bluetooth": "#4a90d9",
	}
	for name, hex := range cases {
		if got := sourceStyle(st, name).GetForeground(); got != lipgloss.Color(hex) {
			t.Errorf("sourceStyle(%s) fg = %v, want %s", name, got, hex)
		}
	}
	// unknown falls back to the theme accent
	if sourceStyle(st, "Whatever").GetForeground() != st.sAcc.GetForeground() {
		t.Error("unknown source should fall back to the accent")
	}
}

// The full player's source line: "● Spotify" beside a named track, the input
// before any VND, nothing when no track is shown (metaLines names the source
// there) or nothing is named.
func TestCov_fullSourceLine(t *testing.T) {
	m, _, _ := makeModel(t)
	s := m.st.Snap()
	if got := stripANSI(m.fullSourceLine(s, 60)); got != "● Spotify" {
		t.Errorf("fullSourceLine wide = %q", got)
	}
	// too narrow -> a plain dim clip that still respects the width contract
	if got := stripANSI(m.fullSourceLine(s, 5)); DispW(got) > 5 || !strings.HasSuffix(got, GL["ell"]) {
		t.Errorf("fullSourceLine narrow = %q, want a clip within 5", got)
	}
	// a track before any VND: the input
	if got := stripANSI(m.fullSourceLine(protocol.Snapshot{Source: "BT", Track: &protocol.Track{TrackName: "x"}}, 60)); got != "● Bluetooth" {
		t.Errorf("fullSourceLine(BT) = %q", got)
	}
	// no track, or nothing named -> ""
	if got := m.fullSourceLine(protocol.Snapshot{Service: "spotify"}, 60); got != "" {
		t.Errorf("fullSourceLine(no track) = %q, want empty", got)
	}
	if got := m.fullSourceLine(protocol.Snapshot{Track: &protocol.Track{TrackName: "x"}}, 60); got != "" {
		t.Errorf("fullSourceLine(nothing named) = %q, want empty", got)
	}
}

func TestCov_controlsRow(t *testing.T) {
	m, _, _ := makeModel(t)
	// withVol == false returns just the transport cluster (no volume)
	noVol := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, false))
	if strings.Contains(noVol, "vol") || strings.Contains(noVol, "%") {
		t.Errorf("controlsRow(withVol=false) should omit volume: %q", noVol)
	}
	live := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, true))
	if !strings.Contains(live, "vol") || !strings.Contains(live, "44%") || !strings.Contains(live, " mute ") {
		t.Errorf("controlsRow live = %q", live)
	}
	if DispW(live) != 80 {
		t.Errorf("controlsRow width = %d, want 80", DispW(live))
	}
	// muted + withVol shows the MUTED badge and offers unmute
	m.do("mute")
	muted := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, true))
	if !strings.Contains(muted, "MUTED") || !strings.Contains(muted, "unmute") {
		t.Errorf("controlsRow muted = %q", muted)
	}
	// a flashing button lights up even when it is not focused
	m.flash["next"] = time.Now().Add(time.Second)
	if lit, plain := m.controlsRow(m.st.Snap(), time.Now(), 80, false), m.controlsRow(m.st.Snap(), time.Now().Add(2*time.Second), 80, false); lit == plain {
		t.Error("a flashing next should be styled differently from a settled one")
	}
}

// metaLines while disconnected with an error shows the friendly reason, not
// the raw dial error.
func TestCov_metaLinesDisconnectedError(t *testing.T) {
	st := protocol.NewState()
	st.Note("cannot reach :2018: dial tcp: lookup lp10.local: no such host")
	md, _, _ := modelWith(st)
	lines := md.metaLines(md.st.Snap(), 50)
	assertWithin(t, "metaLines", lines, 50)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "connecting to LP10") || !strings.Contains(joined, "can't find the device") {
		t.Errorf("metaLines disconnected+error = %q", joined)
	}
}

func TestCov_metaLinesTrackVariants(t *testing.T) {
	m, _, _ := makeModel(t)
	// empty title -> "—", artist + album joined on the second line
	s1 := protocol.Snapshot{Track: &protocol.Track{Artist: "A", Album: "Al"}}
	l1 := stripANSI(strings.Join(m.metaLines(s1, 40), "\n"))
	if !strings.Contains(l1, "—") || !strings.Contains(l1, "A · Al") {
		t.Errorf("metaLines empty-title = %q", l1)
	}
	// no artist but an album -> the album alone, linked to an album search
	s2 := protocol.Snapshot{Track: &protocol.Track{TrackName: "T", Album: "OnlyAlbum"}}
	l2 := m.metaLines(s2, 40)
	if got := clean(l2[1]); got != "OnlyAlbum" || !strings.Contains(l2[1], spotifySearch("OnlyAlbum")) {
		t.Errorf("metaLines album-only = %q", l2[1])
	}
	assertWithin(t, "metaLines", m.metaLines(s1, 40), 40)
	assertWithin(t, "metaLines", l2, 40)
	// a bare title: an empty, unlinked second line
	l3 := m.metaLines(protocol.Snapshot{Track: &protocol.Track{TrackName: "T"}}, 40)
	if clean(l3[1]) != "" || strings.Contains(l3[1], "\x1b]8") {
		t.Errorf("metaLines title-only second line = %q", l3[1])
	}
}

func TestCov_fullMetaVariants(t *testing.T) {
	m, _, _ := makeModel(t)
	// empty title -> "—", and with no artist/album there's a single line
	out := m.fullMeta(protocol.Snapshot{Track: &protocol.Track{}}, 40)
	if len(out) != 1 || clean(out[0]) != "—" {
		t.Errorf("fullMeta empty-title = %q", out)
	}
	// the three fields on their own lines
	full := m.fullMeta(m.st.Snap(), 40)
	assertWithin(t, "fullMeta", full, 40)
	assertWithin(t, "fullMeta narrow", m.fullMeta(m.st.Snap(), 8), 8)
	if len(full) != 3 || clean(full[0]) != "De Música Ligera" || clean(full[1]) != "Soda Stereo" || clean(full[2]) != "Canción Animal" {
		t.Errorf("fullMeta = %q", full)
	}
}

func TestCov_footerRowEQHint(t *testing.T) {
	m, _, _ := makeModel(t)
	m.view = viewEQ
	if got := stripANSI(m.footerRow(80)); strings.TrimSpace(got) != eqHint || DispW(got) != 80 {
		t.Errorf("footer EQ hint = %q", got)
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

func TestCov_clipStyled(t *testing.T) {
	if got := clipStyled("abc", 10); got != "abc" {
		t.Errorf("clipStyled fits = %q", got)
	}
	if got := clipStyled("abcdefgh", 4); stripANSI(got) != "abc"+GL["ell"] {
		t.Errorf("clipStyled overflow = %q", stripANSI(got))
	}
	if got := clipStyled("abcdefgh", 0); got != "" {
		t.Errorf("clipStyled w=0 = %q, want empty", got)
	}
	if got := clipStyled("abcdefgh", 1); stripANSI(got) != "a" {
		t.Errorf("clipStyled no-ellipsis-room = %q, want hard cut", stripANSI(got))
	}
}

// ============================================================================
// renderDashboard — compact path and error lines (full + compact)
// ============================================================================

func TestCov_renderDashboardCompactAndErrors(t *testing.T) {
	// compact layout: cols between MiniCols and FullCols
	m, _, _ := makeModel(t)
	m.rows, m.cols = 20, 64
	out := clean(render(t, m))
	if strings.Contains(out, "Max volume") || !strings.Contains(out, GL["rew"]) {
		t.Errorf("compact dashboard should show the transport and nothing of the equalizer:\n%s", out)
	}

	// a connected error paints the red error line in the compact tail
	me, st, _ := makeModel(t)
	st.Note("dial tcp: connection refused")
	me.rows, me.cols = 20, 64
	if !strings.Contains(clean(render(t, me)), "the device refused the connection") {
		t.Error("compact errLine should show the friendly reason")
	}

	// and in the full layout, the idle screen included
	for _, stf := range []*protocol.State{playingState(), idleState()} {
		mf, _, _ := modelWith(stf)
		stf.Note("dial tcp: connection refused")
		mf.rows, mf.cols = 40, 120
		if !strings.Contains(clean(render(t, mf)), "the device refused the connection on :2018") {
			t.Error("full errLine should show the friendly reason")
		}
	}

	// an error older than ErrorDisplayDuration has left the player
	mo, sto, _ := makeModel(t)
	sto.Note("dial tcp: connection refused")
	mo.rows, mo.cols = 40, 120
	if got := mo.renderDashboard(sto.Snap(), time.Now().Add(ErrorDisplayDuration+time.Second), 114, true); strings.Contains(clean(strings.Join(got, "\n")), "refused") {
		t.Error("an aged error should leave the player")
	}
}

// TestCov_renderDashboardGeometry forces the full player's cover-sizing clamps
// by calling renderDashboard with full=true at a tiny width / short height and
// a known cell-pixel size, so coverH<6, the maxW reservation, coverW<8, and the
// measured-cell aspect branch all fire in one paint.
func TestCov_renderDashboardGeometry(t *testing.T) {
	m, _, _ := makeModel(t)
	m.cellW, m.cellH = 8, 16 // a real measured cell aspect (the m.cellW>0 branch)
	m.rows = 18              // short inner region -> coverH floors to 6
	if out := m.renderDashboard(m.st.Snap(), time.Now(), 40, true); len(out) != m.bodyRows() {
		t.Errorf("renderDashboard rows = %d, want %d", len(out), m.bodyRows())
	}
	// a very wide cell aspect caps the cover by width and floors its height
	m.cellW, m.cellH = 2, 40
	m.rows = 40
	if out := m.renderDashboard(m.st.Snap(), time.Now(), 60, true); len(out) != m.bodyRows() {
		t.Errorf("renderDashboard rows = %d, want %d", len(out), m.bodyRows())
	}
}

// ============================================================================
// art: noteBox, boxArt, artColumn
// ============================================================================

func TestCov_noteBox(t *testing.T) {
	m, _, _ := makeModel(t)
	// normal motif (room for the 5x3 note motif), every line w wide
	normal := m.noteBox(10, 6)
	if !strings.Contains(strings.Join(normal, "\n"), "●") {
		t.Errorf("noteBox normal should draw the note motif: %q", normal)
	}
	assertWithin(t, "noteBox", normal, 10)
	for i, ln := range normal {
		if visWidth(ln) != 10 {
			t.Errorf("noteBox line %d width %d, want 10", i, visWidth(ln))
		}
	}
	// too small for the motif -> a single centred ♪
	small := strings.Join(m.noteBox(3, 2), "\n")
	if !strings.Contains(small, GL["note"]) {
		t.Errorf("noteBox small should fall back to a single note: %q", small)
	}
	// width 0 -> blank lines, no panic
	if got := m.noteBox(0, 3); len(got) != 3 {
		t.Errorf("noteBox(0,3) len = %d, want 3", len(got))
	}
	// under a CJK locale the box falls back to the single note too
	defer func(orig int) { localeAmb = orig }(localeAmb)
	localeAmb = 2
	if got := strings.Join(m.noteBox(10, 6), "\n"); strings.Contains(got, "●") {
		t.Errorf("CJK noteBox should not draw the ambiguous-width motif: %q", got)
	}
}

// boxArt adds a one-cell frame: +2 lines, +2 columns, with the corner glyphs.
func TestCov_boxArt(t *testing.T) {
	m, _, _ := makeModel(t)
	framed := m.boxArt([]string{"abcd", "efgh"}, 4)
	if len(framed) != 4 {
		t.Fatalf("got %d lines, want 4 (2 content + top/bottom)", len(framed))
	}
	if !strings.Contains(framed[0], GL["tl"]) || !strings.Contains(framed[3], GL["bl"]) {
		t.Error("missing top/bottom frame corners")
	}
	for i, ln := range framed {
		if w := lipgloss.Width(ln); w != 6 {
			t.Errorf("framed line %d width %d, want 6 (4 + 2 border)", i, w)
		}
	}
}

// The art column has three faces: the searching arcs while (re)connecting, the
// plasma motif while there is something to show (a track, or playback without
// one), and the calm note motif when connected and idle.
func TestCov_artColumnStates(t *testing.T) {
	cases := []struct {
		name             string
		st               *protocol.State
		want             string
		motif, searching bool
	}{
		{"connecting", protocol.NewState(), "searching for LP10", false, true},
		{"playing a track", playingState(), "█", true, false},
		{"playing untitled", untitledState(), "█", true, false},
		{"idle", idleState(), "●", false, false},
	}
	for _, c := range cases {
		m, _, _ := modelWith(c.st)
		col := m.artColumn(c.st.Snap(), 24, 8)
		if len(col) != 8 {
			t.Errorf("%s: %d rows, want 8", c.name, len(col))
		}
		assertWithin(t, c.name, col, 24)
		if got := stripANSI(strings.Join(col, "\n")); !strings.Contains(got, c.want) {
			t.Errorf("%s: art column lacks %q:\n%s", c.name, c.want, got)
		}
		if m.motifLive != c.motif || m.searchLive != c.searching {
			t.Errorf("%s: motifLive=%v searchLive=%v, want %v %v", c.name, m.motifLive, m.searchLive, c.motif, c.searching)
		}
	}
}

// ============================================================================
// Second pass: remaining small/defensive branches.
// ============================================================================

func TestCov_nbSendDropOldest(t *testing.T) {
	// a full buffer drops the oldest queued item and retries (the non-blocking path)
	ch := make(chan int, 1)
	nbSend(ch, 1)
	nbSend(ch, 2) // full -> drop 1, enqueue 2
	if got := <-ch; got != 2 {
		t.Errorf("nbSend drop-oldest = %d, want 2", got)
	}
	// an unbuffered channel nobody reads never blocks the caller
	nbSend(make(chan int), 3)
}

func TestCov_UpdateUnknownAndViewZero(t *testing.T) {
	m, _, _ := makeModel(t)
	// an unrecognized message type falls through to (m, nil)
	if _, cmd := m.Update(struct{ unknownMsg int }{}); cmd != nil {
		t.Error("unknown msg should return no command")
	}
	// a 0-sized window renders nothing
	m.rows, m.cols = 0, 80
	if m.viewContent() != "" {
		t.Error("View at 0 rows should be empty")
	}
}

func TestCov_themeDegenerate(t *testing.T) {
	st := newTheme()
	if st.searchBox(0, 5, 0) != nil {
		t.Error("searchBox(w<=0) should be nil")
	}
	// a 1x1 box degrades to just the beacon dot
	if got := st.searchBox(1, 1, 0); len(got) != 1 || lipgloss.Width(got[0]) != 1 {
		t.Errorf("searchBox(1,1) = %q, want one 1-wide line", got)
	}
	// a meter with no cells is empty; frac 0 puts the head at cell 0
	if lineMeterCells(0.5, 0, nil, "●", "─") != "" {
		t.Error("lineMeterCells(cells<=0) should be empty")
	}
	if got := stripANSI(st.lineMeter(0, 10)); !strings.Contains(got, "●") {
		t.Errorf("lineMeter(frac 0) should still draw the head: %q", got)
	}
	// an empty vbar fill draws the track only
	if got := st.vbar(0, 3); len(got) != 3 || strings.Contains(stripANSI(strings.Join(got, "")), "█") {
		t.Errorf("vbar(0) = %q, want three track cells", got)
	}
}

func TestCov_marqueeZeroWidth(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.marquee("anything", 0) != "" {
		t.Error("marquee(w<=0) should be empty")
	}
}

func TestCov_transportLayoutNarrow(t *testing.T) {
	// too narrow for inter-button gaps: falls back to a solid cluster (gap 0)
	pad, widths, gap := transportLayout(2)
	if gap != 0 || len(widths) != len(actions) {
		t.Errorf("transportLayout(2) = pad %d widths %v gap %d, want gap 0", pad, widths, gap)
	}
	sum := 0
	for _, w := range widths {
		sum += w
	}
	if sum != 2 {
		t.Errorf("narrow transport widths sum %d, want 2", sum)
	}
}

func TestCov_headerRowReconnectAndNarrow(t *testing.T) {
	st := protocol.NewState()
	st.StartConnection()
	st.StartConnection() // attempts -> 2, still disconnected
	m, _, _ := modelWith(st)
	// the attempt counter is this module's own behaviour, not an environment
	// capability: failing to establish the precondition IS the regression, so it
	// must fail rather than skip (a skip would report a green run).
	if m.st.Snap().Attempts <= 1 {
		t.Fatalf("setup: two StartConnection calls should bump attempts, got %d", m.st.Snap().Attempts)
	}
	if got := stripANSI(m.headerRow(m.st.Snap(), time.Now(), 80, false)); !strings.Contains(got, "● reconnecting (2)…") {
		t.Errorf("disconnected header should read reconnecting: %q", got)
	}
	// the first attempt reads plain "connecting…"
	first := protocol.NewState()
	first.StartConnection()
	if got := stripANSI(m.headerRow(first.Snap(), time.Now(), 80, false)); !strings.Contains(got, "● connecting…") {
		t.Errorf("first-attempt header = %q", got)
	}
	// a tiny width drives the device-name budget below its floor (nameMax clamp)
	_ = m.headerRow(m.st.Snap(), time.Now(), 12, false)
}

// The compact header names the source beside the strip; brand-tinted when it
// fits, a dim clip when only part of it does, and nothing below 8 columns of
// room. The full player keeps it off the header (it has a source line).
func TestCov_headerRowSource(t *testing.T) {
	m, st, _ := makeModel(t)
	now := time.Now()
	if got := stripANSI(m.headerRow(st.Snap(), now, 80, false)); !strings.HasSuffix(got, "Spotify  1 player  2 equalizer  3 diagnostics") {
		t.Errorf("compact header = %q", got)
	}
	if got := stripANSI(m.headerRow(st.Snap(), now, 114, true)); strings.Contains(got, "Spotify") || !strings.HasSuffix(got, "Vol  ") {
		t.Errorf("full header = %q, want Vol over the rail and no source", got)
	}
	// a long unknown service clipped into what the strip leaves
	st.ApplyVendor("a-very-long-vendor-word")
	got := stripANSI(m.headerRow(st.Snap(), now, 80, false))
	if !strings.Contains(got, "a-very-long-ven"+GL["ell"]+"  1 player") || DispW(got) != 80 {
		t.Errorf("clipped source header = %q (%d wide)", got, DispW(got))
	}
	// under 8 columns of room the source gives way to the strip
	if got := stripANSI(m.headerRow(st.Snap(), now, 64, false)); strings.Contains(got, "a-very") || DispW(got) != 64 {
		t.Errorf("crowded header = %q (%d wide)", got, DispW(got))
	}
	// muted: the rail label turns MUTED from the top
	st.ApplyMute(true)
	if got := stripANSI(m.headerRow(st.Snap(), now, 114, true)); !strings.Contains(got, "MUTED") {
		t.Errorf("muted full header = %q", got)
	}
}

// eqSpecIndex is the tunnel.Specs index of a wire code.
func eqSpecIndex(code string) int {
	return slices.IndexFunc(tunnel.Specs, func(sp tunnel.Spec) bool { return sp.Code == code })
}
