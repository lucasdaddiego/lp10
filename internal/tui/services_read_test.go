package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// lineWith is the first line of s that contains sub, trimmed ("" when none does).
func lineWith(s, sub string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if strings.Contains(ln, sub) {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

// The services pane before @@c arrives says "reading services from the
// device…" and draws no row, yet enter used to send a Spotify switch computed
// from an ASSUMED state ("" read as off → "spotify hifi"), which kills a
// running Pro engine and rewrites both flags. Now it sends nothing and says so.
func TestServicesEnterBeforeCapabilitiesSendsNothing(t *testing.T) {
	m, st, collect := makeModel(t) // the playing record: connected, no @@c yet
	if st.ConfView() != nil {
		t.Fatal("precondition: no capability block")
	}
	m.sty = newTheme()
	m.setView(viewServices)
	if out := stripANSI(strings.Join(m.renderServices(time.Now(), 120), "\n")); !strings.Contains(out, "reading services from the device") {
		t.Fatalf("precondition: the pane should be waiting:\n%s", out)
	}
	collect()
	m.key(ke(kEnter))
	for _, c := range collect() {
		if c.Mid == 92 {
			t.Errorf("enter on the unread services pane sent MID 92 %q", c.Data)
		}
	}
	if !strings.Contains(m.notice, "services not read yet") {
		t.Errorf("notice = %q, want it to say the services are not read yet", m.notice)
	}
}

// The same on a box still connecting, reached by its view key.
func TestServicesEnterOnWaitingScreenSendsNothing(t *testing.T) {
	m, _, collect := modelWith(protocol.NewState()) // connecting: no @@c yet
	m.rows, m.cols = 40, 120
	m.key(kr('3'))
	if !strings.Contains(clean(m.viewContent()), "reading services from the device…") {
		t.Fatal("setup: the pane should be waiting for the device")
	}
	m.key(ke(kEnter))
	for _, c := range collect() {
		if c.Mid == 92 {
			t.Errorf("enter on the waiting screen sent %d %q to the device", c.Mid, c.Data)
		}
	}
}

// The reconnect rate divides a count read with the digest by the window that
// count covers, which ends at the read (OpsAt), not now: the services pane
// shows the connect-time digest, and hours later its rate must neither have
// decayed nor — on a window too short at the read — have grown one.
func TestServicesReconnectRateUsesTheDigestWindow(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	applyFixtureRecords(st, "config_record.txt")
	since := time.Now().Add(-time.Hour).Format("Jan _2 15:04:05")
	protocol.ApplyRecord(st, protocol.Record{"o": {"n=6", "t=" + since}})
	m.rows, m.cols = 60, 166
	m.setView(viewServices)
	later := time.Now().Add(4 * time.Hour) // the pane opened four hours after connect
	out := stripANSI(strings.Join(m.renderServices(later, 160), "\n"))
	if !strings.Contains(out, "6.0/h") {
		t.Errorf("6 reconnects in a 1 h log window should read 6.0/h, got:\n%s", lineWith(out, "reconnect"))
	}

	// the diagnostics verdict reads the same rate: a storm at the read stays one
	protocol.ApplyRecord(st, protocol.Record{"o": {"n=20", "t=" + since}})
	d := st.DiagnosticView(later)
	if _, why := diagVerdict(withOpsVitals(collectVitals(nil, nil), d, later), d.LastRx, d.LastRx); len(why) == 0 || !strings.Contains(why[0], "engine reconnects 20.0/h") {
		t.Errorf("20 reconnects in a 1 h window, read 4 h ago: verdict reasons %q", why)
	}

	// a 10-minute window at the read is too short for a rate, however late the pane opens
	short := time.Now().Add(-10 * time.Minute).Format("Jan _2 15:04:05")
	protocol.ApplyRecord(st, protocol.Record{"o": {"n=3", "t=" + short}})
	if got := lineWith(stripANSI(strings.Join(m.renderServices(later, 160), "\n")), "reconnect"); strings.Contains(got, "/h") {
		t.Errorf("a rate over a 10-minute window: %q", got)
	}
}

// "started … ago" is the engine age read with the capability block, carried
// forward by the time since that read: @@c is re-read only at connect and after
// a toggle, so without it the age stood still for the whole connection.
func TestServicesEngineAgeAdvancesFromItsRead(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	applyFixtureRecords(st, "config_record.txt") // spotify.proc=638794 1 → 7d 9h 26m
	cv, read := st.ConfView(), time.Now()
	at := func(confAt, now time.Time) string { return stripANSI(m.engineStarted(cv, confAt, now)) }
	if got := at(read, read.Add(3*time.Hour)); !strings.HasPrefix(got, "7d 12h 26m ago") {
		t.Errorf("three hours after the read: %q, want 7d 12h 26m ago", got)
	}
	if got := at(read, read); !strings.HasPrefix(got, "7d 9h 26m ago") {
		t.Errorf("at the read: %q, want the age as read", got)
	}
	// no read time stamped: the age as read, not a guess
	if got := at(time.Time{}, read.Add(3*time.Hour)); !strings.HasPrefix(got, "7d 9h 26m ago") {
		t.Errorf("unstamped read: %q, want the age as read", got)
	}
	// the pane passes the snapshot's read time through
	d := st.DiagnosticView(read)
	d.ConfAt = read
	if got := lineWith(stripANSI(strings.Join(m.spotifyInsight(cv, d, read.Add(3*time.Hour), 160), "\n")), "started"); !strings.Contains(got, "7d 12h 26m ago") {
		t.Errorf("engine section three hours on: %q", got)
	}
}

// The Spotify pair IS consulted by the init scripts, and "configured Pro, no
// engine running" contradicts what runs — the README reserves the warn hue for
// exactly that — so the row says so and the diagnostics strip marks Spotify.
func TestServicesConfiguredEngineNotRunningIsFlagged(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "spotify.sdk=", "spotify.cfg=pro", "spotify.proc=",
		"dirty=SpotifyEnabled SpotifyProEnabled", "airplay=on", "dlna=on", "bt=on", "cast=off",
		"tidal=off", "tidal.env=off", "qobuz=off", "qobuz.env=off", "usb=off"}})
	m.rows, m.cols = 60, 160
	m.setView(viewServices)
	out := stripANSI(m.viewContent())
	if row := lineWith(out, "▸ Spotify"); !strings.Contains(row, "⚠ Pro not running") {
		t.Errorf("Pro configured, no engine running — the row says nothing is wrong:\n%s", row)
	}
	if strip := stripANSI(strings.Join(m.serviceStrip(120), "\n")); !strings.Contains(strip, "◌ Spotify") {
		t.Errorf("the diagnostics strip files Spotify under off unmarked:\n%s", strip)
	}
}

// Every way the pair and the engine can disagree, and the ways they cannot.
func TestServicesEngineMismatchCases(t *testing.T) {
	cases := []struct {
		eng, cfg string
		want     string
	}{
		{"spotifymusicpro", "hifi", "Pro is running"},
		{"newspotifyhifi", "pro", "HiFi is running"},
		{"spotifymusicpro", "none", "Pro is running"},
		{"", "hifi", "HiFi not running"},
		{"", "pro", "Pro not running"},
		{"newspotifyhifi", "hifi", ""},
		{"spotifymusicpro", "pro", ""},
		{"", "none", ""},
		{"", "both", ""},            // its state cell already says neither starts
		{"", "", ""},                // the pair unread
		{"futureengine", "pro", ""}, // an engine this build cannot place
	}
	for _, c := range cases {
		st := protocol.NewState()
		protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=" + c.eng, "spotify.cfg=" + c.cfg}})
		m, _, _ := modelWith(st)
		if got := m.engineMismatch(st.ConfView(), time.Now()); got != c.want {
			t.Errorf("eng %q cfg %q: %q, want %q", c.eng, c.cfg, got, c.want)
		}
	}
	// a loop that did not say what runs is never flagged
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.cfg=pro"}})
	m, _, _ := modelWith(st)
	if got := m.engineMismatch(st.ConfView(), time.Now()); got != "" {
		t.Errorf("engine unread: %q, want nothing", got)
	}
}

// A switch from the pane writes the pair at once but the engine takes seconds
// to appear, and the loop's second read comes eight ticks later: until then
// "set but not running" is the switch in progress, and neither the row nor
// the strip may cry fault.
func TestServicesEngineMismatchWaitsForTheSwitch(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	applyFixtureRecords(st, "config_record.txt") // hifi, running
	m.rows, m.cols = 60, 160
	m.setView(viewServices)
	now := time.Now()
	m.svcFocus = 0
	m.svcToggle(now) // → pro: the loop kills HiFi, writes the pair, reads at once
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "spotify.cfg=pro", "spotify.proc="}})
	if row := lineWith(stripANSI(m.viewContent()), "▸ Spotify"); strings.Contains(row, "⚠") {
		t.Errorf("the switch's own first read flagged as a fault: %q", row)
	}
	if strip := stripANSI(strings.Join(m.serviceStrip(120), "\n")); strings.Contains(strip, "◌ Spotify") {
		t.Errorf("the strip marked a switch still in progress:\n%s", strip)
	}
	// the engine never came up: once the second read has had its time, it is a fault
	m.svcPendingAt = now.Add(-engineAppears)
	if row := lineWith(stripANSI(m.viewContent()), "▸ Spotify"); !strings.Contains(row, "⚠ Pro not running") {
		t.Errorf("an engine still missing after the switch settled: %q", row)
	}
}

// At 80×24 the pane is taller than the frame. The rows enter acts on stay put;
// the read-out under them — the engine's age and link, the focused row's
// explanation — scrolls a page at a time, and the footer says how much is
// off-screen. Nothing is cut off out of reach.
func TestServicesPaneScrollsItsReadoutAt80x24(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	applyFixtureRecords(st, "config_record.txt")
	since := time.Now().Add(-time.Hour).Format("Jan _2 15:04:05")
	protocol.ApplyRecord(st, protocol.Record{"o": {"n=6", "t=" + since}})
	m.rows, m.cols = 24, 80
	m.setView(viewServices)
	first := stripANSI(m.viewContent())
	if foot := lineWith(first, "←→ scroll"); !strings.Contains(foot, "more rows below") {
		t.Fatalf("a too-short pane should say how much is below:\n%s", first)
	}
	seen := map[string]bool{}
	for range 10 {
		out := stripANSI(m.viewContent())
		for _, row := range []string{"▸ Spotify", "Google Cast"} {
			if !strings.Contains(out, row) {
				t.Fatalf("the control surface scrolled away (%q missing):\n%s", row, out)
			}
		}
		for _, want := range []string{"started", "reconnects", "Two engines, one runs"} {
			seen[want] = seen[want] || strings.Contains(out, want)
		}
		m.diagScrollBy(m.svcPage()) // what → pages by
	}
	for _, want := range []string{"started", "reconnects", "Two engines, one runs"} {
		if !seen[want] {
			t.Errorf("services at 80x24: %q is out of reach", want)
		}
	}
	if bottom := stripANSI(m.viewContent()); !strings.Contains(bottom, "rows above") {
		t.Errorf("paged to the end, the footer should say what is above:\n%s", bottom)
	}
	// tall enough: no scrolling, and the footer is back to its reminder
	m.rows = 60
	if tall := stripANSI(m.viewContent()); strings.Contains(tall, "←→ scroll") || !strings.Contains(tall, "enter writes the device's config") {
		t.Errorf("a tall pane should not scroll:\n%s", tall)
	}
}

// svcPage sizes ←→ by the read-out's own window, which is only right while
// svcSurfaceRows matches the surface renderServices actually draws.
func TestServicesSurfaceRowsMatchTheRender(t *testing.T) {
	m, st, _ := makeModel(t)
	m.sty = newTheme()
	applyFixtureRecords(st, "config_record.txt")
	m.rows, m.cols = 60, 120
	lines := m.renderServices(time.Now(), 114)
	for i, l := range lines {
		if strings.Contains(stripANSI(l), "── Spotify engine") {
			if i != svcSurfaceRows() { // the read-out opens with its heading
				t.Errorf("the read-out starts at row %d, svcSurfaceRows says %d", i, svcSurfaceRows())
			}
			return
		}
	}
	t.Fatal("no engine section drawn")
}
