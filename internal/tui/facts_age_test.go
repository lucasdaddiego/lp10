package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The one-shot facts are read at a known time: the engine's age keeps
// counting from its read, and the reconnect rate stays the rate of the window
// that was read — five hours on, the same reading reads the same.
func TestServicesPaneFactsAgeWithTheirRead(t *testing.T) {
	st := protocol.NewState()
	read := time.Now()
	protocol.ApplyRecord(st, protocol.Record{
		"c": {"spotify.eng=spotifymusicpro", "spotify.cfg=pro", "spotify.proc=3600 1"},
		"o": {"n=10", "t=" + read.Add(-time.Hour).Format("Jan _2 15:04:05")},
	})
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.sty = newTheme()
	m.setView(viewServices)
	now := clean(strings.Join(m.renderServices(read, 120), "\n"))
	later := clean(strings.Join(m.renderServices(read.Add(5*time.Hour), 120), "\n"))
	if a, b := lineWith(now, "started"), lineWith(later, "started"); a == "" || a == b {
		t.Errorf("engine age did not advance in 5 h:\n  at read: %q\n  +5 h:    %q", a, b)
	}
	if a, b := lineWith(now, "link"), lineWith(later, "link"); a == "" || a != b {
		t.Errorf("the same reading reads differently 5 h later:\n  at read: %q\n  +5 h:    %q", a, b)
	}
}
