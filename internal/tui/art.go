package tui

import (
	"strings"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// motif returns the cached art block, recomputing only when (w,h,frame) changes.
func (m *model) motif(w, h int) []string {
	m.motifLive = true // the animated plasma is actually on screen this frame
	key := [3]int{w, h, m.frame}
	if m.motifBlk == nil || m.motifKey != key {
		m.motifBlk = m.sty.motifBlock(w, h, m.frame)
		m.motifKey = key
	}
	return m.motifBlk
}

// artColumn renders the left art panel. The tunnel carries no cover, so it is
// the procedural plasma motif whenever there is something to show — a track,
// or playback the device reports without one yet (it moves while playing and
// freezes when paused) — the pulsing searching arcs while (re)connecting, and
// the calm note motif when the box is connected and idle.
func (m *model) artColumn(s protocol.Snapshot, w, h int) []string {
	switch {
	case !s.Connected:
		m.searchLive = true // keep the frame clock ticking so the arcs keep pulsing
		return m.sty.searchBox(w, h, m.frame)
	case s.Track != nil || s.Playing:
		return m.motif(w, h)
	}
	return m.noteBox(w, h)
}

// noteMotif is the small beamed-pair glyph drawn in the idle cover slot — two
// stems under a beam over two note heads, so an empty box reads as "music,
// paused" rather than abandoned. Plain box/▪ glyphs (all width-1 to DispW).
var noteMotif = []string{"┏━━━┓", "┃   ┃", "●   ●"}

// noteBox draws the calm note motif centred in a w×h field — the idle art —
// falling back to a single ♪ in a box too small for the motif (or under a CJK
// locale).
func (m *model) noteBox(w, h int) []string {
	ps := m.sty.pens()
	out := make([]string, h)
	blank := spaces(w)
	for i := range out {
		out[i] = blank
	}
	const nw = 5 // width of every noteMotif line
	if localeAmb == 2 || w < nw || h < len(noteMotif) {
		if g := GL["note"]; h > 0 && w >= DispW(g) {
			col := (w - DispW(g)) / 2
			out[h/2] = spaces(col) + ps.dim.render(g) + spaces(w-col-DispW(g))
		}
		return out
	}
	top := (h - len(noteMotif)) / 2
	col := (w - nw) / 2
	for i, ln := range noteMotif {
		out[top+i] = spaces(col) + ps.dmr.render(ln) + spaces(w-col-nw)
	}
	return out
}

// boxArt wraps art (each line contentW display columns wide) in a thin
// box-drawing frame, so the motif reads as a framed print rather than a
// floating block. The frame is bevelled — the top and left edges lit, the
// bottom and right edges in shadow — so it lifts off the background as if lit
// from the top-left. The result is contentW+2 wide and len(art)+2 tall.
func (m *model) boxArt(art []string, contentW int) []string {
	ps := m.sty.pens()
	lit, shadow := ps.dim, ps.dmr
	h := strings.Repeat(GL["h"], contentW)
	leftBar, rightBar := lit.render(GL["v"]), shadow.render(GL["v"])
	out := make([]string, 0, len(art)+2)
	out = append(out, lit.render(GL["tl"]+h+GL["tr"]))
	for _, line := range art {
		out = append(out, leftBar+line+rightBar)
	}
	return append(out, shadow.render(GL["bl"]+h+GL["br"]))
}
