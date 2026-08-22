// The LSSDP liveness worker: a one-datagram probe of the device's own UDP:1800
// responder, which answers with no ssh and no auth — so it still works while
// the box's sshd is refusing lp10 (the rapid-reconnect lockout) or while the
// ssh stream is dead for any other reason. It runs fast while disconnected
// (that's when "is the device even there?" matters) and slowly while
// connected, just to keep the diag row fresh.

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
	lssdpFirstProbeLag = 500 * time.Millisecond // let the ssh connect race ahead at startup
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
		info, ok := discovery.ProbeLSSDP(host, lssdpTimeout)
		if !ok {
			st.SetLSSDP(nil)
			return
		}
		st.SetLSSDP(&protocol.LSSDPInfo{
			Name: info.Name, FW: info.FW, State: info.State, NetMode: info.NetMode,
		})
	}
	wait := lssdpFirstProbeLag
	for !control.stop.IsSet() && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if control.stop.IsSet() {
			return
		}
		probe()
		if st.Snap().Connected {
			wait = lssdpConnected
		} else {
			wait = lssdpDisconnected
		}
	}
}
