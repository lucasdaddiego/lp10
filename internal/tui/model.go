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
	"github.com/lucasdaddiego/lp10/internal/sweep"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// Display/timing constants.
const (
	FlashDuration        = 350 * time.Millisecond
	ErrorDisplayDuration = 4 * time.Second
	MaxTitleLength       = 120

	// diagErrWindow is how long the diagnostics keep showing a transient
	// error after it was recorded (age-stamped). Longer than the dashboard's
	// ErrorDisplayDuration — the diagnostics are where one goes to
	// investigate AFTER the flash — but bounded, so a long-recovered hiccup
	// can't sit under a healthy masthead reading as a live fault.
	diagErrWindow = 60 * time.Second

	// Layout thresholds (rows × cols). Below mini -> one frameless line; below
	// the full size -> a compact player with no art and no volume rail.
	MiniRows = 9
	MiniCols = 58
	FullRows = 25 // full dashboard (the framed cover beside the volume rail) needs the height
	FullCols = 70 // the cover, a usable metadata column and the volume rail side by side
)

// actions is the focusable transport-button order in the now-playing pane.
var actions = []string{"prev", "toggle", "next"}

// view is which screen the frame shows. Exactly one is up at a time: the
// player, or one of the two others reached by number, letter or tab — and
// the help page behind ?. The header's view strip names them in this order.
type view int

const (
	viewPlayer view = iota
	viewEQ
	viewDiag
	viewHelp
)

// numberedViews is how many views the 1-3 keys (and tab) reach; help sits
// outside the cycle and behind ? alone.
const numberedViews = 3

// viewNames labels the view strip, in view order.
var viewNames = [...]string{"player", "equalizer", "diagnostics", "help"}

// miniMode reports whether the terminal is too small for the dashboard, so only
// the one-line mini view renders (no EQ pane). Key dispatch and syncViews
// consult this (the view has its own rows==0 guard), so before the first
// WindowSizeMsg it reports mini: nothing is drawn yet, and scripted input racing
// startup (`tmux send-keys "e" Left`) must not adjust an invisible equalizer
// through the gap.
func (m *model) miniMode() bool {
	return m.rows < MiniRows || m.cols < MiniCols
}

// model is the Bubble Tea model: controller logic plus render state.
type model struct {
	st   *protocol.State
	cfg  config.Config
	cmds chan workers.Command

	focus int  // transport-button focus (index into actions)
	view  view // the screen on show (viewPlayer … viewHelp)

	// the notice line (notice.go): what it says, until when, and in which pen
	notice      string
	noticeUntil time.Time
	noticeWarn  bool
	// connection tracking for the notices: the last seen state, whether a
	// connection ever came up this run, and when the connect summary is due
	wasConnected  bool
	connectedOnce bool
	summaryDue    time.Time

	// diagScroll is the diagnostics read-out's scroll offset (rows) when it is
	// taller than the frame; the render clamps it
	diagScroll int

	// baseline is the last `lp10 sweep` (nil: none saved); the diagnostics
	// name what has moved since it
	baseline *sweep.Report

	// theme selection: the terminal's background as reported (nil until it
	// answers), and whether the palette was built for a dark background
	bgDark     *bool
	themeDark  bool
	eqFocus    int  // EQ-strip display position (index into eqOrder)
	eqScroll   int  // first slider row drawn when the frame is too short for all nine (see eqWindow)
	frame      int  // animation frame for the art motif (advances while playing)
	motifLive  bool // the plasma motif was actually drawn last render (gates the fast frame tick)
	searchLive bool // the connecting search figure was drawn last render (keeps the frame clock ticking while idle)
	scroll     int  // tick counter driving the now-playing marquee (advances every tick)
	flash      map[string]time.Time

	// sleep timer (sleep.go): sleepAt is the host-side deadline at which the
	// logic tick pauses playback (zero == off); sleepPreset is the index into
	// sleepPresets the 's' key last armed, so repeated presses step upward.
	sleepAt     time.Time
	sleepPreset int

	rows, cols   int
	cellW, cellH int // terminal cell size in device px (0 if unknown); squares the art box
	curTitle     string

	// motif cache: the plasma is a pure function of (w,h,frame), so a frozen
	// frame (paused/idle) or any non-tick re-render reuses the last block
	// instead of rebuilding 72 styled cells ~10x/sec. Byte-identical output.
	motifBlk []string
	motifKey [3]int // w, h, frame the cache was built for

	// volume-rail cache: the rail repaints only when the volume/mute/height
	// change (see volRail), not on every animated frame.
	volBlk []string
	volKey volRailKey

	interrupted bool // Ctrl-C, so Run can exit 130 (128 + SIGINT)

	sty *theme
}

func newModel(st *protocol.State, cfg config.Config, cmds chan workers.Command) *model {
	m := &model{
		st: st, cfg: cfg, cmds: cmds,
		focus: 1,
		flash: map[string]time.Time{},
	}
	m.cellW, m.cellH = cellPixelSize() // refreshed on every resize; squares the art box
	// The window title rides every tea.View, so seed it before the first frame —
	// otherwise the opening frames would carry an empty title until the first
	// logic tick recomputes it.
	m.curTitle = m.computeTitle(st.Snap())
	return m
}

// cellPixelSize reports the terminal's cell size in device pixels (width, height)
// via TIOCGWINSZ, or (0, 0) when the terminal doesn't report pixel dimensions (or
// stdout isn't a tty, e.g. in tests). The player uses it to draw the art box
// square in pixels rather than in cells.
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
