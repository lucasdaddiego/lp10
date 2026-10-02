package protocol

import (
	"strings"
	"testing"
)

func TestPrintableMatchesCPythonCategories(t *testing.T) {
	// non-printable == Other (C*) or Separator (Z*) except the ASCII space:
	// U+200B (Cf) and U+00A0 (Zs) are stripped; the ASCII space is kept.
	if got := Printable("a\u200bb\u00a0c d"); got != "abc d" {
		t.Errorf("Printable = %q, want %q", got, "abc d")
	}
	if got := Printable("x\x07y\ty"); got != "xyy" {
		t.Errorf("Printable should strip control chars: %q", got)
	}
}

// Every class a device string could use to break out of its cell is
// stripped: C0 and DEL (ESC starts a CSI/OSC, BEL ends an OSC), C1 (U+009B is
// a one-rune CSI on some terminals), the bidi embeddings, overrides and
// isolates (a reversed title), the line and paragraph separators, and the
// invisible format runes.
func TestPrintableStripsEveryControlClass(t *testing.T) {
	var hostile []rune
	for r := range rune(0x20) {
		hostile = append(hostile, r) // C0
	}
	hostile = append(hostile, 0x7f) // DEL
	for r := rune(0x80); r < 0xa0; r++ {
		hostile = append(hostile, r) // C1
	}
	hostile = append(hostile,
		'\u061c', '\u200e', '\u200f', // ALM, LRM, RLM
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e', // LRE RLE PDF LRO RLO
		'\u2066', '\u2067', '\u2068', '\u2069', // LRI RLI FSI PDI
		'\u2028', '\u2029', // line / paragraph separator
		'\u00a0', '\u3000', '\u2000', // non-ASCII spaces (Zs)
		'\u200b', '\u00ad', '\ufeff', '\u2060', // ZWSP, soft hyphen, BOM, word joiner
		'\ue000', '\U000f0000', // private use (Co)
	)
	for _, r := range hostile {
		in := "x" + string(r) + "y"
		if got := Printable(in); got != "xy" {
			t.Errorf("Printable(%+q) = %+q, want \"xy\"", in, got)
		}
	}
	// all of them at once, around real text
	in := "Big" + string(hostile) + " Bang"
	if got := Printable(in); got != "Big Bang" {
		t.Errorf("Printable(all classes) = %+q, want \"Big Bang\"", got)
	}
}

// Real metadata passes untouched: accents, other scripts, emoji, the ASCII
// space, and punctuation the wire format itself uses.
func TestPrintableKeepsRealText(t *testing.T) {
	for _, s := range []string{
		"Usted Señalemelo",
		"Big Bang",
		"Live: At Wembley (2024)",
		"ナルト",
		"مرحبا",
		"Ελληνικά",
		"🎵 \U0001f1e6\U0001f1f7",
		"a;b,c@d",
		"",
		"   ",
	} {
		if got := Printable(s); got != s {
			t.Errorf("Printable(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestPrintableIsTheExportedPrintable(t *testing.T) {
	for _, s := range []string{"a\x1bb", "Cafe\u0301", "\u200da\u200d", "x"} {
		if Printable(s) != printable(s) {
			t.Errorf("Printable(%q) != printable(%q)", s, s)
		}
	}
}

func TestPrintableNormalizesNFC(t *testing.T) {
	// NFD "Cafe\u0301" (e + combining acute) composes to the form the terminal
	// displays, so the width math agrees with what is actually rendered.
	if got := Printable("Cafe\u0301"); got != "Caf\u00e9" {
		t.Errorf("Printable(NFD) = %q, want composed %q", got, "Caf\u00e9")
	}
}

func TestPrintableCapsCombiningMarkFloods(t *testing.T) {
	// A zalgo run is ONE grapheme cluster — ansi.Truncate can never clip it —
	// so the flood must be bounded here at the parse boundary. NFC folds the
	// first mark into the base (A+0301 = Á); the cap keeps maxMarkRun more.
	flood := "A" + strings.Repeat("\u0301", 10000)
	want := "\u00c1" + strings.Repeat("\u0301", maxMarkRun)
	if got := Printable(flood); got != want {
		t.Errorf("Printable(flood) kept %d runes, want composed base + %d marks",
			len([]rune(got)), maxMarkRun)
	}
	// Legit stacks under the cap survive whole: Thai vowel + tone mark
	// (U+0E49 is category Mn), and a keycap sequence (VS16 U+FE0F Mn +
	// combining enclosing keycap U+20E3 Me).
	for _, s := range []string{"\u0e19\u0e49\u0e33", "1\ufe0f\u20e3"} {
		if got := Printable(s); got != s {
			t.Errorf("Printable(%q) = %q, want unchanged", s, got)
		}
	}
	// A non-mark resets the run: marks in separate words don't pool into one cap.
	sep := "\u00e1\u0301 \u00e1\u0301"
	if got := Printable(sep); got != sep {
		t.Errorf("Printable(%q) = %q, want unchanged", sep, got)
	}
	// A stripped control does not reset it: hiding a BEL between two halves
	// of a flood must not double the cap.
	split := "A" + strings.Repeat("\u0301", 10) + "\x07" + strings.Repeat("\u0301", 10)
	if got := Printable(split); got != want {
		t.Errorf("Printable(flood split by BEL) = %+q, want %+q", got, want)
	}
}

func TestPrintableKeepsZWJInsideEmojiSequences(t *testing.T) {
	family := "\U0001f468\u200d\U0001f469\u200d\U0001f466" // family: man+ZWJ+woman+ZWJ+boy
	if got := Printable(family); got != family {
		t.Errorf("Printable(family emoji) = %q, want unchanged", got)
	}
	// Outside a joined pair the ZWJ stays stripped (leading, trailing, and
	// runs collapse): it is still an invisible Cf character everywhere else.
	if got := Printable("\u200da\u200d"); got != "a" {
		t.Errorf("Printable = %q, want %q (bare ZWJs stripped)", got, "a")
	}
	if got := Printable("a\u200d\u200d\u200db"); got != "a\u200db" {
		t.Errorf("Printable = %q, want single ZWJ kept (run collapses)", got)
	}
	// A stripped control between the ZWJ and the next rune unarms the join:
	// in "a ZWJ ESC b" the ZWJ joined a and the ESC, and keeping it would
	// fabricate an a-b join that was never in the input.
	if got := Printable("a\u200d\x1bb"); got != "ab" {
		t.Errorf("Printable = %q, want %q (stripped rune unarms the join)", got, "ab")
	}
}

// Invalid UTF-8 cannot smuggle a raw byte through: each bad byte becomes
// U+FFFD, which is printable, and the result is valid UTF-8.
func TestPrintableReplacesInvalidUTF8(t *testing.T) {
	if got := Printable("a\xffb\xc3"); got != "a\ufffdb\ufffd" {
		t.Errorf("Printable(invalid) = %+q", got)
	}
	// a C1 control written as its raw single byte is invalid UTF-8, not C1
	if got := Printable("a\x9b31mb"); got != "a\ufffd31mb" {
		t.Errorf("Printable(raw 0x9b) = %+q", got)
	}
}

// TestPrintableIsIdempotentAcrossAStrippedControl pins the two inputs that
// broke idempotence when Printable normalized only before the strip: the
// control between two composable runes went, and the pair stayed decomposed
// until a second pass composed it.
func TestPrintableIsIdempotentAcrossAStrippedControl(t *testing.T) {
	for in, want := range map[string]string{
		"e\x07́": "é", // é
		"ᄀ\x07ᅡ": "가", // 가
	} {
		got := Printable(in)
		if got != want {
			t.Errorf("Printable(%+q) = %+q, want %+q", in, got, want)
		}
		if again := Printable(got); again != got {
			t.Errorf("Printable is not idempotent on %+q: %+q -> %+q", in, got, again)
		}
	}
}
