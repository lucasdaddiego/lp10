package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The muted volume rail is impossible to miss: a SOLID red column and a bold
// "MUTED" badge, distinct from a live rail showing a percentage.
func TestVolRailMuted(t *testing.T) {
	m, _, _ := makeModel(t)
	muted := m.volRail(protocol.Snapshot{Muted: true, Vol: 44}, 5)
	if len(muted) != 6 {
		t.Fatalf("a 5-high rail is %d rows, want 6 (bar + value)", len(muted))
	}
	joined := strings.Join(muted, "\n")
	if !strings.Contains(joined, "MUTED") || !strings.Contains(joined, "█") {
		t.Error("a muted rail should show the MUTED badge over a solid column")
	}
	live := strings.Join(m.volRail(protocol.Snapshot{Vol: 60}, 5), "\n")
	if strings.Contains(live, "MUTED") {
		t.Error("a live rail should not read muted")
	}
	if !strings.Contains(stripANSI(live), "60%") {
		t.Error("a live rail should show the percentage")
	}
	for _, rail := range [][]string{muted, m.volRail(protocol.Snapshot{Vol: 60}, 5)} {
		for i, ln := range rail {
			if w := lipgloss.Width(ln); w != volColW {
				t.Errorf("rail row %d width %d, want %d", i, w, volColW)
			}
		}
	}
}

// The rail is cached by what it shows: the same volume, mute and height reuse
// the block; a change of any rebuilds it.
func TestVolRailCache(t *testing.T) {
	m, _, _ := makeModel(t)
	a := m.volRail(protocol.Snapshot{Vol: 60}, 5)
	if b := m.volRail(protocol.Snapshot{Vol: 60}, 5); &a[0] != &b[0] {
		t.Error("an unchanged rail should come from the cache")
	}
	for _, s := range []struct {
		snap protocol.Snapshot
		h    int
	}{{protocol.Snapshot{Vol: 61}, 5}, {protocol.Snapshot{Vol: 61, Muted: true}, 5}, {protocol.Snapshot{Vol: 61, Muted: true}, 6}} {
		prev := m.volBlk
		if got := m.volRail(s.snap, s.h); &got[0] == &prev[0] {
			t.Errorf("%+v h=%d reused the stale rail", s.snap, s.h)
		}
	}
}

// The warm (boost) and cool (cut) EQ knob colours — which signal a tone band's
// sign (eqSliderRow) — are distinct, asserted at the style level so it's
// independent of the test terminal's colour profile.
func TestToneKnobsDistinct(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th := newThemeFor(dark)
		if th.warmKnob.GetForeground() == th.coolKnob.GetForeground() {
			t.Errorf("dark=%v: the warm (boost) and cool (cut) knobs should be different colours", dark)
		}
	}
}

// On a wide column the transport buttons form a centred cluster (padded both
// sides) rather than stretching edge to edge — but still fill the column width.
func TestTransportClusterCentred(t *testing.T) {
	m, _, _ := makeModel(t)
	for _, w := range []int{60, 40, 10, 3} {
		row := m.transportSegments(protocol.Snapshot{Playing: true}, time.Now(), w)
		if lipgloss.Width(row) != w {
			t.Errorf("transport row width %d, want %d", lipgloss.Width(row), w)
		}
	}
	if row := m.transportSegments(protocol.Snapshot{}, time.Now(), 60); !strings.HasPrefix(row, " ") {
		t.Error("a wide transport cluster should be padded (centred), not edge-to-edge")
	}
	// the focused button lights up only on the player
	m.focus = 2
	onPlayer := m.transportSegments(protocol.Snapshot{}, time.Now(), 60)
	m.view = viewEQ
	if off := m.transportSegments(protocol.Snapshot{}, time.Now(), 60); off == onPlayer {
		t.Error("the transport focus should not light while another view has the keys")
	}
}

// While disconnected, a connection error shows a calm friendly reason in the
// idle area and never the raw dial error as a red bottom line.
func TestDisconnectedErrorIsFriendly(t *testing.T) {
	for _, size := range [][2]int{{32, 100}, {20, 64}} {
		st := protocol.NewState()
		st.StartConnection()
		st.Note("cannot reach :2018: dial tcp: lookup lp10.local: no such host")
		m, _, _ := modelWith(st)
		m.rows, m.cols = size[0], size[1]
		view := clean(render(t, m))
		if !strings.Contains(view, "can't find the device") {
			t.Errorf("%dx%d: disconnected idle should show the friendly reason:\n%s", size[1], size[0], view)
		}
		if strings.Contains(view, "lookup lp10.local") {
			t.Errorf("%dx%d: the raw dial error must not appear while reconnecting", size[1], size[0])
		}
		if strings.Contains(view, GL["warn"]) {
			t.Errorf("%dx%d: a reconnect reason is not a red error line:\n%s", size[1], size[0], view)
		}
	}
}

// The diagnostics show the friendly reason, not the raw dial error — the
// masthead already says "disconnected" and the tunnel row "down".
func TestDiagErrorIsFriendly(t *testing.T) {
	st := protocol.NewState()
	st.StartConnection()
	st.Note("cannot reach :2018: dial tcp: lookup lp10.local: no such host")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 32, 100
	m.view = viewDiag
	view := clean(render(t, m))
	if strings.Contains(view, "lookup lp10.local") {
		t.Error("the diagnostics must not show the raw dial error")
	}
	if !strings.Contains(view, "can't find the device") {
		t.Error("the diagnostics should show the friendly reason")
	}
}
