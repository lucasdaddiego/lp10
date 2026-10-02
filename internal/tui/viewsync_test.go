package tui

import (
	"testing"
)

// The LSSDP and ZeroConf probes are asked nothing the screen will not show:
// every logic tick tells them whether the diagnostics — the one view that
// shows their answers — are up. syncViews sends nothing down the tunnel.
func TestSyncViewsQuietsTheProbesOffTheDiagnostics(t *testing.T) {
	m, st, collect := makeModel(t)
	m.rows, m.cols = 40, 120
	if !st.ProbeWanted() {
		t.Fatal("setup: a State with no TUI probes")
	}
	for _, step := range []struct {
		key    keyEvent
		v      view
		wanted bool
	}{
		{kr('1'), viewPlayer, false},
		{kr('2'), viewEQ, false},
		{kr('3'), viewDiag, true},
		{kr('?'), viewHelp, false},
		{kr('i'), viewDiag, true},
		{ke(kEsc), viewPlayer, false},
	} {
		m.key(step.key)
		m.dispatch(logicMsg{})
		if m.view != step.v {
			t.Fatalf("setup: view %s, want %s", viewNames[m.view], viewNames[step.v])
		}
		if got := st.ProbeWanted(); got != step.wanted {
			t.Errorf("on %s the probes wanted=%v, want %v", viewNames[step.v], got, step.wanted)
		}
	}
	if got := collect(); len(got) != 0 {
		t.Errorf("view changes sent %v down the tunnel", wire(got))
	}
}
