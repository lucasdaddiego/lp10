package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The painted blocks (motif, searching arcs) and the meters must measure
// exactly w columns to lipgloss — the property JoinHorizontal/boxArt rely on to
// keep the three player columns aligned. The painter writes raw 24-bit SGR
// (lipgloss v2 renders profile-independently; the program's renderer
// downsamples), so one sweep pins it.
func TestPaintedBlockWidths(t *testing.T) {
	th := newTheme()
	for _, dim := range [][2]int{{30, 16}, {8, 6}, {1, 1}, {41, 7}} {
		w, h := dim[0], dim[1]
		for _, blk := range [][]string{th.motifBlock(w, h, 3), th.searchBox(w, h, 3)} {
			if len(blk) != h {
				t.Fatalf("%dx%d: %d lines", w, h, len(blk))
			}
			for i, ln := range blk {
				if got := lipgloss.Width(ln); got != w {
					t.Errorf("%dx%d line %d: width %d, want %d", w, h, i, got, w)
				}
			}
		}
	}
	for _, cells := range []int{0, 1, 12, 60} {
		if got := lipgloss.Width(th.lineMeter(0.42, cells)); got != cells {
			t.Errorf("lineMeter(%d) width %d", cells, got)
		}
		if got := lipgloss.Width(th.gaugeBar(0.42, cells, th.sAcc)); got != cells {
			t.Errorf("gaugeBar(%d) width %d", cells, got)
		}
	}
	for _, ln := range th.vbar(0.5, 9) {
		if got := lipgloss.Width(ln); got != 1 {
			t.Errorf("vbar cell width %d, want 1", got)
		}
	}
}

// The searching figure's behaviour: the arc pairs light up outward one frame
// step at a time over the dim track (2·lit+1 bright cells, dot always lit) on
// a 4-step cycle; the label shows exactly when it fits; the figure degrades
// spaced → tight → bare dot as the box narrows; and the dot falls back to
// ASCII under a CJK locale.
func TestSearchBoxFigure(t *testing.T) {
	defer func(orig int) { localeAmb = orig }(localeAmb)
	localeAmb = 1
	th := newTheme()
	briR, briG, briB := hslRGB(168, 0.70, 0.75)
	bri := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", briR, briG, briB)

	// full box: spaced arcs + the label, pulse growing then restarting
	for frame, want := range []int{1, 3, 5, 7, 1} {
		blk := strings.Join(th.searchBox(30, 16, frame), "\n")
		if got := strings.Count(blk, bri); got != want {
			t.Errorf("frame %d: %d bright cells, want %d", frame, got, want)
		}
	}
	full := th.searchBox(30, 16, 1)
	txt := stripANSI(strings.Join(full, "\n"))
	if !strings.Contains(txt, "( ( ( ● ) ) )") {
		t.Errorf("full box should show the spaced arcs: %q", txt)
	}
	if !strings.Contains(txt, "searching for LP10"+GL["ell"]) {
		t.Errorf("full box should show the label: %q", txt)
	}
	if a, b := th.searchBox(30, 16, 2), th.searchBox(30, 16, 6); !slices.Equal(a, b) {
		t.Error("the pulse should cycle every 4 frames")
	}

	// narrow: tight arcs, no room for the label
	if txt := stripANSI(strings.Join(th.searchBox(8, 6, 0), "\n")); !strings.Contains(txt, "(((●)))") || strings.Contains(txt, "searching") {
		t.Errorf("8-wide box should be tight arcs, no label: %q", txt)
	}
	// too small for arcs / for the label row: the bare dot, centred
	if txt := stripANSI(strings.Join(th.searchBox(3, 1, 0), "\n")); txt != " ● " {
		t.Errorf("3x1 box should be the bare dot: %q", txt)
	}
	if blk := th.searchBox(30, 2, 0); !strings.Contains(stripANSI(blk[0]), "●") || stripANSI(blk[1]) != spaces(30) {
		t.Error("h=2 drops the label and keeps the figure")
	}

	// the block is vertically BALANCED at every height: the blank space above
	// the arcs equals the blank space below the label (the gap between them
	// absorbs the parity), so the figure never drifts off-centre
	for _, dim := range [][2]int{{30, 16}, {30, 17}, {41, 7}, {26, 10}, {19, 3}, {19, 4}} {
		blk := th.searchBox(dim[0], dim[1], 2)
		first, last := -1, -1
		for i, ln := range blk {
			if strings.TrimSpace(stripANSI(ln)) != "" {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first != len(blk)-1-last {
			t.Errorf("%dx%d: %d blank rows above vs %d below", dim[0], dim[1], first, len(blk)-1-last)
		}
	}

	// CJK locale: the ambiguous-width dot falls back to ASCII
	localeAmb = 2
	if txt := stripANSI(strings.Join(th.searchBox(30, 16, 0), "\n")); !strings.Contains(txt, "( ( ( * ) ) )") || strings.Contains(txt, "●") {
		t.Errorf("CJK locale should use the ASCII dot: %q", txt)
	}
}
