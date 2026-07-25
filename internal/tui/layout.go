// Width-aware layout primitives shared by the player views and the diagnostics
// overlay: padding, centring, and column splitting. Two width vocabularies
// coexist deliberately — DispW for plain text, lipgloss.Width for styled text
// (see padDisp vs padVis).

package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Padding runs. strings.Repeat allocates on every call, and the dashboard pads
// dozens of gaps per frame — slicing a fixed run instead is allocation-free (a
// substring of a package-level string is just a new header). The fallback keeps
// correctness on absurdly wide terminals.
var (
	spRun   = strings.Repeat(" ", 512)
	dashRun = strings.Repeat("─", 512) // "─" is 3 bytes: slice dashRun[:3n]
)

// sp returns n spaces without allocating (for n ≤ 512).
func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	if n <= len(spRun) {
		return spRun[:n]
	}
	return strings.Repeat(" ", n)
}

// dashes returns n light-horizontal rules ("─", the EQ slider track) without
// allocating. Callers that draw locale-dependent glyphs (GL) keep strings.Repeat.
func dashes(n int) string {
	if n <= 0 {
		return ""
	}
	if b := 3 * n; b <= len(dashRun) {
		return dashRun[:b]
	}
	return strings.Repeat("─", n)
}

// ccell centres an already-styled string in a colW-wide block via lipgloss
// (display-width aware, ANSI-safe), so labels, bars, and values of differing
// widths line up identically — the volume rail and the EQ bands lean on it.
//
// Do NOT "optimise" this into plain padding. Style.Width(w) does not only pad:
// it also WRAPS content wider than w onto extra lines. Every caller happens to
// pass something that fits, so the two look interchangeable — right up until one
// doesn't and a single cell silently becomes several rows. Measured at ~4% of a
// frame's allocations, which is not worth that.
func ccell(s string, colW int) string {
	return lipgloss.NewStyle().Width(colW).Align(lipgloss.Center).Render(s)
}

// padDisp right-pads s with spaces to display width w (a no-op if already ≥ w).
// For PLAIN text only — use padVis for already-styled strings (DispW counts the
// bytes of any ANSI escapes, which over-measures a styled string).
func padDisp(s string, w int) string {
	if d := w - DispW(s); d > 0 {
		return s + spaces(d)
	}
	return s
}

// rpadDisp left-pads s with spaces to display width w (right-justify); no-op if ≥ w.
func rpadDisp(s string, w int) string {
	if d := w - DispW(s); d > 0 {
		return spaces(d) + s
	}
	return s
}

// padVis right-pads a (possibly ANSI-styled) string to visible width w, measuring
// with visWidth so colour escapes aren't counted. The diag cards and the player
// columns lean on it to stay aligned once styling is applied on a real terminal.
func padVis(s string, w int) string {
	if d := w - visWidth(s); d > 0 {
		return s + spaces(d)
	}
	return s
}

// visWidth is the visible width of ONE already-rendered line. It is what
// lipgloss.Width computes for a single line, minus the strings.Split(s, "\n")
// that call makes first — measurably the top allocator once everything else was
// pen-ified, since the frame measures every body line every frame. Callers must
// pass line-at-a-time strings (everything in this package is).
func visWidth(s string) int { return ansi.StringWidth(s) }

// between places left- and right-aligned styled segments W columns apart, using
// the segments' known visible widths (styled strings carry ANSI codes).
func between(left string, leftW int, right string, rightW int, W int) string {
	return left + spaces(max(W-leftW-rightW, 1)) + right
}

// splitWidth divides total into n column widths summing to total (earlier
// columns get the remainder).
func splitWidth(total, n int) []int {
	base, extra := total/n, total%n
	w := make([]int, n)
	for i := range w {
		w[i] = base
		if i < extra {
			w[i]++
		}
	}
	return w
}
