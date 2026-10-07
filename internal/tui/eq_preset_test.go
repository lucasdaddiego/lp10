package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

var livePresets = []string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"}

func TestPresetRowNamesAndHighlight(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	const w = 70
	eqs := eqSpecIndex("EQS")
	st.SetEQPresets(livePresets)
	st.ApplyTunnel("EQS", 4) // Rock
	_, vals := st.EQView()
	row := m.eqSliderRow(eqs, vals, false, w)
	plain := stripANSI(row)
	if !strings.HasPrefix(plain, "Preset") {
		t.Errorf("row = %q, want the Preset label", plain)
	}
	for _, n := range livePresets {
		if !strings.Contains(plain, n) {
			t.Errorf("row %q lacks preset %q", plain, n)
		}
	}
	if got := DispW(plain); got != w {
		t.Errorf("row width = %d, want %d", got, w)
	}
	// the selected name is lit in the accent; its neighbours are dim
	ps := m.sty.pens()
	if !strings.Contains(row, ps.acc.render("Rock")) || !strings.Contains(row, ps.dmr.render("Jazz")) {
		t.Errorf("Rock should carry the accent, Jazz the dim pen: %q", row)
	}
	// focused, the current one is bold
	if row := m.eqSliderRow(eqs, vals, true, w); !strings.Contains(row, ps.accB.render("Rock")) {
		t.Errorf("focused row should light Rock bold: %q", row)
	}
	// unknown value: every name dim, nothing lit, still full width
	if got := stripANSI(m.eqSliderRow(eqs, map[string]int{}, true, w)); DispW(got) != w || !strings.Contains(got, "Flat") {
		t.Errorf("unknown-value row = %q", got)
	}
	// no PEQ list yet: the index shows as "preset N"; no list and no value: "—"
	m2, _, _ := modelWith(protocol.NewState())
	if got := stripANSI(m2.eqSliderRow(eqs, map[string]int{"EQS": 2}, false, w)); !strings.Contains(got, "preset 2") {
		t.Errorf("nameless row = %q, want 'preset 2'", got)
	}
	if got := stripANSI(m2.eqSliderRow(eqs, map[string]int{}, false, w)); !strings.Contains(got, "—") || DispW(got) != w {
		t.Errorf("empty row = %q", got)
	}
	// a narrow row clips the list instead of overflowing
	for _, nw := range []int{24, 20, sliderLabelW + sliderValW + 1} {
		if got := stripANSI(m.eqSliderRow(eqs, vals, false, nw)); DispW(got) != nw {
			t.Errorf("narrow row width = %d, want %d: %q", DispW(got), nw, got)
		}
	}
}

func TestPresetKeysStepAndWrap(t *testing.T) {
	m, st, collect := eqModel(t)
	st.SetEQPresets(livePresets)
	m.key(kr('e'))
	m.eqFocus = 1 // Preset
	if m.eqSpec().Code != "EQS" {
		t.Fatalf("slot 1 is %s, want EQS", m.eqSpec().Code)
	}
	m.key(ke(kRight))                      // 0 -> 1
	st.PreloadEQ(map[string]int{"EQS": 5}) // last named (PreloadEQ sidesteps the echo hold)
	m.key(ke(kRight))                      // stays at the last named preset
	m.key(ke(kEnter))                      // enter steps to the next, wrapping to 0
	m.key(ke(kLeft))                       // 0 -> clamp 0
	if got := wire(collect()); !slices.Equal(got, []string{"EQS:1", "EQS:5", "EQS:0", "EQS:0"}) {
		t.Errorf("preset keys sent %v, want [EQS:1 EQS:5 EQS:0 EQS:0]", got)
	}
	// before the PEQ list arrives the spec bound applies, not the list length
	st2 := protocol.NewState()
	connect(st2)
	st2.ApplyTunnel("EQS", 7)
	m2, _, collect2 := modelWith(st2)
	m2.key(kr('e'))
	m2.eqFocus = 1
	m2.key(ke(kRight))
	m2.key(ke(kEnter))
	if got := wire(collect2()); !slices.Equal(got, []string{"EQS:8", "EQS:9"}) {
		t.Errorf("no list: sent %v, want [EQS:8 EQS:9]", got)
	}
	// a readback past the list (another client set it) steps back to 0
	st.PreloadEQ(map[string]int{"EQS": 9})
	m.key(ke(kEnter))
	if got := wire(collect()); !slices.Equal(got, []string{"EQS:0"}) {
		t.Errorf("enter past the list sent %v, want [EQS:0]", got)
	}
}

func TestBalanceRowAndSummary(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	bal := eqSpecIndex("BAL")
	for v, want := range map[int]string{-20: "L20", 0: "0", 35: "R35", -100: "L100"} {
		if got := stripANSI(m.eqSliderRow(bal, map[string]int{"BAL": v}, true, 60)); !strings.HasSuffix(got, want) || !strings.HasPrefix(got, "Balance") {
			t.Errorf("BAL %d row = %q, want %q", v, got, want)
		}
	}
	st.PreloadEQ(map[string]int{"EQE": 1, "EQS": 3, "BAL": -10})
	st.SetEQPresets(livePresets)
	m.view = viewEQ
	m.rows = 40
	view := m.renderEQ(200)
	assertWithin(t, "renderEQ", view, 200)
	plain := stripANSI(strings.Join(view, "\n"))
	for _, want := range []string{"● on", "Jazz", "L10"} {
		if !strings.Contains(plain, want) {
			t.Errorf("equalizer view %q lacks %q", plain, want)
		}
	}
}

// The equalizer view explains the focused control under the sliders: the EQ
// switch's note says the tone sliders stay live, the preset's that it is heard
// only while EQ is on, Max volume's that a low cap is what feels stuck.
func TestEQNotesForTheFocusedControl(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.rows, m.cols = 30, 106
	m.view = viewEQ
	for focus, want := range map[int]string{0: "live either way", 1: "heard only while EQ is on", len(eqOrder) - 1: "stuck near the top"} {
		m.eqFocus = focus
		body := m.renderEQ(100)
		assertWithin(t, "renderEQ", body, 100)
		if got := stripANSI(strings.Join(body, "\n")); !strings.Contains(got, want) {
			t.Errorf("focus %d note missing %q:\n%s", focus, want, got)
		}
	}
}

// The preset selector drew names left to right and stopped at the row's end;
// at the narrowest frame that is not the mini line the current preset (Vocal,
// the sixth) was never drawn, so nothing on the row was lit.
func TestEQCurrentPresetIsDrawnAtTheNarrowestFrame(t *testing.T) {
	m, st, _ := makeModel(t)
	st.SetEQPresets([]string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"})
	st.ApplyTunnel("EQS", 5)
	_, vals := st.EQView()
	W := MiniCols - 6 // the narrowest frame that is not the mini line
	row := stripANSI(m.eqSliderRow(eqSpecIndex("EQS"), vals, false, W))
	if !strings.Contains(row, "Vocal") {
		t.Errorf("W=%d: the current preset is not on the row: %q", W, row)
	}
	if DispW(row) != W {
		t.Errorf("row width = %d, want %d: %q", DispW(row), W, row)
	}
	// a current preset that fits from the first name leaves the list there
	st.ApplyTunnel("EQS", 1)
	_, vals = st.EQView()
	if row := stripANSI(m.eqSliderRow(eqSpecIndex("EQS"), vals, false, W)); !strings.Contains(row, "Flat · Classical") {
		t.Errorf("W=%d, Classical current: %q, want the list from Flat", W, row)
	}
}
