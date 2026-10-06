// The notice line: one row under the header, in every view, where transient
// events print for a moment and fade — a volume step, the mute, the sleep
// timer, the connection coming and going, and the summary that greets a
// connect. One place to look, and no layout shifts: the row is
// always there, blank when there is nothing to say.

package tui

import (
	"strings"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

const (
	noticeFor        = 2 * time.Second // an ordinary event
	noticeForSummary = 5 * time.Second // the connect summary: more to read
	// startupSummaryWait bounds how long a fresh connection waits for the
	// facts it names (the MCU build, the ZeroConf answer) before the summary
	// prints with what it has.
	startupSummaryWait = 3 * time.Second
)

// notify shows text on the notice line for d.
func (m *model) notify(text string, d time.Duration) {
	m.notice, m.noticeUntil, m.noticeWarn = text, time.Now().Add(d), false
}

// notifyWarn is notify in the warning pen (a lost connection, a failed step).
func (m *model) notifyWarn(text string, d time.Duration) {
	m.notify(text, d)
	m.noticeWarn = true
}

// noticeRow renders the notice line: the text while it lasts, else blank.
func (m *model) noticeRow(now time.Time, W int) string {
	if m.notice == "" || !now.Before(m.noticeUntil) {
		return ""
	}
	ps := m.sty.pens()
	pen := ps.dim
	if m.noticeWarn {
		pen = ps.warn
	}
	return pen.render(Clip(m.notice, W))
}

// trackConnection runs on every logic tick: it notices the connection coming
// and going, and prints the startup summary once the connect's one-shot facts
// have arrived (or the wait runs out).
func (m *model) trackConnection(s protocol.Snapshot, now time.Time) {
	if s.Connected != m.wasConnected {
		m.wasConnected = s.Connected
		if s.Connected {
			m.summaryDue = now.Add(startupSummaryWait)
		} else if !m.connectedOnce {
			// never connected this run: the header already says "connecting…"
		} else {
			m.summaryDue = time.Time{}
			m.notifyWarn("connection lost · reconnecting…", noticeFor*2)
		}
		if s.Connected {
			m.connectedOnce = true
		}
	}
	if m.summaryDue.IsZero() || !s.Connected {
		return
	}
	d := m.st.DiagnosticView()
	// wait for the facts the summary names unless the deadline passes first
	if (d.MCU == "" || d.SpotifyZC == nil) && now.Before(m.summaryDue) {
		return
	}
	m.summaryDue = time.Time{}
	m.notify(startupSummary(d), noticeForSummary)
}

// startupSummary is the connect greeting: "connected · firmware
// AR241CP_8747.29.2 · MCU 29 · Spotify eSDK 3.216.31" — whatever of it is
// known. The firmware is the LSSDP answer's, the MCU build the tunnel's VER,
// the eSDK the Spotify engine's ZeroConf answer.
func startupSummary(d protocol.DiagnosticSnapshot) string {
	parts := []string{"connected"}
	if d.LSSDP != nil && d.LSSDP.FW != "" {
		parts = append(parts, "firmware "+d.LSSDP.FW)
	}
	if mcu := protocol.Before(d.MCU, "-"); mcu != "" {
		parts = append(parts, "MCU "+mcu)
	}
	if d.SpotifyZC != nil && d.SpotifyZC.LibraryVersion != "" {
		parts = append(parts, "Spotify eSDK "+protocol.Before(d.SpotifyZC.LibraryVersion, "-"))
	}
	return strings.Join(parts, " · ")
}
