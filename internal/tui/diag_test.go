package tui

// The diagnostics view under the tunnel: the audio, connection, device and
// hardware sections, the health verdict and its reasons, the error line's
// window, the scrolling read-out, and the two layouts by width.

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/sweep"
)

// fullDiagState is a box with every diagnostics source answered: the tunnel
// (status, VND, a track, VER, the EQ and its presets), the LSSDP responder,
// the Spotify engine's ZeroConf, and a vendor verdict.
func fullDiagState() *protocol.State {
	st := playingState()
	st.ApplyVersion("29-1d316f0c-10")
	st.SetEQPresets(livePresets)
	st.ApplyTunnel("MXV", 100)
	st.ApplyTunnel("EQE", 1)
	st.ApplyTunnel("EQS", 2)
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2", State: "S", NetMode: "ETH0"})
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK", LibraryVersion: "3.216.31-g1"}, 9095)
	st.SetOTA(protocol.OTAInfo{At: time.Now(), Asked: "AR241CP_8747", UpToDate: true})
	return st
}

// testBaseline is a saved `lp10 sweep` that read the same identity on Sep 12.
func testBaseline() *sweep.Report {
	base := &sweep.Report{At: time.Date(2026, 9, 12, 16, 0, 0, 0, time.Local)}
	base.LSSDP.FW = "AR241CP_8747.29.2"
	base.Tunnel.MCU = "29"
	base.ZeroConf.LibraryVersion = "3.216.31-g1"
	return base
}

// sectionHeads lists the rows of a clean frame that carry a section rule.
func sectionHeads(frame string) []string {
	var out []string
	for ln := range strings.SplitSeq(frame, "\n") {
		if strings.Contains(ln, "─ audio") || strings.Contains(ln, "─ connection") ||
			strings.Contains(ln, "─ device") || strings.Contains(ln, "─ hardware") {
			out = append(out, ln)
		}
	}
	return out
}

// W ≥ diagCardsMinW: two ruled columns side by side, the sections split where
// the heights balance. Narrower: one stacked column, alphabetical.
func TestDiagLayoutsByWidth(t *testing.T) {
	st := fullDiagState()
	m, _, _ := modelWith(st)
	m.baseline = testBaseline()
	m.view = viewDiag
	m.rows = 60

	m.cols = diagCardsMinW + 6 // W = diagCardsMinW exactly: the wide layout
	wide := clean(render(t, m))
	secs := m.diagSections(st.DiagnosticView(), time.Now())
	split := splitSections(secs)
	if split <= 0 || split >= len(secs) {
		t.Fatalf("split = %d of %d sections", split, len(secs))
	}
	if !hasRow(wide, "─ "+secs[0].title+" ", "─ "+secs[split].title+" ") {
		t.Errorf("wide: %s and %s should head the two columns side by side:\n%s", secs[0].title, secs[split].title, wide)
	}
	if n := len(sectionHeads(wide)); n >= len(secs) {
		t.Errorf("wide: %d rows carry section rules, want fewer than %d (paired)", n, len(secs))
	}

	m.cols = diagCardsMinW + 5 // one column short: stacked
	narrow := clean(render(t, m))
	heads := sectionHeads(narrow)
	if len(heads) != 4 {
		t.Fatalf("stacked: %d section rows, want 4 (one per row):\n%s", len(heads), narrow)
	}
	for i, title := range []string{"audio", "connection", "device", "hardware"} {
		if !strings.Contains(heads[i], "─ "+title+" ") {
			t.Errorf("stacked section %d = %q, want %s (alphabetical)", i, heads[i], title)
		}
	}
	for _, out := range []string{wide, narrow} {
		for _, want := range []string{"diagnostics", "● healthy", "● good   ● warn   ● fault", "u asks the vendor"} {
			if !strings.Contains(out, want) {
				t.Errorf("diagnostics lack %q:\n%s", want, out)
			}
		}
	}
}

// Each fact sits in the section that answers its question — asserted in the
// stacked layout, where the single column makes rule order == membership.
func TestDiagSectionsHoldTheirFacts(t *testing.T) {
	st := fullDiagState()
	m, _, _ := modelWith(st)
	m.baseline = testBaseline()
	m.rows, m.cols = 60, 90
	m.view = viewDiag
	flat := clean(render(t, m))
	lines := strings.Split(flat, "\n")
	idx := func(sub string) int {
		t.Helper()
		for i, ln := range lines {
			if strings.Contains(ln, sub) {
				return i
			}
		}
		t.Fatalf("diagnostics missing %q:\n%s", sub, flat)
		return -1
	}
	inSection := func(row, sec, next string) {
		t.Helper()
		r, a := idx(row), idx("─ "+sec+" ─")
		b := len(lines)
		if next != "" {
			b = idx("─ " + next + " ─")
		}
		if r < a || r > b {
			t.Errorf("the %q row should sit in the %s section", row, sec)
		}
	}
	for _, c := range []struct{ row, sec, next string }{
		{"source    Spotify", "audio", "connection"},
		{"state     playing · De Música Ligera — Soda Stereo", "audio", "connection"},
		{"volume    44%", "audio", "connection"},
		{"max vol   100%", "audio", "connection"},
		{"eq        on · Pop", "audio", "connection"},
		{"tunnel    live · :2018 · last frame", "connection", "device"},
		{"host      " + testHost, "connection", "device"},
		{"lssdp     answered", "connection", "device"},
		{"spotify   answered", "connection", "device"},
		{"firmware  AR241CP_8747.29.2", "device", "hardware"},
		{"mcu       29-1d316f0c-10", "device", "hardware"},
		{"eSDK      3.216.31-g1", "device", "hardware"},
		{"sweep     unchanged since Sep 12 16:00", "device", "hardware"},
		{"vendor    up to date · checked", "device", "hardware"},
		{"dac       MVSilicon BP10xx MCU", "hardware", ""},
		{"soc       Amlogic A113L", "hardware", ""},
	} {
		inSection(c.row, c.sec, c.next)
	}
}

// audioFacts is the player as the tunnel reports it.
func TestAudioFacts(t *testing.T) {
	facts := func(st *protocol.State) map[string]string {
		m, _, _ := modelWith(st)
		out := map[string]string{}
		for _, f := range m.audioFacts(st.DiagnosticView()) {
			out[f.k] = f.v
		}
		return out
	}
	// the tunnel down: one row says so
	down := protocol.NewState()
	down.ApplyTunnel("MXV", 100)
	if got := facts(down); len(got) != 1 || got["source"] != "— (the tunnel is down)" {
		t.Errorf("down = %v", got)
	}
	// idle, the input named, no EQ read: source, state, volume only
	if got := facts(idleState()); got["source"] != "Network" || got["state"] != "idle" || got["volume"] != "44%" || len(got) != 3 {
		t.Errorf("idle = %v", got)
	}
	// playing untitled: the hint stands in for the title
	if got := facts(untitledState())["state"]; got != "playing · "+untitledHint {
		t.Errorf("untitled state = %q", got)
	}
	// a paused track
	paused := playingState()
	paused.ApplyPlaying(false)
	if got := facts(paused)["state"]; got != "paused · De Música Ligera — Soda Stereo" {
		t.Errorf("paused state = %q", got)
	}
	// the mute is the MCU's: the level stays and the row says so
	muted := playingState()
	muted.ToggleMute()
	if got := facts(muted)["volume"]; got != "44% · muted (in the MCU)" {
		t.Errorf("muted volume = %q", got)
	}
	// the EQ: off, on with the preset's name, on with an index past the list
	eq := playingState()
	eq.ApplyTunnel("MXV", 85)
	eq.ApplyTunnel("EQE", 0)
	if got := facts(eq); got["max vol"] != "85%" || got["eq"] != "off" {
		t.Errorf("eq off = %v", got)
	}
	eq.ApplyTunnel("EQE", 1)
	if got := facts(eq)["eq"]; got != "on" {
		t.Errorf("eq on, no preset read = %q", got)
	}
	eq.ApplyTunnel("EQS", 4)
	if got := facts(eq)["eq"]; got != "on" {
		t.Errorf("eq on, no names read = %q", got)
	}
	eq.SetEQPresets(livePresets)
	if got := facts(eq)["eq"]; got != "on · Rock" {
		t.Errorf("eq on · preset = %q", got)
	}
	eq.ApplyTunnel("EQS", 9)
	if got := facts(eq)["eq"]; got != "on" {
		t.Errorf("eq on, preset past the list = %q", got)
	}
	// connected before anything is named: no empty source row
	bare := protocol.NewState()
	connect(bare)
	if got, ok := facts(bare)["source"]; ok {
		t.Errorf("an unnamed source should leave no row, got %q", got)
	}
}

// tunnelReadout: down with the attempts so far, or live with the age of the
// last frame in its severity pen.
func TestTunnelReadout(t *testing.T) {
	m, _, _ := makeModel(t)
	now := time.Now()
	ps := m.sty
	cases := []struct {
		name string
		d    protocol.DiagnosticSnapshot
		want string
	}{
		{"down, first try", protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Attempts: 1}}, "down"},
		{"down, retrying", protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Attempts: 3}}, "down · 3 attempts"},
		{"live, no frame stamp", protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}}, "live · :2018"},
		{"live", protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}, LastRx: now.Add(-800 * time.Millisecond)},
			"live · :2018 · last frame 0.8s ago"},
	}
	for _, c := range cases {
		if got := stripANSI(m.tunnelReadout(c.d, now)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// the age takes the verdict's thresholds: good, warn, fault
	for _, c := range []struct {
		age time.Duration
		sev int
	}{{time.Second, 0}, {3500 * time.Millisecond, 1}, {6 * time.Second, 2}} {
		d := protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}, LastRx: now.Add(-c.age)}
		want := stylePen(ps.sevs[c.sev]).render(fmt.Sprintf("%.1fs", c.age.Seconds()))
		if got := m.tunnelReadout(d, now); !strings.Contains(got, want) {
			t.Errorf("a %v gap should render in severity %d: %q", c.age, c.sev, got)
		}
	}
}

// The verdict rolls the live signals up: the tunnel's quiet against thrRx
// (warn past one and three quarter polls, fault at SilentAfter), an LSSDP
// probe nobody answered, a ZeroConf probe nobody answered. It names at most
// two reasons, worst first.
func TestDiagVerdict(t *testing.T) {
	now := time.Now()
	live := func(gap time.Duration) protocol.DiagnosticSnapshot {
		return protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}, LastRx: now.Add(-gap)}
	}
	probed := func(d protocol.DiagnosticSnapshot, lssdp, zc bool) protocol.DiagnosticSnapshot {
		if lssdp {
			d.LSSDPProbeAt = now
		}
		if zc {
			d.ZCProbeAt = now
		}
		return d
	}
	if thrRx != [2]float64{3.5, 6} {
		t.Fatalf("thrRx = %v, want [3.5 6] (StatusEvery 2 s, SilentAfter 6 s)", thrRx)
	}
	answered := probed(live(time.Second), true, true)
	answered.LSSDP, answered.SpotifyZC = &protocol.LSSDPInfo{}, &protocol.SpotifyZC{}
	cases := []struct {
		name  string
		d     protocol.DiagnosticSnapshot
		worst int
		why   []string
	}{
		{"fresh frame", live(time.Second), 0, nil},
		{"a missed poll", live(3 * time.Second), 0, nil},
		{"quiet", live(3500 * time.Millisecond), 1, []string{"tunnel quiet"}},
		{"silent", live(6 * time.Second), 2, []string{"tunnel quiet"}},
		{"no frame yet", protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}}, 0, nil},
		{"lssdp silent", probed(live(time.Second), true, false), 1, []string{"lssdp not answering"}},
		{"zeroconf silent", probed(live(time.Second), false, true), 1, []string{"spotify engine not answering"}},
		{"two warns", probed(live(time.Second), true, true), 1, []string{"lssdp not answering", "spotify engine not answering"}},
		{"everything, worst first, two named", probed(live(7*time.Second), true, true), 2, []string{"tunnel quiet", "lssdp not answering"}},
		{"probes answered", answered, 0, nil},
	}
	for _, c := range cases {
		worst, why := diagVerdict(c.d, now)
		if worst != c.worst || !slices.Equal(why, c.why) {
			t.Errorf("%s: verdict %d %q, want %d %q", c.name, worst, why, c.worst, c.why)
		}
	}
}

// The masthead says the verdict and why, or "disconnected" with no verdict;
// at any width it fits the row, the reasons clipped first.
func TestDiagMasthead(t *testing.T) {
	m, _, _ := makeModel(t)
	now := time.Now()
	clock := now.Format("15:04")
	live := protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}, LastRx: now}
	quiet := protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}, LastRx: now.Add(-4 * time.Second)}
	silent := protocol.DiagnosticSnapshot{Snapshot: protocol.Snapshot{Connected: true}, LastRx: now.Add(-7 * time.Second),
		LSSDPProbeAt: now, ZCProbeAt: now}
	cases := []struct {
		name string
		d    protocol.DiagnosticSnapshot
		want []string
	}{
		{"healthy", live, []string{"diagnostics   ● healthy", "● " + clock}},
		{"warn", quiet, []string{"diagnostics   ● warn · tunnel quiet", "● " + clock}},
		{"fault", silent, []string{"diagnostics   ● fault · tunnel quiet · lssdp not answering"}},
		{"disconnected", protocol.DiagnosticSnapshot{}, []string{"diagnostics", "● disconnected"}},
	}
	for _, c := range cases {
		for _, W := range []int{MiniCols - 6, 74, 114} {
			row := m.diagMasthead(c.d, now, W)
			if got := visWidth(row); got != W {
				t.Errorf("%s at W=%d: masthead %d wide", c.name, W, got)
			}
			if W < 114 {
				continue
			}
			for _, want := range c.want {
				if !strings.Contains(stripANSI(row), want) {
					t.Errorf("%s: masthead %q lacks %q", c.name, stripANSI(row), want)
				}
			}
		}
	}
	if got := stripANSI(m.diagMasthead(protocol.DiagnosticSnapshot{}, now, 114)); strings.Contains(got, "healthy") || strings.Contains(got, "fault") {
		t.Errorf("a disconnected masthead names no verdict: %q", got)
	}
	// the reasons give way to the row: clipped, the verdict word kept
	if got := stripANSI(m.diagMasthead(silent, now, MiniCols-6)); !strings.Contains(got, "● fault ·") || !strings.Contains(got, GL["ell"]) {
		t.Errorf("narrow masthead = %q", got)
	}
}

// deviceFacts is the identity list: the LSSDP firmware, the MCU build, the
// eSDK, what moved since the last sweep, and the vendor's verdict when asked.
func TestDeviceFacts(t *testing.T) {
	st := fullDiagState()
	m, _, _ := modelWith(st)
	m.baseline = testBaseline()
	got := m.deviceFacts(st.DiagnosticView(), time.Now())
	var keys []string
	for _, f := range got {
		keys = append(keys, f.k)
	}
	if !slices.Equal(keys, []string{"firmware", "mcu", "eSDK", "sweep", "vendor"}) {
		t.Errorf("device rows = %v", keys)
	}
	if got[0].v != "AR241CP_8747.29.2" || got[1].v != "29-1d316f0c-10" || got[2].v != "3.216.31-g1" ||
		got[3].v != "unchanged since Sep 12 16:00" || !strings.HasPrefix(got[4].v, "up to date · checked ") {
		t.Errorf("device facts = %+v", got)
	}
	// nothing read yet: no device section at all
	m2, st2, _ := modelWith(playingState())
	m2.baseline = testBaseline()
	if facts := m2.deviceFacts(st2.DiagnosticView(), time.Now()); len(facts) != 0 {
		t.Errorf("an unread identity should leave no rows, got %+v", facts)
	}
	for _, sec := range m2.diagSections(st2.DiagnosticView(), time.Now()) {
		if sec.title == "device" {
			t.Error("an empty device section should be dropped")
		}
	}
	if eSDK(protocol.DiagnosticSnapshot{}) != "" {
		t.Error("eSDK before a ZeroConf answer should be empty")
	}
}

// sweepDeltaFact compares the live identity with the last sweep's baseline,
// dated to when the oldest compared value was read.
func TestSweepDeltaFact(t *testing.T) {
	base := testBaseline()
	if got := sweepDeltaFact("AR241CP_8747.29.2", "29-1d316f0c-10", "3.216.31-g1", base); got != "unchanged since Sep 12 16:00" {
		t.Errorf("unchanged = %q", got)
	}
	got := sweepDeltaFact("AR241CP_9001.30.1", "30-0abc1234-10", "3.220.4-g2", base)
	if want := "firmware AR241CP_8747.29.2 → AR241CP_9001.30.1 · mcu 29 → 30 · eSDK 3.216.31-g1 → 3.220.4-g2 · since Sep 12 16:00"; got != want {
		t.Errorf("moved = %q, want %q", got, want)
	}
	// the MCU compares VER's version field: the commit and API level are not a move
	if got := sweepDeltaFact("", "29-ffffffff-11", "", base); got != "unchanged since Sep 12 16:00" {
		t.Errorf("same MCU version = %q", got)
	}
	if sweepDeltaFact("AR241CP_8747.29.2", "29", "x", nil) != "" {
		t.Error("no baseline should print nothing")
	}
	if sweepDeltaFact("", "", "", base) != "" {
		t.Error("no live identity yet should print nothing")
	}
	// a baseline that never read a value cannot compare it
	partial := &sweep.Report{At: base.At}
	partial.Tunnel.MCU = "29"
	if got := sweepDeltaFact("AR241CP_9001.30.1", "30", "3.220.4", partial); got != "mcu 29 → 30 · since Sep 12 16:00" {
		t.Errorf("partial baseline = %q", got)
	}
	// a merged baseline: the Sep 14 sweep carried the firmware and the MCU
	// read on Sep 12 and Sep 13, so "unchanged" dates to the oldest, Sep 12
	merged := *testBaseline()
	merged.At = time.Date(2026, 9, 14, 9, 0, 0, 0, time.Local)
	merged.Carried = map[string]time.Time{
		"lssdp":      time.Date(2026, 9, 12, 16, 0, 0, 0, time.Local),
		"tunnel.ver": time.Date(2026, 9, 13, 8, 30, 0, 0, time.Local),
	}
	if got := sweepDeltaFact("AR241CP_8747.29.2", "29", "3.216.31-g1", &merged); got != "unchanged since Sep 12 16:00" {
		t.Errorf("carried identity dated %q, want the oldest read", got)
	}
	// comparing only what this sweep read itself dates it to the sweep
	if got := sweepDeltaFact("", "", "3.216.31-g1", &merged); got != "unchanged since Sep 14 09:00" {
		t.Errorf("fresh-only compare dated %q, want the sweep's own date", got)
	}
	if got := sweepDeltaFact("", "29", "", &merged); got != "unchanged since Sep 13 08:30" {
		t.Errorf("carried MCU dated %q", got)
	}
}

// otaFact: nothing until u, "checking…" while asked, then the verdict aged.
func TestOTAFact(t *testing.T) {
	now := time.Now()
	at := now.Add(-12 * time.Second)
	cases := []struct {
		d    protocol.DiagnosticSnapshot
		want string
	}{
		{protocol.DiagnosticSnapshot{}, ""},
		{protocol.DiagnosticSnapshot{OTAPending: true}, "checking…"},
		{protocol.DiagnosticSnapshot{OTA: &protocol.OTAInfo{At: at, UpToDate: true}}, "up to date · checked 12s ago"},
		{protocol.DiagnosticSnapshot{OTA: &protocol.OTAInfo{At: at, Offered: "AR241CP_9001"}}, "AR241CP_9001 available · checked 12s ago"},
		{protocol.DiagnosticSnapshot{OTA: &protocol.OTAInfo{At: at}}, "update available · checked 12s ago"},
		{protocol.DiagnosticSnapshot{OTA: &protocol.OTAInfo{At: at, Err: "vendor unreachable", UpToDate: true}}, "check failed · vendor unreachable · checked 12s ago"},
		// a verdict in hand while a new check runs: the verdict stays until the new one lands
		{protocol.DiagnosticSnapshot{OTAPending: true, OTA: &protocol.OTAInfo{At: at, UpToDate: true}}, "up to date · checked 12s ago"},
	}
	for _, c := range cases {
		if got := otaFact(c.d, now); got != c.want {
			t.Errorf("otaFact(%+v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// The connection section: the tunnel, the configured host, and the two
// tunnel-free probes once they have run.
func TestConnectionRows(t *testing.T) {
	m, st, _ := makeModel(t)
	now := time.Now()
	labels := func() []string {
		var out []string
		for _, f := range m.connectionRows(st.DiagnosticView(), now) {
			out = append(out, f.k)
		}
		return out
	}
	if got := labels(); !slices.Equal(got, []string{"tunnel", "host"}) {
		t.Errorf("before the probes = %v", got)
	}
	st.SetLSSDP(nil)
	st.SetSpotifyZC(nil, 0)
	if got := labels(); !slices.Equal(got, []string{"tunnel", "host", "lssdp", "spotify"}) {
		t.Errorf("after the probes = %v", got)
	}
	if rows := m.connectionRows(st.DiagnosticView(), now); stripANSI(rows[1].v) != testHost {
		t.Errorf("host row = %q", stripANSI(rows[1].v))
	}
}

// diagRow: a dim label in its fixed column, a plain value in the body pen, a
// styled value kept as it came, and a value too long for the row clipped.
func TestDiagRow(t *testing.T) {
	m, _, _ := makeModel(t)
	ps := m.sty.pens()
	if got := m.diagRow(kv{"host", "x"}, 40); got != ps.dim.render("host")+labelGap("host", diagLabelW)+ps.txt.render("x") {
		t.Errorf("plain row = %q", got)
	}
	styled := ps.warn.render("down")
	if got := m.diagRow(kv{"tunnel", styled}, 40); !strings.HasSuffix(got, styled) {
		t.Errorf("styled row lost its pen: %q", got)
	}
	long := m.diagRow(kv{"radio", strings.Repeat("x", 80)}, 30)
	if visWidth(long) != 30 || !strings.HasSuffix(stripANSI(long), GL["ell"]) {
		t.Errorf("long row = %q (%d)", stripANSI(long), visWidth(long))
	}
	// the hardware rows fit the stacked column at the narrowest frame, clipped
	for _, f := range confHardware {
		assertWithin(t, f.k, []string{m.diagRow(f, MiniCols-6-2)}, MiniCols-6-2)
	}
}

// splitSections balances the two columns at a section boundary.
func TestSplitSections(t *testing.T) {
	sec := func(title string, n int) diagSection { return diagSection{title, make([]kv, n)} }
	if h := sectionsHeight([]diagSection{sec("a", 3), sec("b", 1)}); h != 4+1+2 {
		t.Errorf("sectionsHeight = %d, want 7", h)
	}
	if sectionsHeight(nil) != 0 {
		t.Error("no sections, no height")
	}
	for _, c := range []struct {
		secs []diagSection
		want int
	}{
		{[]diagSection{sec("a", 3), sec("b", 3)}, 1},
		{[]diagSection{sec("a", 1), sec("b", 1), sec("c", 6)}, 2},
		{[]diagSection{sec("a", 8), sec("b", 1), sec("c", 1)}, 1},
		{nil, 0},
	} {
		if got := splitSections(c.secs); got != c.want {
			t.Errorf("splitSections(%d sections) = %d, want %d", len(c.secs), got, c.want)
		}
	}
}

// diagWindow cuts the read-out to the room and says what is off-screen, in
// the singular where it is one row; the offset clamps at both ends.
func TestDiagWindow(t *testing.T) {
	m, _, _ := makeModel(t)
	rows := make([]string, 10)
	for i := range rows {
		rows[i] = fmt.Sprint(i)
	}
	cases := []struct {
		scroll, room int
		first        string
		hint         string
		wantScroll   int
	}{
		{0, 4, "0", "k · 6 more rows below", 0},
		{5, 4, "5", "k · 5 above · 1 below", 5},
		{99, 4, "6", "k · 6 rows above", 6},
		{0, 9, "0", "k · 1 more row below", 0},
		{1, 9, "1", "k · 1 row above", 1},
		{3, 10, "0", "", 0},
		{3, 20, "0", "", 0},
	}
	for _, c := range cases {
		m.diagScroll = c.scroll
		got, hint := m.diagWindow(rows, c.room, "k")
		if len(got) != min(c.room, len(rows)) || got[0] != c.first || hint != c.hint || m.diagScroll != c.wantScroll {
			t.Errorf("scroll %d room %d: rows from %q (%d), hint %q, scroll %d; want from %q, %q, %d",
				c.scroll, c.room, got[0], len(got), hint, m.diagScroll, c.first, c.hint, c.wantScroll)
		}
	}
	if got, hint := m.diagWindow(rows, 0, "k"); got != nil || hint != "" {
		t.Errorf("no room = %v %q", got, hint)
	}
	m.diagScroll = 0
	m.diagScrollBy(-3)
	if m.diagScroll != 0 {
		t.Errorf("scrolling above the top = %d", m.diagScroll)
	}
	m.rows = 3
	if m.diagPage() != 1 {
		t.Errorf("a tiny frame's page = %d, want 1", m.diagPage())
	}
}

// When the diagnostics are taller than the frame they scroll: ↑↓ by a row,
// ←→ by a page, clamped at both ends, with the footer saying how much is
// off-screen instead of the colour legend.
func TestDiagScrollsWhenTallerThanTheFrame(t *testing.T) {
	st := fullDiagState()
	m, _, _ := modelWith(st)
	m.baseline = testBaseline()
	m.rows, m.cols = 20, 80 // stacked, ~15 rows of room for ~30 rows of read-out
	m.setView(viewDiag)
	first := clean(render(t, m))
	if !strings.Contains(first, "↑↓ scroll") || !strings.Contains(first, "more rows below") {
		t.Fatalf("a too-short frame should offer to scroll:\n%s", first)
	}
	if !strings.Contains(first, "─ audio") {
		t.Error("the top of the read-out should show first")
	}
	if strings.Contains(first, "● good") {
		t.Error("the legend gives way to the scroll count")
	}
	m.key(ke(kDown))
	m.key(ke(kDown))
	scrolled := clean(render(t, m))
	if scrolled == first || !strings.Contains(scrolled, "2 above") {
		t.Errorf("↓↓ should scroll two rows and say so:\n%s", scrolled)
	}
	if !strings.Contains(scrolled, "diagnostics") {
		t.Error("the masthead stays put while the read-out scrolls")
	}
	m.key(ke(kRight)) // a page
	if m.diagScroll <= 2 {
		t.Error("→ should page down")
	}
	for range 40 {
		m.key(ke(kRight))
	}
	bottom := clean(render(t, m))
	if !strings.Contains(bottom, "rows above") || strings.Contains(bottom, "below") || !strings.Contains(bottom, "soc") {
		t.Errorf("over-scrolling should clamp at the bottom:\n%s", bottom)
	}
	for range 40 {
		m.key(ke(kLeft))
	}
	// (the frames carry ages that tick, so the top is compared by its rows)
	top := clean(render(t, m))
	if m.diagScroll != 0 || !strings.Contains(top, "─ audio") || strings.Contains(top, "above") {
		t.Errorf("← past the top should clamp at the first row:\n%s", top)
	}
	// tall enough: nothing to scroll, the legend is back
	m.rows = 60
	tall := clean(render(t, m))
	if strings.Contains(tall, "↑↓ scroll") || !strings.Contains(tall, "● good   ● warn   ● fault") {
		t.Errorf("a tall frame should show the legend, not a scroll hint:\n%s", tall)
	}
	// a narrow footer keeps the count and lets the keys go
	m.rows, m.cols = 20, MiniCols
	if foot := footerOf(clean(render(t, m))); !strings.Contains(foot, "more rows below") || strings.Contains(foot, "u asks the vendor about updates") {
		t.Errorf("narrow footer = %q", foot)
	}
}

// The error line shows age-stamped for diagErrWindow above the footer, then
// leaves; the read-out gives it the two rows it takes.
func TestDiagErrorLineWindow(t *testing.T) {
	st := fullDiagState()
	st.Note("command not delivered")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 60, 120
	m.view = viewDiag
	d := st.DiagnosticView()
	errAt := d.Snapshot.ErrorAt
	for _, W := range []int{114, 74} {
		m.cols = W + 6
		within := m.renderDiagnostic(d, errAt.Add(3*time.Second), W)
		assertWithin(t, "diag with an error", within, W)
		if len(within) != m.bodyRows() {
			t.Errorf("W=%d: %d body rows, want %d", W, len(within), m.bodyRows())
		}
		tail := stripANSI(within[len(within)-3])
		if tail != GL["warn"]+" command not delivered · 3.0s ago" {
			t.Errorf("W=%d: the row above the blank and the footer = %q", W, tail)
		}
		after := m.renderDiagnostic(d, errAt.Add(diagErrWindow+time.Second), W)
		if flat := stripANSI(strings.Join(after, "\n")); strings.Contains(flat, "command not delivered") {
			t.Errorf("W=%d: the error outlived diagErrWindow", W)
		}
	}
}

// footerFit lays a hint beside the view's fact: the widest hint that fits,
// none when no hint does, and only a fact wider than the row is clipped.
func TestFooterFit(t *testing.T) {
	m, _, _ := makeModel(t)
	fact := m.sty.pens().dim.render("FACT")
	for _, W := range []int{80, 60, 40, 30, 12, 10, 6, 4, 3} {
		got := m.footerFit(diagFooters, fact, 4, W)
		if visWidth(got) != W {
			t.Errorf("W=%d: footer %d wide", W, visWidth(got))
		}
		plain := stripANSI(got)
		if W > 4 && !strings.HasSuffix(plain, "FACT") {
			t.Errorf("W=%d: the fact lost its place: %q", W, plain)
		}
		for _, h := range diagFooters {
			if DispW(h)+1+4 <= W {
				if !strings.HasPrefix(plain, h+" ") {
					t.Errorf("W=%d: footer %q, want the hint %q", W, plain, h)
				}
				break
			}
		}
	}
	// no hint fits beside the fact: the fact alone, right-aligned
	if got := stripANSI(m.footerFit(diagFooters, fact, 4, 9)); got != "     FACT" {
		t.Errorf("no hint fits: %q", got)
	}
	// a fact wider than the row is clipped, never wrapped
	if got := m.footerFit(diagFooters, strings.Repeat("x", 90), 90, 74); visWidth(got) != 74 {
		t.Errorf("an over-wide fact is %d wide at 74", visWidth(got))
	}
}

func TestCov_clipStyled(t *testing.T) {
	if got := clipStyled("abc", 10); got != "abc" {
		t.Errorf("clipStyled fits = %q", got)
	}
	if got := clipStyled("abcdefgh", 4); stripANSI(got) != "abc"+GL["ell"] {
		t.Errorf("clipStyled overflow = %q", stripANSI(got))
	}
	if got := clipStyled("abcdefgh", 0); got != "" {
		t.Errorf("clipStyled w=0 = %q, want empty", got)
	}
	if got := clipStyled("abcdefgh", 1); stripANSI(got) != "a" {
		t.Errorf("clipStyled no-ellipsis-room = %q, want hard cut", stripANSI(got))
	}
}

// LP10_OTA_URL="" switches the vendor check off (its worker never starts),
// but u still raised the request and the vendor line said "checking…" for the
// rest of the run.
func TestUpdateCheckSwitchedOffDoesNotHang(t *testing.T) {
	t.Setenv("LP10_OTA_URL", "")
	m, st, _ := makeModel(t)
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2", State: "S", NetMode: "ETH0"})
	m.rows, m.cols = 60, 160
	m.key(kr('3'))
	m.key(kr('u'))
	if out := clean(render(t, m)); strings.Contains(out, "checking…") {
		t.Error("with the check switched off, u leaves the vendor line on \"checking…\" forever")
	}
	if !strings.Contains(m.notice, "update check is off") {
		t.Errorf("notice = %q, want the check named off", m.notice)
	}
	// switched on, u raises the request (no OTA worker runs here, so nothing
	// leaves the test)
	t.Setenv("LP10_OTA_URL", "http://127.0.0.1:9/")
	m.key(kr('U'))
	if !st.DiagnosticView().OTAPending {
		t.Error("with the check on, U should raise the request")
	}
	if m.view != viewDiag {
		t.Error("u must not leave the diagnostics")
	}
}

// The diagnostics show the friendly reason, not the raw dial error — the
// masthead already says "disconnected" and the tunnel row "down".
func TestDiagErrorIsFriendly(t *testing.T) {
	st := protocol.NewState()
	st.StartConnection()
	st.Note("cannot reach :2018: dial tcp: lookup lp10.local: no such host")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 32, 100
	m.view = viewDiag
	view := clean(render(t, m))
	if strings.Contains(view, "lookup lp10.local") {
		t.Error("the diagnostics must not show the raw dial error")
	}
	if !strings.Contains(view, "can't find the device") {
		t.Error("the diagnostics should show the friendly reason")
	}
}

// A clipped styled row keeps its per-segment colours — it used to be stripped
// and re-rendered uniformly dim, so the rows "lost their colours" the moment a
// larger font cost the column a couple of cells.
func TestClipStyledKeepsColours(t *testing.T) {
	sty := newTheme()
	row := sty.sAcc.Render("●") + " " + sty.sTxt.Render("Spotify and more text")
	got := clipStyled(row, 12)
	if w := lipgloss.Width(got); w > 12 {
		t.Errorf("clipped width = %d, want ≤ 12", w)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("clipped row lost its styling: %q", got)
	}
	if !strings.HasSuffix(stripANSI(got), GL["ell"]) {
		t.Errorf("clipped row should end with the ellipsis, got %q", stripANSI(got))
	}
	if !strings.HasPrefix(got, sty.pens().acc.render("●")) {
		t.Errorf("the first segment should keep its accent: %q", got)
	}
}
