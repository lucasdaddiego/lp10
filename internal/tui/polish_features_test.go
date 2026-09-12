package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/sweep"
)

// The notice line under the header carries transient events and fades: a
// volume step names the level, mute says so, the timers and night mode report
// their state, a service toggle says what was asked.
func TestNoticeLineEvents(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 120
	st.SetVol(50)
	line := func() string { return stripANSI(strings.Split(m.viewContent(), "\n")[2]) }
	if strings.TrimSpace(strings.Trim(line(), "┃")) != "" {
		t.Errorf("notice line should start blank, got %q", line())
	}
	m.key(ke(kUp))
	if !strings.Contains(line(), "volume 52%") {
		t.Errorf("volume notice missing: %q", line())
	}
	m.key(kr('m'))
	if !strings.Contains(line(), "muted") {
		t.Errorf("mute notice missing: %q", line())
	}
	m.key(kr('s'))
	if !strings.Contains(line(), "sleep") {
		t.Errorf("sleep notice missing: %q", line())
	}
	m.key(kr('S'))
	if !strings.Contains(line(), "sleep timer cancelled") {
		t.Errorf("cancel notice missing: %q", line())
	}
	m.key(kr('d'))
	if !strings.Contains(line(), "night mode") {
		t.Errorf("night notice missing: %q", line())
	}
	// it fades
	m.noticeUntil = time.Now().Add(-time.Second)
	if strings.TrimSpace(strings.Trim(line(), "┃")) != "" {
		t.Errorf("notice should have faded, got %q", line())
	}
	// the services toggle reports what it asked for
	m.setView(viewServices)
	m.svcFocus = 0
	m.svcToggle(time.Now())
	if !strings.Contains(line(), "Spotify →") || !strings.Contains(line(), "asked the device") {
		t.Errorf("toggle notice missing: %q", line())
	}
}

// A connect prints a summary once the capability block has arrived; a lost
// connection prints a warning; the summary waits for the facts but not forever.
func TestStartupSummaryAndConnectionNotices(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 120
	now := time.Now()
	applyFixtureRecords(st, "device_record.txt")
	applyFixtureRecords(st, "config_record.txt")
	protocol.ApplyRecord(st, protocol.Record{"o": {"n=88", "t=Sep 11 01:47:58", "u="}})
	m.trackConnection(st.Snap(), now)
	got := stripANSI(m.noticeRow(now, 120))
	for _, want := range []string{"connected", "firmware AR241CE_8530.23.2", "HiFi engine", "88 reconnects in the log", "power-on"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
	// a disconnect after a connection warns
	st.Disconnect()
	m.trackConnection(st.Snap(), now.Add(time.Second))
	if got := stripANSI(m.noticeRow(now.Add(time.Second), 120)); !strings.Contains(got, "connection lost") || !m.noticeWarn {
		t.Errorf("disconnect notice = %q (warn=%v)", got, m.noticeWarn)
	}
	// a fresh connect with no facts yet waits, then prints what it has
	m2, st2, _ := modelWith(protocol.NewState())
	m2.sty = newTheme()
	protocol.ApplyRecord(st2, protocol.Record{"v": {"MID-Read:64 Data:40 Length:2"}})
	m2.trackConnection(st2.Snap(), now)
	if got := stripANSI(m2.noticeRow(now, 120)); got != "" {
		t.Errorf("summary printed before the facts arrived: %q", got)
	}
	m2.trackConnection(st2.Snap(), now.Add(startupSummaryWait+time.Second))
	if got := stripANSI(m2.noticeRow(now.Add(startupSummaryWait+time.Second), 120)); got != "connected" {
		t.Errorf("late summary = %q, want just 'connected'", got)
	}
}

// Connected with nothing playing, the full player shows the big clock and the
// sources that are on; the compact player names them in its hint line.
func TestIdleScreen(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "config_record.txt")
	protocol.ApplyRecord(st, protocol.Record{"v": {"MID-Read:64 Data:40 Length:2"}, "B": {"MID-Read:42 Data: Length:0"}})
	m, _, _ := modelWith(st)
	m.sty = newTheme()
	m.rows, m.cols = 40, 120
	out := stripANSI(m.viewContent())
	if !strings.Contains(out, "█████") {
		t.Errorf("idle screen lacks the block clock:\n%s", out)
	}
	for _, want := range []string{"nothing playing", "start something on", "Spotify", "AirPlay 2", "Bluetooth", "DLNA / UPnP"} {
		if !strings.Contains(out, want) {
			t.Errorf("idle screen missing %q", want)
		}
	}
	if strings.Contains(out, "Tidal") {
		t.Error("an off service was named as a way to wake the box")
	}
	// the clock reads the current time
	if !strings.Contains(strings.Join(bigClock(time.Date(2026, 9, 12, 16, 4, 0, 0, time.UTC)), "\n"), "█") {
		t.Error("bigClock drew nothing")
	}
	// compact: the hint names the same sources
	m.rows, m.cols = 20, 80
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "start something on Spotify · AirPlay 2") {
		t.Errorf("compact idle hint missing:\n%s", out)
	}
	// no capability block yet: the fixed three
	if got := sourcesOn(nil); got != "Spotify · AirPlay · Bluetooth" {
		t.Errorf("sourcesOn(nil) = %q", got)
	}
	if got := sourcesOn(&protocol.ConfInfo{Svc: map[string]string{"spotify": "off"}}); !strings.Contains(got, "no streaming service") {
		t.Errorf("all-off = %q", got)
	}
}

// The diagnostics' device card names what moved since the last sweep.
func TestSinceSweepFact(t *testing.T) {
	base := &sweep.Report{At: time.Date(2026, 9, 12, 16, 0, 0, 0, time.Local), Build: "AR241CE_8530", MCU: "23", VendorApp: "32"}
	same := diagIdentity{fw: "AR241CE_8530.23.2", mcu: "v23"}
	if got := sweepDeltaFact(same, &protocol.DevInfo{VendorApp: "32"}, base); got != "nothing changed since the sweep of Sep 12 16:00" {
		t.Errorf("unchanged = %q", got)
	}
	moved := diagIdentity{fw: "AR241CE_9000.24.1", mcu: "v24"}
	got := sweepDeltaFact(moved, &protocol.DevInfo{VendorApp: "33"}, base)
	for _, want := range []string{"firmware AR241CE_8530 → AR241CE_9000", "mcu 23 → 24", "vendor app 32 → 33", "sweep of Sep 12 16:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("delta %q missing %q", got, want)
		}
	}
	if sweepDeltaFact(same, nil, nil) != "" {
		t.Error("no baseline should print nothing")
	}
	if sweepDeltaFact(diagIdentity{fw: "—", mcu: "—"}, nil, base) != "" {
		t.Error("no identity yet should print nothing")
	}
	// on the card
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 44, 160
	m.baseline = base
	applyFixtureRecords(st, "device_record.txt")
	m.setView(viewDiag)
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "since sweep") || !strings.Contains(out, "sweep of Sep 12") {
		t.Errorf("device card lacks the since-sweep row:\n%s", out)
	}
}

// F follows the open log: an immediate fetch, then one every 100 ticks while
// the view shows, and the title says so.
func TestLogsFollowMode(t *testing.T) {
	m, _, collect := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 120
	m.setView(viewLogs)
	collect() // the fetch on open
	m.key(kr('F'))
	if !m.logFollow || len(collect()) != 1 {
		t.Fatal("F should turn follow on and fetch at once")
	}
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "following") || !strings.Contains(out, "F follow") {
		t.Errorf("follow state not shown:\n%s", out)
	}
	m.scroll = 99
	m.dispatch(logicMsg{})
	if len(collect()) != 1 {
		t.Error("the 100th tick should refetch")
	}
	m.dispatch(logicMsg{})
	if len(collect()) != 0 {
		t.Error("an ordinary tick should not fetch")
	}
	m.setView(viewPlayer)
	m.scroll = 199
	m.dispatch(logicMsg{})
	if len(collect()) != 0 {
		t.Error("follow must not fetch while another view shows")
	}
	m.setView(viewLogs)
	collect()
	m.key(kr('F'))
	if m.logFollow {
		t.Error("F again should turn follow off")
	}
}

// The strip has a short form between the full names and the numerals, and the
// footer rotates a second page of rarer keys for four seconds in sixteen.
func TestStripShortFormAndFooterRotation(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	if s, _ := m.viewStrip(40); stripANSI(s) != "1 play  2 eq  3 svc  4 log  5 diag" {
		t.Errorf("short strip = %q", stripANSI(s))
	}
	m.scroll = 0
	first := stripANSI(m.footerRow(120))
	m.scroll = 130
	second := stripANSI(m.footerRow(120))
	if !strings.Contains(first, "space play/pause") || !strings.Contains(second, "S cancel sleep") || first == second {
		t.Errorf("footer pages:\n%q\n%q", first, second)
	}
	m.scroll = 160
	if got := stripANSI(m.footerRow(120)); got != first {
		t.Errorf("footer should be back on the first page: %q", got)
	}
}

// theme = light|dark decides the palette; auto follows the terminal's answer
// and stays dark until it comes.
func TestThemeSelection(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = nil
	m.ensureTheme()
	if !m.themeDark {
		t.Error("auto with no answer should be dark")
	}
	light := false
	m.bgDark = &light
	m.ensureTheme()
	if m.themeDark {
		t.Error("a light background should switch the palette")
	}
	m.cfg.Theme = "dark"
	m.ensureTheme()
	if !m.themeDark {
		t.Error("theme = dark should win over the terminal")
	}
	m.cfg.Theme = "light"
	m.bgDark = nil
	m.ensureTheme()
	if m.themeDark {
		t.Error("theme = light should win with no answer")
	}
	// the two palettes differ where it matters
	if newThemeFor(true).sTxt.GetForeground() == newThemeFor(false).sTxt.GetForeground() {
		t.Error("light and dark text colours should differ")
	}
}
