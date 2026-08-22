package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func tnow() time.Time { return time.Date(2026, 8, 22, 23, 0, 0, 0, time.UTC) }

func TestNightKeyTogglesAndSends(t *testing.T) {
	m, st, collect := makeModel(t)
	// unknown state: assumes off, sends on
	m.key(kr('d'))
	got := collect()
	if len(got) != 1 || got[0].Mid != 91 || got[0].Data != "1" {
		t.Fatalf("sent = %+v, want [91 1]", got)
	}
	if s := st.Snap(); !s.NightKnown || !s.Night {
		t.Error("toggle should flip the snapshot optimistically")
	}
	m.key(kr('d'))
	if got := collect(); len(got) != 1 || got[0].Mid != 91 || got[0].Data != "0" {
		t.Errorf("sent = %+v, want [91 0]", got)
	}
	// device says on (e.g. set by another session): next press sends off
	protocol.ApplyRecord(st, protocol.Record{"n": {"  : values=on"}})
	m.pane = paneEQ // global key: works from the EQ pane too
	m.key(kr('d'))
	if got := collect(); len(got) != 1 || got[0].Data != "0" {
		t.Errorf("sent = %+v, want [91 0]", got)
	}
}

func TestNightBadgeInHeaderAndMini(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	W := FullCols - 6
	if h := stripANSI(m.headerRow(st.Snap(), tnow(), W, true)); strings.Contains(h, "night") {
		t.Fatalf("unknown/off: header must not show the badge: %q", h)
	}
	protocol.ApplyRecord(st, protocol.Record{"n": {"  : values=on"}})
	for _, full := range []bool{true, false} {
		styled := m.headerRow(st.Snap(), tnow(), W, full)
		if plain := stripANSI(styled); !strings.Contains(plain, GL["night"]+" night") {
			t.Errorf("full=%v: header %q lacks the night badge", full, plain)
		}
		if got := visWidth(styled); got != W {
			t.Errorf("full=%v: header width = %d, want %d", full, got, W)
		}
	}
	// with the sleep countdown too: both ride after the clock, width intact
	m.sleepAt = tnow().Add(30 * time.Minute)
	styled := m.headerRow(st.Snap(), tnow(), W, true)
	if plain := stripANSI(styled); !strings.Contains(plain, GL["sleep"]) || !strings.Contains(plain, "night") {
		t.Errorf("header %q should carry both the countdown and the badge", plain)
	}
	if got := visWidth(styled); got != W {
		t.Errorf("header width = %d, want %d", got, W)
	}
	m.rows, m.cols = MiniRows-1, 120
	if got := stripANSI(m.renderMini(st.Snap())); !strings.Contains(got, GL["night"]+" night") {
		t.Errorf("mini line = %q, want the badge", got)
	}
	protocol.ApplyRecord(st, protocol.Record{"n": {"  : values=off"}})
	if got := stripANSI(m.renderMini(st.Snap())); strings.Contains(got, "night") {
		t.Errorf("mini line = %q, off but still badged", got)
	}
}

func TestNightFooterHintAdaptsToWidth(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	// the minimum full width drops "d night" rather than clipping
	if got := stripANSI(m.footerRow(FullCols - 6)); strings.Contains(got, "night") || strings.Contains(got, GL["ell"]) {
		t.Errorf("W=64 footer = %q, want the short hint, unclipped", got)
	}
	if got := stripANSI(m.footerRow(80)); !strings.Contains(got, "d night") || strings.Contains(got, GL["ell"]) {
		t.Errorf("W=80 footer = %q, want the night hint, unclipped", got)
	}
	if got := stripANSI(m.footerRow(120)); !strings.Contains(got, "e/tab EQ") {
		t.Errorf("W=120 footer = %q, want the full hint", got)
	}
	for _, W := range []int{52, 64, 70, 80, 120} {
		if got := visWidth(m.footerRow(W)); got > W {
			t.Errorf("W=%d: footer width %d overflows", W, got)
		}
	}
}

func TestNightDiagRow(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 120
	m.diag = true
	if out := stripANSI(m.viewContent()); strings.Contains(out, "night") {
		t.Fatal("diag must not show a night row before the device reports one")
	}
	protocol.ApplyRecord(st, protocol.Record{"n": {"  : values=on"}})
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "night") || !strings.Contains(out, "multi-band DRC") {
		t.Errorf("wide diag lacks the night row:\n%s", out)
	}
	m.cols = 70 // stacked layout
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "night") {
		t.Errorf("stacked diag lacks the night row:\n%s", out)
	}
}

func TestNightGlyphHasASCIIFallback(t *testing.T) {
	if g := glyphs(2)["night"]; g != "N" {
		t.Errorf("glyphs(2)[night] = %q, want N", g)
	}
}


func TestNightRestoreToOnBaseline(t *testing.T) {
	m, st, collect := makeModel(t)
	protocol.ApplyRecord(st, protocol.Record{"n": {"  : values=on"}}) // baseline on
	m.nightRestore()
	if got := collect(); len(got) != 0 {
		t.Errorf("at the baseline nothing is sent: %+v", got)
	}
	m.key(kr('d')) // off
	collect()
	m.nightRestore() // back to on
	if got := collect(); len(got) != 1 || got[0].Mid != 91 || got[0].Data != "1" {
		t.Errorf("restore sent %+v, want [91 1]", got)
	}
	if !st.Snap().Night {
		t.Error("restore should flip the snapshot back on")
	}
}
