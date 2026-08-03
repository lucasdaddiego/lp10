// Package tui is the Bubble Tea terminal UI: rendering, input dispatch, and the
// display-formatting helpers. Port of lp10lib/tui.py.
package tui

import (
	"cmp"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"golang.org/x/text/width"
)

// localeAmb is 2 under a CJK locale, 1 otherwise. It no longer affects width
// measurement (charW fixes ambiguous glyphs at 1 to match lipgloss); it only
// selects the glyph set — under localeAmb==2, GL falls back to ASCII glyphs so a
// terminal that *does* render ambiguous double-width still stays aligned.
var localeAmb = detectAmb()

// GL is the glyph set, with ASCII fallbacks when localeAmb == 2 (so every positioned
// glyph is width-1).
var GL = glyphs(localeAmb)

func detectAmb() int {
	// POSIX precedence for character handling: LC_ALL > LC_CTYPE > LANG.
	loc := cmp.Or(os.Getenv("LC_ALL"), os.Getenv("LC_CTYPE"), os.Getenv("LANG"))
	if len(loc) >= 2 {
		switch strings.ToLower(loc[:2]) {
		case "ja", "ko", "zh":
			return 2
		}
	}
	return 1
}

func glyphs(amb int) map[string]string {
	if amb == 2 {
		return map[string]string{
			"play": ">", "pause": "#", "rew": "<<", "ff": ">>", "note": "*", "warn": "!",
			"fill": "=", "track": "-",
			"tl": "+", "tr": "+", "bl": "+", "br": "+", "h": "-", "v": "|",
			"ell": "...",
		}
	}
	return map[string]string{
		"play": "▶", "pause": "⏸", "rew": "◀◀", "ff": "▶▶", "note": "♪", "warn": "⚠",
		"fill": "━", "track": "─",
		"tl": "╭", "tr": "╮", "bl": "╰", "br": "╯", "h": "─", "v": "│",
		"ell": "…",
	}
}

// FmtMs formats milliseconds as MM:SS. Hand-rolled (one alloc) because the seek
// row formats two of these on every animated frame; the Sprintf fallback keeps
// the identical %02d widening for a 100-minute-plus position.
func FmtMs(ms int) string {
	if ms < 0 {
		ms = 0
	}
	s := ms / 1000
	mm, ss := s/60, s%60
	if mm > 99 {
		return fmt.Sprintf("%02d:%02d", mm, ss)
	}
	return string([]byte{'0' + byte(mm/10), '0' + byte(mm%10), ':', '0' + byte(ss/10), '0' + byte(ss%10)})
}

// charW is the rendered width of one rune: W/F -> 2, everything else -> 1.
//
// East Asian *Ambiguous* glyphs (●, ·, the box/block/meter glyphs lp10 draws)
// are width 1 here regardless of locale. lipgloss — which does the actual
// rendering and padding — always measures them as 1, and so does a modern
// terminal (Ghostty); counting them as 2 under a CJK locale made DispW disagree
// with lipgloss and tore the layout by a column. Glyph *selection* still adapts
// to a CJK locale via `localeAmb` / the GL ASCII fallbacks (defensive for terminals
// configured to render ambiguous double-width); only measurement is fixed at 1.
func charW(r rune) int {
	// Fast path: the first East Asian Wide/Fullwidth block is Hangul Jamo at
	// U+1100, so everything below it is width 1 without consulting the table.
	// That covers every rune the UI itself draws and almost all track metadata;
	// DispW runs dozens of times per rendered frame. TestCharWFastPath sweeps the
	// boundary against the table to keep the two in agreement.
	if r < 0x1100 {
		return 1
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}

// DispW is the total rendered width of a string.
func DispW(s string) int {
	w := 0
	for _, r := range s {
		w += charW(r)
	}
	return w
}

// Clip truncates s to display width w, appending the ellipsis glyph when it
// overflows. Returns "" for non-positive widths.
func Clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if DispW(s) <= w {
		return s
	}
	ell := GL["ell"]
	budget := w - DispW(ell)
	if budget <= 0 {
		// no room for the ellipsis itself (e.g. the width-3 ASCII "..." on a
		// CJK terminal at w<3): hard-truncate to width w, no ellipsis.
		ell, budget = "", w
	}
	// Find the byte offset where the budget runs out and slice there, rather than
	// rebuilding the prefix rune by rune — Clip runs on nearly every line of every
	// frame, so this is one allocation instead of a Builder's several.
	cut, used := len(s), 0
	for i, ch := range s {
		cw := charW(ch)
		if used+cw > budget {
			cut = i
			break
		}
		used += cw
	}
	return s[:cut] + ell
}

// dispWindow returns the run of s covering display columns [off, off+w),
// space-padded to exactly w columns so callers stay aligned. A double-width
// rune straddling either edge is rendered as spaces for its visible cells.
func dispWindow(s string, off, w int) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	col, taken := 0, 0
	for _, r := range s {
		if taken >= w {
			break
		}
		cw := charW(r)
		end := col + cw
		switch {
		case end <= off: // entirely before the window
		case col >= off && taken+cw <= w: // entirely inside
			b.WriteRune(r)
			taken += cw
		default: // straddles an edge — fill its visible cells with spaces
			lo, hi := off, off+w
			if col > lo {
				lo = col
			}
			if end < hi {
				hi = end
			}
			for vis := hi - lo; vis > 0 && taken < w; vis-- {
				b.WriteByte(' ')
				taken++
			}
		}
		col = end
	}
	for taken < w {
		b.WriteByte(' ')
		taken++
	}
	return b.String()
}

// SourceName resolves the playback source label from the track's URL/source id.
func SourceName(t *protocol.Track) string {
	if t == nil {
		return ""
	}
	url := strings.ToLower(t.PlayURL)
	switch {
	case strings.HasPrefix(url, "spotify:"):
		return "Spotify"
	case strings.Contains(url, "tidal"):
		return "TIDAL"
	case strings.Contains(url, "airplay"):
		return "AirPlay"
	}
	src := t.CurrentSource
	if src == 0 {
		return ""
	}
	switch src {
	case 1:
		return "AirPlay"
	case 2:
		return "DLNA"
	case 3:
		return "Bluetooth"
	case 4:
		return "Spotify"
	case 5:
		return "Line-In"
	case 6:
		return "USB"
	}
	return fmt.Sprintf("Source %d", src)
}

// Quality renders the "Mime · NN kHz" quality line for a track.
func Quality(t *protocol.Track) string {
	if t == nil {
		return ""
	}
	var bits []string
	if t.MIME != "" {
		bits = append(bits, t.MIME)
	}
	if t.SampleRate != 0 {
		bits = append(bits, strconv.FormatFloat(float64(t.SampleRate)/1000, 'g', -1, 64)+" kHz")
	}
	return strings.Join(bits, " · ")
}

// friendlyError condenses a raw ssh / network error into a short, calm line for
// the UI. The raw stderr (e.g. "ssh: Could not resolve hostname lp10.local:
// nodename nor servname provided, or not known") is accurate but long and
// alarming; these map the common cases to something human and actionable, and at
// worst just drop the "ssh:" prefix.
func friendlyError(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "could not resolve") || strings.Contains(low, "name or service not known"):
		return "can't find the device — are you on the home network?"
	case strings.Contains(low, "no route to host") || strings.Contains(low, "network is unreachable"):
		return "no route to the device — check the network"
	case strings.Contains(low, "connection refused"):
		return "the device refused the connection"
	case strings.Contains(low, "timed out") || strings.Contains(low, "timeout"):
		return "connection timed out — the device may be off or away"
	case strings.Contains(low, "permission denied") || strings.Contains(low, "publickey"):
		return "ssh authentication failed"
	case strings.HasPrefix(low, "ssh: "):
		return strings.TrimSpace(msg[5:])
	}
	return msg
}
