package tui

import (
	"image/color"
	"math"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/lucasdaddiego/lp10/internal/artwork"
)

// theme holds the lipgloss styles and rendering primitives for the UI, built
// once per run. lipgloss downsamples the hex colors to the terminal's profile
// (256-color or, on a monochrome terminal, attribute-only — emphasis then falls
// back to the bold/reverse already baked into the prominent styles).
type theme struct {
	border color.Color

	sAcc lipgloss.Style // accent (teal)
	sBri lipgloss.Style // bright track title
	sTxt lipgloss.Style // body text
	sDim lipgloss.Style // dim metadata
	sDmr lipgloss.Style // dimmer hints

	fill  []lipgloss.Style // meter/bar gradient, dark -> bright
	track lipgloss.Style   // empty meter/bar cell
	head  lipgloss.Style   // meter position head

	warmKnob lipgloss.Style // EQ slider knob, tone boosted (> 0): gold
	coolKnob lipgloss.Style // EQ slider knob, tone cut (< 0): sky

	// sevs is the diagnostics severity palette — good / warn / bad — held as a
	// field so the gauges don't rebuild the triple on every row.
	sevs [3]lipgloss.Style

	// sFocusBU is the eqSummary focused-band style (accent+bold+underline). It
	// is deliberately NOT a pen: lipgloss renders underline styles rune-by-rune
	// (UnderlineSpaces handling), which a flattened prefix/suffix cannot mimic.
	sFocusBU lipgloss.Style

	trueColor     bool // terminal advertises 24-bit color (gates the half-block album art)
	kittyGraphics bool // terminal supports the Kitty graphics protocol (true-pixel album art)

	btnOn  lipgloss.Style // focused button
	btnOff lipgloss.Style // unfocused button
	segOn  lipgloss.Style // focused segmented (33%) transport button
	segOff lipgloss.Style // unfocused segmented transport button

	penCache *penSet // per-profile flattened styles; see pens()
}

func newTheme() *theme {
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }
	t := &theme{border: lipgloss.Color("#4a5562")}
	t.sAcc = fg("#34d9ad")
	t.sBri = fg("#f0f2f5").Bold(true)
	t.sTxt = fg("#d7dbe2")
	t.sDim = fg("#6b7480")
	t.sDmr = fg("#515863")
	for _, h := range []string{"#157a63", "#1d9e75", "#2bbf94", "#34d9ad", "#5fe0bf"} {
		t.fill = append(t.fill, fg(h))
	}
	t.track = fg("#3a4150") // empty meter/rail cells: a visible grey, not near-black
	t.head = fg("#8af0d4")
	// Tone colours for the graphic-EQ slider knob: a boosted band reads warm, a cut
	// band reads cool — so the sign of a tone control is legible at a glance.
	t.warmKnob, t.coolKnob = fg("#ffc861"), fg("#86b6ff")
	t.sevs = [3]lipgloss.Style{t.sAcc, stWarn, stRed}
	t.sFocusBU = t.sAcc.Bold(true).Underline(true)
	// colorprofile replaces v1's termenv: same env-driven detection (COLORTERM /
	// TERM / NO_COLOR), used only to gate the half-block album art — everything
	// else renders 24-bit and lets the program's renderer downsample.
	t.trueColor = colorprofile.Detect(os.Stdout, os.Environ()) == colorprofile.TrueColor
	t.kittyGraphics = detectKittyGraphics()
	t.btnOn = lipgloss.NewStyle().Foreground(lipgloss.Color("#06231b")).Background(lipgloss.Color("#34d9ad")).Bold(true).Padding(0, 1)
	t.btnOff = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b7480")).Padding(0, 1)
	t.segOn = lipgloss.NewStyle().Foreground(lipgloss.Color("#06231b")).Background(lipgloss.Color("#34d9ad")).Bold(true)
	t.segOff = lipgloss.NewStyle().Foreground(lipgloss.Color("#aab3c0")).Background(lipgloss.Color("#1c222c"))
	return t
}

// detectKittyGraphics reports whether the terminal is known to support the Kitty
// graphics protocol with Unicode placeholders. There's no in-band capability
// query before Bubble Tea seizes the terminal, so this goes by environment
// fingerprint: Ghostty and kitty both implement it; everything else falls back
// to the half-block raster. A false negative just means half-blocks (still real
// art); kitty/auto can be forced via the art_mode config.
func detectKittyGraphics() bool {
	// A multiplexer inherits the host terminal's env (KITTY_WINDOW_ID, GHOSTTY_*)
	// but doesn't pass the graphics protocol through, so don't trust those vars
	// under tmux/screen — the half-block raster renders correctly there.
	if os.Getenv("TMUX") != "" {
		return false
	}
	if t := os.Getenv("TERM"); strings.HasPrefix(t, "screen") || strings.HasPrefix(t, "tmux") {
		return false
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" || os.Getenv("GHOSTTY_BIN_DIR") != "" {
		return true
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "kitty":
		return true
	}
	term := strings.ToLower(os.Getenv("TERM"))
	return strings.Contains(term, "kitty") || strings.Contains(term, "ghostty")
}

// ---- per-cell painting ---------------------------------------------------------
//
// The block-drawing primitives below (the plasma motif, the searching arcs,
// the meters and the EQ bars) paint one styled glyph per character cell, and the
// animated ones repaint every frame at ~30fps. Calling lipgloss.Style.Render per
// cell costs roughly 25 allocations — a Style copy, a hex parse via go-colorful
// (which uses fmt.Sscanf), a colour-profile conversion and an ANSI parse — which
// made the motif alone 65% of the whole process's allocations and put the GC
// (runtime.madvise) at ~68% of CPU.
//
// Where a cell's style comes from a small fixed set (the meters, the EQ bars) the
// fix is just to render each distinct glyph ONCE and index the results. Only the
// motif — whose colour genuinely differs per cell — and the two-tone searching
// arcs need the painter below.

// paintCell / paintEndLine paint glyphs whose colour differs every cell,
// writing the 24-bit SGR directly and resetting once per line instead of once
// per cell (the same shape artwork.HalfBlock already ships). Under
// lipgloss/bubbletea v2 this is universally correct: styles always emit the
// colour as specified and the program's renderer downsamples for the actual
// terminal (v1 needed a profile-gated lipgloss fallback here).
// TestPaintedBlockWidths pins the one property the layout depends on: a line
// is still exactly w columns.
func paintCell(b *strings.Builder, r, g, bl uint8, glyph string) {
	b.WriteString("\x1b[38;2;")
	writeDec(b, r)
	b.WriteByte(';')
	writeDec(b, g)
	b.WriteByte(';')
	writeDec(b, bl)
	b.WriteByte('m')
	b.WriteString(glyph)
}

// paintEndLine closes a painted line: the foreground stays set as the cells
// run, so one reset ends the line (an empty line needs none).
func paintEndLine(b *strings.Builder) {
	if b.Len() > 0 {
		b.WriteString("\x1b[0m")
	}
}

// writeDec appends a byte in decimal without allocating (strconv would need a
// scratch slice per call, and this runs once per colour channel per cell).
func writeDec(b *strings.Builder, v uint8) {
	if v >= 100 {
		b.WriteByte('0' + v/100)
	}
	if v >= 10 {
		b.WriteByte('0' + v/10%10)
	}
	b.WriteByte('0' + v%10)
}

// ---- flattened styles (pens) -----------------------------------------------------
//
// A lipgloss.Style.Render costs ~15 heap allocations even for a plain foreground
// style: the style is copied, its hex colour is re-PARSED (go-colorful.Hex uses
// fmt.Sscanf) and converted for the profile, and the output re-tokenised. The
// dashboard makes ~200 such calls per frame at up to 30fps, which made this
// chain the top allocation source once the per-cell loops were fixed.
//
// A pen is a style flattened to the escape pair it wraps a SINGLE-LINE string
// in, derived by rendering a sentinel once. pen.render(s) is then two concats
// and is byte-identical to Style.Render(s) for any single-line s. Two style
// classes are NOT pen-safe and must stay on Style.Render: multi-line strings
// (lipgloss styles each line separately) and Underline/Strikethrough styles
// (lipgloss renders those rune-by-rune to keep the decoration off spaces — the
// eqSummary focus style, sFocusBU, is the one such style here).
// TestPenMatchesStyleRender pins the equivalence for every pen below.
// lipgloss v2 renders profile-independently (the program's renderer does the
// downsampling), so one flattening serves the whole process.

// pen is a flattened style: the SGR prefix and reset suffix it wraps text in.
type pen struct{ pre, post string }

func (p pen) render(s string) string { return p.pre + s + p.post }

// stylePen flattens a style by rendering a sentinel that cannot occur inside an
// escape sequence. It costs one Render — do it once and cache, never per call.
func stylePen(s lipgloss.Style) pen {
	const sentinel = "\x00"
	r := s.Render(sentinel)
	before, after, ok := strings.Cut(r, sentinel)
	if !ok {
		return pen{} // sentinel swallowed (never observed): degrade to unstyled
	}
	return pen{before, after}
}

// penSet is every pen and pre-rendered meter cell the per-frame render path
// needs. Built lazily, once, by theme.pens().
type penSet struct {
	acc, accB          pen // accent; accent bold
	bri, txt, dim, dmr pen
	warn, warnB, red   pen
	segOn, segOff      pen
	btnOn, btnOff      pen
	warmKnob, coolKnob pen
	border             pen

	brand map[string]pen // per-source tints (sourceStyle), keyed by source name

	// meter cells, pre-rendered: one styled glyph per ramp entry / rail part.
	mFill  []string // "━" per fill ramp entry (seek/volume meters)
	mHead  string   // "●" meter head
	mTrack string   // "─" empty meter cell

	// the header's volume-rail label, centred in its column — one lipgloss
	// Width/Align render per process instead of one per frame.
	volCell   string // "Vol" (dim)
	mutedCell string // "MUTED" (alarm red)
}

// pens returns the flattened styles, building them on first use.
func (t *theme) pens() *penSet {
	if t.penCache == nil {
		ps := &penSet{
			acc:      stylePen(t.sAcc),
			accB:     stylePen(t.sAcc.Bold(true)),
			bri:      stylePen(t.sBri),
			txt:      stylePen(t.sTxt),
			dim:      stylePen(t.sDim),
			dmr:      stylePen(t.sDmr),
			warn:     stylePen(stWarn),
			warnB:    stylePen(stWarn.Bold(true)),
			red:      stylePen(stRed),
			segOn:    stylePen(t.segOn),
			segOff:   stylePen(t.segOff),
			btnOn:    stylePen(t.btnOn),
			btnOff:   stylePen(t.btnOff),
			warmKnob: stylePen(t.warmKnob),
			coolKnob: stylePen(t.coolKnob),
			border:   stylePen(lipgloss.NewStyle().Foreground(t.border)),
			mHead:    t.head.Render("●"),
			mTrack:   t.track.Render("─"),
		}
		ps.brand = make(map[string]pen, len(brandNames))
		for _, n := range brandNames {
			ps.brand[n] = stylePen(sourceStyle(t, n))
		}
		ps.mFill = make([]string, len(t.fill))
		for i := range t.fill {
			ps.mFill[i] = t.fill[i].Render("━")
		}
		ps.volCell = ccell(ps.dim.render("Vol"), volColW)
		ps.mutedCell = ccell(ps.red.render("MUTED"), volColW)
		t.penCache = ps
	}
	return t.penCache
}

// brandNames are the source names sourceStyle tints; the penSet pre-flattens one
// pen per name so the header/source line never rebuilds a style per frame.
var brandNames = []string{"Spotify", "TIDAL", "AirPlay", "Bluetooth"}

// brandPen is the flattened sourceStyle for a source name (accent for unknowns).
func (ps *penSet) brandPen(name string) pen {
	if p, ok := ps.brand[name]; ok {
		return p
	}
	return ps.acc
}

// ambientTint is a per-album recolouring derived from the cover's dominant hue:
// a fill gradient + head for the seek bar, plus a dim pen for the cover frame.
// nil means "use the theme defaults" (no cover, a greyscale cover, or art
// disabled). The connected status dot deliberately stays the theme green — it's a
// status light, so it must not drift to an album hue that reads as a warning.
type ambientTint struct {
	fill  []lipgloss.Style
	head  lipgloss.Style
	frame lipgloss.Style

	// flattened forms for the per-frame paths (seek bar, cover frame), built
	// once by ensure() — the ambient analogue of the theme's penSet.
	mFill    []string
	mHead    string
	framePen pen
}

// ensure builds the tint's cached cells on first use.
func (at *ambientTint) ensure() {
	if at.mHead != "" {
		return
	}
	at.mHead = at.head.Render("●")
	at.framePen = stylePen(at.frame)
	at.mFill = make([]string, len(at.fill))
	for i := range at.fill {
		at.mFill[i] = at.fill[i].Render("━")
	}
}

// tint derives an ambientTint from a cover's representative colour c. Only the
// hue (and a clamped saturation) ride along from c; lightness is swept across a
// fixed dark→bright ramp, so the seek bar keeps the same readable contrast as the
// default teal — just in the album's colour. Saturation is floored so even a
// muted cover still reads as tinted, and ceilinged so a neon cover never glares.
func (t *theme) tint(c color.RGBA) *ambientTint {
	h, s, _ := artwork.RGBToHSL(c.R, c.G, c.B)
	s = clampRange(s, 0.35, 0.85)
	pen := func(h, s, l float64) lipgloss.Style {
		// lipgloss v2 takes a color.Color directly — no hex string round-trip.
		pr, pg, pb := hslRGB(h, s, l)
		return lipgloss.NewStyle().Foreground(color.RGBA{R: pr, G: pg, B: pb, A: 0xff})
	}
	at := &ambientTint{
		head:  pen(h, math.Min(s+0.1, 1), 0.78),
		frame: pen(h, s*0.55, 0.44),
	}
	for _, l := range []float64{0.26, 0.34, 0.44, 0.55, 0.68} {
		at.fill = append(at.fill, pen(h, s, l))
	}
	return at
}

func clampRange(v, lo, hi float64) float64 { return max(lo, min(hi, v)) }

// rampIdx maps a position within span onto an index into an n-entry ramp, so
// the meter loops index a pre-rendered ramp of cells rather than re-rendering a
// ramp of styles per cell.
func rampIdx(n, pos, span int) int {
	r := 0.0
	if span > 1 {
		r = float64(pos) / float64(span-1)
	}
	i := int(r*float64(n-1) + 0.5)
	if i < 0 {
		i = 0
	} else if i >= n {
		i = n - 1
	}
	return i
}

// motifBlock renders a w×h animated "plasma" of block cells. A gentle domain warp
// braids the field like a lava lamp; the hue is a smooth low-spatial-frequency
// gradient — adjacent cells differ by only a few degrees, a continuous flow rather
// than confetti — plus a slow global rotation that cycles the whole spectrum over
// time. Brightness is layered sines. Advanced by frame (a frozen frame yields a
// still image, e.g. while paused). 24-bit truecolor; lipgloss downsamples on lesser
// terminals.
func (t *theme) motifBlock(w, h, frame int) []string {
	ph := float64(frame) * 0.16
	const warpAmp = 0.9
	// The warp field is SEPARABLE: each of wx/wy is a row term plus a column
	// term, so precompute w+h sines instead of 2·w·h. The remaining five sines
	// per cell go through fastSin — the motif rebuilds every animation frame,
	// and trig was ~2/3 of its cost; TestMotifMatchesMathSin bounds the error
	// at ≤1/255 per channel (in practice the bytes come out identical).
	rowWX := make([]float64, h) // sin(fy·0.30 + ph·0.6)
	rowWY := make([]float64, h) // 0.5·sin(fy·0.24 + ph·0.45)
	colWX := make([]float64, w) // 0.5·sin(fx·0.22 − ph·0.35)
	colWY := make([]float64, w) // sin(fx·0.27 − ph·0.5)
	for y := range h {
		fy := float64(y)
		rowWX[y] = fastSin(fy*0.30 + ph*0.6)
		rowWY[y] = 0.5 * fastSin(fy*0.24+ph*0.45)
	}
	for x := range w {
		fx := float64(x)
		colWX[x] = 0.5 * fastSin(fx*0.22-ph*0.35)
		colWY[x] = fastSin(fx*0.27 - ph*0.5)
	}
	lines := make([]string, h)
	var b strings.Builder
	for y := range h {
		fy := float64(y)
		b.Reset() // Reset drops the buffer rather than reusing it, so the string handed out below stays valid
		b.Grow(w*cellSGRBytes + resetBytes)
		for x := range w {
			fx := float64(x)
			// gentle low-frequency vector warp so the bands braid organically;
			// small coeffs + amp keep ax,ay within ~1 cell of fx,fy (stays smooth)
			ax := fx + warpAmp*(rowWX[y]+colWX[x])
			ay := fy + warpAmp*(colWY[x]+rowWY[y])
			// brightness plasma on the warped coords; v in [-3,3] -> n in [0,1]
			v := fastSin(ax*0.55+ph) + fastSin(ay*0.75-ph*0.8) + fastSin((ax+ay)*0.42+ph*1.3)
			n := (v + 3) / 6
			// hue: global spectrum rotation + a broad spatial gradient + a swirl,
			// all continuous (no random term) so neighbours stay within a few °
			hue := math.Mod(ph*57.29578+
				74*fastSin(ax*0.20+ay*0.16+ph*0.5)+
				24*fastSin((ax-ay)*0.17-ph*0.35)+360, 360)
			r, g, bl := hslRGB(hue, 0.70+0.18*n, 0.20+0.40*n)
			paintCell(&b, r, g, bl, "█")
		}
		paintEndLine(&b)
		lines[y] = b.String()
	}
	return lines
}

// fastSin approximates math.Sin with a linearly interpolated 4096-entry table.
// Max error ≈ 2.9e-7 — three orders of magnitude below the 1/255 channel
// quantization in hslRGB/to8, so the painted bytes are (near-)identical to the
// math.Sin ones; TestMotifMatchesMathSin holds the line at ≤1/255 per channel.
// Used only by the plasma motif, where 5 sines/cell × ~500 cells × 30fps made
// trig the dominant cost of an animated frame.
func fastSin(x float64) float64 {
	t := x * (1 / (2 * math.Pi))
	t -= math.Floor(t) // wrap into [0,1)
	f := t * sinLUTSize
	i := int(f)
	return sinLUT[i] + (sinLUT[i+1]-sinLUT[i])*(f-float64(i))
}

const sinLUTSize = 4096

// sinLUT spans one full period with a duplicated endpoint so the interpolation
// never wraps its index.
var sinLUT = func() [sinLUTSize + 1]float64 {
	var l [sinLUTSize + 1]float64
	for i := range l {
		l[i] = math.Sin(float64(i) * (2 * math.Pi / sinLUTSize))
	}
	return l
}()

// Sizing hints for the painted-line buffers: "\x1b[38;2;R;G;Bm" plus a 3-byte
// block glyph is at most 22 bytes, and the line's single trailing reset is 4.
const (
	cellSGRBytes = 22
	resetBytes   = 4
)

// searchBox fills the idle cover slot while (re)connecting: a cast-style
// "looking for the speaker" figure — a bright dot with three arc pairs that
// light up outward one per tick over an always-dim track, so the pulse reads
// as reaching out for the device — with a dim "searching for LP10…" label two
// rows below (three when that balances the box — see the parity note in the
// body). In the app's teal. The figure degrades with the box: spaced
// arcs, then tight arcs, then the bare dot; the label drops when it can't fit
// (narrow box or h<3). Arcs and label are ASCII (width-1 everywhere); only
// the dot is East-Asian-Ambiguous, so it falls back under a CJK locale like
// the GL glyphs do. Each line is exactly w display columns.
func (t *theme) searchBox(w, h, frame int) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	arcs, sep := 3, " "
	switch {
	case w >= 13: // ( ( ( ● ) ) )
	case w >= 7: // (((●)))
		sep = ""
	default: // just the beacon dot
		arcs, sep = 0, ""
	}
	figW := 1 + 2*arcs*(1+len(sep))
	dot := "●"
	if localeAmb == 2 {
		dot = "*"
	}
	briR, briG, briB := hslRGB(168, 0.70, 0.75) // the lit arcs and the dot
	dimR, dimG, dimB := hslRGB(168, 0.40, 0.30) // the waiting track
	lit := frame % 4                            // 0 (dot only) → 3 (fully reached out), then rest
	var fig strings.Builder
	fig.Grow(figW*cellSGRBytes + resetBytes)
	for ring := arcs; ring >= 1; ring-- {
		if ring <= lit {
			paintCell(&fig, briR, briG, briB, "(")
		} else {
			paintCell(&fig, dimR, dimG, dimB, "(")
		}
		fig.WriteString(sep)
	}
	paintCell(&fig, briR, briG, briB, dot)
	for ring := 1; ring <= arcs; ring++ {
		fig.WriteString(sep)
		if ring <= lit {
			paintCell(&fig, briR, briG, briB, ")")
		} else {
			paintCell(&fig, dimR, dimG, dimB, ")")
		}
	}
	paintEndLine(&fig)

	out := make([]string, h)
	blank := spaces(w)
	for i := range out {
		out[i] = blank
	}
	col := (w - figW) / 2
	figLine := spaces(col) + fig.String() + spaces(w-col-figW)
	label := "searching for LP10" + GL["ell"]
	if lw := DispW(label); h >= 3 && lw <= w {
		// The arcs↔label gap stretches one extra row whenever that makes the
		// block's height parity match the box's, so the space above the arcs
		// always EQUALS the space below the label. (A fixed 3-row block in an
		// even-height box sat half a row high, which — compounded with the
		// unavoidable half-cell horizontal parity bias — read as drifting
		// toward the top-left corner.)
		e := (h - 3) % 2
		top := (h - 3 - e) / 2
		out[top] = figLine
		lcol := (w - lw) / 2
		out[top+2+e] = spaces(lcol) + t.pens().dim.render(label) + spaces(w-lcol-lw)
	} else {
		out[(h-1)/2] = figLine
	}
	return out
}

// hslRGB converts HSL (h in degrees, s and l in 0..1) to 8-bit RGB, feeding the
// per-cell painters and the ambient tint (as a color.RGBA) directly.
func hslRGB(h, s, l float64) (uint8, uint8, uint8) {
	c := (1 - math.Abs(2*l-1)) * s
	hp := math.Mod(h/60, 6)
	if hp < 0 {
		hp += 6
	}
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, bl float64
	switch int(hp) {
	case 0:
		r, g, bl = c, x, 0
	case 1:
		r, g, bl = x, c, 0
	case 2:
		r, g, bl = 0, c, x
	case 3:
		r, g, bl = 0, x, c
	case 4:
		r, g, bl = x, 0, c
	default:
		r, g, bl = c, 0, x
	}
	m := l - c/2
	return to8(r + m), to8(g + m), to8(bl + m)
}

// to8 maps a 0..1 channel to a clamped 0..255 byte.
func to8(v float64) uint8 { return uint8(max(0, min(255, int(v*255+0.5)))) }

// lineMeter renders a horizontal meter cells wide: a gradient filled run, a
// bright head, then a dim track — used for the seek and volume bars. The fill
// ramps over the *played* length (not the whole bar), so it darkens at the start
// and brightens up to the playhead regardless of progress.
func (t *theme) lineMeter(frac float64, cells int) string {
	ps := t.pens()
	return lineMeterCells(frac, cells, ps.mFill, ps.mHead, ps.mTrack)
}

// lineMeterCells is the meter core over pre-rendered cells: each position is one
// of the fill ramp, the head, or the track, so a bar costs one Builder — no
// styling work at all.
func lineMeterCells(frac float64, cells int, fillCells []string, headCell, trackCell string) string {
	if cells <= 0 {
		return ""
	}
	frac = clampF(frac)
	h := int(math.Round(frac * float64(cells)))
	var b strings.Builder
	b.Grow(cells * cellSGRBytes)
	for i := range cells {
		switch {
		case i == h-1 || (h == 0 && i == 0):
			b.WriteString(headCell)
		case i < h-1:
			b.WriteString(fillCells[rampIdx(len(fillCells), i, h)])
		default:
			b.WriteString(trackCell)
		}
	}
	return b.String()
}

// gaugeBar renders a horizontal LINE meter cells wide — a heavy rule (GL["fill"]
// "━") in the health colour over a light rule (GL["track"] "─") in dim grey, like
// the seek bar and EQ sliders. A thin centred line (rather than a full-height
// block) keeps vertically-stacked gauges from merging into one solid region, and
// the heavy/light weight difference still distinguishes fill from track on a
// no-colour terminal. The caller picks the fill colour for health.
func (t *theme) gaugeBar(frac float64, cells int, fillPen lipgloss.Style) string {
	frac = clampF(frac)
	n := int(math.Round(frac * float64(cells)))
	fillCell, trackCell := fillPen.Render(GL["fill"]), t.track.Render(GL["track"])
	var b strings.Builder
	b.Grow(cells * cellSGRBytes)
	for i := range cells {
		if i < n {
			b.WriteString(fillCell)
		} else {
			b.WriteString(trackCell)
		}
	}
	return b.String()
}

// vbar renders a vertical bar h rows tall (top line first) for a graphic-EQ
// band: filled from the bottom, brighter toward the top of the fill.
func (t *theme) vbar(frac float64, h int) []string {
	frac = clampF(frac)
	filled := int(math.Round(frac * float64(h)))
	trackCell := t.track.Render("▓") // a visible grey channel, not a faint ░
	var fillCells []string
	if filled > 0 {
		fillCells = make([]string, len(t.fill))
		for i := range t.fill {
			fillCells[i] = t.fill[i].Render("█")
		}
	}
	lines := make([]string, h)
	for row := range h {
		fromBottom := h - 1 - row
		if fromBottom < filled {
			lines[row] = fillCells[rampIdx(len(t.fill), fromBottom, h)]
		} else {
			lines[row] = trackCell
		}
	}
	return lines
}

func clampF(f float64) float64 { return clampRange(f, 0, 1) }

// Shared alert tokens: the warn amber and the bold alarm red used across the
// player, EQ, and diagnostics views (header states, MUTED, error lines).
var (
	stWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0b34d"))
	stRed  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e2655f")).Bold(true)
)
