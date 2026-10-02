// Hostile-input fuzzing for the trust-boundary strip. Every device string
// reaches the terminal through Printable, so its output invariants must hold
// for arbitrary bytes, not only for the inputs the unit tests thought of.

package protocol

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// stripped reports whether printable removes r wherever it stands: every
// control, format or separator rune but the ASCII space (the ZWJ is kept only
// between two kept runes, so it counts here too).
func stripped(r rune) bool { return r != ' ' && unicode.In(r, unicode.C, unicode.Z) }

// bidi are the embedding, override, isolate and mark runes that can reorder
// what a terminal shows — named here as well as covered by Cf, so a change to
// the category test that let them through would fail loudly.
func bidi(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

func FuzzPrintable(f *testing.F) {
	f.Add("Big Bang")
	f.Add("Usted Señalemelo")
	f.Add("a\x1b[2J\x07\u009b\u202eb\u2066c")
	f.Add("Cafe\u0301")
	f.Add("A" + strings.Repeat("\u0301", 64))
	f.Add("\U0001f468\u200d\U0001f469\u200d\U0001f466")
	f.Add("\u200da\u200d\u200d\u200db\u200d\x1bc\u200d")
	f.Add("e\x07\u0301")      // a strip that leaves a composable pair (see below)
	f.Add("\u1100\x07\u1161") // the same for Hangul jamo
	f.Add("a\xffb\xc3\x9b")
	f.Add("\u0e19\u0e49\u0e33 1\ufe0f\u20e3")
	f.Fuzz(func(t *testing.T, in string) {
		out := Printable(in)
		if !utf8.ValidString(out) {
			t.Fatalf("Printable(%+q) = %+q is not valid UTF-8", in, out)
		}
		rs := []rune(out)
		marks := 0
		for i, r := range rs {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Fatalf("Printable(%+q) = %+q keeps the C0/C1 control %U", in, out, r)
			}
			if bidi(r) {
				t.Fatalf("Printable(%+q) = %+q keeps the bidi control %U", in, out, r)
			}
			if r == zwj {
				// only between two kept runes, never doubled
				if i == 0 || i == len(rs)-1 || rs[i-1] == zwj || rs[i+1] == zwj {
					t.Fatalf("Printable(%+q) = %+q keeps an unjoined ZWJ at %d", in, out, i)
				}
			} else if stripped(r) {
				t.Fatalf("Printable(%+q) = %+q keeps %U", in, out, r)
			}
			if unicode.In(r, unicode.M) {
				if marks++; marks > maxMarkRun {
					t.Fatalf("Printable(%+q) = %+q keeps a run of %d marks", in, out, marks)
				}
			} else if r != zwj {
				marks = 0
			}
		}
		// Idempotent and NFC on every input: removing a control can leave two
		// runes that compose ("e BEL U+0301" -> "e U+0301"), so Printable
		// normalizes again after the strip; without that a second pass turned
		// the pair into "é".
		if again := Printable(out); again != out {
			t.Fatalf("Printable is not idempotent on %+q: %+q -> %+q", in, out, again)
		}
		if !norm.NFC.IsNormalString(out) {
			t.Fatalf("Printable(%+q) = %+q is not NFC", in, out)
		}
	})
}
