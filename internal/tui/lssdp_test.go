package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestConnectingCopyExplainsViaLSSDP(t *testing.T) {
	st := protocol.NewState()
	m, _, _ := modelWith(st)
	m.sty = newTheme()
	join := func() string { return stripANSI(strings.Join(m.metaLines(st.Snap(), 60), "\n")) }
	if out := join(); !strings.Contains(out, "connecting") || strings.Contains(out, "LAN") {
		t.Errorf("before any probe: %q", out)
	}
	st.SetLSSDP(nil)
	if out := join(); !strings.Contains(out, "not answering on the LAN") {
		t.Errorf("after a silent probe: %q", out)
	}
	st.SetLSSDP(&protocol.LSSDPInfo{State: "S", NetMode: "ETH0"})
	if out := join(); !strings.Contains(out, "device is up on the LAN") {
		t.Errorf("after an answer: %q", out)
	}
	// connected: none of it
	protocol.ApplyRecord(st, playingRecord())
	st.Preload(nil, 0, 50)
	if out := join(); strings.Contains(out, "LAN") {
		t.Errorf("connected idle copy must not mention the LAN: %q", out)
	}
}

func TestDiagLSSDPRow(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 120
	m.view = viewDiag
	if out := stripANSI(m.viewContent()); strings.Contains(out, "lssdp") {
		t.Fatal("no lssdp row before a probe")
	}
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CE_8530.23.2", State: "S", NetMode: "ETH0"})
	out := stripANSI(m.viewContent())
	if !strings.Contains(out, "lssdp") || !strings.Contains(out, "answered") || !strings.Contains(out, "eth0") {
		t.Errorf("answered row missing:\n%s", out)
	}
	st.SetLSSDP(nil)
	out = stripANSI(m.viewContent())
	if !strings.Contains(out, "no answer") || !strings.Contains(out, "last ") {
		t.Errorf("silent row missing:\n%s", out)
	}
	m.cols = 70
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "no answer") {
		t.Errorf("stacked layout lacks the row:\n%s", out)
	}
}

func TestFmtAgeShort(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "0s", 600 * time.Millisecond: "0.6s", 12 * time.Second: "12s",
		3 * time.Minute: "3m", 5 * time.Hour: "5h",
	} {
		if got := fmtAgeShort(d); got != want {
			t.Errorf("fmtAgeShort(%v) = %q, want %q", d, got, want)
		}
	}
}

// The Spotify ZeroConf row sits in the connection block beside LSSDP (it is
// the other ssh-free signal) and, with the engine facts, in the services pane.
func TestDiagAndServicesZeroConfRow(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 160
	m.view = viewDiag
	if out := stripANSI(m.viewContent()); strings.Contains(out, "spotify ") && strings.Contains(out, "probed") {
		t.Fatal("no zeroconf row before a probe")
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK", ActiveUser: "lucas"}, 9096)
	out := stripANSI(m.viewContent())
	for _, want := range []string{"answered", ":9096", "signed in as lucas"} {
		if !strings.Contains(out, want) {
			t.Errorf("answered row missing %q:\n%s", want, out)
		}
	}
	// An empty activeUser is not "nobody": the Pro engine leaves it empty while
	// playing, so the row says nothing about users rather than asserting an
	// absence — and never carries the eSDK build, which the services card has.
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK"}, 9096)
	if out := stripANSI(m.viewContent()); strings.Contains(out, "signed in") || strings.Contains(out, "3.211") ||
		!strings.Contains(out, "answered") {
		t.Errorf("no-user row wrong:\n%s", out)
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "ERROR-SPOTIFY"}, 9096)
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "error-spotify") {
		t.Errorf("status row missing:\n%s", out)
	}
	st.SetSpotifyZC(nil, 9096)
	out = stripANSI(m.viewContent())
	if !strings.Contains(out, "no answer · :9096") || !strings.Contains(out, "last ") {
		t.Errorf("silent row missing:\n%s", out)
	}
	st.SetSpotifyZC(nil, 0)
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "not advertised") {
		t.Errorf("not-advertised row missing:\n%s", out)
	}
	m.cols = 70
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "not advertised") {
		t.Errorf("stacked layout lacks the row:\n%s", out)
	}
	// the services pane's engine section carries the same readout
	m.view = viewPlayer
	m.rows, m.cols = 44, 120
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=newspotifyhifi", "spotify.cfg=hifi"}})
	st.SetSpotifyZC(&protocol.SpotifyZC{ActiveUser: "lucas"}, 9096)
	pane := stripANSI(strings.Join(m.renderServices(time.Now(), 114), "\n"))
	if !strings.Contains(pane, "zeroconf") || !strings.Contains(pane, "signed in as lucas") {
		t.Errorf("services pane lacks the zeroconf line:\n%s", pane)
	}
}

// Opening the diagnostics overlay asks the vendor nothing: the update line is
// the verdict the box fetched itself (its ota daemon asks every 4 h and logs
// the answer). A vendor query is the u key inside the overlay, and its answer
// lands on a separate "vendor" line.
func TestDiagOpenRequestsOTAAndShowsVerdict(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 160
	if st.DiagnosticView(time.Now()).OTAPending {
		t.Fatal("pending before the overlay opened")
	}
	m.key(keyEvent{kind: kRune, r: 'i'})
	if m.view != viewDiag || st.DiagnosticView(time.Now()).OTAPending {
		t.Fatal("i did not open the diagnostics, or asked the vendor on its own")
	}
	// the box's own verdict, from the syslog digest
	protocol.ApplyRecord(st, protocol.Record{"o": {"n=2", "t=Sep 11 01:47:58",
		"u=" + time.Now().Add(-3*time.Hour).Format("Jan _2 15:04:05") + ":000000 E/ota[923]: ota: OTA:error string =  No update available"}})
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "update    up to date · the box asked 3h ago · it asks every 4 h") {
		t.Errorf("the box's own verdict missing:\n%s", out)
	}
	// u asks the vendor; the overlay stays open
	m.key(keyEvent{kind: kRune, r: 'u'})
	if m.view != viewDiag || !st.DiagnosticView(time.Now()).OTAPending {
		t.Fatal("u did not request a vendor check (or closed the overlay)")
	}
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "vendor    checking…") {
		t.Errorf("pending check not shown:\n%s", out)
	}
	// from inside another overlay too
	m.view = viewPlayer
	m.openOverlay(ovServices)
	m.key(keyEvent{kind: kRune, r: 'i'})
	if m.view != viewDiag {
		t.Fatal("i from the services view did not switch to diagnostics")
	}
	st.TakeOTARequest()
	now := time.Now()
	st.SetOTA(protocol.OTAInfo{At: now, Asked: "AR241CE_8530", UpToDate: true})
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "vendor    up to date · checked") {
		t.Errorf("up-to-date verdict missing:\n%s", out)
	}
	st.SetOTA(protocol.OTAInfo{At: now, Asked: "AR241CE_9243", Offered: "AR241CE_8530"})
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "AR241CE_8530 available") {
		t.Errorf("offer missing:\n%s", out)
	}
	st.SetOTA(protocol.OTAInfo{At: now, Asked: "AR241CE_9243"})
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "update available") {
		t.Errorf("bare offer missing:\n%s", out)
	}
	st.SetOTA(protocol.OTAInfo{At: now, Err: "vendor unreachable"})
	out := stripANSI(m.viewContent())
	if !strings.Contains(out, "check failed · vendor unreachable") {
		t.Errorf("failure missing:\n%s", out)
	}
	m.cols = 70
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "check failed") {
		t.Errorf("stacked layout lacks the row:\n%s", out)
	}
	// never asked: no row at all
	if f := otaFact(protocol.DiagnosticSnapshot{}, now); f != "" {
		t.Errorf("unasked fact = %q", f)
	}
}

// At mini size the diagnostics overlay cannot be drawn, so ? must not pretend
// to open it.
func TestDiagAtMiniSizeIsInert(t *testing.T) {
	m, st, _ := makeModel(t)
	m.rows, m.cols = MiniRows-1, 40
	m.key(keyEvent{kind: kRune, r: 'i'})
	if m.view == viewDiag || st.DiagnosticView(time.Now()).OTAPending {
		t.Errorf("mini: diag=%v otaPending=%v, want neither", m.view == viewDiag, st.DiagnosticView(time.Now()).OTAPending)
	}
}

// The device card says how the box came up, and the connection and network
// sections carry the engine's own reconnect account and the radio warning —
// each of which also moves the health verdict.
func TestDiagBootReconnectsAndRadio(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 44, 160
	now := time.Now()
	// a wired box that came up from a power loss 2 days ago, quiet log
	protocol.ApplyRecord(st, protocol.Record{
		"i": {"net=eth", "ip=192.0.2.13", "rboot=cold_boot"},
		"s": {"172800.00 0.10 0.10 0.10 100000 220000 2 AR241CE_8530.23 Linux-5.15"},
		"o": {"n=0", "t=" + now.Add(-20*time.Hour).Format("Jan _2 15:04:05"), "u="},
		"v": {"MID-Read:64 Data:40 Length:2"},
	})
	m.key(keyEvent{kind: kRune, r: 'i'})
	out := stripANSI(m.viewContent())
	for _, want := range []string{
		"boot      power-on (cold boot) · " + now.Add(-172800*time.Second).Format("Jan 2 15:04") + " · 2d 0h 0m ago",
		"engine    no reconnects",
		"● healthy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "on wi-fi") {
		t.Error("radio warning shown on a wired box")
	}
	// the same box on its radio, with a reconnect storm: both are warns
	protocol.ApplyRecord(st, protocol.Record{
		"i": {"net=wifi", "ip=192.0.2.13", "ssid=home", "freq=5180", "rboot=normal"},
		"o": {"n=100", "t=" + now.Add(-20*time.Hour).Format("Jan _2 15:04:05"), "u="},
		"v": {"MID-Read:64 Data:40 Length:2"},
	})
	out = stripANSI(m.viewContent())
	for _, want := range []string{
		"boot      software reboot (normal)",
		"100 reconnects · 5.0/h",
		"radio     ⚠ on wi-fi · the radio firmware can wedge — wire it",
		"● warn · on wi-fi · engine reconnects 5.0/h", // the verdict names its reasons
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q:\n%s", want, out)
		}
	}
	// the stacked layout carries the same rows
	m.cols = 70
	out = stripANSI(m.viewContent())
	for _, want := range []string{"power-on", "software reboot", "100 reconnects", "on wi-fi"} {
		if want == "power-on" {
			continue // the second record replaced the reason
		}
		if !strings.Contains(out, want) {
			t.Errorf("stacked overlay missing %q:\n%s", want, out)
		}
	}
	// no reason shipped (an older loop): no boot row at all
	if f := bootFact(protocol.DiagnosticSnapshot{DevInfo: &protocol.DevInfo{Net: "eth"}}, now); f != "" {
		t.Errorf("boot fact without a reason = %q", f)
	}
	// an offered update is carried as the box logged it
	d := protocol.DiagnosticSnapshot{Ops: &protocol.DevOps{OTAOK: true, OTAText: "update offered", OTAAt: now.Add(-time.Hour)}}
	if f := boxUpdateFact(d, now); f != "update offered · the box asked 60m ago · it asks every 4 h" {
		t.Errorf("offered fact = %q", f)
	}
}
