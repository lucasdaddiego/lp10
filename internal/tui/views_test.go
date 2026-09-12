package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The header's view strip names the five views with the one on show lit, falls
// back to bare numerals when the names do not fit, and disappears below that.
func TestViewStripAdaptsToWidth(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	m.view = viewServices
	full, w := m.viewStrip(80)
	if plain := stripANSI(full); plain != "1 player  2 equalizer  3 services  4 logs  5 diagnostics" || w != DispW(plain) {
		t.Errorf("wide strip = %q (%d)", plain, w)
	}
	nums, w := m.viewStrip(20)
	if plain := stripANSI(nums); plain != "1  2  3  4  5" || w != 13 {
		t.Errorf("narrow strip = %q (%d)", plain, w)
	}
	if s, w := m.viewStrip(5); s != "" || w != 0 {
		t.Errorf("too narrow should render nothing, got %q (%d)", s, w)
	}
	// every view's header carries the strip, and the frame stays exactly cols wide
	for _, v := range []view{viewPlayer, viewEQ, viewServices, viewLogs, viewDiag, viewHelp} {
		m.view = v
		m.rows, m.cols = 40, 120
		lines := strings.Split(m.viewContent(), "\n")
		if !strings.Contains(stripANSI(lines[1]), "1 player") {
			t.Errorf("view %d header lacks the strip: %q", v, stripANSI(lines[1]))
		}
		for i, ln := range lines {
			if w := DispW(stripANSI(ln)); w != m.cols {
				t.Errorf("view %d line %d is %d wide, want %d", v, i, w, m.cols)
				break
			}
		}
	}
}

// The tone strip is the player's one-line read-out of the equalizer: full
// words when they fit, the compact form otherwise, and a pointer to view 2
// before anything has been read.
func TestToneStripForms(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	if got := stripANSI(m.toneStrip(80)); !strings.Contains(got, "not read yet") {
		t.Errorf("unread strip = %q", got)
	}
	st.PreloadEQ(map[string]int{"EQE": 0, "EQS": 0, "TRE": 3, "MID": 0, "BAS": 3, "VBS": 1, "VBI": 15, "BAL": 0, "MXV": 100})
	st.SetEQPresets([]string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"})
	wide := stripANSI(m.toneStrip(120))
	if wide != "tone   EQ off · Flat · treble +3 · mid 0 · bass +3 · sub on 15 · balance 0 · max 100" {
		t.Errorf("wide strip = %q", wide)
	}
	narrow := stripANSI(m.toneStrip(74))
	if narrow != "tone   EQ off · Flat · T+3 M0 B+3 · sub on 15 · bal 0 · max 100" {
		t.Errorf("compact strip = %q", narrow)
	}
	for _, w := range []int{74, 52, 30} {
		if got := DispW(stripANSI(m.toneStrip(w))); got > w {
			t.Errorf("strip at %d is %d wide", w, got)
		}
	}
}

// The help page lists every view's keys and leaves by esc, q or ?.
func TestHelpViewListsKeys(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 44, 120
	m.key(kr('?'))
	out := stripANSI(m.viewContent())
	for _, want := range []string{"── views", "── player", "── equalizer", "── services", "── logs", "── diagnostics", "sleep timer", "esc · q · ? back to the player"} {
		if !strings.Contains(out, want) {
			t.Errorf("help page missing %q", want)
		}
	}
	// a playback key does nothing on the help page; q returns to the player
	if m.key(kr(' ')); m.view != viewHelp {
		t.Error("space should not leave the help page")
	}
	if m.key(kr('q')) || m.view != viewPlayer {
		t.Error("q should return to the player without quitting")
	}
}

// Playback keys reach the player from the diagnostics; the logs keep their own s.
func TestPlaybackKeysWorkAcrossViews(t *testing.T) {
	m, _, collect := makeModel(t)
	m.key(kr('5'))
	if m.view != viewDiag {
		t.Fatal("5 should open the diagnostics")
	}
	collect()
	m.key(kr(' '))
	if c := last(collect()); c.Mid != 40 {
		t.Errorf("space in the diagnostics did not toggle playback: %+v", c)
	}
	m.key(kr('4'))
	collect()
	before := m.logSrc
	m.key(kr('s'))
	if m.logSrc == before {
		t.Error("s in the logs should cycle the source, not arm the sleep timer")
	}
	if lbl, _ := m.sleepLabel(time.Now()); lbl != "" {
		t.Errorf("s in the logs armed the sleep timer: %q", lbl)
	}
}
