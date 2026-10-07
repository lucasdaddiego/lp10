// The display helpers (display.go): windows, glyphs, width detection and
// the friendly error wording.

package tui

import (
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestCov_dispWindowEdges(t *testing.T) {
	// content entirely before the window: a,b,c skipped, "de" taken
	if got := dispWindow("abcdef", 3, 2); got != "de" {
		t.Errorf("dispWindow(abcdef,3,2) = %q, want de", got)
	}
	// a double-width rune straddling each edge renders its visible cells as spaces
	got := dispWindow("漢字漢字", 1, 4)
	if DispW(got) != 4 {
		t.Errorf("dispWindow straddle width = %d, want 4", DispW(got))
	}
	if !strings.Contains(got, "字") {
		t.Errorf("dispWindow straddle should keep the fully-inside 字: %q", got)
	}
}

func TestCov_glyphsAndDetectAmb(t *testing.T) {
	// glyphs(2) is the ASCII fallback set (CJK locale / ambiguous-wide terminal).
	if g := glyphs(2); g["play"] != ">" || g["note"] != "*" || g["ell"] != "..." {
		t.Errorf("glyphs(2) ASCII fallback wrong: %v", g["play"])
	}
	if g := glyphs(1); g["play"] != "▶" || g["note"] != "♪" {
		t.Errorf("glyphs(1) unicode set wrong: %v", g["play"])
	}
	// every glyph has a fallback, and every fallback is pure ASCII
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

	// detectAmb: CJK locales -> 2, everything else -> 1, with LC_ALL > LC_CTYPE > LANG.
	for _, lang := range []string{"ja_JP.UTF-8", "ko_KR.UTF-8", "zh_CN.UTF-8"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("LC_ALL", "")
			t.Setenv("LC_CTYPE", "")
			t.Setenv("LANG", lang)
			if got := detectAmb(); got != 2 {
				t.Errorf("detectAmb(%s) = %d, want 2", lang, got)
			}
		})
	}
	t.Run("default", func(t *testing.T) {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", "en_US.UTF-8")
		if got := detectAmb(); got != 1 {
			t.Errorf("detectAmb(en) = %d, want 1", got)
		}
	})
	t.Run("unset", func(t *testing.T) {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", "C")
		if got := detectAmb(); got != 1 {
			t.Errorf("detectAmb(C) = %d, want 1", got)
		}
	})
	t.Run("lc_all_precedence", func(t *testing.T) {
		t.Setenv("LC_ALL", "ja_JP.UTF-8")
		t.Setenv("LC_CTYPE", "en_US.UTF-8")
		t.Setenv("LANG", "en_US.UTF-8")
		if got := detectAmb(); got != 2 {
			t.Errorf("detectAmb(LC_ALL=ja) = %d, want 2 (LC_ALL wins)", got)
		}
	})
}

// friendlyError condenses a raw dial error to a calm, actionable line; an
// error it does not recognise passes through unchanged.
func TestCov_friendlyErrorAllCases(t *testing.T) {
	const (
		host    = "can't find the device — are you on the home network?"
		route   = "no route to the device — check the network"
		refused = "the device refused the connection on :2018"
		timeout = "connection timed out — the device may be off or away"
	)
	cases := map[string]string{
		"cannot reach :2018: dial tcp: lookup lp10.local: no such host": host,
		"Could not resolve hostname x":                                  host,
		"dial tcp 192.0.2.13:2018: connect: no route to host":           route,
		"dial tcp: connect: network is unreachable":                     route,
		"dial tcp 192.0.2.13:2018: connect: Connection refused":         refused,
		"dial tcp 192.0.2.13:2018: i/o timeout":                         timeout,
		"Operation timed out":                                           timeout,
		"connected, but no answer for 6s":                               timeout,
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
