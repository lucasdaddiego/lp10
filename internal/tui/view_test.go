// The player rendering (view.go): the dashboard, its rows, the rail, the
// transport, the marquee and the footer.

package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The compact controls row flags the mute and offers unmute; a pressed button
// flashes even when it is not focused.
func TestControlsRowMutedAndFlash(t *testing.T) {
	m, _, _ := makeModel(t)
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

// Partial metadata (Bluetooth, AirPlay): an empty title reads "—" with the
// artist and album joined under it, an album alone is linked, a bare title
// leaves an empty, unlinked second line; the full player's single "—" line.
func TestMetaLinesPartialMetadata(t *testing.T) {
	m, _, _ := makeModel(t)
	// empty title -> "—", artist + album joined on the second line
	s1 := protocol.Snapshot{Track: &protocol.Track{Artist: "A", Album: "Al"}}
	l1 := stripANSI(strings.Join(m.metaLines(s1, 40), "\n"))
	if !strings.Contains(l1, "—") || !strings.Contains(l1, "A · Al") {
		t.Errorf("metaLines empty-title = %q", l1)
	}
	if out := m.fullMeta(protocol.Snapshot{Track: &protocol.Track{}}, 40); len(out) != 1 || clean(out[0]) != "—" {
		t.Errorf("fullMeta empty-title = %q", out)
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

// The compact dashboard carries the transport and nothing of the equalizer;
// a connected error paints the friendly reason in every layout and leaves
// after ErrorDisplayDuration.
func TestDashboardCompactAndErrorLine(t *testing.T) {
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

// A second attempt reads "reconnecting (2)…" in the header; a tiny width
// drives the device-name budget below its floor without a panic.
func TestHeaderRowReconnecting(t *testing.T) {
	st := protocol.NewState()
	st.StartConnection()
	st.StartConnection() // attempts -> 2, still disconnected
	m, _, _ := modelWith(st)
	if got := stripANSI(m.headerRow(m.st.Snap(), time.Now(), 80, false)); !strings.Contains(got, "● reconnecting (2)…") {
		t.Errorf("disconnected header should read reconnecting: %q", got)
	}
	_ = m.headerRow(m.st.Snap(), time.Now(), 12, false)
}

// The compact header names the source beside the strip; brand-tinted when it
// fits, a dim clip when only part of it does, and nothing below 8 columns of
// room. The full player keeps it off the header (it has a source line).
func TestHeaderRowSource(t *testing.T) {
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

// Every footer width gets the widest hint that fits, right-aligned and exactly
// W wide; only below the narrowest hint is it clipped.
func TestFooterHintLadderFitsW(t *testing.T) {
	m, _, _ := makeModel(t)
	for _, page := range []int{0, 130} {
		m.scroll = page
		ladder := playerHints
		if page > 0 {
			ladder = playerHintsRare
		}
		narrowest := DispW(ladder[len(ladder)-1])
		for W := 1; W <= 130; W++ {
			got := m.footerRow(W)
			if visWidth(got) != W {
				t.Fatalf("page %d W=%d: footer width %d", page, W, visWidth(got))
			}
			hint := strings.TrimSpace(stripANSI(got))
			if W >= narrowest {
				if strings.Contains(hint, GL["ell"]) {
					t.Errorf("page %d W=%d: a hint that fits was clipped: %q", page, W, hint)
				}
				// the widest that fits
				for _, h := range ladder {
					if DispW(h) <= W {
						if hint != h {
							t.Errorf("page %d W=%d: %q, want %q", page, W, hint, h)
						}
						break
					}
				}
			} else if !strings.HasSuffix(hint, GL["ell"]) && W > 1 {
				t.Errorf("page %d W=%d: a too-narrow footer should be clipped: %q", page, W, hint)
			}
		}
	}
	// the help and diagnostics footers name no removed views or keys
	for _, h := range append(append(append([]string{}, playerHints...), playerHintsRare...), diagFooters...) {
		for _, gone := range []string{"services", "logs", "night", "bedtime", "remaining"} {
			if strings.Contains(h, gone) {
				t.Errorf("footer hint %q names the removed %q", h, gone)
			}
		}
	}
}

// A line that measures wider than the frame is clipped to it; lipgloss would
// have grown the box to the widest line and pushed every row, borders
// included, past the terminal.
func TestFrameLinesClipsOverWideLine(t *testing.T) {
	m := &model{sty: newTheme()}
	m.rows, m.cols = 9, 60
	W := m.cols - 6
	lines := make([]string, m.rows-2)
	lines[0] = between("♪ LP10", 6, "12:00", 5, W)
	lines[3] = strings.Repeat("x", W+15)
	lines[4] = m.sty.sBri.Render(strings.Repeat("y", W+3))
	orig := lines[3]
	for i, ln := range strings.Split(m.frameLines(lines, W), "\n") {
		if w := lipgloss.Width(ln); w != m.cols {
			t.Errorf("line %d width %d, want %d: %q", i, w, m.cols, clean(ln))
		}
	}
	if lines[3] != orig {
		t.Error("frameLines must not mutate the caller's lines")
	}
}
