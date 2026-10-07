// The notice line (notice.go): transient events, the startup summary and the
// connection notices.

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
