package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// diagCardsMinW is the inner width at/above which the diagnostics overlay uses the
// two-column card grid; below it, the single-column stacked layout (which fits a
// narrow terminal and degrades gracefully) is used instead.
const diagCardsMinW = 100

// diagIdentity is the static device-identity readout shared by both diagnostics
// layouts (renderDiagStacked / renderDiagCards), so the two can't drift apart.
type diagIdentity struct {
	host, model, os, fw, cores, up, build, mac string
}

// collectIdentity derives the identity strings from the sysinfo/devinfo (either may
// be nil). host starts from the configured target and gains the resolved IP, plus an
// mDNS tag when discovery found the device.
func (m *model) collectIdentity(si *protocol.SysInfo, dev *protocol.DevInfo) diagIdentity {
	d := diagIdentity{host: m.cfg.User + "@" + m.cfg.Host,
		model: "—", os: "—", fw: "—", cores: "—", up: "—", build: "—", mac: "—"}
	if si != nil {
		if si.FW != "" {
			d.fw, d.model = si.FW, "Arylic "+firstSeg(si.FW, '_')
		}
		if si.OS != "" {
			d.os = strings.Replace(si.OS, "-", " ", 1)
		}
		if si.NCPU != "" {
			d.cores = si.NCPU
		}
		d.up = fmtUptime(si.Up)
	}
	if dev != nil {
		if dev.IP != "" {
			d.host = m.cfg.User + "@" + dev.IP
		}
		if dev.Platform != "" && d.model != "—" {
			d.model += " · " + dev.Platform
		}
		if dev.Build != "" {
			d.build = dev.Build
			if dev.App != "" {
				d.build += " · app " + dev.App
			}
		}
		if dev.MAC != "" {
			d.mac = dev.MAC
		}
	}
	if m.cfg.Discovered {
		d.host += " · mDNS"
	}
	return d
}

// renderDiag picks the diagnostics layout by width: a two-column card grid on a
// wide terminal (filling the space and surfacing the audio-chain metrics), the
// stacked single-column read-out when narrow.
func (m *model) renderDiag(s protocol.Snapshot, now time.Time, W int) string {
	if W >= diagCardsMinW {
		return m.renderDiagCards(s, now, W)
	}
	return m.renderDiagStacked(s, now, W)
}

func (m *model) renderDiagStacked(s protocol.Snapshot, now time.Time, W int) string {
	t := m.sty
	lastRx, dData, att, derr, si := m.st.DiagView()
	dev := m.st.DevInfoView()
	netv := m.st.NetView()
	eqConn, eqv := m.st.EQView()

	gw := max(min(20, W-52), 8) // gauge width, leaving room for label/value/detail
	// lower-is-better health picker (good < a <= warn < b <= bad)
	lo := func(v, a, b float64) lipgloss.Style {
		switch {
		case v < a:
			return t.sAcc
		case v < b:
			return stWarn
		default:
			return stRed
		}
	}

	var L []string
	add := func(s string) { L = append(L, s) }

	clock := now.Format("15:04")
	var hr string
	var hrW int
	switch {
	case !s.Connected:
		hr, hrW = stWarn.Render("● disconnected"), DispW("● disconnected")
	case !dData.IsZero() && now.Sub(dData) > workers.SilentAfter:
		// Match the watchdog's silence threshold (not a tighter one): the device's
		// idle loop legitimately drops to a ~3s poll cadence, so a shorter window
		// would flash "LUCI silent" between healthy low-poll frames.
		hr, hrW = stWarn.Render("● LUCI silent · "+clock), DispW("● LUCI silent · "+clock)
	default:
		hr, hrW = t.sAcc.Render("●")+t.sDim.Render(" "+clock), DispW("● "+clock)
	}
	add(between(t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics"), hr, hrW, W))
	add("")

	id := m.collectIdentity(si, dev)
	add(m.gridRow("host", id.host, "uptime", id.up, W))
	add(m.gridRow("device", id.model, "os", id.os, W))
	add(m.gridRow("firmware", id.fw, "build", id.build, W))
	add(m.gridRow("mac", id.mac, "cores", id.cores, W))

	add(m.dividerRow("network", W))
	rxTxt, rxPen := "—", t.sDim
	if !lastRx.IsZero() {
		secs := now.Sub(lastRx).Seconds()
		rxTxt, rxPen = fmt.Sprintf("%.1fs", secs), lo(secs, 3, 8)
	}
	attWord := "attempts"
	if att == 1 {
		attWord = "attempt"
	}
	tunTxt, tunPen := "down", stRed
	if eqConn {
		tunTxt, tunPen = "live", t.sAcc
	}
	if dev != nil && (dev.IP != "" || dev.Net != "") {
		if dev.Net == "wifi" {
			band := ""
			if f, err := strconv.Atoi(dev.Freq); err == nil && f > 0 {
				b := " · 2.4 GHz"
				if f >= 5000 {
					b = " · 5 GHz"
				}
				band = fmt.Sprintf(" · ch %d%s", freqToChan(f), b)
			}
			add(m.diagLine("link", t.sBri.Render("wi-fi")+t.sDim.Render(" · ")+t.sTxt.Render(orDash(dev.SSID))+t.sDim.Render(band)))
			if si != nil {
				if dbm, err := strconv.Atoi(si.SignalDBm); err == nil {
					pen := lo(float64(-dbm), 60, 72) // -dBm: 41 good, 72 warn
					valTxt := fmt.Sprintf("%d dBm", dbm)
					detail := ""
					if dev.Rate != "" {
						detail = "   " + dev.Rate + " Mbit/s"
					}
					if lq, e := strconv.Atoi(si.LinkQ); e == nil && lq > 0 {
						detail += fmt.Sprintf("  · link %d/70", lq)
					}
					add(m.diagGauge("signal", t.gaugeBar(float64(dbm+90)/60, gw, pen), pen.Render(valTxt), detail, W))
				}
			}
		} else {
			detail := ""
			if sp, err := strconv.Atoi(dev.Speed); err == nil && sp > 0 {
				detail += fmt.Sprintf(" · %d Mbit/s", sp)
			}
			if dev.Duplex != "" {
				detail += " · " + dev.Duplex + " duplex"
			}
			add(m.diagLine("link", t.sBri.Render("ethernet")+t.sDim.Render(detail)))
		}
		add(m.diagLine("address", t.sTxt.Render(orDash(dev.IP))+t.sDim.Render(" · gw "+orDash(dev.Gateway))))
		if netv.RatesOK {
			add(m.diagLine("traffic", t.sDim.Render("rx ")+t.sTxt.Render(fmtRate(netv.RxRate))+
				t.sDim.Render(" · tx ")+t.sTxt.Render(fmtRate(netv.TxRate))))
		}
		// one row per latency target: avg · jitter · peak · a sparkline of the
		// rolling window. The sparkline fills the remaining width (its column
		// starts after the fixed numeric fields), bounded by the ring size.
		sparkW := min(pingHistory, W-latencyFixedCols)
		names := [3]string{"you", "gw", pingLabel(m.cfg.PingHost)}
		latLabel := "latency"
		for i, ps := range netv.Ping {
			if !ps.OK {
				continue
			}
			add(m.diagLine(latLabel, m.latencyRow(names[i], ps, sparkW)))
			latLabel = ""
		}
	}
	// lp10's own connection to the device, folded into the network section.
	add(m.diagLine("player", t.sTxt.Render("ssh stream · rx ")+rxPen.Render(rxTxt)+
		t.sTxt.Render(fmt.Sprintf(" ago · %d %s", att, attWord))))
	add(m.diagLine("control", t.sTxt.Render("tunnel :2018 · ")+tunPen.Render(tunTxt)))

	add(m.dividerRow("audio", W))
	formatTxt := "—"
	if tr := s.Track; tr != nil {
		var ps []string
		if q := Quality(tr); q != "" {
			ps = append(ps, q)
		}
		if ch := tr.GetInt("ChannelCount"); ch > 0 {
			ps = append(ps, fmt.Sprintf("%d ch", ch))
		}
		if len(ps) > 0 {
			formatTxt = strings.Join(ps, " · ")
		}
	}
	add(m.diagLine("format", t.sTxt.Render(formatTxt)))
	volPen, volTxt := t.sAcc, fmt.Sprintf("%d%%", s.Vol)
	if s.Muted {
		volPen, volTxt = stRed, "muted"
	}
	add(m.diagGauge("volume", t.gaugeBar(float64(s.Vol)/100, gw, volPen), volPen.Render(volTxt), "", W))
	add(m.diagLine("eq", m.eqReadout(eqv)))

	add(m.dividerRow("resources", W))
	if si != nil {
		loads := strings.Fields(si.Load)
		nc, _ := strconv.Atoi(si.NCPU)
		if nc < 1 {
			nc = 1
		}
		if len(loads) >= 1 {
			if l1, err := strconv.ParseFloat(loads[0], 64); err == nil {
				frac := l1 / float64(nc)
				pen := lo(frac*100, 60, 85)
				detail := "   1m " + loads[0]
				if len(loads) >= 3 {
					detail += " · 5m " + loads[1] + " · 15m " + loads[2]
				}
				add(m.diagGauge("cpu", t.gaugeBar(frac, gw, pen),
					pen.Render(fmt.Sprintf("%d%%", int(frac*100+0.5))), detail, W))
			}
		}
		av, e1 := strconv.Atoi(si.Avail)
		tot, e2 := strconv.Atoi(si.Total)
		if e1 == nil && e2 == nil && tot > 0 {
			uf := float64(tot-av) / float64(tot)
			pen := lo(uf*100, 70, 88)
			add(m.diagGauge("memory", t.gaugeBar(uf, gw, pen),
				pen.Render(fmt.Sprintf("%d%%", int(uf*100+0.5))),
				fmt.Sprintf("   %d / %d MB free", av/1024, tot/1024), W))
		}
		if mc, err := strconv.Atoi(si.TempmC); err == nil {
			c := mc / 1000
			pen := lo(float64(c), 60, 75)
			add(m.diagGauge("temp", t.gaugeBar(float64(c)/85, gw, pen),
				pen.Render(fmt.Sprintf("%d °C", c)), "   SoC", W))
		}
	}
	if dev != nil {
		used, e1 := strconv.Atoi(dev.DataUsed)
		tot, e2 := strconv.Atoi(dev.DataTotal)
		if e1 == nil && e2 == nil && tot > 0 {
			uf := float64(used) / float64(tot)
			pen := lo(uf*100, 80, 92)
			add(m.diagGauge("storage", t.gaugeBar(uf, gw, pen),
				pen.Render(fmt.Sprintf("%d%%", int(uf*100+0.5))),
				fmt.Sprintf("   %d / %d MB used · data", used/1024, tot/1024), W))
		}
	}

	add(m.dividerRow("hardware", W))
	for _, h := range confHardware {
		add(m.diagLine(h.k, t.sTxt.Render(Clip(h.v, max(1, W-diagLabelW)))))
	}

	add(m.dividerRow("services", W))
	for _, r := range m.serviceStrip(W) {
		add(r)
	}

	// footer (and any device error) pins to the bottom; the gap fills the frame
	var tail []string
	if derr != "" {
		// prettified, not the raw ssh dump — the overlay already shows the state
		// (disconnected · tunnel down · N attempts), so keep only the readable reason
		tail = append(tail, stWarn.Render(Clip(GL["warn"]+" "+friendlyError(derr), W)), "")
	}
	tail = append(tail, t.sDmr.Render("live · any key returns to the dashboard"))

	// on a too-short pane, trim the read-out from the bottom and flag it
	if room := m.rows - 2 - len(tail); room > 2 && len(L) > room {
		L = L[:room]
		L[room-1] = t.sDmr.Render("… resize for more")
	}
	return strings.Join(frameBody(L, tail, m.rows-2, false), "\n") // top-aligned: read-out hugs the top, footer stays pinned below
}

// renderDiagCards is the wide-terminal diagnostics layout: a two-column grid of
// titled cards balanced by content — left: device · connection · network (the
// static identity/connectivity side); right: audio · resources · latency (the live
// metrics). Filling the space the stacked view left empty and surfacing the
// audio-chain / contention metrics. Sparklines and gauges get full card width.
// renderDiagCards is the wide diagnostics layout — the "vitals ribbon" design: a
// masthead carrying a health VERDICT, a one-line color-coded vitals ribbon under a
// heavy rule (health at a glance), then the detail in two boxless, ruled columns
// (identity left, live right). No card boxes — the section rule + a left gutter of
// aligned labels carry the structure, so it reads faster and sits a couple lines
// shorter than the old 7-card grid.
func (m *model) renderDiagCards(s protocol.Snapshot, now time.Time, W int) string {
	t := m.sty
	lastRx, dData, att, derr, si := m.st.DiagView()
	dev := m.st.DevInfoView()
	netv := m.st.NetView()
	eqConn, eqv := m.st.EQView()

	// severity (0 good · 1 warn · 2 bad) and the matching pen — one shared rule so a
	// ribbon chip, its gauge, and the verdict rollup can never disagree.
	sevPen := [3]lipgloss.Style{t.sAcc, stWarn, stRed}
	sev := func(v, a, b float64) int {
		switch {
		case v < a:
			return 0
		case v < b:
			return 1
		default:
			return 2
		}
	}
	lo := func(v, a, b float64) lipgloss.Style { return sevPen[sev(v, a, b)] }
	worst := 0
	bump := func(sv int) {
		if sv > worst {
			worst = sv
		}
	}

	// two asymmetric columns: identity left, live right (the live side carries the
	// wider service strip, the eq line, and the sparklines).
	const (
		gutter = 4
		gwc    = 12 // gauge width
	)
	leftW := max((W-gutter)*44/100, 30)
	rightW := W - gutter - leftW
	innerL, innerR := leftW-2, rightW-2 // rows sit under a 2-space indent

	kvP := func(inner int, label, value string, pen lipgloss.Style) string {
		return t.sDim.Render(label) + labelGap(label, diagLabelW) + pen.Render(Clip(value, max(1, inner-diagLabelW)))
	}
	kvR := func(label, styled string) string { return t.sDim.Render(label) + labelGap(label, diagLabelW) + styled }
	cg := func(inner int, label, valuePlain string, frac float64, pen lipgloss.Style, detail string) string {
		out := t.sDim.Render(label) + labelGap(label, diagLabelW) + t.gaugeBar(frac, gwc, pen) + "  " + pen.Render(valuePlain)
		if detail != "" {
			if d := Clip(detail, inner-(diagLabelW+gwc+2+DispW(valuePlain))-1); d != "" {
				out += " " + t.sDmr.Render(d)
			}
		}
		return out
	}
	// boxless section: a left-anchored "─ title ─────" head + indented rows.
	sectionHead := func(title string, w int) string {
		fill := max(w-3-DispW(title), 0) // "─ " + title + " "
		return t.sDmr.Render("─ ") + t.sAcc.Bold(true).Render(title) + t.sDmr.Render(" "+strings.Repeat("─", fill))
	}
	section := func(title string, rows []string, w int) []string {
		out := make([]string, 0, len(rows)+1)
		out = append(out, sectionHead(title, w))
		for _, r := range rows {
			out = append(out, "  "+m.clipStyled(r, w-2))
		}
		return out
	}

	// ---- hoist the live numeric vitals: the ribbon (top) and the gauges (below)
	// read the same values, and each feeds the verdict rollup. ----
	var (
		haveCpu, haveMem, haveTemp, haveData, haveBuf bool
		cpuFrac, memUf, dataUf, bufFill               float64
		tempC                                         int
		cpuDetail, memDetail, dataDetail              string
		bufSev                                        int
	)
	if si != nil {
		loads := strings.Fields(si.Load)
		nc, _ := strconv.Atoi(si.NCPU)
		if nc < 1 {
			nc = 1
		}
		if len(loads) >= 1 {
			if l1, err := strconv.ParseFloat(loads[0], 64); err == nil {
				cpuFrac, haveCpu = l1/float64(nc), true
				cpuDetail = "1m " + loads[0]
				if si.CpuKHz != "" {
					if khz, e := strconv.Atoi(si.CpuKHz); e == nil {
						cpuDetail += fmt.Sprintf(" · %d MHz", khz/1000)
					}
				}
			}
		}
		if av, e1 := strconv.Atoi(si.Avail); e1 == nil {
			if tot, e2 := strconv.Atoi(si.Total); e2 == nil && tot > 0 {
				memUf, haveMem = float64(tot-av)/float64(tot), true
				memDetail = fmt.Sprintf("%d/%d MB free", av/1024, tot/1024)
			}
		}
		if mc, err := strconv.Atoi(si.TempmC); err == nil {
			tempC, haveTemp = mc/1000, true
		}
		if si.BufAvail != "" && si.BufSize != "" {
			if a2, e := strconv.Atoi(si.BufAvail); e == nil {
				if bs, e2 := strconv.Atoi(si.BufSize); e2 == nil && bs > 0 {
					bufFill = float64(bs-a2) / float64(bs)
					if bufFill < 0 {
						bufFill = 0
					}
					haveBuf = true
					switch { // buffer health is inverted: a FULL ring is healthy
					case bufFill >= 0.5:
						bufSev = 0
					case bufFill >= 0.25:
						bufSev = 1
					default:
						bufSev = 2
					}
				}
			}
		}
	}
	if dev != nil {
		if u, e1 := strconv.Atoi(dev.DataUsed); e1 == nil {
			if tt, e2 := strconv.Atoi(dev.DataTotal); e2 == nil && tt > 0 {
				dataUf, haveData = float64(u)/float64(tt), true
				dataDetail = fmt.Sprintf("%d/%d MB /lsync", u/1024, tt/1024)
			}
		}
	}
	volPen, volTxt := t.sAcc, fmt.Sprintf("%d%%", s.Vol)
	if s.Muted {
		volPen, volTxt = stRed, "muted"
	}

	// the buffer ring is only a health signal WHILE PLAYING — an empty ring on an
	// idle/paused device is normal, so it stays neutral and out of the verdict then.
	playing := si != nil && si.PcmState == "RUNNING"
	bufPen, bufDetail := t.sDim, "idle"
	if playing {
		bufPen, bufDetail = sevPen[bufSev], "ring"
	}

	// roll the live health signals into the worst-of verdict.
	if haveCpu {
		bump(sev(cpuFrac*100, 60, 85))
	}
	if haveMem {
		bump(sev(memUf*100, 70, 88))
	}
	if haveTemp {
		bump(sev(float64(tempC), 60, 75))
	}
	if haveData {
		bump(sev(dataUf*100, 80, 92))
	}
	if haveBuf && playing {
		bump(bufSev)
	}
	if !lastRx.IsZero() {
		bump(sev(now.Sub(lastRx).Seconds(), 3, 8))
	}

	// ---- status line: title + health verdict + the key live vitals (left), the
	// connection light + clock (right). One dense line — no separate ribbon, no gap. ----
	clock := now.Format("15:04")
	var hr string
	var hrW int
	silent := false
	switch {
	case !s.Connected:
		hr, hrW = stWarn.Render("● disconnected"), DispW("● disconnected")
	case !dData.IsZero() && now.Sub(dData) > workers.SilentAfter:
		hr, hrW, silent = stWarn.Render("● LUCI silent · "+clock), DispW("● LUCI silent · "+clock), true
	default:
		hr, hrW = t.sAcc.Render("●")+t.sDim.Render(" "+clock), DispW("● "+clock)
	}
	left, leftHdrW := t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics")
	if s.Connected && !silent { // a fresh device gets a one-glance health verdict
		verWord, verPen := "healthy", t.sAcc
		switch worst {
		case 1:
			verWord, verPen = "warn", stWarn
		case 2:
			verWord, verPen = "fault", stRed
		}
		vd := "● " + verWord
		left += "   " + verPen.Render(vd)
		leftHdrW += 3 + DispW(vd)
	}
	// the key live vitals, health-coloured, inline after the verdict. (Latency has its
	// own section, so it isn't duplicated here.) Each is appended only while it still
	// fits clear of the clock, so a narrow terminal sheds the trailing ones cleanly.
	type vital struct {
		label, value string
		pen          lipgloss.Style
	}
	var vitals []vital
	if haveTemp {
		vitals = append(vitals, vital{"temp", fmt.Sprintf("%d °C", tempC), sevPen[sev(float64(tempC), 60, 75)]})
	}
	if haveCpu {
		vitals = append(vitals, vital{"cpu", fmt.Sprintf("%d%%", int(cpuFrac*100+0.5)), sevPen[sev(cpuFrac*100, 60, 85)]})
	}
	if haveMem {
		vitals = append(vitals, vital{"mem", fmt.Sprintf("%d%%", int(memUf*100+0.5)), sevPen[sev(memUf*100, 70, 88)]})
	}
	if haveBuf {
		bufVal := fmt.Sprintf("%d%%", int(bufFill*100+0.5))
		if !playing { // an empty ring while stopped is idle, not a reading worth a number
			bufVal = "idle"
		}
		vitals = append(vitals, vital{"buffer", bufVal, bufPen})
	}
	// volume is intentionally NOT a vital here — it's a setting, not a health signal,
	// and it already has its gauge in the audio section.
	for i, v := range vitals {
		seg := t.sDim.Render(v.label) + " " + v.pen.Render(v.value)
		segW := DispW(v.label + " " + v.value)
		if leftHdrW+3+segW > W-hrW-2 { // keep clear of the clock; drop the rest
			break
		}
		if i == 0 {
			left += "   " + seg
		} else {
			left += t.sDmr.Render(" · ") + seg
		}
		leftHdrW += 3 + segW
	}
	masthead := between(left, leftHdrW, hr, hrW, W)

	// ---- left column: device · network · hardware (static identity) ----
	id := m.collectIdentity(si, dev)
	deviceRows := []string{
		kvP(innerL, "host", id.host, t.sTxt),
		kvP(innerL, "device", id.model, t.sTxt),
		kvP(innerL, "os", id.os+" · "+id.cores+" cores", t.sTxt),
		kvP(innerL, "firmware", id.fw, t.sTxt),
		kvP(innerL, "build", id.build, t.sTxt),
		kvP(innerL, "uptime", id.up, t.sTxt),
		kvP(innerL, "mac", id.mac, t.sTxt),
	}

	rxTxt, rxPen := "—", t.sDim
	if !lastRx.IsZero() {
		secs := now.Sub(lastRx).Seconds()
		rxTxt, rxPen = fmt.Sprintf("%.1fs", secs), lo(secs, 3, 8)
	}
	attWord := "attempts"
	if att == 1 {
		attWord = "attempt"
	}
	tunTxt, tunPen := "down", stRed
	if eqConn {
		tunTxt, tunPen = "live", t.sAcc
	}
	var nrows, lrows []string
	if dev != nil && (dev.IP != "" || dev.Net != "") {
		if dev.Net == "wifi" {
			band := ""
			if f, err := strconv.Atoi(dev.Freq); err == nil && f > 0 {
				b := " · 2.4 GHz"
				if f >= 5000 {
					b = " · 5 GHz"
				}
				band = fmt.Sprintf(" · ch %d%s", freqToChan(f), b)
			}
			nrows = append(nrows, kvR("link", t.sBri.Render("wi-fi")+t.sDim.Render(" · ")+t.sTxt.Render(orDash(dev.SSID))+t.sDim.Render(band)))
			if si != nil {
				if dbm, err := strconv.Atoi(si.SignalDBm); err == nil {
					pen := lo(float64(-dbm), 60, 72)
					detail := ""
					if nz, e := strconv.Atoi(si.NoiseDBm); e == nil && nz < 0 {
						detail = fmt.Sprintf("snr %d dB", dbm-nz) // signal − noise
					} else if lq, e := strconv.Atoi(si.LinkQ); e == nil && lq > 0 {
						detail = fmt.Sprintf("link %d/70", lq)
					}
					nrows = append(nrows, cg(innerL, "signal", fmt.Sprintf("%d dBm", dbm), float64(dbm+90)/60, pen, detail))
				}
			}
			if dev.Rate != "" {
				nrows = append(nrows, kvP(innerL, "rate", dev.Rate+" Mbit/s", t.sTxt))
			}
		} else {
			detail := ""
			if sp, err := strconv.Atoi(dev.Speed); err == nil && sp > 0 {
				detail += fmt.Sprintf(" · %d Mbit/s", sp)
			}
			if dev.Duplex != "" {
				detail += " · " + dev.Duplex + " duplex"
			}
			nrows = append(nrows, kvR("link", t.sBri.Render("ethernet")+t.sDim.Render(detail)))
		}
		nrows = append(nrows, kvR("address", t.sTxt.Render(orDash(dev.IP))+t.sDim.Render(" · gw "+orDash(dev.Gateway))))
		if dev.DNS != "" {
			nrows = append(nrows, kvP(innerL, "dns", dev.DNS, t.sTxt))
		}
		if netv.RatesOK {
			nrows = append(nrows, kvR("traffic", t.sDim.Render("rx ")+t.sTxt.Render(fmtRate(netv.RxRate))+t.sDim.Render(" · tx ")+t.sTxt.Render(fmtRate(netv.TxRate))))
		}
		// latency rows (built while netv is in hand; the section is placed right).
		sw := max(innerR-19, 4) // name(6)+avg(6)+1+jit(5)+1
		names := [3]string{"you", "gw", pingLabel(m.cfg.PingHost)}
		for i, ps := range netv.Ping {
			if !ps.OK {
				continue
			}
			peakPen := t.sDmr
			if ps.Peak > ps.Avg*2 && ps.Peak-ps.Avg > 10 {
				peakPen = stWarn
			}
			lrows = append(lrows, t.sDim.Render(padDisp(names[i], 6))+
				t.sTxt.Render(rpadDisp(fmtLatencyMs(ps.Avg)+"ms", 6))+" "+
				peakPen.Render(padDisp("±"+fmtLatencyMs(ps.Jitter), 5))+" "+
				t.sDim.Render(sparkline(ps.Series, sw)))
		}
	}
	// lp10's own link to the device (ssh player stream + :2018 control tunnel).
	nrows = append(nrows,
		kvR("player", t.sTxt.Render("ssh · rx ")+rxPen.Render(rxTxt)+t.sTxt.Render(fmt.Sprintf(" ago · %d %s", att, attWord))),
		kvR("control", t.sTxt.Render("tunnel :2018 · ")+tunPen.Render(tunTxt)))

	hwRows := make([]string, 0, len(confHardware))
	for _, h := range confHardware {
		hwRows = append(hwRows, kvP(innerL, h.k, h.v, t.sTxt))
	}

	left2 := section("device", deviceRows, leftW)
	left2 = append(left2, "")
	left2 = append(left2, section("network", nrows, leftW)...)
	left2 = append(left2, "")
	left2 = append(left2, section("hardware", hwRows, leftW)...)

	// ---- right column: audio · resources · latency · services (live) ----
	formatTxt := "—"
	if tr := s.Track; tr != nil {
		var ps []string
		if q := Quality(tr); q != "" {
			ps = append(ps, q)
		}
		if ch := tr.GetInt("ChannelCount"); ch > 0 {
			ps = append(ps, fmt.Sprintf("%d ch", ch))
		}
		if len(ps) > 0 {
			formatTxt = strings.Join(ps, " · ")
		}
	}
	arows := []string{kvP(innerR, "stream", formatTxt, t.sTxt)}
	if si != nil && si.DacRate != "" {
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
		dac := t.sTxt.Render(strings.Join(parts, " · "))
		if playing {
			dac += t.sAcc.Render(" ● live")
		}
		arows = append(arows, kvR("dac", dac))
	}
	if haveBuf {
		arows = append(arows, cg(innerR, "buffer", fmt.Sprintf("%d%%", int(bufFill*100+0.5)), bufFill, bufPen, bufDetail))
	}
	arows = append(arows, cg(innerR, "volume", volTxt, float64(s.Vol)/100, volPen, ""))
	arows = append(arows, kvR("eq", m.clipStyled(m.eqReadout(eqv), innerR-diagLabelW)))

	var rrows []string
	if haveCpu {
		rrows = append(rrows, cg(innerR, "cpu", fmt.Sprintf("%d%%", int(cpuFrac*100+0.5)), cpuFrac, lo(cpuFrac*100, 60, 85), cpuDetail))
	}
	if si != nil && si.Procs != "" {
		if run, tot, ok := strings.Cut(si.Procs, "/"); ok {
			rrows = append(rrows, kvR("tasks", t.sTxt.Render(run)+t.sDim.Render(" running · ")+t.sTxt.Render(tot)+t.sDim.Render(" total")))
		}
	}
	if haveMem {
		rrows = append(rrows, cg(innerR, "memory", fmt.Sprintf("%d%%", int(memUf*100+0.5)), memUf, lo(memUf*100, 70, 88), memDetail))
	}
	if haveTemp {
		rrows = append(rrows, cg(innerR, "temp", fmt.Sprintf("%d °C", tempC), float64(tempC)/85, lo(float64(tempC), 60, 75), "SoC"))
	}
	if haveData {
		rrows = append(rrows, cg(innerR, "data", fmt.Sprintf("%d%%", int(dataUf*100+0.5)), dataUf, lo(dataUf*100, 80, 92), dataDetail))
	}

	right2 := section("audio", arows, rightW)
	right2 = append(right2, "")
	right2 = append(right2, section("resources", rrows, rightW)...)
	if len(lrows) > 0 {
		right2 = append(right2, "")
		right2 = append(right2, section("latency", lrows, rightW)...)
	}
	right2 = append(right2, "")
	right2 = append(right2, section("services", m.serviceStrip(innerR), rightW)...)

	// ---- compose: the status line, a heavy rule, then the zipped columns ----
	content := []string{masthead, t.sDmr.Render(strings.Repeat("━", W))}
	gut := strings.Repeat(" ", gutter)
	blankR := strings.Repeat(" ", rightW)
	for i := 0; i < max(len(left2), len(right2)); i++ {
		l := strings.Repeat(" ", leftW)
		if i < len(left2) {
			l = padVis(left2[i], leftW)
		}
		r := blankR
		if i < len(right2) {
			r = padVis(right2[i], rightW)
		}
		content = append(content, l+gut+r)
	}

	// footer + a small colour legend so the verdict/ribbon hues decode at a glance.
	legend := t.sAcc.Render("●") + t.sDmr.Render(" good   ") + stWarn.Render("●") + t.sDmr.Render(" warn   ") + stRed.Render("●") + t.sDmr.Render(" fault")
	var tail []string
	if derr != "" {
		tail = append(tail, stWarn.Render(Clip(GL["warn"]+" "+friendlyError(derr), W)), "")
	}
	foot := "live · any key returns to the dashboard"
	tail = append(tail, between(t.sDmr.Render(foot), DispW(foot), legend, DispW("● good   ● warn   ● fault"), W))
	return strings.Join(frameBody(content, tail, m.rows-2, false), "\n")
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

// confServices is the capability matrix in display order — the LP10's *marketed*
// streaming features only (the enabled four first, then the off-but-real rest).
// LibreWireless reference-image baggage that this box doesn't actually offer
// (Roon / Alexa / Matter / QPlay — installed but env-gated off, not on Arylic's
// spec sheet; see teardown §13/§7.4) is deliberately omitted. id matches the @@c
// wire key.
var confServices = []struct{ id, label string }{
	{"spotify", "Spotify"},
	{"airplay", "AirPlay 2"},
	{"dlna", "DLNA / UPnP"},
	{"bt", "Bluetooth"},
	{"cast", "Google Cast"},
	{"tidal", "Tidal"},
	{"qobuz", "Qobuz"},
	{"usb", "USB playback"},
}

// confHardware is the invariant hardware reference for the LP10 (the one model this
// tool targets), encoding the teardown's findings: a line-level streamer, no power
// amp, WM8904 codec, optical S/PDIF up to 24-bit/192 kHz. The audio-chain and
// compute facts only — live memory/link usage is the resources/network cards' job,
// so nothing here repeats a live gauge.
var confHardware = []struct{ k, v string }{
	{"soc", "Amlogic A113L · 2× Cortex-A35"},
	{"codec", "Wolfson WM8904 (DAC + ADC)"},
	{"line out", "3.5 mm · 1 Vrms (no power amp)"},
	{"optical", "S/PDIF TOSLINK ≤ 24-bit/192 kHz"},
	{"line in", "3.5 mm aux → WM8904 ADC"},
	{"radio", "dual-band 802.11ac · BT 5.0"},
}

// serviceStrip renders the capability matrix (from ConfView) as two dense grouped
// rows — "on  ● a  ● b …" / "off ○ c  ○ d …" — plus the env-gating note. Compact (2-3
// lines) for the diagnostics overlay's services section; degrades to a "reading…"
// line until @@c arrives. The prefix column is 4 wide so the dots align.
func (m *model) serviceStrip(w int) []string {
	cv := m.st.ConfView()
	if cv == nil {
		return []string{m.clipStyled(m.sty.sDmr.Render("reading from device…"), w)}
	}
	var on, off []string
	for _, sv := range confServices {
		if cv.Svc[sv.id] == "on" {
			on = append(on, m.sty.sAcc.Render("●")+" "+m.sty.sTxt.Render(sv.label))
		} else {
			off = append(off, m.sty.sDmr.Render("○")+" "+m.sty.sDim.Render(sv.label))
		}
	}
	sep := "  "
	rows := make([]string, 0, 3)
	if len(on) > 0 {
		rows = append(rows, m.sty.sDim.Render("on")+"  "+strings.Join(on, sep))
	}
	if len(off) > 0 {
		rows = append(rows, m.sty.sDim.Render("off")+" "+strings.Join(off, sep))
	}
	rows = append(rows, m.sty.sDmr.Render("env-gated · toggle in the Arylic app"))
	// Budget every row to w (visible cols): a narrow pane clips the dense on/off
	// strip rather than sizing the bordered frame past the terminal width. (The
	// cards path also re-clips via section(); clipStyled is a no-op when it fits.)
	for i, r := range rows {
		rows[i] = m.clipStyled(r, w)
	}
	return rows
}

// fmtKHz renders a sample rate in kHz: "44.1 kHz", "48 kHz", "96 kHz".
func fmtKHz(hz int) string {
	if hz%1000 == 0 {
		return strconv.Itoa(hz/1000) + " kHz"
	}
	return strconv.FormatFloat(float64(hz)/1000, 'f', 1, 64) + " kHz"
}

// clipStyled clips an already-styled string to display width w by stripping it,
// clipping the plain text, and re-dimming — used where a styled readout (eq) must
// fit a card cell and exact per-segment colour isn't worth preserving on overflow.
func (m *model) clipStyled(styled string, w int) string {
	if lipgloss.Width(styled) <= w {
		return styled
	}
	return m.sty.sDim.Render(Clip(ansiSGR.ReplaceAllString(styled, ""), w))
}

// gridRow renders a two-column "label value | label value" row, exactly W wide.
func (m *model) gridRow(k1, v1, k2, v2 string, W int) string {
	half := W / 2
	return m.cellKV(k1, v1, half) + m.cellKV(k2, v2, W-half)
}

func (m *model) cellKV(k, v string, w int) string {
	const labW = 9
	vv := Clip(v, w-labW)
	out := m.sty.sDim.Render(k) + labelGap(k, labW) + m.sty.sTxt.Render(vv)
	if vis := labW + DispW(vv); vis < w {
		out += strings.Repeat(" ", w-vis)
	}
	return out
}

// diagLine renders "label  value" with a fixed dim label column.
func (m *model) diagLine(label, value string) string {
	return m.sty.sDim.Render(label) + labelGap(label, diagLabelW) + value
}

// diagGauge renders "label  [gauge]  value detail", clipping the dim detail to the
// body width w so a long detail (e.g. the cpu load triplet at a narrow terminal)
// can't size the row past the frame — the stacked counterpart to the cards cg()
// detail clip. Pass detail="" for a gauge with no trailing note.
func (m *model) diagGauge(label, gauge, value, detail string, w int) string {
	row := m.sty.sDim.Render(label) + labelGap(label, diagLabelW) + gauge + "  " + value
	if detail != "" {
		row += m.sty.sDmr.Render(Clip(detail, w-lipgloss.Width(row))) // Clip("",<=0)→""
	}
	return m.clipStyled(row, w) // never exceed the body width (a no-op when it fits)
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

// sparkline renders the values as block glyphs scaled to the window's own
// min/max — a flat baseline reads low and a transient spike stands tall — using
// the last maxW samples (so it shows the most recent history when space is tight).
func sparkline(vals []float64, maxW int) string {
	if maxW <= 0 || len(vals) == 0 {
		return ""
	}
	if len(vals) > maxW {
		vals = vals[len(vals)-maxW:]
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	span := hi - lo
	// Floor the span relative to the peak so a steady signal with a little jitter
	// (latency that barely moves) reads as a calm low band instead of amplifying
	// sub-millisecond noise into a full-height jagged mess. A real spike still
	// exceeds the floor and towers over the baseline.
	if floor := hi * 0.8; span < floor {
		span = floor
	}
	var b strings.Builder
	for _, v := range vals {
		idx := 0
		if span > 0 {
			idx = int((v-lo)/span*float64(len(sparkRunes)-1) + 0.5)
		}
		if idx >= len(sparkRunes) {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// latencyRow renders one target — name, average, jitter, peak (amber once a real
// spike has landed), and the sparkline. The numeric fields are fixed-width so the
// sparkline column lines up across the three rows.
func (m *model) latencyRow(name string, ps protocol.PingStat, sparkW int) string {
	t := m.sty
	pad := func(s string, w int) string {
		if d := w - DispW(s); d > 0 {
			return s + strings.Repeat(" ", d)
		}
		return s
	}
	rpad := func(s string, w int) string {
		if d := w - DispW(s); d > 0 {
			return strings.Repeat(" ", d) + s
		}
		return s
	}
	peakPen := t.sDmr
	if ps.Peak > ps.Avg*2 && ps.Peak-ps.Avg > 10 { // a genuine spike, not baseline wobble
		peakPen = stWarn
	}
	return t.sDim.Render(pad(name, latNameW)) +
		t.sTxt.Render(rpad(fmtLatencyMs(ps.Avg), latAvgW)+latAvgUnit) + " " +
		t.sDmr.Render(pad("±"+fmtLatencyMs(ps.Jitter), latJitW)) + " " +
		peakPen.Render(pad("max "+fmtLatencyMs(ps.Peak), latPeakW)) + " " +
		t.sDim.Render(sparkline(ps.Series, sparkW)) // dim: a subtle inline trend, not a glare
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
