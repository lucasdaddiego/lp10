package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// paneModel is a sized model with the capability fixture applied and one of the
// interactive overlays open.
func paneModel(t *testing.T, which int) (*model, *protocol.State, func() []protocol.Command) {
	t.Helper()
	st := protocol.NewState()
	applyFixtureRecords(st, "config_record.txt")
	m, st, collect := modelWith(st)
	m.rows, m.cols = 44, 120
	m.sty = newTheme() // renderers are called directly here, bypassing viewContent
	m.openOverlay(which)
	collect() // drop the fetch the logs pane sends on open
	return m, st, collect
}

// The pane shows ONE state per row and says what enter will do to it. The second
// truth (the configured flag) appears only where it contradicts the first —
// printing both on every row taught the eye to skip the line, which is exactly
// where the interesting case was hiding.
func TestServicesPaneShowsStateAndAction(t *testing.T) {
	m, _, _ := paneModel(t, ovServices)
	flat := clean(m.viewContent())
	for _, want := range []string{
		"─ services", "Spotify", "AirPlay 2", "DLNA / UPnP", "Tidal", "Qobuz",
		"USB playback", "Bluetooth", "Google Cast",
		"● on", "○ off",
		"legacy (hifi)",                            // the engine, not a boolean
		"enter → new (volume problem)",             // and what enter does to the focused row
		"reported only · not switchable from here", // its own group, not a broken row
		"the remote control runs on it",            // and why, in the action column
		"─ spotify engine",                         // the deep readout
		"newspotifyhifi",                           // which binary is live
		"3.203.239-g1d6bd565",                      // its eSDK build
		"writes the device's config",               // honest about what enter does
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("services pane missing %q", want)
		}
	}
	// The action belongs to the cursor: exactly one row advertises one, and it
	// names that row's real cost rather than a generic "toggle".
	if n := strings.Count(flat, "enter →"); n != 1 {
		t.Errorf("%d rows show an action, want exactly the focused one", n)
	}
	focusOn := func(id string) {
		for i, row := range svcRows {
			if row.id == id {
				m.svcFocus = i
			}
		}
	}
	for _, c := range []struct{ id, want string }{
		{"airplay", "enter → turn off · until reboot"}, // no env gate: lost on reboot
		{"usb", "enter → turn on · on reboot"},         // no daemon to kick
		{"tidal", "enter → turn on"},
	} {
		focusOn(c.id)
		if got := clean(m.viewContent()); !strings.Contains(got, c.want) {
			t.Errorf("focused on %s, pane missing %q", c.id, c.want)
		}
	}
	// A row with nothing to do must not fake an action.
	focusOn("bt")
	if strings.Contains(clean(m.viewContent()), "enter →") {
		t.Error("a not-switchable row advertised an action")
	}
	m.svcFocus = 0
	// Every framed line must still be exactly cols wide.
	for i, ln := range strings.Split(m.viewContent(), "\n") {
		if w := DispW(clean(ln)); w != m.cols {
			t.Fatalf("line %d is %d cols, want %d", i, w, m.cols)
		}
	}
}

// Enter on the Spotify row steps off -> legacy -> new -> off, writing the engine
// name rather than a boolean, because the two engines are not interchangeable.
func TestServicesPaneSpotifyCycle(t *testing.T) {
	cases := []struct{ cfg, want string }{
		{"none", "spotify hifi"},
		{"hifi", "spotify pro"},
		{"pro", "spotify off"},
		// "both" is the broken pair, not a cycle position: stepping out of it must
		// land on the safe engine, never back into it.
		{"both", "spotify hifi"},
	}
	for _, c := range cases {
		st := protocol.NewState()
		protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "spotify.cfg=" + c.cfg}})
		m, _, collect := modelWith(st)
		m.rows, m.cols = 44, 120
		m.openOverlay(ovServices)
		m.svcFocus = 0 // the Spotify row
		m.svcToggle(time.Now())
		got := collect()
		if len(got) != 1 || got[0].Mid != 92 || got[0].Data != c.want {
			t.Errorf("cfg=%s toggled to %+v, want one MID 92 %q", c.cfg, got, c.want)
		}
	}
}

// A boolean service sends 0/1, flipping whatever the daemon is currently doing.
func TestServicesPaneBooleanToggle(t *testing.T) {
	m, _, collect := paneModel(t, ovServices)
	for _, c := range []struct{ id, want string }{
		{"airplay", "airplay 0"}, // fixture has it running
		{"tidal", "tidal 1"},     // fixture has it stopped
	} {
		for i, row := range svcRows {
			if row.id == c.id {
				m.svcFocus = i
			}
		}
		m.svcToggle(time.Now())
		got := collect()
		if len(got) != 1 || got[0].Data != c.want {
			t.Errorf("%s toggled to %+v, want %q", c.id, got, c.want)
		}
	}
}

// Bluetooth carries the remote control and Cast lives in a config layer setenv
// cannot reach: both must send nothing at all rather than a command that would
// report success and do nothing.
func TestServicesPaneFixedRowsSendNothing(t *testing.T) {
	m, _, collect := paneModel(t, ovServices)
	for i, row := range svcRows {
		if row.gate != gateFixed {
			continue
		}
		m.svcFocus = i
		m.svcToggle(time.Now())
		if got := collect(); len(got) != 0 {
			t.Errorf("%s (gateFixed) sent %+v, want nothing", row.id, got)
		}
	}
}

// A service configured on but not running (or the reverse) is the fault the
// device cannot see itself; the pane must call it out on the row.
func TestServicesPaneMarksDivergence(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"c": {
		"spotify.eng=", "spotify.cfg=both", "tidal=off", "tidal.env=on",
	}})
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.openOverlay(ovServices)
	flat := clean(m.viewContent())
	if !strings.Contains(flat, "flag says on") {
		t.Error("a configured-on / not-running service was not marked")
	}
	// The both-flags-set pair must be named for what it is, not shown as a state.
	for _, want := range []string{"both flags set — neither runs", "neither starts"} {
		if !strings.Contains(flat, want) {
			t.Errorf("broken Spotify pair not explained: missing %q", want)
		}
	}
}

// Opening the logs pane asks the device once; reopening reuses the tail in hand
// rather than stalling on a fetch, and 'r' forces a fresh one.
func TestLogsPaneFetchesOnceThenOnDemand(t *testing.T) {
	st := protocol.NewState()
	m, _, collect := modelWith(st)
	m.rows, m.cols = 44, 120

	m.openOverlay(ovLogs)
	got := collect()
	if len(got) != 1 || got[0].Mid != 93 || got[0].Data != "1" {
		t.Fatalf("opening the logs pane sent %+v, want one MID 93 \"1\"", got)
	}
	m.ov = ovNone
	m.openOverlay(ovLogs)
	if got := collect(); len(got) != 0 {
		t.Errorf("reopening re-fetched: %+v", got)
	}
	m.logRequest()
	if got := collect(); len(got) != 1 {
		t.Errorf("refresh sent %+v, want one fetch", got)
	}
}

// The severity filter is a view over the one tail the device sends, so switching
// it costs no round trip — the point of filtering laptop-side.
func TestLogsPaneFilterIsLocal(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"l": {
		" Aug 25 15:28:39:871948 I/S84CAST_LITE: CastLite: SSIDSuffix h104",
		" Aug 25 15:28:42:835054 W/bluealsad: bluealsa: Read-only file system",
		" Aug 25 15:28:46:724128 E/sddp: SDDP_Service: SDDP is not enabled",
		" a line with no severity shape at all",
	}})
	m, _, collect := modelWith(st)
	m.rows, m.cols = 44, 120
	m.openOverlay(ovLogs)
	collect()

	flat := clean(m.viewContent())
	for _, want := range []string{"device log · all", "CastLite", "SDDP is not enabled", "no severity shape"} {
		if !strings.Contains(flat, want) {
			t.Errorf("logs pane missing %q", want)
		}
	}
	if !strings.Contains(flat, "15:28:39") || strings.Contains(flat, "871948") {
		t.Error("row should keep the clock and drop the microseconds")
	}

	m.logCycleFilter()
	if got := collect(); len(got) != 0 {
		t.Errorf("cycling the filter hit the device: %+v", got)
	}
	flat = clean(m.viewContent())
	if !strings.Contains(flat, "errors + warnings") {
		t.Error("filter label did not change")
	}
	if strings.Contains(flat, "CastLite") || strings.Contains(flat, "no severity shape") {
		t.Error("info and shapeless lines survived the errors filter")
	}
	for _, want := range []string{"SDDP is not enabled", "Read-only file system"} {
		if !strings.Contains(flat, want) {
			t.Errorf("errors filter dropped %q", want)
		}
	}
}

// The scroll offset counts up from the newest line and is clamped both ways, so
// a held key can neither run off the top nor unpin the tail.
func TestLogsPaneScrollClamps(t *testing.T) {
	st := protocol.NewState()
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = " Aug 25 15:28:39:000001 I/x: line"
	}
	protocol.ApplyRecord(st, protocol.Record{"l": lines})
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.openOverlay(ovLogs)

	m.logScrollBy(-5, 10)
	if m.logScroll != 0 {
		t.Errorf("scrolled below the tail: %d", m.logScroll)
	}
	m.logScrollBy(1000, 10)
	if want := 40 - 10; m.logScroll != want {
		t.Errorf("scroll clamped to %d, want %d", m.logScroll, want)
	}
}

func TestLogSeverity(t *testing.T) {
	cases := []struct {
		line string
		sev  byte
		ok   bool
	}{
		{"Aug 25 15:28:39:871948 I/luci_service[284]: hi", 'I', true},
		{"Aug 25 15:28:46:724128 E/sddp: nope", 'E', true},
		{"Aug 25 15:28:42:835054 W/bluealsad: warn", 'W', true},
		{"no severity here", 0, false},
		{"", 0, false},
		{"E/", 0, false}, // too short to carry the shape
	}
	for _, c := range cases {
		sev, _, ok := logSeverity(c.line)
		if sev != c.sev || ok != c.ok {
			t.Errorf("logSeverity(%q) = (%q, %v), want (%q, %v)", c.line, sev, ok, c.sev, c.ok)
		}
	}
}

// The three overlays are mutually exclusive, each pane's own letter closes it,
// esc backs out, and q still quits from anywhere.
func TestOverlayKeyRouting(t *testing.T) {
	m, _, _ := paneModel(t, ovServices)

	// A pane's own letter closes it; another pane's letter switches straight
	// there, because toggle-then-read-the-log is one workflow.
	if quit := m.key(keyEvent{kind: kRune, r: 'l'}); quit {
		t.Fatal("l quit the app")
	}
	if m.ov != ovLogs {
		t.Errorf("l inside the services pane went to %d, want the logs pane", m.ov)
	}
	m.key(keyEvent{kind: kRune, r: 'l'})
	if m.ov != ovNone {
		t.Error("l did not close the logs pane")
	}
	m.key(keyEvent{kind: kRune, r: 'c'})
	if m.ov != ovServices {
		t.Error("c did not open the services pane")
	}
	m.key(keyEvent{kind: kEsc})
	if m.ov != ovNone {
		t.Error("esc did not back out of the logs pane")
	}
	// The read-only diagnostics overlay is reachable from inside a pane, and
	// opening a pane closes it again — at most one of the three is ever up.
	m.key(keyEvent{kind: kRune, r: 'c'})
	m.key(keyEvent{kind: kRune, r: '?'})
	if !m.diag || m.ov != ovNone {
		t.Errorf("? inside a pane did not open the diag overlay: diag=%v ov=%d", m.diag, m.ov)
	}
	m.key(keyEvent{kind: kOther}) // any key closes the diag overlay again
	m.diag = true
	m.openOverlay(ovLogs)
	if m.diag || m.ov != ovLogs {
		t.Errorf("opening a pane left the diag overlay up: diag=%v ov=%d", m.diag, m.ov)
	}
	if quit := m.key(keyEvent{kind: kRune, r: 'q'}); !quit {
		t.Error("q did not quit from inside a pane")
	}
}

// Below the mini threshold there is no room to draw a pane, so the keys must stay
// with the dashboard rather than opening something invisible.
func TestOverlayNotOpenedAtMiniSize(t *testing.T) {
	st := protocol.NewState()
	m, _, collect := modelWith(st)
	m.rows, m.cols = 6, 40
	m.openOverlay(ovServices)
	m.openOverlay(ovLogs)
	if m.ov != ovNone {
		t.Errorf("a pane opened at mini size: ov=%d", m.ov)
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("a pane that never opened still talked to the device: %+v", got)
	}
}

// Before the first @@c the pane says it is waiting rather than drawing an empty
// list that would read as "no services".
func TestServicesPaneWaitsForDevice(t *testing.T) {
	st := protocol.NewState()
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.openOverlay(ovServices)
	if !strings.Contains(clean(m.viewContent()), "reading services from device…") {
		t.Error("services pane did not say it was waiting")
	}
}

// A toggle shows the row as pending until the device answers — and stops the
// INSTANT it does. Clearing on a fixed timer instead left the row frozen on
// "applying…" long after the device had already reported the new state, which
// made a working toggle look broken.
func TestServicesPanePendingClearsOnDeviceAnswer(t *testing.T) {
	m, st, _ := paneModel(t, ovServices)
	now := time.Now()
	m.svcFocus = 0 // Spotify: the fixture has it on hifi, so enter asks for pro
	m.svcToggle(now)
	// pending shows the state it is HEADING TO, not a blank "applying"
	if !strings.Contains(clean(m.renderJoin(now)), "… new (volume problem)") {
		t.Fatal("a just-toggled row did not read as pending")
	}
	// the device reports something else: still waiting
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "spotify.cfg=hifi"}})
	if !strings.Contains(clean(m.renderJoin(now)), "… new (volume problem)") {
		t.Error("pending cleared on an answer that was not the state asked for")
	}
	// the device reports the state that was asked for: done, well inside the window
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=spotifymusicpro", "spotify.cfg=pro"}})
	if strings.Contains(clean(m.renderJoin(now)), "…") {
		t.Error("pending survived the device confirming the toggle")
	}
}

// If the device never answers at all, the row gives up rather than sitting on
// "applying…" forever.
func TestServicesPanePendingGivesUp(t *testing.T) {
	m, _, _ := paneModel(t, ovServices)
	now := time.Now()
	m.svcFocus = 0
	m.svcToggle(now)
	if strings.Contains(clean(m.renderJoin(now.Add(svcPendingFor+time.Second))), "…") {
		t.Error("the pending marker outlived its window")
	}
}

// renderJoin renders the services pane at a given instant.
func (m *model) renderJoin(now time.Time) string {
	return strings.Join(m.renderServices(now, m.cols-6), "\n")
}

// Navigation wraps in both directions, so a held arrow cannot walk off the list.
func TestServicesPaneNavigationWraps(t *testing.T) {
	m, _, _ := paneModel(t, ovServices)
	m.svcFocus = 0
	m.svcMove(-1)
	if want := len(svcRows) - 1; m.svcFocus != want {
		t.Errorf("up from the top landed on %d, want %d", m.svcFocus, want)
	}
	m.svcMove(+1)
	if m.svcFocus != 0 {
		t.Errorf("down from the bottom landed on %d, want 0", m.svcFocus)
	}
}

// Each pane's arrow and letter keys reach their handler (and only theirs).
func TestOverlayKeyHandlers(t *testing.T) {
	m, _, collect := paneModel(t, ovServices)
	m.svcFocus = 0
	m.key(keyEvent{kind: kDown})
	if m.svcFocus != 1 {
		t.Errorf("down moved to %d, want 1", m.svcFocus)
	}
	m.key(keyEvent{kind: kUp})
	if m.svcFocus != 0 {
		t.Errorf("up moved to %d, want 0", m.svcFocus)
	}
	m.key(keyEvent{kind: kEnter})
	if got := collect(); len(got) != 1 || got[0].Mid != 92 {
		t.Errorf("enter sent %+v, want one MID 92", got)
	}

	lm, _, lc := paneModel(t, ovLogs)
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = " Aug 25 15:28:39:000001 I/x: line"
	}
	protocol.ApplyRecord(lm.st, protocol.Record{"l": lines})
	lc()
	lm.key(keyEvent{kind: kUp})
	if lm.logScroll != 1 {
		t.Errorf("up scrolled to %d, want 1", lm.logScroll)
	}
	lm.key(keyEvent{kind: kLeft}) // page up
	if lm.logScroll <= 1 {
		t.Errorf("left did not page: %d", lm.logScroll)
	}
	lm.key(keyEvent{kind: kRight}) // page down
	lm.key(keyEvent{kind: kDown})
	if lm.logScroll != 0 {
		t.Errorf("paging back did not return to the tail: %d", lm.logScroll)
	}
	if lm.key(keyEvent{kind: kRune, r: 'f'}); lm.logFilter != 1 {
		t.Errorf("f did not cycle the filter: %d", lm.logFilter)
	}
	if got := lc(); len(got) != 0 {
		t.Errorf("f hit the device: %+v", got)
	}
	lm.key(keyEvent{kind: kRune, r: 'r'})
	if got := lc(); len(got) != 1 || got[0].Mid != 93 {
		t.Errorf("r sent %+v, want one MID 93", got)
	}
	// a key neither pane claims is simply ignored
	lm.key(keyEvent{kind: kRune, r: 'z'})
	if lm.ov != ovLogs {
		t.Error("an unclaimed key closed the pane")
	}
}

// The logs pane distinguishes never-asked, asked-but-unanswered, and answered-
// but-empty: all three would otherwise render as the same blank box.
func TestLogsPaneEmptyStates(t *testing.T) {
	st := protocol.NewState()
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.sty = newTheme()

	if !strings.Contains(clean(strings.Join(m.renderLogs(time.Now(), 114), "\n")), "press r to fetch") {
		t.Error("un-asked pane did not invite a fetch")
	}
	m.openOverlay(ovLogs)
	if !strings.Contains(clean(strings.Join(m.renderLogs(time.Now(), 114), "\n")), "asking the device…") {
		t.Error("asked-but-unanswered pane did not say so")
	}
	// an answer that matched nothing under the current filter
	protocol.ApplyRecord(st, protocol.Record{"l": {" Aug 25 15:28:39:000001 I/x: info only"}})
	m.logFilter = 1 // errors + warnings
	if !strings.Contains(clean(strings.Join(m.renderLogs(time.Now(), 114), "\n")), "nothing matched this filter") {
		t.Error("an empty filtered view did not say so")
	}
	// the scrolled marker appears once the viewport is off the tail
	m.logFilter = 0
	m.logScroll = 3
	if !strings.Contains(clean(strings.Join(m.renderLogs(time.Now(), 114), "\n")), "scrolled 3") {
		t.Error("scroll position not reported")
	}
}

// The engine readout names the cost of each engine, and says plainly when none
// is running — the row it replaces would otherwise imply Spotify simply works.
func TestSpotifyInsightPerEngine(t *testing.T) {
	cases := []struct{ eng, want string }{
		{"newspotifyhifi", "legacy · Ogg/AAC only · volume works"},
		{"spotifymusicpro", "volume does not attenuate"},
		{"", "none running"},
		{"somethingelse", "somethingelse"},
	}
	for _, c := range cases {
		st := protocol.NewState()
		protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=" + c.eng, "spotify.cfg=hifi"}})
		m, _, _ := modelWith(st)
		m.rows, m.cols, m.sty = 44, 120, newTheme()
		got := clean(strings.Join(m.spotifyInsight(m.st.ConfView(), m.st.DiagnosticView(time.Now()), time.Now(), 114), "\n"))
		if !strings.Contains(got, c.want) {
			t.Errorf("engine %q readout %q, want it to contain %q", c.eng, got, c.want)
		}
	}
}

// A section rule narrower than its own title must not produce a negative repeat.
func TestSectionHeadNarrow(t *testing.T) {
	m, _, _ := paneModel(t, ovServices)
	if got := clean(m.sectionHead("a very long section title", 4)); got == "" {
		t.Error("narrow section head rendered nothing")
	}
}

// Toggling before the first @@c has landed must still send something sane
// rather than reading a nil capability view as "everything is on".
func TestServicesToggleBeforeFirstCapabilityBlock(t *testing.T) {
	st := protocol.NewState()
	m, _, collect := modelWith(st)
	m.rows, m.cols, m.sty = 44, 120, newTheme()
	m.openOverlay(ovServices)
	m.svcFocus = 0 // Spotify: unknown config steps to the safe engine
	m.svcToggle(time.Now())
	if got := collect(); len(got) != 1 || got[0].Data != "spotify hifi" {
		t.Errorf("spotify toggle with no @@c sent %+v, want \"spotify hifi\"", got)
	}
	for i, row := range svcRows {
		if row.id == "tidal" {
			m.svcFocus = i
		}
	}
	m.svcToggle(time.Now())
	if got := collect(); len(got) != 1 || got[0].Data != "tidal 1" {
		t.Errorf("tidal toggle with no @@c sent %+v, want \"tidal 1\"", got)
	}
}

// A frame with no room left for the viewport still renders at least one row
// rather than computing a zero or negative page.
func TestLogsPaneTinyFrame(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"l": {" Aug 25 15:28:39:000001 I/x: only line"}})
	m, _, _ := modelWith(st)
	m.rows, m.cols, m.sty = MiniRows+1, 80, newTheme()
	m.openOverlay(ovLogs)
	if got := m.renderLogs(time.Now(), 74); len(got) != m.rows-2 {
		t.Errorf("tiny frame produced %d lines, want %d", len(got), m.rows-2)
	}
}

// Pressing enter repeatedly must keep advancing the cycle even while the device
// has not answered yet. Stepping from the device's last report instead meant a
// second press re-sent the same target, so the row appeared frozen for as long as
// the daemon took to start — the cycle was effectively one press per round trip.
func TestServicesPaneCyclesFasterThanTheDevice(t *testing.T) {
	m, _, collect := paneModel(t, ovServices)
	now := time.Now()
	m.svcFocus = 0 // Spotify, reported as hifi by the fixture

	var asked []string
	for range 4 {
		m.svcToggle(now)
		for _, c := range collect() {
			asked = append(asked, c.Data)
		}
	}
	// hifi -> pro -> off -> hifi -> pro, without a single device answer.
	want := []string{"spotify pro", "spotify off", "spotify hifi", "spotify pro"}
	if strings.Join(asked, "|") != strings.Join(want, "|") {
		t.Errorf("presses asked for %v, want %v", asked, want)
	}
	// The row shows where it is heading, not a blank "applying".
	if got := clean(m.renderJoin(now)); !strings.Contains(got, "… new (volume problem)") {
		t.Error("a pending row did not show the state it is heading to")
	}
	// And a burst reaches the device as one command carrying the final choice.
	reduced := protocol.ReduceCommands([]protocol.Command{
		{Mid: 92, Data: "spotify pro"}, {Mid: 92, Data: "spotify off"},
		{Mid: 92, Data: "spotify hifi"}, {Mid: 92, Data: "spotify pro"},
	})
	if len(reduced) != 1 || reduced[0].Data != "spotify pro" {
		t.Errorf("burst reduced to %+v, want one \"spotify pro\"", reduced)
	}
}

// A flag its init script never reads is inert, not a fault. AirPlay and DLNA are
// both in that position; marking only whichever one's flag happens to disagree,
// in the warn hue, read as "this service is broken" when nothing is wrong. Both
// carry the same neutral note, and the warn hue is kept for a flag that IS
// consulted and still contradicts what is running.
func TestServicesPaneDistinguishesInertFlagFromFault(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"c": {
		"spotify.eng=newspotifyhifi", "spotify.cfg=hifi",
		"airplay=on", "airplay.env=on", // agrees, but the flag is still inert
		"dlna=on", "dlna.env=off", // disagrees, and still not a fault
		"tidal=off", "tidal.env=on", // consulted AND contradicted: a real fault
	}})
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.openOverlay(ovServices)
	flat := clean(m.viewContent())

	if n := strings.Count(flat, "flag not consulted"); n != 2 {
		t.Errorf("%d rows say the flag is inert, want both daemon-gated ones", n)
	}
	if n := strings.Count(flat, "⚠ flag says"); n != 1 {
		t.Errorf("%d rows carry the warn marker, want only the genuine fault", n)
	}
	for ln := range strings.SplitSeq(flat, "\n") {
		if strings.Contains(ln, "DLNA") && strings.Contains(ln, "⚠") {
			t.Errorf("an inert flag was marked as a fault: %q", strings.TrimRight(ln, " "))
		}
	}
}

// A record that carries no spotify.cfg at all is UNKNOWN, not off. Falling
// through to the first cycle position painted a lit dot beside the word "off".
func TestServicesPaneUnknownSpotifyConfig(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "tidal=off"}})
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.openOverlay(ovServices)
	for ln := range strings.SplitSeq(clean(m.viewContent()), "\n") {
		if strings.Contains(ln, "Spotify ") {
			if !strings.Contains(ln, "—") {
				t.Errorf("unknown Spotify config rendered as %q, want an unknown marker", strings.TrimRight(ln, " "))
			}
			return
		}
	}
	t.Fatal("no Spotify row rendered")
}

// The engine path had a settle test; the boolean one did not. A toggle on an
// ordinary service must clear the moment the device reports the daemon in the
// requested state, and stay pending while it reports the opposite.
func TestServicesPaneBooleanTogglePending(t *testing.T) {
	for _, c := range []struct{ start, want, confirm string }{
		{"on", "0", "off"}, // running -> asked to stop -> device reports stopped
		{"off", "1", "on"}, // and the reverse
	} {
		st := protocol.NewState()
		protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "spotify.cfg=hifi", "tidal=" + c.start}})
		m, _, collect := modelWith(st)
		m.rows, m.cols, m.sty = 44, 120, newTheme()
		m.openOverlay(ovServices)
		for i, row := range svcRows {
			if row.id == "tidal" {
				m.svcFocus = i
			}
		}
		now := time.Now()
		m.svcToggle(now)
		if got := collect(); len(got) != 1 || got[0].Data != "tidal "+c.want {
			t.Fatalf("start=%s toggled to %+v, want %q", c.start, got, "tidal "+c.want)
		}
		// pending shows the state it is heading to, named as on/off
		if got := clean(m.renderJoin(now)); !strings.Contains(got, "… "+c.confirm) {
			t.Errorf("start=%s: pending row did not name its target %q", c.start, c.confirm)
		}
		// the device still reporting the old state keeps it pending
		protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "tidal=" + c.start}})
		if got := clean(m.renderJoin(now)); !strings.Contains(got, "… "+c.confirm) {
			t.Errorf("start=%s: pending cleared before the device moved", c.start)
		}
		// and the device confirming clears it
		protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=", "tidal=" + c.confirm}})
		if got := clean(m.renderJoin(now)); strings.Contains(got, "… "+c.confirm) {
			t.Errorf("start=%s: pending survived confirmation", c.start)
		}
	}
}

// An engine string the cycle does not recognise must step to the safe engine
// rather than wedging, and must not be painted as one of the known states.
func TestServicesPaneUnknownEngineFallsBackSafely(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"c": {"spotify.eng=weirdengine", "spotify.cfg=hifi"}})
	m, _, collect := modelWith(st)
	m.rows, m.cols, m.sty = 44, 120, newTheme()
	m.openOverlay(ovServices)
	m.svcFocus = 0
	// the engine readout names it verbatim rather than guessing
	if got := clean(strings.Join(m.spotifyInsight(m.st.ConfView(), m.st.DiagnosticView(time.Now()), time.Now(), 114), "\n")); !strings.Contains(got, "weirdengine") {
		t.Errorf("unknown engine not reported: %q", got)
	}
	// A pending target the row cannot interpret must settle rather than wedge the
	// row on "applying…" for the full window, and the next press then steps from
	// the device's own position.
	m.svcPending, m.svcPendingWant, m.svcPendingAt = "spotify", "nonsense", time.Now()
	if m.svcPendingRow("spotify", m.st.ConfView(), time.Now()) {
		t.Error("an uninterpretable pending target left the row waiting")
	}
	m.svcToggle(time.Now())
	if got := collect(); len(got) != 1 || got[0].Data != "spotify pro" {
		t.Errorf("next press sent %+v, want it to step from the device position", got)
	}
}

// The diagnostics strip and the services pane must agree about what counts as a
// divergence, or the same device state reads as broken in one view and fine in
// the other.
func TestDiagStripDivergenceMatchesPane(t *testing.T) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, protocol.Record{"c": {
		"spotify.eng=newspotifyhifi", "spotify.cfg=hifi",
		"dlna=on",                   // inert flag, not carried at all now
		"tidal=off", "tidal.env=on", // consulted and contradicted: a real fault
	}})
	m, _, _ := modelWith(st)
	m.rows, m.cols, m.sty = 44, 120, newTheme()
	strip := clean(strings.Join(m.serviceStripFor(m.st.ConfView(), 114), "\n"))
	if !strings.Contains(strip, "Tidal") {
		t.Fatalf("strip missing Tidal: %q", strip)
	}
	// The strip renders; the pane's rule (gateDaemon flags are inert) is applied
	// there too, so DLNA is never singled out.
	if cv := m.st.ConfView(); cv.Divergent("dlna") {
		t.Error("an uncarried flag was treated as a divergence")
	}
}

// 's' switches the pane between the device syslog and the vendor app's log.
// Each source is fetched once on first view (MID 93 with its own wire value)
// and then shown from the tail in hand; 'r' refreshes the selected one.
func TestLogsPaneSourceSwitch(t *testing.T) {
	m, st, collect := paneModel(t, ovLogs)
	m.key(keyEvent{kind: kRune, r: 's'})
	got := collect()
	if len(got) != 1 || got[0].Mid != 93 || got[0].Data != "2" {
		t.Fatalf("switching to the vendor log sent %+v, want one MID 93 \"2\"", got)
	}
	if !strings.Contains(strings.Join(m.renderLogs(time.Now(), 100), "\n"), "vendor app log") {
		t.Error("heading does not name the vendor source")
	}
	protocol.ApplyRecord(st, protocol.Record{"L": {
		" [2026-09-02 00:06:26.775] [DEBUG] [luci-rx][tunnel] MB#112 payload=\"TRE;\"",
		" [2026-09-02 00:06:26.779] [ERROR] [preset-current] publish failed: nope",
	}})
	flat := strings.Join(m.renderLogs(time.Now(), 100), "\n")
	for _, want := range []string{"00:06:26", "[luci-rx][tunnel] MB#112", "publish failed"} {
		if !strings.Contains(flat, want) {
			t.Errorf("vendor tail not rendered: missing %q in\n%s", want, flat)
		}
	}
	if strings.Contains(flat, "2026-09-02") || strings.Contains(flat, ".775") {
		t.Error("the date and the milliseconds should be dropped from the clock column")
	}
	// The severity filter understands the vendor levels too.
	m.logCycleFilter()
	if lines, _ := m.logVisible(); len(lines) != 1 || !strings.Contains(lines[0], "[ERROR]") {
		t.Errorf("errors filter over the vendor tail = %q", lines)
	}
	m.logCycleFilter()
	// Back to syslog: shown from the tail in hand, no second fetch; 'r' refreshes
	// the selected source with its own wire value.
	m.key(keyEvent{kind: kRune, r: 's'})
	if got := collect(); len(got) != 0 {
		t.Errorf("returning to a fetched source re-fetched: %+v", got)
	}
	m.key(keyEvent{kind: kRune, r: 'r'})
	if got := collect(); len(got) != 1 || got[0].Data != "1" {
		t.Errorf("refresh on syslog sent %+v, want MID 93 \"1\"", got)
	}
	m.key(keyEvent{kind: kRune, r: 's'})
	m.key(keyEvent{kind: kRune, r: 'r'})
	if got := collect(); len(got) != 1 || got[0].Data != "2" {
		t.Errorf("refresh on the vendor log sent %+v, want MID 93 \"2\"", got)
	}
}

func TestParseLogLine(t *testing.T) {
	cases := []struct {
		line, clock, rest string
		sev               byte
		ok                bool
	}{
		{"Aug 25 15:28:39:871948 I/luci_service[284]: hi", "15:28:39", "luci_service[284]: hi", 'I', true},
		{"[2026-09-02 00:06:26.775] [DEBUG] [luci-rx] MB#112", "00:06:26", "[luci-rx] MB#112", 'D', true},
		{"[2026-09-02 00:06:26.775] [WARN] slow", "00:06:26", "slow", 'W', true},
		{"[2026-09-02 00:06:26.775] [INFO] up", "00:06:26", "up", 'I', true},
		{"[2026-09-02 00:06:26.775] [FATAL] down", "00:06:26", "down", 'F', true},
		{"[2026-09-02 00:06:26.775] [WHAT] odd level", "", "", 0, false},
		{"[2026-09-02 00:06:26.775] no level", "", "", 0, false},
		{"[unterminated", "", "", 0, false},
		{"[00:06:26] [DEBUG", "", "", 0, false},
		{"plain text", "", "", 0, false},
	}
	for _, c := range cases {
		clock, sev, rest, ok := parseLogLine(c.line)
		if clock != c.clock || sev != c.sev || rest != c.rest || ok != c.ok {
			t.Errorf("parseLogLine(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
				c.line, clock, sev, rest, ok, c.clock, c.sev, c.rest, c.ok)
		}
	}
}

// The vendor app's version sits on the device card beside the firmware build,
// because it moves on its own schedule.
func TestDeviceCardShowsVendorApp(t *testing.T) {
	st := protocol.NewState()
	applyRaw(st, "@@i\nnet=eth\nbuild=2026-01-12\napp=318\nvapp=32\n@@E\n")
	id := collectIdentity(nil, st.DiagnosticView(time.Now()).DevInfo, nil)
	if id.build != "2026-01-12 · app 318 · vendor app v32" {
		t.Errorf("build line = %q", id.build)
	}
}
