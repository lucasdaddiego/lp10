package workers

import (
	"context"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// streamOnce is one ssh connection lifecycle with a throwaway process slot and
// no snapshot persistence — the shape the focused stream tests drive.
func streamOnce(st *protocol.State, cfg config.Config, backoff time.Duration, control *runControl) time.Duration {
	return streamOnceWithSnapshot(st, cfg, backoff, "", newProcessSlot(), control)
}

// tunnelOnce is one tunnel connection lifecycle with no carried command.
func tunnelOnce(control *runControl, st *protocol.State, cfg config.Config, eqcmds <-chan EQCommand, backoff time.Duration) time.Duration {
	next, _ := tunnelOnceContext(context.Background(), control, st, cfg, eqcmds, backoff, nil)
	return next
}
