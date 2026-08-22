// The sleep timer: a host-side "pause in N minutes" that needs nothing from the
// device beyond the ordinary PAUSE the space bar sends. The LP10's own sleep
// timer is hidden on this MCU build, so the deadline lives here — armed and
// stepped with 's', cancelled with 'S', checked on the logic tick, and shown
// beside the clock. It is deliberately not persisted: a timer that outlives
// the process would pause the room some other day.

package tui

import (
	"strconv"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// sleepPresets are the minutes 's' cycles through, in order; one more press
// past the last entry turns the timer off again.
var sleepPresets = []int{15, 30, 45, 60, 90}

// sleepCycle arms the next preset: off -> 15 -> 30 -> 45 -> 60 -> 90 -> off. A
// re-arm restarts the countdown from now, so "s s" is a fresh 30 minutes, not
// 30 minus the seconds already spent at 15.
func (m *model) sleepCycle(now time.Time) {
	if m.sleepAt.IsZero() {
		m.sleepPreset = 0
	} else {
		m.sleepPreset++
	}
	if m.sleepPreset >= len(sleepPresets) {
		m.sleepCancel()
		return
	}
	m.sleepAt = now.Add(time.Duration(sleepPresets[m.sleepPreset]) * time.Minute)
}

// sleepCancel disarms the timer (idempotent).
func (m *model) sleepCancel() {
	m.sleepAt = time.Time{}
	m.sleepPreset = 0
}

// sleepFire is the tick hook: once the deadline passes it pauses the player —
// via the same optimistic toggle the space bar uses, so the screen flips at once
// and the device's echo is held off — and disarms. Already paused or idle, it
// just disarms: a timer must never RESUME (the toggle is a flip, so the play
// state is checked first). One-shot by construction. No note is posted: the
// seek row's amber "Paused" and the countdown leaving the header say it all,
// and State's note slot renders as the red error line.
func (m *model) sleepFire(now time.Time, s protocol.Snapshot) {
	if m.sleepAt.IsZero() || now.Before(m.sleepAt) {
		return
	}
	m.sleepCancel()
	if s.Playing == 0 && s.Track != nil {
		m.do("toggle")
	}
}

// sleepLabel is the countdown shown beside the clock ("☾ 29m"; "☾ 45s" inside
// the last minute) and whether it is in that final minute (drawn in the warn
// colour so the imminent pause is noticeable). "" when the timer is off. The
// minute figure rounds UP, so a just-armed 30-minute timer reads "30m", not
// "29m", and it never shows "0m" while still armed.
func (m *model) sleepLabel(now time.Time) (label string, final bool) {
	if m.sleepAt.IsZero() {
		return "", false
	}
	left := m.sleepAt.Sub(now)
	if left <= 0 {
		left = 0
	}
	secs := int((left + time.Second - 1) / time.Second)
	if secs < 60 {
		return GL["sleep"] + " " + strconv.Itoa(secs) + "s", true
	}
	mins := (secs + 59) / 60
	return GL["sleep"] + " " + strconv.Itoa(mins) + "m", false
}
