package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// The painted blocks (motif, sonar) and the meters must measure exactly w
// columns to lipgloss — the property JoinHorizontal/boxArt rely on to keep the
// three player columns aligned. The painter writes raw 24-bit SGR (lipgloss v2
// renders profile-independently; the program's renderer downsamples), so one
// sweep pins it.
func TestPaintedBlockWidths(t *testing.T) {
	th := newTheme()
	for _, dim := range [][2]int{{30, 16}, {8, 6}, {1, 1}, {41, 7}} {
		w, h := dim[0], dim[1]
		for _, blk := range [][]string{th.motifBlock(w, h, 3), th.sonar(w, h, 3)} {
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
