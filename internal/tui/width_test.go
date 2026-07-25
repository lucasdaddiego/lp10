package tui

import (
	"testing"

	"golang.org/x/text/width"
)

// charW short-circuits every rune below U+1100 to width 1 instead of consulting
// the Unicode table — the claim being that the first East Asian Wide/Fullwidth
// block starts there. Sweep the whole fast-path range plus a margin above it
// against the table so a future x/text update that widened something lower down
// fails here rather than silently tearing the layout by a column.
func TestCharWFastPathMatchesTable(t *testing.T) {
	table := func(r rune) int {
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			return 2
		default:
			return 1
		}
	}
	for r := range rune(0x1200) {
		if got, want := charW(r), table(r); got != want {
			t.Fatalf("charW(%U) = %d, want %d — the fast-path bound is wrong", r, got, want)
		}
	}
	// spot-check that wide runes above the bound are still measured as 2
	for _, r := range []rune{'漢', '字', 'あ', '한', '％'} {
		if charW(r) != 2 {
			t.Errorf("charW(%U) = %d, want 2", r, charW(r))
		}
	}
}
