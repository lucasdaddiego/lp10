package tui

import (
	"strings"

	"github.com/lucasdaddiego/lp10/internal/artwork"
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

// artRender is the chosen album-art renderer for the current frame.
type artRender int

const (
	artMotif artRender = iota // procedural plasma (no art, or unsupported terminal)
	artHalf                   // 24-bit half-block raster (any truecolor terminal)
	artKitty                  // Kitty true-pixel image (Ghostty / kitty)
)

// kittyImageID is the fixed Kitty image id every cover is transmitted under. One
// id suffices: the current cover is (re)loaded into it whenever the art changes,
// immediately before the placeholders that reference it are painted, so a stale
// image is never shown. The value is arbitrary but stable. The idle ghost cover
// rides a separate id so the two never share a placement slot.
const (
	kittyImageID = 1981
	kittyGhostID = 1982
)

// artKey identifies a rasterized cover by source URL, cell dimensions, the cell
// pixel size (the Kitty PNG is sized to it), and the renderer that produced it
// (so switching modes, sizes, or fonts rebuilds the cache).
type artKey struct {
	url    string
	w, h   int
	cw, ch int
	mode   artRender
}

// artChoice resolves which renderer to use for a loaded cover, honoring the
// art_mode config and the terminal's capabilities, degrading kitty → half-block
// → motif as support runs out.
func (m *model) artChoice() artRender {
	if !m.cfg.Art {
		return artMotif
	}
	switch m.cfg.ArtMode {
	case "off":
		return artMotif
	case "halfblock":
		if m.sty.trueColor {
			return artHalf
		}
	case "kitty":
		// explicit override: force the Kitty path even when auto-detection didn't
		// fire (e.g. a terminal that supports the protocol but isn't fingerprinted).
		// artColumn still degrades to half-block / motif if the encode yields nothing.
		return artKitty
	default: // "auto"
		if m.sty.kittyGraphics {
			return artKitty
		}
		if m.sty.trueColor {
			return artHalf
		}
	}
	return artMotif
}

// artColumn renders the left art panel: the real album cover (Kitty pixels or a
// half-block raster) when one is loaded and supported, otherwise the procedural
// plasma motif (radio/idle, art disabled, or a lesser terminal). The raster is
// cached by (url,w,h,mode) so a steady cover costs nothing per frame; the Kitty
// transmit rides the first line, so it re-sends only when that line repaints.
func (m *model) artColumn(s protocol.Snapshot, w, h int) []string {
	if s.Track == nil {
		return m.idleArt(s, w, h) // nothing playing: sonar while connecting, else the ghost cover
	}
	if s.Art == nil {
		return m.motif(w, h) // playing without a cover (radio): the plasma motif
	}
	mode := m.artChoice()
	if mode == artMotif {
		return m.motif(w, h)
	}
	key := artKey{s.CoverURL, w, h, m.cellW, m.cellH, mode}
	if m.artBlk == nil || m.artKey != key {
		var built []string
		switch mode {
		case artKitty:
			if transmit, lines := artwork.KittyImage(s.Art, w, h, kittyImageID, w*m.cellW, h*m.cellH); len(lines) > 0 {
				lines[0] = transmit + lines[0] // zero-width: loads the image, then the cells composite it
				built = lines
			} else if m.sty.trueColor {
				built = artwork.HalfBlock(s.Art, w, h) // encode failed: degrade in place
			}
		case artHalf:
			built = artwork.HalfBlock(s.Art, w, h)
		}
		if built == nil {
			return m.motif(w, h) // give up to the motif without poisoning the cache
		}
		m.artBlk, m.artKey = built, key
	}
	return m.artBlk
}

// noteMotif is the small beamed-pair glyph drawn in the idle cover slot — two
// stems under a beam over two note heads, so an empty box reads as "music,
// paused" rather than abandoned. Plain box/▪ glyphs (all width-1 to charW).
var noteMotif = []string{"┏━━━┓", "┃   ┃", "●   ●"}

// refreshAmbient recomputes the per-album tint when the cover changes. It clears
// the tint whenever no real cover is on screen (idle, radio, art disabled, or a
// lesser terminal), and otherwise derives it from the cover's dominant hue —
// keeping the theme default for a greyscale cover (DominantOK==false).
func (m *model) refreshAmbient(s protocol.Snapshot) {
	if s.Art == nil || m.artChoice() == artMotif {
		m.amb, m.ambKey = nil, ""
		return
	}
	if m.ambKey == s.CoverURL {
		return // already resolved (tint or deliberate nil) for this cover
	}
	m.ambKey = s.CoverURL
	if s.DominantOK {
		m.amb = m.sty.tint(s.Dominant) // hue precomputed off the render path by the art worker
	} else {
		m.amb = nil
	}
}

// idleArt fills the cover slot when nothing is playing, telling the connection
// state at a glance: a live radar sweep while (re)connecting, then — once
// connected and simply idle — a dimmed "ghost" of the last cover played (a calm
// note motif when there's no cover to recall). boxArt adds the frame around it.
func (m *model) idleArt(s protocol.Snapshot, w, h int) []string {
	if !s.Connected {
		m.sonarLive = true // keep the frame clock ticking so the beam keeps sweeping
		return m.sty.sonar(w, h, m.frame)
	}
	if g := m.ghostCover(s, w, h); g != nil {
		return g
	}
	return m.noteBox(w, h)
}

// ghostCover renders the last-played cover dimmed and desaturated — a faint
// memory in the idle slot — reusing the Kitty / half-block path on a ghosted
// image, cached in its own slot. Returns nil when there's nothing to recall or
// art is off, so the caller can fall back to the note motif.
func (m *model) ghostCover(s protocol.Snapshot, w, h int) []string {
	mode := m.artChoice()
	if s.LastArt == nil || mode == artMotif {
		return nil
	}
	key := artKey{url: "ghost:" + s.LastCoverURL, w: w, h: h, cw: m.cellW, ch: m.cellH, mode: mode}
	if m.ghostBlk == nil || m.ghostKey != key {
		img := artwork.Ghost(s.LastArt)
		var built []string
		switch mode {
		case artKitty:
			if transmit, lines := artwork.KittyImage(img, w, h, kittyGhostID, w*m.cellW, h*m.cellH); len(lines) > 0 {
				lines[0] = transmit + lines[0]
				built = lines
			} else if m.sty.trueColor {
				built = artwork.HalfBlock(img, w, h)
			}
		case artHalf:
			built = artwork.HalfBlock(img, w, h)
		}
		if built == nil {
			return nil
		}
		m.ghostBlk, m.ghostKey = built, key
	}
	return m.ghostBlk
}

// noteBox draws the calm note motif centred in a w×h field — the idle fallback
// when there's no cover to ghost — falling back to a single ♪ in a box too small
// for the motif (or under a CJK locale).
func (m *model) noteBox(w, h int) []string {
	out := make([]string, h)
	blank := strings.Repeat(" ", w)
	for i := range out {
		out[i] = blank
	}
	const nw = 5 // width of every noteMotif line
	if localeAmb == 2 || w < nw || h < len(noteMotif) {
		if g := GL["note"]; h > 0 && w >= DispW(g) {
			col := (w - DispW(g)) / 2
			out[h/2] = strings.Repeat(" ", col) + m.sty.sDim.Render(g) + strings.Repeat(" ", w-col-DispW(g))
		}
		return out
	}
	top := (h - len(noteMotif)) / 2
	col := (w - nw) / 2
	for i, ln := range noteMotif {
		out[top+i] = strings.Repeat(" ", col) + m.sty.sDmr.Render(ln) + strings.Repeat(" ", w-col-nw)
	}
	return out
}

// boxArt wraps art (each line contentW display columns wide) in a thin
// box-drawing frame, so the cover reads as a framed print rather than a floating
// image. The frame is bevelled — the top and left edges lit, the bottom and
// right edges in shadow — so the cover lifts off the background as if lit from
// the top-left. The lit edge takes the album's ambient hue when one is active.
// The result is contentW+2 wide and len(art)+2 tall.
func (m *model) boxArt(art []string, contentW int) []string {
	lit := m.sty.sDim
	if m.amb != nil {
		lit = m.amb.frame
	}
	shadow := m.sty.sDmr
	h := strings.Repeat(GL["h"], contentW)
	leftBar, rightBar := lit.Render(GL["v"]), shadow.Render(GL["v"])
	out := make([]string, 0, len(art)+2)
	out = append(out, lit.Render(GL["tl"]+h+GL["tr"]))
	for _, line := range art {
		out = append(out, leftBar+line+rightBar)
	}
	return append(out, shadow.Render(GL["bl"]+h+GL["br"]))
}
