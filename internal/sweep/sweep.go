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
	"context"
	"encoding/json"
	"encoding/xml"
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
	"time"
	"unicode/utf8"

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
// so it names no package at all. (A deviceId past the vendor's five offers gets
// that answer too, which is why workers.OTACheck sends a fresh id each time.)
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
// LAN input, and a redirect would send the sweep wherever it names. It takes no
// proxy either: an HTTP_PROXY cannot reach the LAN (discovery's zcClient, too).
var lanClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

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
	build := protocol.FirmwareBuild(r.LSSDP.FW)
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
	// The target, before anything is dialled: discovery hands Run the address
	// a responder chose, and the scan that follows touches every port of it.
	fmt.Fprintln(stderr, "lp10 sweep: "+targetNote(ctx, cfg))
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

// targetNote names the host the sweep is about to scan, the address it
// resolves to when that differs, and where the host came from: discovery, the
// LP10_HOST override or the config.
func targetNote(ctx context.Context, cfg config.Config) string {
	how := "from config"
	switch {
	case cfg.Discovered:
		how = "found on the LAN"
	case os.Getenv(config.HostEnv) != "":
		how = "from " + config.HostEnv
	}
	note := "target " + cfg.Host
	if ip, err := resolveHost(ctx, cfg.Host); err == nil && ip != cfg.Host {
		note += " (" + ip + ")"
	}
	return note + ", " + how
}
