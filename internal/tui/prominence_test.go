package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// toggleVerb is the icon-free action label: "pause" while playing (Playing==0),
// "play" while paused or idle — so it never duels with the seek-row STATE glyph.
func TestToggleVerb(t *testing.T) {
	if got := toggleVerb(protocol.Snapshot{Playing: 0}); got != "pause" {
		t.Errorf("playing -> %q, want pause", got)
	}
	if got := toggleVerb(protocol.Snapshot{Playing: 2}); got != "play" {
		t.Errorf("paused -> %q, want play", got)
	}
	// the label carries no play/pause glyph (that would re-create the duelling icons)
	for _, s := range []protocol.Snapshot{{Playing: 0}, {Playing: 2}} {
		if strings.ContainsAny(toggleVerb(s), "▶⏸") {
			t.Errorf("toggleVerb(%v) must be icon-free, got %q", s, toggleVerb(s))
		}
	}
}

// The seek row shows a colour-coded STATE label ("Playing" / "Paused"), fills the
// column to exactly W, and the transport toggle is an icon-free verb — so the
// state indicator and the action button never show contradictory icons.
func TestSeekRowStateLabelAndWidth(t *testing.T) {
	m, _, _ := makeModel(t) // fixture is playing (Playing == 0)
	m.sty = newTheme()

	for _, w := range []int{40, 60, 96, 120} {
		if got := DispW(stripANSI(m.seekRow(m.st.Snap(), w))); got != w {
			t.Errorf("seekRow width at W=%d: %d, want %d", w, got, w)
		}
	}

	playing := stripANSI(m.seekRow(m.st.Snap(), 96))
	if !strings.Contains(playing, "Playing") {
		t.Errorf("playing seek row should read Playing: %q", playing)
	}
	trans := stripANSI(m.transportSegments(m.st.Snap(), time.Time{}, 60))
	if !strings.Contains(trans, "pause") || strings.Contains(trans, "⏸") {
		t.Errorf("playing transport should read a glyph-free \"pause\": %q", trans)
	}

	m.st.ToggleOptimistic() // -> paused
	paused := stripANSI(m.seekRow(m.st.Snap(), 96))
	if !strings.Contains(paused, "Paused") {
		t.Errorf("paused seek row should read Paused: %q", paused)
	}
	transP := stripANSI(m.transportSegments(m.st.Snap(), time.Time{}, 60))
	if !strings.Contains(transP, "play") || strings.Contains(transP, "⏸") {
		t.Errorf("paused transport should read a glyph-free \"play\": %q", transP)
	}
}

// In the full layout the header flags mute prominently: the "Vol" label becomes a
// red "MUTED" over the rail, and the rail itself fills with a solid column + badge.
func TestMutedHeaderAndRail(t *testing.T) {
	m, st, _ := makeModel(t)
	st.SetVol(50)
	m.rows, m.cols = 40, 120

	live := clean(m.View())
	if !strings.Contains(live, "Vol") {
		t.Error("live full header should label the rail \"Vol\"")
	}

	m.do("mute")
	muted := clean(m.View())
	if !strings.Contains(muted, "MUTED") {
		t.Error("muted full layout should flag MUTED (header + rail badge)")
	}
	// the solid red column glyph appears in the muted rail
	if !strings.Contains(muted, "█") {
		t.Error("muted rail should draw a solid column")
	}
}

// The compact EQ summary runs in display order, shows each band's value, stays
// within W, and — when the EQ pane has focus — visibly marks the selected band
// (asserted under a forced colour profile, since the default test profile strips
// all styling). This is the "small screen shows what's selected" fix.
func TestEQSummaryOrderWidthAndFocus(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	st.PreloadEQ(map[string]int{"EQS": 0, "TRE": 0, "MID": 0, "BAS": 3, "VBS": 0, "VBI": 0, "MXV": 100})

	// display order EQ · Treble · Mid · Bass · Sub · Lvl · Max Vol, Bass shows +3
	plain := stripANSI(m.eqSummary(80))
	for _, want := range []string{"EQ off", "B+3", "Max Vol 100"} {
		if !strings.Contains(plain, want) {
			t.Errorf("eqSummary missing %q: %q", want, plain)
		}
	}
	if strings.Index(plain, "EQ off") > strings.Index(plain, "Max Vol") {
		t.Errorf("summary should run EQ…Max Vol (display order): %q", plain)
	}

	// width-safe: never exceeds the column, even when narrow
	for _, w := range []int{80, 56, 40, 24} {
		if got := DispW(stripANSI(m.eqSummary(w))); got > w {
			t.Errorf("eqSummary(%d) width %d exceeds the column", w, got)
		}
	}

	// focus highlight: force a colour profile so styling actually emits SGR codes
	// (the default test profile is Ascii and strips everything), then prove the
	// focused render differs from the unfocused one — i.e. the selected band is
	// visibly marked. The plain text is identical, so any difference is the cue.
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(old)

	m.pane, m.eqFocus = paneNow, 3
	unfocused := m.eqSummary(80)
	m.pane = paneEQ // Bass focused (eqOrder index 3)
	focused := m.eqSummary(80)
	if focused == unfocused {
		t.Error("the focused band should be visibly marked when the EQ pane has focus")
	}
	if stripANSI(focused) != stripANSI(unfocused) {
		t.Error("focus should change only styling, not the summary text/width")
	}
}
