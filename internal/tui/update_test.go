// The message loop (update.go): Update, Init, the command queue, the window
// title and the volume keys.

package tui

import (
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestCov_UpdateMessages(t *testing.T) {
	// WindowSizeMsg sets rows/cols
	m, _, _ := makeModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.cols != 100 || m.rows != 40 {
		t.Errorf("WindowSizeMsg: %dx%d, want 100x40", m.cols, m.rows)
	}

	// logicMsg advances the marquee, refreshes the title and reschedules
	scroll := m.scroll
	m.curTitle = ""
	if _, cmd := m.Update(logicMsg{}); cmd == nil || m.scroll != scroll+1 {
		t.Error("logicMsg should advance scroll and reschedule")
	}
	if m.curTitle == "" {
		t.Error("logicMsg should recompute the window title")
	}

	// frameMsg with the search figure live while disconnected advances the frame
	d, _, _ := modelWith(protocol.NewState())
	d.searchLive = true
	f := d.frame
	if _, cmd := d.Update(frameMsg{}); cmd == nil {
		t.Error("frameMsg should reschedule")
	}
	if d.frame != f+1 {
		t.Errorf("search frame %d -> %d, want +1", f, d.frame)
	}

	// a bracketed paste drives the hotkeys ('2' opens the equalizer)
	if _, _ = m.Update(tea.PasteMsg{Content: "2"}); m.view != viewEQ {
		t.Error("PasteMsg should dispatch its runes as keys")
	}

	// Ctrl-C marks interrupted and quits
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil || !m.interrupted {
		t.Error("ctrl-c should set interrupted and quit")
	}

	// a plain key press that does not quit returns no command
	if _, cmd := m.Update(tea.KeyPressMsg{Code: '1', Text: "1"}); cmd != nil {
		t.Error("a view key should return no command")
	}

	// the View carries the alt screen and the window title
	v := m.View()
	if !v.AltScreen || v.WindowTitle != m.curTitle {
		t.Errorf("View: alt=%v title=%q, want the alt screen and %q", v.AltScreen, v.WindowTitle, m.curTitle)
	}
}

// The terminal's background answer picks the palette (theme = auto).
func TestCov_UpdateBackgroundColor(t *testing.T) {
	m, _, _ := makeModel(t)
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.bgDark == nil || *m.bgDark || m.themeDark {
		t.Error("a white background should select the light palette")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if !m.themeDark {
		t.Error("a black background should select the dark palette again")
	}
}

func TestCov_computeTitle(t *testing.T) {
	// idle -> the device name, with non-printable runes filtered out
	m, _, _ := modelWith(protocol.NewState())
	m.cfg.Name = "ab\x01cd"
	if got := m.computeTitle(m.st.Snap()); got != "abcd" {
		t.Errorf("computeTitle(idle, ctrl char) = %q, want abcd", got)
	}
	// over-long names are capped at MaxTitleLength
	m.cfg.Name = strings.Repeat("x", MaxTitleLength+10)
	if got := m.computeTitle(m.st.Snap()); len([]rune(got)) != MaxTitleLength {
		t.Errorf("computeTitle long name len = %d, want %d", len([]rune(got)), MaxTitleLength)
	}
	// a track title is "♪ Name — Artist"
	mp, _, _ := makeModel(t)
	if got := mp.computeTitle(mp.st.Snap()); got != GL["note"]+" De Música Ligera — Soda Stereo" {
		t.Errorf("computeTitle(track) = %q", got)
	}
	// playing without a title: the device name, not a guess
	mu, _, _ := modelWith(untitledState())
	if got := mu.computeTitle(mu.st.Snap()); got != defaultCfg().Name {
		t.Errorf("computeTitle(untitled) = %q, want the device name", got)
	}
}

func TestCov_InitTicksCellPixel(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.Init() == nil {
		t.Error("Init should return a batched command")
	}
	m.cfg.Theme = "dark" // a fixed theme asks the terminal nothing
	if m.Init() == nil {
		t.Error("Init with a fixed theme still starts the ticks")
	}
	// the tick commands sleep their interval then yield the right message type
	if _, ok := logicTick()().(logicMsg); !ok {
		t.Error("logicTick cmd should yield a logicMsg")
	}
	if _, ok := frameTick(time.Millisecond)().(frameMsg); !ok {
		t.Error("frameTick cmd should yield a frameMsg")
	}
	// cellPixelSize: no tty in tests -> (0,0); just exercise it without panicking
	if w, h := cellPixelSize(); w != 0 || h != 0 {
		t.Logf("cellPixelSize returned %dx%d (a real tty?)", w, h)
	}
}

func TestCov_nbSendDropOldest(t *testing.T) {
	// a full buffer drops the oldest queued item and retries (the non-blocking path)
	ch := make(chan int, 1)
	nbSend(ch, 1)
	nbSend(ch, 2) // full -> drop 1, enqueue 2
	if got := <-ch; got != 2 {
		t.Errorf("nbSend drop-oldest = %d, want 2", got)
	}
	// an unbuffered channel nobody reads never blocks the caller
	nbSend(make(chan int), 3)
}

func TestCov_UpdateUnknownAndViewZero(t *testing.T) {
	m, _, _ := makeModel(t)
	// an unrecognized message type falls through to (m, nil)
	if _, cmd := m.Update(struct{ unknownMsg int }{}); cmd != nil {
		t.Error("unknown msg should return no command")
	}
	// a 0-sized window renders nothing
	m.rows, m.cols = 0, 80
	if m.viewContent() != "" {
		t.Error("View at 0 rows should be empty")
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
