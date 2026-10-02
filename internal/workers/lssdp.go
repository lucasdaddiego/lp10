// The LSSDP liveness worker: a one-datagram probe of the device's own UDP:1800
// responder, which answers with no tunnel and no auth — so it still works while
// :2018 is refusing lp10 or silent. It runs fast while disconnected (that's
// when "is the device even there?" matters) and slowly while connected, just
// to keep the diag row fresh. It is also where the box names its firmware
// build: the update check asks the vendor about this answer's build.

package workers

import (
	"context"
	"os"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

const (
	lssdpTimeout       = 1500 * time.Millisecond
	lssdpDisconnected  = 5 * time.Second
	lssdpConnected     = 30 * time.Second
	lssdpFirstProbeLag = 500 * time.Millisecond // let the tunnel connect race ahead at startup
	// probeQuietPoll is how often a quiet probe worker (connected, nothing
	// showing its answer) re-checks whether a view now wants it — a flag read,
	// no network.
	probeQuietPoll = 2 * time.Second
)

// lssdpHost is the probe target: the configured host, or LP10_LSSDP_HOST
// (tests point it at a local responder or nowhere; empty disables the
// worker entirely, as the hermetic e2e runs do).
func lssdpHost(cfg config.Config) (string, bool) {
	if h, set := os.LookupEnv("LP10_LSSDP_HOST"); set {
		return h, h != ""
	}
	return cfg.Host, cfg.Host != ""
}

func lssdpWorker(ctx context.Context, control *runControl, st *protocol.State, cfg config.Config) {
	host, ok := lssdpHost(cfg)
	if !ok {
		return
	}
	probe := func() {
		info, ok := discovery.ProbeLSSDP(ctx, host, lssdpTimeout)
		if !ok {
			st.SetLSSDP(nil)
			return
		}
		st.SetLSSDP(&protocol.LSSDPInfo{FW: info.FW, State: info.State, NetMode: info.NetMode})
	}
	wait := lssdpFirstProbeLag
	asked := false // a probe has run: the first one always does (see below)
	for !control.stop.IsSet() && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if control.stop.IsSet() {
			return
		}
		if asked && st.Snap().Connected && !st.ProbeWanted() {
			// connected and nobody is looking at the answer: ask nothing, and
			// look again soon so an opened view gets a fresh probe within
			// seconds. The first probe runs regardless: its firmware build is
			// the connect summary's and the update check's, and the tunnel
			// connects before it is due.
			wait = probeQuietPoll
			continue
		}
		asked = true
		fence(st, control, "lssdp worker", probe) // it parses what the LAN answers
		if st.Snap().Connected {
			wait = lssdpConnected
		} else {
			wait = lssdpDisconnected
		}
	}
}
