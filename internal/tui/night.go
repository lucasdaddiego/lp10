// Night mode: the LP10's multi-band DRC (the SoC's AED block), the one audio
// effect on the box a host can switch — the EQ / DRC coefficient tables are
// read-only from userspace and the WM8904 mixer controls drive a chip that is
// not on the bus (live probe 2026-08-22). With the firmware's stock 3-band
// table the compressor is clearly audible: peaks reined in, quiet passages
// lifted — late-night listening. One boolean over the existing ssh stream
// (MID 91), read back by the device (@@n) so the badge shows device truth, and
// put back to its connect-time value on quit, so a session never leaves the
// room compressed the next morning. Not persisted, like the sleep timer.

package tui

import "github.com/lucasdaddiego/lp10/internal/protocol"

// nightToggle flips night mode: optimistic local flip (the badge reacts at
// once), then the MID-91 set; the device's @@n readback confirms or corrects.
// Before the device has reported a state the toggle assumes "off" and sends on.
func (m *model) nightToggle() {
	s := m.st.Snap()
	on := s.NightKnown && s.Night
	m.st.SetNightLocal(!on)
	if on {
		m.send(91, "0")
	} else {
		m.send(91, "1")
	}
}

// nightLabel is the header badge while night mode is on ("◐ night"), or "".
func nightLabel(s protocol.Snapshot) string {
	if s.NightKnown && s.Night {
		return GL["night"] + " night"
	}
	return ""
}

// nightRestore puts night mode back to the value first read this process (the
// same restore quit performs), optimistically and over the wire. A no-op when
// nothing is known or nothing changed.
func (m *model) nightRestore() {
	orig, needed := m.st.NightRestore()
	if !needed {
		return
	}
	m.st.SetNightLocal(orig)
	if orig {
		m.send(91, "1")
	} else {
		m.send(91, "0")
	}
}
