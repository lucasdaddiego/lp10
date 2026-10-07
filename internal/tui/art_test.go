// The art slot (art.go): the note box, the boxed art and the column states.

package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestCov_noteBox(t *testing.T) {
	m, _, _ := makeModel(t)
	// normal motif (room for the 5x3 note motif), every line w wide
	normal := m.noteBox(10, 6)
	if !strings.Contains(strings.Join(normal, "\n"), "●") {
		t.Errorf("noteBox normal should draw the note motif: %q", normal)
	}
	assertWithin(t, "noteBox", normal, 10)
	for i, ln := range normal {
		if visWidth(ln) != 10 {
			t.Errorf("noteBox line %d width %d, want 10", i, visWidth(ln))
		}
	}
	// too small for the motif -> a single centred ♪
	small := strings.Join(m.noteBox(3, 2), "\n")
	if !strings.Contains(small, GL["note"]) {
		t.Errorf("noteBox small should fall back to a single note: %q", small)
	}
	// width 0 -> blank lines, no panic
	if got := m.noteBox(0, 3); len(got) != 3 {
		t.Errorf("noteBox(0,3) len = %d, want 3", len(got))
	}
	// under a CJK locale the box falls back to the single note too
	defer func(orig int) { localeAmb = orig }(localeAmb)
	localeAmb = 2
	if got := strings.Join(m.noteBox(10, 6), "\n"); strings.Contains(got, "●") {
		t.Errorf("CJK noteBox should not draw the ambiguous-width motif: %q", got)
	}
}

// boxArt adds a one-cell frame: +2 lines, +2 columns, with the corner glyphs.
func TestCov_boxArt(t *testing.T) {
	m, _, _ := makeModel(t)
	framed := m.boxArt([]string{"abcd", "efgh"}, 4)
	if len(framed) != 4 {
		t.Fatalf("got %d lines, want 4 (2 content + top/bottom)", len(framed))
	}
	if !strings.Contains(framed[0], GL["tl"]) || !strings.Contains(framed[3], GL["bl"]) {
		t.Error("missing top/bottom frame corners")
	}
	for i, ln := range framed {
		if w := lipgloss.Width(ln); w != 6 {
			t.Errorf("framed line %d width %d, want 6 (4 + 2 border)", i, w)
		}
	}
}

// The art column has three faces: the searching arcs while (re)connecting, the
// plasma motif while there is something to show (a track, or playback without
// one), and the calm note motif when connected and idle.
func TestCov_artColumnStates(t *testing.T) {
	cases := []struct {
		name             string
		st               *protocol.State
		want             string
		motif, searching bool
	}{
		{"connecting", protocol.NewState(), "searching for LP10", false, true},
		{"playing a track", playingState(), "█", true, false},
		{"playing untitled", untitledState(), "█", true, false},
		{"idle", idleState(), "●", false, false},
	}
	for _, c := range cases {
		m, _, _ := modelWith(c.st)
		col := m.artColumn(c.st.Snap(), 24, 8)
		if len(col) != 8 {
			t.Errorf("%s: %d rows, want 8", c.name, len(col))
		}
		assertWithin(t, c.name, col, 24)
		if got := stripANSI(strings.Join(col, "\n")); !strings.Contains(got, c.want) {
			t.Errorf("%s: art column lacks %q:\n%s", c.name, c.want, got)
		}
		if m.motifLive != c.motif || m.searchLive != c.searching {
			t.Errorf("%s: motifLive=%v searchLive=%v, want %v %v", c.name, m.motifLive, m.searchLive, c.motif, c.searching)
		}
	}
}
