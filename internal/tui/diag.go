package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// diagCardsMinW is the inner width at/above which the diagnostics overlay uses the
// two-column card grid; below it, the single-column stacked layout (which fits a
// narrow terminal and degrades gracefully) is used instead.
const diagCardsMinW = 100

// diagFooter is the overlay's bottom help line (both layouts).
const diagFooter = "live · any key returns to the dashboard"

// ---- shared severity model -----------------------------------------------------
//
// Health thresholds, lower-is-better: sev(v, thr) reads 0 (good) below thr[0],
// 1 (warn) below thr[1], 2 (bad) at/above. One table shared by both layouts so a
// stacked gauge, a cards gauge, the vitals line, and the verdict rollup can never
// disagree on where "warn" starts.
var (
	thrCPU    = [2]float64{60, 85} // % of all cores (1m load / NCPU)
	thrMem    = [2]float64{70, 88} // % used
	thrTemp   = [2]float64{60, 75} // °C SoC
	thrData   = [2]float64{80, 92} // % of /lsync used
	thrRx     = [2]float64{3, 8}   // seconds since the last framed record
	thrSignal = [2]float64{60, 72} // Wi-Fi signal as -dBm (-41 good, -72 warn)
)

func sev(v float64, thr [2]float64) int {
	switch {
	case v < thr[0]:
		return 0
	case v < thr[1]:
		return 1
	default:
		return 2
	}
}

// sevPens maps a severity to its pen: good (accent) · warn (amber) · bad (red).
func (m *model) sevPens() [3]lipgloss.Style { return m.sty.sevs }

// sevPen picks the pen for a value against its threshold pair.
func (m *model) sevPen(v float64, thr [2]float64) lipgloss.Style { return m.sty.sevs[sev(v, thr)] }

// ---- shared collectors (both layouts read the same derived state) --------------

// diagIdentity is the device section's readout, shared by both diagnostics
// layouts (renderDiagStacked / renderDiagCards) so the two can't drift apart:
// identity ONLY — what the box is, not how it's doing or how it's reached.
// Wire facts live in the connection/network sections (host, mac) and runtime
// state in resources (uptime). The first row of fields defaults to "—" (always
// shown); the second row stays "" until the device reports it (regs 90/92),
// and its rows render only then.
type diagIdentity struct {
	model, os, fw, build  string
	name, serial, bt, mcu string
}

// collectIdentity derives the identity strings from the sysinfo/devinfo/details
// (any may be nil).
func collectIdentity(si *protocol.SysInfo, dev *protocol.DevInfo, dt *protocol.DevDetails) diagIdentity {
	d := diagIdentity{model: "—", os: "—", fw: "—", build: "—"}
	if si != nil {
		if si.FW != "" {
			d.fw, d.model = si.FW, "Arylic "+firstSeg(si.FW, '_')
		}
		if si.OS != "" {
			d.os = strings.Replace(si.OS, "-", " ", 1)
			if si.NCPU != "" {
				d.os += " · " + si.NCPU + " cores"
			}
		}
	}
	if dev != nil {
		if dev.Platform != "" && d.model != "—" {
			d.model += " · " + dev.Platform
		}
		if dev.Build != "" {
			d.build = dev.Build
			if dev.App != "" {
				d.build += " · app " + dev.App
			}
		}
		d.name = dev.Name
	}
	if dt != nil {
		d.serial, d.bt = dt.Serial, dt.BTMAC
		if dt.MCU != "" {
			d.mcu = "v" + dt.MCU
		}
		if dt.FW != "" {
			d.fw = dt.FW // the fuller string — carries the trailing sub-version
		}
	}
	return d
}

// hostReadout is the connection section's target line: how lp10 reaches the
// device — the ssh user @ the configured host, upgraded to the resolved IP
// once @@i reports it, tagged when mDNS discovery found the box.
func (m *model) hostReadout(dev *protocol.DevInfo) string {
	h := m.cfg.User + "@" + m.cfg.Host
	if dev != nil && dev.IP != "" {
		h = m.cfg.User + "@" + dev.IP
	}
	if m.cfg.Discovered {
		h += " · mDNS"
	}
	return h
}

// sshReadout is the connection section's stream line: how fresh the framed
// records are, plus the connect-attempt count.
func (m *model) sshReadout(ls diagLinkStatus, att int) string {
	tail := m.sty.pens().txt.render(fmt.Sprintf(" · %d %s", att, ls.attWord))
	if ls.rxTxt == "—" { // nothing framed yet — say so instead of "rx — ago"
		return m.sty.pens().dim.render("no data yet") + tail
	}
	return m.sty.pens().txt.render("rx ") + ls.rxPen.Render(ls.rxTxt) + m.sty.pens().txt.render(" ago") + tail
}

// tunnelReadout is the connection section's :2018 line (the EQ / Max-Vol
// control tunnel): the port and its live/down state.
func (m *model) tunnelReadout(ls diagLinkStatus) string {
	return m.sty.pens().txt.render(":2018 · ") + ls.tunPen.Render(ls.tunTxt)
}

// errReadout renders the interface error/drop counters as session deltas:
// calm dim zeros, amber the moment a counter grows while connected.
func (m *model) errReadout(ns protocol.NetStat) string {
	cell := func(label string, v int64) string {
		pen := m.sty.sDim
		if v > 0 {
			pen = stWarn
		}
		return m.sty.pens().dim.render(label+" ") + pen.Render(strconv.FormatInt(v, 10))
	}
	sep := m.sty.pens().dmr.render(" · ")
	return cell("rx", ns.RxErrs) + sep + cell("tx", ns.TxErrs) + sep + cell("drop", ns.Drops) +
		m.sty.pens().dmr.render(" · session")
}

// multiroomReadout renders the group state: "solo", or the linked device count.
func (m *model) multiroomReadout(mr *protocol.Multiroom) string {
	if mr.Devices == 0 {
		return m.sty.pens().txt.render("solo")
	}
	word := "devices"
	if mr.Devices == 1 {
		word = "device"
	}
	return m.sty.pens().acc.render(fmt.Sprintf("linked · %d %s", mr.Devices, word))
}

// diagVitals is the parsed live-numeric readout shared by both layouts: raw
// numbers only — each layout formats its own labels and detail strings.
type diagVitals struct {
	haveCPU bool
	cpuFrac float64  // 1m load / cores
	loads   []string // the raw loadavg triplet, for the detail strings

	haveMem          bool
	memUf            float64 // fraction used
	availKB, totalKB int

	haveTemp bool
	tempC    int

	haveData       bool
	dataUf         float64 // fraction of /lsync used
	usedKB, dataKB int

	haveBuf bool
	bufFill float64 // ALSA ring fill fraction
	bufSev  int     // inverted health: a FULL ring is healthy

	playing bool // ALSA reports RUNNING — gates the buffer's health meaning
}

// collectVitals parses the @@s/@@i numerics both layouts gauge (either source may
// be nil; the have* flags gate each reading).
func collectVitals(si *protocol.SysInfo, dev *protocol.DevInfo) diagVitals {
	var v diagVitals
	if si != nil {
		v.loads = strings.Fields(si.Load)
		nc, _ := strconv.Atoi(si.NCPU)
		if nc < 1 {
			nc = 1
		}
		if len(v.loads) >= 1 {
			if l1, err := strconv.ParseFloat(v.loads[0], 64); err == nil {
				v.cpuFrac, v.haveCPU = l1/float64(nc), true
			}
		}
		av, e1 := strconv.Atoi(si.Avail)
		tot, e2 := strconv.Atoi(si.Total)
		if e1 == nil && e2 == nil && tot > 0 {
			v.memUf, v.availKB, v.totalKB, v.haveMem = float64(tot-av)/float64(tot), av, tot, true
		}
		if mc, err := strconv.Atoi(si.TempmC); err == nil {
			v.tempC, v.haveTemp = mc/1000, true
		}
		if si.BufAvail != "" && si.BufSize != "" {
			if a, e1 := strconv.Atoi(si.BufAvail); e1 == nil {
				if bs, e2 := strconv.Atoi(si.BufSize); e2 == nil && bs > 0 {
					v.bufFill, v.haveBuf = max(float64(bs-a)/float64(bs), 0), true
					switch { // buffer health is inverted: a FULL ring is healthy
					case v.bufFill >= 0.5:
						v.bufSev = 0
					case v.bufFill >= 0.25:
						v.bufSev = 1
					default:
						v.bufSev = 2
					}
				}
			}
		}
		v.playing = si.PcmState == "RUNNING"
	}
	if dev != nil {
		u, e1 := strconv.Atoi(dev.DataUsed)
		tt, e2 := strconv.Atoi(dev.DataTotal)
		if e1 == nil && e2 == nil && tt > 0 {
			v.dataUf, v.usedKB, v.dataKB, v.haveData = float64(u)/float64(tt), u, tt, true
		}
	}
	return v
}

// diagLinkStatus is lp10's own link readout — ssh stream freshness, the attempt
// count's noun, and the :2018 tunnel state — shared verbatim by both layouts.
type diagLinkStatus struct {
	rxTxt   string
	rxPen   lipgloss.Style
	attWord string
	tunTxt  string
	tunPen  lipgloss.Style
}

func (m *model) linkStatus(lastRx, now time.Time, att int, eqConn bool) diagLinkStatus {
	ls := diagLinkStatus{rxTxt: "—", rxPen: m.sty.sDim, attWord: "attempts", tunTxt: "down", tunPen: stRed}
	if !lastRx.IsZero() {
		secs := now.Sub(lastRx).Seconds()
		ls.rxTxt, ls.rxPen = fmt.Sprintf("%.1fs", secs), m.sevPen(secs, thrRx)
	}
	if att == 1 {
		ls.attWord = "attempt"
	}
	if eqConn {
		ls.tunTxt, ls.tunPen = "live", m.sty.sAcc
	}
	return ls
}

// diagStatus is the connection light + clock on the masthead's right. The
// silence window matches the watchdog's threshold (not a tighter one): the
// device's idle loop legitimately drops to a ~3s poll cadence, so a shorter
// window would flash "LUCI silent" between healthy low-poll frames.
func (m *model) diagStatus(connected bool, dData, now time.Time) (hr string, hrW int, silent bool) {
	clock := now.Format("15:04")
	switch {
	case !connected:
		return stWarn.Render("● disconnected"), DispW("● disconnected"), false
	case !dData.IsZero() && now.Sub(dData) > workers.SilentAfter:
		return stWarn.Render("● LUCI silent · " + clock), DispW("● LUCI silent · " + clock), true
	default:
		return m.sty.pens().acc.render("●") + m.sty.pens().dim.render(" "+clock), DispW("● " + clock), false
	}
}

// diagErrLine renders the overlay's bottom error line (prettified, not the raw
// ssh dump — the sections above already carry the state), or ok=false when
// there is nothing current to show. A fatal error always shows: it IS present
// state, latched until data flows again. A transient note is history the moment
// it is recorded, so it shows age-stamped for diagErrWindow and then leaves —
// a recovered hiccup must not sit under a healthy masthead reading as a live
// fault.
func diagErrLine(s protocol.Snapshot, now time.Time, W int) (string, bool) {
	switch {
	case s.Error == "":
		return "", false
	case s.Fatal:
		return stWarn.Render(Clip(GL["warn"]+" "+friendlyError(s.Error), W)), true
	case now.Sub(s.ErrorAt) < diagErrWindow:
		age := fmt.Sprintf(" · %.1fs ago", now.Sub(s.ErrorAt).Seconds())
		return stWarn.Render(Clip(GL["warn"]+" "+friendlyError(s.Error)+age, W)), true
	default:
		return "", false
	}
}

// wifiBand renders the " · ch N · 2.4|5 GHz" suffix from the @@i freq (MHz), or
// "" when the frequency is unknown.
func wifiBand(freq string) string {
	f, err := strconv.Atoi(freq)
	if err != nil || f <= 0 {
		return ""
	}
	b := " · 2.4 GHz"
	if f >= 5000 {
		b = " · 5 GHz"
	}
	return fmt.Sprintf(" · ch %d%s", freqToChan(f), b)
}

// ethDetail renders the " · N Mbit/s · full duplex" suffix from the @@i link fields.
func ethDetail(speed, duplex string) string {
	detail := ""
	if sp, err := strconv.Atoi(speed); err == nil && sp > 0 {
		detail += fmt.Sprintf(" · %d Mbit/s", sp)
	}
	if duplex != "" {
		detail += " · " + duplex + " duplex"
	}
	return detail
}

// diagFormat is the source-stream descriptor for the audio section — "Ogg ·
// 44.1 kHz · 2 ch" — or "—" when nothing is playing.
func diagFormat(tr *protocol.Track) string {
	if tr == nil {
		return "—"
	}
	var ps []string
	if q := Quality(tr); q != "" {
		ps = append(ps, q)
	}
	if ch := tr.ChannelCount; ch > 0 {
		ps = append(ps, fmt.Sprintf("%d ch", ch))
	}
	if len(ps) == 0 {
		return "—"
	}
	return strings.Join(ps, " · ")
}

// bufMeter picks the buffer gauge's pen + detail word, shared by both layouts:
// the ring is a health signal only WHILE PLAYING ("NN% full", severity-
// coloured); an empty ring on an idle device is normal ("idle", neutral).
func (m *model) bufMeter(vit diagVitals) (lipgloss.Style, string) {
	if vit.playing {
		return m.sevPens()[vit.bufSev], "full"
	}
	return m.sty.sDim, "idle"
}

// dacReadout is the audio section's output line — the DAC's actual rate /
// format / channels, tagged live while ALSA reports RUNNING — or "" until @@s
// carries a rate.
func (m *model) dacReadout(si *protocol.SysInfo, playing bool) string {
	if si == nil || si.DacRate == "" {
		return ""
	}
	rate := si.DacRate
	if hz, err := strconv.Atoi(si.DacRate); err == nil {
		rate = fmtKHz(hz)
	}
	parts := []string{rate}
	if si.DacFmt != "" {
		parts = append(parts, si.DacFmt)
	}
	if si.DacCh != "" {
		parts = append(parts, si.DacCh+"ch")
	}
	out := m.sty.pens().txt.render(strings.Join(parts, " · "))
	if playing {
		out += m.sty.pens().acc.render(" ● live")
	}
	return out
}

// tasksReadout is the resources section's scheduler line from /proc's
// running/total pair, or "" when the sample lacks one.
func (m *model) tasksReadout(si *protocol.SysInfo) string {
	if si == nil || si.Procs == "" {
		return ""
	}
	run, tot, ok := strings.Cut(si.Procs, "/")
	if !ok {
		return ""
	}
	return m.sty.pens().txt.render(run) + m.sty.pens().dim.render(" running · ") +
		m.sty.pens().txt.render(tot) + m.sty.pens().dim.render(" total")
}

// latencyPeakPen flags a genuine spike (peak well past the average), not
// baseline wobble.
func (m *model) latencyPeakPen(ps protocol.PingStat) lipgloss.Style {
	if ps.Peak > ps.Avg*2 && ps.Peak-ps.Avg > 10 {
		return stWarn
	}
	return m.sty.sDmr
}

// kv is one labelled fact; presentKVs keeps the ones the device has reported
// (empty values are the "not read yet" sentinel for the optional identity rows).
type kv struct{ k, v string }

func presentKVs(facts []kv) []kv {
	out := make([]kv, 0, len(facts))
	for _, f := range facts {
		if f.v != "" {
			out = append(out, f)
		}
	}
	return out
}

// latTarget is one responding ping target: its row label and stats.
type latTarget struct {
	name string
	ps   protocol.PingStat
}

// latencyTargets returns the responding ping targets in alphabetical name order
// (row order matches the a-z ordering of every other diag item, not hop order).
func (m *model) latencyTargets(netv protocol.NetStat) []latTarget {
	names := [3]string{"you", "gw", pingLabel(m.cfg.PingHost)}
	out := make([]latTarget, 0, 3)
	for i, ps := range netv.Ping {
		if ps.OK {
			out = append(out, latTarget{names[i], ps})
		}
	}
	slices.SortFunc(out, func(a, b latTarget) int { return strings.Compare(a.name, b.name) })
	return out
}

// ---- narrow-layout composition ------------------------------------------------

func (m *model) diagStackedAudioRows(d protocol.DiagnosticSnapshot, v diagVitals, w, gaugeW int) []string {
	t := m.sty
	var rows []string
	bufPen, bufDetail := m.bufMeter(v)
	if v.haveBuf {
		rows = append(rows, m.diagGauge("buffer", t.gaugeBar(v.bufFill, gaugeW, bufPen),
			bufPen.Render(fmt.Sprintf("%d%%", int(v.bufFill*100+0.5))), "   "+bufDetail, w))
	}
	if dac := m.dacReadout(d.SysInfo, v.playing); dac != "" {
		rows = append(rows, m.diagLine("dac", dac))
	}
	if nr := m.nightReadout(d.Snapshot); nr != "" {
		rows = append(rows, m.diagLine("night", nr))
	}
	return append(rows, m.diagLine("stream", t.pens().txt.render(diagFormat(d.Snapshot.Track))))
}

// nightReadout is the diag audio row for night mode — the device's multi-band
// DRC enable as last read back — or "" before the device has reported it.
func (m *model) nightReadout(s protocol.Snapshot) string {
	if !s.NightKnown {
		return ""
	}
	ps := m.sty.pens()
	if s.Night {
		return ps.acc.render("on") + ps.dim.render(" · multi-band DRC · d toggles")
	}
	return ps.dim.render("off · multi-band DRC · d toggles")
}

func (m *model) diagStackedConnectionRows(d protocol.DiagnosticSnapshot, now time.Time) []string {
	status := m.linkStatus(d.LastRx, now, d.ConnectAttempts, d.EQConnected)
	return []string{
		m.diagLine("host", m.sty.pens().txt.render(m.hostReadout(d.DevInfo))),
		m.diagLine("ssh", m.sshReadout(status, d.ConnectAttempts)),
		m.diagLine("tunnel", m.tunnelReadout(status)),
	}
}

// identityFacts is the present-only identity list both diag layouts render, so
// the stacked and cards views can't drift apart.
func identityFacts(d protocol.DiagnosticSnapshot) []kv {
	id := collectIdentity(d.SysInfo, d.DevInfo, d.Details)
	return presentKVs([]kv{
		{"bt", id.bt},
		{"build", id.build},
		{"firmware", id.fw},
		{"mcu", id.mcu},
		{"model", id.model},
		{"name", id.name},
		{"os", id.os},
		{"serial", id.serial},
	})
}

func (m *model) diagStackedDeviceRows(d protocol.DiagnosticSnapshot, w int) []string {
	facts := identityFacts(d)
	rows := make([]string, 0, (len(facts)+1)/2)
	for i := 0; i < len(facts); i += 2 {
		k2, v2 := "", ""
		if i+1 < len(facts) {
			k2, v2 = facts[i+1].k, facts[i+1].v
		}
		rows = append(rows, m.gridRow(facts[i].k, facts[i].v, k2, v2, w))
	}
	return rows
}

func (m *model) diagStackedHardwareRows(w int) []string {
	rows := make([]string, 0, len(confHardware))
	for _, item := range confHardware {
		rows = append(rows, m.diagLine(item.k,
			m.sty.pens().txt.render(Clip(item.v, max(1, w-diagLabelW)))))
	}
	return rows
}

func (m *model) diagStackedSignalRow(d protocol.DiagnosticSnapshot, w, gaugeW int) (string, bool) {
	dev, si := d.DevInfo, d.SysInfo
	if dev == nil || dev.Net != "wifi" || si == nil {
		return "", false
	}
	dbm, err := strconv.Atoi(si.SignalDBm)
	if err != nil {
		return "", false
	}
	pen := m.sevPen(float64(-dbm), thrSignal)
	detail := ""
	if dev.Rate != "" {
		detail = dev.Rate + " Mbit/s"
	}
	if link, e := strconv.Atoi(si.LinkQ); e == nil && link > 0 {
		if detail != "" {
			detail += "  · "
		}
		detail += fmt.Sprintf("link %d/70", link)
	}
	if detail != "" {
		detail = "   " + detail
	}
	value := fmt.Sprintf("%d dBm", dbm)
	return m.diagGauge("signal", m.sty.gaugeBar(float64(dbm+90)/60, gaugeW, pen),
		pen.Render(value), detail, w), true
}

func (m *model) diagStackedNetworkRows(d protocol.DiagnosticSnapshot, w, gaugeW int) []string {
	t, dev, netv := m.sty, d.DevInfo, d.Net
	haveDev := dev != nil && (dev.IP != "" || dev.Net != "")
	var rows []string
	if haveDev {
		rows = append(rows, m.diagLine("address", t.pens().txt.render(orDash(dev.IP))+t.pens().dim.render(" · gw "+orDash(dev.Gateway))))
		if dev.DNS != "" {
			rows = append(rows, m.diagLine("dns", t.pens().txt.render(dev.DNS)))
		}
	}
	if netv.ErrsOK {
		rows = append(rows, m.diagLine("errors", m.errReadout(netv)))
	}
	if haveDev {
		label := "latency"
		for _, target := range m.latencyTargets(netv) {
			rows = append(rows, m.diagLine(label, m.latencyRow(target.name, target.ps)))
			label = ""
		}
		if dev.Net == "wifi" {
			rows = append(rows, m.diagLine("link", t.pens().bri.render("wi-fi")+t.pens().dim.render(" · ")+
				t.pens().txt.render(orDash(dev.SSID))+t.pens().dim.render(wifiBand(dev.Freq))))
		} else {
			rows = append(rows, m.diagLine("link", t.pens().bri.render("ethernet")+
				t.pens().dim.render(ethDetail(dev.Speed, dev.Duplex))))
		}
		if dev.MAC != "" {
			rows = append(rows, m.diagLine("mac", t.pens().txt.render(dev.MAC)))
		}
	}
	if d.Multiroom != nil {
		rows = append(rows, m.diagLine("multiroom", m.multiroomReadout(d.Multiroom)))
	}
	if signal, ok := m.diagStackedSignalRow(d, w, gaugeW); ok {
		rows = append(rows, signal)
	}
	if haveDev && netv.RatesOK {
		rows = append(rows, m.diagLine("traffic", t.pens().dim.render("rx ")+t.pens().txt.render(fmtRate(netv.RxRate))+
			t.pens().dim.render(" · tx ")+t.pens().txt.render(fmtRate(netv.TxRate))))
	}
	return rows
}

func (m *model) diagStackedResourceRows(d protocol.DiagnosticSnapshot, v diagVitals, w, gaugeW int) []string {
	t := m.sty
	var rows []string
	if v.haveCPU {
		pen := m.sevPen(v.cpuFrac*100, thrCPU)
		detail := "   1m " + v.loads[0]
		if len(v.loads) >= 3 {
			detail += " · 5m " + v.loads[1] + " · 15m " + v.loads[2]
		}
		rows = append(rows, m.diagGauge("cpu", t.gaugeBar(v.cpuFrac, gaugeW, pen),
			pen.Render(fmt.Sprintf("%d%%", int(v.cpuFrac*100+0.5))), detail, w))
	}
	if v.haveMem {
		pen := m.sevPen(v.memUf*100, thrMem)
		rows = append(rows, m.diagGauge("memory", t.gaugeBar(v.memUf, gaugeW, pen),
			pen.Render(fmt.Sprintf("%d%%", int(v.memUf*100+0.5))),
			fmt.Sprintf("   %d / %d MB free", v.availKB/1024, v.totalKB/1024), w))
	}
	if v.haveData {
		pen := m.sevPen(v.dataUf*100, thrData)
		rows = append(rows, m.diagGauge("storage", t.gaugeBar(v.dataUf, gaugeW, pen),
			pen.Render(fmt.Sprintf("%d%%", int(v.dataUf*100+0.5))),
			fmt.Sprintf("   %d / %d MB used · /lsync", v.usedKB/1024, v.dataKB/1024), w))
	}
	if tasks := m.tasksReadout(d.SysInfo); tasks != "" {
		rows = append(rows, m.diagLine("tasks", tasks))
	}
	if v.haveTemp {
		pen := m.sevPen(float64(v.tempC), thrTemp)
		rows = append(rows, m.diagGauge("temp", t.gaugeBar(float64(v.tempC)/85, gaugeW, pen),
			pen.Render(fmt.Sprintf("%d °C", v.tempC)), "   SoC", w))
	}
	if d.SysInfo != nil {
		if up := fmtUptime(d.SysInfo.Up); up != "—" {
			rows = append(rows, m.diagLine("uptime", t.pens().txt.render(up)))
		}
	}
	return rows
}

func (m *model) appendDiagStackedSection(lines []string, title string, rows []string, w int) []string {
	lines = append(lines, m.dividerRow(title, w))
	// Clip every row to the body width — the stacked counterpart of the cards
	// section() clip: one long device-supplied value (an SSID, the configured
	// host, an IPv6 address) must degrade to a clipped row, not size contentW
	// past the terminal and wrap every overlay line.
	for _, row := range rows {
		lines = append(lines, clipStyled(row, w))
	}
	return lines
}

func (m *model) diagStackedContent(d protocol.DiagnosticSnapshot, v diagVitals, now time.Time, w, gaugeW int) []string {
	t := m.sty
	hr, hrW, _ := m.diagStatus(d.Snapshot.Connected, d.LastData, now)
	lines := []string{between(t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics"), hr, hrW, w), ""}
	lines = m.appendDiagStackedSection(lines, "audio", m.diagStackedAudioRows(d, v, w, gaugeW), w)
	lines = m.appendDiagStackedSection(lines, "connection", m.diagStackedConnectionRows(d, now), w)
	lines = m.appendDiagStackedSection(lines, "device", m.diagStackedDeviceRows(d, w), w)
	lines = m.appendDiagStackedSection(lines, "hardware", m.diagStackedHardwareRows(w), w)
	lines = m.appendDiagStackedSection(lines, "network", m.diagStackedNetworkRows(d, w, gaugeW), w)
	lines = m.appendDiagStackedSection(lines, "resources", m.diagStackedResourceRows(d, v, w, gaugeW), w)
	lines = m.appendDiagStackedSection(lines, "services", m.serviceStripFor(d.ConfInfo, w), w)
	return lines
}

// ---- wide-layout composition --------------------------------------------------

const (
	diagCardsGutter = 4
	diagCardsGaugeW = 12
)

type diagSection struct {
	title string
	rows  []string
}

// diagCardFmt owns the repeated row primitives for the wide layout. Keeping
// clipping and label/gauge arithmetic here makes the section collectors about
// diagnostics content rather than terminal mechanics.
type diagCardFmt struct {
	m     *model
	inner int
}

func (f diagCardFmt) plain(label, value string, pen lipgloss.Style) string {
	t := f.m.sty
	return t.pens().dim.render(label) + labelGap(label, diagLabelW) +
		pen.Render(Clip(value, max(1, f.inner-diagLabelW)))
}

func (f diagCardFmt) styled(label, value string) string {
	return f.m.sty.pens().dim.render(label) + labelGap(label, diagLabelW) + value
}

func (f diagCardFmt) gauge(label, value string, frac float64, pen lipgloss.Style, detail string) string {
	t := f.m.sty
	out := t.pens().dim.render(label) + labelGap(label, diagLabelW) +
		t.gaugeBar(frac, diagCardsGaugeW, pen) + "  " + pen.Render(value)
	if detail != "" {
		if d := Clip(detail, f.inner-(diagLabelW+diagCardsGaugeW+2+DispW(value))-1); d != "" {
			out += " " + t.pens().dmr.render(d)
		}
	}
	return out
}

func (f diagCardFmt) section(sec diagSection, w int) []string {
	t := f.m.sty
	fill := max(w-3-DispW(sec.title), 0) // "─ " + title + " "
	head := t.pens().dmr.render("─ ") + t.sAcc.Bold(true).Render(sec.title) +
		t.pens().dmr.render(" "+strings.Repeat("─", fill))
	out := make([]string, 0, len(sec.rows)+1)
	out = append(out, head)
	for _, row := range sec.rows {
		out = append(out, "  "+clipStyled(row, w-2))
	}
	return out
}

func diagWorst(v diagVitals, lastRx, now time.Time) int {
	worst := 0
	bump := func(sv int) { worst = max(worst, sv) }
	if v.haveCPU {
		bump(sev(v.cpuFrac*100, thrCPU))
	}
	if v.haveMem {
		bump(sev(v.memUf*100, thrMem))
	}
	if v.haveTemp {
		bump(sev(float64(v.tempC), thrTemp))
	}
	if v.haveData {
		bump(sev(v.dataUf*100, thrData))
	}
	if v.haveBuf && v.playing {
		bump(v.bufSev)
	}
	if !lastRx.IsZero() {
		bump(sev(now.Sub(lastRx).Seconds(), thrRx))
	}
	return worst
}

func (m *model) diagCardMasthead(d protocol.DiagnosticSnapshot, v diagVitals, now time.Time, w int) string {
	t := m.sty
	hr, hrW, silent := m.diagStatus(d.Snapshot.Connected, d.LastData, now)
	left, leftW := t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics")
	if d.Snapshot.Connected && !silent {
		word, pen := "healthy", t.sAcc
		switch diagWorst(v, d.LastRx, now) {
		case 1:
			word, pen = "warn", stWarn
		case 2:
			word, pen = "fault", stRed
		}
		verdict := "● " + word
		left += "   " + pen.Render(verdict)
		leftW += 3 + DispW(verdict)
	}
	return between(left, leftW, hr, hrW, w)
}

func (m *model) diagCardDeviceRows(d protocol.DiagnosticSnapshot, f diagCardFmt) []string {
	facts := identityFacts(d)
	rows := make([]string, 0, len(facts))
	for _, fact := range facts {
		rows = append(rows, f.plain(fact.k, fact.v, m.sty.sTxt))
	}
	return rows
}

func (m *model) diagCardConnectionRows(d protocol.DiagnosticSnapshot, now time.Time, f diagCardFmt) []string {
	ls := m.linkStatus(d.LastRx, now, d.ConnectAttempts, d.EQConnected)
	return []string{
		f.plain("host", m.hostReadout(d.DevInfo), m.sty.sTxt),
		f.styled("ssh", m.sshReadout(ls, d.ConnectAttempts)),
		f.styled("tunnel", m.tunnelReadout(ls)),
	}
}

func (m *model) diagCardSignalRow(d protocol.DiagnosticSnapshot, f diagCardFmt) (string, bool) {
	dev, si := d.DevInfo, d.SysInfo
	if dev == nil || dev.Net != "wifi" || si == nil {
		return "", false
	}
	dbm, err := strconv.Atoi(si.SignalDBm)
	if err != nil {
		return "", false
	}
	pen := m.sevPen(float64(-dbm), thrSignal)
	detail := ""
	if noise, e := strconv.Atoi(si.NoiseDBm); e == nil && noise < 0 {
		detail = fmt.Sprintf("snr %d dB", dbm-noise)
	} else if link, e := strconv.Atoi(si.LinkQ); e == nil && link > 0 {
		detail = fmt.Sprintf("link %d/70", link)
	}
	return f.gauge("signal", fmt.Sprintf("%d dBm", dbm), float64(dbm+90)/60, pen, detail), true
}

func (m *model) diagCardNetworkRows(d protocol.DiagnosticSnapshot, f diagCardFmt) []string {
	t, dev, netv := m.sty, d.DevInfo, d.Net
	haveDev := dev != nil && (dev.IP != "" || dev.Net != "")
	var rows []string
	if haveDev {
		rows = append(rows, f.styled("address", t.pens().txt.render(orDash(dev.IP))+t.pens().dim.render(" · gw "+orDash(dev.Gateway))))
		if dev.DNS != "" {
			rows = append(rows, f.plain("dns", dev.DNS, t.sTxt))
		}
	}
	if netv.ErrsOK {
		rows = append(rows, f.styled("errors", m.errReadout(netv)))
	}
	if haveDev {
		if dev.Net == "wifi" {
			rows = append(rows, f.styled("link", t.pens().bri.render("wi-fi")+t.pens().dim.render(" · ")+t.pens().txt.render(orDash(dev.SSID))+t.pens().dim.render(wifiBand(dev.Freq))))
		} else {
			rows = append(rows, f.styled("link", t.pens().bri.render("ethernet")+t.pens().dim.render(ethDetail(dev.Speed, dev.Duplex))))
		}
		if dev.MAC != "" {
			rows = append(rows, f.plain("mac", dev.MAC, t.sTxt))
		}
	}
	if d.Multiroom != nil {
		rows = append(rows, f.styled("multiroom", m.multiroomReadout(d.Multiroom)))
	}
	if haveDev && dev.Net == "wifi" {
		if dev.Rate != "" {
			rows = append(rows, f.plain("rate", dev.Rate+" Mbit/s", t.sTxt))
		}
		if signal, ok := m.diagCardSignalRow(d, f); ok {
			rows = append(rows, signal)
		}
	}
	if haveDev && netv.RatesOK {
		rows = append(rows, f.styled("traffic", t.pens().dim.render("rx ")+t.pens().txt.render(fmtRate(netv.RxRate))+
			t.pens().dim.render(" · tx ")+t.pens().txt.render(fmtRate(netv.TxRate))))
	}
	return rows
}

func (m *model) diagCardLatencyRows(d protocol.DiagnosticSnapshot) []string {
	if d.DevInfo == nil || (d.DevInfo.IP == "" && d.DevInfo.Net == "") {
		return nil
	}
	targets := m.latencyTargets(d.Net)
	rows := make([]string, 0, len(targets))
	for _, target := range targets {
		rows = append(rows, m.latencyRow(target.name, target.ps))
	}
	return rows
}

func (m *model) diagCardHardwareRows(f diagCardFmt) []string {
	rows := make([]string, 0, len(confHardware))
	for _, item := range confHardware {
		rows = append(rows, f.plain(item.k, item.v, m.sty.sTxt))
	}
	return rows
}

func (m *model) diagCardAudioRows(d protocol.DiagnosticSnapshot, v diagVitals, f diagCardFmt) []string {
	var rows []string
	bufPen, bufDetail := m.bufMeter(v)
	if v.haveBuf {
		rows = append(rows, f.gauge("buffer", fmt.Sprintf("%d%%", int(v.bufFill*100+0.5)), v.bufFill, bufPen, bufDetail))
	}
	if dac := m.dacReadout(d.SysInfo, v.playing); dac != "" {
		rows = append(rows, f.styled("dac", dac))
	}
	if nr := m.nightReadout(d.Snapshot); nr != "" {
		rows = append(rows, f.styled("night", nr))
	}
	return append(rows, f.plain("stream", diagFormat(d.Snapshot.Track), m.sty.sTxt))
}

func (m *model) diagCardResourceRows(d protocol.DiagnosticSnapshot, v diagVitals, f diagCardFmt) []string {
	var rows []string
	if v.haveCPU {
		detail := "1m " + v.loads[0]
		if d.SysInfo.CpuKHz != "" {
			if khz, err := strconv.Atoi(d.SysInfo.CpuKHz); err == nil {
				detail += fmt.Sprintf(" · %d MHz", khz/1000)
			}
		}
		rows = append(rows, f.gauge("cpu", fmt.Sprintf("%d%%", int(v.cpuFrac*100+0.5)),
			v.cpuFrac, m.sevPen(v.cpuFrac*100, thrCPU), detail))
	}
	if v.haveMem {
		detail := fmt.Sprintf("%d/%d MB free", v.availKB/1024, v.totalKB/1024)
		rows = append(rows, f.gauge("memory", fmt.Sprintf("%d%%", int(v.memUf*100+0.5)),
			v.memUf, m.sevPen(v.memUf*100, thrMem), detail))
	}
	if v.haveData {
		detail := fmt.Sprintf("%d/%d MB /lsync", v.usedKB/1024, v.dataKB/1024)
		rows = append(rows, f.gauge("storage", fmt.Sprintf("%d%%", int(v.dataUf*100+0.5)),
			v.dataUf, m.sevPen(v.dataUf*100, thrData), detail))
	}
	if tasks := m.tasksReadout(d.SysInfo); tasks != "" {
		rows = append(rows, f.styled("tasks", tasks))
	}
	if v.haveTemp {
		rows = append(rows, f.gauge("temp", fmt.Sprintf("%d °C", v.tempC), float64(v.tempC)/85,
			m.sevPen(float64(v.tempC), thrTemp), "SoC"))
	}
	if d.SysInfo != nil {
		if up := fmtUptime(d.SysInfo.Up); up != "—" {
			rows = append(rows, f.plain("uptime", up, m.sty.sTxt))
		}
	}
	return rows
}

func (m *model) diagCardSections(d protocol.DiagnosticSnapshot, v diagVitals, now time.Time, f diagCardFmt) []diagSection {
	candidates := []diagSection{
		{"audio", m.diagCardAudioRows(d, v, f)},
		{"connection", m.diagCardConnectionRows(d, now, f)},
		{"device", m.diagCardDeviceRows(d, f)},
		{"hardware", m.diagCardHardwareRows(f)},
		{"latency", m.diagCardLatencyRows(d)},
		{"network", m.diagCardNetworkRows(d, f)},
		{"resources", m.diagCardResourceRows(d, v, f)},
		{"services", m.serviceStripFor(d.ConfInfo, f.inner)},
	}
	sections := make([]diagSection, 0, len(candidates))
	for _, section := range candidates {
		if len(section.rows) > 0 {
			sections = append(sections, section)
		}
	}
	return sections
}

func diagSectionsHeight(sections []diagSection) int {
	height := 0
	for i, section := range sections {
		if i > 0 {
			height++
		}
		height += 1 + len(section.rows)
	}
	return height
}

func splitDiagSections(sections []diagSection) int {
	split, best := 0, 1<<30
	for i := 0; i <= len(sections); i++ {
		delta := diagSectionsHeight(sections[:i]) - diagSectionsHeight(sections[i:])
		if distance := max(delta, -delta); distance < best {
			split, best = i, distance
		}
	}
	return split
}

func diagColumn(f diagCardFmt, sections []diagSection, w int) []string {
	var rows []string
	for i, section := range sections {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, f.section(section, w)...)
	}
	return rows
}

// ---- the two layouts ------------------------------------------------------------

// renderDiag picks the diagnostics layout by width: a two-column card grid on a
// wide terminal (filling the space and surfacing the audio-chain metrics), the
// stacked single-column read-out when narrow.
func (m *model) renderDiag(s protocol.Snapshot, now time.Time, W int) []string {
	d := m.st.DiagnosticView(now)
	d.Snapshot = s // preserve the explicit snapshot contract used by focused tests
	return m.renderDiagnostic(d, now, W)
}

func (m *model) renderDiagnostic(d protocol.DiagnosticSnapshot, now time.Time, W int) []string {
	if W >= diagCardsMinW {
		return m.renderDiagCardsSnapshot(d, now, W)
	}
	return m.renderDiagStackedSnapshot(d, now, W)
}

func (m *model) renderDiagStackedSnapshot(d protocol.DiagnosticSnapshot, now time.Time, W int) []string {
	t := m.sty
	s := d.Snapshot
	gaugeW := max(min(20, W-52), 8) // leaves room for label/value/detail
	L := m.diagStackedContent(d, collectVitals(d.SysInfo, d.DevInfo), now, W, gaugeW)

	// footer (and any device error) pins to the bottom; the gap fills the frame
	var tail []string
	if line, ok := diagErrLine(s, now, W); ok {
		tail = append(tail, line, "")
	}
	tail = append(tail, t.pens().dmr.render(diagFooter))

	// on a too-short pane, trim the read-out from the bottom and flag it
	if room := m.rows - 2 - len(tail); room > 2 && len(L) > room {
		L = L[:room]
		L[room-1] = t.pens().dmr.render("… resize for more")
	}
	return frameBody(L, tail, m.rows-2, false) // top-aligned: read-out hugs the top, footer stays pinned below
}

// renderDiagCards is the wide diagnostics layout: a minimal masthead — the
// title, a health VERDICT (the worst-of rollup of the live signals), and the
// clock — over a heavy rule, then the detail in two boxless, ruled columns.
// The sections run in alphabetical order, flowing down the left column and
// continuing down the right, with the split chosen to balance the two heights.
// No card boxes — the section rule + a left gutter of aligned labels carry the
// structure, so it reads faster and sits a couple lines shorter than the old
// 7-card grid.
func (m *model) renderDiagCards(s protocol.Snapshot, now time.Time, W int) []string {
	d := m.st.DiagnosticView(now)
	d.Snapshot = s
	return m.renderDiagCardsSnapshot(d, now, W)
}

func (m *model) renderDiagCardsSnapshot(d protocol.DiagnosticSnapshot, now time.Time, W int) []string {
	t := m.sty
	s := d.Snapshot
	vit := collectVitals(d.SysInfo, d.DevInfo)
	colW := (W - diagCardsGutter) / 2
	rightW := W - diagCardsGutter - colW // absorbs the odd column
	format := diagCardFmt{m: m, inner: colW - 2}
	sections := m.diagCardSections(d, vit, now, format)
	split := splitDiagSections(sections)
	left2 := diagColumn(format, sections[:split], colW)
	right2 := diagColumn(format, sections[split:], rightW)
	masthead := m.diagCardMasthead(d, vit, now, W)

	// ---- compose: the status line, a heavy rule, then the zipped columns ----
	content := []string{masthead, t.pens().dmr.render(strings.Repeat("━", W))}
	gut := strings.Repeat(" ", diagCardsGutter)
	blankR := strings.Repeat(" ", rightW)
	for i := 0; i < max(len(left2), len(right2)); i++ {
		l := strings.Repeat(" ", colW)
		if i < len(left2) {
			l = padVis(left2[i], colW)
		}
		r := blankR
		if i < len(right2) {
			r = padVis(right2[i], rightW)
		}
		content = append(content, l+gut+r)
	}

	// footer + a small colour legend so the verdict/ribbon hues decode at a glance.
	legend := t.pens().acc.render("●") + t.pens().dmr.render(" good   ") + stWarn.Render("●") + t.pens().dmr.render(" warn   ") + stRed.Render("●") + t.pens().dmr.render(" fault")
	var tail []string
	if line, ok := diagErrLine(s, now, W); ok {
		tail = append(tail, line, "")
	}
	tail = append(tail, between(t.pens().dmr.render(diagFooter), DispW(diagFooter), legend, DispW("● good   ● warn   ● fault"), W))
	return frameBody(content, tail, m.rows-2, false)
}

// ---- device capabilities + hardware (shown in the diagnostics overlay) -------
//
// "What can this box do, and what is it" — surfaced inside the `?` overlay rather
// than a separate view, so the device identity is never shown twice. The
// streaming-capability matrix is read live from the device (the one-shot @@c block
// — running daemons via pidof, env-gated features via getenv — exposed by
// ConfView); the hardware list encodes the model's verified, invariant facts (see
// arylic-lp10-teardown.md). @@c rides the connect unconditionally, so the matrix is
// already in hand whenever the overlay opens.

// confServices is the capability matrix in display order (alphabetical by label,
// like every diag section's items) — the LP10's *marketed* streaming features
// only. LibreWireless reference-image baggage that this box doesn't actually
// offer (Roon / Alexa / Matter / QPlay — installed but env-gated off, not on
// Arylic's spec sheet; see teardown §13/§7.4) is deliberately omitted. id
// matches the @@c wire key; the on/off grouping is decided live, so each group
// row also reads a-z.
var confServices = []struct{ id, label string }{
	{"airplay", "AirPlay 2"},
	{"bt", "Bluetooth"},
	{"dlna", "DLNA / UPnP"},
	{"cast", "Google Cast"},
	{"qobuz", "Qobuz"},
	{"spotify", "Spotify"},
	{"tidal", "Tidal"},
	{"usb", "USB playback"},
}

// confHardware is the invariant hardware reference for the LP10 (the one model
// this tool targets), alphabetical by label, encoding the teardown's findings
// as corrected by the 2026-08-22 live probes: a line-level streamer, no power
// amp, optical S/PDIF up to 24-bit/192 kHz. The DAC is the front-panel MCU
// itself — an MVSilicon BP10xx Bluetooth-audio SoC running in I2S-in mode,
// which also hosts every tone / EQ-preset / virtual-bass / balance / max-volume
// stage (the :2018 tunnel's controls); the firmware's device tree declares a
// Wolfson WM8904 at I2C 0x1a, but nothing answers there and its mixer
// controls are inert. The audio-chain and compute facts only — live
// memory/link usage is the resources/network cards' job, so nothing here
// repeats a live gauge.
var confHardware = []struct{ k, v string }{
	{"dac", "MVSilicon BP10xx (the MCU) · I2S in · tone/EQ/balance on-chip"},
	{"line in", "3.5 mm aux · ADC unidentified (WM8904 declared, absent)"},
	{"line out", "3.5 mm · 1 Vrms (no power amp)"},
	{"optical", "S/PDIF TOSLINK ≤ 24-bit/192 kHz"},
	{"radio", "dual-band 802.11ac · BT 5.0"},
	{"soc", "Amlogic A113L · 2× Cortex-A35"},
}

// serviceStrip renders the capability matrix (from ConfView) as dense grouped
// rows — "on  ● a ● b …" / "off ○ c ○ d …" — plus the env-gating note. A group
// that outgrows the column WRAPS onto aligned continuation rows (flowGroup)
// rather than clipping, so no service is ever hidden and the dots keep their
// colours at any width. Degrades to a "reading…" line until @@c arrives.
func (m *model) serviceStrip(w int) []string {
	return m.serviceStripFor(m.st.ConfView(), w)
}

func (m *model) serviceStripFor(cv *protocol.ConfInfo, w int) []string {
	if cv == nil {
		return []string{clipStyled(m.sty.pens().dmr.render("reading from device…"), w)}
	}
	var on, off []string
	for _, sv := range confServices {
		if cv.Svc[sv.id] == "on" {
			on = append(on, m.sty.pens().acc.render("●")+" "+m.sty.pens().txt.render(sv.label))
		} else {
			off = append(off, m.sty.pens().dmr.render("○")+" "+m.sty.pens().dim.render(sv.label))
		}
	}
	rows := m.flowGroup("on", on, w)
	rows = append(rows, m.flowGroup("off", off, w)...)
	rows = append(rows, m.flowGroup("open", m.exposedItems(cv), w)...)
	rows = append(rows, m.sty.pens().dmr.render("env-gated · toggle in the Arylic app"))
	// Budget every row to w (visible cols) — after the wrap this only bites on a
	// single item wider than the whole column, or the note at a tiny width.
	for i, r := range rows {
		rows[i] = clipStyled(r, w)
	}
	return rows
}

// confExposed are the unauthenticated listeners the loop checks (key = the
// @@c id), with the port and whether reaching it is a security concern —
// telnet and adb hand out a root shell to anyone on the LAN; the web config
// page and the :2018 control tunnel are the vendor's design (no credentials
// either, but they're what the app uses).
var confExposed = []struct {
	id, label string
	risky     bool
}{
	{"telnet", "telnet :23", true},
	{"adb", "adb :5555", true},
	{"web", "web :80", false},
	{"control", "control :2018", false},
}

// exposedItems renders the listening unauthenticated ports for the "open"
// group: risky ones in the warn colour, the by-design ones dim. Nothing is
// listed for a loop that didn't report them (older loop / unreadable
// /proc/net/tcp), so the group row disappears rather than claiming "closed".
func (m *model) exposedItems(cv *protocol.ConfInfo) []string {
	ps := m.sty.pens()
	var items []string
	for _, e := range confExposed {
		if cv.Svc[e.id] != "on" {
			continue
		}
		if e.risky {
			items = append(items, ps.warn.render("●")+" "+ps.warn.render(e.label))
		} else {
			items = append(items, ps.dmr.render("●")+" "+ps.dim.render(e.label))
		}
	}
	return items
}

// flowGroup flows one service group into rows at most w wide, separated by
// single spaces (the ● / ○ dots already separate the items visually). The
// 4-column group label heads the first row — "on  " / "off " keep the dots
// aligned across groups — and continuation rows indent to sit under the items.
func (m *model) flowGroup(label string, items []string, w int) []string {
	if len(items) == 0 {
		return nil
	}
	const indent = 4
	var out []string
	line, lineW := m.sty.pens().dim.render(label)+strings.Repeat(" ", indent-len(label)), indent
	for _, it := range items {
		itW := lipgloss.Width(it)
		if lineW > indent && lineW+1+itW > w { // +1: the separating space
			out = append(out, line)
			line, lineW = strings.Repeat(" ", indent), indent
		}
		if lineW > indent {
			line, lineW = line+" ", lineW+1
		}
		line, lineW = line+it, lineW+itW
	}
	return append(out, line)
}

// ---- row primitives & formatters -----------------------------------------------

// fmtKHz renders a sample rate in kHz: "44.1 kHz", "48 kHz", "96 kHz".
func fmtKHz(hz int) string {
	if hz%1000 == 0 {
		return strconv.Itoa(hz/1000) + " kHz"
	}
	return strconv.FormatFloat(float64(hz)/1000, 'f', 1, 64) + " kHz"
}

// clipStyled clips an already-styled string to display width w, keeping the
// styling: ansi.Truncate cuts between escape sequences (measuring width the way
// lipgloss does, so it agrees with padVis) and every segment left of the cut
// keeps its colour. It used to strip-and-re-dim instead, which flattened a
// clipped services/eq row to a uniform grey the moment a larger font cost the
// column a couple of cells.
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

// gridRow renders a two-column "label value | label value" row, exactly W wide.
func (m *model) gridRow(k1, v1, k2, v2 string, W int) string {
	half := W / 2
	return m.cellKV(k1, v1, half) + m.cellKV(k2, v2, W-half)
}

func (m *model) cellKV(k, v string, w int) string {
	const labW = 9
	vv := Clip(v, w-labW)
	out := m.sty.pens().dim.render(k) + labelGap(k, labW) + m.sty.pens().txt.render(vv)
	if vis := labW + DispW(vv); vis < w {
		out += strings.Repeat(" ", w-vis)
	}
	return out
}

// diagLine renders "label  value" with a fixed dim label column.
func (m *model) diagLine(label, value string) string {
	return m.sty.pens().dim.render(label) + labelGap(label, diagLabelW) + value
}

// diagGauge renders "label  [gauge]  value detail", clipping the dim detail to the
// body width w so a long detail (e.g. the cpu load triplet at a narrow terminal)
// can't size the row past the frame — the stacked counterpart to the cards cg()
// detail clip. Pass detail="" for a gauge with no trailing note.
func (m *model) diagGauge(label, gauge, value, detail string, w int) string {
	row := m.sty.pens().dim.render(label) + labelGap(label, diagLabelW) + gauge + "  " + value
	if detail != "" {
		row += m.sty.pens().dmr.render(Clip(detail, w-lipgloss.Width(row))) // Clip("",<=0)→""
	}
	return clipStyled(row, w) // never exceed the body width (a no-op when it fits)
}

func freqToChan(mhz int) int {
	switch {
	case mhz == 2484:
		return 14
	case mhz >= 2412 && mhz <= 2472:
		return (mhz-2412)/5 + 1
	case mhz >= 5000:
		return (mhz - 5000) / 5
	}
	return 0
}

// fmtRate renders a bytes/sec throughput in the largest unit that keeps it ≥1.
func fmtRate(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f KB/s", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

// fmtLatencyMs renders a millisecond latency with one decimal under 10ms (sub-ms
// LAN hops would otherwise round to a meaningless "0"), whole numbers above.
// (Distinct from FmtMs(int), which formats a track position as MM:SS.)
func fmtLatencyMs(ms float64) string {
	if ms < 10 {
		return fmt.Sprintf("%.1f", ms)
	}
	return fmt.Sprintf("%.0f", ms)
}

// latencyRow renders one target — name, average, jitter, and the window peak
// (amber once a real spike has landed, so an intermittent glitch is visible
// after the fact). The fields are fixed-width so the columns line up across the
// three rows. Plain text on purpose: the earlier per-row sparkline rendered as
// ragged block glyphs on fonts whose block elements don't fill the cell.
func (m *model) latencyRow(name string, ps protocol.PingStat) string {
	t := m.sty
	return t.pens().dim.render(padDisp(name, latNameW)) +
		t.pens().txt.render(rpadDisp(fmtLatencyMs(ps.Avg), latAvgW)+latAvgUnit) + " " +
		t.pens().dmr.render(padDisp("±"+fmtLatencyMs(ps.Jitter), latJitW)) + " " +
		m.latencyPeakPen(ps).Render("max "+fmtLatencyMs(ps.Peak))
}

// pingLabel shortens the configured internet target for the latency row: an IP
// is shown whole, a hostname collapses to its second-level domain
// (apresolve.spotify.com → spotify).
func pingLabel(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "net"
	}
	parts := strings.Split(host, ".")
	if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		return host // numeric final label → an IPv4 address; show it whole
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return host
}

func fmtUptime(up string) string {
	secs, err := strconv.ParseFloat(strings.TrimSpace(up), 64)
	if err != nil || secs < 0 {
		return "—"
	}
	s := int(secs)
	switch d, h, mn := s/86400, s%86400/3600, s%3600/60; {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, mn)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, mn)
	default:
		return fmt.Sprintf("%dm", mn)
	}
}

const (
	// diagLabelW is the dim label column shared by every diagnostics row (see
	// diagLine / diagGauge): the label, left-padded to this width, then the value.
	diagLabelW = 10

	// The latency row's fixed fields, in render order (see latencyRow).
	latNameW   = 8     // target name (left-padded)
	latAvgW    = 4     // average ms (right-aligned), before its unit
	latAvgUnit = " ms" // the avg field's trailing unit
	latJitW    = 5     // ±jitter
)

func orDash(s string) string { return cmp.Or(s, "—") }

func firstSeg(s string, sep byte) string {
	if before, _, ok := strings.Cut(s, string(sep)); ok {
		return before
	}
	return s
}

// labelGap is the space run after a fixed-width diagnostics label: the column
// width minus the label's display width, floored at 0 so a label wider than its
// column can never produce a negative (panicking) repeat count.
func labelGap(label string, col int) string {
	return strings.Repeat(" ", max(0, col-DispW(label)))
}
