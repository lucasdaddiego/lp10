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
	st.SetLSSDP(&protocol.LSSDPInfo{Name: "Living", State: "S", NetMode: "ETH0"})
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
	m.diag = true
	if out := stripANSI(m.viewContent()); strings.Contains(out, "lssdp") {
		t.Fatal("no lssdp row before a probe")
	}
	st.SetLSSDP(&protocol.LSSDPInfo{Name: "Living", FW: "AR241CE_8530.23.2", State: "S", NetMode: "ETH0"})
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
	m.diag = true
	if out := stripANSI(m.viewContent()); strings.Contains(out, "spotify ") && strings.Contains(out, "probed") {
		t.Fatal("no zeroconf row before a probe")
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{Status: 101, StatusString: "OK", LibraryVersion: "3.203.239-g1d6bd565", ActiveUser: "lucas"}, 9096)
	out := stripANSI(m.viewContent())
	for _, want := range []string{"answered", ":9096", "eSDK 3.203.239", "signed in as lucas"} {
		if !strings.Contains(out, want) {
			t.Errorf("answered row missing %q:\n%s", want, out)
		}
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{Status: 101, StatusString: "OK"}, 9096)
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "nobody signed in") {
		t.Errorf("no-user row missing:\n%s", out)
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{Status: 102, StatusString: "ERROR-SPOTIFY"}, 9096)
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
	m.diag = false
	m.rows, m.cols = 44, 120
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=newspotifyhifi", "spotify.cfg=hifi"}})
	st.SetSpotifyZC(&protocol.SpotifyZC{Status: 101, ActiveUser: "lucas"}, 9096)
	pane := stripANSI(strings.Join(m.renderServices(time.Now(), 114), "\n"))
	if !strings.Contains(pane, "zeroconf") || !strings.Contains(pane, "signed in as lucas") {
		t.Errorf("services pane lacks the zeroconf line:\n%s", pane)
	}
}

// Opening the diagnostics overlay is the one gesture that asks the vendor
// whether the firmware is current; the device card then carries the verdict.
func TestDiagOpenRequestsOTAAndShowsVerdict(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	m.rows, m.cols = 40, 160
	if st.OTAPending() {
		t.Fatal("pending before the overlay opened")
	}
	m.key(keyEvent{kind: kRune, r: '?'})
	if !m.diag || !st.OTAPending() {
		t.Fatal("? did not open the overlay and request a check")
	}
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "checking…") {
		t.Errorf("pending check not shown:\n%s", out)
	}
	// from inside another overlay too
	m.diag = false
	m.openOverlay(ovServices)
	m.key(keyEvent{kind: kRune, r: '?'})
	if !m.diag || m.ov != ovNone {
		t.Fatal("? from the services pane did not switch to diagnostics")
	}
	st.TakeOTARequest()
	now := time.Now()
	st.SetOTA(protocol.OTAInfo{At: now, Asked: "AR241CE_8530", UpToDate: true})
	if out := stripANSI(m.viewContent()); !strings.Contains(out, "update    up to date · checked") {
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
