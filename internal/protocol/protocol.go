// Package protocol holds the shared State that the worker goroutines mutate
// and the TUI reads (state.go), and the sanitization every device string
// passes on its way in (sanitize.go). The wire format itself — the :2018
// tunnel's frames — lives in package tunnel; the workers translate its
// updates into the State calls here.
package protocol

import "time"

// Echo-suppression windows: after a local change, the device's own reading
// of the same value is ignored for a moment, so a poll answered just before
// the change landed cannot undo the optimistic flip.
const (
	VolHoldDuration  = 2500 * time.Millisecond
	MuteHoldDuration = 2500 * time.Millisecond
	PlayHoldDuration = 1500 * time.Millisecond
	// EQHoldDuration suppresses the device's own broadcast echo of an EQ/tone
	// control just changed locally, so rapid repeated nudges aren't fought by
	// the echo.
	EQHoldDuration = 600 * time.Millisecond
)
