// The device-command side of the protocol: the queued Command type, batch
// reduction, and the stdin write whitelist.

package protocol

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var transportWords = map[string]bool{
	"PAUSE": true, "RESUME": true, "NEXT": true, "PREV": true,
}

var volRe = regexp.MustCompile(`^\d{1,3}$`)

// svcStates is the MID-92 service-toggle whitelist: id -> the states it accepts.
// Only services this app can actually move are listed. Bluetooth is deliberately
// absent — the LP10's remote is a Bluetooth device, so switching bluetoothd off
// would take the physical remote down with it. Google Cast is absent too: it is
// gated by CF_GOOGLE_CAST in /etc/libre_ConfigureENV, a different config layer
// that a setenv cannot reach, so a toggle here would silently do nothing.
//
// Spotify is the odd one out: it takes an engine name rather than a boolean,
// because its two engines are not interchangeable. "hifi" (newspotifyhifi) is
// the safe one; "pro" (spotifymusicpro) reaches lossless but does not drive this
// box's ALSA softvol, so the volume — app, phone and remote alike — stops
// attenuating. See the services pane, which labels that cost.
var svcStates = map[string]map[string]bool{
	"spotify": {"off": true, "hifi": true, "pro": true},
	"airplay": {"0": true, "1": true},
	"dlna":    {"0": true, "1": true},
	"tidal":   {"0": true, "1": true},
	"qobuz":   {"0": true, "1": true},
	"usb":     {"0": true, "1": true},
}

// Command is a queued device command carrying its own enqueue time.
type Command struct {
	Mid  int
	Data string
	TS   time.Time
}

// ReduceCommands collapses a command list: last volume wins (at its own
// position), consecutive PAUSE/RESUME runs collapse to the final one, every
// NEXT/PREV is preserved, order stable.
func ReduceCommands(cmds []Command) []Command {
	out := make([]Command, 0, len(cmds))
	for _, c := range cmds {
		switch {
		case c.Mid == 64 || c.Mid == 90 || c.Mid == 91 || c.Mid == 93:
			// last value wins for volume (64), the stats toggle (90), the
			// night-mode toggle (91) and the log request (93): drop any earlier
			// command with the same mid, keep this one
			out = append(slices.DeleteFunc(out, func(cc Command) bool { return cc.Mid == c.Mid }), c)
		case c.Mid == 92:
			// service toggles collapse PER SERVICE, not per mid: two different
			// services toggled in one batch are two independent intents, but
			// flipping one service twice should only reach the device once.
			id, _, _ := strings.Cut(c.Data, " ")
			out = append(slices.DeleteFunc(out, func(cc Command) bool {
				if cc.Mid != 92 {
					return false
				}
				ccID, _, _ := strings.Cut(cc.Data, " ")
				return ccID == id
			}), c)
		case c.Mid == 40 && (c.Data == "PAUSE" || c.Data == "RESUME") &&
			len(out) > 0 && out[len(out)-1].Mid == 40 &&
			(out[len(out)-1].Data == "PAUSE" || out[len(out)-1].Data == "RESUME"):
			out[len(out)-1] = c
		default:
			out = append(out, c)
		}
	}
	return out
}

// ValidatePayload whitelists what may be written to the device's stdin. MID 90
// is the diagnostics-stats toggle (1 = overlay open, send @@s; 0 = closed): it
// only ever flips a flag on the device, never reaches LUCI_local. MID 91 is the
// night-mode toggle: the loop sets the SoC's multi-band DRC enable (an ALSA
// boolean) and answers with an @@n readback; like 90 it never reaches LUCI_local.
// MID 92 is the service toggle ("<id> <state>", see svcStates) — it writes the
// device's env and kicks an init script, so it is the one command here that
// changes configuration rather than playback, and its payload is whitelisted by
// id AND by state. MID 93 requests a syslog tail and answers with @@l. None of
// 90-93 reach LUCI_local.
func ValidatePayload(mid int, data string) bool {
	switch mid {
	case 40:
		return transportWords[data]
	case 64:
		if !volRe.MatchString(data) {
			return false
		}
		n, _ := strconv.Atoi(data)
		return n <= 100
	case 90, 91:
		return data == "0" || data == "1"
	case 92:
		id, state, ok := strings.Cut(data, " ")
		if !ok {
			return false
		}
		return svcStates[id][state]
	case 93:
		// a bare fetch request naming the source — 1 the device syslog (@@l),
		// 2 the vendor app's own log (@@L). The severity filter is a laptop-side
		// view over the answer, not something the device is asked to re-run.
		return data == "1" || data == "2"
	}
	return false
}
