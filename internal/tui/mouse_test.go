package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// ---- synthetic mouse events --------------------------------------------------

func mPress(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}
func mDrag(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft}
}
func mWheel(x, y int, up bool) tea.MouseMsg {
	b := tea.MouseButtonWheelDown
	if up {
		b = tea.MouseButtonWheelUp
	}
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: b}
}

func center(r rect) (int, int) { return r.x + r.w/2, r.y + r.h/2 }

func btnZoneFor(m *model, action string) (btnZone, bool) {
	for _, z := range m.mzBtns {
		if z.action == action {
			return z, true
		}
	}
	return btnZone{}, false
}

func eqZoneFor(m *model, code string) (eqZone, bool) {
	for _, z := range m.mzEQ {
		if z.code == code {
			return z, true
		}
	}
	return eqZone{}, false
}

// render sizes the model and paints once, populating the hit-zones.
func render(m *model, rows, cols int) { m.rows, m.cols = rows, cols; m.View() }

// ---- transport: clicking the buttons fires the action ------------------------

func TestMouseTransportFull(t *testing.T) {
	cases := []struct{ action, mid, data string }{
		{"toggle", "40", "PAUSE"}, // the fixture is playing, so a toggle pauses
		{"next", "40", "NEXT"},
		{"prev", "40", "PREV"},
	}
	for _, tc := range cases {
		m, _, collect := makeModel(t)
		render(m, 40, 120)
		z, ok := btnZoneFor(m, tc.action)
		if !ok {
			t.Fatalf("%s: no transport zone recorded", tc.action)
		}
		m.Update(mPress(center(z.rect)))
		got := collect()
		if len(got) == 0 {
			t.Fatalf("%s: no command sent", tc.action)
		}
		c := last(got)
		if c.Mid != 40 || c.Data != tc.data {
			t.Errorf("%s: sent mid=%d data=%q, want 40 %q", tc.action, c.Mid, c.Data, tc.data)
		}
	}
	// clicking a button also moves keyboard focus to it
	m, _, _ := makeModel(t)
	render(m, 40, 120)
	z, _ := btnZoneFor(m, "next")
	m.Update(mPress(center(z.rect)))
	if m.pane != paneNow || m.focus != 2 {
		t.Errorf("after clicking next: pane=%d focus=%d, want %d 2", m.pane, m.focus, paneNow)
	}
}

func TestMouseTransportCompact(t *testing.T) {
	for _, action := range []string{"toggle", "next", "prev"} {
		m, _, collect := makeModel(t)
		render(m, 20, 80) // below FullRows -> compact dashboard
		z, ok := btnZoneFor(m, action)
		if !ok {
			t.Fatalf("%s: no zone in compact layout", action)
		}
		m.Update(mPress(center(z.rect)))
		if got := collect(); len(got) == 0 || last(got).Mid != 40 {
			t.Errorf("%s: want a mid-40 transport command, got %v", action, got)
		}
	}
	// the compact layout also exposes a mute button
	m, st, collect := makeModel(t)
	st.SetVol(40) // ensure a non-zero level so the mute click silences (-> 0)
	render(m, 20, 80)
	z, ok := btnZoneFor(m, "mute")
	if !ok {
		t.Fatal("no mute zone in compact layout")
	}
	m.Update(mPress(center(z.rect)))
	if got := collect(); len(got) == 0 || last(got).Mid != 64 || last(got).Data != "0" {
		t.Errorf("mute click: want mid-64 data-0 (mute), got %v", got)
	}
}

// TestMouseZonesAlignWithRender is the geometry guard: it proves a recorded zone
// actually overlaps the glyphs the renderer drew there, so the hit math can't
// silently drift from the layout. lipgloss.Width on the cleaned prefix yields the
// true display column even past wide/zero-width runes.
func TestMouseZonesAlignWithRender(t *testing.T) {
	colOf := func(line, sub string) int {
		before, _, ok := strings.Cut(line, sub)
		if !ok {
			return -1
		}
		return lipgloss.Width(before)
	}
	for _, sz := range [][2]int{{40, 120}, {20, 80}} {
		m, _, _ := makeModel(t) // fixture is playing -> the toggle reads "pause"
		view := m.View2(sz[0], sz[1])
		lines := strings.Split(clean(view), "\n")
		z, ok := btnZoneFor(m, "toggle")
		if !ok {
			t.Fatalf("%dx%d: no toggle zone", sz[0], sz[1])
		}
		if z.y < 0 || z.y >= len(lines) {
			t.Fatalf("%dx%d: toggle row %d out of range", sz[0], sz[1], z.y)
		}
		col := colOf(lines[z.y], "pause")
		if col < 0 {
			t.Fatalf("%dx%d: no \"pause\" label on the toggle row %q", sz[0], sz[1], lines[z.y])
		}
		if col < z.x || col >= z.x+z.w {
			t.Errorf("%dx%d: pause at col %d, outside toggle zone [%d,%d)", sz[0], sz[1], col, z.x, z.x+z.w)
		}
	}
}

// TestMouseEQZonesAlign proves the EQ band zones overlap the band the renderer
// drew: the BAS column's label row carries "Bass" within its recorded x-range.
func TestMouseEQZonesAlign(t *testing.T) {
	m, _, _ := makeModel(t)
	view := m.View2(40, 120)
	lines := strings.Split(clean(view), "\n")
	z, ok := eqZoneFor(m, "BAS")
	if !ok {
		t.Fatal("no BAS band zone")
	}
	if z.y < 0 || z.y >= len(lines) {
		t.Fatalf("BAS label row %d out of range", z.y)
	}
	i := strings.Index(lines[z.y], "Bass") // the band label sits on the band's first row
	if i < 0 {
		t.Fatalf("no \"Bass\" label on row %d: %q", z.y, lines[z.y])
	}
	if col := lipgloss.Width(lines[z.y][:i]); col < z.x || col >= z.x+z.w {
		t.Errorf("Bass label at col %d, outside BAS zone [%d,%d)", col, z.x, z.x+z.w)
	}
}

// ---- volume ------------------------------------------------------------------

func TestMouseVolumeRailFull(t *testing.T) {
	m, _, collect := makeModel(t)
	render(m, 40, 120)
	if !m.mzVol.vertical || m.mzVol.h == 0 {
		t.Fatal("no vertical volume rail recorded in full layout")
	}
	// top of the rail = full volume
	m.Update(mPress(m.mzVol.x+m.mzVol.w/2, m.mzVol.y))
	if got := collect(); len(got) == 0 || last(got).Mid != 64 || last(got).Data != "100" {
		t.Errorf("click rail top: want mid-64 data-100, got %v", got)
	}
	// bottom of the rail = silence
	m.Update(mPress(m.mzVol.x+m.mzVol.w/2, m.mzVol.y+m.mzVol.h-1))
	if got := collect(); len(got) == 0 || last(got).Data != "0" {
		t.Errorf("click rail bottom: want data-0, got %v", got)
	}
	// dragging is honoured the same as a press
	m.Update(mDrag(m.mzVol.x+m.mzVol.w/2, m.mzVol.y))
	if got := collect(); len(got) == 0 || last(got).Data != "100" {
		t.Errorf("drag rail top: want data-100, got %v", got)
	}
}

func TestMouseWheelVolume(t *testing.T) {
	m, st, collect := makeModel(t)
	st.SetVol(50)
	collect() // drain the SetVol-less seed (SetVol doesn't enqueue)
	render(m, 40, 120)
	// wheel somewhere away from any control (the header row) adjusts volume
	m.Update(mWheel(10, bodyY0, true))
	got := collect()
	if len(got) == 0 || last(got).Mid != 64 {
		t.Fatalf("wheel up: want a mid-64 volume command, got %v", got)
	}
	if last(got).Data != "52" { // VolStep is 2 in defaultCfg
		t.Errorf("wheel up from 50: data=%q, want 52", last(got).Data)
	}
	m.Update(mWheel(10, bodyY0, false))
	if got := collect(); len(got) == 0 || last(got).Data != "50" {
		t.Errorf("wheel down: data=%q, want 50", lastData(got))
	}
}

// ---- equalizer ---------------------------------------------------------------

func TestMouseWheelEQBand(t *testing.T) {
	m, st, _ := makeModel(t)
	st.PreloadEQ(map[string]int{"BAS": 0})
	render(m, 40, 120)
	z, ok := eqZoneFor(m, "BAS")
	if !ok {
		t.Fatal("no BAS band zone recorded")
	}
	m.Update(mWheel(z.x+z.w/2, z.bar.y+z.bar.h/2, true))
	if v, _ := st.EQValue("BAS"); v != 1 { // BAS step is 1
		t.Errorf("wheel up over BAS: value=%d, want 1", v)
	}
	if m.pane != paneEQ || m.eqFocus != z.d {
		t.Errorf("wheel over BAS: pane=%d eqFocus=%d, want %d %d", m.pane, m.eqFocus, paneEQ, z.d)
	}
}

func TestMouseClickEQToggle(t *testing.T) {
	m, st, _ := makeModel(t)
	st.PreloadEQ(map[string]int{"EQS": 0})
	render(m, 40, 120)
	z, ok := eqZoneFor(m, "EQS")
	if !ok || !z.toggle {
		t.Fatal("no EQS toggle band zone recorded")
	}
	m.Update(mPress(center(z.rect)))
	if v, _ := st.EQValue("EQS"); v != 1 {
		t.Errorf("click EQS toggle: value=%d, want 1 (on)", v)
	}
	if m.pane != paneEQ || m.eqFocus != z.d {
		t.Errorf("click EQS: pane=%d eqFocus=%d, want %d %d", m.pane, m.eqFocus, paneEQ, z.d)
	}
}

func TestMouseClickEQRangedSetsByPosition(t *testing.T) {
	m, st, _ := makeModel(t)
	st.PreloadEQ(map[string]int{"BAS": 0})
	render(m, 40, 120)
	z, ok := eqZoneFor(m, "BAS") // ranged -10..+10
	if !ok {
		t.Fatal("no BAS band zone recorded")
	}
	// right end of the horizontal slider -> maximum value (+10)
	m.Update(mPress(z.bar.x+z.bar.w-1, z.bar.y))
	if v, _ := st.EQValue("BAS"); v != 10 {
		t.Errorf("click BAS bar right end: value=%d, want 10 (max)", v)
	}
	// left end -> minimum value (-10)
	m.Update(mPress(z.bar.x, z.bar.y))
	if v, _ := st.EQValue("BAS"); v != -10 {
		t.Errorf("click BAS bar left end: value=%d, want -10 (min)", v)
	}
}

// ---- diagnostics overlay -----------------------------------------------------

func TestMouseClickClosesDiag(t *testing.T) {
	m, _, _ := makeModel(t)
	render(m, 40, 120)
	m.diag = true
	m.Update(mPress(10, 10))
	if m.diag {
		t.Error("a left click should dismiss the diagnostics overlay")
	}
}

// ---- helpers ----------------------------------------------------------------

// View2 renders at a given size and returns the view, leaving zones populated.
func (m *model) View2(rows, cols int) string { m.rows, m.cols = rows, cols; return m.View() }

func lastData(c []protocol.Command) string {
	if len(c) == 0 {
		return ""
	}
	return c[len(c)-1].Data
}
