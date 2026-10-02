// Package tui is the Bubble Tea terminal UI: rendering, input dispatch, and the
// display-formatting helpers.
package tui

import (
	"cmp"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// localeAmb is 2 under a CJK locale, 1 otherwise. It does not affect width
// measurement (DispW fixes ambiguous glyphs at 1 to match lipgloss); it only
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
			"sleep": "z", "night": "N", "fill": "=", "track": "-",
			"tl": "+", "tr": "+", "bl": "+", "br": "+", "h": "-", "v": "|",
			"ell": "...",
		}
	}
	return map[string]string{
		"play": "▶", "pause": "⏸", "rew": "◀◀", "ff": "▶▶", "note": "♪", "warn": "⚠",
		"sleep": "☾", "night": "◐", "fill": "━", "track": "─",
		"tl": "╭", "tr": "╮", "bl": "╰", "br": "╯", "h": "─", "v": "│",
		"ell": "…",
	}
}

// narrow reports whether every rune of s is below U+0300 — no combining
// marks, no wide glyphs, no emoji, no joiners — so each rune is one cell and
// the byte-level fast paths apply. U+0300 encodes as CC 80, so a byte ≥ 0xCC
// can only start a rune at or above it. That covers every string the UI
// itself draws and almost all track metadata; DispW runs dozens of times per
// rendered frame.
func narrow(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0xCC {
			return false
		}
	}
	return true
}

// DispW is the rendered width of a string. Outside the narrow fast path it is
// ansi.StringWidth — what lipgloss, frameLines and the terminal itself measure
// by: graphemes, with emoji presentation (U+FE0F), ZWJ sequences and combining
// marks counted as the terminal draws them, and East Asian *Ambiguous* glyphs
// (●, ·, the box/meter glyphs lp10 draws) at 1 regardless of locale. The
// sanitizer deliberately keeps VS16 and ZWJ, so a "❤️" in a Spotify title
// reaches here as two cells; a per-rune table used to count it as one, and
// every heart grew the frame a column. (Glyph *selection* still adapts to a
// CJK locale via localeAmb / the GL ASCII fallbacks; only measurement is
// fixed.) TestDispWAgreesWithANSI pins the agreement.
func DispW(s string) int {
	if narrow(s) {
		return utf8.RuneCountInString(s)
	}
	return ansi.StringWidth(s)
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
	if narrow(s) {
		// Every rune is one cell: slice at the byte offset of the budget-th
		// rune rather than rebuilding the prefix — Clip runs on nearly every
		// line of every frame, so this is one allocation.
		n := 0
		for i := range s {
			if n == budget {
				return s[:i] + ell
			}
			n++
		}
	}
	// Grapheme by grapheme with the same measure as DispW (ansi.Truncate
	// sizes a keycap sequence differently from ansi.StringWidth and would
	// let a 2-cell "1️⃣" through a 1-cell budget).
	var b strings.Builder
	used := 0
	for rest := s; rest != ""; {
		seg, cw := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if used+cw > budget {
			break
		}
		b.WriteString(seg)
		used += cw
		rest = rest[len(seg):]
	}
	return b.String() + ell
}

// dispWindow returns the run of s covering display columns [off, off+w),
// space-padded to exactly w columns so callers stay aligned. A wide grapheme
// straddling either edge is rendered as spaces for its visible cells.
func dispWindow(s string, off, w int) string {
	if w <= 0 {
		return ""
	}
	fast := narrow(s)
	var b strings.Builder
	col, taken := 0, 0
	for rest := s; rest != "" && taken < w; {
		var seg string
		var cw int
		if fast {
			_, n := utf8.DecodeRuneInString(rest)
			seg, cw = rest[:n], 1
		} else {
			seg, cw = ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		}
		rest = rest[len(seg):]
		end := col + cw
		switch {
		case end <= off: // entirely before the window
		case col >= off && taken+cw <= w: // entirely inside
			b.WriteString(seg)
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

// SourceName names what is playing: the service the device named with its
// VND push ("spotify" → "Spotify") — the track's own, else the latest — and,
// before any, the input the status reports (NET → "Network"). "" when the
// device has named neither.
func SourceName(s protocol.Snapshot) string {
	svc := s.Service
	if s.Track != nil && s.Track.Service != "" {
		svc = s.Track.Service
	}
	if svc != "" {
		return serviceName(svc)
	}
	switch strings.ToUpper(s.Source) {
	case "":
		return ""
	case "NET":
		return "Network"
	case "BT":
		return "Bluetooth"
	case "LINE-IN":
		return "Line-In"
	case "USBPLAY", "USBDAC":
		return "USB"
	default:
		return s.Source
	}
}

// serviceNames are the UART API's VND words with a display spelling of their
// own; any other word shows as the device sent it.
var serviceNames = map[string]string{
	"spotify": "Spotify", "tidal": "TIDAL", "airplay": "AirPlay", "dlna": "DLNA", "upnp": "DLNA",
	"qobuz": "Qobuz", "tunein": "TuneIn", "amazon": "Amazon Music", "usb": "USB", "iheart": "iHeart",
	"napster": "Napster", "vtuner": "vTuner", "qplay": "QPlay", "phone": "Phone", "http": "Radio",
}

func serviceName(w string) string {
	if n, ok := serviceNames[strings.ToLower(w)]; ok {
		return n
	}
	return w
}

// friendlyError condenses a raw network error into a short, calm line for the
// UI. The raw dial error (e.g. "cannot reach :2018: dial tcp: lookup
// lp10.local: no such host") is accurate but long and alarming; these map the
// common cases to something human and actionable.
func friendlyError(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "no such host") || strings.Contains(low, "could not resolve"):
		return "can't find the device — are you on the home network?"
	case strings.Contains(low, "no route to host") || strings.Contains(low, "network is unreachable"):
		return "no route to the device — check the network"
	case strings.Contains(low, "connection refused"):
		return "the device refused the connection on :2018"
	case strings.Contains(low, "timed out") || strings.Contains(low, "timeout") || strings.Contains(low, "no answer"):
		return "connection timed out — the device may be off or away"
	}
	return msg
}
