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

// eqDisplay is the equalizer's row order by wire code: the EQ enable and the
// preset it applies first (they belong together — the preset colours the sound
// only while EQ is on), then the always-live tone, deep bass, balance, and the
// rarely-touched output cap last.
var eqDisplay = []string{"EQE", "EQS", "TRE", "MID", "BAS", "VBS", "VBI", "BAL", "MXV"}

// eqOrder maps EQ-strip display position -> index into tunnel.Specs (derived
// from eqDisplay so the wire-level Specs order can change freely).
var eqOrder = func() []int {
	idx := make(map[string]int, len(tunnel.Specs))
	for i, sp := range tunnel.Specs {
		idx[sp.Code] = i
	}
	out := make([]int, len(eqDisplay))
	for d, code := range eqDisplay {
		i, ok := idx[code]
		if !ok {
			panic("eqDisplay names an unknown tunnel code: " + code)
		}
		out[d] = i
	}
	return out
}()

// eqShort is the compact band label per wire code (≤ sliderLabelW-1 cells).
var eqShort = map[string]string{
	"MXV": "Max Vol", "EQE": "EQ", "EQS": "Preset", "TRE": "Treble", "MID": "Mid",
	"BAS": "Bass", "VBS": "Sub", "VBI": "Lvl", "BAL": "Balance",
}

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
	// cur is the device's raw readback (inbound values are deliberately not
	// clamped), so the addition must not wrap: a hostile MaxInt readback plus
	// a positive step would otherwise clamp to Min — for MXV a hard 0% cap.
	step := dir * sp.Step
	target := cur + step
	switch {
	case step > 0 && target < cur:
		target = sp.Max
	case step < 0 && target > cur:
		target = sp.Min
	}
	target = tunnel.Clamp(sp.Code, target)
	if sp.Kind == tunnel.Choice {
		// The preset index stops at the last NAMED preset once the device has
		// listed them (PEQ); before that the spec bound applies.
		if n := len(m.st.EQPresets()); n > 0 {
			target = max(0, min(n-1, target))
		}
	}
	m.sendEQ(sp.Code, target)
}

// eqToggleFocused flips a focused on/off control, or steps a choice control
// to its next option (wrapping). No-op on ranged controls; an unknown value
// re-queries instead, like eqAdjust.
func (m *model) eqToggleFocused() {
	sp := m.eqSpec()
	cur, known := m.st.EQValue(sp.Code)
	switch sp.Kind {
	case tunnel.Toggle:
		if !known {
			m.queryEQ(sp.Code)
			return
		}
		v := 0
		if cur == 0 {
			v = 1
		}
		m.sendEQ(sp.Code, v)
	case tunnel.Choice:
		if !known {
			m.queryEQ(sp.Code)
			return
		}
		hi := sp.Max
		if n := len(m.st.EQPresets()); n > 0 {
			hi = n - 1
		}
		next := cur + 1
		if next > hi || next < 0 {
			next = 0
		}
		m.sendEQ(sp.Code, next)
	}
}

// presetName is the display name for an EQS index: the device's PEQ label, or
// "preset N" when the list hasn't arrived or the index is past it.
func presetName(names []string, idx int) string {
	if idx >= 0 && idx < len(names) && names[idx] != "" {
		return names[idx]
	}
	return "preset " + strconv.Itoa(idx)
}

// balStr formats the balance: "0" centred, "L20" / "R20" for the favoured side
// (the wire sign is positive-right).
func balStr(v int) string {
	switch {
	case v < 0:
		return "L" + strconv.Itoa(-v)
	case v > 0:
		return "R" + strconv.Itoa(v)
	}
	return "0"
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

	if spec.Kind == tunnel.Choice {
		// A selector: every named option in a row, the current one lit. The
		// whole list is clipped to the row, never wrapped; the current name is
		// always drawn first-class when it fits (the device names six).
		names := m.st.EQPresets()
		roomW := trackW + sliderValW
		var parts []string
		used := 0
		cur := -1
		if known {
			cur = v
		}
		n := max(len(names), cur+1)
		for i := range n {
			txt := presetName(names, i)
			segW := DispW(txt)
			if i > 0 {
				segW += 3
			}
			if used+segW > roomW {
				break
			}
			if i > 0 {
				parts = append(parts, ps.dmr.render(" · "))
			}
			switch {
			case i == cur && focused:
				parts = append(parts, ps.accB.render(txt))
			case i == cur:
				parts = append(parts, ps.acc.render(txt))
			default:
				parts = append(parts, ps.dmr.render(txt))
			}
			used += segW
		}
		if n == 0 {
			parts = append(parts, ps.dmr.render("—"))
			used = 1
		}
		return labelCell + strings.Join(parts, "") + spaces(max(roomW-used, 0))
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
		switch {
		case spec.Code == "BAL":
			valStr = balStr(v)
		case spec.Min < 0:
			valStr = toneStr(v)
		default:
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

// eqSummary is the compact dashboard's EQ readout: the controls in eqOrder
// (so the display position matches the focus index), highlighting the focused
// one when the EQ pane has focus (accent + bold + underline; the underline
// keeps the cue legible on a no-colour terminal), so a small screen still
// shows what ↑↓ will pick and ←→ will change. Nine controls don't fit one
// narrow line, so the readout flows onto a second line (at most two — the
// compact tail stays bounded); whatever still doesn't fit is dropped, never
// overflowed. Every returned line is ≤ W.
func (m *model) eqSummary(W int) []string {
	_, vals := m.st.EQView()
	names := m.st.EQPresets()
	part := func(code string) string {
		sp, _ := tunnel.Lookup(code)
		v, known := vals[code]
		if !known {
			return eqShort[code] + " —"
		}
		switch {
		case sp.Kind == tunnel.Toggle:
			st := "off"
			if v != 0 {
				st = "on"
			}
			return eqShort[code] + " " + st
		case sp.Kind == tunnel.Choice:
			return presetName(names, v) // the name alone reads fine beside "EQ on/off"
		case code == "BAL":
			return "Bal " + balStr(v)
		case sp.Min < 0:
			return string([]rune(eqShort[code])[:1]) + toneStr(v)
		}
		return fmt.Sprintf("%s %d", eqShort[code], v)
	}
	ps := m.sty.pens()
	sep := ps.dmr.render(" · ")
	const maxLines = 2
	var lines []string
	var b strings.Builder
	used := 0
	for d, idx := range eqOrder {
		txt := part(tunnel.Specs[idx].Code)
		segW := DispW(txt)
		if used > 0 {
			segW += 3 // the " · " separator preceding every part but a line's first
		}
		if used+segW > W {
			if len(lines)+1 >= maxLines || used == 0 {
				break // out of lines (or a single part wider than W): stop cleanly
			}
			lines = append(lines, b.String())
			b.Reset()
			used, segW = 0, DispW(txt)
			if segW > W {
				break
			}
		}
		if used > 0 {
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
	if used > 0 || len(lines) == 0 {
		lines = append(lines, b.String())
	}
	return lines
}

// toneStr formats a signed tone value: "+3", "0", "-6" (avoids an odd "+0").
func toneStr(v int) string {
	if v == 0 {
		return "0"
	}
	return fmt.Sprintf("%+d", v)
}
