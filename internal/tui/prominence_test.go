package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// toggleVerb is the icon-free action label: "pause" while playing, "play"
// while paused or idle — so it never duels with the status row's STATE glyph.
func TestToggleVerb(t *testing.T) {
	if got := toggleVerb(protocol.Snapshot{Playing: true}); got != "pause" {
		t.Errorf("playing -> %q, want pause", got)
	}
	if got := toggleVerb(protocol.Snapshot{}); got != "play" {
		t.Errorf("paused -> %q, want play", got)
	}
	// the label carries no play/pause glyph (that would re-create the duelling icons)
	for _, s := range []protocol.Snapshot{{Playing: true}, {}} {
		if strings.ContainsAny(toggleVerb(s), "▶⏸") {
			t.Errorf("toggleVerb(%v) must be icon-free, got %q", s.Playing, toggleVerb(s))
		}
	}
}

// The status row replaced the seek bar (the tunnel reports no position): a
// colour-coded STATE label — teal "Playing", amber "Paused" — a quiet marker
// while connecting or idle, and the mute beside the state. It is never wider
// than its column.
func TestStatusRow(t *testing.T) {
	m, _, _ := makeModel(t)
	track := &protocol.Track{TrackName: "x"}
	cases := []struct {
		name string
		s    protocol.Snapshot
		want string
	}{
		{"connecting", protocol.Snapshot{}, GL["pause"]},
		{"connecting, muted as last read", protocol.Snapshot{Muted: true}, GL["pause"]},
		{"playing a track", protocol.Snapshot{Connected: true, Playing: true, Track: track}, GL["play"] + " Playing"},
		{"playing untitled", protocol.Snapshot{Connected: true, Playing: true}, GL["play"] + " Playing"},
		{"paused", protocol.Snapshot{Connected: true, Track: track}, GL["pause"] + " Paused"},
		{"idle", protocol.Snapshot{Connected: true}, GL["pause"]},
		{"playing, muted", protocol.Snapshot{Connected: true, Playing: true, Muted: true}, GL["play"] + " Playing · muted"},
		{"paused, muted", protocol.Snapshot{Connected: true, Track: track, Muted: true}, GL["pause"] + " Paused · muted"},
		{"idle, muted", protocol.Snapshot{Connected: true, Muted: true}, GL["pause"] + " · muted"},
	}
	for _, c := range cases {
		row := m.statusRow(c.s, 60)
		if got := stripANSI(row); got != c.want {
			t.Errorf("%s: status row %q, want %q", c.name, got, c.want)
		}
		for W := 0; W <= 24; W++ {
			if got := visWidth(m.statusRow(c.s, W)); got > W {
				t.Errorf("%s: status row at W=%d is %d wide", c.name, W, got)
			}
		}
	}
	// the state colours: Playing in the accent, Paused in the warning amber
	ps := m.sty.pens()
	if got := m.statusRow(protocol.Snapshot{Connected: true, Playing: true}, 60); got != ps.accB.render(GL["play"]+" Playing") {
		t.Errorf("Playing should be bold accent: %q", got)
	}
	if got := m.statusRow(protocol.Snapshot{Connected: true, Track: track}, 60); got != ps.warnB.render(GL["pause"]+" Paused") {
		t.Errorf("Paused should be bold amber: %q", got)
	}
	// the transport says the opposite verb, glyph-free
	trans := stripANSI(m.transportSegments(protocol.Snapshot{Playing: true}, time.Time{}, 60))
	if !strings.Contains(trans, "pause") || strings.Contains(trans, "⏸") {
		t.Errorf("playing transport should read a glyph-free \"pause\": %q", trans)
	}
	transP := stripANSI(m.transportSegments(protocol.Snapshot{}, time.Time{}, 60))
	if !strings.Contains(transP, "play") || strings.Contains(transP, "⏸") {
		t.Errorf("paused transport should read a glyph-free \"play\": %q", transP)
	}
}

// In the full layout the header flags mute prominently: the "Vol" label becomes a
// red "MUTED" over the rail, and the rail itself fills with a solid column + badge.
func TestMutedHeaderAndRail(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 40, 120

	live := clean(render(t, m))
	if !strings.Contains(strings.Split(live, "\n")[1], "Vol") {
		t.Error("live full header should label the rail \"Vol\"")
	}

	m.do("mute")
	muted := clean(render(t, m))
	if !strings.Contains(strings.Split(muted, "\n")[1], "MUTED") {
		t.Error("muted full header should flag MUTED over the rail")
	}
	if strings.Count(muted, "MUTED") != 2 {
		t.Errorf("muted full layout should flag MUTED twice (header + rail badge):\n%s", muted)
	}
	// the solid red column glyph appears in the muted rail, the status row says so
	if !strings.Contains(muted, "█") || !strings.Contains(muted, "Playing · muted") {
		t.Errorf("muted rail/status missing:\n%s", muted)
	}
}
