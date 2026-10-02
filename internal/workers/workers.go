// Package workers owns the background runtime: the :2018 tunnel — the one
// connection to the box, carrying the player and the equalizer — its command
// queue and reconnects, snapshot persistence, and the probes that need no
// tunnel (LSSDP, Spotify ZeroConf, the on-demand OTA check).
package workers

import (
	"fmt"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

const (
	InitialBackoff = 250 * time.Millisecond
	MaxBackoff     = 3 * time.Second
	DrainTimeout   = 1 * time.Second

	// SnapshotPersistInterval bounds how often the instant-first-paint snapshot
	// is rewritten mid-session. Its consumer only seeds the next launch's first
	// frame, and Close persists a final fresh copy on quit, so a generous
	// interval is fine.
	SnapshotPersistInterval = 30 * time.Second
)

// fence runs fn under a recover that notes the panic and holds the worker a
// second, so a deterministic failure cannot spin. Each worker that parses
// what the LAN sends (the ZeroConf and LSSDP probes) runs its probe under it:
// one parser bug there must cost a noted error, not the program — a panic
// that escapes a worker goroutine kills the process with the terminal still
// in raw mode.
func fence(st *protocol.State, control *runControl, name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			st.Note(fmt.Sprintf("%s: %v", name, r))
			control.stop.Wait(time.Second)
		}
	}()
	fn()
}

// waitBackoff sleeps the current backoff (interruptible by Stop) and returns the
// doubled-and-capped next value.
func waitBackoff(control *runControl, backoff time.Duration) time.Duration {
	if !control.stop.Wait(backoff) {
		backoff = min(backoff*2, MaxBackoff)
	}
	return backoff
}

// selfSnap is the persisted subset of a snapshot: the volume and the
// last-known EQ/tone values, so the volume rail and the equalizer paint
// instantly on the next launch.
func selfSnap(st *protocol.State) config.CachedSnapshot {
	_, eq := st.EQView()
	return config.CachedSnapshot{Vol: st.Snap().Vol, EQ: eq}
}
