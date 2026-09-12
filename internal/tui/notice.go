// The notice line: one row under the header, in every view, where transient
// events print for a moment and fade — a volume step, the sleep timer, night
// mode, an engine switch, the connection coming and going, and the summary
// that greets a connect. One place to look, and no layout shifts: the row is
// always there, blank when there is nothing to say.

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

const (
	noticeFor        = 2 * time.Second // an ordinary event
	noticeForSummary = 5 * time.Second // the connect summary: more to read
	// startupSummaryWait bounds how long a fresh connection waits for the
	// one-shot facts (@@c, @@d, @@o) before the summary prints with what it has.
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
	d := m.st.DiagnosticView(now)
	// the facts a connect ships once: wait for the capability block unless the
	// deadline passes first
	if d.ConfInfo == nil && now.Before(m.summaryDue) {
		return
	}
	m.summaryDue = time.Time{}
	m.notify(startupSummary(d), noticeForSummary)
}

// startupSummary is the connect greeting: "connected · firmware AR241CE_8530.23.2
// · Pro engine · 88 reconnects in the log" — whatever of it is known.
func startupSummary(d protocol.DiagnosticSnapshot) string {
	parts := []string{"connected"}
	id := collectIdentity(d.SysInfo, d.DevInfo, d.Details)
	if id.fw != "" && id.fw != "—" {
		parts = append(parts, "firmware "+id.fw)
	}
	switch d.ConfInfo.Engine() {
	case "spotifymusicpro":
		parts = append(parts, "Pro engine")
	case "newspotifyhifi":
		parts = append(parts, "HiFi engine")
	case "":
		if d.ConfInfo != nil {
			parts = append(parts, "no Spotify engine running")
		}
	}
	if d.Ops != nil && d.Ops.ReconnectsOK {
		parts = append(parts, fmt.Sprintf("%d reconnect%s in the log", d.Ops.Reconnects, plural(d.Ops.Reconnects)))
	}
	if d.DevInfo != nil && d.DevInfo.Reboot == "cold_boot" && d.DevInfo.Net != "" {
		parts = append(parts, "last boot was a power-on")
	}
	return strings.Join(parts, " · ")
}
