package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// footerOf is the footer row of a rendered frame: the last row inside the
// border, trimmed.
func footerOf(frame string) string {
	ls := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if len(ls) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.Trim(ls[len(ls)-2], "┃"))
}

// With the diagnostics open the player is hidden, so the loop ticks every 3 s
// and every third tick adds three pings of up to 1 s each: a record gap of up
// to about 6.2 s is the loop's normal cadence and must read healthy, where the
// old 3 s threshold flickered "warn · ssh stream quiet" on every tick.
func TestDiagRecordGapInsideTheLoopTickIsHealthy(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	now := time.Now()
	for _, c := range []struct {
		gap  time.Duration
		want string
	}{
		{3200 * time.Millisecond, "healthy"}, // a plain tick
		{6200 * time.Millisecond, "healthy"}, // a tick with three silent pings
		{7 * time.Second, "warn · ssh stream quiet"},
	} {
		d := protocol.DiagnosticSnapshot{
			Snapshot: protocol.Snapshot{Connected: true},
			LastRx:   now.Add(-c.gap),
			LastData: now.Add(-c.gap),
		}
		v := withOpsVitals(collectVitals(nil, nil), d, now)
		if got := stripANSI(m.diagMasthead(d, v, now, 140)); !strings.Contains(got, c.want) {
			t.Errorf("a %v record gap reads %q, want %q", c.gap, strings.TrimSpace(got), c.want)
		}
	}
	if sev(8, thrRx) != 2 {
		t.Error("the fault must stay at the watchdog's 8 s")
	}
}

// On a terminal narrower than the card grid the stacked layout is used, and
// its top line carries the same verdict: a box at 95 % cpu must not read as
// fine on a standard 80-column terminal.
func TestDiagStackedCarriesTheVerdict(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	applyFixtureRecords(st, "device_record.txt")
	protocol.ApplyRecord(st, protocol.Record{"s": {"12345.6 1.90 1.00 0.50 100000 215000 2 AR241CE_8530.23 Linux-5.15.137"}})
	m.rows, m.cols = 60, 80
	m.setView(viewDiag)
	out := stripANSI(m.viewContent())
	if top := lineWith(out, "● fault"); !strings.Contains(top, "diagnostics") || !strings.Contains(top, "cpu 95%") {
		t.Errorf("cpu 95%% at 80 cols: no verdict in the stacked masthead:\n%s", out)
	}
}

// The stacked device section pairs its facts two to a row, but the
// sentence-length ones get a row of their own: at 80 columns a half-width cell
// cut the vendor app version off the build and the "ago" off the boot.
func TestDiagStackedDeviceFactsAreNotHalfWidthClipped(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	applyFixtureRecords(st, "device_record.txt")
	protocol.ApplyRecord(st, protocol.Record{"i": {"net=eth", "ip=192.168.1.13", "gw=192.168.1.1",
		"build=2026-01-12", "app=318", "platform=LS8", "name=Living", "vapp=32", "rboot=cold_boot"}})
	protocol.ApplyRecord(st, protocol.Record{"s": {"694800.5 0.40 0.30 0.20 140000 215000 2 AR241CE_8530.23 Linux-5.15.137"}})
	m.rows, m.cols = 80, 80
	m.setView(viewDiag)
	out := stripANSI(m.viewContent())
	if !strings.Contains(out, "vendor app v32") {
		t.Errorf("build row lost the vendor app version:\n%s", lineWith(out, "build"))
	}
	if boot := lineWith(out, "boot "); !strings.Contains(boot, "ago") {
		t.Errorf("boot row lost when/how long ago:\n%s", boot)
	}
	// the short facts still pair up
	if row := lineWith(out, "firmware "); !strings.Contains(row, "mcu") {
		t.Errorf("firmware and mcu should share a row: %q", row)
	}
}

// The footers lay their live facts beside a key hint; at 80 columns the hint
// gives way instead of the facts: the logs keep their tail age, the stacked
// diagnostics the count of rows off-screen, the services their reminder.
func TestFootersKeepTheirFactsAt80Cols(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	lines := make([]string, 0, 40)
	for i := range 40 {
		lines = append(lines, fmt.Sprintf(" Sep 23 10:%02d:00:000000 E/tag[1]: line %d", i, i))
	}
	protocol.ApplyRecord(st, protocol.Record{"l": lines})
	m.rows, m.cols = 24, 80
	m.setView(viewLogs)
	m.logScrollBy(3, m.logPage())
	if foot := footerOf(stripANSI(m.viewContent())); !strings.Contains(foot, "ago · 40 lines · scrolled 3") || !strings.Contains(foot, "r refresh") {
		t.Errorf("logs footer at 80 cols: %q", foot)
	}
	applyFixtureRecords(st, "device_record.txt")
	applyFixtureRecords(st, "config_record.txt")
	m.setView(viewDiag)
	if foot := footerOf(stripANSI(m.viewContent())); !strings.Contains(foot, "more rows below") || !strings.Contains(foot, "? help") {
		t.Errorf("stacked diag footer at 80 cols: %q", foot)
	}
	m.rows = 60
	m.setView(viewServices)
	if foot := footerOf(stripANSI(m.viewContent())); !strings.Contains(foot, "enter writes the device's config") {
		t.Errorf("services footer at 80 cols: %q", foot)
	}
	// a fact wider than the row is clipped, never wrapped
	if got := m.footerFit(logKeys, strings.Repeat("x", 90), 90, 74); visWidth(got) != 74 {
		t.Errorf("an over-wide fact is %d wide at 74", visWidth(got))
	}
	// one that leaves no room for even the shortest hint stands alone, right-aligned
	if got := m.footerFit(logKeys, strings.Repeat("x", 70), 70, 74); got != "    "+strings.Repeat("x", 70) {
		t.Errorf("no hint fits beside a 70-wide fact: %q", got)
	}
}

// The diagnostics strip filed an unread service ("" — the contract's
// "unknown") under off, where the services pane says "—". It has its own group.
func TestDiagStripDoesNotFileUnknownAsOff(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=spotifymusicpro", "spotify.cfg=pro",
		"airplay=on", "dlna=on", "bt=on", "cast=off", "tidal=off", "qobuz=off", "usb="}})
	strip := stripANSI(strings.Join(m.serviceStrip(200), "\n"))
	if off := lineWith(strip, "off "); strings.Contains(off, "USB playback") {
		t.Errorf("usb= (unread) is listed as off: %q", off)
	}
	if unread := lineWith(strip, "?   "); !strings.Contains(unread, "USB playback") {
		t.Errorf("usb= (unread) should sit in its own group:\n%s", strip)
	}
}

// "checking…" must last until the vendor answers, not only until the worker
// takes the request: the manifest round trip itself runs up to its 6 s timeout.
func TestDiagVendorRowSaysCheckingWhileTheRequestIsInFlight(t *testing.T) {
	_, st, _ := makeModel(t)
	protocol.ApplyRecord(st, protocol.Record{"s": {"100 0.1 0.1 0.1 100000 215000 2 AR241CE_8530.23"}})
	st.RequestOTA()
	if _, ok := st.TakeOTARequest(); !ok { // otaWorker: now blocked in OTACheck
		t.Fatal("precondition: the worker should take the request")
	}
	now := time.Now()
	if got := otaFact(st.DiagnosticView(now), now); got != "checking…" {
		t.Errorf("vendor row while the manifest request is in flight = %q", got)
	}
	st.SetOTA(protocol.OTAInfo{At: now, Asked: "AR241CE_8530", UpToDate: true})
	if got := otaFact(st.DiagnosticView(now), now); !strings.HasPrefix(got, "up to date") {
		t.Errorf("vendor row once answered = %q", got)
	}
}

// An LSSDP answer with no State and no NETMODE rendered a dangling separator
// ("answered 1.0s ago · "); the ZeroConf row already guards the same case.
func TestDiagLSSDPRowHasNoDanglingSeparator(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_8530.23.2"})
	now := time.Now()
	if got := stripANSI(m.lssdpReadout(st.DiagnosticView(now), now)); strings.HasSuffix(got, "· ") || !strings.HasPrefix(got, "answered") {
		t.Errorf("lssdp row = %q", got)
	}
}
