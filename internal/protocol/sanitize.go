// Sanitization at the trust boundary: every string the device sends is
// reduced to printable runes before it reaches the terminal.

package protocol

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Track is the now-playing schema: the fields the tunnel pushes on a track
// change (TIT, ART, ALB) and the service named when playback starts (VND).
// Each is control-stripped on the way in. There is no position, duration,
// format or cover: the tunnel carries none of them.
type Track struct {
	TrackName string
	Artist    string
	Album     string
	Service   string // the VND word, e.g. "spotify" ("" until the device names one)
}

// Empty reports whether t contains no sanitized track data.
func (t *Track) Empty() bool {
	return t == nil || *t == (Track{})
}

// Printable strips control/separator characters from a device-supplied string
// before it reaches the terminal, so a raw ESC/BEL/C1 byte can't start an
// escape sequence (SGR colour bleed, an injected OSC-8 hyperlink) in the
// rendered frame. It is the exported form of printable, for the few
// device-string boundaries outside this package (the mDNS device name, the
// tunnel's frames, the vendor's replies).
func Printable(s string) string { return printable(s) }

// maxMarkRun caps consecutive combining marks (category M) kept by printable.
// Marks are neither C nor Z, so a "zalgo" flood (one base rune + tens of
// thousands of U+0301) would otherwise pass the strip whole — and because a
// mark run is a single grapheme cluster, ansi.Truncate can never clip it, so
// one hostile device string would dump the entire run into the terminal on
// every frame. Three consecutive marks accommodate real orthographies (Thai
// vowel + tone stacks, emoji VS16 + keycap) while bounding the flood.
const maxMarkRun = 3

// zwj is U+200D ZERO WIDTH JOINER — category Cf, so Python's isprintable
// rejects it, but stripping it decomposes emoji ZWJ sequences (a family emoji
// renders as its member emoji), which real Spotify metadata carries.
const zwj = '\u200d'

// printable strips control/separator characters the way CPython's
// str.isprintable does: non-printable == category Other (C*) or Separator (Z*),
// except the ASCII space. Using the category test (rather than Go's
// unicode.IsPrint) keeps characters that are assigned in a newer Unicode version
// than Go's tables, matching Python more closely.
//
// Three deliberate deviations from the plain Python semantics, all at the
// same trust boundary:
//   - the input is NFC-normalized first, so decomposed metadata (NFD "Café"
//     from macOS/AirPlay sources) reaches the width math in the same form the
//     terminal displays — and again last: a stripped control between two
//     runes that compose ("e", BEL, U+0301) leaves them adjacent, and the
//     result must be NFC too, or a second pass would change it;
//   - runs of combining marks are capped at maxMarkRun (zalgo flood);
//   - a ZWJ is kept when (and only when) it sits between two kept runes, so
//     emoji ZWJ sequences survive while the invisible-character class stays
//     stripped everywhere else.
func printable(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	marks := 0         // consecutive combining marks kept
	joinArmed := false // a ZWJ waiting for a kept rune on its right
	for _, c := range s {
		if c == zwj {
			joinArmed = b.Len() > 0
			continue
		}
		if c != ' ' && unicode.In(c, unicode.C, unicode.Z) {
			joinArmed = false
			continue
		}
		if unicode.In(c, unicode.M) {
			marks++
			if marks > maxMarkRun {
				continue
			}
		} else {
			marks = 0
		}
		if joinArmed {
			b.WriteRune(zwj)
			joinArmed = false
		}
		b.WriteRune(c)
	}
	return norm.NFC.String(b.String())
}
