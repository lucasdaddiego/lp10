package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// 1 2 3 pick a view outright; e and i open theirs and close it again; ? is
// the help page, outside the numbered cycle.
func TestViewKeys(t *testing.T) {
	m, _, collect := makeModel(t)
	for _, step := range []struct {
		key  keyEvent
		want view
	}{
		{kr('2'), viewEQ},
		{kr('3'), viewDiag},
		{kr('1'), viewPlayer},
		{kr('3'), viewDiag},
		{kr('3'), viewDiag}, // a number is not a toggle
		{kr('e'), viewEQ},
		{kr('e'), viewPlayer},
		{kr('E'), viewEQ},
		{kr('i'), viewDiag}, // i from another view switches to it
		{kr('I'), viewPlayer},
		{kr('?'), viewHelp},
		{kr('2'), viewEQ},
		{kr('?'), viewHelp},
		{kr('?'), viewPlayer},
	} {
		m.key(step.key)
		if m.view != step.want {
			t.Fatalf("after %q: view %s, want %s", step.key.r, viewNames[m.view], viewNames[step.want])
		}
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("view keys sent %v", wire(got))
	}
}

// tab steps the three numbered views in order and wraps; from the help page
// it steps into the cycle rather than staying put.
func TestTabCyclesTheNumberedViews(t *testing.T) {
	m, _, _ := makeModel(t)
	for i, want := range []view{viewEQ, viewDiag, viewPlayer, viewEQ, viewDiag, viewPlayer} {
		m.key(ke(kTab))
		if m.view != want {
			t.Fatalf("tab %d: view %s, want %s", i+1, viewNames[m.view], viewNames[want])
		}
	}
	m.key(kr('?'))
	m.key(ke(kTab))
	if m.view == viewHelp {
		t.Error("tab from the help page should step into the numbered views")
	}
}

// The letters of the removed views and toggles — c (services), l (logs),
// d (night), b (bedtime), t (remaining/total), 4 and 5 (the old view numbers)
// — are inert now: no command, no view change, no notice, no timer.
func TestRemovedLettersAreInert(t *testing.T) {
	for _, v := range []view{viewPlayer, viewEQ, viewDiag, viewHelp} {
		m, st, collect := makeModel(t)
		m.setView(v)
		before := st.Snap()
		for _, r := range "cldbtCLDBT45" {
			if m.key(kr(r)) {
				t.Fatalf("%c quit from %s", r, viewNames[v])
			}
		}
		if got := collect(); len(got) != 0 {
			t.Errorf("%s: removed letters sent %v", viewNames[v], wire(got))
		}
		if m.view != v {
			t.Errorf("%s: a removed letter moved to %s", viewNames[v], viewNames[m.view])
		}
		if m.notice != "" || !m.sleepAt.IsZero() {
			t.Errorf("%s: notice %q, sleep %v", viewNames[v], m.notice, m.sleepAt)
		}
		if after := st.Snap(); after.Playing != before.Playing || after.Muted != before.Muted || after.Vol != before.Vol {
			t.Errorf("%s: the player changed: %+v -> %+v", viewNames[v], before, after)
		}
	}
}

// Off the player the arrows belong to the view: they scroll the diagnostics
// and the help page and never touch the volume or the transport focus.
func TestArrowsBelongToTheView(t *testing.T) {
	m, st, collect := makeModel(t)
	m.rows, m.cols = 24, 80
	for _, v := range []view{viewDiag, viewHelp} {
		m.setView(v)
		m.key(ke(kDown))
		m.key(ke(kRight))
		m.key(ke(kEnter))
		if got := collect(); len(got) != 0 {
			t.Errorf("%s: arrows sent %v", viewNames[v], wire(got))
		}
		if m.diagScroll == 0 || m.focus != 1 || st.Snap().Vol != 44 {
			t.Errorf("%s: scroll %d focus %d vol %d", viewNames[v], m.diagScroll, m.focus, st.Snap().Vol)
		}
		m.key(ke(kUp))
		m.key(ke(kLeft))
		if m.diagScroll != 0 {
			t.Errorf("%s: ↑ ← should scroll back to the top, at %d", viewNames[v], m.diagScroll)
		}
	}
}

// The sleep notice reads the timer it just armed.
func TestSleepNotice(t *testing.T) {
	m, _, _ := makeModel(t)
	if got := m.sleepNotice(); got != "sleep timer off" {
		t.Errorf("off: %q", got)
	}
	m.sleepAt = time.Now().Add(30 * time.Minute)
	if got := m.sleepNotice(); got != "sleep timer set · "+GL["sleep"]+" 30m" {
		t.Errorf("armed: %q", got)
	}
}

// A bracketed paste is dispatched only when it is a few hotkeys and nothing
// else: a pasted Spotify link used to run prev, two sleep-timer steps, the
// equalizer, a skipped track and a quit.
func TestPasteIsOnlyAFewHotkeys(t *testing.T) {
	m, _, collect := makeModel(t)
	for _, s := range []string{"https://open.spotify.com/track/x", "nnnn", "nx", "n\n", "", "ñ"} {
		m.notice = ""
		if _, cmd := m.Update(tea.PasteMsg{Content: s}); cmd != nil {
			t.Errorf("paste %q returned a command", s)
		}
		if got := collect(); len(got) != 0 || m.view != viewPlayer {
			t.Errorf("paste %q sent %v, view %v; want nothing", s, wire(got), m.view)
		}
		if !strings.Contains(m.notice, "paste ignored") {
			t.Errorf("paste %q: notice %q, want it ignored", s, m.notice)
		}
	}
	// up to three hotkeys still work as typed: next, volume up, the equalizer
	m.Update(tea.PasteMsg{Content: "n+e"})
	if got := wire(collect()); !slices.Equal(got, []string{"NXT", "VOL:46"}) || m.view != viewEQ {
		t.Errorf("paste n+e sent %v, view %v; want [NXT VOL:46] and the equalizer", got, m.view)
	}
}

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
