// The services pane ('c'): what the box's streaming services are actually doing,
// and the ones this app can move.
//
// The pane exists because the LP10 has two independent notions of "on" and they
// drift apart. The device's own web page reads only the env flag, so it will
// cheerfully report "Spotify: on" while no engine is running at all — which is
// exactly the state the AR241CE_8530 OTA left this box in (it flipped the
// factory default to the Pro engine while the user DB still held the HiFi flag;
// both init scripts are guarded on the OTHER flag being clear, so neither
// started). Every row here therefore shows BOTH columns, running and
// configured, and marks the disagreement rather than picking a winner.
//
// The other half of the pane's job is honesty about leverage. These services are
// not gated the same way: some are enforced by their init script (a setenv
// sticks across reboots), some have no env gate at all (they restart on every
// netready no matter what the flag says, so the only lever is stop/start and it
// is lost on reboot), and two cannot be touched from here at all. Offering an
// identical switch for all of them would reproduce the vendor's own bug — a
// control that reports success and does nothing.

package tui

import (
	"strings"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// svcGate is how a service's on/off state is actually enforced on the device,
// which decides what a toggle here can promise.
type svcGate int

const (
	// gateEnv: the init script reads the env flag and refuses to start without
	// it. setenv + a netready kick, and it survives a reboot.
	gateEnv svcGate = iota
	// gateDaemon: the init script has NO env test — it starts the daemon on
	// every netready regardless. Stop/start works, but the next boot undoes it.
	gateDaemon
	// gateFixed: not reachable from here at all.
	gateFixed
	// gateEngine: Spotify. Not a boolean — a choice between two engines that
	// are not interchangeable.
	gateEngine
)

// svcDef is one row of the pane. reason fills the action column for a row that
// has no action — a blank there would read as an oversight rather than a
// decision; detail is the longer explanation shown for the selected row only, so
// the list stays dense but nothing is left unexplained.
type svcDef struct {
	id, label string
	gate      svcGate
	reason    string
	detail    []string
}

// svcRows is the pane in display order: the things you actually switch first,
// then the ones that are only reported. Order is deliberate rather than
// alphabetical here — this pane is a control surface, not a readout.
var svcRows = []svcDef{
	{
		id: "spotify", label: "Spotify", gate: gateEngine,
		detail: []string{
			"legacy (hifi) drives this box's ALSA softvol, so the volume works — from the app,",
			"the phone and the physical remote. it tops out at Ogg/AAC: no lossless, ever.",
			"new (pro) is the newer eSDK and the only one that negotiates FLAC, but on this",
			"firmware it bypasses softvol: output pins at full scale and NOTHING attenuates.",
		},
	},
	{
		id: "airplay", label: "AirPlay 2", gate: gateDaemon,
		detail: []string{
			"S99airplay_v2 has no env test — it starts airplaydemo on every netready whatever",
			"the flag says, so switching it off here lasts until the next reboot.",
		},
	},
	{
		id: "dlna", label: "DLNA / UPnP", gate: gateDaemon,
		detail: []string{
			"S99dmr never reads DMPEnable — its restart() is just \"killall -9 dmr; dmr &\" with",
			"no env test — so the flag is inert and the daemon returns on every netready.",
			"that is why the flag and the daemon can disagree here without anything being",
			"wrong; AirPlay is in exactly the same position, its flag just happens to agree.",
		},
	},
	{
		id: "tidal", label: "Tidal", gate: gateEnv,
		detail: []string{
			"gated inside the tidalConnect binary rather than its init script — the script",
			"starts it either way and the process exits on its own when the flag is clear.",
		},
	},
	{
		id: "qobuz", label: "Qobuz", gate: gateEnv,
		detail: []string{
			"S50avahi-daemon reads the same flag, so enabling Qobuz also brings up avahi.",
		},
	},
	{
		id: "usb", label: "USB playback", gate: gateEnv,
		detail: []string{
			"there is no daemon to kick for this one: the flag is read at startup, so the",
			"change only lands on the next boot.",
		},
	},
	{
		id: "bt", label: "Bluetooth", gate: gateFixed,
		reason: "the remote control runs on it",
		detail: []string{
			"deliberately not offered: the LP10's remote control IS a Bluetooth device, so",
			"stopping bluetoothd would take the remote down with it.",
		},
	},
	{
		id: "cast", label: "Google Cast", gate: gateFixed,
		reason: "set in /etc/libre_ConfigureENV",
		detail: []string{
			"gated by CF_GOOGLE_CAST in /etc/libre_ConfigureENV, a different config layer that",
			"setenv cannot reach — a switch here would report success and do nothing.",
		},
	},
}

// spotifyStates is the engine cycle for the Spotify row, in the order enter
// steps through them, with the label each wears. The wire values are what the
// device loop's tg() accepts.
var spotifyStates = []struct{ wire, label string }{
	{"off", "off"},
	{"hifi", "legacy (hifi)"},
	{"pro", "new (volume problem)"},
}

// svcPendingFor bounds how long a row can sit on "applying…" when the device
// never answers. It is a backstop, not the normal path: the marker clears the
// moment the device reports the state that was asked for (see svcSettled), so a
// working toggle feels immediate rather than freezing the row for a fixed wait.
const svcPendingFor = 12 * time.Second

// svcMove steps the selection, wrapping.
func (m *model) svcMove(d int) {
	m.svcFocus = (m.svcFocus + d + len(svcRows)) % len(svcRows)
}

// svcPos is the row's position in its own cycle: what the device reports, or —
// while a toggle is still in flight — what was last ASKED for.
//
// That second case is what makes the Spotify row cyclable at speed. Stepping
// from the device's report means a second press before the device has answered
// re-sends the same target, so the cycle appears frozen for as long as the daemon
// takes to come up. Stepping from the pending target instead lets presses stack:
// the display advances immediately, ReduceCommands collapses the burst to the
// last one, and the device is asked for exactly where the user stopped.
func (m *model) svcPos(row svcDef, cv *protocol.ConfInfo, pending bool) string {
	if pending {
		return m.svcPendingWant
	}
	if row.gate == gateEngine {
		return spotifyStates[spotifyStateIdx(cv)].wire
	}
	if cv != nil && cv.Svc[row.id] == "on" {
		return "1"
	}
	return "0"
}

// svcWant is the state the row would move to — the single source for both what
// enter sends and what the row advertises it will do, so the label can never
// promise something different from the command.
func (m *model) svcWant(row svcDef, cv *protocol.ConfInfo, pending bool) string {
	cur := m.svcPos(row, cv, pending)
	if row.gate == gateEngine {
		for i, st := range spotifyStates {
			if st.wire == cur {
				return spotifyStates[(i+1)%len(spotifyStates)].wire
			}
		}
		return spotifyStates[1].wire // unrecognised: step to the safe engine
	}
	if cur == "1" {
		return "0"
	}
	return "1"
}

// svcLabelFor names a wire state for display.
func svcLabelFor(row svcDef, wire string) string {
	if row.gate == gateEngine {
		for _, st := range spotifyStates {
			if st.wire == wire {
				return st.label
			}
		}
		return wire
	}
	if wire == "1" {
		return "on"
	}
	return "off"
}

// svcToggle acts on the focused row. A gateFixed row has nothing to send — it
// says why instead of pretending. Everything else sends one MID-92 command and
// marks the row pending until the device reports back; the pane never paints an
// optimistic local state, because the whole point of it is to show what the
// device actually did.
func (m *model) svcToggle(now time.Time) {
	row := svcRows[m.svcFocus]
	if row.gate == gateFixed {
		return
	}
	cv := m.st.ConfView()
	next := m.svcWant(row, cv, m.svcPendingRow(row.id, cv, now))
	if !protocol.ValidatePayload(92, row.id+" "+next) {
		return // an id the wire refuses is a bug here, not something to send
	}
	m.svcPending, m.svcPendingWant, m.svcPendingAt = row.id, next, now
	m.send(92, row.id+" "+next)
}

// svcSettled reports whether the device has confirmed the pending toggle, so the
// row can stop saying "applying…" the instant the answer lands.
func (m *model) svcSettled(cv *protocol.ConfInfo) bool {
	switch m.svcPendingWant {
	case "off", "hifi", "pro":
		return cv.Cfg() == m.svcPendingWant
	case "1":
		return cv.Svc[m.svcPending] == "on"
	case "0":
		return cv.Svc[m.svcPending] == "off"
	}
	return true
}

// svcPendingRow reports whether this row is still waiting on the device.
func (m *model) svcPendingRow(id string, cv *protocol.ConfInfo, now time.Time) bool {
	if m.svcPending != id || now.Sub(m.svcPendingAt) >= svcPendingFor {
		return false
	}
	return !m.svcSettled(cv)
}

// spotifyStateIdx maps the device's reported engine config onto the cycle. The
// "both" pair (each init script blocked by the other flag) has no cycle slot of
// its own: it is a broken state, not a choice, so enter from there steps to the
// safe engine rather than back into it.
func spotifyStateIdx(cv *protocol.ConfInfo) int {
	switch cv.Cfg() {
	case "hifi":
		return 1
	case "pro":
		return 2
	case "both":
		return 0 // next press lands on hifi
	}
	return 0
}

// svcState is the row's ONE state cell — what is actually true right now. For
// most services that is whether the daemon is up; for Spotify it is which engine
// is configured (a three-way, not a boolean); for USB, which has no daemon to
// observe, the loop reports its flag under the same key. Showing one honest cell
// rather than a running
// column beside a configured column is the whole readability fix: the two agree
// on almost every row, so printing both taught the eye to skip the line, which is
// precisely where the interesting case was hiding.
func (m *model) svcState(row svcDef, cv *protocol.ConfInfo) string {
	t := m.sty.pens()
	if row.gate == gateEngine {
		if cv.Cfg() == "both" {
			return m.sty.sevs[2].Render("⚠ both flags set — neither runs")
		}
		if cv.Cfg() == "none" {
			return t.dim.render("○") + " " + t.dim.render("off")
		}
		return t.acc.render("●") + " " + t.txt.render(spotifyStates[spotifyStateIdx(cv)].label)
	}
	switch cv.Svc[row.id] {
	case "on":
		return t.acc.render("●") + " " + t.txt.render("on")
	case "off":
		return t.dim.render("○") + " " + t.dim.render("off")
	}
	return t.dmr.render("—")
}

// svcAction spells out what enter does, on the focused row only. A control
// surface that makes you press a key to discover what the key does is a guessing
// game — but printing the same phrase on every row is noise, and only one row is
// ever actionable. Naming the destination also makes the Spotify row's three-way
// obvious without a legend.
func (m *model) svcAction(row svcDef, cv *protocol.ConfInfo, focused, pending bool) string {
	t := m.sty.pens()
	if row.gate == gateFixed {
		return t.dmr.render(row.reason) // a standing fact, not an action
	}
	if !focused {
		return ""
	}
	want := m.svcWant(row, cv, pending)
	var dest string
	switch {
	case row.gate == gateEngine:
		dest = svcLabelFor(row, want)
		if want == "pro" {
			return t.dmr.render("enter → ") + m.sty.sevs[1].Render(dest)
		}
	case want == "1":
		dest = "turn on"
	default:
		dest = "turn off"
	}
	if row.gate == gateDaemon {
		dest += " · until reboot"
	}
	if row.id == "usb" {
		dest += " · on reboot"
	}
	return t.dmr.render("enter → " + dest)
}

// svcFlagNote surfaces the SECOND truth only where it means something.
//
// A gateDaemon row's init script never reads its env flag, so the flag is inert
// there whatever it says — and whether it happens to agree with the running
// daemon is pure coincidence. Marking only the disagreeing one in the warn hue
// said "DLNA has a problem" when DLNA is fine and AirPlay is in exactly the same
// state; both now carry the same neutral note instead.
//
// The warn hue is kept for the case that IS a fault: a flag the init script DOES
// consult, contradicted by what is actually running. That is the mismatch the
// device's own web page structurally cannot show, and hiding it among agreeing
// columns is what made it invisible in the first place.
func (m *model) svcFlagNote(row svcDef, cv *protocol.ConfInfo) string {
	if row.gate == gateDaemon {
		return m.sty.pens().dmr.render("flag not consulted")
	}
	if row.id == "usb" || !cv.Divergent(row.id) {
		return ""
	}
	return m.sty.sevs[1].Render("⚠ flag says " + cv.Env(row.id))
}

// renderServices draws the pane: the services this app can move, then the ones it
// can only report on, kept in their own group so a switch that is absent reads as
// deliberate rather than broken.
func (m *model) renderServices(now time.Time, W int) []string {
	t := m.sty.pens()
	cv := m.st.ConfView()
	if cv == nil {
		return frameBody([]string{t.dmr.render("reading services from device…")},
			[]string{m.svcFooter(W)}, m.rows-2, true)
	}

	row := func(i int, d svcDef) string {
		cur, label := "  ", t.txt.render(d.label)
		if i == m.svcFocus {
			cur, label = t.acc.render("▸ "), t.bri.render(d.label)
		}
		pend := m.svcPendingRow(d.id, cv, now)
		state := m.svcState(d, cv)
		if pend {
			state = t.dmr.render("… " + svcLabelFor(d, m.svcPendingWant))
		}
		action := m.svcAction(d, cv, i == m.svcFocus, pend)
		return clipStyled(cur+padVis(label, 16)+padVis(state, 22)+
			padVis(m.svcFlagNote(d, cv), 18)+action, W)
	}

	var content []string
	content = append(content, m.sectionHead("services", W), "")
	for i, d := range svcRows {
		if d.gate != gateFixed {
			content = append(content, row(i, d))
		}
	}
	content = append(content, "", m.sectionHead("reported only · not switchable from here", W), "")
	for i, d := range svcRows {
		if d.gate == gateFixed {
			content = append(content, row(i, d))
		}
	}

	content = append(content, "", m.sectionHead("spotify engine", W), "")
	content = append(content, m.spotifyInsight(cv, W)...)
	content = append(content, "", m.sectionHead(svcRows[m.svcFocus].label, W), "")
	for _, d := range svcRows[m.svcFocus].detail {
		content = append(content, t.dmr.render(Clip(d, W)))
	}
	return frameBody(content, []string{"", m.svcFooter(W)}, m.rows-2, false)
}

// spotifyInsight is the deep readout the rest of the pane's rows don't need: the
// engine actually loaded, its Spotify eSDK build, and what that build can
// receive. The eSDK version is the only honest signal for the codec ceiling —
// both engines link libFLAC, but only the newer one negotiates FLAC delivery, so
// "has a FLAC decoder" says nothing about what arrives over the wire.
func (m *model) spotifyInsight(cv *protocol.ConfInfo, W int) []string {
	t := m.sty.pens()
	var out []string
	eng := cv.Engine()
	switch eng {
	case "":
		out = append(out, m.diagLine("engine", t.dim.render("none running")))
	case "newspotifyhifi":
		out = append(out, m.diagLine("engine", t.txt.render(eng)+t.dmr.render(" · legacy · Ogg/AAC only · volume works")))
	case "spotifymusicpro":
		out = append(out, m.diagLine("engine", t.txt.render(eng)+t.dmr.render(" · new · FLAC capable · ")+
			m.sty.sevs[2].Render("volume does not attenuate")))
	default:
		out = append(out, m.diagLine("engine", t.txt.render(eng)))
	}
	if sdk := cv.SDK(); sdk != "" {
		out = append(out, m.diagLine("eSDK", t.txt.render(sdk)))
	}
	if cv.Cfg() == "both" {
		out = append(out, m.diagLine("config", m.sty.sevs[2].Render(
			"both engine flags set — each init script is blocked by the other, so neither starts")))
	}
	for i, l := range out {
		out[i] = clipStyled(l, W)
	}
	return out
}

// sectionHead is the pane's rule-and-title row, matching the dashboard's
// equalizer divider so the overlays read as the same product.
func (m *model) sectionHead(title string, W int) string {
	lead := 2
	body := " " + title + " "
	rest := max(W-lead-DispW(body), 0)
	return m.sty.pens().dmr.render(strings.Repeat("─", lead) + body + strings.Repeat("─", rest))
}

func (m *model) svcFooter(W int) string {
	left := "↑↓ select · enter toggle · esc back"
	right := "writes the device's config"
	return between(m.sty.pens().dmr.render(left), DispW(left),
		m.sty.pens().dmr.render(right), DispW(right), W)
}
