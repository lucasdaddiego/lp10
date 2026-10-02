// The diagnostics view: what the box says about itself without ssh — the
// tunnel's link and player read-out, the LSSDP responder, the Spotify
// engine's ZeroConf answer, the vendor's update verdict on request, what moved
// since the last `lp10 sweep`, and the model's invariant hardware facts. Two
// layouts by width: ruled two-column sections on a wide terminal, one stacked
// column on a narrow one.

package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/sweep"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// diagCardsMinW is the inner width at/above which the diagnostics use the
// two-column layout; below it, the single stacked column.
const diagCardsMinW = 100

// diagFooters is the view's bottom help line (both layouts), widest first:
// on a narrow terminal the keys give way to the fact beside them — how much of
// the read-out is off-screen — rather than push it off the row (footerFit).
var diagFooters = []string{
	"live · u asks the vendor about updates · esc player · ? help",
	"u asks the vendor about updates · esc player · ? help",
	"u asks the vendor · esc player · ? help",
	"u asks the vendor · ? help",
	"? help",
}

// thrRx is the seconds since the tunnel's last frame: a live box answers the
// status poll every workers.StatusEvery, so a gap past one and a half polls is
// a warn, and the worker gives up on the link at workers.SilentAfter.
var thrRx = [2]float64{workers.StatusEvery.Seconds() * 1.75, workers.SilentAfter.Seconds()}

// sev reads a lower-is-better value against its thresholds: 0 (good) below
// thr[0], 1 (warn) below thr[1], 2 (bad) at or above.
func sev(v float64, thr [2]float64) int {
	switch {
	case v >= thr[1]:
		return 2
	case v >= thr[0]:
		return 1
	}
	return 0
}

func (m *model) sevPen(v float64, thr [2]float64) lipgloss.Style { return m.sty.sevs[sev(v, thr)] }

// lssdpFresh is how recent an LSSDP answer must be to count the device as
// "up on the LAN" on the connecting screen: the probe runs every 5 s while
// disconnected, so this spans a few missed probes.
const lssdpFresh = 20 * time.Second

// ---- rows ---------------------------------------------------------------------

// kv is one label/value row.
type kv struct{ k, v string }

// presentKVs drops the rows with no value, so a fact not read yet leaves no
// empty label behind.
func presentKVs(facts []kv) []kv {
	out := facts[:0:0]
	for _, f := range facts {
		if f.v != "" {
			out = append(out, f)
		}
	}
	return out
}

// audioFacts is the player as the tunnel reports it: the source, the play
// state, the volume, the output cap and the EQ.
func (m *model) audioFacts(d protocol.DiagnosticSnapshot) []kv {
	s := d.Snapshot
	if !s.Connected {
		return []kv{{"source", "— (the tunnel is down)"}}
	}
	state := "idle"
	switch {
	case s.Playing:
		state = "playing"
	case s.Track != nil:
		state = "paused"
	}
	if title := trackTitleOf(s.Track); title != "" {
		state += " · " + title
	} else if s.Playing {
		state += " · " + untitledHint
	}
	vol := fmt.Sprintf("%d%%", s.Vol)
	if s.Muted {
		vol += " · muted (in the MCU)"
	}
	_, eq := m.st.EQView()
	facts := []kv{
		{"source", SourceName(s)},
		{"state", state},
		{"volume", vol},
	}
	if v, ok := eq["MXV"]; ok {
		facts = append(facts, kv{"max vol", strconv.Itoa(v) + "%"})
	}
	if v, ok := eq["EQE"]; ok {
		word := "off"
		if v == 1 {
			word = "on"
			if p, ok := eq["EQS"]; ok {
				if names := m.st.EQPresets(); p >= 0 && p < len(names) && names[p] != "" {
					word += " · " + names[p]
				}
			}
		}
		facts = append(facts, kv{"eq", word})
	}
	return presentKVs(facts)
}

// trackTitleOf is trackTitle for a track that may be nil.
func trackTitleOf(t *protocol.Track) string {
	if t == nil {
		return ""
	}
	return trackTitle(t)
}

// tunnelReadout is the link row: "live · :2018 · last frame 0.8s ago" or
// "down" with the attempts so far.
func (m *model) tunnelReadout(d protocol.DiagnosticSnapshot, now time.Time) string {
	ps := m.sty.pens()
	s := d.Snapshot
	if !s.Connected {
		txt := "down"
		if s.Attempts > 1 {
			txt += fmt.Sprintf(" · %d attempts", s.Attempts)
		}
		return ps.warn.render(txt)
	}
	out := ps.acc.render("live") + ps.dim.render(" · :"+strconv.Itoa(tunnel.Port))
	if !d.LastRx.IsZero() {
		secs := now.Sub(d.LastRx).Seconds()
		out += ps.dim.render(" · last frame ") + stylePen(m.sevPen(secs, thrRx)).render(fmt.Sprintf("%.1fs", secs)) +
			ps.dim.render(" ago")
	}
	return out
}

// lssdpReadout is the row for the device's own UDP:1800 responder — the
// tunnel-free liveness signal: "answered 3s ago · S · eth0" (accent) or, after
// an unanswered probe, "no answer · probed 4s ago" (warn). "" until the first
// probe has run.
func (m *model) lssdpReadout(d protocol.DiagnosticSnapshot, now time.Time) string {
	if d.LSSDPProbeAt.IsZero() {
		return ""
	}
	ps := m.sty.pens()
	if d.LSSDP == nil {
		txt := "no answer"
		if !d.LSSDPOKAt.IsZero() {
			txt += fmt.Sprintf(" · last %s ago", fmtAgeShort(now.Sub(d.LSSDPOKAt)))
		}
		return ps.warn.render(txt) + ps.dim.render(fmt.Sprintf(" · probed %s ago", fmtAgeShort(now.Sub(d.LSSDPProbeAt))))
	}
	facts := []string{fmt.Sprintf("answered %s ago", fmtAgeShort(now.Sub(d.LSSDPOKAt)))}
	if d.LSSDP.State != "" {
		facts = append(facts, d.LSSDP.State)
	}
	if d.LSSDP.NetMode != "" {
		facts = append(facts, strings.ToLower(d.LSSDP.NetMode))
	}
	if len(facts) == 1 {
		return ps.acc.render(facts[0])
	}
	return ps.acc.render(facts[0]) + ps.dim.render(" · "+strings.Join(facts[1:], " · "))
}

// zcReadout is the row for the Spotify engine's ZeroConf endpoint: "answered
// 4s ago · :9095" (accent), plus "signed in as x" on the rare answer that
// names a user, or after a miss "no answer · :9095 · probed 12s ago" / "not
// advertised · probed 12s ago" (warn: the engine is not up). "" until the
// first probe has run. An empty activeUser is NOT "nobody signed in": the Pro
// engine leaves it empty while it plays a session, so the field can only ever
// add a fact, never assert an absence.
func (m *model) zcReadout(d protocol.DiagnosticSnapshot, now time.Time) string {
	if d.ZCProbeAt.IsZero() {
		return ""
	}
	ps := m.sty.pens()
	if d.SpotifyZC == nil {
		txt := "not advertised"
		if d.ZCPort > 0 {
			txt = "no answer · :" + strconv.Itoa(d.ZCPort)
		}
		if !d.ZCOKAt.IsZero() {
			txt += fmt.Sprintf(" · last %s ago", fmtAgeShort(now.Sub(d.ZCOKAt)))
		}
		return ps.warn.render(txt) + ps.dim.render(fmt.Sprintf(" · probed %s ago", fmtAgeShort(now.Sub(d.ZCProbeAt))))
	}
	zc := d.SpotifyZC
	facts := []string{fmt.Sprintf("answered %s ago", fmtAgeShort(now.Sub(d.ZCOKAt)))}
	if d.ZCPort > 0 {
		facts = append(facts, ":"+strconv.Itoa(d.ZCPort))
	}
	switch {
	case zc.ActiveUser != "":
		facts = append(facts, "signed in as "+zc.ActiveUser)
	case zc.StatusString != "" && zc.StatusString != "OK":
		facts = append(facts, strings.ToLower(zc.StatusString))
	}
	if len(facts) == 1 {
		return ps.acc.render(facts[0])
	}
	return ps.acc.render(facts[0]) + ps.dim.render(" · "+strings.Join(facts[1:], " · "))
}

// connectionRows are the three ways lp10 reaches the box, each styled.
func (m *model) connectionRows(d protocol.DiagnosticSnapshot, now time.Time) []kv {
	return presentKVs([]kv{
		{"tunnel", m.tunnelReadout(d, now)},
		{"host", m.sty.pens().txt.render(m.cfg.Host)},
		{"lssdp", m.lssdpReadout(d, now)},
		{"spotify", m.zcReadout(d, now)},
	})
}

// fmtAgeShort renders a duration as "0.6s" / "12s" / "3m" / "2h".
func fmtAgeShort(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < 2*time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// eSDK is the Spotify engine's build from its ZeroConf answer, "" until one.
func eSDK(d protocol.DiagnosticSnapshot) string {
	if d.SpotifyZC == nil {
		return ""
	}
	return d.SpotifyZC.LibraryVersion
}

// deviceFacts is the identity list both layouts render: the firmware (the
// LSSDP answer — the one place the box names its build without ssh), the MCU
// build (the tunnel's VER), the Spotify eSDK (ZeroConf), what moved since the
// last sweep, and the vendor's verdict when u has asked.
func (m *model) deviceFacts(d protocol.DiagnosticSnapshot, now time.Time) []kv {
	fw := ""
	if d.LSSDP != nil {
		fw = d.LSSDP.FW
	}
	return presentKVs([]kv{
		{"firmware", fw},
		{"mcu", d.MCU},
		{"eSDK", eSDK(d)},
		{"sweep", sweepDeltaFact(fw, d.MCU, eSDK(d), m.baseline)},
		{"vendor", otaFact(d, now)},
	})
}

// sweepDeltaFact compares the live identity with the last `lp10 sweep`'s
// baseline: the firmware build, the MCU build and the Spotify eSDK — what an
// update moves that the box still reports without ssh. "" without a baseline
// or before any of them has been read live; otherwise what changed, or that
// nothing did, dated to when the oldest of the compared values was read: a
// baseline is merged fact by fact, so a value can be an earlier sweep's
// (Carried).
func sweepDeltaFact(fw, mcu, sdk string, base *sweep.Report) string {
	if base == nil {
		return ""
	}
	var changes []string
	var read time.Time // the oldest read among the compared values
	compare := func(key, label, was, now string) {
		if was == "" || now == "" {
			return
		}
		at, ok := base.Carried[key]
		if !ok {
			at = base.At
		}
		if read.IsZero() || at.Before(read) {
			read = at
		}
		if was != now {
			changes = append(changes, label+" "+was+" → "+now)
		}
	}
	compare("lssdp", "firmware", base.LSSDP.FW, fw)
	compare("tunnel.ver", "mcu", base.Tunnel.MCU, firstSeg(mcu, '-')) // the sweep keeps VER's version field
	compare("zeroconf", "eSDK", base.ZeroConf.LibraryVersion, sdk)
	if read.IsZero() {
		return ""
	}
	when := read.Format("Jan 2 15:04")
	if len(changes) == 0 {
		return "unchanged since " + when
	}
	return strings.Join(changes, " · ") + " · since " + when
}

// otaFact is the device card's vendor line: "" until u has asked, "checking…"
// while the vendor is being asked, then the verdict with its age — "up to
// date", "AR241CP_9xxx available", or why there is none.
func otaFact(d protocol.DiagnosticSnapshot, now time.Time) string {
	if d.OTA == nil {
		if d.OTAPending {
			return "checking…"
		}
		return ""
	}
	age := " · checked " + fmtAgeShort(now.Sub(d.OTA.At)) + " ago"
	switch {
	case d.OTA.Err != "":
		return "check failed · " + d.OTA.Err + age
	case d.OTA.UpToDate:
		return "up to date" + age
	case d.OTA.Offered != "":
		return d.OTA.Offered + " available" + age
	}
	return "update available" + age
}

// confHardware is the invariant hardware reference for the LP10 (the one model
// this tool targets), alphabetical by label, encoding the teardown's findings:
// a line-level streamer, no power amp, optical S/PDIF up to 24-bit/192 kHz. The
// DAC is the front-panel MCU itself — an MVSilicon BP10xx Bluetooth-audio SoC
// running in I2S-in mode, which also hosts every tone / EQ-preset /
// virtual-bass / balance / max-volume stage and the volume and mute (the
// tunnel's controls); the firmware's device tree declares a Wolfson WM8904 at
// I2C 0x1a, but nothing answers there.
var confHardware = []kv{
	{"dac", "MVSilicon BP10xx MCU · I2S in · tone/EQ/balance on-chip"},
	{"line in", "3.5 mm aux · ADC unidentified (WM8904 declared, absent)"},
	{"line out", "3.5 mm · 1 Vrms (no power amp)"},
	{"optical", "S/PDIF TOSLINK ≤ 24-bit/192 kHz"},
	{"radio", "dual-band 802.11ac · BT 5.0"},
	{"soc", "Amlogic A113L · 2× Cortex-A35"},
}

// diagSection is one titled block of rows.
type diagSection struct {
	title string
	rows  []kv
}

// diagSections are the read-out's sections, alphabetical, the empty ones
// dropped. The connection rows arrive styled; the others are plain text.
func (m *model) diagSections(d protocol.DiagnosticSnapshot, now time.Time) []diagSection {
	all := []diagSection{
		{"audio", m.audioFacts(d)},
		{"connection", m.connectionRows(d, now)},
		{"device", m.deviceFacts(d, now)},
		{"hardware", confHardware},
	}
	out := all[:0:0]
	for _, sec := range all {
		if len(sec.rows) > 0 {
			out = append(out, sec)
		}
	}
	return out
}

// diagRow renders one label/value row into w columns: the dim label in its
// fixed column, then the value — plain text in the body pen, or kept as it is
// when it arrives styled — clipped so a long value (a host name, a title)
// degrades to a clipped row instead of wrapping the frame.
func (m *model) diagRow(f kv, w int) string {
	ps := m.sty.pens()
	v := f.v
	if !strings.Contains(v, "\x1b") { // plain text; a styled value keeps its pens
		v = ps.txt.render(v)
	}
	return clipStyled(ps.dim.render(f.k)+labelGap(f.k, diagLabelW)+v, w)
}

// sectionRows renders one section: a rule with its title, then the rows.
func (m *model) sectionRows(sec diagSection, w int) []string {
	t := m.sty
	fill := max(w-3-DispW(sec.title), 0) // "─ " + title + " "
	out := []string{t.pens().dmr.render("─ ") + t.sAcc.Bold(true).Render(sec.title) +
		t.pens().dmr.render(" "+strings.Repeat("─", fill))}
	for _, f := range sec.rows {
		out = append(out, "  "+m.diagRow(f, w-2))
	}
	return out
}

// sectionsHeight is how many rows the sections take in one column, a blank
// row between each.
func sectionsHeight(secs []diagSection) int {
	h := 0
	for i, sec := range secs {
		if i > 0 {
			h++
		}
		h += 1 + len(sec.rows)
	}
	return h
}

// splitSections picks where the left column ends so the two columns come out
// as close in height as the section boundaries allow.
func splitSections(secs []diagSection) int {
	split, best := 0, 1<<30
	for i := 0; i <= len(secs); i++ {
		delta := sectionsHeight(secs[:i]) - sectionsHeight(secs[i:])
		if dist := max(delta, -delta); dist < best {
			split, best = i, dist
		}
	}
	return split
}

// column renders sections one under another, a blank row between each.
func (m *model) column(secs []diagSection, w int) []string {
	var rows []string
	for i, sec := range secs {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, m.sectionRows(sec, w)...)
	}
	return rows
}

// diagVerdict is the health rollup with its reasons: the worst severity across
// the live signals, and the signals that put it there, worst first — so the
// masthead says WHY it is amber or red rather than leave the reader hunting
// through the sections.
func diagVerdict(d protocol.DiagnosticSnapshot, now time.Time) (worst int, why []string) {
	type cause struct {
		sev  int
		text string
	}
	var causes []cause
	add := func(sv int, text string) {
		if sv > 0 {
			causes = append(causes, cause{sv, text})
		}
		worst = max(worst, sv)
	}
	if !d.LastRx.IsZero() {
		add(sev(now.Sub(d.LastRx).Seconds(), thrRx), "tunnel quiet")
	}
	if !d.LSSDPProbeAt.IsZero() && d.LSSDP == nil {
		add(1, "lssdp not answering")
	}
	if !d.ZCProbeAt.IsZero() && d.SpotifyZC == nil {
		add(1, "spotify engine not answering")
	}
	// worst first, then in signal order; at most two are named
	for sv := 2; sv >= 1 && len(why) < 2; sv-- {
		for _, c := range causes {
			if c.sev == sv && len(why) < 2 {
				why = append(why, c.text)
			}
		}
	}
	return worst, why
}

// diagMasthead is the top line of both layouts: the title, the health verdict
// with the signals behind it (while connected), and the connection light +
// clock.
func (m *model) diagMasthead(d protocol.DiagnosticSnapshot, now time.Time, w int) string {
	t := m.sty
	hr, hrW := t.pens().acc.render("●")+t.pens().dim.render(" "+now.Format("15:04")), DispW("● 15:04")
	if !d.Snapshot.Connected {
		hr, hrW = stWarn.Render("● disconnected"), DispW("● disconnected")
	}
	left, leftW := t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics")
	if d.Snapshot.Connected {
		word, pen := "healthy", t.sAcc
		worst, why := diagVerdict(d, now)
		switch worst {
		case 1:
			word, pen = "warn", stWarn
		case 2:
			word, pen = "fault", stRed
		}
		verdict := "● " + word
		left += "   " + pen.Render(verdict)
		leftW += 3 + DispW(verdict)
		if len(why) > 0 {
			reason := " · " + strings.Join(why, " · ")
			if room := w - leftW - hrW - 2; DispW(reason) > room {
				reason = Clip(reason, max(room, 0))
			}
			left += t.pens().dim.render(reason)
			leftW += DispW(reason)
		}
	}
	return between(left, leftW, hr, hrW, w)
}

// diagErrLine renders the view's bottom error line, or ok=false when there is
// nothing current to show. A note is history the moment it is recorded, so it
// shows age-stamped for diagErrWindow and then leaves — a recovered hiccup must
// not sit under a healthy masthead reading as a live fault.
func diagErrLine(s protocol.Snapshot, now time.Time, W int) (string, bool) {
	if s.Error == "" || now.Sub(s.ErrorAt) >= diagErrWindow {
		return "", false
	}
	age := fmt.Sprintf(" · %.1fs ago", now.Sub(s.ErrorAt).Seconds())
	return stWarn.Render(Clip(GL["warn"]+" "+friendlyError(s.Error)+age, W)), true
}

// ---- the two layouts ------------------------------------------------------------

// renderDiagnostic picks the layout by width: two ruled columns on a wide
// terminal, one stacked column when narrow. Either scrolls when it is taller
// than the frame, and the footer says how much is off-screen.
func (m *model) renderDiagnostic(d protocol.DiagnosticSnapshot, now time.Time, W int) []string {
	t := m.sty
	secs := m.diagSections(d, now)
	head := []string{m.diagMasthead(d, now, W), t.pens().dmr.render(strings.Repeat("━", W))}
	var body []string
	if W >= diagCardsMinW {
		colW := (W - diagCardsGutter) / 2
		rightW := W - diagCardsGutter - colW // absorbs the odd column
		split := splitSections(secs)
		left, right := m.column(secs[:split], colW), m.column(secs[split:], rightW)
		gut := spaces(diagCardsGutter)
		for i := range max(len(left), len(right)) {
			l, r := spaces(colW), spaces(rightW)
			if i < len(left) {
				l = padVis(left[i], colW)
			}
			if i < len(right) {
				r = padVis(right[i], rightW)
			}
			body = append(body, l+gut+r)
		}
	} else {
		body = m.column(secs, W)
	}

	var tail []string
	if line, ok := diagErrLine(d.Snapshot, now, W); ok {
		tail = append(tail, line, "")
	}
	body, hint := m.diagWindow(body, m.bodyRows()-len(head)-len(tail)-1, "↑↓ scroll")
	right := t.pens().acc.render("●") + t.pens().dmr.render(" good   ") + stWarn.Render("●") +
		t.pens().dmr.render(" warn   ") + stRed.Render("●") + t.pens().dmr.render(" fault")
	rightW := DispW("● good   ● warn   ● fault")
	if hint != "" {
		right, rightW = t.pens().dim.render(hint), DispW(hint)
	}
	tail = append(tail, m.footerFit(diagFooters, right, rightW, W))
	return frameBody(append(head, body...), tail, m.bodyRows(), false)
}

// diagCardsGutter is the blank columns between the wide layout's two columns.
const diagCardsGutter = 4

// diagScrollBy moves the read-out by n rows (negative = up); the render clamps
// it to what is actually off-screen, so over-scrolling is inert.
func (m *model) diagScrollBy(n int) {
	m.diagScroll = max(m.diagScroll+n, 0)
}

// diagPage is one page of the read-out for ←→.
func (m *model) diagPage() int { return max(m.bodyRows()-4, 1) }

// diagWindow cuts the scrollable rows to the room, honouring and clamping the
// scroll offset, and returns the rows plus a hint for the footer when there is
// more above or below ("" when everything fits). keys names what scrolls.
func (m *model) diagWindow(rows []string, room int, keys string) ([]string, string) {
	if room <= 0 {
		return nil, ""
	}
	over := len(rows) - room
	if over <= 0 {
		m.diagScroll = 0
		return rows, ""
	}
	if m.diagScroll > over {
		m.diagScroll = over
	}
	below := over - m.diagScroll
	hint := fmt.Sprintf("%s · %d more row%s below", keys, below, plural(below))
	switch {
	case below == 0:
		hint = fmt.Sprintf("%s · %d row%s above", keys, m.diagScroll, plural(m.diagScroll))
	case m.diagScroll > 0:
		hint = fmt.Sprintf("%s · %d above · %d below", keys, m.diagScroll, below)
	}
	return rows[m.diagScroll : m.diagScroll+room], hint
}

// plural is "s" for any count but one.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// clipStyled clips an already-styled string to display width w, keeping the
// styling: ansi.Truncate cuts between escape sequences (measuring width the way
// lipgloss does, so it agrees with padVis) and every segment left of the cut
// keeps its colour.
func clipStyled(styled string, w int) string {
	if lipgloss.Width(styled) <= w {
		return styled
	}
	if w <= 0 {
		return ""
	}
	if ell := GL["ell"]; w > DispW(ell) {
		return ansi.Truncate(styled, w, ell)
	}
	return ansi.Truncate(styled, w, "") // no room for the ellipsis: hard cut
}

// footerFit lays a view's key hint beside its fact on the footer row, the fact
// right-aligned (fact is styled; factW is its visible width). The fact is what
// the row is for — how much is off-screen — so it keeps its place and the
// keys give way: the widest of hints (widest first) that still fits beside it,
// or none. Only a fact wider than the row is clipped.
func (m *model) footerFit(hints []string, fact string, factW, W int) string {
	if factW >= W {
		return clipStyled(fact, W)
	}
	for _, h := range hints {
		if hw := DispW(h); hw+1+factW <= W {
			return between(m.sty.pens().dmr.render(h), hw, fact, factW, W)
		}
	}
	return spaces(W-factW) + fact
}

// diagLabelW is the dim label column shared by every diagnostics row.
const diagLabelW = 10

// labelGap is the space run after a fixed-width label: the column width minus
// the label's display width, floored at 0 so a label wider than its column can
// never produce a negative (panicking) repeat count.
func labelGap(label string, col int) string {
	return strings.Repeat(" ", max(0, col-DispW(label)))
}

// firstSeg is s up to the first sep ("29-1d316f0c-10" → "29"), or s whole.
func firstSeg(s string, sep byte) string {
	if before, _, ok := strings.Cut(s, string(sep)); ok {
		return before
	}
	return s
}
