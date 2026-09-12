// Keyboard input: normalization of Bubble Tea key messages and the pane-aware
// dispatch of each keypress.

package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// keyKind is the normalized key class dispatched to the controller logic.
type keyKind int

const (
	kOther keyKind = iota
	kEnter
	kEsc
	kLeft
	kRight
	kUp
	kDown
	kTab // tab and shift-tab alike: with two panes the toggle is its own inverse
	kRune
)

type keyEvent struct {
	kind keyKind
	r    rune
}

// translate normalizes one key press: special keys dispatch on Key.Code
// (shift+tab arrives as KeyTab + ModShift under bubbletea v2 and folds into
// kTab), printable keys carry their character in Key.Text.
func translate(k tea.Key) keyEvent {
	switch k.Code {
	case tea.KeyEnter:
		return keyEvent{kind: kEnter}
	case tea.KeyEscape:
		return keyEvent{kind: kEsc}
	case tea.KeyLeft:
		return keyEvent{kind: kLeft}
	case tea.KeyRight:
		return keyEvent{kind: kRight}
	case tea.KeyUp:
		return keyEvent{kind: kUp}
	case tea.KeyDown:
		return keyEvent{kind: kDown}
	case tea.KeyTab:
		return keyEvent{kind: kTab}
	case tea.KeySpace:
		return keyEvent{kind: kRune, r: ' '}
	}
	if isText(k) {
		if rs := []rune(k.Text); len(rs) == 1 {
			return keyEvent{kind: kRune, r: rs[0]}
		}
	}
	return keyEvent{kind: kOther}
}

// isText reports whether a key press is printable input. Shift and CapsLock
// must NOT disqualify it: under the Kitty keyboard protocol (Ghostty) a '?' or
// '+' arrives as its base code PLUS ModShift with the shifted character in
// Text — masking only the text-compatible modifiers here mirrors ultraviolet's
// own Key.MatchString semantics, so '?', '+', '_' and 'Q' keep working when the
// terminal upgrades the wire encoding. (Legacy encodings send Mod == 0.)
func isText(k tea.Key) bool {
	return k.Text != "" && k.Mod&^(tea.ModShift|tea.ModCapsLock) == 0
}

// translateAll expands one key message into the events to dispatch. A press
// normally carries a single printable rune, but Key.Text may carry several
// (legacy fast-typing/IME paths coalesce); each must be dispatched in order or
// the whole batch is silently lost. A bracketed paste arrives separately as
// tea.PasteMsg — Update feeds its text through runeEvents for the same effect.
func translateAll(msg tea.KeyPressMsg) []keyEvent {
	k := tea.Key(msg)
	if isText(k) && len(k.Text) > 1 {
		return runeEvents(k.Text)
	}
	return []keyEvent{translate(k)}
}

// runeEvents turns a run of printable text into one rune-key event per
// character, preserving the historical behaviour that pasted/scripted input
// (e.g. `tmux send-keys`) drives the hotkeys exactly like typed input.
func runeEvents(s string) []keyEvent {
	rs := []rune(s)
	evs := make([]keyEvent, len(rs))
	for i, r := range rs {
		evs[i] = keyEvent{kind: kRune, r: r}
	}
	return evs
}

// key dispatches one key event. The view strip is global — 1-5 and tab
// switch views, esc (and q, off the player) return to the player, ? toggles
// the help page, and the letters e / c / l / i still open (and close) their
// view — then the current view takes what is left. Playback keys work from
// every view that does not claim the letter, so a track can be paused from
// the diagnostics without leaving them.
func (m *model) key(ev keyEvent) (quit bool) {
	if m.miniMode() {
		m.view = viewPlayer // only the player is drawn at mini size
	}
	if m.viewKey(ev) {
		return false
	}
	if ev.kind == kRune && (ev.r == 'q' || ev.r == 'Q') {
		if m.view != viewPlayer {
			m.view = viewPlayer // q backs out first; a second q quits
			return false
		}
		return true
	}
	switch m.view {
	case viewEQ:
		if m.eqKey(ev) {
			return false
		}
	case viewServices:
		if m.servicesKey(ev) {
			return false
		}
	case viewLogs:
		if m.logsKey(ev) {
			return false
		}
	case viewDiag:
		if ev.kind == kRune && (ev.r == 'u' || ev.r == 'U') {
			// u asks the vendor's manifest directly — the one request that
			// leaves the LAN, so it is a deliberate keystroke, never a side
			// effect of opening the view (which shows the box's own 4-hourly
			// verdict).
			m.st.RequestOTA()
			return false
		}
	case viewHelp:
		return false // a reference page: esc, q or ? leave it
	default:
		if m.playerKey(ev) {
			return false
		}
	}
	m.playbackKey(ev)
	return false
}

// viewKey handles the keys that move between views. It reports whether the
// event was one of them.
func (m *model) viewKey(ev keyEvent) bool {
	switch ev.kind {
	case kTab:
		// tab cycles the numbered views; help is not in the loop
		if !m.miniMode() {
			m.setView((m.view + 1) % numberedViews)
		}
		return true
	case kEsc:
		m.view = viewPlayer
		return true
	case kRune:
		switch ev.r {
		case '1', '2', '3', '4', '5':
			m.setView(view(ev.r - '1'))
			return true
		case '?':
			m.toggleView(viewHelp)
			return true
		case 'i', 'I':
			m.toggleView(viewDiag)
			return true
		case 'e', 'E':
			m.toggleView(viewEQ)
			return true
		case 'c', 'C':
			m.toggleView(viewServices)
			return true
		case 'l', 'L':
			m.toggleView(viewLogs)
			return true
		}
	}
	return false
}

// setView shows a view. Nothing but the player is drawn at mini size, so the
// switch is refused there. Opening the logs costs a device round trip, so it
// is asked for once per run unless the user refreshes: reopening shows the
// tail already in hand instead of stalling on a fresh fetch.
func (m *model) setView(v view) {
	if m.miniMode() {
		return
	}
	m.view = v
	if v == viewLogs && !m.logAsked[m.logSrc] {
		m.logRequest()
	}
}

// toggleView shows a view, or returns to the player when it is the one on
// show — so the key that opened a view also closes it.
func (m *model) toggleView(v view) {
	if m.view == v {
		m.view = viewPlayer
		return
	}
	m.setView(v)
}

// openOverlay is the older name for setView, kept for its callers.
func (m *model) openOverlay(which view) { m.setView(which) }

// playerKey is the player's own keys: arrows move the volume and the
// transport focus, enter presses the focused button.
func (m *model) playerKey(ev keyEvent) bool {
	switch ev.kind {
	case kUp:
		m.do("volup")
	case kDown:
		m.do("voldn")
	case kLeft:
		m.focus = (m.focus - 1 + len(actions)) % len(actions)
	case kRight:
		m.focus = (m.focus + 1) % len(actions)
	case kEnter:
		m.do(actions[m.focus])
	default:
		return false
	}
	return true
}

// eqKey drives the equalizer: ↑↓ select a control, ←→ adjust it, enter
// toggles a switch or steps a preset.
func (m *model) eqKey(ev keyEvent) bool {
	switch ev.kind {
	case kUp:
		m.eqFocus = (m.eqFocus - 1 + len(eqOrder)) % len(eqOrder)
	case kDown:
		m.eqFocus = (m.eqFocus + 1) % len(eqOrder)
	case kLeft:
		m.eqAdjust(-1)
	case kRight:
		m.eqAdjust(+1)
	case kEnter:
		m.eqToggleFocused()
	default:
		return false
	}
	return true
}

func (m *model) servicesKey(ev keyEvent) bool {
	switch ev.kind {
	case kUp:
		m.svcMove(-1)
	case kDown:
		m.svcMove(+1)
	case kEnter:
		m.svcToggle(time.Now())
	default:
		return false
	}
	return true
}

func (m *model) logsKey(ev keyEvent) bool {
	page := m.logPage()
	switch {
	case ev.kind == kUp:
		m.logScrollBy(+1, page)
	case ev.kind == kDown:
		m.logScrollBy(-1, page)
	case ev.kind == kLeft:
		m.logScrollBy(+page, page)
	case ev.kind == kRight:
		m.logScrollBy(-page, page)
	case ev.kind == kRune && ev.r == 'f':
		m.logCycleFilter()
	case ev.kind == kRune && ev.r == 's':
		m.logCycleSource() // the logs' s wins over the sleep timer here
	case ev.kind == kRune && ev.r == 'r':
		m.logRequest()
	default:
		return false
	}
	return true
}

// playbackKey is the transport and the timers — available from every view
// that has not claimed the letter.
func (m *model) playbackKey(ev keyEvent) {
	if ev.kind != kRune {
		return
	}
	switch ev.r {
	case ' ':
		m.do("toggle")
	case 'n':
		m.do("next")
	case 'p':
		m.do("prev")
	case '+', '=':
		m.do("volup")
	case '-', '_':
		m.do("voldn")
	case 'm':
		m.do("mute")
	case 't':
		m.showRemaining = !m.showRemaining
	case 's':
		m.sleepCycle(time.Now()) // off -> 15 -> 30 -> 45 -> 60 -> 90 min -> off
	case 'S':
		m.sleepCancel()
	case 'b':
		m.bedtimeCycle(time.Now()) // sleep step + night mode on, restored when the timer ends
	case 'd':
		m.nightToggle() // night mode: the device's multi-band DRC
	}
}
