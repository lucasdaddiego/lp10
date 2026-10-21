package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// ---- controller: focus / quit / transport ------------------------------------

func TestQuitAndFocusKeys(t *testing.T) {
	m, _, _ := makeModel(t)
	if !m.key(kr('q')) {
		t.Error("q should quit from the player")
	}
	if m.focus != 1 {
		t.Errorf("focus = %d, want 1", m.focus)
	}
	m.key(ke(kRight))
	if m.focus != 2 {
		t.Errorf("focus = %d, want 2", m.focus)
	}
	m.key(ke(kLeft))
	m.key(ke(kLeft))
	if m.focus != 0 {
		t.Errorf("focus = %d, want 0", m.focus)
	}
	m.key(ke(kLeft))
	if m.focus != len(actions)-1 {
		t.Errorf("focus = %d, want wrap to %d", m.focus, len(actions)-1)
	}
	m.key(ke(kRight))
	if m.focus != 0 {
		t.Errorf("focus = %d, want wrap to 0", m.focus)
	}
}

// Enter presses the focused transport button, each one its own tunnel action.
func TestEnterPressesFocusedButton(t *testing.T) {
	m, _, collect := makeModel(t)
	for focus, want := range []string{"PRE", "POP", "NXT"} {
		m.focus = focus
		m.key(ke(kEnter))
		if got := wire(collect()); !slices.Equal(got, []string{want}) {
			t.Errorf("enter on %s sent %v, want [%s]", actions[focus], got, want)
		}
		if !m.flash[actions[focus]].After(time.Now()) {
			t.Errorf("enter on %s did not flash its button", actions[focus])
		}
	}
}

// The device's POP is a toggle, so the wire command is the same both ways;
// the screen flips at once and the echo hold keeps a stale PLA from undoing it.
func TestToggleIsOptimistic(t *testing.T) {
	m, st, collect := makeModel(t)
	if !st.Snap().Playing {
		t.Fatal("setup: should start playing")
	}
	m.key(kr(' '))
	if got := wire(collect()); !slices.Equal(got, []string{"POP"}) {
		t.Errorf("space sent %v, want [POP]", got)
	}
	if st.Snap().Playing {
		t.Error("playing should optimistically flip to paused")
	}
	st.ApplyPlaying(true) // a poll answered before the POP landed
	if st.Snap().Playing {
		t.Error("the echo hold should keep the stale PLA:1 from undoing the flip")
	}
	m.key(kr(' '))
	if got := wire(collect()); !slices.Equal(got, []string{"POP"}) || !st.Snap().Playing {
		t.Errorf("second space sent %v, playing=%v; want [POP] and playing again", got, st.Snap().Playing)
	}
}

// The mute is real (MUT in the MCU): the level stays where it is, the wire
// says MUT:1 then MUT:0, and the notice line names each step.
func TestMuteKeySendsTheRealMute(t *testing.T) {
	m, st, collect := makeModel(t)
	m.key(kr('m'))
	if got := wire(collect()); !slices.Equal(got, []string{"MUT:1"}) {
		t.Errorf("m sent %v, want [MUT:1]", got)
	}
	if s := st.Snap(); !s.Muted || s.Vol != 44 {
		t.Errorf("after m: muted=%v vol=%d, want muted at the unchanged 44", s.Muted, s.Vol)
	}
	if m.notice != "muted" {
		t.Errorf("notice = %q, want muted", m.notice)
	}
	st.ApplyMute(false) // a poll answered before the MUT landed: held off
	if !st.Snap().Muted {
		t.Error("the echo hold should keep a stale MUT:0 from unmuting")
	}
	m.key(kr('m'))
	if got := wire(collect()); !slices.Equal(got, []string{"MUT:0"}) {
		t.Errorf("second m sent %v, want [MUT:0]", got)
	}
	if st.Snap().Muted || m.notice != "unmuted" {
		t.Errorf("after the second m: muted=%v notice=%q", st.Snap().Muted, m.notice)
	}
}

func TestEscAndQBackOutOfAView(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.key(ke(kEsc)) || m.view != viewPlayer {
		t.Error("esc on the player should neither quit nor move")
	}
	m.key(kr('i'))
	if m.view != viewDiag {
		t.Fatal("i should open the diagnostics")
	}
	if m.key(kr('q')) {
		t.Error("q in a view returns to the player, it does not quit")
	}
	if m.view != viewPlayer {
		t.Error("q should close the diagnostics")
	}
}

// A volume key sends the new absolute level and names it on the notice line.
func TestVolumeKeysSendTheNewLevel(t *testing.T) {
	m, st, collect := makeModel(t)
	m.key(ke(kUp))
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:46"}) {
		t.Errorf("↑ from 44 sent %v, want [VOL:46]", got)
	}
	if m.notice != "volume 46%" {
		t.Errorf("notice = %q, want volume 46%%", m.notice)
	}
	m.key(kr('-'))
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:44"}) {
		t.Errorf("- sent %v, want [VOL:44]", got)
	}
	// clamped at both ends: the level never leaves 0..100
	st.SetVol(99)
	m.key(kr('+'))
	m.key(kr('+'))
	st.SetVol(1)
	m.key(ke(kDown))
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:100", "VOL:100", "VOL:0"}) {
		t.Errorf("at the ends sent %v, want [VOL:100 VOL:100 VOL:0]", got)
	}
}

// The window title rides every frame, so it is seeded before the first one.
func TestControllerInitialization(t *testing.T) {
	m := newModel(playingState(), defaultCfg(), nil)
	if want := GL["note"] + " De Música Ligera — Soda Stereo"; m.curTitle != want {
		t.Errorf("curTitle = %q, want %q", m.curTitle, want)
	}
}

// ---- display helpers --------------------------------------------------------

// SourceName names what plays: the VND word in its display spelling (the
// track's own service before the latest), and before any VND the input the
// status reports. Unknown words show as the device sent them.
func TestSourceName(t *testing.T) {
	cases := []struct {
		name string
		s    protocol.Snapshot
		want string
	}{
		{"nothing named", protocol.Snapshot{}, ""},
		{"a VND word, any case", protocol.Snapshot{Service: "SPOTIFY"}, "Spotify"},
		{"an unknown VND word shows as sent", protocol.Snapshot{Service: "deezer"}, "deezer"},
		{"the track's own service before the latest", protocol.Snapshot{Service: "tidal",
			Track: &protocol.Track{TrackName: "x", Service: "spotify"}}, "Spotify"},
		{"a track with no service falls back to the latest", protocol.Snapshot{Service: "tidal",
			Track: &protocol.Track{TrackName: "x"}}, "TIDAL"},
		{"a service beats the input", protocol.Snapshot{Service: "airplay", Source: "BT"}, "AirPlay"},
		{"an input, any case", protocol.Snapshot{Source: "net"}, "Network"},
		{"USBPLAY", protocol.Snapshot{Source: "USBPLAY"}, "USB"},
		{"an unknown input shows as sent", protocol.Snapshot{Source: "OPT"}, "OPT"},
	}
	for _, c := range cases {
		if got := SourceName(c.s); got != c.want {
			t.Errorf("%s: SourceName = %q, want %q", c.name, got, c.want)
		}
	}
	// through State: the VND push names the track it arrives with, and every
	// track after it
	st := playingState()
	st.ApplyVendor("tidal")
	if got := SourceName(st.Snap()); got != "TIDAL" {
		t.Errorf("after VND:tidal the source reads %q", got)
	}
	st.ApplyTrackField(protocol.FieldTitle, "Next")
	if s := st.Snap(); s.Track.Service != "tidal" || SourceName(s) != "TIDAL" {
		t.Errorf("the next track's service = %q (%q)", s.Track.Service, SourceName(s))
	}
}

// ---- input: coalesced multi-rune key batches --------------------------------

// A coalesced "++" (one key press whose Text carries two runes, as legacy input
// paths deliver fast typing) must raise the volume twice, not be dropped — and
// the same batch arriving as a bracketed paste behaves identically.
func TestCoalescedRunesAreNotDropped(t *testing.T) {
	m, _, collect := makeModel(t)
	m.Update(tea.KeyPressMsg{Code: '+', Text: "++"})
	if got := wire(collect()); !slices.Equal(got, []string{"VOL:46", "VOL:48"}) {
		t.Errorf("++ should step the volume twice 44->46->48, got %v", got)
	}
	// a batch containing 'q' still quits
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "xq"}); cmd == nil {
		t.Error("a batch containing q should still quit")
	}
	// pasted input drives the same dispatch
	m2, _, collect2 := makeModel(t)
	m2.Update(tea.PasteMsg{Content: "++"})
	if got := wire(collect2()); !slices.Equal(got, []string{"VOL:46", "VOL:48"}) {
		t.Errorf("pasted ++ should step the volume twice, got %v", got)
	}
	if _, cmd := m2.Update(tea.PasteMsg{Content: "q"}); cmd == nil {
		t.Error("a pasted q should quit")
	}
}

// ---- Clip: the ellipsis, East Asian width, degenerate widths ---------------

func TestClip(t *testing.T) {
	if Clip("abc", 10) != "abc" {
		t.Error("no clip when it fits")
	}
	if Clip("abcdef", 4) != "abc"+GL["ell"] {
		t.Errorf("Clip(abcdef,4) = %q", Clip("abcdef", 4))
	}
	if got := Clip("漢字漢字", 4); got != "漢"+GL["ell"] {
		t.Errorf("Clip(漢字漢字,4) = %q, want 漢%s", got, GL["ell"])
	}
	if Clip("", 5) != "" || Clip("hello", 0) != "" || Clip("hello", -1) != "" {
		t.Error("empty/zero/negative width should yield empty")
	}
	if got := Clip("abcdef", 1); got != "a" {
		t.Errorf("Clip(abcdef,1)=%q, want a (no room for ellipsis)", got)
	}
	// the CJK-locale ellipsis "..." is 3 wide: below that, a hard cut
	defer func(orig map[string]string) { GL = orig }(GL)
	GL = glyphs(2)
	if got := Clip("abcdef", 2); got != "ab" {
		t.Errorf("ASCII-ellipsis Clip(abcdef,2) = %q, want ab", got)
	}
	if got := Clip("abcdef", 5); got != "ab..." {
		t.Errorf("ASCII-ellipsis Clip(abcdef,5) = %q, want ab...", got)
	}
}

// ---- now-playing marquee ----------------------------------------------------

func TestDispWindow(t *testing.T) {
	cases := []struct {
		s      string
		off, w int
		want   string
	}{
		{"abcdef", 2, 3, "cde"},
		{"abcdef", 4, 4, "ef  "}, // past the end -> padded to w
		{"ab", 0, 5, "ab   "},    // shorter than w -> padded
		{"abcdef", 0, 0, ""},     // zero width
	}
	for _, c := range cases {
		got := dispWindow(c.s, c.off, c.w)
		if got != c.want {
			t.Errorf("dispWindow(%q,%d,%d) = %q, want %q", c.s, c.off, c.w, got, c.want)
		}
		if c.w > 0 && DispW(got) != c.w {
			t.Errorf("dispWindow(%q,%d,%d) width = %d, want exactly %d", c.s, c.off, c.w, DispW(got), c.w)
		}
	}
}

func TestMarqueeFitsAndScrolls(t *testing.T) {
	m, _, _ := makeModel(t)

	// a line that fits is returned untouched (no padding, no scroll)
	if got := m.marquee("short line", 40); got != "short line" {
		t.Errorf("fitting line changed: %q", got)
	}

	long := "A very long album title that simply will not fit in this column"

	// at scroll 0 it pauses on the head, exactly w wide
	m.scroll = 0
	head := m.marquee(long, 20)
	if DispW(head) != 20 {
		t.Fatalf("overflow window width = %d, want 20", DispW(head))
	}
	if !strings.HasPrefix(head, "A very long album ti") {
		t.Errorf("head window = %q, want the start of the title", head)
	}

	// it stays on the head through the pause window...
	m.scroll = marqueePauseCol * marqueeColTicks
	if m.marquee(long, 20) != head {
		t.Error("should still show the head during the pause")
	}

	// ...then scrolls (a different, still-exactly-w window)
	m.scroll = (marqueePauseCol + 5) * marqueeColTicks
	scrolled := m.marquee(long, 20)
	if scrolled == head {
		t.Error("should have scrolled past the head after the pause")
	}
	if DispW(scrolled) != 20 {
		t.Errorf("scrolled window width = %d, want 20", DispW(scrolled))
	}

	// and it loops back to the head after a full cycle
	strip := long + marqueeGap
	cycle := (DispW(strip) + marqueePauseCol) * marqueeColTicks
	m.scroll = cycle
	if m.marquee(long, 20) != head {
		t.Error("should loop back to the head after one full cycle")
	}
}

// Both startup problems must reach the user through State's single note slot:
// a media-key failure must APPEND to a config warning, never replace it. (The
// pty test in internal/e2e only exercises the media-key arm on a Mac that
// hasn't granted Accessibility; this pins the composition on every platform.)
func TestStartupNote(t *testing.T) {
	tapErr := errors.New("grant Accessibility")
	for _, c := range []struct {
		warn string
		err  error
		want string
	}{
		{"", nil, ""},
		{"config.toml ignored: bad", nil, "config.toml ignored: bad"},
		{"", tapErr, "media keys off — grant Accessibility"},
		{"config.toml ignored: bad", tapErr, "config.toml ignored: bad · media keys off — grant Accessibility"},
	} {
		if got := startupNote(c.warn, c.err); got != c.want {
			t.Errorf("startupNote(%q, %v) = %q, want %q", c.warn, c.err, got, c.want)
		}
	}
}
