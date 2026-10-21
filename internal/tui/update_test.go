// The message loop (update.go): the View, the window title and the volume
// keys.

package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The View carries the alt screen and the window title the logic tick keeps
// current.
func TestViewCarriesTheWindowTitle(t *testing.T) {
	m, _, _ := makeModel(t)
	m.curTitle = ""
	m.Update(logicMsg{})
	if m.curTitle == "" {
		t.Error("logicMsg should recompute the window title")
	}
	v := m.View()
	if !v.AltScreen || v.WindowTitle != m.curTitle {
		t.Errorf("View: alt=%v title=%q, want the alt screen and %q", v.AltScreen, v.WindowTitle, m.curTitle)
	}
}

// The window title: "♪ Name — Artist" for a track, the name alone without an
// artist, the device name while idle, playing untitled or on a track with no
// usable metadata; control runes filtered, over-long names capped.
func TestWindowTitle(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.cfg.Name = "ab\x01cd"
	if got := m.computeTitle(m.st.Snap()); got != "abcd" {
		t.Errorf("computeTitle(idle, ctrl char) = %q, want abcd", got)
	}
	m.cfg.Name = strings.Repeat("x", MaxTitleLength+10)
	if got := m.computeTitle(m.st.Snap()); len([]rune(got)) != MaxTitleLength {
		t.Errorf("computeTitle long name len = %d, want %d", len([]rune(got)), MaxTitleLength)
	}
	mp, _, _ := makeModel(t)
	if got := mp.computeTitle(mp.st.Snap()); got != GL["note"]+" De Música Ligera — Soda Stereo" {
		t.Errorf("computeTitle(track) = %q", got)
	}
	mu, _, _ := modelWith(untitledState())
	if got := mu.computeTitle(mu.st.Snap()); got != defaultCfg().Name {
		t.Errorf("computeTitle(untitled) = %q, want the device name", got)
	}
	s := mu.st.Snap()
	s.Track = &protocol.Track{TrackName: "Solo"}
	if got := mu.computeTitle(s); got != GL["note"]+" Solo" {
		t.Errorf("artist-less title = %q, want %q", got, GL["note"]+" Solo")
	}
	s.Track = &protocol.Track{}
	if got := mu.computeTitle(s); got != defaultCfg().Name {
		t.Errorf("empty-track title = %q, want the device name", got)
	}
}

// Volume keys before the device's first volume read of the run computed from
// the cached snapshot: cached 40, the phone set 70 meanwhile, ↑ during
// "connecting…" sent VOL:42 and the room dropped from 70 to 42 on connect. The
// mute waits on the same read (the status reply carries both).
func TestVolumeKeysWaitForTheLiveVolume(t *testing.T) {
	st := protocol.NewState()
	st.Preload(40) // the last run's snapshot
	m, _, collect := modelWith(st)
	for _, ev := range []keyEvent{ke(kUp), ke(kDown), kr('+'), kr('-'), kr('m')} {
		m.key(ev)
	}
	m.do("volup")
	if got := collect(); len(got) != 0 {
		t.Fatalf("sent %v before the device reported its volume", wire(got))
	}
	if s := st.Snap(); s.Vol != 40 || s.Muted {
		t.Errorf("the cached volume moved to %d (muted %v)", s.Vol, s.Muted)
	}
	if !strings.Contains(m.notice, "volume not read yet") {
		t.Errorf("notice = %q, want why the key did nothing", m.notice)
	}
	if !m.flash["mute"].IsZero() || !m.flash["volup"].IsZero() {
		t.Error("a refused key flashed its button")
	}
	// the live read: the phone's 70 (a connected tunnel, first status reply)
	connect(st)
	st.ApplyStatus("NET", false, 70, true)
	m.key(ke(kUp))
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:72"}) {
		t.Errorf("↑ after the live read sent %v, want [VOL:72]", got)
	}
	m.key(kr('m'))
	if got := wire(collect()); !slices.Equal(got, []string{"MUT:1"}) {
		t.Errorf("m after the live read sent %v, want [MUT:1]", got)
	}
	// a later outage refuses the keys, like the equalizer: the write would
	// wait in the queue until the worker dropped it, with the rail moved
	m.dispatch(logicMsg{})
	st.Disconnect()
	for _, ev := range []keyEvent{kr('-'), ke(kUp), kr('m')} {
		m.key(ev)
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("keys during an outage sent %v, want nothing", wire(got))
	}
	if s := st.Snap(); s.Vol != 72 || !s.Muted {
		t.Errorf("the rail moved during an outage: %d (muted %v), want 72 (muted true) as before", s.Vol, s.Muted)
	}
	if !strings.Contains(m.notice, "volume read-only") || !m.noticeWarn {
		t.Errorf("notice = %q (warn=%v), want the read-only warning", m.notice, m.noticeWarn)
	}
	// the tunnel back, the same key goes through
	connect(st)
	m.key(kr('-'))
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:70"}) {
		t.Errorf("- with the tunnel back sent %v, want [VOL:70]", got)
	}
	// a VOL push alone (no status) makes the level live too
	st2 := protocol.NewState()
	st2.Preload(40)
	m2, _, collect2 := modelWith(st2)
	connect(st2)
	st2.ApplyVolume(55)
	m2.key(kr('+'))
	if got := wire(collect2()); !slices.Equal(got, []string{"VOL:57"}) {
		t.Errorf("+ after a VOL push sent %v, want [VOL:57]", got)
	}
}
