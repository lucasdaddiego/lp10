// The port scan: a TCP connect scan of every port of the box, the ports
// confirmed one by one afterwards, and the helpers that word the result.

package sweep

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// scanSpec is how a port scan runs: the range, how many connects are in
// flight at once, each connect's timeout, the whole scan's budget, the ports
// to confirm afterwards and the patience for them, and the dialer (a fake in
// tests).
type scanSpec struct {
	lo, hi  int
	workers int
	timeout time.Duration
	budget  time.Duration
	// recheck are the ports a fast pass may not call closed on its own: each
	// one it did not find open is dialled again, up to recheckTries times
	// with recheckTimeout, before the scan says it is closed.
	recheck        []int
	recheckTimeout time.Duration
	recheckTries   int
	dial           func(ctx context.Context, network, addr string) (net.Conn, error)
}

// liveScan scans every port. On AR241CP_8747 a closed port does not refuse a
// connect at once: the refusal comes after about 1 s (measured 2026-10-01 —
// most likely the box drops the first SYN and resets the retransmit), while an
// open one accepts in about 25 ms. So every closed port costs the whole
// timeout, and the scan runs at workers/timeout ports a second: 256 connects
// of 800 ms covered 9,478 ports in 30 s. A 400 ms timeout still tells an open
// port from a closed one many times over, and 768 in flight cover the range in
// about 35 s; the budget leaves room for a slower LAN.
//
// At that rate the box also drops a SYN to an OPEN port now and then: the
// first saved 8747 baseline (2026-10-01) lacked 9095 while the ZeroConf probe
// of the same sweep answered on it. So the listeners the box is known to run,
// and the debug ports, are confirmed one by one afterwards (recheck): a fast
// pass alone would turn a dropped SYN into "closed" and the next sweep into a
// false "opened".
var liveScan = scanSpec{lo: 1, hi: 65535, workers: 768, timeout: 400 * time.Millisecond, budget: 60 * time.Second,
	recheck: knownPorts, recheckTimeout: 1500 * time.Millisecond, recheckTries: 2}

// knownPorts are the tcp listeners the box has had on any firmware swept so
// far (TEARDOWN §7: 8530 and 8747), the debug ports included.
var knownPorts = []int{22, 23, 80, 2018, 2345, 5000, 5037, 5555, 7000, 7777, 9095, 9096, 49494}

// scanPorts connect-scans host's TCP ports s.lo..s.hi and returns the ones
// that accepted, sorted; each is closed at once, nothing is sent. A refused or
// timed-out connect is a closed port. Any other failure (no route to host, a
// sandbox's "operation not permitted") says nothing about the port, and would
// say the same about every other: the scan stops with that error. A scan cut
// off by its budget returns what it found and says how far it got. The debug
// ports in the range go first, and debugChecked says each of them answered.
func scanPorts(ctx context.Context, host string, s scanSpec) (open []int, debugChecked bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()
	ip, err := resolveHost(ctx, host)
	if err != nil {
		return nil, false, err
	}
	dial, redial := s.dial, s.dial
	if dial == nil {
		d := net.Dialer{Timeout: s.timeout}
		dial = d.DialContext
		// the recheck's own patience: a Dialer stops at the EARLIER of its
		// Timeout and the ctx deadline, so the fast dialer would cap it at s.timeout
		r := net.Dialer{Timeout: s.recheckTimeout}
		redial = r.DialContext
	}
	var first []int // the debug ports in the range, scanned before the rest
	for _, d := range debugPorts {
		if d.port >= s.lo && d.port <= s.hi && !slices.Contains(first, d.port) {
			first = append(first, d.port)
		}
	}
	var (
		mu       sync.Mutex
		answered int
		debugOK  int // the debug ports that answered
		fatal    error
		wg       sync.WaitGroup
	)
	ports := make(chan int)
	for range min(s.workers, s.hi-s.lo+1) {
		wg.Go(func() {
			for p := range ports {
				c, err := dial(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(p)))
				mu.Lock()
				switch {
				case err == nil:
					c.Close()
					open = append(open, p)
				case ctx.Err() != nil: // the budget or the caller ended it: no answer
					mu.Unlock()
					continue
				case !portClosed(err):
					if fatal == nil {
						fatal = err
					}
					cancel()
					mu.Unlock()
					continue
				}
				answered++
				if slices.Contains(first, p) {
					debugOK++
				}
				mu.Unlock()
			}
		})
	}
	feed := func(p int) bool {
		select {
		case ports <- p:
			return true
		case <-ctx.Done():
			return false
		}
	}
	ok := true
	for _, p := range first {
		if ok = feed(p); !ok {
			break
		}
	}
	for p := s.lo; ok && p <= s.hi; p++ {
		if !slices.Contains(first, p) {
			ok = feed(p)
		}
	}
	close(ports)
	wg.Wait()
	if fatal == nil && ctx.Err() == nil {
		open = append(open, recheckPorts(ctx, ip, redial, s, open)...)
		if ctx.Err() != nil { // the budget cut the recheck: a missed known port is unconfirmed
			fatal = fmt.Errorf("scan cut off: the recheck of the known ports ran past %s", s.budget)
		}
	}
	slices.Sort(open)
	debugChecked = debugOK == len(first)
	total := s.hi - s.lo + 1
	switch {
	case fatal != nil:
		return open, debugChecked, fatal
	case answered < total:
		return open, debugChecked, fmt.Errorf("scan cut off: %d of %d ports answered in %s", answered, total, s.budget)
	}
	return open, debugChecked, nil
}

// recheckPorts dials each of s.recheck the fast pass did not find open — in
// parallel, there are a dozen — up to s.recheckTries times with
// s.recheckTimeout, and returns the ones that accepted.
func recheckPorts(ctx context.Context, ip string, dial func(context.Context, string, string) (net.Conn, error), s scanSpec, open []int) []int {
	var (
		mu    sync.Mutex
		found []int
		wg    sync.WaitGroup
	)
	for _, p := range s.recheck {
		if p < s.lo || p > s.hi || slices.Contains(open, p) {
			continue
		}
		wg.Go(func() {
			for range s.recheckTries {
				dctx, cancel := context.WithTimeout(ctx, s.recheckTimeout)
				c, err := dial(dctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(p)))
				cancel()
				if err == nil {
					c.Close()
					mu.Lock()
					found = append(found, p)
					mu.Unlock()
					return
				}
				if ctx.Err() != nil {
					return
				}
			}
		})
	}
	wg.Wait()
	return found
}

// portClosed says a connect's failure is the port's answer: refused (a reset)
// or no answer within the timeout (dropped).
func portClosed(err error) bool {
	var ne net.Error
	return errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &ne) && ne.Timeout())
}

// resolveHost is host's address, an IPv4 one when it has one: resolved once,
// not once per port (a .local name goes through mDNS).
func resolveHost(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 { // the resolver errs first; this only keeps the index safe
		return "", fmt.Errorf("no address for %s", host)
	}
	for _, a := range addrs {
		if a.IP.To4() != nil {
			return a.IP.String(), nil
		}
	}
	return addrs[0].String(), nil // IPAddr.String keeps a link-local zone (fe80::1%en0)
}

// The Linux ephemeral range (ip_local_port_range's default). A socket bound to
// port 0 gets a new port from it on every start: rakoit_app's second tcp
// listener went 43761 → 33719 → 46835 → 44317 across app restarts and boots.
// A port in it says nothing about the firmware.
const ephemeralLo, ephemeralHi = 32768, 60999

// isDynamic says a port moves by itself: in the ephemeral range, except dmr's
// 49494, which is fixed there (the UPnP probe depends on it).
func isDynamic(p int) bool {
	return p >= ephemeralLo && p <= ephemeralHi && p != upnpPort
}

// debugPorts are the listeners AR241CP_8747 removed, with their owners:
// dropbear, inetd's telnet, adbd's local and network ports. One of them open
// again is the first thing the scan has to say.
var debugPorts = []struct {
	port int
	name string
}{{22, "ssh"}, {23, "telnet"}, {5037, "adb"}, {5555, "adb"}}

// portsState is the scan as Diff compares it: the fixed ports, "none" for a
// scan that found none, "" for no scan to compare.
func portsState(p PortFacts) string {
	if !p.OK {
		return ""
	}
	fixed := slices.DeleteFunc(slices.Clone(p.Open), isDynamic)
	if len(fixed) == 0 {
		return "none"
	}
	return fmtPorts(fixed)
}

// fmtPorts joins ports for display.
func fmtPorts(p []int) string {
	s := make([]string, len(p))
	for i, n := range p {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, " ")
}
