// The help page (help.go): scrolling and the keys it keeps.

package tui

import (
	"slices"
	"strings"
	"testing"
)

// The help page is taller than 80×24 and must scroll: at 80×24 (and at the
// 25-row full size) the lower groups were cut off and no key reached them. It
// scrolls like the diagnostics, the footer saying how much is off-screen, and
// opens at its top.
func TestHelpScrollsToEveryGroupAt80x24(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 24, 80
	body := func() string { // the frame below the header and the notice line
		return strings.Join(strings.Split(clean(render(t, m)), "\n")[3:], "\n")
	}
	m.key(kr('?'))
	first := body()
	if !strings.Contains(first, "more rows below") {
		t.Fatalf("a too-short help page should offer to scroll:\n%s", first)
	}
	seen := map[string]bool{}
	for range 40 {
		frame := clean(render(t, m))
		for _, g := range helpGroups {
			if strings.Contains(frame, "── "+g.title+" ─") {
				seen[g.title] = true
			}
		}
		m.key(ke(kDown))
	}
	for _, g := range helpGroups {
		if !seen[g.title] {
			t.Errorf("scrolling never reached the %q group", g.title)
		}
	}
	if bottom := body(); !strings.Contains(bottom, "rows above") || strings.Contains(bottom, "below") {
		t.Errorf("scrolling past the end should clamp at the bottom:\n%s", bottom)
	}
	m.key(ke(kUp))
	if up := body(); !strings.Contains(up, "above · 1 below") {
		t.Errorf("↑ from the bottom should scroll back one row:\n%s", up)
	}
	for range 10 {
		m.key(ke(kLeft)) // a page at a time, clamped at the top
	}
	if body() != first {
		t.Error("← past the top should come back to the first row")
	}
	m.key(ke(kRight))
	if m.diagScroll != m.diagPage() {
		t.Errorf("→ should page down by %d, scrolled %d", m.diagPage(), m.diagScroll)
	}
	// it opens at its top, whatever the diagnostics' scroll was
	m.key(kr('3'))
	for range 5 {
		m.key(ke(kDown))
	}
	m.key(kr('?'))
	if body() != first {
		t.Error("the help page should open at its top")
	}
	// too narrow for the scroll count and the way out together: the count stays
	m.cols = MiniCols
	if foot := frameRowWith(clean(render(t, m)), "more rows below"); foot == "" || strings.Contains(foot, "back to the player") {
		t.Errorf("narrow help footer = %q, want the scroll count alone", foot)
	}
	// tall enough: no scroll count, the way out alone
	m.rows = 60
	if foot := footerOf(clean(render(t, m))); foot != "esc · q · ? back to the player" {
		t.Errorf("tall help footer = %q", foot)
	}
}

// The help page says the playback keys work from every view that does not use
// the letter; on the help page itself they reach the device and the page stays.
func TestHelpPageTakesThePlaybackKeys(t *testing.T) {
	m, _, collect := makeModel(t)
	m.key(kr('?'))
	collect()
	m.key(kr(' '))
	m.key(kr('n'))
	if got := wire(collect()); !slices.Equal(got, []string{"POP", "NXT"}) {
		t.Errorf("space n on the help page sent %v, want [POP NXT]", got)
	}
	if m.view != viewHelp {
		t.Errorf("a playback key closed the help page (view %s)", viewNames[m.view])
	}
}
