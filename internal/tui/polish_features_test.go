package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The notice line under the header carries transient events and fades: a
// volume step names the level, the mute says so both ways, the sleep timer
// reports its state.
func TestNoticeLineEvents(t *testing.T) {
	m, _, _ := makeModel(t)
	m.rows, m.cols = 40, 120
	line := func() string { return stripANSI(strings.Split(render(t, m), "\n")[2]) }
	if strings.TrimSpace(strings.Trim(line(), "┃")) != "" {
		t.Errorf("notice line should start blank, got %q", line())
	}
	for _, step := range []struct {
		ev   keyEvent
		want string
	}{
		{ke(kUp), "volume 46%"},
		{kr('m'), "muted"},
		{kr('m'), "unmuted"},
		{kr('s'), "sleep timer set · " + GL["sleep"] + " 15m"},
		{kr('S'), "sleep timer cancelled"},
	} {
		m.key(step.ev)
		if got := strings.TrimSpace(strings.Trim(line(), "┃")); got != step.want {
			t.Errorf("notice = %q, want %q", got, step.want)
		}
	}
	// it fades
	m.noticeUntil = time.Now().Add(-time.Second)
	if strings.TrimSpace(strings.Trim(line(), "┃")) != "" {
		t.Errorf("notice should have faded, got %q", line())
	}
	// a long notice is clipped to the row, and the warning pen differs from the plain one
	m.notifyWarn(strings.Repeat("w", 200), time.Minute)
	warn := m.noticeRow(time.Now(), 50)
	if visWidth(warn) != 50 || !strings.HasSuffix(stripANSI(warn), GL["ell"]) {
		t.Errorf("long notice = %q", stripANSI(warn))
	}
	m.notify(strings.Repeat("w", 200), time.Minute)
	if plain := m.noticeRow(time.Now(), 50); plain == warn {
		t.Error("a warning notice should not render in the plain pen")
	}
}

// startupSummary names whatever of the identity is known: the firmware from
// the LSSDP answer, the MCU build's version field, the eSDK's release.
func TestStartupSummary(t *testing.T) {
	all := protocol.DiagnosticSnapshot{
		LSSDP:     &protocol.LSSDPInfo{FW: "AR241CP_8747.29.2"},
		MCU:       "29-1d316f0c-10",
		SpotifyZC: &protocol.SpotifyZC{LibraryVersion: "3.216.31-gdeadbeef"},
	}
	if got, want := startupSummary(all), "connected · firmware AR241CP_8747.29.2 · MCU 29 · Spotify eSDK 3.216.31"; got != want {
		t.Errorf("all parts = %q, want %q", got, want)
	}
	partial := protocol.DiagnosticSnapshot{
		LSSDP:     &protocol.LSSDPInfo{State: "S"}, // answered, but named no build
		MCU:       "29",
		SpotifyZC: &protocol.SpotifyZC{StatusString: "OK"}, // no library version
	}
	if got := startupSummary(partial); got != "connected · MCU 29" {
		t.Errorf("partial = %q, want connected · MCU 29", got)
	}
	if got := startupSummary(protocol.DiagnosticSnapshot{}); got != "connected" {
		t.Errorf("none = %q, want connected", got)
	}
}

// A connect prints the summary once the MCU build (VER) and the ZeroConf
// answer are in, or after startupSummaryWait with what it has; a lost
// connection warns; a reconnect prints the summary again.
func TestStartupSummaryAndConnectionNotices(t *testing.T) {
	now := time.Now()
	st := protocol.NewState()
	m, _, _ := modelWith(st)
	// never connected: no lost-connection warning, the header says connecting
	m.trackConnection(st.Snap(), now)
	if m.notice != "" {
		t.Fatalf("notice before any connection = %q", m.notice)
	}
	connect(st)
	st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2"})
	m.trackConnection(st.Snap(), now)
	if m.notice != "" {
		t.Errorf("summary printed before the MCU and ZeroConf facts: %q", m.notice)
	}
	st.ApplyVersion("29-1d316f0c-10")
	m.trackConnection(st.Snap(), now.Add(time.Second))
	if m.notice != "" {
		t.Errorf("summary printed with the ZeroConf answer still out: %q", m.notice)
	}
	st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK", LibraryVersion: "3.216.31-g1"}, 9095)
	m.trackConnection(st.Snap(), now.Add(2*time.Second))
	if want := "connected · firmware AR241CP_8747.29.2 · MCU 29 · Spotify eSDK 3.216.31"; m.notice != want {
		t.Errorf("summary = %q, want %q", m.notice, want)
	}
	if got := stripANSI(m.noticeRow(time.Now(), 120)); got != m.notice {
		t.Errorf("notice row = %q", got)
	}
	// printed once: a later tick leaves a newer notice alone
	m.notify("volume 46%", noticeFor)
	m.trackConnection(st.Snap(), now.Add(3*time.Second))
	if m.notice != "volume 46%" {
		t.Errorf("the summary printed twice: %q", m.notice)
	}

	// a disconnect after a connection warns
	st.Disconnect()
	m.trackConnection(st.Snap(), now.Add(4*time.Second))
	if m.notice != "connection lost · reconnecting…" || !m.noticeWarn {
		t.Errorf("disconnect notice = %q (warn=%v)", m.notice, m.noticeWarn)
	}
	// the reconnect: the facts are already in hand, so the summary is at once
	connect(st)
	m.trackConnection(st.Snap(), now.Add(5*time.Second))
	if !strings.HasPrefix(m.notice, "connected · ") || m.noticeWarn {
		t.Errorf("reconnect notice = %q (warn=%v)", m.notice, m.noticeWarn)
	}

	// a fresh connect with no facts yet waits, then prints what it has
	st2 := idleState()
	m2, _, _ := modelWith(st2)
	m2.trackConnection(st2.Snap(), now)
	if m2.notice != "" {
		t.Errorf("summary printed before the facts arrived: %q", m2.notice)
	}
	m2.trackConnection(st2.Snap(), now.Add(startupSummaryWait-time.Millisecond))
	if m2.notice != "" {
		t.Errorf("summary printed before the wait ran out: %q", m2.notice)
	}
	m2.trackConnection(st2.Snap(), now.Add(startupSummaryWait))
	if m2.notice != "connected" {
		t.Errorf("late summary = %q, want just 'connected'", m2.notice)
	}
	// the summary is due only while connected: a drop inside the wait cancels it
	st3 := idleState()
	m3, _, _ := modelWith(st3)
	m3.trackConnection(st3.Snap(), now)
	st3.Disconnect()
	m3.trackConnection(st3.Snap(), now.Add(time.Second))
	if !m3.summaryDue.IsZero() {
		t.Error("a drop inside the wait should cancel the pending summary")
	}
}

// The logic tick drives the same notices end to end.
func TestLogicTickPrintsTheSummary(t *testing.T) {
	st := idleState()
	st.ApplyVersion("29-1d316f0c-10")
	st.SetSpotifyZC(&protocol.SpotifyZC{LibraryVersion: "3.216.31"}, 9095)
	m, _, _ := modelWith(st)
	m.dispatch(logicMsg{})
	if m.notice != "connected · MCU 29 · Spotify eSDK 3.216.31" {
		t.Errorf("first tick notice = %q", m.notice)
	}
}

// Connected with nothing playing, the full player shows the big clock and
// the sources that always wake the box; the compact player says the same in
// its hint line; the mini line says nothing plays.
func TestIdleScreen(t *testing.T) {
	m, _, _ := modelWith(idleState())
	m.rows, m.cols = 40, 120
	out := clean(render(t, m))
	if !strings.Contains(out, "█████") {
		t.Errorf("idle screen lacks the block clock:\n%s", out)
	}
	for _, want := range []string{"nothing playing", wakeHint} {
		if !strings.Contains(out, want) {
			t.Errorf("idle screen missing %q", want)
		}
	}
	for _, absent := range []string{"Vol", "Playing", "Paused", GL["rew"]} {
		if strings.Contains(out, absent) {
			t.Errorf("idle screen carries %q:\n%s", absent, out)
		}
	}
	// compact: the hint line
	m.rows, m.cols = 20, 80
	if out := clean(render(t, m)); !strings.Contains(out, "nothing playing") || !strings.Contains(out, wakeHint) {
		t.Errorf("compact idle hint missing:\n%s", out)
	}
	// mini
	m.rows = MiniRows - 1
	if out := clean(render(t, m)); out != GL["note"]+" nothing playing" {
		t.Errorf("mini idle = %q", out)
	}
	// a narrow idle frame clips the hint rather than overflowing
	m.rows, m.cols = 30, FullCols
	render(t, m)
}

// bigClock draws HH:MM in the five-row block font: five digits' worth of
// glyphs, two columns apart, the colon one column wide.
func TestBigClock(t *testing.T) {
	rows := bigClock(time.Date(2026, 9, 12, 16, 4, 0, 0, time.UTC))
	if len(rows) != 5 {
		t.Fatalf("bigClock rows = %d, want 5", len(rows))
	}
	want := 4*5 + 1 + 4*2 // four digits, the colon, four gaps
	for i, r := range rows {
		if DispW(r) != want {
			t.Errorf("row %d width %d, want %d", i, DispW(r), want)
		}
	}
	if rows[0] != blockDigits['1'][0]+"  "+blockDigits['6'][0]+"  "+blockDigits[':'][0]+"  "+blockDigits['0'][0]+"  "+blockDigits['4'][0] {
		t.Errorf("first row = %q", rows[0])
	}
	for r, g := range blockDigits {
		for i, ln := range g {
			if w := DispW(g[0]); DispW(ln) != w {
				t.Errorf("glyph %q row %d width %d, want %d", r, i, DispW(ln), w)
			}
		}
	}
}

// The strip has a short form between the full names and the numerals, and the
// footer rotates a second page of rarer keys for four seconds in sixteen.
func TestStripShortFormAndFooterRotation(t *testing.T) {
	m, _, _ := makeModel(t)
	if s, w := m.viewStrip(30); stripANSI(s) != "1 play  2 eq  3 diag" || w != 20 {
		t.Errorf("short strip = %q (%d)", stripANSI(s), w)
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

// Every footer width gets the widest hint that fits, right-aligned and exactly
// W wide; only below the narrowest hint is it clipped.
func TestFooterHintLadderFitsW(t *testing.T) {
	m, _, _ := makeModel(t)
	for _, page := range []int{0, 130} {
		m.scroll = page
		ladder := playerHints
		if page > 0 {
			ladder = playerHintsRare
		}
		narrowest := DispW(ladder[len(ladder)-1])
		for W := 1; W <= 130; W++ {
			got := m.footerRow(W)
			if visWidth(got) != W {
				t.Fatalf("page %d W=%d: footer width %d", page, W, visWidth(got))
			}
			hint := strings.TrimSpace(stripANSI(got))
			if W >= narrowest {
				if strings.Contains(hint, GL["ell"]) {
					t.Errorf("page %d W=%d: a hint that fits was clipped: %q", page, W, hint)
				}
				// the widest that fits
				for _, h := range ladder {
					if DispW(h) <= W {
						if hint != h {
							t.Errorf("page %d W=%d: %q, want %q", page, W, hint, h)
						}
						break
					}
				}
			} else if !strings.HasSuffix(hint, GL["ell"]) && W > 1 {
				t.Errorf("page %d W=%d: a too-narrow footer should be clipped: %q", page, W, hint)
			}
		}
	}
	// the help and diagnostics footers name no removed views or keys
	for _, h := range append(append(append([]string{}, playerHints...), playerHintsRare...), diagFooters...) {
		for _, gone := range []string{"services", "logs", "night", "bedtime", "remaining"} {
			if strings.Contains(h, gone) {
				t.Errorf("footer hint %q names the removed %q", h, gone)
			}
		}
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
	// the light palette renders every view inside the frame too
	m.rows, m.cols = 40, 120
	for _, v := range []view{viewPlayer, viewEQ, viewDiag, viewHelp} {
		m.view = v
		render(t, m)
	}
}
