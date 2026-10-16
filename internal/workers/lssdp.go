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
	probeLoop(ctx, control, st, "lssdp worker", probeCadence{first: lssdpFirstProbeLag, connected: lssdpConnected, disconnected: lssdpDisconnected}, probe)
}

// probeCadence is a probe worker's timing: the lag before the first probe,
// then the wait between probes while the tunnel is up and while it is down.
type probeCadence struct{ first, connected, disconnected time.Duration }

// probeLoop runs probe on cad until stop or ctx: fast while disconnected
// (that's when "is the device even there?" matters), slowly while connected.
// Connected with nobody looking at the answer (State.ProbeWanted false), it
// asks nothing and looks again every probeQuietPoll, so an opened view gets
// a fresh probe within seconds. The first probe runs regardless: the LSSDP
// firmware build is the connect summary's and the update check's, and the
// tunnel connects before it is due. Each probe is fenced: it parses what the
// LAN answers.
func probeLoop(ctx context.Context, control *runControl, st *protocol.State, name string, cad probeCadence, probe func()) {
	wait := cad.first
	asked := false // a probe has run
	for !control.stop.IsSet() && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-control.stop.Done():
			return
		case <-time.After(wait):
		}
		if asked && st.Snap().Connected && !st.ProbeWanted() {
			wait = probeQuietPoll
			continue
		}
		asked = true
		fence(st, control, name, probe)
		if st.Snap().Connected {
			wait = cad.connected
		} else {
			wait = cad.disconnected
		}
	}
}
