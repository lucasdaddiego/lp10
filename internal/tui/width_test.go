package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// DispW measures like ansi.StringWidth — what lipgloss, frameLines and the
// terminal go by — on everything the sanitizer lets through: emoji with a
// presentation selector, ZWJ sequences, combining marks, East Asian wide
// glyphs, and the plain ASCII fast path. Clip and dispWindow honour the same
// widths, so a heart in a title can no longer grow the frame a column.
func TestDispWAgreesWithANSI(t *testing.T) {
	samples := []string{
		"plain ascii", "café", "naïve ﬁ", "漢字テスト", "한글",
		"❤️", "1️⃣", "👨‍👩‍👧‍👦", "e\u0301\u0301x", "ก้อน",
		strings.Repeat("❤️", 18) + " Love", "a❤️b漢c👨‍👩‍👧‍👦d",
		"▶ ⏸ ◀◀ ▶▶ ♪ ⚠ ☾ ◐ ━ ─ ╭ ● ·",
	}
	for _, s := range samples {
		if got, want := DispW(s), ansi.StringWidth(s); got != want {
			t.Errorf("DispW(%q) = %d, want %d", s, got, want)
		}
		full := ansi.StringWidth(s)
		for w := 1; w <= full+1; w++ {
			if c := Clip(s, w); ansi.StringWidth(c) > w {
				t.Errorf("Clip(%q, %d) = %q measures %d", s, w, c, ansi.StringWidth(c))
			}
			for off := 0; off <= full; off++ {
				if win := dispWindow(s, off, w); ansi.StringWidth(win) != w {
					t.Errorf("dispWindow(%q, %d, %d) = %q measures %d, want %d", s, off, w, win, ansi.StringWidth(win), w)
				}
			}
		}
		if c := Clip(s, full); c != s {
			t.Errorf("Clip(%q, its own width) = %q, want unchanged", s, c)
		}
	}
	// the narrow fast path is exactly "every rune below U+0300"
	if !narrow("plain ascii \u02ff") || narrow("\u0300") || narrow("❤️") {
		t.Error("narrow() boundary wrong")
	}
}
