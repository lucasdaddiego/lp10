package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

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

// A tall cell aspect makes the square-in-pixels cover short (its height floors
// at 6), shorter than the now-playing block beside it; the block used to take
// the cover's height and trim the transport row off its bottom. It now takes
// the taller of the two.
func TestShortCoverKeepsTransportRow(t *testing.T) {
	m, _, _ := makeModel(t)
	m.cellW, m.cellH = 4, 16 // cells four times taller than wide
	m.rows, m.cols = FullRows, FullCols
	out := clean(render(t, m))
	for _, want := range []string{GL["rew"], GL["ff"], "Playing", "● Spotify", "De Música Ligera"} {
		if !strings.Contains(out, want) {
			t.Errorf("full player lacks %q with a short cover:\n%s", want, out)
		}
	}
}

// Emoji in a title (a presentation selector, a ZWJ family) measure two cells
// each on the terminal; the frame, the compact layout and the mini line all
// stay inside the window while the marquee windows through them.
func TestEmojiTitleKeepsEveryLayoutInsideTheWindow(t *testing.T) {
	for _, sz := range [][2]int{{25, 70}, {20, 58}, {40, 120}, {8, 50}} {
		m, st, _ := makeModel(t)
		st.ApplyTrackField(protocol.FieldTitle, strings.Repeat("❤️", 18)+" 1️⃣")
		st.ApplyTrackField(protocol.FieldArtist, "👨‍👩‍👧‍👦 "+strings.Repeat("❤️", 30))
		st.ApplyTrackField(protocol.FieldAlbum, "漢字 ❤️ album")
		m.rows, m.cols = sz[0], sz[1]
		for range 40 { // the marquee windows through the title
			m.scroll++
			render(t, m)
		}
	}
}

// At 9–10 rows the compact frame cannot hold everything: the blank separators
// yield before the player's own status and transport rows. From 11 rows up
// there is room for all of it, and all of it shows.
func TestCompactShortFrameKeepsTransport(t *testing.T) {
	for _, rows := range []int{9, 10, 11, 12, 13} {
		m, _, _ := makeModel(t)
		m.rows, m.cols = rows, 64
		out := clean(render(t, m))
		if !strings.Contains(out, GL["rew"]) || !strings.Contains(out, GL["ff"]) {
			t.Errorf("%d rows: transport row missing:\n%s", rows, out)
		}
		if !strings.Contains(out, "Playing") {
			t.Errorf("%d rows: status row missing:\n%s", rows, out)
		}
	}
	m, _, _ := makeModel(t)
	m.rows, m.cols = 20, 64
	if out := clean(render(t, m)); !strings.Contains(out, GL["rew"]) || !strings.Contains(out, "Playing") || !strings.Contains(out, "vol") {
		t.Errorf("20 rows: the status, transport and volume must all show:\n%s", out)
	}
}
