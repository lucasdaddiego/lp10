package tui

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The header's view strip names the three views with the one on show lit,
// shortens the names, then falls back to bare numerals when even those do not
// fit, and disappears below that.
func TestViewStripAdaptsToWidth(t *testing.T) {
	m, _, _ := makeModel(t)
	m.view = viewDiag
	cases := []struct {
		room int
		want string
	}{
		{80, "1 player  2 equalizer  3 diagnostics"},
		{36, "1 player  2 equalizer  3 diagnostics"},
		{35, "1 play  2 eq  3 diag"},
		{20, "1 play  2 eq  3 diag"},
		{19, "1  2  3"},
		{7, "1  2  3"},
		{6, ""},
	}
	for _, c := range cases {
		s, w := m.viewStrip(c.room)
		if plain := stripANSI(s); plain != c.want || w != DispW(plain) {
			t.Errorf("viewStrip(%d) = %q (%d), want %q", c.room, plain, w, c.want)
		}
	}
	// the view on show is lit; the help page lights none (it is behind ?)
	ps := m.sty.pens()
	full, _ := m.viewStrip(80)
	if !strings.Contains(full, ps.accB.render("3 diagnostics")) || strings.Contains(full, ps.accB.render("1 player")) {
		t.Errorf("only the diagnostics should be lit: %q", full)
	}
	nums, _ := m.viewStrip(10)
	if !strings.Contains(nums, ps.accB.render("3")) {
		t.Errorf("the numeral strip should light 3: %q", nums)
	}
	m.view = viewHelp
	if s, _ := m.viewStrip(80); strings.Contains(s, ps.accB.render("1 player")) || strings.Contains(s, ps.accB.render("3 diagnostics")) {
		t.Errorf("help lights no view: %q", s)
	}
	// every view's header carries the strip, and the frame keeps its width
	for _, v := range []view{viewPlayer, viewEQ, viewDiag, viewHelp} {
		m.view = v
		m.rows, m.cols = 40, 120
		lines := strings.Split(clean(render(t, m)), "\n")
		if !strings.Contains(lines[1], "1 player  2 equalizer  3 diagnostics") {
			t.Errorf("view %s header lacks the strip: %q", viewNames[v], lines[1])
		}
	}
}

// The help page lists every view's keys and leaves by esc, q or ?. It names no
// removed view or key.
func TestHelpViewListsKeys(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 44, 120
	m.key(kr('?'))
	out := clean(render(t, m))
	for _, want := range []string{
		"── views", "── player", "── equalizer", "── diagnostics", "── everywhere",
		"1 · 2 · 3", "player · equalizer · diagnostics", "play / pause", "next · previous track",
		"mute (in the device: the level stays where it is)", "sleep timer", "ask the vendor's manifest about updates",
		"esc · q · ? back to the player",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help page missing %q", want)
		}
	}
	for _, gone := range []string{"services", "logs", "night", "bedtime", "remaining", "seek", "cover"} {
		if strings.Contains(strings.ToLower(out), gone) {
			t.Errorf("help page names the removed %q:\n%s", gone, out)
		}
	}
	// every key line fits the narrowest frame: clipped, never wider
	assertWithin(t, "help at the narrowest", m.renderHelp(MiniCols-6), MiniCols-6)
	// a playback key does not leave the help page; q and esc return to the player
	if m.key(kr(' ')); m.view != viewHelp {
		t.Error("space should not leave the help page")
	}
	if m.key(kr('q')) || m.view != viewPlayer {
		t.Error("q should return to the player without quitting")
	}
	m.key(kr('?'))
	if m.key(ke(kEsc)); m.view != viewPlayer {
		t.Error("esc should leave the help page")
	}
}

// The playback keys reach the player from every view: a track can be paused
// from the diagnostics without leaving them.
func TestPlaybackKeysWorkAcrossViews(t *testing.T) {
	m, _, collect := makeModel(t)
	for _, v := range []view{viewEQ, viewDiag, viewHelp} {
		m.setView(v)
		for _, r := range " np+-m" {
			m.key(kr(r))
		}
		if got := wire(collect()); !slices.Equal(got[:3], []string{"POP", "NXT", "PRE"}) || len(got) != 6 {
			t.Errorf("playback keys in %s sent %v", viewNames[v], got)
		}
		if m.view != v {
			t.Errorf("a playback key left %s", viewNames[v])
		}
		m.key(kr('m')) // unmute again
		collect()
	}
	if lbl, _ := m.sleepLabel(time.Now()); lbl != "" {
		t.Errorf("no s was pressed, yet the sleep timer reads %q", lbl)
	}
}

// The strip has a short form between the full names and the numerals, and the
// footer rotates a second page of rarer keys for four seconds in sixteen.
func TestStripShortFormAndFooterRotation(t *testing.T) {
	m, _, _ := makeModel(t)
	if s, w := m.viewStrip(30); stripANSI(s) != "1 play  2 eq  3 diag" || w != 20 {
		t.Errorf("short strip = %q (%d)", stripANSI(s), w)
	}
	m.scroll = 0
	first := stripANSI(m.footerRow(120))
	m.scroll = 130
	second := stripANSI(m.footerRow(120))
	if !strings.Contains(first, "space play/pause") || !strings.Contains(second, "S cancel sleep") || first == second {
		t.Errorf("footer pages:\n%q\n%q", first, second)
	}
	m.scroll = 160
	if got := stripANSI(m.footerRow(120)); got != first {
		t.Errorf("footer should be back on the first page: %q", got)
	}
}
