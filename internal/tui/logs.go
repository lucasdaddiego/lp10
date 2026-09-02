// The logs pane ('l'): the tail of one of the device's own logs.
//
// This is the view that answers "the switch did nothing, why". The LP10's init
// scripts announce their decisions to syslog and nowhere else — "Spotify_hifi
// not enabled", "tidal-connect: Tidal is not Enabled", "Do not launch X as
// device in WAC mode" — so a service that quietly refuses to start leaves its
// reason here and only here. Nothing else on the box will tell you: the web
// config page reports flags, not outcomes.
//
// (The file is /var/log/syslog/messages.log, not /var/log/messages — the box
// touches the latter at boot and then never writes to it.)
//
// Since firmware 8530 / vendor app v32 there is a second narrator: the Rust
// rakoit_app writes /lsync/app.log, where every :2018 tunnel frame and the
// MCU's reply, every preset (favourite) action and every PlayView publish is
// recorded — the place to look when the equalizer or a preset key "did
// nothing". 's' switches between the two tails; each is fetched on first view
// and kept until refreshed.
//
// The log is fetched on demand rather than streamed. It is a request/response
// on the same single ssh connection the rest of the app rides (MID 93 out, an
// @@l / @@L section back), which keeps the per-tick cost at exactly zero while
// the pane is closed — the same bargain the diagnostics overlay makes with @@s.

package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// logFilters are views over the ONE tail the device sends, in the order 'f'
// steps through them. Filtering here rather than on the device costs nothing on
// the wire and makes switching instant — the alternative was a second round trip
// per keystroke, and the device loop has no command-length budget to spare.
// (The one filter that does live on the device is dropping luci_service, which
// is most of the file and none of it about services.)
var logFilters = []struct {
	label string
	keep  func(sev byte, ok bool) bool
}{
	{"all", func(byte, bool) bool { return true }},
	{"errors + warnings", func(sev byte, ok bool) bool {
		return ok && (sev == 'E' || sev == 'W' || sev == 'F' || sev == 'N')
	}},
}

// logSources are the two device-side tails, in the order 's' steps through
// them. The wire value is the MID-93 payload (protocol.LogSource), so the loop
// and this pane can never disagree about which file "2" means.
var logSources = []struct {
	label string
	src   protocol.LogSource
}{
	{"device log", protocol.LogSyslog},
	{"vendor app log", protocol.LogVendor},
}

// logRequest asks the device for a fresh tail of the selected source; the
// answer replaces the held one for that source only.
func (m *model) logRequest() {
	m.send(93, strconv.Itoa(int(logSources[m.logSrc].src)))
	m.logAsked[m.logSrc] = true
}

// logCycleFilter steps the view. No fetch: the same tail is simply shown through
// a different sieve, so the pane answers the keystroke on the next frame.
func (m *model) logCycleFilter() {
	m.logFilter = (m.logFilter + 1) % len(logFilters)
	m.logScroll = 0
}

// logCycleSource switches between the device syslog and the vendor app's log.
// A source seen before shows its held tail at once; a first visit costs the
// one round trip, exactly like opening the pane did.
func (m *model) logCycleSource() {
	m.logSrc = (m.logSrc + 1) % len(logSources)
	m.logScroll = 0
	if !m.logAsked[m.logSrc] {
		m.logRequest()
	}
}

// logVisible is the held tail of the selected source through the current filter.
func (m *model) logVisible() ([]string, time.Time) {
	lines, at := m.st.LogView(logSources[m.logSrc].src)
	keep := logFilters[m.logFilter].keep
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if _, sev, _, ok := parseLogLine(ln); keep(sev, ok) {
			out = append(out, ln)
		}
	}
	return out, at
}

// logScrollBy moves the viewport. The offset counts lines UP from the bottom,
// because a log is read from its tail: 0 pins to the newest line and stays there
// when a refresh brings more.
func (m *model) logScrollBy(d, page int) {
	lines, _ := m.logVisible()
	m.logScroll += d
	if hi := len(lines) - page; m.logScroll > hi {
		m.logScroll = hi
	}
	if m.logScroll < 0 {
		m.logScroll = 0
	}
}

// logSeverity extracts the syslog priority letter the device stamps between the
// timestamp and the tag ("… I/luci_service[284]: …"), returning the letter and
// the byte offset where the tag begins. ok is false for a line that doesn't
// carry the shape at all, which then renders undecorated rather than mangled.
func logSeverity(line string) (sev byte, tagStart int, ok bool) {
	for i := 2; i+1 < len(line); i++ {
		if line[i] != '/' {
			continue
		}
		c := line[i-1]
		if (c == 'E' || c == 'W' || c == 'I' || c == 'D' || c == 'N' || c == 'F') && line[i-2] == ' ' {
			return c, i + 1, true
		}
	}
	return 0, 0, false
}

// vendorLevels maps the vendor app's bracketed level onto the syslog letters
// the filters and pens already understand, so one severity vocabulary serves
// both tails. Unknown levels stay undecorated.
var vendorLevels = map[string]byte{
	"TRACE": 'D', "DEBUG": 'D', "INFO": 'I', "WARN": 'W', "WARNING": 'W', "ERROR": 'E', "FATAL": 'F',
}

// parseLogLine splits one line of either tail into its clock, severity letter
// and the rest (tag + message). Two shapes are known:
//
//	syslog:  "Aug 25 15:28:39:871948 I/tag[pid]: msg"      → "15:28:39", 'I', "tag[pid]: msg"
//	vendor:  "[2026-09-02 00:06:26.775] [DEBUG] [luci-rx] msg" → "00:06:26", 'D', "[luci-rx] msg"
//
// ok is false for anything else; such a line renders whole and undecorated.
func parseLogLine(line string) (clock string, sev byte, rest string, ok bool) {
	if strings.HasPrefix(line, "[") {
		end := strings.IndexByte(line, ']')
		if end < 0 {
			return "", 0, "", false
		}
		stamp := strings.TrimSpace(line[1:end])
		if i := strings.IndexByte(stamp, ' '); i >= 0 {
			stamp = stamp[i+1:] // drop the date; every tail is one day
		}
		if len(stamp) > 8 {
			stamp = stamp[:8] // drop the milliseconds
		}
		rest = strings.TrimSpace(line[end+1:])
		if !strings.HasPrefix(rest, "[") {
			return "", 0, "", false
		}
		lend := strings.IndexByte(rest, ']')
		if lend < 0 {
			return "", 0, "", false
		}
		sev, known := vendorLevels[strings.ToUpper(strings.TrimSpace(rest[1:lend]))]
		if !known {
			return "", 0, "", false
		}
		return stamp, sev, strings.TrimSpace(rest[lend+1:]), true
	}
	sev, tagStart, ok := logSeverity(line)
	if !ok {
		return "", 0, "", false
	}
	// "Aug 25 15:28:39:871948 I/tag: msg" -> fields[2] is the clock+usec
	if f := strings.Fields(line); len(f) >= 3 {
		clock = f[2]
		if i := strings.LastIndexByte(clock, ':'); i == 8 {
			clock = clock[:i] // drop ":871948"
		}
	}
	return clock, sev, strings.TrimSpace(line[tagStart:]), true
}

// logPen picks the row colour from the priority letter: errors and fatals in the
// fault hue, warnings in the warn hue, everything else muted. The severity is
// the only part of a log line worth spending colour on — it is what the eye
// scans for.
func (m *model) logPen(sev byte) func(string) string {
	switch sev {
	case 'E', 'F':
		return func(s string) string { return m.sty.sevs[2].Render(s) }
	case 'W', 'N':
		return func(s string) string { return m.sty.sevs[1].Render(s) }
	}
	return m.sty.pens().dmr.render
}

// logRow renders one line: the clock time, then the tag and message in the
// severity's colour. The date and the sub-second field are dropped — every line
// in a tail is from the same day, and six digits of precision earn nothing here.
func (m *model) logRow(line string, w int) string {
	t := m.sty.pens()
	clock, sev, rest, ok := parseLogLine(line)
	if !ok {
		return clipStyled(t.dmr.render(Clip(line, w)), w)
	}
	row := t.dim.render(padVis(clock, 9)) + m.logPen(sev)(Clip(rest, w-9))
	return clipStyled(row, w)
}

// renderLogs draws the pane: a heading carrying the source, the filter and the
// age of the tail, then the viewport. Before the first answer it says it is
// waiting rather than showing an empty box that reads as "no logs".
func (m *model) renderLogs(now time.Time, W int) []string {
	t := m.sty.pens()
	lines, at := m.logVisible()

	head := m.sectionHead(logSources[m.logSrc].label+" · "+logFilters[m.logFilter].label, W)
	var content []string
	content = append(content, head, "")

	switch {
	case at.IsZero() && m.logAsked[m.logSrc]:
		content = append(content, t.dmr.render("asking the device…"))
	case at.IsZero():
		content = append(content, t.dmr.render("press r to fetch"))
	case len(lines) == 0:
		content = append(content, t.dmr.render("nothing matched this filter"))
	}

	// The viewport is whatever the frame leaves after the heading, the blank and
	// the two-line tail; the offset counts up from the newest line.
	page := max(m.rows-2-len(content)-2, 1)
	if len(lines) > 0 {
		end := len(lines) - m.logScroll
		end = min(max(end, 1), len(lines))
		start := max(end-page, 0)
		for _, ln := range lines[start:end] {
			content = append(content, m.logRow(ln, W))
		}
	}

	age := ""
	if !at.IsZero() {
		age = fmtAgeShort(now.Sub(at)) + " ago · " + strconv.Itoa(len(lines)) + " lines"
		if m.logScroll > 0 {
			age += " · scrolled " + strconv.Itoa(m.logScroll)
		}
	}
	left := "↑↓ scroll · ←→ page · s source · f filter · r refresh · esc back"
	tail := []string{"", between(t.dmr.render(left), DispW(left), t.dmr.render(age), DispW(age), W)}
	return frameBody(content, tail, m.rows-2, false)
}
