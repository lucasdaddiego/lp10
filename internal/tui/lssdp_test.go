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
