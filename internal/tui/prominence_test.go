package tui

import (
	"strings"
	"testing"
	"time"

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

	live := clean(m.viewContent())
	if !strings.Contains(live, "Vol") {
		t.Error("live full header should label the rail \"Vol\"")
	}

	m.do("mute")
	muted := clean(m.viewContent())
	if !strings.Contains(muted, "MUTED") {
		t.Error("muted full layout should flag MUTED (header + rail badge)")
	}
	// the solid red column glyph appears in the muted rail
	if !strings.Contains(muted, "█") {
		t.Error("muted rail should draw a solid column")
	}
}
