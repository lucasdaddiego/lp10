package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// A clipped styled row keeps its per-segment colours — it used to be stripped
// and re-rendered uniformly dim, so the rows "lost their colours" the moment a
// larger font cost the column a couple of cells.
func TestClipStyledKeepsColours(t *testing.T) {
	sty := newTheme()
	row := sty.sAcc.Render("●") + " " + sty.sTxt.Render("Spotify and more text")
	got := clipStyled(row, 12)
	if w := lipgloss.Width(got); w > 12 {
		t.Errorf("clipped width = %d, want ≤ 12", w)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("clipped row lost its styling: %q", got)
	}
	if !strings.HasSuffix(stripANSI(got), GL["ell"]) {
		t.Errorf("clipped row should end with the ellipsis, got %q", stripANSI(got))
	}
	if !strings.HasPrefix(got, sty.pens().acc.render("●")) {
		t.Errorf("the first segment should keep its accent: %q", got)
	}
}

// A section rule narrower than its own title must not produce a negative
// repeat; at any width that fits the title it fills the row exactly.
func TestSectionHeadNarrow(t *testing.T) {
	m, _, _ := makeModel(t)
	if got := clean(m.sectionHead("a very long section title", 4)); got != "── a very long section title " {
		t.Errorf("narrow section head = %q", got)
	}
	for _, W := range []int{20, 52, 114} {
		if got := DispW(clean(m.sectionHead("equalizer", W))); got != W {
			t.Errorf("section head at W=%d is %d wide", W, got)
		}
	}
	// the diagnostics' section rule is the same idea, with the title lit
	sec := m.sectionRows(diagSection{title: "connection", rows: []kv{{"host", "x"}}}, 40)
	assertWithin(t, "sectionRows", sec, 40)
	if got := clean(sec[0]); got != "─ connection "+strings.Repeat("─", 40-3-len("connection")) {
		t.Errorf("section rule = %q", got)
	}
	if got := clean(m.sectionRows(diagSection{title: "connection"}, 5)[0]); got != "─ connection " {
		t.Errorf("narrow section rule = %q", got)
	}
}
