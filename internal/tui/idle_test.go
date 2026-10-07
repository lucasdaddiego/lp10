// The idle screen (idle.go): the big clock and the sources that wake the box.

package tui

import (
	"strings"
	"testing"
	"time"
)

// Connected with nothing playing, the full player shows the big clock and
// the sources that always wake the box; the compact player says the same in
// its hint line; the mini line says nothing plays.
func TestIdleScreen(t *testing.T) {
	m, _, _ := modelWith(idleState())
	m.rows, m.cols = 40, 120
	out := clean(render(t, m))
	if !strings.Contains(out, "█████") {
		t.Errorf("idle screen lacks the block clock:\n%s", out)
	}
	for _, want := range []string{"nothing playing", wakeHint} {
		if !strings.Contains(out, want) {
			t.Errorf("idle screen missing %q", want)
		}
	}
	for _, absent := range []string{"Vol", "Playing", "Paused", GL["rew"]} {
		if strings.Contains(out, absent) {
			t.Errorf("idle screen carries %q:\n%s", absent, out)
		}
	}
	// compact: the hint line
	m.rows, m.cols = 20, 80
	if out := clean(render(t, m)); !strings.Contains(out, "nothing playing") || !strings.Contains(out, wakeHint) {
		t.Errorf("compact idle hint missing:\n%s", out)
	}
	// mini
	m.rows = MiniRows - 1
	if out := clean(render(t, m)); out != GL["note"]+" nothing playing" {
		t.Errorf("mini idle = %q", out)
	}
	// a narrow idle frame clips the hint rather than overflowing
	m.rows, m.cols = 30, FullCols
	render(t, m)
}

// bigClock draws HH:MM in the five-row block font: five digits' worth of
// glyphs, two columns apart, the colon one column wide.
func TestBigClock(t *testing.T) {
	rows := bigClock(time.Date(2026, 9, 12, 16, 4, 0, 0, time.UTC))
	if len(rows) != 5 {
		t.Fatalf("bigClock rows = %d, want 5", len(rows))
	}
	want := 4*5 + 1 + 4*2 // four digits, the colon, four gaps
	for i, r := range rows {
		if DispW(r) != want {
			t.Errorf("row %d width %d, want %d", i, DispW(r), want)
		}
	}
	if rows[0] != blockDigits['1'][0]+"  "+blockDigits['6'][0]+"  "+blockDigits[':'][0]+"  "+blockDigits['0'][0]+"  "+blockDigits['4'][0] {
		t.Errorf("first row = %q", rows[0])
	}
	for r, g := range blockDigits {
		for i, ln := range g {
			if w := DispW(g[0]); DispW(ln) != w {
				t.Errorf("glyph %q row %d width %d, want %d", r, i, DispW(ln), w)
			}
		}
	}
}
