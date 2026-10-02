package tui

import (
	"slices"
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
	if want := "sleep timer set · " + GL["sleep"] + " 15m"; m.notice != want {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("arming must send nothing to the device, sent %v", wire(got))
	}
	m.key(kr('S'))
	if !m.sleepAt.IsZero() || m.notice != "sleep timer cancelled" {
		t.Errorf("S should cancel the timer: at=%v notice=%q", m.sleepAt, m.notice)
	}
	// S with nothing armed is inert
	m.key(kr('S'))
	if !m.sleepAt.IsZero() || m.sleepPreset != 0 {
		t.Error("S on an idle timer should stay off")
	}
	// the keys work from the other views too (global rune keys)
	for _, v := range []view{viewEQ, viewDiag, viewHelp} {
		m.sleepCancel()
		m.setView(v)
		m.key(kr('s'))
		if m.sleepAt.IsZero() {
			t.Errorf("s should arm from the %s view", viewNames[v])
		}
	}
	// stepping past the last preset says it is off
	m.sleepCancel()
	for range len(sleepPresets) + 1 {
		m.key(kr('s'))
	}
	if !m.sleepAt.IsZero() || m.notice != "sleep timer off" {
		t.Errorf("past the last preset: at=%v notice=%q", m.sleepAt, m.notice)
	}
}

// ---- sleep timer: firing ------------------------------------------------------

// At the deadline the timer pauses once — a POP, since that is the device's
// play/pause — flips the screen at once and disarms.
func TestSleepFiresPauseOnceWhilePlaying(t *testing.T) {
	m, st, collect := makeModel(t)
	m.sleepAt = time.Now().Add(-time.Second) // deadline already passed
	m.dispatch(logicMsg{})
	if got := wire(collect()); !slices.Equal(got, []string{"POP"}) {
		t.Fatalf("sent %v, want [POP]", got)
	}
	s := st.Snap()
	if s.Playing {
		t.Error("playing should optimistically flip to paused")
	}
	if s.Error != "" {
		t.Errorf("firing must not post a note (it renders as the red error line), got %q", s.Error)
	}
	if !m.sleepAt.IsZero() {
		t.Error("the timer must disarm after firing")
	}
	if !m.flash["toggle"].After(time.Now()) {
		t.Error("the fire should flash the play/pause button like the space bar")
	}
	// one-shot: the next tick sends nothing (and never a second POP that would resume)
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Errorf("second tick sent %v, want nothing", wire(got))
	}
}

func TestSleepDoesNotFireBeforeDeadline(t *testing.T) {
	m, st, collect := makeModel(t)
	m.sleepAt = time.Now().Add(time.Hour)
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Errorf("sent %v before the deadline", wire(got))
	}
	if m.sleepAt.IsZero() || !st.Snap().Playing {
		t.Error("an unexpired timer must stay armed and leave playback alone")
	}
}

// POP is a toggle: already paused (or idle) at the deadline, the timer must
// disarm quietly — a POP there would RESUME the room.
func TestSleepNeverResumes(t *testing.T) {
	m, st, collect := makeModel(t)
	st.ApplyPlaying(false) // the device paused
	m.sleepAt = time.Now().Add(-time.Second)
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Errorf("paused at the deadline: sent %v, want nothing", wire(got))
	}
	if !m.sleepAt.IsZero() || st.Snap().Playing {
		t.Error("the timer should disarm and leave the pause alone")
	}

	// idle (connected, no track) at the deadline
	m2, _, collect2 := modelWith(idleState())
	m2.sleepAt = time.Now().Add(-time.Second)
	m2.dispatch(logicMsg{})
	if got := collect2(); len(got) != 0 {
		t.Errorf("idle at the deadline: sent %v, want nothing", wire(got))
	}
	if !m2.sleepAt.IsZero() {
		t.Error("an idle deadline should disarm")
	}
}

// Playing with no track metadata (a run that started mid-track) is still
// playing: the timer pauses it rather than shrugging.
func TestSleepFiresPauseWithoutTrackMetadata(t *testing.T) {
	m, st, collect := modelWith(untitledState())
	m.sleepAt = time.Now().Add(-time.Second)
	m.dispatch(logicMsg{})
	if got := wire(collect()); !slices.Equal(got, []string{"POP"}) {
		t.Fatalf("sent %v, want [POP]", got)
	}
	if st.Snap().Playing || !m.sleepAt.IsZero() {
		t.Error("must flip to paused and disarm")
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
	// the final minute switches the countdown to the warn pen
	m.sleepAt = now.Add(30 * time.Second)
	final := m.headerRow(st.Snap(), now, W, true)
	if !strings.Contains(stripANSI(final), GL["sleep"]+" 30s") || !strings.Contains(final, m.sty.pens().warn.render(GL["sleep"]+" 30s")) {
		t.Errorf("final-minute header = %q, want the countdown in the warn pen", stripANSI(final))
	}
	// disconnected: rides after the reconnecting status without breaking width
	m.sleepAt = now.Add(30 * time.Minute)
	st.Disconnect()
	styled := m.headerRow(st.Snap(), now, W, true)
	if plain := stripANSI(styled); !strings.Contains(plain, "connecting") || !strings.Contains(plain, GL["sleep"]) {
		t.Errorf("disconnected header = %q, want status + countdown", plain)
	}
	if got := visWidth(styled); got != W {
		t.Errorf("disconnected header width = %d, want %d", got, W)
	}
}

// The countdown reaches every face of the player: the mini line, and under
// the idle screen's clock.
func TestSleepShowsOnMiniLineAndIdleScreen(t *testing.T) {
	m, st, _ := makeModel(t)
	m.rows, m.cols = MiniRows-1, 120
	m.sleepAt = time.Now().Add(45 * time.Minute)
	if got := stripANSI(render(t, m)); !strings.Contains(got, GL["sleep"]+" 45m") {
		t.Errorf("mini line = %q, want the countdown", got)
	}
	m.sleepCancel()
	if got := stripANSI(m.renderMini(st.Snap())); strings.Contains(got, GL["sleep"]) {
		t.Errorf("mini line = %q, timer off but still shown", got)
	}
	mi, _, _ := modelWith(idleState())
	mi.rows, mi.cols = 40, 120
	mi.sleepAt = time.Now().Add(45 * time.Minute)
	out := clean(render(t, mi))
	if n := strings.Count(out, GL["sleep"]+" 45m"); n != 2 {
		t.Errorf("idle screen shows the countdown %d times, want 2 (header + under the clock):\n%s", n, out)
	}
}

// The player hint advertises the key and still fits the full dashboard's
// narrowest content width unclipped.
func TestSleepFooterHintFitsMinimumWidth(t *testing.T) {
	m, _, _ := makeModel(t)
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

// With the tunnel down at the deadline the timer stays armed: a POP queued
// into a dead link would expire unheard while the room plays on. It fires as
// soon as the link is back.
func TestSleepWaitsForTheLinkThenFires(t *testing.T) {
	m, st, collect := makeModel(t)
	st.Disconnect()
	m.sleepAt = time.Now().Add(-time.Second)
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Fatalf("fired into a dead link: sent %v", wire(got))
	}
	if m.sleepAt.IsZero() {
		t.Fatal("the timer must stay armed while the link is down")
	}
	connect(st) // the link is back, still playing
	st.ApplyStatus("NET", false, 44, true)
	m.dispatch(logicMsg{})
	if got := wire(collect()); !slices.Equal(got, []string{"POP"}) {
		t.Fatalf("after reconnect sent %v, want [POP]", got)
	}
	if !m.sleepAt.IsZero() {
		t.Error("the timer must disarm once it has fired")
	}
}

// The timer that ran out during an outage fires on the reconnect only on a
// play state the new link reported. Until then State holds the one from
// before the outage, and any recognised frame marks the link live: a track
// push in the read that carried the seed's STA reply drops that STA (the
// reader's track-read guard), so the tick sees Connected with the stale
// "playing". If the room was paused meanwhile (the phone, the box's button),
// a POP then is a toggle that resumes it.
func TestSleepWaitsForAPlayStateOnTheNewLink(t *testing.T) {
	m, st, collect := makeModel(t)
	st.Disconnect()
	m.sleepAt = time.Now().Add(-time.Second)
	m.dispatch(logicMsg{})
	connect(st) // a frame marks the link live; no play state on it yet
	st.ApplyTrackField(protocol.FieldTitle, "Persiana Americana")
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Fatalf("fired on the play state from before the outage: sent %v", wire(got))
	}
	if m.sleepAt.IsZero() {
		t.Fatal("the timer must stay armed until the new link reports a play state")
	}
	st.ApplyStatus("NET", false, 44, false) // the room was paused during the outage
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 || !m.sleepAt.IsZero() || st.Snap().Playing {
		t.Errorf("paused room: sent %v, armed %v, playing %v; want nothing sent, disarmed", wire(got), !m.sleepAt.IsZero(), st.Snap().Playing)
	}
}

// The device paused on its own between the tick's snapshot and the fire: the
// decision is State's, under its lock, so the stale snapshot cannot turn the
// timer's POP into a resume.
func TestSleepFireDecidesUnderTheLock(t *testing.T) {
	m, st, collect := makeModel(t)
	snap := st.Snap() // playing, as the tick saw it
	st.ApplyPlaying(false)
	m.sleepAt = time.Now().Add(-time.Second)
	m.sleepFire(time.Now(), snap)
	if got := collect(); len(got) != 0 {
		t.Errorf("a pause that landed after the snapshot still sent %v", wire(got))
	}
	if st.Snap().Playing {
		t.Error("the timer resumed a paused room")
	}
}
