package tui

import (
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

var livePresets = []string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"}

func TestPresetRowNamesAndHighlight(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	const w = 70
	st.SetEQPresets(livePresets)
	st.ApplyTunnel("EQS", 4) // Rock
	_, vals := st.EQView()
	row := m.eqSliderRow(eqOrder[1], vals, false, w)
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
	// the selected name is styled differently from its neighbours
	if !strings.Contains(row, "Rock") || strings.Index(row, "Rock") == strings.Index(plain, "Rock") {
		// styled output carries escapes before Rock; a bare match at the same
		// offset would mean it was rendered like plain filler
		t.Errorf("Rock should carry its own styling: %q", row)
	}
	// unknown value: every name dim, nothing lit, still full width
	mu, su, _ := modelWith(protocol.NewState())
	mu.sty = newTheme()
	su.SetEQPresets(livePresets)
	if got := stripANSI(mu.eqSliderRow(eqOrder[1], map[string]int{}, true, w)); DispW(got) != w || !strings.Contains(got, "Flat") {
		t.Errorf("unknown-value row = %q", got)
	}
	// no PEQ list yet: the index shows as "preset N"; no list and no value: "—"
	m2, _, _ := modelWith(protocol.NewState())
	m2.sty = newTheme()
	if got := stripANSI(m2.eqSliderRow(eqOrder[1], map[string]int{"EQS": 2}, false, w)); !strings.Contains(got, "preset 2") {
		t.Errorf("nameless row = %q, want 'preset 2'", got)
	}
	if got := stripANSI(m2.eqSliderRow(eqOrder[1], map[string]int{}, false, w)); !strings.Contains(got, "—") || DispW(got) != w {
		t.Errorf("empty row = %q", got)
	}
	// a narrow row clips the list instead of overflowing
	if got := stripANSI(m.eqSliderRow(eqOrder[1], vals, false, 24)); DispW(got) != 24 {
		t.Errorf("narrow row width = %d, want 24: %q", DispW(got), got)
	}
}

func TestPresetKeysStepAndWrap(t *testing.T) {
	m, st, eqcmds := eqModel(t)
	st.SetEQPresets(livePresets)
	m.key(kr('e'))
	m.eqFocus = 1 // Preset
	if m.eqSpec().Code != "EQS" {
		t.Fatalf("slot 1 is %s, want EQS", m.eqSpec().Code)
	}
	m.key(ke(kRight)) // 0 -> 1
	if cmd := <-eqcmds; cmd.Code != "EQS" || cmd.Val != 1 {
		t.Errorf("right: %+v, want EQS 1", cmd)
	}
	st.PreloadEQ(map[string]int{"EQS": 5}) // last named (PreloadEQ sidesteps the echo hold)
	m.key(ke(kRight))                      // stays at the last named preset
	if cmd := <-eqcmds; cmd.Val != 5 {
		t.Errorf("right at the end: val=%d, want 5", cmd.Val)
	}
	m.key(ke(kEnter)) // enter steps to the next, wrapping to 0
	if cmd := <-eqcmds; cmd.Val != 0 {
		t.Errorf("enter at the end: val=%d, want wrap to 0", cmd.Val)
	}
	m.key(ke(kLeft)) // 0 -> clamp 0
	if cmd := <-eqcmds; cmd.Val != 0 {
		t.Errorf("left at 0: val=%d, want 0", cmd.Val)
	}
	// before the PEQ list arrives the spec bound applies, not the list length
	st2 := protocol.NewState()
	st2.ApplyTunnel("EQS", 7)
	m2, _, c2 := modelWith(st2)
	m2.eqcmds = eqcmds
	_ = c2
	m2.key(kr('e'))
	m2.eqFocus = 1
	m2.key(ke(kRight))
	if cmd := <-eqcmds; cmd.Val != 8 {
		t.Errorf("no list: val=%d, want 8", cmd.Val)
	}
}

func TestBalanceRowAndSummary(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	for v, want := range map[int]string{-20: "L20", 0: "0", 35: "R35", -100: "L100"} {
		if got := stripANSI(m.eqSliderRow(eqOrder[7], map[string]int{"BAL": v}, true, 60)); !strings.Contains(got, want) || !strings.HasPrefix(got, "Balance") {
			t.Errorf("BAL %d row = %q, want %q", v, got, want)
		}
	}
	st.PreloadEQ(map[string]int{"EQE": 1, "EQS": 3, "BAL": -10})
	st.SetEQPresets(livePresets)
	sum := stripANSI(strings.Join(m.eqSummary(200), " "))
	for _, want := range []string{"EQ on", "Jazz", "Bal L10"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary %q lacks %q", sum, want)
		}
	}
}

func TestEQSummaryWrapsToTwoLines(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	st.PreloadEQ(map[string]int{"EQE": 0, "EQS": 0, "TRE": 0, "MID": 0, "BAS": 0, "VBS": 0, "VBI": 0, "BAL": 0, "MXV": 100})
	st.SetEQPresets(livePresets)
	one := m.eqSummary(200)
	if len(one) != 1 || !strings.Contains(stripANSI(one[0]), "Max Vol 100") {
		t.Errorf("wide summary = %q, want one line ending in Max Vol", one)
	}
	two := m.eqSummary(52) // the compact minimum width
	if len(two) != 2 {
		t.Fatalf("narrow summary = %d lines, want 2: %q", len(two), two)
	}
	joined := stripANSI(strings.Join(two, " "))
	if !strings.Contains(joined, "EQ off") || !strings.Contains(joined, "Max Vol 100") {
		t.Errorf("narrow summary lost a control: %q", joined)
	}
	for _, l := range two {
		if DispW(stripANSI(l)) > 52 {
			t.Errorf("line %q exceeds 52", stripANSI(l))
		}
	}
}

func TestEQFooterHintsForEnableAndPreset(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	m.pane = paneEQ
	m.eqFocus = 0
	if got := stripANSI(m.footerRow(100)); !strings.Contains(got, "always live") {
		t.Errorf("EQE hint = %q", got)
	}
	m.eqFocus = 1
	if got := stripANSI(m.footerRow(100)); !strings.Contains(got, "preset") {
		t.Errorf("EQS hint = %q", got)
	}
}
