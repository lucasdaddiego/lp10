// The palette (theme.go): the colour helpers, the theme selection and the
// repaint on the terminal's answer.

package tui

import (
	"image/color"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

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
