// Rendering of the player: View's size dispatch and frame, the mini line, the
// full/compact dashboards, and their rows (header, now-playing metadata, seek,
// transport, volume rail, divider, footer).

package tui

import (
	"cmp"
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// View satisfies bubbletea v2's Model: the frame content plus the per-view
// terminal state that v1 carried as program options (alt screen, mouse mode)
// or commands (the window title).
func (m *model) View() tea.View {
	v := tea.NewView(m.viewContent())
	v.AltScreen = true
	v.WindowTitle = m.curTitle
	if m.cfg.Mouse {
		// CellMotion (not AllMotion) reports motion only while a button is held,
		// so a left-drag scrubs a control while idle motion stays out of the loop.
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// viewContent renders the whole frame as a styled string (the v1 View body).
func (m *model) viewContent() string {
	if m.sty == nil {
		m.sty = newTheme()
	}
	rows, cols := m.rows, m.cols
	if rows == 0 || cols == 0 {
		return ""
	}
	s := m.st.Snap()
	m.motifLive, m.searchLive = false, false // set true below iff the plasma / search figure is actually drawn
	// Cleared each frame; renderDashboard repopulates. [:0] keeps the backing
	// arrays (a dozen appends per frame otherwise) — safe because Update and
	// View run sequentially on the program loop, so the zones a mouse event
	// reads are never mid-overwrite.
	m.mzBtns, m.mzVol, m.mzEQ = m.mzBtns[:0], volZone{}, m.mzEQ[:0]
	if rows < MiniRows || cols < MiniCols {
		m.diag = false
		return m.renderMini(s)
	}

	// The frame fills the whole terminal: W is the content width inside the
	// border (1+1) and padding (2+2); the renderers fill the body to the inner
	// height so the box touches all four window edges (no outer margin).
	W := cols - 6
	now := time.Now()

	var body []string
	switch {
	case m.diag:
		body = m.renderDiag(s, now, W)
	default:
		full := rows >= FullRows && cols >= FullCols
		body = m.renderDashboard(s, now, W, full)
	}
	return m.frameLines(body, W)
}

// frameLines wraps the body lines in the full-window thick border with the
// side padding, in one builder pass. It replaces the old
// Border(ThickBorder)+Padding(0,2)+Render+lipgloss.Place pipeline, which split,
// ANSI-measured and re-joined the same lines several times per frame; the
// bytes are identical (TestFrameMatchesLipgloss pins that, including the Place
// step: the framed block is already exactly cols×rows, so Place was a no-op).
func (m *model) frameLines(lines []string, W int) string {
	ps := m.sty.pens()
	// content width = the widest line, floored at W (at least one line — the
	// header/masthead — is built to exactly W, so this is W in practice; the
	// max() mirrors how lipgloss sizes the box if a line ever overflowed).
	widths := make([]int, len(lines))
	contentW := W
	for i, ln := range lines {
		if ln == "" {
			continue // stack/frameBody pad with "" — skip the ANSI parse
		}
		widths[i] = visWidth(ln)
		contentW = max(contentW, widths[i])
	}
	edge := strings.Repeat("━", contentW+4)
	side := ps.border.render("┃")
	top := ps.border.render("┏" + edge + "┓")
	bottom := ps.border.render("┗" + edge + "┛")

	// Grow to the exact byte size, not the visible-column size. Animated art
	// carries one ANSI colour escape per cell, so a 150-column frame can contain
	// several times as many bytes as display cells. Growing by columns made the
	// builder repeatedly reallocate and copy the nearly-complete frame on every
	// animation tick even though the final output is deterministic.
	frameBytes := len(top) + len(bottom) + len(lines) + 1 // one newline per body row + before bottom
	for i, ln := range lines {
		frameBytes += 2*len(side) + 2 + len(ln) + contentW - widths[i] + 2
	}
	var b strings.Builder
	b.Grow(frameBytes)
	b.WriteString(top)
	for i, ln := range lines {
		b.WriteByte('\n')
		b.WriteString(side)
		b.WriteString("  ")
		b.WriteString(ln)
		b.WriteString(spaces(contentW - widths[i] + 2))
		b.WriteString(side)
	}
	b.WriteByte('\n')
	b.WriteString(bottom)
	return b.String()
}

func (m *model) renderMini(s protocol.Snapshot) string {
	ps := m.sty.pens()
	t, cols := s.Track, m.cols
	switch {
	case s.Error != "" && (s.Fatal || time.Since(s.ErrorAt) < ErrorDisplayDuration):
		return ps.red.render(Clip(GL["warn"]+" "+friendlyError(s.Error), cols-1))
	case t != nil:
		glyph := GL["play"]
		if s.Playing != 0 {
			glyph = GL["pause"]
		}
		line := fmt.Sprintf("%s %s — %s  %s/%s  %d%%", glyph, t.Str("TrackName"), t.Str("Artist"),
			FmtMs(s.Pos), m.fmtRight(t.GetInt("TotalTime"), s.Pos), s.Vol)
		return ps.txt.render(Clip(line, cols-1))
	default:
		msg := "connecting to LP10…"
		if s.Connected {
			msg = "nothing playing"
		}
		return ps.dim.render(Clip(GL["note"]+" "+msg, cols-1))
	}
}

func (m *model) renderDashboard(s protocol.Snapshot, now time.Time, W int, full bool) []string {
	m.refreshAmbient(s) // recolour the meter/frame/dot to the cover (must precede headerRow)
	header := m.headerRow(s, now, W, full)
	// The bold-red error line is only for a fatal stop or a hiccup *while
	// connected*. A routine "can't reach the device" during reconnection is
	// already told by the header ("reconnecting…") and the idle reason below the
	// "connecting to LP10…" line, so don't also dump it red across the bottom.
	errLine := ""
	if s.Error != "" && (s.Fatal || (s.Connected && now.Sub(s.ErrorAt) < ErrorDisplayDuration)) {
		errLine = stRed.Render(Clip(GL["warn"]+" "+friendlyError(s.Error), W))
	}
	inner := m.rows - 2

	if full {
		// EQ: one horizontal row per band (W-wide), pinned to the bottom under a
		// divider. Build the tail first so the cover height is based on what's left.
		tail := append([]string{m.dividerRow("equalizer", W)}, m.eqSliders(W)...)
		tail = append(tail, m.footerRow(W))
		if errLine != "" {
			tail = append(tail, errLine)
		}
		// The framed cover fills the region between the header and that tail. Its
		// height comes from the real region; its width makes the box *square in
		// device pixels* using the measured cell aspect (cells are ~2:1, but the
		// exact ratio varies by font/terminal — assuming 2:1 left covers stretched).
		// Bounded by a hard height cap so it stays a cover, not a billboard, and by
		// width so the metadata column stays usable.
		// region minus the 2 frame rows, capped to a tasteful record sleeve (not a billboard)
		coverH := max(min((inner-2-len(tail))-2, coverHCap), 6)
		cellAR := 2.0 // cell height ÷ width; converts a cell count to display pixels
		if m.cellW > 0 && m.cellH > 0 {
			cellAR = float64(m.cellH) / float64(m.cellW)
		}
		// Size the box to the cover's TRUE aspect ratio (album art isn't always
		// square) so neither the half-block raster (which stretches to fill its cell
		// box) nor the Kitty placement distorts it: the box's display footprint
		// (coverW·cellW × coverH·cellH px) tracks the source's width:height. A square
		// cover keeps the old square box; a non-square one no longer gets stretched.
		srcAR := 1.0 // source width ÷ height
		if s.Art != nil {
			if b := s.Art.Bounds(); b.Dx() > 0 && b.Dy() > 0 {
				srcAR = float64(b.Dx()) / float64(b.Dy())
			}
		}
		coverW := int(float64(coverH)*cellAR*srcAR + 0.5)
		if maxW := W - 37; coverW > maxW { // reserve room for the metadata + volume columns
			coverW = maxW
			coverH = int(float64(coverW)/(cellAR*srcAR) + 0.5)
		}
		if coverH < 6 {
			coverH = 6
		}
		if coverW < 8 {
			coverW = 8
		}
		blockH := coverH + 2 // framed cover height = the now-playing block height
		midW := W - (coverW + 2) - volColW - 2*artGap
		// Three columns, all blockH tall: the framed album cover (left, a tidy sleeve);
		// the now-playing column (middle); and a full-height volume rail (right). The
		// middle is built as ONE cohesive block — title / artist / album, a blank, the
		// source·format line, a blank, then the seek bar + transport — and centred
		// vertically beside the cover. Grouping it tightly (rather than spreading the
		// pieces evenly down the whole height) keeps it ordered instead of scattered
		// with the source line floating in a void.
		mid := m.fullMeta(s, midW)
		if src := m.fullSourceLine(s, midW); src != "" {
			mid = append(mid, "", src)
		}
		mid = append(mid, "", m.seekRow(s, midW), "", m.transportSegments(s, now, midW))
		midLen := len(mid)
		mid = frameBody(mid, nil, blockH, true) // centre the cohesive block in the column
		art := m.boxArt(m.artColumn(s, coverW, coverH), coverW)
		block := joinCols(art, mid, m.volRail(s, blockH-1), midW)

		m.recordFullZones(coverW, midW, blockH, midLen, len(tail), inner, W)
		// header pinned top, EQ + footer pinned bottom, the cover block centred between
		return stack([]string{header, ""}, block, tail, inner)
	}

	// Compact: no art / vertical sliders — top-pinned metadata + seek + controls,
	// with the one-line EQ summary and footer pinned to the bottom.
	meta := m.metaLines(s, W)
	content := append([]string{header, ""}, meta...)
	content = append(content, "", m.seekRow(s, W), "", m.controlsRow(s, now, W, true))
	tail := []string{m.dividerRow("equalizer", W), m.eqSummary(W), m.footerRow(W)}
	if errLine != "" {
		tail = append(tail, errLine)
	}
	m.recordCompactZones(s, len(meta), len(tail), inner, W)
	return frameBody(content, tail, inner, false)
}

// joinCols composes the full layout's three player columns — the framed cover,
// the now-playing middle, and the volume rail — row by row with the artGap
// between them. It replaces JoinHorizontal(Join(…),…) + re-Split, which ANSI-
// measured every line of every column per frame. The art and vol columns are
// uniform-width by construction (boxArt frames, ccell cells); mid lines vary,
// so each is padded to midW — exactly what JoinHorizontal did, since mid always
// contains a full-width line (the seek row), making midW its widest.
// TestJoinColsMatchesJoinHorizontal pins the byte equivalence. All three
// columns are blockH tall by construction; the min() guards a future mismatch
// from panicking (the columns would visibly misalign, caught by tests).
func joinCols(art, mid, vol []string, midW int) []string {
	gap := spaces(artGap)
	n := min(len(art), len(mid), len(vol))
	out := make([]string, n)
	for i := range out {
		// Fold the middle-column padding into the final concatenation. Calling
		// padVis first built and copied an intermediate string, then copied it
		// again into this row. This produces the same bytes with one row-sized
		// allocation instead of two.
		pad := spaces(max(midW-visWidth(mid[i]), 0))
		out[i] = art[i] + gap + mid[i] + pad + gap + vol[i]
	}
	return out
}

// stack composes exactly h lines: top pinned to the top, bottom pinned to the
// bottom, and middle vertically centred in the gap between. Callers size middle
// to fit the gap; any excess is trimmed from its bottom.
func stack(top, middle, bottom []string, h int) []string {
	if h <= 0 {
		return nil
	}
	out := make([]string, h)
	copy(out, top)
	copy(out[max(0, h-len(bottom)):], bottom)
	region := max(h-len(top)-len(bottom), 0)
	if len(middle) > region {
		middle = middle[:region]
	}
	copy(out[len(top)+(region-len(middle))/2:], middle)
	return out
}

// frameBody lays content and tail into exactly h lines so the bordered frame can
// span the full window height. The tail (footer / help / error) is always pinned
// to the bottom; the content is either top-aligned or vertically centred in the
// space above it (center). When content + tail overflow h, content is trimmed
// from the bottom so the tail stays visible (rather than letting Bubble Tea
// guillotine the top off-screen).
func frameBody(content, tail []string, h int, center bool) []string {
	if h <= 0 {
		return nil
	}
	if len(tail) >= h {
		return tail[len(tail)-h:]
	}
	room := h - len(tail)
	if len(content) > room {
		content = content[:room]
	}
	top := 0
	if center {
		top = (room - len(content)) / 2
	}
	out := make([]string, h) // zero value "" fills the gaps
	copy(out[top:], content)
	copy(out[room:], tail)
	return out
}

func (m *model) headerRow(s protocol.Snapshot, now time.Time, W int, full bool) string {
	ps := m.sty.pens()
	clock := now.Format("15:04")
	note := GL["note"]

	// connection status sits on the left, next to the device name; in full mode
	// "Vol" labels the volume rail from the right, centred over its column so it
	// sits directly above the bar (which starts on the row just below).
	statTxt := "● " + clock // the green dot reads unambiguously as "connected"
	// The connected dot stays the theme's green — a status light, not an accent: an
	// album-tinted dot (e.g. orange for a sepia cover) reads as a warning. The
	// ambient hue still colours the seek bar and cover frame, just not this light.
	statStyled := ps.acc.render("●") + ps.dim.render(" "+clock)
	if !s.Connected {
		statTxt = "● connecting…"
		if s.Attempts > 1 {
			statTxt = fmt.Sprintf("● reconnecting (%d)…", s.Attempts)
		}
		statStyled = ps.warn.render(statTxt)
	}

	prefixW := DispW(note) + 1 // "♪ "
	statW := DispW(statTxt)

	var vol string
	volW := 0
	if full {
		vol = ps.volCell
		if s.Muted {
			vol = ps.mutedCell // flag mute from the top, over the rail
		}
		volW = volColW
	}

	// device name on the left; clip it so the status (and Vol) always fit, but
	// don't let a short name sprawl across a wide header.
	nameMax := max(min(W-prefixW-2-statW-volW-4, 24), 4)
	name := Clip(m.cfg.Name, nameMax)
	left := ps.acc.render(note) + " " + ps.acc.render(name) + "  " + statStyled
	leftW := prefixW + DispW(name) + 2 + statW

	// source/format fills the gap before Vol when a track is playing and there's
	// room; clipped to whatever space is left so the header never overflows W.
	right, rightW := vol, volW
	if q := sourceFormat(s.Track); q != "" {
		room := W - leftW - 1 // compact: one min gap before the right edge
		if full {
			room = W - leftW - volW - 3 // gap before quality + 2-col gap before Vol
		}
		if room >= 8 {
			var qStyled string
			var qW int
			if name := SourceName(s.Track); DispW(q) <= room && name != "" && strings.HasPrefix(q, name) {
				// fits fully: tint the source name in its brand colour, dim the format
				qStyled = ps.brandPen(name).render(name) + ps.dmr.render(strings.TrimPrefix(q, name))
				qW = DispW(q)
			} else {
				c := Clip(q, room)
				qStyled, qW = ps.dmr.render(c), DispW(c)
			}
			if full {
				right, rightW = qStyled+"  "+vol, qW+2+volW
			} else {
				right, rightW = qStyled, qW
			}
		}
	}
	return between(left, leftW, right, rightW, W)
}

// Now-playing marquee tuning: a line wider than its column scrolls horizontally,
// looping with a gap and pausing briefly at the start so the head stays readable.
const (
	marqueeGap      = "      " // blank run between loop repetitions
	marqueeColTicks = 2        // ticks per one-column shift (~200ms at the 100ms tick)
	marqueePauseCol = 10       // columns of pause at the start of each loop
)

// marquee renders one now-playing line into width w: returned unchanged when it
// fits, otherwise a scrolling w-wide window that loops over time (driven by the
// model's tick counter, so all lines advance together).
func (m *model) marquee(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if DispW(s) <= w {
		return s
	}
	strip := s + marqueeGap
	stripW := DispW(strip)
	pos := (m.scroll / marqueeColTicks) % (stripW + marqueePauseCol)
	off := 0
	if pos > marqueePauseCol {
		off = pos - marqueePauseCol
	}
	return dispWindow(strip+strip, off, w)
}

// metaLines renders the now-playing text: title, artist · album, and the
// technical format line (or a connecting/idle message). The track lines scroll
// as a marquee when they overflow w; the idle messages are clipped.
func (m *model) metaLines(s protocol.Snapshot, w int) []string {
	ps := m.sty.pens()
	t := s.Track
	if t == nil {
		msg := "connecting to LP10…"
		if s.Connected {
			msg = "nothing playing"
		}
		out := []string{ps.dim.render(Clip(msg, w))}
		switch {
		case s.Connected:
			out = append(out, ps.dmr.render(Clip("start something on Spotify / AirPlay / BT", w)))
		case s.Error != "":
			// disconnected: a calm reason under "connecting…", not a red bottom line
			out = append(out, ps.dmr.render(Clip(friendlyError(s.Error), w)))
		}
		return out
	}
	name := cmp.Or(t.Str("TrackName"), "—")
	second := t.Str("Artist")
	if al := t.Str("Album"); al != "" {
		if second != "" {
			second += " · " + al
		} else {
			second = al
		}
	}
	// Make the title and artist clickable (OSC 8) where the terminal supports
	// it — a degrades-to-plain enhancement, so it's always on. The link wraps
	// the fully styled+marqueed line so no later width math (DispW) ever sees
	// the URL bytes; downstream layout measures via lipgloss, which ignores it.
	// The source/format ("Spotify · Ogg · 44.1 kHz") rides the header row, not
	// here, so the now-playing block stays a tight two lines.
	artist := t.Str("Artist")
	trackLink := spotifySearch(strings.TrimSpace(name + " " + artist))
	secondLink := cmp.Or(spotifySearch(artist), spotifySearch(t.Str("Album")))
	return []string{
		osc8(trackLink, ps.bri.render(m.marquee(name, w))),
		osc8(secondLink, ps.dim.render(m.marquee(second, w))),
	}
}

// sourceStyle tints a source name in its brand colour (a small, tasteful accent
// in the otherwise teal/grey header), falling back to the theme accent.
func sourceStyle(t *theme, name string) lipgloss.Style {
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }
	switch name {
	case "Spotify":
		return fg("#1db954")
	case "TIDAL":
		return fg("#4fd4d4")
	case "AirPlay":
		return fg("#cfd6df")
	case "Bluetooth":
		return fg("#4a90d9")
	default:
		return t.sAcc
	}
}

// sourceFormat is the "Source · Mime · NN kHz" descriptor for a track (e.g.
// "Spotify · Ogg · 44.1 kHz"), or "" when nothing is playing.
func sourceFormat(t protocol.Track) string {
	if t == nil {
		return ""
	}
	var q []string
	if sn := SourceName(t); sn != "" {
		q = append(q, sn)
	}
	if ql := Quality(t); ql != "" {
		q = append(q, ql)
	}
	return strings.Join(q, " · ")
}

// osc8 wraps text in an OSC 8 hyperlink to url. Terminals that support
// hyperlinks (Ghostty, kitty, iTerm2, modern VTE) make the text clickable;
// others ignore the escape and show the text verbatim. The sequence is zero
// display-width to lipgloss/x-ansi, but NOT to DispW (which counts the URL
// bytes), so only ever apply it at the outermost layer, past all width math.
func osc8(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// spotifySearch builds an open.spotify.com search URL for query, or "" when the
// query is empty. Robust across sources (works for AirPlay/Bluetooth tracks too,
// where there's no Spotify URI), at the cost of landing on a search rather than
// the exact track.
func spotifySearch(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	return "https://open.spotify.com/search/" + url.PathEscape(query)
}

// transportSegments renders prev / play-pause / next as three equal-width (~33%)
// filled segments spanning width w, each with its label centred.
func (m *model) transportSegments(s protocol.Snapshot, now time.Time, w int) string {
	ps := m.sty.pens()
	segs := []struct{ action, label string }{
		{"prev", GL["rew"]}, {"toggle", toggleVerb(s)}, {"next", GL["ff"]},
	}
	pad, widths, gap := transportLayout(w)
	var b strings.Builder
	b.WriteString(spaces(pad))
	cluster := 0
	for i, sg := range segs {
		if i > 0 {
			b.WriteString(spaces(gap)) // horizontal gap between buttons
			cluster += gap
		}
		st := ps.segOff
		if (m.pane == paneNow && sg.action == actions[m.focus]) || m.flash[sg.action].After(now) {
			st = ps.segOn
		}
		cw := widths[i]
		lab := Clip(sg.label, cw)
		lw := DispW(lab)
		lp := (cw - lw) / 2
		b.WriteString(st.render(spaces(lp) + lab + spaces(cw-lw-lp)))
		cluster += cw
	}
	b.WriteString(spaces(w - cluster - pad))
	return b.String()
}

// transportLayout returns the leading pad, the three segment widths, and the gap
// between buttons for the transport cluster in a w-wide column. The buttons are a
// tidy centred cluster (capped, with leftover width padded on either side), and a
// small gap separates them so they read as three distinct buttons rather than one
// connected bar. Shared by transportSegments (rendering) and the mouse hit-zone
// builder so the two never disagree.
func transportLayout(w int) (pad int, widths []int, gap int) {
	const maxCluster = 52
	gap = transportGap
	cluster := min(w, maxCluster)
	btnTotal := cluster - gap*(len(actions)-1)
	if btnTotal < len(actions) { // too narrow for gaps: fall back to a solid cluster
		btnTotal, gap = cluster, 0
	}
	return (w - cluster) / 2, splitWidth(btnTotal, len(actions)), gap
}

// transportGap is the blank columns between transport buttons (horizontal separation).
const transportGap = 2

const (
	volColW   = 7  // width of the volume rail column
	artGap    = 2  // blank columns between the three player columns (art | mid | vol)
	coverHCap = 16 // max album-cover height (rows): a record sleeve, not a billboard
)

// fullMeta is the now-playing metadata for the full dashboard: title, artist, and
// album each on their own line (clickable + marqueed), so the smaller cover's freed
// width reads as a card. Falls back to metaLines' idle/connecting copy when nothing
// is playing. The compact view keeps metaLines' tighter two-line form.
func (m *model) fullMeta(s protocol.Snapshot, w int) []string {
	t := s.Track
	if t == nil {
		return m.metaLines(s, w)
	}
	ps := m.sty.pens()
	name := cmp.Or(t.Str("TrackName"), "—")
	artist := t.Str("Artist")
	out := []string{osc8(spotifySearch(strings.TrimSpace(name+" "+artist)),
		ps.bri.render(m.marquee(name, w)))}
	if artist != "" {
		out = append(out, osc8(spotifySearch(artist), ps.dim.render(m.marquee(artist, w))))
	}
	if album := t.Str("Album"); album != "" {
		out = append(out, osc8(spotifySearch(album), ps.dmr.render(m.marquee(album, w))))
	}
	return out
}

// fullSourceLine is the prominent source/format line in the full player: a
// brand-tinted dot + "Spotify · Ogg · 44.1 kHz · 2 ch". The source name wears its
// brand colour, the rest is dim. Returns "" when nothing is playing or there's no
// format to show; degrades to a dim clip when it can't fit w.
func (m *model) fullSourceLine(s protocol.Snapshot, w int) string {
	t := s.Track
	if t == nil {
		return ""
	}
	q := sourceFormat(t)
	if q == "" {
		return ""
	}
	ps := m.sty.pens()
	if ch := t.GetInt("ChannelCount"); ch > 0 {
		q += fmt.Sprintf(" · %d ch", ch)
	}
	plain := "● " + q
	if DispW(plain) > w { // too narrow: a plain dim clip keeps the width contract
		return ps.dmr.render(Clip(plain, w))
	}
	bp := ps.brandPen(SourceName(t))
	body := ps.dmr.render(q)
	if name := SourceName(t); name != "" && strings.HasPrefix(q, name) {
		body = bp.render(name) + ps.dmr.render(strings.TrimPrefix(q, name))
	}
	return bp.render("●") + " " + body
}

// volRailKey identifies a cached volume-rail block: everything the rail's
// pixels depend on. (lipgloss v2 renders profile-independently — downsampling
// happens in the program's renderer — so no profile rides in the key.)
type volRailKey struct {
	vol   int
	muted bool
	barH  int
}

// volRail renders the volume like an EQ band: a vertical bar barH squares tall
// with the value (percentage, or "muted") centred on the row below it. "Vol"
// labels it from the header; the m key toggles mute. Returns barH+1 lines.
// The block is cached on the model: it changes only when the volume does, so a
// steady volume costs a key compare per frame instead of barH ccell renders.
func (m *model) volRail(s protocol.Snapshot, barH int) []string {
	key := volRailKey{vol: s.Vol, muted: s.Muted, barH: barH}
	if m.volBlk == nil || m.volKey != key {
		m.volBlk, m.volKey = m.buildVolRail(s, barH), key
	}
	return m.volBlk
}

func (m *model) buildVolRail(s protocol.Snapshot, barH int) []string {
	rows := make([]string, 0, barH+1)
	if s.Muted {
		// Impossible to miss: a SOLID red column (not a faint hollow one that reads
		// as "volume happens to be 0") under a bold red MUTED badge. The header's
		// "Vol" label also flips to a red "MUTED" so it's caught from the top too.
		col := stRed.Render("█")
		for range barH {
			rows = append(rows, ccell(col, volColW))
		}
		return append(rows, ccell(stRed.Render("MUTED"), volColW))
	}
	for _, bl := range m.sty.vbar(float64(s.Vol)/100, barH) {
		rows = append(rows, ccell(bl, volColW))
	}
	return append(rows, ccell(m.sty.sDim.Render(fmt.Sprintf("%d%%", s.Vol)), volColW))
}

func (m *model) seekRow(s protocol.Snapshot, W int) string {
	t := s.Track
	playing := s.Playing == 0 && t != nil

	// A colour-coded STATE label owns play/pause prominence: a teal "Playing" while
	// playing, an amber "Paused" when paused. The transport toggle button is an
	// icon-free verb (play/pause), so the state indicator and the action label never
	// duel. Padded to a fixed width so the meter's start column doesn't jump on a
	// state change.
	ps := m.sty.pens()
	const statusW = 9 // DispW("▶ Playing")
	var status string
	switch {
	case playing:
		status = ps.accB.render(padDisp(GL["play"]+" Playing", statusW))
	case t != nil:
		status = ps.warnB.render(padDisp(GL["pause"]+" Paused", statusW))
	default:
		status = ps.dmr.render(padDisp(GL["pause"], statusW)) // idle: a quiet marker
	}

	total, pos := 0, s.Pos
	if t != nil {
		total = t.GetInt("TotalTime")
	} else {
		pos = 0 // nothing playing: don't bleed a stale elapsed time into the idle bar
	}
	cur := FmtMs(pos)
	rem := m.fmtRight(total, pos)
	cells := max(W-(statusW+1+DispW(cur)+1+1+DispW(rem)), 1)
	frac := 0.0
	if total > 0 {
		frac = float64(pos) / float64(total)
	}
	fillCells, headCell := ps.mFill, ps.mHead
	if m.amb != nil {
		m.amb.ensure() // the seek bar wears the album's colour
		fillCells, headCell = m.amb.mFill, m.amb.mHead
	}
	return status + " " + ps.dim.render(cur) + " " +
		lineMeterCells(frac, cells, fillCells, headCell, ps.mTrack) + " " + ps.dim.render(rem)
}

func (m *model) controlsRow(s protocol.Snapshot, now time.Time, W int, withVol bool) string {
	ps := m.sty.pens()
	btn := func(action, label string) (string, int) {
		st := ps.btnOff
		if (m.pane == paneNow && action == actions[m.focus]) || m.flash[action].After(now) {
			st = ps.btnOn
		}
		return st.render(label), DispW(label) + 2
	}
	pv, pvW := btn("prev", GL["rew"])
	tg, tgW := btn("toggle", toggleVerb(s))
	nx, nxW := btn("next", GL["ff"])
	left := pv + " " + tg + " " + nx
	leftW := pvW + 1 + tgW + 1 + nxW
	if !withVol {
		return left // volume is shown as a vertical band in the now-playing block
	}

	muteLbl := "mute"
	if s.Muted {
		muteLbl = "unmute"
	}
	volCells := 10
	volVal := fmt.Sprintf("%d%%", s.Vol)
	volPen, volLabel := ps.bri, ps.dmr
	if s.Muted {
		volVal, volPen = "MUTED", ps.red
	}
	mt, mtW := btn("mute", muteLbl)
	right := volLabel.render("vol") + " " + m.sty.lineMeter(float64(s.Vol)/100, volCells) + " " +
		volPen.render(volVal) + "  " + mt
	rightW := 3 + 1 + volCells + 1 + DispW(volVal) + 2 + mtW

	return between(left, leftW, right, rightW, W)
}

// dividerRow is a section separator: the label centred between two dim rules,
// "──── label ────", W cells wide, so the title reads as a heading.
func (m *model) dividerRow(label string, W int) string {
	ps := m.sty.pens()
	rule := max(W-DispW(label)-2, 0) // a space flanks the label on each side
	left := rule / 2
	bar := func(n int) string { return ps.dmr.render(strings.Repeat(GL["track"], n)) }
	return bar(left) + " " + ps.dim.render(label) + " " + bar(rule-left)
}

func (m *model) footerRow(W int) string {
	var hint string
	switch {
	case m.pane == paneEQ && m.eqSpec().Code == "MXV":
		// The one band with a device-wide gotcha (teardown §5.3): a low output cap
		// is why the remote / Spotify volume feels stuck near the top.
		hint = "Max Vol caps remote & Spotify volume · ←→ adjust · q quit"
	case m.pane == paneEQ:
		hint = "↑↓ pick · ←→ adjust · enter toggle · tab player · q quit"
	default:
		hint = "space play · ↑↓ vol · m mute · e/tab EQ · ? diag · q quit"
	}
	// Manual right-align. Safe from ccell's wrapping trap ONLY because Clip
	// bounds the content to ≤ W first — with that guarantee this is
	// byte-identical to lipgloss's Width(W).Align(Right) (probe-verified).
	clipped := Clip(hint, W)
	return spaces(W-DispW(clipped)) + m.sty.pens().dmr.render(clipped)
}

// toggleVerb is the transport toggle's icon-free action label: "pause" while
// playing (press to pause), "play" while paused or idle (press to play). It carries
// no play/pause glyph so it never duels with the colour-coded STATE shown on the
// seek row. Shared by transportSegments, controlsRow, and recordCompactZones so the
// rendered button and its hit-zone width never disagree.
func toggleVerb(s protocol.Snapshot) string {
	if s.Playing == 0 {
		return "pause"
	}
	return "play"
}
