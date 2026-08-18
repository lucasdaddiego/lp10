// The equalizer pane: display order, the control verbs that ride the :2018
// tunnel, and the three renderers (full-dashboard sliders, the compact one-line
// summary, and the diagnostics readout).

package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// eqOrder maps EQ-strip display position -> index into tunnel.Specs, so the
// graphic equalizer reads EQ · Treble · Mid · Bass · Sub · Lvl · Max Vol while the
// wire-level Specs order is unchanged. Max Vol (the rarely-touched output cap) sits
// last.
var eqOrder = []int{1, 4, 3, 2, 5, 6, 0}

// eqShort is the compact band label per wire code.
var eqShort = map[string]string{"MXV": "Max Vol", "EQS": "EQ", "TRE": "Treble", "MID": "Mid", "BAS": "Bass", "VBS": "Sub", "VBI": "Lvl"}

// sliderLabelW / sliderValW are the fixed-width columns of the eqSliders rows:
// the band label on the left, the right-aligned value on the right, the track
// filling the width between them.
const (
	sliderLabelW = 8 // "Max Vol " — label column: longest name "Max Vol" (7) + 1 space
	sliderValW   = 4 // right-aligned value column: " +10", " 100", "  -1", …
)

// eqSpec returns the tunnel.Spec for the focused EQ-strip position.
func (m *model) eqSpec() tunnel.Spec { return tunnel.Specs[eqOrder[m.eqFocus]] }

// eqAdjust nudges the focused control by dir*step, clamps it, and sends it.
// A control the device hasn't reported yet (slider shows "—") is never nudged:
// that would fabricate a 0 baseline and send it — for MXV a hard cap of the
// speaker's output at 0/5%. Instead the keypress re-queries the control, so a
// lost seed reply self-heals on the next press (the device only broadcasts on
// change, so nothing else would ever repopulate it mid-connection).
func (m *model) eqAdjust(dir int) {
	sp := m.eqSpec()
	cur, known := m.st.EQValue(sp.Code)
	if !known {
		m.queryEQ(sp.Code)
		return
	}
	m.sendEQ(sp.Code, tunnel.Clamp(sp.Code, cur+dir*sp.Step))
}

// eqToggleFocused flips a focused on/off control (no-op on ranged controls; an
// unknown toggle re-queries instead, like eqAdjust).
func (m *model) eqToggleFocused() {
	sp := m.eqSpec()
	if sp.Kind != tunnel.Toggle {
		return
	}
	cur, known := m.st.EQValue(sp.Code)
	if !known {
		m.queryEQ(sp.Code)
		return
	}
	v := 0
	if cur == 0 {
		v = 1
	}
	m.sendEQ(sp.Code, v)
}

// queryEQ asks the device to re-broadcast one control's value (no local write,
// no echo hold — the broadcast lands via ApplyTunnel like any other).
func (m *model) queryEQ(code string) {
	nbSend(m.eqcmds, workers.EQCommand{Code: code, Query: true, TS: time.Now()})
}

// sendEQ records the change optimistically (arming the echo hold) and enqueues
// the tunnel write, never blocking the update loop (drop-oldest like send).
func (m *model) sendEQ(code string, val int) {
	m.st.SetEQLocal(code, val)
	nbSend(m.eqcmds, workers.EQCommand{Code: code, Val: val, TS: time.Now()})
}

// eqSliders renders one horizontal row per EQ band, all W columns wide, in
// display order. The rows are pinned to the bottom tail of the full dashboard.
func (m *model) eqSliders(W int) []string {
	_, vals := m.st.EQView()
	rows := make([]string, len(eqOrder))
	for d, idx := range eqOrder {
		rows[d] = m.eqSliderRow(idx, vals, m.pane == paneEQ && m.eqFocus == d, W)
	}
	return rows
}

// eqSliderRow renders one EQ control as a W-wide horizontal row:
//
//	Toggle: "Label    ● on                        "
//	Ranged: "Label    ────────────●────────────  +3"
//
// The label column is sliderLabelW wide; the value column is sliderValW wide
// (right-aligned); the slider track fills the rest.
func (m *model) eqSliderRow(specIdx int, vals map[string]int, focused bool, W int) string {
	ps := m.sty.pens()
	trackW := max(W-sliderLabelW-sliderValW, 1)
	spec := tunnel.Specs[specIdx]
	v, known := vals[spec.Code]

	// Label column: accent+bold when focused, dim otherwise.
	labelPen := ps.dim
	if focused {
		labelPen = ps.accB
	}
	raw := Clip(eqShort[spec.Code], sliderLabelW-1)
	labelCell := labelPen.render(raw) + spaces(sliderLabelW-DispW(raw))

	if spec.Kind == tunnel.Toggle {
		knob, state := "○", "off"
		knobPen, statePen := ps.dmr, ps.dmr
		if known && v != 0 {
			knob, state = "●", "on"
			knobPen, statePen = ps.acc, ps.acc
		}
		content := knobPen.render(knob) + " " + statePen.render(state)
		// pad content out to fill trackW + sliderValW (the right portion of the row)
		pad := max(trackW+sliderValW-1-1-DispW(state), 0)
		return labelCell + content + spaces(pad)
	}

	// Ranged: a horizontal slider ────●────
	frac := 0.0
	if known && spec.Max > spec.Min {
		frac = float64(v-spec.Min) / float64(spec.Max-spec.Min)
	}
	knobPos := max(int(frac*float64(trackW-1)+0.5), 0)
	if knobPos >= trackW {
		knobPos = trackW - 1
	}

	// Knob colour: warm for a positive tone boost, cool for a cut, accent otherwise.
	knobPen := ps.dim
	if focused {
		switch {
		case spec.Min < 0 && known && v > 0:
			knobPen = ps.warmKnob
		case spec.Min < 0 && known && v < 0:
			knobPen = ps.coolKnob
		default:
			knobPen = ps.acc
		}
	}
	track := ps.dmr.render(dashes(knobPos)) + knobPen.render("●") + ps.dmr.render(dashes(trackW-1-knobPos))

	// Value column: right-aligned within sliderValW cells.
	valStr := "—"
	if known {
		if spec.Min < 0 {
			valStr = toneStr(v)
		} else {
			valStr = strconv.Itoa(v)
		}
	}
	valPen := ps.dim
	if focused {
		valPen = ps.bri
	}
	vraw := Clip(valStr, sliderValW)
	return labelCell + track + spaces(sliderValW-DispW(vraw)) + valPen.render(vraw)
}

// eqSummary is the compact dashboard's one-line EQ readout. It runs in eqOrder so
// the display position matches the focus index, and — when the EQ pane has focus —
// highlights the selected band (accent + bold + underline; the underline keeps the
// cue legible even on a no-colour terminal), so a small screen still shows what
// ↑↓ will pick and ←→ will change. Parts are added until W is full (width-safe).
func (m *model) eqSummary(W int) string {
	_, vals := m.st.EQView()
	part := func(code string) string {
		sp, _ := tunnel.Lookup(code)
		v, known := vals[code]
		if !known {
			return eqShort[code] + " —"
		}
		if sp.Kind == tunnel.Toggle {
			st := "off"
			if v != 0 {
				st = "on"
			}
			return eqShort[code] + " " + st
		}
		if sp.Min < 0 {
			return string([]rune(eqShort[code])[:1]) + toneStr(v)
		}
		return fmt.Sprintf("%s %d", eqShort[code], v)
	}
	ps := m.sty.pens()
	sep := ps.dmr.render(" · ")
	var b strings.Builder
	used := 0
	for d, idx := range eqOrder {
		txt := part(tunnel.Specs[idx].Code)
		segW := DispW(txt)
		if d > 0 {
			segW += 3 // the " · " separator preceding every part but the first
		}
		if used+segW > W {
			break // out of room: stop cleanly rather than overflow the line
		}
		if d > 0 {
			b.WriteString(sep)
		}
		if m.pane == paneEQ && m.eqFocus == d {
			// accent+bold+underline focus cue: a real Style.Render — underline
			// styles are not pen-safe (see sFocusBU) — on one short segment.
			b.WriteString(m.sty.sFocusBU.Render(txt))
		} else {
			b.WriteString(ps.dim.render(txt))
		}
		used += segW
	}
	return b.String()
}

// toneStr formats a signed tone value: "+3", "0", "-6" (avoids an odd "+0").
func toneStr(v int) string {
	if v == 0 {
		return "0"
	}
	return fmt.Sprintf("%+d", v)
}
