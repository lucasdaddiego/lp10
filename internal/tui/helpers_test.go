package tui

import (
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// Test-only entry points into the diagnostics renderers: they take the player
// snapshot explicitly (the production path reads DiagnosticView itself), which
// is what the focused rendering tests drive.

func (m *model) renderDiag(s protocol.Snapshot, now time.Time, W int) []string {
	d := m.st.DiagnosticView(now)
	d.Snapshot = s
	return m.renderDiagnostic(d, now, W)
}

func (m *model) renderDiagCards(s protocol.Snapshot, now time.Time, W int) []string {
	d := m.st.DiagnosticView(now)
	d.Snapshot = s
	return m.renderDiagCardsSnapshot(d, now, W)
}

func (m *model) serviceStrip(w int) []string {
	return m.serviceStripFor(m.st.ConfView(), w)
}
