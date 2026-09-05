// Package protocol implements the LUCI wire protocol: record framing (records.go),
// MB42 parsing (mb42.go), sanitization at the parse boundary (sanitize.go),
// record application (apply.go), command reduction and the write whitelist
// (commands.go), the diagnostics network readout (netstat.go), and the shared
// State that worker goroutines mutate and the TUI reads (state.go).
// The device protocol: record framing, parsing, and the locked State.
package protocol

import "time"

// Echo-suppression / debounce windows.
const (
	VolHoldDuration = 2500 * time.Millisecond
	// NightHoldDuration suppresses a device @@n read-back after a local night
	// mode set, like the play/volume holds: a read taken before the set was
	// applied (the connect-time prologue parsed late, the last broadcast in
	// flight) would otherwise undo the optimistic flip — and, pressed within
	// the first second of a run, leave the room compressed after quit because
	// the restore then saw nothing to put back.
	NightHoldDuration = 2500 * time.Millisecond
	PlayHoldDuration  = 1500 * time.Millisecond
	DebounceWindow    = 3 * time.Second
	// EQHoldDuration suppresses the device's own broadcast echo of an EQ/tone
	// control just changed locally, so rapid repeated nudges aren't fought by
	// the echo.
	EQHoldDuration = 600 * time.Millisecond
)
