// The message loop and controller actions: tick cadences, Update, and the
// send/do verbs that turn input into device commands.

package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/lucasdaddiego/lp10/internal/mediakey"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// Two cadences drive the UI. The logic tick (100ms) advances the marquee and
// the window title — its constants (marqueeColTicks) are counted in these
// 100ms units. The frame tick is the animation clock, decoupled so the plasma
// motif glides at the renderer's 15 fps (renderFPS) without speeding up the
// logic above. It idles to a gentle rate while paused/idle, when the motif is
// frozen and the motif cache makes those wake-ups nearly free.
type logicMsg struct{}
type frameMsg struct{}

// mediaKeyMsg carries a macOS media transport key captured by the background
// event tap (internal/mediakey). It is delivered through the program so the
// action runs on the update loop — the tap thread must never touch model state.
type mediaKeyMsg struct{ action string }

// keyToAction maps a captured media key to the transport action do() understands.
func keyToAction(k mediakey.Key) (action string, ok bool) {
	switch k {
	case mediakey.PlayPause:
		return "toggle", true
	case mediakey.Next:
		return "next", true
	case mediakey.Prev:
		return "prev", true
	}
	return "", false
}

const (
	logicInterval = 100 * time.Millisecond
	framePlaying  = 66 * time.Millisecond  // ~15fps while playing: the plasma drifts slowly, and every frame is a full re-parse in the renderer
	frameSearch   = 350 * time.Millisecond // one searching-arc step per tick (~1.4s per pulse) while connecting
	frameIdle     = 250 * time.Millisecond // frozen motif: just keep the clock alive
)

func logicTick() tea.Cmd {
	return tea.Tick(logicInterval, func(time.Time) tea.Msg { return logicMsg{} })
}

func frameTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return frameMsg{} })
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{logicTick(), frameTick(framePlaying)}
	if m.cfg.Theme == "" || m.cfg.Theme == "auto" {
		// ask the terminal for its background once; the answer picks the palette
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	return tea.Batch(cmds...)
}

// nbSend enqueues v without ever blocking the caller: on a full buffer it drops
// the oldest queued item and retries once. Stale commands are coalesced/aged-out
// downstream, so a dropped one is harmless.
func nbSend[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- v:
		default:
		}
	}
}

// send enqueues a tunnel command without ever blocking the update loop.
func (m *model) send(code string, val int) {
	nbSend(m.cmds, workers.Command{Code: code, Val: val, TS: time.Now()})
}

// syncViews tells the probe workers whether anyone is looking at what LSSDP
// and ZeroConf find (only the diagnostics show them).
func (m *model) syncViews() {
	m.st.SetProbeQuiet(m.view != viewDiag)
}

func (m *model) adjustVol(delta int) {
	value := m.st.AdjustVol(delta)
	m.send(tunnel.VolumeCode, value)
	m.volumeNotice(value)
}

// volumeNotice prints the level a volume key just set.
func (m *model) volumeNotice(value int) {
	m.notify(fmt.Sprintf("volume %d%%", value), noticeFor)
}

// volumeLive reports whether the volume in State is the device's own, read
// this run. Until then it is the snapshot cached by the last run, and a step
// computed from it lands on the device as an absolute level: cached 40, the
// phone set 70 meanwhile, ↑ during "connecting…" sends VOL:42 and the room
// drops to 42 on connect. A later outage keeps the keys: the level in hand is
// this run's own, and a key pressed during a blip is delivered when it ends.
// The first status read also brings the mute, so the mute key waits on it too.
func (m *model) volumeLive() bool {
	return m.st.Snap().VolLive
}

func (m *model) do(action string) {
	switch action {
	case "volup", "voldn", "mute":
		if !m.volumeLive() {
			m.notify("volume not read yet · waiting for the device", noticeFor)
			return
		}
	}
	m.flash[action] = time.Now().Add(FlashDuration)
	switch action {
	case "toggle":
		// The device's POP is a toggle: the flip here only keeps the screen a
		// step ahead of its PLA push.
		m.st.ToggleOptimistic()
		m.send(tunnel.ToggleCode, 0)
	case "next":
		m.send(tunnel.NextCode, 0)
	case "prev":
		// One PRE; on Spotify the device restarts the current track first, so
		// skipping back is a double-press (device behavior, not modeled here).
		m.send(tunnel.PrevCode, 0)
	case "volup":
		m.adjustVol(+m.cfg.VolStep)
	case "voldn":
		m.adjustVol(-m.cfg.VolStep)
	case "mute":
		// A real mute in the MCU: the volume stays where it is.
		if m.st.ToggleMute() {
			m.send(tunnel.MuteCode, 1)
			m.notify("muted", noticeFor)
		} else {
			m.send(tunnel.MuteCode, 0)
			m.notify("unmuted", noticeFor)
		}
	}
}

// Update is the Bubble Tea message loop.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.dispatch(msg)
}

// dispatch handles one message. KeyReleaseMsg is deliberately not handled:
// releases arrive only when keyboard enhancements are requested, and this UI
// acts on presses alone.
func (m *model) dispatch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.rows, m.cols = msg.Height, msg.Width
		m.cellW, m.cellH = cellPixelSize() // window px changed; re-square the art box
		return m, nil
	case tea.BackgroundColorMsg:
		dark := msg.IsDark()
		m.bgDark = &dark
		m.ensureTheme() // rebuilds the palette when the answer differs from the one drawn
		return m, nil
	case logicMsg:
		m.scroll++       // advance the now-playing marquee (independent of play state)
		s := m.st.Snap() // one snapshot per tick, reused below
		m.syncViews()    // the LAN probes follow the view on screen
		now := time.Now()
		m.trackConnection(s, now)
		m.sleepFire(now, s) // after the connect summary, so a timer it cancels keeps the notice line
		// The window title rides View (tea.View.WindowTitle under bubbletea v2),
		// so the tick only has to keep the cached string current.
		m.curTitle = m.computeTitle(s)
		return m, logicTick()
	case frameMsg:
		// Advance the animation clock only when something on screen is animating:
		// the plasma motif while playing (frozen when paused), or the connecting
		// search figure in the art slot. Otherwise nothing moves, so idle the
		// clock — the 100ms logic tick still drives slow updates. m.motifLive /
		// m.searchLive are set by the last render.
		fs := m.st.Snap()
		next := frameIdle
		switch {
		case m.motifLive && fs.Playing:
			m.frame++
			next = framePlaying
		case m.searchLive && !fs.Connected:
			m.frame++
			next = frameSearch
		}
		return m, frameTick(next)
	case tea.KeyPressMsg:
		if k := tea.Key(msg); k.Mod&tea.ModCtrl != 0 && k.Code == 'c' {
			m.interrupted = true
			return m, tea.Quit
		}
		if m.dispatchKeys(translateAll(msg)) {
			return m, tea.Quit
		}
		return m, nil
	case tea.PasteMsg:
		// Bracketed paste: drive the hotkeys with the pasted text, exactly like
		// the same characters typed (see runeEvents).
		if m.dispatchKeys(runeEvents(msg.Content)) {
			return m, tea.Quit
		}
		return m, nil
	case mediaKeyMsg:
		m.do(msg.action)
		return m, nil
	}
	return m, nil
}

// dispatchKeys runs a batch of key events in order, reporting whether any asked
// to quit. An event that closes an overlay consumes the REST of its batch: a
// paste landing while an overlay is open should only dismiss it, not keep
// driving the dashboard underneath (a stray 'n' later in the same paste would
// skip the track; a second 'q' would quit the app).
func (m *model) dispatchKeys(evs []keyEvent) (quit bool) {
	for _, ev := range evs {
		wasOpen := m.view != viewPlayer
		if m.key(ev) {
			return true
		}
		if wasOpen && m.view == viewPlayer {
			return false
		}
	}
	return false
}

func (m *model) computeTitle(s protocol.Snapshot) string {
	text := m.cfg.Name
	if s.Track != nil {
		if tt := trackTitle(s.Track); tt != "" {
			text = GL["note"] + " " + tt
		}
	}
	var b strings.Builder
	n := 0
	for _, r := range text {
		if n >= MaxTitleLength {
			break
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}
