package tui

// The player's faces under the tunnel: the tunnel names a track only when it
// changes, so the player has three connected states — a track shown, playing
// without a title yet, and idle — besides connecting. Each is drawn by the
// full dashboard, the compact one and the mini line.

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestIdle(t *testing.T) {
	track := &protocol.Track{TrackName: "x"}
	for _, c := range []struct {
		name string
		s    protocol.Snapshot
		want bool
	}{
		{"connecting", protocol.Snapshot{}, false},
		{"connecting, last seen playing", protocol.Snapshot{Playing: true}, false},
		{"connected, nothing", protocol.Snapshot{Connected: true}, true},
		{"connected, playing untitled", protocol.Snapshot{Connected: true, Playing: true}, false},
		{"connected, paused track", protocol.Snapshot{Connected: true, Track: track}, false},
		{"connected, playing track", protocol.Snapshot{Connected: true, Playing: true, Track: track}, false},
	} {
		if got := idle(c.s); got != c.want {
			t.Errorf("%s: idle = %v, want %v", c.name, got, c.want)
		}
	}
}

// A track the tunnel announced: title, artist and album on their own lines,
// the source under them, the play state and the transport; the motif in the
// art box; "Vol" over the rail.
func TestPlayerShowsTheTrack(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 40, 120
	out := clean(render(t, m))
	for _, want := range []string{"De Música Ligera", "Soda Stereo", "Canción Animal", "● Spotify", GL["play"] + " Playing", "pause", "Vol", "44%"} {
		if !strings.Contains(out, want) {
			t.Errorf("full player lacks %q:\n%s", want, out)
		}
	}
	if !m.motifLive || m.searchLive {
		t.Error("a track should draw the plasma motif")
	}
	for _, absent := range []string{untitledHint, "nothing playing", "connecting"} {
		if strings.Contains(out, absent) {
			t.Errorf("full player carries %q", absent)
		}
	}
	// compact: title then artist · album; the source in the header
	m.rows, m.cols = 20, 80
	out = clean(render(t, m))
	for _, want := range []string{"De Música Ligera", "Soda Stereo · Canción Animal", GL["play"] + " Playing", "vol", "44%"} {
		if !strings.Contains(out, want) {
			t.Errorf("compact player lacks %q:\n%s", want, out)
		}
	}
	if header := strings.Split(out, "\n")[1]; !strings.Contains(header, "Spotify") {
		t.Errorf("the compact header should name the source: %q", header)
	}
}

// Playing without a title — a run that started mid-track — says where it
// plays and that the title comes with the next track, instead of an empty
// sleeve; the motif still moves.
func TestPlayerPlayingWithoutATitle(t *testing.T) {
	m, st, _ := modelWith(untitledState())
	m.rows, m.cols = 40, 120
	out := clean(render(t, m))
	for _, want := range []string{"playing on Spotify", untitledHint, GL["play"] + " Playing", "pause"} {
		if !strings.Contains(out, want) {
			t.Errorf("full player lacks %q:\n%s", want, out)
		}
	}
	if !m.motifLive {
		t.Error("playback without a title should still draw the motif")
	}
	if strings.Contains(out, "● Spotify") {
		t.Error("no source line under a missing title: the first line names the source already")
	}
	m.rows, m.cols = 20, 80
	if out := clean(render(t, m)); !strings.Contains(out, "playing on Spotify") || !strings.Contains(out, untitledHint) {
		t.Errorf("compact player:\n%s", out)
	}
	// before any VND: the input names it; with neither, the box does
	st2 := protocol.NewState()
	connect(st2)
	st2.ApplyStatus("BT", false, 30, true)
	m2, _, _ := modelWith(st2)
	if got := stripANSI(m2.metaLines(st2.Snap(), 60)[0]); got != "playing on Bluetooth" {
		t.Errorf("BT untitled = %q", got)
	}
	if got := stripANSI(m2.metaLines(protocol.Snapshot{Connected: true, Playing: true}, 60)[0]); got != "playing on the LP10" {
		t.Errorf("unnamed untitled = %q", got)
	}
	// the next track change names it
	st.ApplyTrackField(protocol.FieldTitle, "Big Bang")
	st.ApplyTrackField(protocol.FieldArtist, "Usted Señalemelo")
	m.rows, m.cols = 40, 120
	if out := clean(render(t, m)); !strings.Contains(out, "Big Bang") || strings.Contains(out, untitledHint) {
		t.Errorf("after TIT/ART:\n%s", out)
	}
}

// Connected and idle: the clock and the wake hint, the note motif in the art
// slot where one is drawn, no transport.
func TestPlayerConnectedIdle(t *testing.T) {
	m, st, _ := modelWith(idleState())
	m.rows, m.cols = 40, 120
	out := clean(render(t, m))
	if !strings.Contains(out, "█████") || !strings.Contains(out, wakeHint) {
		t.Errorf("idle screen:\n%s", out)
	}
	if m.motifLive || m.searchLive {
		t.Error("the idle screen animates nothing")
	}
	// a paused track is not idle: the player stays, the motif frozen
	st.ApplyTrackField(protocol.FieldTitle, "Paused Song")
	out = clean(render(t, m))
	if !strings.Contains(out, "Paused Song") || !strings.Contains(out, GL["pause"]+" Paused") || !strings.Contains(out, "play") {
		t.Errorf("paused player:\n%s", out)
	}
}

// Connecting: the search figure in the art slot, the connecting copy.
func TestPlayerConnecting(t *testing.T) {
	st := protocol.NewState()
	st.StartConnection()
	m, _, _ := modelWith(st)
	m.rows, m.cols = 40, 120
	out := clean(render(t, m))
	for _, want := range []string{"connecting to LP10…", "searching for LP10", "● connecting…"} {
		if !strings.Contains(out, want) {
			t.Errorf("connecting screen lacks %q:\n%s", want, out)
		}
	}
	if !m.searchLive || m.motifLive {
		t.Error("connecting should draw the search figure")
	}
}

// The mini line names each state in one row: the error while connected and
// fresh, the track (or the source while untitled) with the volume or "muted"
// and the sleep countdown, "nothing playing" when idle, "connecting" else.
func TestMiniLineStates(t *testing.T) {
	now := time.Now()
	track := &protocol.Track{TrackName: "Song", Artist: "Band"}
	cases := []struct {
		name string
		s    protocol.Snapshot
		want string
	}{
		{"connecting", protocol.Snapshot{}, GL["note"] + " connecting to LP10…"},
		{"connecting with an error", protocol.Snapshot{Error: "no such host", ErrorAt: now}, GL["note"] + " connecting to LP10…"},
		{"idle", protocol.Snapshot{Connected: true, Vol: 30}, GL["note"] + " nothing playing"},
		{"playing a track", protocol.Snapshot{Connected: true, Playing: true, Track: track, Vol: 30}, GL["play"] + " Song — Band  30%"},
		{"paused a track", protocol.Snapshot{Connected: true, Track: track, Vol: 30}, GL["pause"] + " Song — Band  30%"},
		{"playing untitled", protocol.Snapshot{Connected: true, Playing: true, Service: "spotify", Vol: 30}, GL["play"] + " Spotify  30%"},
		{"playing unnamed", protocol.Snapshot{Connected: true, Playing: true, Vol: 30}, GL["play"] + " playing  30%"},
		{"muted", protocol.Snapshot{Connected: true, Playing: true, Track: track, Vol: 30, Muted: true}, GL["play"] + " Song — Band  muted"},
		{"a fresh error", protocol.Snapshot{Connected: true, Playing: true, Track: track, Error: "connection refused", ErrorAt: now},
			GL["warn"] + " the device refused the connection on :2018"},
		{"an aged error", protocol.Snapshot{Connected: true, Playing: true, Track: track, Vol: 30, Error: "x", ErrorAt: now.Add(-ErrorDisplayDuration)},
			GL["play"] + " Song — Band  30%"},
	}
	m, _, _ := makeModel(t)
	m.cols = 80
	for _, c := range cases {
		if got := stripANSI(m.renderMini(c.s)); got != c.want {
			t.Errorf("%s: mini = %q, want %q", c.name, got, c.want)
		}
		for _, cols := range []int{1, 10, 30} {
			m.cols = cols
			if got := visWidth(m.renderMini(c.s)); got >= max(cols, 2) {
				t.Errorf("%s at %d cols: mini is %d wide", c.name, cols, got)
			}
		}
		m.cols = 80
	}
	m.sleepAt = now.Add(20 * time.Minute)
	if got := stripANSI(m.renderMini(cases[3].s)); got != GL["play"]+" Song — Band  30%  "+GL["sleep"]+" 20m" {
		t.Errorf("mini with the timer = %q", got)
	}
}

// Through View at a mini size the frame is one bare line, and any open view
// falls back to the player.
func TestMiniFrameFallsBackToThePlayer(t *testing.T) {
	m, _, _ := makeModel(t)
	m.view = viewDiag
	m.rows, m.cols = MiniRows-1, MiniCols+20
	if out := clean(render(t, m)); out != GL["play"]+" De Música Ligera — Soda Stereo  44%" {
		t.Errorf("mini frame = %q", out)
	}
	if m.view != viewPlayer {
		t.Errorf("the mini frame left the view on %s", viewNames[m.view])
	}
	m.rows, m.cols = MiniRows+5, MiniCols-1
	render(t, m)
}

// The frame clock ticks fast only while something on screen moves: the motif
// while playing, the search figure while connecting. Paused, idle or with
// neither on screen it idles, and the logic tick never advances it.
func TestFrameTickFollowsWhatMoves(t *testing.T) {
	m, st, _ := makeModel(t)
	m.motifLive = true
	before := m.frame
	if _, cmd := m.Update(frameMsg{}); m.frame != before+1 || cmd == nil {
		t.Errorf("playing+motif: frame %d -> %d, want +1 and a reschedule", before, m.frame)
	}
	m.motifLive = false
	held := m.frame
	m.Update(frameMsg{})
	if m.frame != held {
		t.Errorf("motif not live: frame advanced to %d", m.frame)
	}
	m.motifLive = true
	st.ApplyPlaying(false)
	m.Update(frameMsg{})
	if m.frame != held {
		t.Errorf("paused: frame advanced to %d, want frozen at %d", m.frame, held)
	}
	// the search figure needs the link down
	m.searchLive = true
	m.Update(frameMsg{})
	if m.frame != held {
		t.Error("a connected search figure (a stale flag) should not tick")
	}
	st.Disconnect()
	m.Update(frameMsg{})
	if m.frame != held+1 {
		t.Errorf("connecting: frame %d, want %d", m.frame, held+1)
	}
	// the logic tick advances the marquee, never the frame
	scroll, frame := m.scroll, m.frame
	m.Update(logicMsg{})
	if m.scroll != scroll+1 || m.frame != frame {
		t.Errorf("logic tick: scroll %d->%d frame %d->%d", scroll, m.scroll, frame, m.frame)
	}
}

// The plasma is a pure function of (w, h, frame): a re-render of a still frame
// reuses the cached block, a new frame rebuilds it.
func TestMotifCache(t *testing.T) {
	m, _, _ := makeModel(t)
	a := m.motif(20, 8)
	if b := m.motif(20, 8); &a[0] != &b[0] {
		t.Error("an unchanged frame should come from the cache")
	}
	m.frame++
	if c := m.motif(20, 8); &a[0] == &c[0] {
		t.Error("a new frame should rebuild the motif")
	}
	if d := m.motif(21, 8); len(d) != 8 || visWidth(d[0]) != 21 {
		t.Errorf("a resized motif = %d rows of %d", len(d), visWidth(d[0]))
	}
}

// TestUntitledTrackFallsBack: a track the device announced with no title and
// no artist reads as its album, and with nothing at all the mini line names
// the source rather than an empty slot.
func TestUntitledTrackFallsBack(t *testing.T) {
	if got := trackTitle(&protocol.Track{Album: "Big Bang"}); got != "Big Bang" {
		t.Errorf("trackTitle(album only) = %q", got)
	}
	if got := trackTitle(&protocol.Track{}); got != "" {
		t.Errorf("trackTitle(empty) = %q, want empty", got)
	}
	if got := trackTitleOf(nil); got != "" {
		t.Errorf("trackTitleOf(nil) = %q", got)
	}
}
