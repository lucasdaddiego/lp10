// The Bubble Tea model: its fields (controller + render state), construction,
// and the terminal-geometry probe. The message loop lives in update.go, key
// dispatch in keys.go, rendering in view.go / eq.go / diag.go / art.go.

package tui

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// Display/timing constants.
const (
	FlashDuration        = 350 * time.Millisecond
	ErrorDisplayDuration = 4 * time.Second
	MaxTitleLength       = 120

	// diagErrWindow is how long the diagnostics overlay keeps showing a
	// transient error after it was recorded (age-stamped). Longer than the
	// dashboard's ErrorDisplayDuration — the overlay is where one goes to
	// investigate AFTER the flash — but bounded, so a long-recovered hiccup
	// can't sit under a healthy masthead reading as a live fault. Fatal errors
	// are exempt: they are current state, latched until data flows again.
	diagErrWindow = 60 * time.Second

	// StatsReassertTicks re-sends the "stats on" signal while the diagnostics
	// overlay is open (30 ticks × 100ms ≈ 3s), so the device — which resets the
	// flag on every reconnect — resumes emitting @@s within a few seconds of a
	// dropped/restored connection. Kept under CommandDeadline so it stays fresh.
	StatsReassertTicks = 30

	// Layout thresholds (rows × cols). Below mini -> one frameless line; below
	// the full size -> a compact dashboard with no art and a one-line EQ.
	MiniRows = 9
	MiniCols = 58
	FullRows = 25 // full dashboard (art + graphic EQ + volume slider) needs the height
	FullCols = 70 // 7 EQ bands need ≥9 cols each so "Deep Bass" fits its column
)

// actions is the focusable transport-button order in the now-playing pane.
var actions = []string{"prev", "toggle", "next"}

// pane identifiers (which half of the dashboard has focus).
const (
	paneNow = 0 // now-playing transport
	paneEQ  = 1 // equalizer strip
)

// model is the Bubble Tea model: controller logic plus render state.
type model struct {
	st     *protocol.State
	cfg    config.Config
	cmds   chan *protocol.Command
	eqcmds chan workers.EQCommand
	// Persistence belongs to the controller/runtime, not protocol.State.
	premutePath string

	focus         int  // transport-button focus (index into actions)
	pane          int  // paneNow | paneEQ
	eqFocus       int  // EQ-strip display position (index into eqOrder)
	frame         int  // animation frame for the art motif (advances while playing)
	motifLive     bool // the plasma motif was actually drawn last render (gates the fast frame tick)
	searchLive    bool // the connecting search figure was drawn last render (keeps the frame clock ticking while idle)
	scroll        int  // tick counter driving the now-playing marquee (advances every tick)
	diag          bool
	showRemaining bool
	flash         map[string]time.Time

	rows, cols   int
	cellW, cellH int // terminal cell size in device px (0 if unknown); sizes the Kitty cover
	curTitle     string

	// statsOn tracks whether the device has been told to emit resource stats
	// (@@s), so it runs only while the diagnostics overlay is open; statsTicks
	// counts down to the next keep-alive re-assert.
	statsOn    bool
	statsTicks int

	// motif cache: the plasma is a pure function of (w,h,frame), so a frozen
	// frame (paused/idle) or any non-tick re-render reuses the last block
	// instead of rebuilding 72 styled cells ~10x/sec. Byte-identical output.
	motifBlk []string
	motifKey [3]int // w, h, frame the cache was built for

	// album-art cache: rasterizing the cover to half-blocks is keyed by
	// (url,w,h), so a steady cover reuses the last raster rather than
	// re-rasterizing every frame. Cleared implicitly when the key changes.
	artBlk []string
	artKey artKey

	// ghost-cover cache: the dimmed last cover shown in the idle slot, keyed like
	// artBlk but in its own field so it never collides with the live cover.
	ghostBlk []string
	ghostKey artKey

	// kittyTx holds the pending Kitty transmit escape (APC) for a just-(re)built
	// cover raster. The bubbletea v2 cell renderer parses View content into a
	// cell grid and drops escape sequences it doesn't model, so the transmit
	// can't ride the first art line as it did under v1 — the placeholder cells
	// survive (plain graphemes plus an SGR foreground) but the image payload
	// would be silently swallowed, leaving an empty cover box. artColumn /
	// ghostCover stash the escape here and Update flushes it out-of-band via
	// tea.Raw on the next message (≤100ms, the tick heartbeat). Ordering is
	// safe either way: placeholder cells composite whenever an image with
	// their id exists, whether transmitted before or after they were drawn.
	kittyTx string

	// volume-rail cache: the rail repaints only when the volume/mute/height
	// change (see volRail), not on every animated frame.
	volBlk []string
	volKey volRailKey

	// ambient tint: the seek bar / cover frame / status dot recoloured to the
	// current cover's dominant hue. amb is nil for the theme default (no cover,
	// greyscale art, or art disabled); ambKey is the CoverURL it was computed for
	// (recompute only on a cover change, including a deliberate nil result).
	amb    *ambientTint
	ambKey string

	interrupted bool // Ctrl-C, so Run can exit 130 like Python's KeyboardInterrupt

	sty *theme
}

func newModel(st *protocol.State, cfg config.Config, cmds chan *protocol.Command, eqcmds chan workers.EQCommand) *model {
	m := &model{
		st: st, cfg: cfg, cmds: cmds, eqcmds: eqcmds,
		premutePath:   config.PremutePath(cfg),
		focus:         1,
		showRemaining: true,
		flash:         map[string]time.Time{},
	}
	m.cellW, m.cellH = cellPixelSize() // refreshed on every resize; sizes the Kitty cover
	// The window title rides every tea.View, so seed it before the first frame —
	// otherwise the opening frames would carry an empty title until the first
	// logic tick recomputes it.
	m.curTitle = m.computeTitle(st.Snap())
	return m
}

// cellPixelSize reports the terminal's cell size in device pixels (width, height)
// via TIOCGWINSZ, or (0, 0) when the terminal doesn't report pixel dimensions (or
// stdout isn't a tty, e.g. in tests). The Kitty cover path sizes its image to the
// cover's exact pixel footprint, since a virtual placement is drawn at the image's
// native resolution rather than scaled to the cell box.
func cellPixelSize() (w, h int) {
	var ws struct{ rows, cols, xpix, ypix uint16 }
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return 0, 0
	}
	if ws.cols == 0 || ws.rows == 0 || ws.xpix == 0 || ws.ypix == 0 {
		return 0, 0
	}
	return int(ws.xpix) / int(ws.cols), int(ws.ypix) / int(ws.rows)
}
