// The palette (theme.go): the colour helpers, the theme selection and the
// repaint on the terminal's answer.

package tui

import (
	"image/color"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestCov_clampFClampRangeTo8(t *testing.T) {
	if clampF(-0.5) != 0 || clampF(0.5) != 0.5 || clampF(1.5) != 1 {
		t.Error("clampF wrong")
	}
	if clampRange(0.1, 0.35, 0.85) != 0.35 {
		t.Error("clampRange below")
	}
	if clampRange(0.5, 0.35, 0.85) != 0.5 {
		t.Error("clampRange in")
	}
	if clampRange(0.99, 0.35, 0.85) != 0.85 {
		t.Error("clampRange above")
	}
	if to8(-0.1) != 0 || to8(0.5) != 128 || to8(2) != 255 {
		t.Errorf("to8 wrong: %d %d %d", to8(-0.1), to8(0.5), to8(2))
	}
}

func TestCov_rampIdx(t *testing.T) {
	const n = 5 // the fill ramp's length
	chk := func(pos, span, want int) {
		if got := rampIdx(n, pos, span); got != want {
			t.Errorf("rampIdx(pos=%d span=%d) = %d, want %d", pos, span, got, want)
		}
	}
	chk(0, 1, 0)   // span<=1 -> ratio 0
	chk(2, 5, 2)   // middle
	chk(10, 2, 4)  // i>=len -> clamp to last
	chk(-10, 5, 0) // negative pos -> i<0 -> clamp to first
}

func TestCov_hslRGBAllArms(t *testing.T) {
	// every hue feeds a different switch arm (int(hp) 0..5) and yields a
	// distinct colour at fixed s/l
	seen := map[[3]uint8]bool{}
	for _, h := range []float64{30, 90, 150, 210, 270, 330} {
		r, g, b := hslRGB(h, 0.7, 0.5)
		seen[[3]uint8{r, g, b}] = true
	}
	if len(seen) != 6 {
		t.Errorf("6 hues produced %d distinct colours, want 6", len(seen))
	}
	// a negative hue wraps (hp<0 correction) and equals its +360 equivalent
	r1, g1, b1 := hslRGB(-30, 1, 0.5)
	r2, g2, b2 := hslRGB(330, 1, 0.5)
	if r1 != r2 || g1 != g2 || b1 != b2 {
		t.Error("hslRGB(-30) should equal hslRGB(330)")
	}
	// anchor: pure red
	if r, g, b := hslRGB(0, 1, 0.5); r != 255 || g != 0 || b != 0 {
		t.Errorf("hslRGB(0,1,0.5) = %d,%d,%d, want 255,0,0", r, g, b)
	}
}

func TestCov_writeDecAndStylePen(t *testing.T) {
	for _, v := range []uint8{0, 7, 42, 100, 255} {
		var b strings.Builder
		writeDec(&b, v)
		if got, want := b.String(), strconv.Itoa(int(v)); got != want {
			t.Errorf("writeDec(%d) = %q, want %q", v, got, want)
		}
	}
	// an empty painted line needs no reset
	var b strings.Builder
	paintEndLine(&b)
	if b.Len() != 0 {
		t.Error("paintEndLine on an empty line should write nothing")
	}
	// an unstyled style flattens to an empty pen that renders text as is
	if p := stylePen(lipgloss.NewStyle()); p.render("x") != "x" {
		t.Errorf("unstyled pen renders %q", p.render("x"))
	}
}

func TestCov_themeDegenerate(t *testing.T) {
	st := newTheme()
	if st.searchBox(0, 5, 0) != nil {
		t.Error("searchBox(w<=0) should be nil")
	}
	// a 1x1 box degrades to just the beacon dot
	if got := st.searchBox(1, 1, 0); len(got) != 1 || lipgloss.Width(got[0]) != 1 {
		t.Errorf("searchBox(1,1) = %q, want one 1-wide line", got)
	}
	// a meter with no cells is empty; frac 0 puts the head at cell 0
	if lineMeterCells(0.5, 0, nil, "●", "─") != "" {
		t.Error("lineMeterCells(cells<=0) should be empty")
	}
	if got := stripANSI(st.lineMeter(0, 10)); !strings.Contains(got, "●") {
		t.Errorf("lineMeter(frac 0) should still draw the head: %q", got)
	}
	// an empty vbar fill draws the track only
	if got := st.vbar(0, 3); len(got) != 3 || strings.Contains(stripANSI(strings.Join(got, "")), "█") {
		t.Errorf("vbar(0) = %q, want three track cells", got)
	}
}

// theme = auto draws the first frame dark: the terminal has not answered yet.
// The light answer rebuilt the palette, but the volume rail, cached by volume,
// mute and height alone, kept the dark colours until the volume changed.
func TestThemeAnswerRepaintsTheVolumeRail(t *testing.T) {
	light := tea.BackgroundColorMsg{Color: color.White}
	m, st, _ := makeModel(t)
	m.dispatch(tea.WindowSizeMsg{Width: 120, Height: 40})
	render(t, m) // the frame before the terminal's answer
	if m.volBlk == nil {
		t.Fatal("setup: the full player should draw the rail")
	}
	m.Update(light)
	if m.themeDark {
		t.Fatal("setup: a white background should switch to the light palette")
	}
	render(t, m)
	if want := m.buildVolRail(st.Snap(), len(m.volBlk)-1); !slices.Equal(m.volBlk, want) {
		t.Errorf("after the light answer the rail kept the dark palette:\n got %q\nwant %q", m.volBlk[len(m.volBlk)-2], want[len(want)-2])
	}
	fresh, _, _ := makeModel(t)
	fresh.dispatch(tea.WindowSizeMsg{Width: 120, Height: 40})
	fresh.dispatch(light)
	render(t, fresh)
	if !slices.Equal(m.volBlk, fresh.volBlk) {
		t.Error("the rail differs from one drawn light from the start")
	}
}

// The warm (boost) and cool (cut) EQ knob colours — which signal a tone band's
// sign (eqSliderRow) — are distinct, asserted at the style level so it's
// independent of the test terminal's colour profile.
func TestToneKnobsDistinct(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th := newThemeFor(dark)
		if th.warmKnob.GetForeground() == th.coolKnob.GetForeground() {
			t.Errorf("dark=%v: the warm (boost) and cool (cut) knobs should be different colours", dark)
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
