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
	kTab
	kShiftTab
	kRune
)

type keyEvent struct {
	kind keyKind
	r    rune
}

// translate normalizes one key press: special keys dispatch on Key.Code (with
// shift+tab arriving as KeyTab + ModShift under bubbletea v2), printable keys
// carry their character in Key.Text.
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
		if k.Mod&tea.ModShift != 0 {
			return keyEvent{kind: kShiftTab}
		}
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

// key dispatches one keypress, reporting whether it asked to quit.
func (m *model) key(ev keyEvent) (quit bool) {
	// The services and logs panes are navigated, not merely dismissed, so they
	// take the key first and nothing leaks through to the dashboard beneath.
	if m.ov != ovNone {
		return m.overlayKey(ev)
	}
	if m.diag {
		m.diag = false // any key closes the overlay
		return false
	}

	// The EQ pane isn't drawn at mini size, so keep focus on the player there:
	// otherwise a pane focus held from before a shrink (or a tab press) would let
	// the arrow keys silently drive an invisible equalizer — including nudging
	// the Max Vol hardware cap down with no on-screen feedback.
	if m.miniMode() {
		m.pane = paneNow
	}

	// tab toggles which pane has focus (no-op at mini size — no second pane).
	if ev.kind == kTab || ev.kind == kShiftTab {
		if !m.miniMode() {
			m.pane = (m.pane + 1) % 2
		}
		return false
	}
	if ev.kind == kEsc {
		// Esc steps back out of the EQ pane; on the player it does nothing (quit
		// is q, deliberately — Esc is too easy to hit by accident).
		if m.pane == paneEQ {
			m.pane = paneNow
		}
		return false
	}
	if ev.kind == kRune && (ev.r == 'q' || ev.r == 'Q') {
		return true
	}

	// directional keys are pane-specific.
	switch ev.kind {
	case kUp:
		if m.pane == paneEQ {
			m.eqFocus = (m.eqFocus - 1 + len(eqOrder)) % len(eqOrder) // select band above
		} else {
			m.do("volup")
		}
		return false
	case kDown:
		if m.pane == paneEQ {
			m.eqFocus = (m.eqFocus + 1) % len(eqOrder) // select band below
		} else {
			m.do("voldn")
		}
		return false
	case kLeft:
		if m.pane == paneEQ {
			m.eqAdjust(-1) // nudge the focused slider left (decrease value)
		} else {
			m.focus = (m.focus - 1 + len(actions)) % len(actions)
		}
		return false
	case kRight:
		if m.pane == paneEQ {
			m.eqAdjust(+1) // nudge the focused slider right (increase value)
		} else {
			m.focus = (m.focus + 1) % len(actions)
		}
		return false
	case kEnter:
		if m.pane == paneEQ {
			m.eqToggleFocused()
		} else {
			m.do(actions[m.focus])
		}
		return false
	}

	// playback / global rune keys work regardless of pane.
	if ev.kind == kRune {
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
		case 'e':
			if !m.miniMode() { // no EQ pane to focus at mini size
				m.pane = paneEQ
			}
		case '?':
			m.diag, m.ov = true, ovNone
		case 'c':
			m.openOverlay(ovServices)
		case 'l':
			m.openOverlay(ovLogs)
		}
	}
	return false
}

// openOverlay engages one interactive pane, closing whatever else was open —
// the three overlays are mutually exclusive by construction rather than by
// convention. Opening the logs pane costs a device round trip, so it is only
// asked for once per run unless the user refreshes: reopening the pane shows
// the tail already in hand instead of stalling on a fresh fetch.
func (m *model) openOverlay(which int) {
	if m.miniMode() {
		return // no room to draw it; the dashboard keeps the keys
	}
	m.diag, m.ov = false, which
	if which == ovLogs && !m.logAsked {
		m.logRequest()
	}
}

// toggleOverlay opens a pane, or closes it if it is the one already open.
func (m *model) toggleOverlay(which int) {
	if m.ov == which {
		m.ov = ovNone
		return
	}
	m.openOverlay(which)
}

// overlayKey drives the interactive panes. Esc backs out, q still quits the app
// from anywhere (matching the dashboard), and each pane's own letter closes it
// so the key that opened it also dismisses it.
func (m *model) overlayKey(ev keyEvent) (quit bool) {
	if ev.kind == kRune && (ev.r == 'q' || ev.r == 'Q') {
		return true
	}
	if ev.kind == kEsc {
		m.ov = ovNone
		return false
	}
	// The overlay letters work from inside an overlay too: pressing the pane's own
	// letter closes it, another pane's letter switches straight to it. Toggling a
	// service and then reading the log for why it did nothing is the whole
	// workflow, and making that a two-step (esc, then l) would be needless.
	if ev.kind == kRune {
		switch ev.r {
		case 'c':
			m.toggleOverlay(ovServices)
			return false
		case 'l':
			m.toggleOverlay(ovLogs)
			return false
		case '?':
			m.diag, m.ov = true, ovNone
			return false
		}
	}
	switch m.ov {
	case ovServices:
		switch {
		case ev.kind == kUp:
			m.svcMove(-1)
		case ev.kind == kDown:
			m.svcMove(+1)
		case ev.kind == kEnter:
			m.svcToggle(time.Now())
		}
	case ovLogs:
		page := max(m.rows-8, 1)
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
		case ev.kind == kRune && ev.r == 'r':
			m.logRequest()
		}
	}
	return false
}
