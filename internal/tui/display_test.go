// The display helpers (display.go): windows, glyphs, width detection and
// the friendly error wording.

package tui

import (
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// Every glyph has an ASCII fallback for a CJK locale, and the locale is read
// from LC_ALL before LC_CTYPE and LANG.
func TestGlyphFallbackAndLocale(t *testing.T) {
	one, two := glyphs(1), glyphs(2)
	for k := range one {
		fb, ok := two[k]
		if !ok {
			t.Errorf("glyph %q has no ASCII fallback", k)
		}
		for _, r := range fb {
			if r > 0x7e {
				t.Errorf("fallback for %q is not ASCII: %q", k, fb)
			}
		}
	}

	for _, c := range []struct {
		lcAll, lang string
		want        int
	}{{"", "ja_JP.UTF-8", 2}, {"", "en_US.UTF-8", 1}, {"ja_JP.UTF-8", "en_US.UTF-8", 2}} {
		t.Setenv("LC_ALL", c.lcAll)
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", c.lang)
		if got := detectAmb(); got != c.want {
			t.Errorf("detectAmb(LC_ALL=%q LANG=%q) = %d, want %d", c.lcAll, c.lang, got, c.want)
		}
	}
}

// friendlyError condenses a raw dial error to a calm, actionable line; an
// error it does not recognise passes through unchanged.
func TestFriendlyError(t *testing.T) {
	cases := map[string]string{
		"cannot reach :2018: dial tcp: lookup lp10.local: no such host": "can't find the device — are you on the home network?",
		"dial tcp 192.0.2.13:2018: connect: no route to host":           "no route to the device — check the network",
		"dial tcp 192.0.2.13:2018: connect: Connection refused":         "the device refused the connection on :2018",
		"connected, but no answer for 6s":                               "connection timed out — the device may be off or away",
		"command not delivered":                                         "command not delivered",
		"":                                                              "",
	}
	for in, want := range cases {
		if got := friendlyError(in); got != want {
			t.Errorf("friendlyError(%q) = %q, want %q", in, got, want)
		}
	}
}

// While disconnected, a connection error shows a calm friendly reason in the
// idle area and never the raw dial error as a red bottom line.
func TestDisconnectedErrorIsFriendly(t *testing.T) {
	for _, size := range [][2]int{{32, 100}, {20, 64}} {
		st := protocol.NewState()
		st.StartConnection()
		st.Note("cannot reach :2018: dial tcp: lookup lp10.local: no such host")
		m, _, _ := modelWith(st)
		m.rows, m.cols = size[0], size[1]
		view := clean(render(t, m))
		if !strings.Contains(view, "can't find the device") {
			t.Errorf("%dx%d: disconnected idle should show the friendly reason:\n%s", size[1], size[0], view)
		}
		if strings.Contains(view, "lookup lp10.local") {
			t.Errorf("%dx%d: the raw dial error must not appear while reconnecting", size[1], size[0])
		}
		if strings.Contains(view, GL["warn"]) {
			t.Errorf("%dx%d: a reconnect reason is not a red error line:\n%s", size[1], size[0], view)
		}
	}
}
