// The sleep timer: a host-side "pause in N minutes" that needs nothing from the
// device beyond the ordinary play/pause the space bar sends. The LP10's own sleep
// timer is hidden on this MCU build, so the deadline lives here — armed and
// stepped with 's', cancelled with 'S', checked on the logic tick, and shown
// beside the clock. It is deliberately not persisted: a timer that outlives
// the process would pause the room some other day.

package tui

import (
	"strconv"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

// sleepPresets are the minutes 's' cycles through, in order; one more press
// past the last entry turns the timer off again.
var sleepPresets = []int{15, 30, 45, 60, 90}

// sleepLate bounds how long after its deadline a timer the link kept from
// firing may still pause. After a blip of a few minutes the pause is still
// the one that was asked for; after an outage that ran through the night it
// would stop the next morning's playback, hours after anyone wanted it.
const sleepLate = 10 * time.Minute

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
// optimistically, like the space bar, so the screen flips at once and the
// device's echo is held off — and disarms. Already paused or idle, it just
// disarms. The device's POP is a toggle, so the pause is decided inside State
// under its lock (PauseOptimistic): the POP goes out only while the device
// last said it plays — a timer must never RESUME, and a device-side pause
// landing between the tick's snapshot and the flip would have turned the
// toggle into exactly that. It needs no track metadata, so a source playing
// without a title still goes quiet. One-shot by construction. No note is
// posted: the status row's amber "Paused" and the countdown leaving the
// header say it all, and State's note slot renders as the red error line.
//
// With the tunnel down at the deadline the timer stays armed and fires on
// reconnect: a POP queued into a dead link expires unheard ("command not
// delivered") while the room plays on all night. A reconnect more than
// sleepLate past the deadline cancels the timer instead of pausing, and says
// so.
func (m *model) sleepFire(now time.Time, s protocol.Snapshot) {
	if m.sleepAt.IsZero() || now.Before(m.sleepAt) {
		return
	}
	if !s.Connected {
		return
	}
	if late := now.Sub(m.sleepAt); late > sleepLate {
		m.sleepCancel()
		m.notify("sleep timer cancelled · it ran out "+fmtAgeShort(late)+" ago", noticeFor*2)
		return
	}
	if m.st.PauseOptimistic() {
		m.flash["toggle"] = now.Add(FlashDuration)
		m.send(tunnel.ToggleCode, 0)
	}
	m.sleepCancel()
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
	left := max(m.sleepAt.Sub(now), 0)
	secs := int((left + time.Second - 1) / time.Second)
	if secs < 60 {
		return GL["sleep"] + " " + strconv.Itoa(secs) + "s", true
	}
	mins := (secs + 59) / 60
	return GL["sleep"] + " " + strconv.Itoa(mins) + "m", false
}
