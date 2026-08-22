package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// ---- sleep timer: arming ------------------------------------------------------

func TestSleepCyclesPresetsThenOff(t *testing.T) {
	m, _, _ := makeModel(t)
	now := time.Date(2026, 8, 22, 23, 0, 0, 0, time.UTC)
	for i, mins := range sleepPresets {
		m.sleepCycle(now)
		if want := now.Add(time.Duration(mins) * time.Minute); !m.sleepAt.Equal(want) {
			t.Errorf("press %d: sleepAt = %v, want %v", i+1, m.sleepAt, want)
		}
		if lbl, final := m.sleepLabel(now); lbl != GL["sleep"]+" "+strconv.Itoa(mins)+"m" || final {
			t.Errorf("press %d: label = %q final=%v", i+1, lbl, final)
		}
	}
	m.sleepCycle(now) // one past the last preset wraps to off
	if !m.sleepAt.IsZero() {
		t.Errorf("after the last preset the timer should be off, got %v", m.sleepAt)
	}
	if lbl, _ := m.sleepLabel(now); lbl != "" {
		t.Errorf("off label = %q, want empty", lbl)
	}
	m.sleepCycle(now) // and the next press starts over at the first preset
	if want := now.Add(time.Duration(sleepPresets[0]) * time.Minute); !m.sleepAt.Equal(want) {
		t.Errorf("restart: sleepAt = %v, want %v", m.sleepAt, want)
	}
}

// A re-arm restarts the countdown from now: "s" again after ten minutes is a
// fresh 30, not 30 minus the elapsed 10.
func TestSleepRearmRestartsFromNow(t *testing.T) {
	m, _, _ := makeModel(t)
	t0 := time.Date(2026, 8, 22, 23, 0, 0, 0, time.UTC)
	m.sleepCycle(t0)
	t1 := t0.Add(10 * time.Minute)
	m.sleepCycle(t1)
	if want := t1.Add(time.Duration(sleepPresets[1]) * time.Minute); !m.sleepAt.Equal(want) {
		t.Errorf("sleepAt = %v, want %v", m.sleepAt, want)
	}
}

func TestSleepKeysArmAndCancel(t *testing.T) {
	m, _, collect := makeModel(t)
	m.key(kr('s'))
	if m.sleepAt.IsZero() {
		t.Fatal("s should arm the timer")
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("arming must send nothing to the device, sent %+v", got)
	}
	m.key(kr('S'))
	if !m.sleepAt.IsZero() {
		t.Error("S should cancel the timer")
	}
	// S with nothing armed is inert
	m.key(kr('S'))
	if !m.sleepAt.IsZero() || m.sleepPreset != 0 {
		t.Error("S on an idle timer should stay off")
	}
	// the keys work from the EQ pane too (global rune keys)
	m.pane = paneEQ
	m.key(kr('s'))
	if m.sleepAt.IsZero() {
		t.Error("s should arm from the EQ pane")
	}
}

// ---- sleep timer: firing ------------------------------------------------------

func TestSleepFiresPauseOnceWhilePlaying(t *testing.T) {
	m, st, collect := makeModel(t)
	if st.Snap().Playing != 0 {
		t.Fatal("setup: should start playing")
	}
	m.sleepAt = time.Now().Add(-time.Second) // deadline already passed
	m.dispatch(logicMsg{})
	got := collect()
	if len(got) != 1 || got[0].Mid != 40 || got[0].Data != "PAUSE" {
		t.Fatalf("sent = %+v, want [40 PAUSE]", got)
	}
	s := st.Snap()
	if s.Playing == 0 {
		t.Error("playing should optimistically flip to paused")
	}
	if s.Error != "" {
		t.Errorf("firing must not post a note (it renders as the red error line), got %q", s.Error)
	}
	if !m.sleepAt.IsZero() {
		t.Error("the timer must disarm after firing")
	}
	// one-shot: the next tick sends nothing (and never a RESUME)
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Errorf("second tick sent %+v, want nothing", got)
	}
}

func TestSleepDoesNotFireBeforeDeadline(t *testing.T) {
	m, st, collect := makeModel(t)
	m.sleepAt = time.Now().Add(time.Hour)
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Errorf("sent %+v before the deadline", got)
	}
	if m.sleepAt.IsZero() || st.Snap().Playing != 0 {
		t.Error("an unexpired timer must stay armed and leave playback alone")
	}
}

// Already paused (or idle) at the deadline: disarm quietly, never RESUME.
func TestSleepNeverResumes(t *testing.T) {
	m, st, collect := makeModel(t)
	st.ToggleOptimistic() // now paused
	collect()
	m.sleepAt = time.Now().Add(-time.Second)
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Errorf("paused at the deadline: sent %+v, want nothing", got)
	}
	if !m.sleepAt.IsZero() {
		t.Error("the timer should disarm even when there was nothing to pause")
	}

	// idle (no track) at the deadline
	m2, _, collect2 := modelWith(protocol.NewState())
	m2.sleepAt = time.Now().Add(-time.Second)
	m2.dispatch(logicMsg{})
	if got := collect2(); len(got) != 0 {
		t.Errorf("idle at the deadline: sent %+v, want nothing", got)
	}
}

// ---- sleep timer: label + rendering ------------------------------------------

func TestSleepLabelRoundsUpAndFlagsFinalMinute(t *testing.T) {
	m, _, _ := makeModel(t)
	now := time.Date(2026, 8, 22, 23, 0, 0, 0, time.UTC)
	cases := []struct {
		left  time.Duration
		want  string
		final bool
	}{
		{30 * time.Minute, "30m", false},
		{29*time.Minute + 59*time.Second, "30m", false}, // rounds up, never reads a minute early
		{29*time.Minute + 1*time.Second, "30m", false},
		{29 * time.Minute, "29m", false},
		{61 * time.Second, "2m", false},
		{60 * time.Second, "1m", false},
		{59 * time.Second, "59s", true},
		{1500 * time.Millisecond, "2s", true},
		{0, "0s", true},
		{-5 * time.Second, "0s", true}, // past due (fires on the next tick) still renders sanely
	}
	for _, c := range cases {
		m.sleepAt = now.Add(c.left)
		lbl, final := m.sleepLabel(now)
		if lbl != GL["sleep"]+" "+c.want || final != c.final {
			t.Errorf("left=%v: label=%q final=%v, want %q/%v", c.left, lbl, final, c.want, c.final)
		}
	}
}

func TestSleepShowsInHeaderAndKeepsWidth(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	now := time.Now()
	W := FullCols - 6
	base := stripANSI(m.headerRow(st.Snap(), now, W, true))
	if strings.Contains(base, GL["sleep"]) {
		t.Fatalf("no timer armed but the header shows one: %q", base)
	}
	m.sleepAt = now.Add(30 * time.Minute)
	for _, full := range []bool{true, false} {
		styled := m.headerRow(st.Snap(), now, W, full)
		plain := stripANSI(styled)
		if !strings.Contains(plain, GL["sleep"]+" 30m") {
			t.Errorf("full=%v: header %q lacks the countdown", full, plain)
		}
		if got := visWidth(styled); got != W {
			t.Errorf("full=%v: header width = %d, want exactly %d", full, got, W)
		}
	}
	// disconnected: rides after the reconnecting status without breaking width
	st.Disconnect()
	styled := m.headerRow(st.Snap(), now, W, true)
	if plain := stripANSI(styled); !strings.Contains(plain, "connecting") || !strings.Contains(plain, GL["sleep"]) {
		t.Errorf("disconnected header = %q, want status + countdown", plain)
	}
	if got := visWidth(styled); got != W {
		t.Errorf("disconnected header width = %d, want %d", got, W)
	}
}

func TestSleepShowsOnMiniLine(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = MiniRows-1, 120
	m.sleepAt = time.Now().Add(45 * time.Minute)
	if got := stripANSI(m.renderMini(st.Snap())); !strings.Contains(got, GL["sleep"]+" 45m") {
		t.Errorf("mini line = %q, want the countdown", got)
	}
	m.sleepCancel()
	if got := stripANSI(m.renderMini(st.Snap())); strings.Contains(got, GL["sleep"]) {
		t.Errorf("mini line = %q, timer off but still shown", got)
	}
}

// The player hint advertises the key and still fits the full dashboard's
// narrowest content width unclipped.
func TestSleepFooterHintFitsMinimumWidth(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	got := stripANSI(m.footerRow(FullCols - 6))
	if !strings.Contains(got, "s sleep") {
		t.Errorf("footer = %q, want the sleep hint", got)
	}
	if strings.Contains(got, GL["ell"]) {
		t.Errorf("footer = %q, clipped at the minimum full width", got)
	}
}

func TestSleepGlyphHasASCIIFallback(t *testing.T) {
	if g := glyphs(2)["sleep"]; g != "z" {
		t.Errorf("glyphs(2)[sleep] = %q, want ASCII z", g)
	}
	if g := glyphs(1)["sleep"]; g != "☾" {
		t.Errorf("glyphs(1)[sleep] = %q, want ☾", g)
	}
}
