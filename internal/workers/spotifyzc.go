// The Spotify ZeroConf worker: the engine's own unauthenticated HTTP endpoint,
// found over mDNS (_spotify-connect._tcp) and read with one GET. Like the LSSDP
// probe it needs neither ssh nor the tunnel, so it keeps answering "is Spotify
// actually up, on which eSDK, signed in as whom" while the box's sshd is
// refusing lp10 — and it is the only surface that reports the signed-in user
// at all. The port comes from the SRV record every time the endpoint is
// (re)found: firmware 8530 moved it from 9095 to 9096, and the next OTA may
// move it again.

package workers

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

const (
	zcFindTimeout    = 1500 * time.Millisecond
	zcResolveTimeout = 1500 * time.Millisecond
	zcProbeTimeout   = 2 * time.Second
	zcDisconnected   = 10 * time.Second
	zcConnected      = 30 * time.Second
	zcFirstProbeLag  = time.Second // let ssh, the tunnel and LSSDP go first at startup
)

// zcTarget is the worker's target: the configured host (endpoint found over
// mDNS), or LP10_ZC_ADDR — a fixed host:port that skips mDNS (tests point it
// at a local HTTP server); set-but-empty disables the worker entirely, as the
// hermetic e2e runs do.
func zcTarget(cfg config.Config) (host, fixed string, enabled bool) {
	if a, set := os.LookupEnv("LP10_ZC_ADDR"); set {
		return "", a, a != ""
	}
	return cfg.Host, "", cfg.Host != ""
}

// zcResolve turns the configured host into the IPv4 the mDNS A records are
// matched against (nil when it is unresolvable — matching then falls back to
// the SRV host name).
func zcResolve(ctx context.Context, host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4()
	}
	rctx, cancel := context.WithTimeout(ctx, zcResolveTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if ip4 := a.IP.To4(); ip4 != nil {
			return ip4
		}
	}
	return nil
}

// zcFind is the mDNS lookup, a var so tests can stand in a fake advertiser
// without putting multicast on the wire (mirroring workers.classify).
var zcFind = discovery.FindSpotifyZC

func zcWorker(ctx context.Context, control *runControl, st *protocol.State, cfg config.Config) {
	host, fixed, ok := zcTarget(cfg)
	if !ok {
		return
	}
	addr, port := fixed, 0
	if fixed != "" {
		if _, p, err := net.SplitHostPort(fixed); err == nil {
			port = atoiOrZero(p)
		}
	}
	probe := func() {
		if addr == "" {
			ep, found := zcFind(host, zcResolve(ctx, host), zcFindTimeout)
			if !found {
				st.SetSpotifyZC(nil, 0)
				return
			}
			addr, port = ep.Addr(), ep.Port
		}
		info, ok := discovery.ProbeSpotifyZC(addr, zcProbeTimeout)
		if !ok {
			st.SetSpotifyZC(nil, port)
			if fixed == "" {
				addr = "" // the engine may have moved (or restarted on a new port): re-find it next time
			}
			return
		}
		st.SetSpotifyZC(&protocol.SpotifyZC{
			Status: info.Status, StatusString: info.StatusString, ActiveUser: info.ActiveUser,
			LibraryVersion: info.LibraryVersion, RemoteName: info.RemoteName,
		}, port)
	}
	wait := zcFirstProbeLag
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
			wait = zcConnected
		} else {
			wait = zcDisconnected
		}
	}
}

// atoiOrZero parses a port string leniently: a test override with a bad port
// simply reports port 0 rather than failing the worker.
func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return 0
		}
	}
	return n
}
