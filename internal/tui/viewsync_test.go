package tui

import (
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// Leaving the player tells the loop so (94 0), re-asserted every reassert
// window because a reconnected loop starts visible; coming back sends 94 1
// once. The probe workers are told nobody is looking unless the services or
// the diagnostics show.
func TestSyncViewsTellsTheLoopAndTheProbes(t *testing.T) {
	m, st, collect := makeModel(t)
	m.rows, m.cols = 40, 120
	m.dispatch(logicMsg{})
	if got := collect(); len(got) != 0 {
		t.Fatalf("on the player nothing should be sent, got %+v", got)
	}
	if st.ProbeWanted() {
		t.Error("the player view shows no probe answer: the probes should be quiet")
	}
	only94 := func(cs []protocol.Command) []protocol.Command {
		var out []protocol.Command
		for _, c := range cs {
			if c.Mid == 94 {
				out = append(out, c)
			}
		}
		return out
	}
	m.setView(viewLogs)
	m.dispatch(logicMsg{})
	got := only94(collect())
	if len(got) != 1 || got[0].Data != "0" {
		t.Fatalf("leaving the player should send 94 0, got %+v", got)
	}
	if st.ProbeWanted() {
		t.Error("the logs view shows no probe answer: the probes should be quiet")
	}
	for range StatsReassertTicks - 1 {
		m.dispatch(logicMsg{})
	}
	if got := only94(collect()); len(got) != 0 {
		t.Errorf("hidden should not be re-sent before the reassert window: %+v", got)
	}
	m.dispatch(logicMsg{})
	if got := only94(collect()); len(got) != 1 || got[0].Data != "0" {
		t.Errorf("hidden should be re-asserted after the window, got %+v", got)
	}
	m.setView(viewDiag)
	m.dispatch(logicMsg{})
	collect()
	if !st.ProbeWanted() {
		t.Error("the diagnostics show the probes: they should run")
	}
	m.setView(viewPlayer)
	m.dispatch(logicMsg{})
	if got := only94(collect()); len(got) != 1 || got[0].Data != "1" {
		t.Errorf("returning to the player should send 94 1 once, got %+v", got)
	}
	m.dispatch(logicMsg{})
	if got := only94(collect()); len(got) != 0 {
		t.Errorf("visible is not re-asserted: %+v", got)
	}
}
