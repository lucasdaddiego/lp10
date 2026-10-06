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
