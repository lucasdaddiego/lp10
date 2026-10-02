package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The LSSDP probe needs no tunnel, so the connecting screen can say which kind
// of "connecting…" this is: a box up on the LAN whose :2018 is not answering,
// or one that is not there at all.
func TestConnectingCopyExplainsViaLSSDP(t *testing.T) {
	st := protocol.NewState()
	m, _, _ := modelWith(st)
	join := func() string {
		lines := m.metaLines(st.Snap(), 60)
		assertWithin(t, "metaLines", lines, 60)
		return stripANSI(strings.Join(lines, "\n"))
	}
	if out := join(); !strings.Contains(out, "connecting") || strings.Contains(out, "LAN") {
		t.Errorf("before any probe: %q", out)
	}
	st.SetLSSDP(nil)
	if out := join(); !strings.Contains(out, "device not answering on the LAN") {
		t.Errorf("after a silent probe: %q", out)
	}
	st.SetLSSDP(&protocol.LSSDPInfo{State: "S", NetMode: "ETH0"})
	if out := join(); !strings.Contains(out, "device is up on the LAN · :2018 not answering yet") {
		t.Errorf("after an answer: %q", out)
	}
	// an answer older than lssdpFresh no longer vouches for the box
	stale := protocol.Snapshot{LSSDPAlive: true, LSSDPAt: time.Now().Add(-lssdpFresh - time.Second), LSSDPProbeAt: time.Now()}
	if out := stripANSI(strings.Join(m.metaLines(stale, 60), "\n")); !strings.Contains(out, "device not answering on the LAN") {
		t.Errorf("after a stale answer: %q", out)
	}
	// connected: none of it
	connect(st)
	st.ApplyStatus("NET", false, 50, false)
	if out := join(); strings.Contains(out, "LAN") {
		t.Errorf("connected idle copy must not mention the LAN: %q", out)
	}
	// and on the full connecting screen, inside the frame
	m2, _, _ := modelWith(protocol.NewState())
	m2.st.SetLSSDP(&protocol.LSSDPInfo{State: "S"})
	m2.rows, m2.cols = 30, 100
	if out := clean(render(t, m2)); !strings.Contains(out, "device is up on the LAN") || !strings.Contains(out, "searching for LP10") {
		t.Errorf("full connecting screen:\n%s", out)
	}
}

func TestDiagLSSDPRow(t *testing.T) {
	m, st, _ := makeModel(t)
	m.rows, m.cols = 40, 120
	m.view = viewDiag
	if out := clean(render(t, m)); strings.Contains(out, "lssdp") {
		t.Fatal("no lssdp row before a probe")
	}
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2", State: "S", NetMode: "ETH0"})
	out := clean(render(t, m))
	if !hasRow(out, "lssdp", "answered", "ago · S · eth0") {
		t.Errorf("answered row missing:\n%s", out)
	}
	// an answer that names neither state nor link: the age alone, no dangling separator
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2"})
	render(t, m)
	if got := stripANSI(m.lssdpReadout(st.DiagnosticView(), time.Now())); !strings.HasPrefix(got, "answered ") || !strings.HasSuffix(got, "s ago") {
		t.Errorf("bare answer row = %q", got)
	}
	st.SetLSSDP(nil)
	out = clean(render(t, m))
	if !hasRow(out, "lssdp", "no answer · last ", "· probed ") {
		t.Errorf("silent row missing:\n%s", out)
	}
	m.cols = 70
	if out := clean(render(t, m)); !strings.Contains(out, "no answer") {
		t.Errorf("stacked layout lacks the row:\n%s", out)
	}
	// never answered: no "last" age
	st2 := playingState()
	st2.SetLSSDP(nil)
	m2, _, _ := modelWith(st2)
	if got := stripANSI(m2.lssdpReadout(st2.DiagnosticView(), time.Now())); strings.Contains(got, "last") || !strings.HasPrefix(got, "no answer · probed ") {
		t.Errorf("never-answered row = %q", got)
	}
}

func TestFmtAgeShort(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "0s", 600 * time.Millisecond: "0.6s", 12 * time.Second: "12s",
		3 * time.Minute: "3m", 5 * time.Hour: "5h", 119 * time.Second: "119s", 2 * time.Minute: "2m",
	} {
		if got := fmtAgeShort(d); got != want {
			t.Errorf("fmtAgeShort(%v) = %q, want %q", d, got, want)
		}
	}
}

// The Spotify ZeroConf row sits in the connection section beside LSSDP: it is
// the other tunnel-free signal.
func TestDiagZeroConfRow(t *testing.T) {
	m, st, _ := makeModel(t)
	m.rows, m.cols = 40, 160
	m.view = viewDiag
	if out := clean(render(t, m)); hasRow(out, "spotify", "probed") {
		t.Fatal("no zeroconf row before a probe")
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK", ActiveUser: "lucas"}, 9096)
	out := clean(render(t, m))
	if !hasRow(out, "spotify", "answered", "· :9096 · signed in as lucas") {
		t.Errorf("answered row missing:\n%s", out)
	}
	// An empty activeUser is not "nobody": the Pro engine leaves it empty while
	// playing, so the row says nothing about users rather than asserting an
	// absence — and never carries the eSDK build, which the device section has.
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK", LibraryVersion: "3.211.130"}, 9096)
	render(t, m)
	if got := stripANSI(m.zcReadout(st.DiagnosticView(), time.Now())); strings.Contains(got, "signed in") || strings.Contains(got, "3.211") ||
		!strings.HasPrefix(got, "answered ") || !strings.HasSuffix(got, "ago · :9096") {
		t.Errorf("no-user row wrong: %q", got)
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "ERROR-SPOTIFY"}, 9096)
	if out := clean(render(t, m)); !strings.Contains(out, "error-spotify") {
		t.Errorf("status row missing:\n%s", out)
	}
	// an answer on an unknown port: the age alone
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK"}, 0)
	render(t, m)
	if got := stripANSI(m.zcReadout(st.DiagnosticView(), time.Now())); !strings.HasSuffix(got, "s ago") {
		t.Errorf("portless answer row = %q", got)
	}
	st.SetSpotifyZC(nil, 9096)
	out = clean(render(t, m))
	if !hasRow(out, "spotify", "no answer · :9096 · last ", "· probed ") {
		t.Errorf("silent row missing:\n%s", out)
	}
	st.SetSpotifyZC(nil, 0)
	if out := clean(render(t, m)); !hasRow(out, "spotify", "not advertised") {
		t.Errorf("not-advertised row missing:\n%s", out)
	}
	m.cols = 70
	if out := clean(render(t, m)); !strings.Contains(out, "not advertised") {
		t.Errorf("stacked layout lacks the row:\n%s", out)
	}
	// never answered: no "last" age
	st2 := playingState()
	st2.SetSpotifyZC(nil, 0)
	if got := stripANSI(m.zcReadout(st2.DiagnosticView(), time.Now())); strings.Contains(got, "last") || !strings.HasPrefix(got, "not advertised · probed ") {
		t.Errorf("never-answered row = %q", got)
	}
}

// Opening the diagnostics asks the vendor nothing. A vendor query is the u key
// inside them, and its answer lands on the device section's "vendor" line.
func TestDiagUpdateCheckShowsVerdict(t *testing.T) {
	t.Setenv("LP10_OTA_URL", "http://127.0.0.1:9/") // on, but no OTA worker runs here
	m, st, _ := makeModel(t)
	m.rows, m.cols = 40, 160
	if st.DiagnosticView().OTAPending {
		t.Fatal("pending before the view opened")
	}
	m.key(kr('i'))
	if m.view != viewDiag || st.DiagnosticView().OTAPending {
		t.Fatal("i did not open the diagnostics, or asked the vendor on its own")
	}
	if out := clean(render(t, m)); strings.Contains(out, "vendor    ") {
		t.Errorf("a vendor line before u:\n%s", out)
	}
	// u asks the vendor; the view stays open
	m.key(kr('u'))
	if m.view != viewDiag || !st.DiagnosticView().OTAPending {
		t.Fatal("u did not request a vendor check (or closed the view)")
	}
	if out := clean(render(t, m)); !hasRow(out, "vendor", "checking…") {
		t.Errorf("pending check not shown:\n%s", out)
	}
	// u elsewhere is not the update key
	m.key(ke(kEsc))
	m.key(kr('u'))
	if m.view != viewPlayer {
		t.Error("u on the player should do nothing")
	}
	m.key(kr('3'))
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2"})
	if build, ok := st.TakeOTARequest(); !ok || build != "AR241CP_8747" {
		t.Fatalf("TakeOTARequest = %q %v", build, ok)
	}
	if out := clean(render(t, m)); !hasRow(out, "vendor", "checking…") {
		t.Errorf("a check in flight should still say checking:\n%s", out)
	}
	now := time.Now()
	for _, c := range []struct {
		info protocol.OTAInfo
		want string
	}{
		{protocol.OTAInfo{At: now, Asked: "AR241CP_8747", UpToDate: true}, "up to date · checked"},
		{protocol.OTAInfo{At: now, Asked: "AR241CP_8747", Offered: "AR241CP_9001"}, "AR241CP_9001 available · checked"},
		{protocol.OTAInfo{At: now, Asked: "AR241CP_8747"}, "update available · checked"},
		{protocol.OTAInfo{At: now, Err: "vendor unreachable"}, "check failed · vendor unreachable · checked"},
	} {
		st.SetOTA(c.info)
		if out := clean(render(t, m)); !hasRow(out, "vendor", c.want) {
			t.Errorf("verdict %+v: no %q row:\n%s", c.info, c.want, out)
		}
	}
	m.cols = 70
	if out := clean(render(t, m)); !strings.Contains(out, "check failed") {
		t.Errorf("stacked layout lacks the row:\n%s", out)
	}
	// never asked: no row at all
	if f := otaFact(protocol.DiagnosticSnapshot{}, now); f != "" {
		t.Errorf("unasked fact = %q", f)
	}
	if f := otaFact(protocol.DiagnosticSnapshot{OTA: &protocol.OTAInfo{At: now.Add(-3 * time.Minute), UpToDate: true}}, now); f != "up to date · checked 3m ago" {
		t.Errorf("aged fact = %q", f)
	}
}

// At mini size the diagnostics cannot be drawn, so i must not pretend to open
// them — nor u reach the vendor through the gap.
func TestDiagAtMiniSizeIsInert(t *testing.T) {
	m, st, _ := makeModel(t)
	m.rows, m.cols = MiniRows-1, 40
	m.key(kr('i'))
	m.key(kr('u'))
	if m.view == viewDiag || st.DiagnosticView().OTAPending {
		t.Errorf("mini: diag=%v otaPending=%v, want neither", m.view == viewDiag, st.DiagnosticView().OTAPending)
	}
}
