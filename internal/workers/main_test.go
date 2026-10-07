package workers

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/discovery"
)

// TestMain makes the package's tests hermetic whatever the shell exports: no
// ambient LP10_* override leaks in (a parallel test points a worker at its
// own fake through config.Config, which an inherited override would beat),
// the state dir is a throwaway (Runtime.Close writes a snapshot there), the
// vendor check is off unless a test points it at its own server, and the
// mDNS lookup never puts multicast on the wire: it finds only what a test
// advertises with advertiseZC.
func TestMain(m *testing.M) {
	for _, v := range []string{"LP10_TUNNEL_ADDR", "LP10_LSSDP_HOST", "LP10_ZC_ADDR", "LP10_HOST", "LP10_DEBUG"} {
		os.Unsetenv(v)
	}
	os.Setenv("LP10_OTA_URL", "")
	dir, err := os.MkdirTemp("", "lp10-workers-state-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("LP10_STATE_DIR", dir)
	zcFind = advertisedZC
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// zcAdverts maps a configured host to the test's stand-in for the mDNS lookup.
var zcAdverts sync.Map // string -> zcFinder

type zcFinder func(host string, ip net.IP) (discovery.SpotifyEndpoint, bool)

// advertisedZC replaces discovery.FindSpotifyZC for the whole package: a host
// no test advertised is simply not found.
func advertisedZC(_ context.Context, host string, ip net.IP, _ time.Duration) (discovery.SpotifyEndpoint, bool) {
	if f, ok := zcAdverts.Load(host); ok {
		return f.(zcFinder)(host, ip)
	}
	return discovery.SpotifyEndpoint{}, false
}

// advertiseZC makes the mDNS stand-in answer for host until the test ends.
// Each test takes its own host, so tests that advertise can run in parallel.
func advertiseZC(t *testing.T, host string, find zcFinder) {
	t.Helper()
	zcAdverts.Store(host, find)
	t.Cleanup(func() { zcAdverts.Delete(host) })
}

// waitFor polls cond every 10 ms until it holds or d runs out, and reports
// whether it held.
func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// eventually fails the test when cond does not hold within d.
func eventually(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	if !waitFor(cond, d) {
		t.Fatalf("timed out after %v waiting for %s", d, what)
	}
}
