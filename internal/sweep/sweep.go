// Package sweep is `lp10 sweep`: a one-shot, read-only inventory of the box —
// what the September 2026 re-sweeps did by hand — diffed against the last one
// and kept as a baseline in the state directory, so "did it update?" is one
// command instead of an afternoon. The baseline is merged fact by fact: what a
// sweep could not read (a tunnel that never answered, a probe that timed out)
// keeps its last known value, dated, so the next sweep still has it to compare
// with.
//
// It needs no ssh. Firmware AR241CP_8747 removed dropbear, telnet and adb, so
// every read here is one the box answers on the LAN without a login, or one
// the vendor answers. On the LAN: a TCP connect scan of every port (above all,
// to notice ssh, telnet or adb answering again), the :2018 tunnel's read-only
// getters (the MCU's version, its EQ presets and sources, the user's
// settings), the DLNA renderer's UPnP description, the LSSDP responder and the
// Spotify engine's ZeroConf getInfo. From the vendor — the one thing here that
// leaves the LAN; this command asks on purpose, the TUI never does on its own
// — the vendor-app index the box's loader fetches, the manifest's verdict for
// the build the box reports, and the newest bundle it serves. Nothing is
// written to the device: the tunnel gets only the getters in tunnelQueries,
// never a set and never an action code.
package sweep

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/lucasdaddiego/lp10/internal/atomicfile"
	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// Report is one sweep's findings — and the shape of the baseline file. Every
// field is a fact read this run; a probe that did not answer leaves its OK
// false (or its string empty) rather than inventing a value. A saved baseline
// is a merge (see merge): the facts Carried names were not read by its own
// sweep but kept from an earlier one.
//
// The JSON keys never reuse one the ssh-era baseline (2026-09) used for a
// different shape: encoding/json refuses a whole file whose "vendorApp" is a
// string where a struct is expected, and Load would drop that baseline. Its
// other keys (build, mcu, tcp, hashes, …) are simply ignored.
type Report struct {
	At   time.Time `json:"at"`   // when the sweep ran
	Host string    `json:"host"` // the address it swept (the resolved cfg.Host)

	// the box, on the LAN
	Ports    PortFacts   `json:"ports"`    // the TCP connect scan
	Tunnel   TunnelFacts `json:"tunnel"`   // the :2018 getters
	UPnP     UPnPFacts   `json:"upnp"`     // the DLNA renderer's description.xml
	LSSDP    LSSDPFacts  `json:"lssdp"`    // the UDP:1800 responder
	ZeroConf ZCFacts     `json:"zeroconf"` // the Spotify engine's getInfo

	// the vendor
	VendorApp VendorAppFacts `json:"vendorAppIndex"` // the CDN's app index
	Manifest  ManifestFacts  `json:"manifest"`       // the verdict for the box's build
	Bundle    BundleFacts    `json:"bundle"`         // the newest bundle it serves

	// Carried is set on a saved baseline only: for each fact its sweep did not
	// read, keyed as in facts ("ports", "tunnel.ver", "tunnel.peq",
	// "tunnel.lst", "tunnel.settings", "upnp", "lssdp", "zeroconf",
	// "appIndex", "manifest", "bundle"), when the value it holds was read. A
	// baseline from the ssh sweeps can hold keys no fact has any more
	// ("identity", "mcu", "vendorApp", "hashes.…"): they are ignored.
	Carried map[string]time.Time `json:"carried,omitempty"`
}

// PortFacts is the TCP connect scan of the box: every port from 1 to 65535
// that accepted a connection. Only a scan that got an answer from every port
// is OK; a cut-off or failed one says why in Err, keeps the ports it did find
// in Open (so a debug port still shows), and is never compared.
type PortFacts struct {
	OK   bool  `json:"ok"`
	Open []int `json:"open"` // sorted; the dynamic ones included (see isDynamic)
	// DebugChecked says every debug port (22 23 5037 5555) answered. They are
	// scanned first, so even a scan cut off can say ssh, telnet and adb are
	// still closed.
	DebugChecked bool   `json:"debugChecked"`
	Err          string `json:"err,omitempty"`
}

// TunnelFacts is what the :2018 tunnel's getters answered (TEARDOWN §6.3). The
// MCU's version, its EQ presets and its sources are firmware: a change there
// is an update, and Diff compares them. Settings are the user's: printed,
// never compared.
type TunnelFacts struct {
	OK  bool   `json:"ok"`            // the tunnel answered at least one query this sweep
	Err string `json:"err,omitempty"` // why it did not, or why it stopped early

	// MCU is the MCU firmware's version number, VER's first field ("29"): the
	// diagnostics compare it with the live mcu row.
	MCU     string `json:"mcu"`
	Ver     string `json:"ver"`     // VER's whole answer, version-commit-apilevel: "29-1d316f0c-10"
	Presets string `json:"presets"` // PEQ, the EQ presets by index: "0@Flat,1@Classical,…"
	Sources string `json:"sources"` // LST, the inputs the MCU offers: "NET,BT,LINE-IN,USBPLAY"

	// Settings are the other getters' answers by code — STA (the status
	// line), SRC (the current source), MXV, EQE, EQS, BAS, MID, TRE, VBS, VBI
	// and BAL — as the device sent them, control-stripped.
	Settings map[string]string `json:"settings,omitempty"`

	// Unanswered lists the codes this sweep asked and got no answer for.
	Unanswered []string `json:"unanswered,omitempty"`
}

// UPnPFacts is the DLNA renderer's device description (dmr, tcp 49494).
type UPnPFacts struct {
	OK               bool     `json:"ok"`
	FriendlyName     string   `json:"friendlyName"`
	Manufacturer     string   `json:"manufacturer"`
	ModelName        string   `json:"modelName"`
	ModelNumber      string   `json:"modelNumber"`
	ModelDescription string   `json:"modelDescription"`
	Services         []string `json:"services"` // the serviceType URNs, root and embedded devices, sorted
	Err              string   `json:"err,omitempty"`
}

// LSSDPFacts is the UDP:1800 responder's answer.
type LSSDPFacts struct {
	OK      bool   `json:"ok"`
	FW      string `json:"fw"` // FWVERSION, build.mcu.x: "AR241CP_8747.29.2"
	State   string `json:"state"`
	NetMode string `json:"netMode"`
	Name    string `json:"name"`
}

// ZCFacts is the Spotify engine's ZeroConf getInfo answer.
type ZCFacts struct {
	OK             bool   `json:"ok"`
	Port           int    `json:"port"`
	LibraryVersion string `json:"libraryVersion"` // the eSDK build: "3.211.130-g110e3e03"
	Version        string `json:"version"`        // the ZeroConf API version: "2.10.0"
}

// VendorAppFacts is the rakoit_app entry of the vendor's app index on its CDN
// (app-0.json). The box's own loader fetches that file and installs what it
// names, so this is what the vendor serves — what the box runs, or will run
// after its loader's next cycle — not a reading of the box itself.
type VendorAppFacts struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name"`    // "rakoit_app"
	Version string `json:"version"` // "42"
	MD5     string `json:"md5"`
	Err     string `json:"err,omitempty"`
}

// ManifestFacts is the vendor manifest's verdict for the build the box runs.
// Build is the build it was asked about: the LSSDP answer's, cut to its build
// ("AR241CP_8747.29.2" → "AR241CP_8747").
type ManifestFacts struct {
	Asked    bool   `json:"asked"`
	Build    string `json:"build"`
	UpToDate bool   `json:"upToDate"`
	Offered  string `json:"offered"`
	Err      string `json:"err,omitempty"`
}

// BundleFacts describes the newest bundle the vendor serves, learnt by asking
// the manifest about an old build and HEADing what it offers. None is an
// answer, not a failure: the manifest said "no update" even for the old build,
// so it names no package at all (the vendor's state in mid-September 2026).
type BundleFacts struct {
	Build        string `json:"build"`
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	LastModified string `json:"lastModified"`
	ETag         string `json:"etag"`
	None         bool   `json:"none,omitempty"`
	Err          string `json:"err,omitempty"`
}

const (
	probeTimeout = 6 * time.Second

	tunnelPort = 2018
	upnpPort   = 49494 // dmr: the DLNA renderer's description and control

	// appIndexURL is the vendor-app index the box's loader fetches (§10.2).
	appIndexURL = "https://cdn.rakoit-ota.com/download/LP10/app-0.json"

	maxBody  = 64 << 10 // an app index or a UPnP description; the real ones are under 4 KiB
	maxField = 128      // one string from the device or the vendor, in runes
)

// Probes are the network-side functions Run uses, injectable for tests.
type Probes struct {
	// Ports connect-scans host and returns the open ports, sorted, and
	// whether every debug port answered. With an error, the ports are the
	// ones found before the scan stopped.
	Ports func(ctx context.Context, host string) (open []int, debugChecked bool, err error)
	// Tunnel asks the :2018 getters at addr and returns the answers by code.
	// With an error, the answers are the ones that came before it.
	Tunnel   func(ctx context.Context, addr string) (map[string]string, error)
	UPnP     func(ctx context.Context, host string) ([]byte, error) // description.xml, bounded
	LSSDP    func(ctx context.Context, host string, timeout time.Duration) (discovery.LSSDPInfo, bool)
	FindZC   func(ctx context.Context, host string, ip net.IP, timeout time.Duration) (discovery.SpotifyEndpoint, bool)
	ProbeZC  func(ctx context.Context, addr string, timeout time.Duration) (discovery.SpotifyZCInfo, bool)
	AppIndex func(ctx context.Context) ([]byte, error) // app-0.json, bounded
	Manifest func(ctx context.Context, url, build string) protocol.OTAInfo
	Head     func(ctx context.Context, url string) (*http.Response, error)
	// Manifest0 is the manifest URL; "" disables every vendor probe — the
	// manifest, the bundle and the app index — as LP10_OTA_URL="" does.
	Manifest0 string
}

// probesFor builds the probes Main runs with — DefaultProbes, except under
// test, which swaps in fakes so no unit test waits on an unroutable LAN.
var probesFor = DefaultProbes

// lanClient fetches from the box. It follows no redirect: the box's answer is
// LAN input, and a redirect would send the sweep wherever it names.
var lanClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// DefaultProbes talks to the real box and the real vendor.
func DefaultProbes() Probes {
	url, ok := workers.ManifestURL()
	if !ok {
		url = ""
	}
	return Probes{
		Ports: func(ctx context.Context, host string) ([]int, bool, error) {
			return scanPorts(ctx, host, liveScan)
		},
		Tunnel: func(ctx context.Context, addr string) (map[string]string, error) {
			return readTunnel(ctx, addr, liveTunnel)
		},
		UPnP: func(ctx context.Context, host string) ([]byte, error) {
			return getBounded(ctx, lanClient, upnpURL(host), maxBody)
		},
		LSSDP:   discovery.ProbeLSSDP,
		FindZC:  discovery.FindSpotifyZC,
		ProbeZC: discovery.ProbeSpotifyZC,
		AppIndex: func(ctx context.Context) ([]byte, error) {
			return getBounded(ctx, http.DefaultClient, appIndexURL, maxBody)
		},
		Manifest: workers.OTACheck,
		Head: func(ctx context.Context, url string) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
			if err != nil {
				return nil, err
			}
			return http.DefaultClient.Do(req)
		},
		Manifest0: url,
	}
}

// upnpURL is dmr's device description on host.
func upnpURL(host string) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(upnpPort)) + "/description.xml"
}

// Run performs one sweep against cfg.Host with the given probes. Every string
// from outside — the device's answers, the vendor's reply, the CDN's status
// line and headers, an error that quotes them — is control-stripped
// (protocol.Printable) before it lands in the report: Write prints every field
// as-is.
//
// The order is gentle on the box: the tunnel goes first, on a connection of
// its own, because tcptunnelling serves one client at a time and a scan's
// connect to :2018 must not come before it; the port scan goes last.
func Run(ctx context.Context, cfg config.Config, pr Probes) Report {
	r := Report{At: time.Now(), Host: cfg.Host}

	ans, err := pr.Tunnel(ctx, net.JoinHostPort(cfg.Host, strconv.Itoa(tunnelPort)))
	r.Tunnel = tunnelFacts(ans, err)
	if b, err := pr.UPnP(ctx, cfg.Host); err != nil {
		r.UPnP.Err = clip(err.Error(), maxField)
	} else {
		r.UPnP = parseUPnP(b)
	}
	if info, ok := pr.LSSDP(ctx, cfg.Host, probeTimeout); ok {
		r.LSSDP = LSSDPFacts{OK: true, FW: protocol.Printable(info.FW), State: protocol.Printable(info.State),
			NetMode: protocol.Printable(info.NetMode), Name: protocol.Printable(info.Name)}
	}
	if ep, ok := pr.FindZC(ctx, cfg.Host, net.ParseIP(cfg.Host), probeTimeout); ok {
		r.ZeroConf.Port = ep.Port
		if zc, ok := pr.ProbeZC(ctx, ep.Addr(), probeTimeout); ok {
			r.ZeroConf.OK = true
			r.ZeroConf.LibraryVersion, r.ZeroConf.Version = protocol.Printable(zc.LibraryVersion), protocol.Printable(zc.Version)
		}
	}
	open, debugChecked, err := pr.Ports(ctx, cfg.Host)
	r.Ports = PortFacts{OK: err == nil, Open: open, DebugChecked: debugChecked}
	if err != nil {
		r.Ports.Err = clip(err.Error(), maxField)
	}

	if pr.Manifest0 == "" {
		return r
	}
	// the vendor: what its CDN serves the app loader, the verdict for the
	// build the box reports, then the newest bundle it serves (asked for an
	// old build, so it names the package)
	if b, err := pr.AppIndex(ctx); err != nil {
		r.VendorApp.Err = clip(err.Error(), maxField)
	} else {
		r.VendorApp = parseAppIndex(b)
	}
	build, _, _ := strings.Cut(r.LSSDP.FW, ".")
	if build == "" {
		return r
	}
	v := pr.Manifest(ctx, pr.Manifest0, build)
	r.Manifest = ManifestFacts{Asked: true, Build: build, UpToDate: v.UpToDate,
		Offered: protocol.Printable(v.Offered), Err: protocol.Printable(v.Err)}
	if prefix, _, ok := strings.Cut(build, "_"); ok {
		old := pr.Manifest(ctx, pr.Manifest0, prefix+"_1")
		switch {
		case old.Err != "":
			r.Bundle.Err = protocol.Printable(old.Err)
		case old.UpToDate:
			r.Bundle.None = true
		case old.PackageURL == "":
			r.Bundle.Err = "the offer names no usable package"
		default:
			r.Bundle.Build, r.Bundle.URL = protocol.Printable(old.Offered), protocol.Printable(old.PackageURL)
			headBundle(ctx, pr, old.PackageURL, &r.Bundle)
		}
	}
	return r
}

// headBundle fills the bundle's size, date and etag from one HEAD request for
// url — the package as the vendor named it, not its stripped display form.
func headBundle(ctx context.Context, pr Probes, url string, b *BundleFacts) {
	hctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	resp, err := pr.Head(hctx, url)
	if err != nil {
		b.Err = "cdn unreachable"
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Err = "cdn answered " + protocol.Printable(resp.Status)
		return
	}
	b.Size = resp.ContentLength
	b.LastModified = protocol.Printable(resp.Header.Get("Last-Modified"))
	b.ETag = strings.Trim(protocol.Printable(resp.Header.Get("ETag")), `"`)
}

// clip control-strips s and cuts it to n runes, marking the cut: the bound
// every device or vendor string gets before it lands in the report.
func clip(s string, n int) string {
	s = strings.TrimSpace(protocol.Printable(s))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// getBounded GETs url and returns its body: an error for any status but 200,
// and for a body over limit — cut, it would parse as a different answer.
func getBounded(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("answered %s", protocol.Printable(resp.Status))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("reply over %d KiB", limit>>10)
	}
	return b, nil
}

// ---- the port scan ----

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
	dial := s.dial
	if dial == nil {
		d := net.Dialer{Timeout: s.timeout}
		dial = d.DialContext
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
		open = append(open, recheckPorts(ctx, ip, dial, s, open)...)
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
	return addrs[0].IP.String(), nil
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

// ---- the :2018 tunnel ----

// tunnelQueries are the only codes the sweep sends, each as a bare "CODE;"
// query, in this order. Every one is a getter. Never add a code without
// checking it on the Arylic UART API: several act — POP/NXT/PRE/STP change
// playback, SYS:/WRS/DEF:SAV/PMT/COE reboot or reset the box — and the tunnel
// relays whatever it gets straight to the MCU.
var tunnelQueries = []string{"VER", "STA", "SRC", "LST", "MXV", "PEQ", "EQE", "EQS", "BAS", "MID", "TRE", "VBS", "VBI", "BAL"}

// tunnelTiming is how the tunnel client waits: the connect, the first frame
// on a fresh connection, the pause before the one retry, the gap between
// queries, and each query's reply.
type tunnelTiming struct {
	dial, wake, retry, spacing, reply time.Duration
}

// liveTunnel is the timing the real box needs. The device drops a query sent
// right behind another, so they go out 150 ms apart (the TUI's seed spacing).
// A fresh connection can be accepted and never served (seen 2026-10-01), so
// the first frame gets 3 s, and a silent connection one more try 2 s later —
// never a burst: tcptunnelling serves one client at a time, and 26 quick
// connects in a row once left the next one hanging.
var liveTunnel = tunnelTiming{dial: 3 * time.Second, wake: 3 * time.Second, retry: 2 * time.Second,
	spacing: 150 * time.Millisecond, reply: 1500 * time.Millisecond}

const (
	maxTunnelCarry = 4 << 10   // a partial frame kept between reads; the real ones are under 100 bytes
	maxTunnelRead  = 256 << 10 // everything one connection may send before the sweep gives up on it
)

var (
	errSilentTunnel = errors.New("the tunnel accepted the connection but sent nothing")
	errTunnelFlood  = fmt.Errorf("the tunnel sent over %d KiB", maxTunnelRead>>10)
)

// readTunnel asks the tunnelQueries over one connection to addr and returns
// the answers by code. STA goes first, to wake the connection: with no frame
// back in t.wake, the connection is closed and, after t.retry, one more is
// tried. Then each code not answered yet is asked in turn, t.spacing after the
// last; one without an answer in t.reply is left out. The device's other
// frames — the now-playing title, a remote key — arrive in between and are
// dropped, as is any answer to a code the sweep did not ask. With an error,
// the answers are the ones that came before it.
func readTunnel(ctx context.Context, addr string, t tunnelTiming) (map[string]string, error) {
	got := map[string]string{}
	tc, err := wakeTunnel(ctx, addr, t, got)
	if err != nil && ctx.Err() == nil {
		if !pause(ctx, t.retry) {
			return got, ctx.Err()
		}
		if tc, err = wakeTunnel(ctx, addr, t, got); err != nil {
			return got, fmt.Errorf("after a retry: %w", err)
		}
	}
	if err != nil {
		return got, err
	}
	defer tc.close()
	for _, code := range tunnelQueries {
		if _, ok := got[code]; ok {
			continue
		}
		if err := tc.ask(ctx, code, t, got); err != nil {
			return got, err
		}
	}
	return got, nil
}

// tunnelConn is one held tunnel connection: what it carries between reads, what
// it has read in all, what the sweep has asked on it, and when it last asked.
type tunnelConn struct {
	conn  net.Conn
	stop  func() bool // ends the close-on-cancel hook
	buf   []byte
	skip  bool // a dropped run's tail is still arriving: discard up to the next ';'
	read  int
	asked map[string]bool
	last  time.Time
}

// wakeTunnel connects to addr, sends "STA;" and waits t.wake for any frame. A
// connection that sends nothing is closed and errSilentTunnel returned.
func wakeTunnel(ctx context.Context, addr string, t tunnelTiming, got map[string]string) (*tunnelConn, error) {
	d := net.Dialer{Timeout: t.dial}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// A read blocks until its deadline; a cancel (Ctrl-C) closes the socket
	// so the sweep stops at once.
	tc := &tunnelConn{conn: conn, asked: map[string]bool{}, stop: context.AfterFunc(ctx, func() { conn.Close() })}
	if err := tc.send("STA", t); err != nil {
		tc.close()
		return nil, err
	}
	served, err := tc.await("", time.Now().Add(t.wake), got)
	if err == nil && !served {
		err = errSilentTunnel
	}
	if err != nil {
		tc.close()
		return nil, err
	}
	return tc, nil
}

func (c *tunnelConn) close() {
	c.stop()
	c.conn.Close()
}

// ask sends one query, t.spacing after the last, and waits t.reply for its
// answer. A missing answer is not an error; a broken connection is.
func (c *tunnelConn) ask(ctx context.Context, code string, t tunnelTiming, got map[string]string) error {
	if !pause(ctx, t.spacing-time.Since(c.last)) {
		return ctx.Err()
	}
	if err := c.send(code, t); err != nil {
		return err
	}
	_, err := c.await(code, time.Now().Add(t.reply), got)
	return err
}

// send writes the bare query "CODE;" and notes the code as asked.
func (c *tunnelConn) send(code string, t tunnelTiming) error {
	c.asked[code] = true
	c.last = time.Now()
	if err := c.conn.SetWriteDeadline(c.last.Add(t.reply)); err != nil {
		return err
	}
	_, err := c.conn.Write([]byte(code + ";"))
	return err
}

// await reads frames until one answers want ("" for any frame at all) or the
// deadline passes: true when one did, false at the deadline. Every frame that
// answers an asked code lands in got, the first answer kept. A partial frame
// is carried to the next read, up to maxTunnelCarry: a longer run without a
// ';' is dropped, and so is the rest of it, up to the next ';' — kept, its
// tail would glue onto the next frame's code. A connection that sends more
// than maxTunnelRead in all is given up.
func (c *tunnelConn) await(want string, deadline time.Time, got map[string]string) (bool, error) {
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return false, err
	}
	var b [1024]byte
	for {
		n, err := c.conn.Read(b[:])
		c.read += n
		c.buf = append(c.buf, b[:n]...)
		hit := false
		for {
			i := bytes.IndexByte(c.buf, ';')
			if i < 0 {
				break
			}
			frame := strings.TrimSpace(string(c.buf[:i]))
			c.buf = c.buf[i+1:]
			if c.skip {
				c.skip = false
				continue
			}
			if frame != "" && want == "" {
				hit = true
			}
			code, val, ok := parseTunnelFrame(frame)
			if !ok || !c.asked[code] {
				continue
			}
			if _, dup := got[code]; !dup {
				got[code] = val
			}
			hit = hit || code == want
		}
		if len(c.buf) > maxTunnelCarry {
			c.buf, c.skip = nil, true
		}
		c.buf = slices.Clip(c.buf) // the next append starts a fresh array, not one under the read frames
		switch {
		case hit:
			return true, nil
		case c.read > maxTunnelRead:
			return false, errTunnelFlood
		case err == nil:
			continue
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return false, nil
		}
		return false, err
	}
}

// parseTunnelFrame splits "CODE:VALUE" into its code and its control-stripped,
// bounded value. A frame without a ':' (an echoed query) or with a code that
// is not 2–8 capitals and digits is not an answer.
func parseTunnelFrame(frame string) (code, val string, ok bool) {
	code, val, ok = strings.Cut(frame, ":")
	if !ok || len(code) < 2 || len(code) > 8 {
		return "", "", false
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", "", false
		}
	}
	return code, clip(val, maxField), true
}

// pause waits d (nothing for d ≤ 0); false when ctx ended first.
func pause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// tunnelFacts sorts the tunnel's answers into the report: VER, PEQ and LST
// are the firmware's, the rest the settings. Each value is stripped and
// bounded here, whichever probe read it.
func tunnelFacts(ans map[string]string, err error) TunnelFacts {
	t := TunnelFacts{OK: len(ans) > 0}
	if err != nil {
		t.Err = clip(err.Error(), maxField)
	} else if !t.OK {
		t.Err = "no answer to any query"
	}
	for _, code := range tunnelQueries {
		v, ok := ans[code]
		if !ok {
			t.Unanswered = append(t.Unanswered, code)
			continue
		}
		v = clip(v, maxField)
		switch code {
		case "VER":
			t.Ver = v
			t.MCU, _, _ = strings.Cut(v, "-")
		case "PEQ":
			t.Presets = v
		case "LST":
			t.Sources = v
		default:
			if t.Settings == nil {
				t.Settings = map[string]string{}
			}
			t.Settings[code] = v
		}
	}
	if !t.OK {
		t.Unanswered = nil // nothing was asked of a tunnel that never served
	}
	return t
}

// staFields name STA's comma-separated fields, in the device's order.
var staFields = []string{"source", "mute", "volume", "treble", "bass", "net", "internet", "playing", "led", "upgrading"}

// statusFact is STA's answer with its fields named — "source=NET mute=0
// volume=83 …" — or as sent when it does not have the ten fields.
func statusFact(sta string) string {
	parts := strings.Split(sta, ",")
	if len(parts) != len(staFields) {
		return sta
	}
	for i, p := range parts {
		parts[i] = staFields[i] + "=" + p
	}
	return strings.Join(parts, " ")
}

// settingCodes are the settings Write prints on one line, in this order.
var settingCodes = []string{"MXV", "EQE", "EQS", "BAS", "MID", "TRE", "VBS", "VBI", "BAL"}

// settingsFact is the user's settings as "MXV:100 EQE:0 …", the answered ones only.
func settingsFact(s map[string]string) string {
	var parts []string
	for _, code := range settingCodes {
		if v, ok := s[code]; ok {
			parts = append(parts, code+":"+v)
		}
	}
	return strings.Join(parts, " ")
}

// ---- the UPnP description and the app index ----

// upnpDevice is the part of a UPnP device description the sweep reads. The
// tags name local names only, so they match under any namespace the renderer
// declares.
type upnpDevice struct {
	FriendlyName     string `xml:"friendlyName"`
	Manufacturer     string `xml:"manufacturer"`
	ModelName        string `xml:"modelName"`
	ModelNumber      string `xml:"modelNumber"`
	ModelDescription string `xml:"modelDescription"`
	Services         []struct {
		Type string `xml:"serviceType"`
	} `xml:"serviceList>service"`
	Devices []upnpDevice `xml:"deviceList>device"`
}

// maxServices bounds the service list; a renderer has three or four.
const maxServices = 32

// parseUPnP reads a device description: the root device's names, and the
// service types of it and every embedded device, deduplicated and sorted.
// Anything that is not a <root> with a device in it is an error fact.
func parseUPnP(b []byte) UPnPFacts {
	var root struct {
		XMLName xml.Name
		Device  upnpDevice `xml:"device"`
	}
	if err := xml.Unmarshal(b, &root); err != nil {
		return UPnPFacts{Err: "unreadable description"}
	}
	d := root.Device
	u := UPnPFacts{FriendlyName: clip(d.FriendlyName, maxField), Manufacturer: clip(d.Manufacturer, maxField),
		ModelName: clip(d.ModelName, maxField), ModelNumber: clip(d.ModelNumber, maxField),
		ModelDescription: clip(d.ModelDescription, maxField)}
	seen := map[string]bool{}
	var walk func(d upnpDevice)
	walk = func(d upnpDevice) {
		for _, s := range d.Services {
			if t := clip(s.Type, maxField); t != "" && len(seen) < maxServices {
				seen[t] = true
			}
		}
		for _, e := range d.Devices {
			walk(e)
		}
	}
	walk(d)
	u.Services = slices.Sorted(maps.Keys(seen))
	if root.XMLName.Local != "root" || (u.FriendlyName == "" && u.ModelName == "" && len(u.Services) == 0) {
		return UPnPFacts{Err: "not a device description"}
	}
	u.OK = true
	return u
}

// serviceName shortens a standard service URN for display:
// "urn:schemas-upnp-org:service:AVTransport:1" → "AVTransport:1".
func serviceName(urn string) string {
	return strings.TrimPrefix(urn, "urn:schemas-upnp-org:service:")
}

// parseAppIndex reads the vendor's app index — a JSON list of {name, md5,
// param, version} — and keeps the rakoit_app entry.
func parseAppIndex(b []byte) VendorAppFacts {
	var entries []struct {
		Name    string `json:"name"`
		MD5     string `json:"md5"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		return VendorAppFacts{Err: "unexpected reply"}
	}
	for _, e := range entries {
		if e.Name == "rakoit_app" {
			return VendorAppFacts{OK: true, Name: e.Name, Version: clip(e.Version, maxField), MD5: clip(e.MD5, maxField)}
		}
	}
	return VendorAppFacts{Err: "no rakoit_app entry"}
}

// ---- baseline ----

// Load reads the previous sweep from path ("" or a missing/garbled file: nil).
// A baseline from the ssh sweeps loads too: its ssh-side fields are ignored,
// and its LSSDP, ZeroConf, manifest and bundle facts still compare.
func Load(path string) *Report {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r Report
	if json.Unmarshal(b, &r) != nil || r.At.IsZero() {
		return nil
	}
	return &r
}

// Save writes r — the merge Main builds — as the new baseline (0600: the
// report holds nothing secret, but it is the user's device inventory). It goes
// through atomicfile, like the other persisted state: written in place, a kill
// mid-write would leave a truncated file that Load drops, and the next sweep
// would have nothing to compare with.
func Save(path string, r Report) error {
	if path == "" {
		return fmt.Errorf("no state directory")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'))
}

// A fact is one unit of the baseline merge: the fields one read fills
// together, under the key Carried dates it by. has says whether a report holds
// a reading of it (what Diff needs to compare it), and take copies that
// reading from src into dst.
type fact struct {
	key, label string
	has        func(r *Report) bool
	take       func(dst, src *Report)
}

// facts lists the baseline's facts in the report's order. The tunnel's
// answers are four facts, not one: the device drops a query now and then, and
// one missing answer must not take the others' last values with it.
func facts() []fact {
	return []fact{
		{"ports", "tcp ports", func(r *Report) bool { return r.Ports.OK },
			func(d, s *Report) { d.Ports = s.Ports }},
		{"tunnel.ver", "mcu", func(r *Report) bool { return r.Tunnel.Ver != "" },
			func(d, s *Report) { d.Tunnel.Ver, d.Tunnel.MCU = s.Tunnel.Ver, s.Tunnel.MCU }},
		{"tunnel.peq", "eq presets", func(r *Report) bool { return r.Tunnel.Presets != "" },
			func(d, s *Report) { d.Tunnel.Presets = s.Tunnel.Presets }},
		{"tunnel.lst", "sources", func(r *Report) bool { return r.Tunnel.Sources != "" },
			func(d, s *Report) { d.Tunnel.Sources = s.Tunnel.Sources }},
		{"tunnel.settings", "settings", func(r *Report) bool { return len(r.Tunnel.Settings) > 0 },
			func(d, s *Report) { d.Tunnel.Settings = maps.Clone(s.Tunnel.Settings) }},
		{"upnp", "upnp", func(r *Report) bool { return r.UPnP.OK },
			func(d, s *Report) { d.UPnP = s.UPnP }},
		{"lssdp", "lssdp", func(r *Report) bool { return r.LSSDP.OK },
			func(d, s *Report) { d.LSSDP = s.LSSDP }},
		{"zeroconf", "zeroconf", func(r *Report) bool { return r.ZeroConf.OK },
			func(d, s *Report) { d.ZeroConf = s.ZeroConf }},
		{"appIndex", "vendor app", func(r *Report) bool { return r.VendorApp.OK },
			func(d, s *Report) { d.VendorApp = s.VendorApp }},
		{"manifest", "manifest", func(r *Report) bool { return r.Manifest.Asked && r.Manifest.Err == "" && verdict(r.Manifest) != "" },
			func(d, s *Report) { d.Manifest = s.Manifest }},
		{"bundle", "newest bundle", func(r *Report) bool { return bundleState(r.Bundle) != "" },
			func(d, s *Report) { d.Bundle = s.Bundle }},
	}
}

// merge is the baseline a sweep leaves: cur, with every fact it did not read
// kept from prev — the baseline it was compared with — and dated in Carried to
// when that value was read, so a fact carried again keeps its first date. A
// fact neither sweep read stays unread. Without the merge, a sweep that lost
// one read (a tunnel that never answered, a vendor that timed out) would save
// a hollow baseline — Diff skips empty pairs, so the next sweep would call a
// real MCU update "nothing changed" — or keep the old one whole and drop this
// sweep's fresh answers.
func merge(prev *Report, cur Report) Report {
	out := cur
	out.Carried = nil
	if prev == nil {
		return out
	}
	for _, f := range facts() {
		if f.has(&cur) || !f.has(prev) {
			continue
		}
		f.take(&out, prev)
		if out.Carried == nil {
			out.Carried = map[string]time.Time{}
		}
		out.Carried[f.key] = prev.readAt(f.key)
	}
	return out
}

// readAt is when r's value for the fact key was read: its Carried date, or
// r's own sweep.
func (r *Report) readAt(key string) time.Time {
	if at, ok := r.Carried[key]; ok {
		return at
	}
	return r.At
}

// carriedNote names the facts a baseline holds from before its own sweep,
// and as of when: "mcu, eq presets as of Sep 22 10:00 · lssdp as of Sep 21
// 09:00". Facts kept from one sweep share a date and one entry. "" when every
// fact is the sweep's own.
func carriedNote(b Report) string {
	type group struct {
		at     time.Time
		labels []string
	}
	var groups []group
	for _, f := range facts() {
		at, ok := b.Carried[f.key]
		if !ok {
			continue
		}
		i := slices.IndexFunc(groups, func(g group) bool { return g.at.Equal(at) })
		if i < 0 {
			groups = append(groups, group{at: at})
			i = len(groups) - 1
		}
		groups[i].labels = append(groups[i].labels, f.label)
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = strings.Join(g.labels, ", ") + " as of " + g.at.Format("Jan 2 15:04")
	}
	return strings.Join(parts, " · ")
}

// ---- diff & report ----

// Change is one field that differs from the baseline.
type Change struct{ Field, Was, Now string }

// Diff lists what moved between two sweeps, in a stable order. Probes that did
// not answer this time (or last time) are not "changes" — only two answers that
// disagree are. The tunnel's settings are never compared: a change there is
// the user's, not an update.
func Diff(prev, cur Report) []Change {
	var out []Change
	cmp := func(field, was, now string) {
		if was != "" && now != "" && was != now {
			out = append(out, Change{field, was, now})
		}
	}
	cmp("tcp ports", portsState(prev.Ports), portsState(cur.Ports))
	cmp("mcu", prev.Tunnel.Ver, cur.Tunnel.Ver)
	cmp("eq presets", prev.Tunnel.Presets, cur.Tunnel.Presets)
	cmp("sources", prev.Tunnel.Sources, cur.Tunnel.Sources)
	if prev.UPnP.OK && cur.UPnP.OK {
		cmp("upnp model", prev.UPnP.ModelNumber, cur.UPnP.ModelNumber)
		cmp("upnp services", servicesFact(prev.UPnP.Services), servicesFact(cur.UPnP.Services))
	}
	if prev.LSSDP.OK && cur.LSSDP.OK {
		cmp("lssdp firmware", prev.LSSDP.FW, cur.LSSDP.FW)
		cmp("lssdp netmode", prev.LSSDP.NetMode, cur.LSSDP.NetMode)
	}
	if prev.ZeroConf.OK && cur.ZeroConf.OK {
		cmp("zeroconf port", strconv.Itoa(prev.ZeroConf.Port), strconv.Itoa(cur.ZeroConf.Port))
		cmp("spotify eSDK", prev.ZeroConf.LibraryVersion, cur.ZeroConf.LibraryVersion)
	}
	cmp("vendor app", prev.VendorApp.Version, cur.VendorApp.Version)
	cmp("vendor app md5", prev.VendorApp.MD5, cur.VendorApp.MD5)
	if prev.Manifest.Asked && cur.Manifest.Asked && prev.Manifest.Err == "" && cur.Manifest.Err == "" {
		cmp("vendor verdict", verdict(prev.Manifest), verdict(cur.Manifest))
	}
	cmp("newest bundle", bundleState(prev.Bundle), bundleState(cur.Bundle))
	if prev.Bundle.Err == "" && cur.Bundle.Err == "" {
		cmp("bundle etag", prev.Bundle.ETag, cur.Bundle.ETag)
	}
	return out
}

// servicesFact is a service list for display, the standard prefix shortened.
func servicesFact(s []string) string {
	names := make([]string, len(s))
	for i, urn := range s {
		names[i] = serviceName(urn)
	}
	return strings.Join(names, " ")
}

// bundleState is the newest-bundle answer as one comparable word: the build
// offered, "none offered", or "" when the probe failed. Only two answers make
// a change, so a failed probe never reads as one — but the vendor starting, or
// stopping, to offer a package does.
func bundleState(b BundleFacts) string {
	switch {
	case b.Err != "":
		return ""
	case b.None:
		return "none offered"
	}
	return b.Build
}

func verdict(m ManifestFacts) string {
	if m.UpToDate {
		return "up to date"
	}
	if m.Offered != "" {
		return m.Offered + " offered"
	}
	return ""
}

// Write prints the human report: the device, the vendor's view, the live
// surface, then the diff against prev (or that this is the first sweep).
func Write(w io.Writer, r Report, prev *Report, now time.Time) {
	f := func(label, val string) {
		if val != "" {
			fmt.Fprintf(w, "  %-14s %s\n", label, val)
		}
	}
	dash := func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	}
	fmt.Fprintf(w, "lp10 sweep · %s · %s\n\n", r.Host, r.At.Format("2006-01-02 15:04"))
	fmt.Fprintln(w, "device")
	if r.LSSDP.FW != "" || r.Tunnel.Ver != "" {
		f("firmware", dash(r.LSSDP.FW)+" · mcu "+dash(r.Tunnel.Ver))
	}
	if !r.Tunnel.OK {
		f("tunnel", dash(r.Tunnel.Err)+" (the mcu, its presets, sources and settings are unread)")
	}
	if r.Tunnel.Sources != "" {
		src := r.Tunnel.Sources
		if now := r.Tunnel.Settings["SRC"]; now != "" {
			src += " · now " + now
		}
		f("sources", src)
	}
	f("eq presets", r.Tunnel.Presets)
	f("status", statusFact(r.Tunnel.Settings["STA"]))
	f("settings", settingsFact(r.Tunnel.Settings))
	if r.Tunnel.OK && len(r.Tunnel.Unanswered) > 0 {
		missing := strings.Join(r.Tunnel.Unanswered, " ")
		if r.Tunnel.Err != "" {
			missing += " (" + r.Tunnel.Err + ")"
		}
		f("no answer", missing)
	}
	if r.UPnP.OK {
		f("upnp", joinSet(" · ", r.UPnP.FriendlyName, r.UPnP.Manufacturer,
			joinSet(" ", r.UPnP.ModelName, r.UPnP.ModelNumber), r.UPnP.ModelDescription))
		f("upnp services", servicesFact(r.UPnP.Services))
	} else {
		f("upnp", "no description · "+dash(r.UPnP.Err))
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "vendor")
	switch {
	case !r.Manifest.Asked && r.LSSDP.FW == "":
		f("manifest", "not asked (no build to ask about)")
	case !r.Manifest.Asked:
		f("manifest", "not asked")
	case r.Manifest.Err != "":
		f("manifest", "check failed · "+r.Manifest.Err)
	case r.Manifest.UpToDate:
		f("manifest", "no update for "+r.Manifest.Build)
	default:
		f("manifest", r.Manifest.Offered+" offered for "+r.Manifest.Build)
	}
	switch {
	case r.Bundle.Err != "":
		f("newest bundle", r.Bundle.Err)
	case r.Bundle.None:
		f("newest bundle", "none offered — the manifest names no package, even for an old build")
	case r.Bundle.URL != "":
		f("newest bundle", r.Bundle.Build+" · "+r.Bundle.URL)
		f("", fmt.Sprintf("%d bytes · %s · etag %s", r.Bundle.Size, dash(r.Bundle.LastModified), dash(r.Bundle.ETag)))
	}
	switch {
	case r.VendorApp.OK:
		f("vendor app", "the vendor serves "+r.VendorApp.Name+" v"+r.VendorApp.Version+" · md5 "+short(r.VendorApp.MD5))
	case r.VendorApp.Err != "":
		f("vendor app", "index unread · "+r.VendorApp.Err)
	default:
		f("vendor app", "not asked")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "live surface")
	if r.LSSDP.OK {
		f("lssdp", r.LSSDP.FW+" · "+r.LSSDP.State+" · "+r.LSSDP.NetMode+" · "+r.LSSDP.Name)
	} else {
		f("lssdp", "no answer")
	}
	switch {
	case r.ZeroConf.OK:
		f("spotify", fmt.Sprintf(":%d · eSDK %s · zeroconf %s", r.ZeroConf.Port, dash(r.ZeroConf.LibraryVersion), dash(r.ZeroConf.Version)))
	case r.ZeroConf.Port != 0:
		f("spotify", fmt.Sprintf(":%d advertised · getInfo unanswered", r.ZeroConf.Port))
	default:
		f("spotify", "not advertised (no engine running)")
	}
	writePorts(f, r.Ports)
	fmt.Fprintln(w)

	// Whether this run becomes the baseline is not known here: Main decides
	// after the report, and says what it did.
	if prev == nil {
		fmt.Fprintln(w, "first sweep — nothing to compare with yet")
		return
	}
	// The baseline can hold facts older than its sweep (kept because that
	// sweep could not read them): the header says what the diff is really
	// against.
	about := []string{prev.At.Format("2006-01-02 15:04") + ", " + fmtDur(now.Sub(prev.At)) + " ago"}
	if note := carriedNote(*prev); note != "" {
		about = append(about, note)
	}
	fmt.Fprintf(w, "since the last sweep (%s)\n", strings.Join(about, " · "))
	ch := Diff(*prev, r)
	if len(ch) == 0 {
		fmt.Fprintln(w, "  nothing changed")
	}
	for _, c := range ch {
		fmt.Fprintf(w, "  %-16s %s → %s\n", c.Field, c.Was, c.Now)
	}
	// A fact this sweep read and the baseline never held — every new probe,
	// against a baseline from the ssh sweeps — has nothing to differ from.
	var fresh []string
	for _, fa := range facts() {
		if fa.has(&r) && !fa.has(prev) {
			fresh = append(fresh, fa.label)
		}
	}
	if len(fresh) > 0 {
		fmt.Fprintf(w, "  %-16s %s (nothing to compare with yet)\n", "first read", strings.Join(fresh, ", "))
	}
}

// writePorts prints the scan: the fixed ports, the dynamic ones apart (they
// move with every app restart, so they are never compared), and whether the
// debug listeners the firmware removed answer again — said even for a scan
// cut off, as long as it found one, or heard from all four.
func writePorts(f func(label, val string), p PortFacts) {
	var fixed, dynamic []int
	for _, n := range p.Open {
		if isDynamic(n) {
			dynamic = append(dynamic, n)
		} else {
			fixed = append(fixed, n)
		}
	}
	switch {
	case !p.OK && len(p.Open) == 0:
		f("tcp", "scan failed · "+p.Err)
	case !p.OK:
		f("tcp", fmtPorts(p.Open)+" · so far: "+p.Err)
	default:
		if len(fixed) == 0 {
			f("tcp", "nothing fixed open")
		} else {
			f("tcp", fmtPorts(fixed))
		}
		if len(dynamic) > 0 {
			f("dynamic tcp", fmtPorts(dynamic)+" (they move with each app restart; not compared)")
		}
	}
	var again []string
	var all []string
	for _, d := range debugPorts {
		all = append(all, strconv.Itoa(d.port))
		if slices.Contains(p.Open, d.port) {
			again = append(again, fmt.Sprintf("%d (%s)", d.port, d.name))
		}
	}
	switch {
	case len(again) > 0:
		f("ssh/telnet/adb", "answer again: "+strings.Join(again, " · "))
	case p.DebugChecked:
		f("ssh/telnet/adb", "closed ("+strings.Join(all, " ")+")")
	}
}

// joinSet joins the parts that are set, so a missing one leaves no gap.
func joinSet(sep string, parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), sep)
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// Main is the `lp10 sweep` entry point: parses the flags, runs the sweep,
// prints the report (or the JSON), and saves the new baseline — this sweep
// merged over the last one, so a fact it could not read keeps its last value
// and date — unless the run was interrupted, which leaves the previous
// baseline in place; then says which it did. Returns the exit code: 0; 1 when
// the box's own inventory is incomplete — the port scan or the tunnel failed
// (the report still prints, and the baseline still takes what was read); 2 for
// a bad flag.
func Main(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lp10 sweep", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the sweep as JSON (the baseline's shape) instead of the report")
	noSave := fs.Bool("no-save", false, "do not replace the saved baseline")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "lp10 sweep: takes no positional arguments")
		return 2
	}
	path := config.SweepPath(cfg)
	prev := Load(path)
	r := Run(ctx, cfg, probesFor())
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		Write(stdout, r, prev, time.Now())
	}
	if !*noSave {
		if ctx.Err() != nil {
			// Ctrl-C mid-run degrades every probe at once (the scan, the
			// tunnel, the LAN answers, the vendor all report the
			// cancellation). The merge would carry the old facts over those
			// gaps, but they are the user's doing, not the box's: a sweep
			// called off records nothing.
			fmt.Fprintln(stderr, "lp10 sweep: interrupted — baseline left in place")
		} else {
			b := merge(prev, r)
			if err := Save(path, b); err != nil {
				fmt.Fprintf(stderr, "lp10 sweep: baseline not saved: %v\n", err)
			} else if !*asJSON {
				saved := "baseline saved"
				if note := carriedNote(b); note != "" {
					saved += " (kept from earlier sweeps: " + note + ")"
				}
				fmt.Fprintln(stdout, "\n"+saved+"; run again after a suspected update")
			}
		}
	}
	if !r.Ports.OK || !r.Tunnel.OK {
		return 1
	}
	return 0
}
