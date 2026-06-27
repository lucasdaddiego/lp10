package tui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// A coloured cover sets a per-album tint, a grey cover clears it (keep the
// default teal), and an unchanged cover is served from the cache (no recompute).
func TestRefreshAmbient(t *testing.T) {
	m := artModel(t)
	m.cfg.ArtMode = "halfblock"
	m.sty.trueColor = true // so artChoice() resolves to a real cover, not the motif

	// the art worker precomputes the hue (Dominant/DominantOK); refreshAmbient now
	// consumes it rather than scanning pixels on the render path
	red := protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "http://x/red",
		Art: fillImg(40, 40, color.RGBA{210, 30, 30, 255}), Dominant: color.RGBA{210, 30, 30, 255}, DominantOK: true}
	m.refreshAmbient(red)
	if m.amb == nil {
		t.Fatal("a coloured cover should set an ambient tint")
	}
	prev := m.amb
	m.refreshAmbient(red) // same cover: cached, not rebuilt
	if m.amb != prev {
		t.Error("an unchanged cover recomputed the tint")
	}

	grey := protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "http://x/grey",
		Art: fillImg(40, 40, color.RGBA{128, 128, 128, 255})}
	m.refreshAmbient(grey)
	if m.amb != nil {
		t.Error("a greyscale cover should clear the tint (keep the default accent)")
	}

	m.amb, m.ambKey = m.sty.tint(color.RGBA{200, 30, 30, 255}), "stale"
	m.refreshAmbient(protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}}) // no art
	if m.amb != nil || m.ambKey != "" {
		t.Error("no cover should clear the tint")
	}
}

// The ambient tint recolours the seek-bar fill to the cover's hue (asserted at
// the style level, so it's independent of the test terminal's colour profile),
// without changing the meter's width.
func TestAmbientTintRecolorsMeter(t *testing.T) {
	m := artModel(t)
	at := m.sty.tint(color.RGBA{210, 30, 30, 255}) // red cover
	if len(at.fill) != len(m.sty.fill) {
		t.Fatalf("tint fill has %d steps, want %d", len(at.fill), len(m.sty.fill))
	}
	differs := false
	for i := range at.fill {
		if at.fill[i].GetForeground() != m.sty.fill[i].GetForeground() {
			differs = true
		}
	}
	if !differs {
		t.Error("a red tint should differ from the default teal fill")
	}
	if a, b := m.sty.lineMeterPen(0.5, 20, at.fill, at.head), m.sty.lineMeter(0.5, 20); lipgloss.Width(a) != lipgloss.Width(b) {
		t.Errorf("tinted meter width %d != default %d", lipgloss.Width(a), lipgloss.Width(b))
	}
}

// The muted volume rail is impossible to miss: a SOLID red column and a bold
// "MUTED" badge, distinct from a live rail showing a percentage.
func TestVolRailMuted(t *testing.T) {
	m := artModel(t)
	muted := strings.Join(m.volRail(protocol.Snapshot{Muted: true, Vol: 0}, 5), "\n")
	if !strings.Contains(muted, "MUTED") {
		t.Error("a muted rail should show the MUTED badge")
	}
	if !strings.Contains(muted, "█") {
		t.Error("a muted rail should show the solid red column")
	}
	live := strings.Join(m.volRail(protocol.Snapshot{Vol: 60}, 5), "\n")
	if strings.Contains(live, "MUTED") {
		t.Error("a live rail should not read muted")
	}
	if !strings.Contains(live, "60%") {
		t.Error("a live rail should show the percentage")
	}
}

// The warm (boost) and cool (cut) EQ ramps — the slider-knob colours that signal
// a tone band's sign (eqSliderRow) — are distinct, asserted at the style level so
// it's independent of the test terminal's colour profile.
func TestToneRampsDistinct(t *testing.T) {
	m := artModel(t)
	if m.sty.warm[2].GetForeground() == m.sty.cool[2].GetForeground() {
		t.Error("the warm (boost) and cool (cut) ramps should be different colours")
	}
}

// On a wide column the transport buttons form a centred cluster (padded both
// sides) rather than stretching edge to edge — but still fill the column width.
func TestTransportClusterCentred(t *testing.T) {
	m := artModel(t)
	const w = 60
	row := m.transportSegments(protocol.Snapshot{Playing: 0}, time.Now(), w)
	if lipgloss.Width(row) != w {
		t.Errorf("transport row width %d, want %d", lipgloss.Width(row), w)
	}
	if !strings.HasPrefix(row, " ") {
		t.Error("a wide transport cluster should be padded (centred), not edge-to-edge")
	}
}

// friendlyError condenses common raw ssh/network errors to short human lines.
func TestFriendlyError(t *testing.T) {
	cases := map[string]string{
		"ssh: Could not resolve hostname lp10.local: nodename nor servname provided, or not known": "can't find the device — are you on the home network?",
		"connect to host lp10.local port 22: Connection refused":                                   "the device refused the connection",
		"ssh: connect to host x port 22: Operation timed out":                                      "connection timed out — the device may be off or away",
		"Permission denied (publickey).":                                                           "ssh authentication failed",
	}
	for in, want := range cases {
		if got := friendlyError(in); got != want {
			t.Errorf("friendlyError(%q) = %q, want %q", in, got, want)
		}
	}
}

// While disconnected, a connection error shows a calm friendly reason in the
// idle area and never the raw ssh text as a red bottom line.
func TestDisconnectedErrorIsFriendly(t *testing.T) {
	st := protocol.NewState()
	st.Note("ssh: Could not resolve hostname lp10.local: nodename nor servname provided, or not known")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 32, 100
	view := clean(m.View())
	if !strings.Contains(view, "can't find the device") {
		t.Error("disconnected idle should show the friendly reason")
	}
	if strings.Contains(view, "Could not resolve hostname") {
		t.Error("the raw ssh error must not appear while reconnecting")
	}
}

// A fatal error still shows the bottom line, prettified.
func TestFatalErrorStillShown(t *testing.T) {
	st := protocol.NewState()
	st.SetFatal("Permission denied (publickey).")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 32, 100
	view := clean(m.View())
	if !strings.Contains(view, "ssh authentication failed") {
		t.Error("a fatal error should show a friendly bottom line")
	}
}

// The diagnostics overlay shows the friendly reason, not the raw ssh dump — the
// overlay already states "disconnected · tunnel down · N attempts".
func TestDiagErrorIsFriendly(t *testing.T) {
	st := protocol.NewState()
	st.Note("ssh: Could not resolve hostname lp10.local: nodename nor servname provided, or not known")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 32, 100
	m.diag = true
	view := clean(m.View())
	if strings.Contains(view, "Could not resolve hostname") {
		t.Error("the diag overlay must not show the raw ssh error")
	}
	if !strings.Contains(view, "can't find the device") {
		t.Error("the diag overlay should show the friendly reason")
	}
}
