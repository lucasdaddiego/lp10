package tui

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

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

// A wide cover capped to its width used to shorten the whole player block and
// trim the transport row off its bottom (a 300×120 radio logo at the 70×25
// minimum). The block now takes the metadata block's height.
func TestWideCoverKeepsTransportRow(t *testing.T) {
	m, st, _ := makeModel(t)
	m.rows, m.cols = FullRows, FullCols
	st.SetArt(st.Snap().CoverURL, image.NewRGBA(image.Rect(0, 0, 300, 120)), color.RGBA{}, false)
	view := m.viewContent()
	if !strings.Contains(clean(view), GL["rew"]) {
		t.Errorf("transport row missing with a wide cover:\n%s", clean(view))
	}
	lines := strings.Split(view, "\n")
	if len(lines) != m.rows {
		t.Fatalf("%d lines, want %d", len(lines), m.rows)
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != m.cols {
			t.Errorf("line %d width %d, want %d", i, w, m.cols)
		}
	}
}

// Emoji in a title (a presentation selector, a ZWJ family) measure two cells
// each on the terminal; the frame, the compact layout and the mini line all
// stay inside the window.
func TestEmojiTitleKeepsEveryLayoutInsideTheWindow(t *testing.T) {
	for _, sz := range [][2]int{{25, 70}, {20, 58}, {40, 120}, {8, 50}} {
		m, st, _ := makeModel(t)
		tr := *st.Snap().Track
		tr.TrackName = strings.Repeat("❤️", 18) + " 1️⃣"
		tr.Artist = "👨‍👩‍👧‍👦 " + strings.Repeat("❤️", 30)
		tr.Album = "漢字 ❤️ album"
		st.Preload(&tr, 0, 44)
		m.rows, m.cols = sz[0], sz[1]
		view := m.viewContent()
		for i, ln := range strings.Split(view, "\n") {
			if w := lipgloss.Width(ln); w > m.cols || (m.rows >= MiniRows && m.cols >= MiniCols && w != m.cols) {
				t.Errorf("%dx%d line %d width %d (cols %d): %q", sz[0], sz[1], i, w, m.cols, clean(ln))
			}
		}
		for tick := range 40 { // the marquee windows through the title
			m.scroll++
			for i, ln := range strings.Split(m.viewContent(), "\n") {
				if w := lipgloss.Width(ln); w > m.cols {
					t.Fatalf("%dx%d scroll %d line %d width %d: %q", sz[0], sz[1], tick, i, w, clean(ln))
				}
			}
		}
	}
}

// The latency rows keep their columns aligned when the target label is a whole
// IPv4 address (wider than the name column).
func TestLatencyRowClipsLongTargetNames(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	ps := protocol.PingStat{OK: true, Avg: 12, Jitter: 1, Peak: 20}
	gw := stripANSI(m.latencyRow("gw", ps))
	ip := stripANSI(m.latencyRow("192.168.0.1", ps))
	if DispW(gw) != DispW(ip) {
		t.Errorf("rows differ in width: %q (%d) vs %q (%d)", gw, DispW(gw), ip, DispW(ip))
	}
	if !strings.HasPrefix(ip, Clip("192.168.0.1", latNameW)) {
		t.Errorf("long name not clipped to its column: %q", ip)
	}
	_ = time.Now
}

// At 9–13 rows the compact frame cannot hold everything: the EQ summary and
// its divider, then the blank separators, yield before the player's own seek
// and transport rows. With room for all of it, all of it shows.
func TestCompactShortFrameKeepsTransportOverEQSummary(t *testing.T) {
	for _, rows := range []int{9, 10, 11, 12, 13} {
		m, _, _ := makeModel(t)
		m.rows, m.cols = rows, 64
		out := clean(m.viewContent())
		if !strings.Contains(out, GL["rew"]) || !strings.Contains(out, GL["ff"]) {
			t.Errorf("%d rows: transport row missing:\n%s", rows, out)
		}
		if strings.Contains(out, "equalizer") {
			t.Errorf("%d rows: the EQ summary stayed while the player was trimmed:\n%s", rows, out)
		}
		if n := len(strings.Split(m.viewContent(), "\n")); n != rows {
			t.Errorf("%d rows: rendered %d lines", rows, n)
		}
	}
	m, _, _ := makeModel(t)
	m.rows, m.cols = 20, 64
	if out := clean(m.viewContent()); !strings.Contains(out, "equalizer") || !strings.Contains(out, GL["rew"]) {
		t.Errorf("20 rows: both fit and both must show:\n%s", out)
	}
}
