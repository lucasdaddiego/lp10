// Package sweep is `lp10 sweep`: a one-shot, read-only inventory of the box —
// what the September 2026 re-sweep did by hand — diffed against the last one
// and kept as a baseline in the state directory, so "did it update?" is one
// command instead of an afternoon.
//
// Three sources, all read-only: the LAN answers that need no ssh (mDNS, the
// LSSDP responder, the Spotify engine's ZeroConf getInfo), the vendor's own
// manifest and CDN (the one thing here that leaves the LAN — this command asks
// on purpose, the TUI never does on its own), and one ssh session running a
// fixed script of reads: versions, the vendor app, hashes of the binaries an
// OTA would replace, the boot reason, the listeners, the Spotify pair, and the
// syslog's reconnect count. Nothing is written to the device; the env store
// is queried for a count, never for values.
package sweep

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/transport"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// Report is one sweep's findings — and the shape of the baseline file. Every
// field is a fact read this run; a probe that did not answer leaves its OK
// false (or its string empty) rather than inventing a value.
type Report struct {
	At   time.Time `json:"at"`
	Host string    `json:"host"`

	// identity (ssh)
	Build     string `json:"build"`     // AR241CE_8530
	BuildDate string `json:"buildDate"` // 2026-01-12
	SVN       string `json:"svn"`       // app_svn_version
	Firmware  string `json:"firmware"`  // LUCI reg 5, the same build string
	MCU       string `json:"mcu"`       // LUCI reg 6
	Kernel    string `json:"kernel"`    // uname -r
	VendorApp string `json:"vendorApp"` // /lsync/app-0.json version
	VendorMD5 string `json:"vendorMd5"` // its recorded md5

	// sha256 of the files an OTA or the app loader would replace, by basename
	Hashes map[string]string `json:"hashes"`

	// boot (ssh)
	BootAt time.Time `json:"bootAt"`
	Reboot string    `json:"reboot"` // reboot_mode: cold_boot / normal / …
	Uptime int       `json:"uptime"` // seconds

	// live surface (ssh)
	TCP          []int    `json:"tcp"`
	UDP          []int    `json:"udp"`
	SpotifyFlags string   `json:"spotifyFlags"` // "SpotifyEnabled/SpotifyProEnabled", e.g. "0/1"
	Running      []string `json:"running"`      // streaming daemons found by pidof
	DirtyKeys    int      `json:"dirtyKeys"`    // env rows a runtime setenv wrote (count only)
	Reconnects   int      `json:"reconnects"`   // eSDK link losses in the retained syslog
	OTALast      string   `json:"otaLast"`      // the box's own last manifest answer, as logged
	SSHErr       string   `json:"sshErr,omitempty"`

	// ssh-free
	LSSDP    LSSDPFacts    `json:"lssdp"`
	ZeroConf ZCFacts       `json:"zeroconf"`
	Manifest ManifestFacts `json:"manifest"`
	Bundle   BundleFacts   `json:"bundle"`
}

// LSSDPFacts is the UDP:1800 responder's answer.
type LSSDPFacts struct {
	OK      bool   `json:"ok"`
	FW      string `json:"fw"`
	State   string `json:"state"`
	NetMode string `json:"netMode"`
	Name    string `json:"name"`
}

// ZCFacts is the Spotify engine's ZeroConf getInfo answer.
type ZCFacts struct {
	OK             bool   `json:"ok"`
	Port           int    `json:"port"`
	LibraryVersion string `json:"libraryVersion"`
	Version        string `json:"version"`
}

// ManifestFacts is the vendor manifest's verdict for the build the box runs.
type ManifestFacts struct {
	Asked    bool   `json:"asked"`
	UpToDate bool   `json:"upToDate"`
	Offered  string `json:"offered"`
	Err      string `json:"err,omitempty"`
}

// BundleFacts describes the newest bundle the vendor serves, learnt by asking
// the manifest about an old build and HEADing what it offers.
type BundleFacts struct {
	Build        string `json:"build"`
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	LastModified string `json:"lastModified"`
	ETag         string `json:"etag"`
	Err          string `json:"err,omitempty"`
}

const (
	probeTimeout = 6 * time.Second
	sshTimeout   = 60 * time.Second
	maxSSHOutput = 64 << 10
)

// deviceScript is the fixed read-only inventory the ssh session runs. It is a
// separate one-shot command, so unlike the streaming loop it has no byte
// budget — but it must stay read-only and must never print a secret: the env
// store is asked for a COUNT, the flags are 0/1, and the syslog is grepped for
// two known lines only.
const deviceScript = `set +e
f=/etc/fwVersion.conf
echo "build=$(grep build_number $f | cut -d'"' -f2)"
echo "build_date=$(grep build_date $f | cut -d'"' -f2)"
echo "svn=$(grep app_svn_version $f | cut -d'"' -f2)"
r=$(LUCI_local -r 5 2>/dev/null); r=${r#*Data:}; echo "fw=${r%% *}"
r=$(LUCI_local -r 6 2>/dev/null); r=${r#*Data:}; echo "mcu=${r%% *}"
echo "kernel=$(uname -r)"
a=$(cat /lsync/app-0.json 2>/dev/null)
v=${a#*\"version\"}; v=${v#*\"}; echo "vapp=${v%%\"*}"
v=${a#*\"md5\"}; v=${v#*\"}; echo "vapp_md5=${v%%\"*}"
for p in /usr/bin/luciserver /usr/bin/tcptunnelling /usr/bin/newspotifyhifi /usr/bin/spotifymusicpro /usr/bin/airplaydemo /factory/custom/csys/bin/daemon /factory/custom/factoryEnv.conf /lsync/rakoit_app; do h=$(sha256sum $p 2>/dev/null); echo "sha:${p##*/}=${h%% *}"; done
while read -r k v x; do [ "$k" = btime ] && echo "btime=$v"; done < /proc/stat
read -r c < /proc/cmdline; c=${c#*reboot_mode=}; echo "rboot=${c%% *}"
read -r up x < /proc/uptime; echo "uptime=${up%.*}"
echo "tcp=$(netstat -tln 2>/dev/null | awk 'NR>2{n=split($4,a,":"); print a[n]}' | sort -un | tr '\n' ' ')"
echo "udp=$(netstat -uln 2>/dev/null | awk 'NR>2{n=split($4,a,":"); print a[n]}' | sort -un | tr '\n' ' ')"
e=$(getenv SpotifyEnabled 2>/dev/null); echo "SpotifyEnabled=${e##*: }"
e=$(getenv SpotifyProEnabled 2>/dev/null); echo "SpotifyProEnabled=${e##*: }"
for d in newspotifyhifi spotifymusicpro airplaydemo dmr bluetoothd tidalConnect qobuzConnect librecast_lite; do pidof $d >/dev/null 2>&1 && echo "run=$d"; done
echo "dirty=$(sqlite3 /data/libre/env/env.db 'select count(*) from ENV_systemENV where dbit=1' 2>/dev/null)"
echo "reconnects=$(grep -c 'has been lost' /var/log/syslog/messages.log 2>/dev/null)"
echo "ota_last=$(grep -h 'error string' /var/log/syslog/messages.log 2>/dev/null | tail -1)"
`

// Probes are the network-side functions Run uses, injectable for tests.
type Probes struct {
	SSH       func(ctx context.Context, cfg config.Config, script string) (string, error)
	LSSDP     func(ctx context.Context, host string, timeout time.Duration) (discovery.LSSDPInfo, bool)
	FindZC    func(ctx context.Context, host string, ip net.IP, timeout time.Duration) (discovery.SpotifyEndpoint, bool)
	ProbeZC   func(ctx context.Context, addr string, timeout time.Duration) (discovery.SpotifyZCInfo, bool)
	Manifest  func(ctx context.Context, url, build string) protocol.OTAInfo
	Head      func(ctx context.Context, url string) (*http.Response, error)
	Manifest0 string // the manifest URL ("" disables the vendor probes)
}

// probesFor builds the probes Main runs with — DefaultProbes, except under
// test, which swaps in fakes so no unit test waits on an unroutable LAN.
var probesFor = DefaultProbes

// DefaultProbes talks to the real box and the real vendor.
func DefaultProbes() Probes {
	url, ok := workers.ManifestURL()
	if !ok {
		url = ""
	}
	return Probes{
		SSH:      runSSH,
		LSSDP:    discovery.ProbeLSSDP,
		FindZC:   discovery.FindSpotifyZC,
		ProbeZC:  discovery.ProbeSpotifyZC,
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

// runSSH runs one read-only script on the box over a fresh ssh connection —
// the same argv, askpass environment and detached session the streaming loop
// uses — and returns its stdout. Dropbear stalls rapid reconnects, so the
// sweep makes exactly one.
func runSSH(ctx context.Context, cfg config.Config, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	argv := append(transport.SSHArgv(cfg), script)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = transport.SpawnEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var out, errb bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, n: maxSSHOutput}
	cmd.Stderr = &errb
	err := cmd.Run()
	if terr := transport.ClassifyStderr(errb.String()); terr != nil {
		return "", terr
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("ssh: %s", msg)
	}
	return out.String(), nil
}

// limitedWriter keeps the first n bytes and drops the rest — a device script
// cannot flood the sweep.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	_, err := l.w.Write(p)
	return len(p), err
}

// Run performs one sweep against cfg.Host with the given probes.
func Run(ctx context.Context, cfg config.Config, pr Probes) Report {
	r := Report{At: time.Now(), Host: cfg.Host, Hashes: map[string]string{}}

	// ssh first: the inventory everything else is compared against
	if out, err := pr.SSH(ctx, cfg, deviceScript); err != nil {
		r.SSHErr = err.Error()
	} else {
		parseDevice(&r, out)
	}

	// ssh-free LAN answers
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

	// the vendor: the verdict for the build the box runs, then the newest
	// bundle it serves (asked for an old build, so it names the package)
	build := r.Build
	if build == "" && r.LSSDP.FW != "" {
		build, _, _ = strings.Cut(r.LSSDP.FW, ".")
	}
	if pr.Manifest0 != "" && build != "" {
		v := pr.Manifest(ctx, pr.Manifest0, build)
		r.Manifest = ManifestFacts{Asked: true, UpToDate: v.UpToDate, Offered: v.Offered, Err: v.Err}
		if prefix, _, ok := strings.Cut(build, "_"); ok {
			old := pr.Manifest(ctx, pr.Manifest0, prefix+"_1")
			switch {
			case old.Err != "":
				r.Bundle.Err = old.Err
			case old.PackageURL == "":
				r.Bundle.Err = "no package named"
			default:
				r.Bundle.Build, r.Bundle.URL = old.Offered, old.PackageURL
				headBundle(ctx, pr, &r.Bundle)
			}
		}
	}
	return r
}

// headBundle fills the bundle's size, date and etag from one HEAD request.
func headBundle(ctx context.Context, pr Probes, b *BundleFacts) {
	hctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	resp, err := pr.Head(hctx, b.URL)
	if err != nil {
		b.Err = "cdn unreachable"
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Err = "cdn answered " + resp.Status
		return
	}
	b.Size = resp.ContentLength
	b.LastModified = resp.Header.Get("Last-Modified")
	b.ETag = strings.Trim(resp.Header.Get("ETag"), `"`)
}

// parseDevice reads the device script's key=value output into r. Values are
// control-stripped; ports and counts must parse; unknown keys are ignored.
func parseDevice(r *Report, out string) {
	for ln := range strings.SplitSeq(out, "\n") {
		k, v, ok := strings.Cut(protocol.Printable(ln), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case k == "build":
			r.Build = v
		case k == "build_date":
			r.BuildDate = v
		case k == "svn":
			r.SVN = v
		case k == "fw":
			r.Firmware = v
		case k == "mcu":
			r.MCU = v
		case k == "kernel":
			r.Kernel = v
		case k == "vapp":
			r.VendorApp = v
		case k == "vapp_md5":
			r.VendorMD5 = v
		case strings.HasPrefix(k, "sha:"):
			if v != "" {
				r.Hashes[strings.TrimPrefix(k, "sha:")] = v
			}
		case k == "btime":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				r.BootAt = time.Unix(n, 0)
			}
		case k == "rboot":
			r.Reboot = v
		case k == "uptime":
			r.Uptime, _ = strconv.Atoi(v)
		case k == "tcp":
			r.TCP = ports(v)
		case k == "udp":
			r.UDP = ports(v)
		case k == "SpotifyEnabled":
			r.SpotifyFlags = v + "/" + after(r.SpotifyFlags, "/")
		case k == "SpotifyProEnabled":
			r.SpotifyFlags = before(r.SpotifyFlags, "/") + "/" + v
		case k == "run":
			if v != "" {
				r.Running = append(r.Running, v)
			}
		case k == "dirty":
			r.DirtyKeys, _ = strconv.Atoi(v)
		case k == "reconnects":
			r.Reconnects, _ = strconv.Atoi(v)
		case k == "ota_last":
			r.OTALast = v
		}
	}
	sort.Strings(r.Running)
}

func before(s, sep string) string { b, _, _ := strings.Cut(s, sep); return b }
func after(s, sep string) string  { _, a, _ := strings.Cut(s, sep); return a }

// ports parses "22 80 2018 " into sorted unique ints.
func ports(s string) []int {
	seen := map[int]bool{}
	var out []int
	for f := range strings.FieldsSeq(s) {
		if n, err := strconv.Atoi(f); err == nil && n > 0 && n < 65536 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// ---- baseline ----

// Load reads the previous sweep from path ("" or a missing/garbled file: nil).
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

// Save writes r as the new baseline (0600: the report holds nothing secret,
// but it is the user's device inventory).
func Save(path string, r Report) error {
	if path == "" {
		return fmt.Errorf("no state directory")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// ---- diff & report ----

// Change is one field that differs from the baseline.
type Change struct{ Field, Was, Now string }

// Diff lists what moved between two sweeps, in a stable order. Probes that did
// not answer this time (or last time) are not "changes" — only two answers that
// disagree are.
func Diff(prev, cur Report) []Change {
	var out []Change
	cmp := func(field, was, now string) {
		if was != "" && now != "" && was != now {
			out = append(out, Change{field, was, now})
		}
	}
	cmp("firmware build", prev.Build, cur.Build)
	cmp("build date", prev.BuildDate, cur.BuildDate)
	cmp("svn", prev.SVN, cur.SVN)
	cmp("mcu", prev.MCU, cur.MCU)
	cmp("kernel", prev.Kernel, cur.Kernel)
	cmp("vendor app", prev.VendorApp, cur.VendorApp)
	cmp("vendor app md5", prev.VendorMD5, cur.VendorMD5)
	var names []string
	for k := range cur.Hashes {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		cmp("sha256 "+k, prev.Hashes[k], cur.Hashes[k])
	}
	if !prev.BootAt.IsZero() && !cur.BootAt.IsZero() && cur.BootAt.Sub(prev.BootAt).Abs() > 2*time.Minute {
		out = append(out, Change{"boot", prev.BootAt.Format("Jan 2 15:04") + " (" + prev.Reboot + ")", cur.BootAt.Format("Jan 2 15:04") + " (" + cur.Reboot + ")"})
	}
	cmp("tcp listeners", fmtPorts(prev.TCP), fmtPorts(cur.TCP))
	cmp("udp listeners", fmtPorts(prev.UDP), fmtPorts(cur.UDP))
	cmp("spotify flags", prev.SpotifyFlags, cur.SpotifyFlags)
	cmp("running", strings.Join(prev.Running, " "), strings.Join(cur.Running, " "))
	if prev.LSSDP.OK && cur.LSSDP.OK {
		cmp("lssdp firmware", prev.LSSDP.FW, cur.LSSDP.FW)
		cmp("lssdp netmode", prev.LSSDP.NetMode, cur.LSSDP.NetMode)
	}
	if prev.ZeroConf.OK && cur.ZeroConf.OK {
		cmp("zeroconf port", strconv.Itoa(prev.ZeroConf.Port), strconv.Itoa(cur.ZeroConf.Port))
		cmp("spotify eSDK", prev.ZeroConf.LibraryVersion, cur.ZeroConf.LibraryVersion)
	}
	if prev.Manifest.Asked && cur.Manifest.Asked && prev.Manifest.Err == "" && cur.Manifest.Err == "" {
		cmp("vendor verdict", verdict(prev.Manifest), verdict(cur.Manifest))
	}
	if prev.Bundle.Err == "" && cur.Bundle.Err == "" {
		cmp("newest bundle", prev.Bundle.Build, cur.Bundle.Build)
		cmp("bundle etag", prev.Bundle.ETag, cur.Bundle.ETag)
	}
	return out
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

// fmtPorts joins ports for display; "" for none (so Diff can tell "unread"
// from "changed").
func fmtPorts(p []int) string {
	if len(p) == 0 {
		return ""
	}
	s := make([]string, len(p))
	for i, n := range p {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, " ")
}

// Write prints the human report: identity, the vendor's view, the live
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
	if r.SSHErr != "" {
		fmt.Fprintf(w, "  ssh            %s (the ssh-side inventory is missing below)\n\n", r.SSHErr)
	}
	fmt.Fprintln(w, "device")
	fw := r.Firmware
	if fw == "" {
		fw = r.Build
	}
	if fw != "" || r.MCU != "" {
		f("firmware", dash(fw)+" · mcu "+dash(r.MCU))
	}
	if r.BuildDate != "" || r.SVN != "" {
		f("build", dash(r.BuildDate)+" · svn "+dash(r.SVN))
	}
	f("kernel", r.Kernel)
	if r.VendorApp != "" {
		f("vendor app", "v"+r.VendorApp+" · md5 "+short(r.VendorMD5))
	}
	if !r.BootAt.IsZero() {
		kind := r.Reboot
		if kind == "cold_boot" {
			kind = "power-on"
		}
		f("boot", r.BootAt.Format("Jan 2 15:04")+" · "+dash(kind)+" · up "+fmtDur(time.Duration(r.Uptime)*time.Second))
	}
	if len(r.Hashes) > 0 {
		var names []string
		for k := range r.Hashes {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			f("sha256 "+k, short(r.Hashes[k]))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "vendor")
	switch {
	case !r.Manifest.Asked:
		f("manifest", "not asked (no build to ask about)")
	case r.Manifest.Err != "":
		f("manifest", "check failed · "+r.Manifest.Err)
	case r.Manifest.UpToDate:
		f("manifest", "no update for "+r.Build)
	default:
		f("manifest", r.Manifest.Offered+" offered for "+r.Build)
	}
	switch {
	case r.Bundle.Err != "":
		f("newest bundle", r.Bundle.Err)
	case r.Bundle.URL != "":
		f("newest bundle", r.Bundle.Build+" · "+r.Bundle.URL)
		f("", fmt.Sprintf("%d bytes · %s · etag %s", r.Bundle.Size, dash(r.Bundle.LastModified), dash(r.Bundle.ETag)))
	}
	f("box's own check", r.OTALast)
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
	f("spotify flags", r.SpotifyFlags)
	f("running", strings.Join(r.Running, " "))
	f("tcp", fmtPorts(r.TCP))
	f("udp", fmtPorts(r.UDP))
	if r.SSHErr == "" {
		f("env store", fmt.Sprintf("%d keys set at runtime", r.DirtyKeys))
		f("reconnects", fmt.Sprintf("%d in the retained syslog", r.Reconnects))
	}
	fmt.Fprintln(w)
	switch {
	case prev == nil:
		fmt.Fprintln(w, "first sweep — baseline saved; run again after a suspected update")
	default:
		ch := Diff(*prev, r)
		fmt.Fprintf(w, "since the last sweep (%s, %s ago)\n", prev.At.Format("2006-01-02 15:04"), fmtDur(now.Sub(prev.At)))
		if len(ch) == 0 {
			fmt.Fprintln(w, "  nothing changed")
		}
		for _, c := range ch {
			fmt.Fprintf(w, "  %-16s %s → %s\n", c.Field, c.Was, c.Now)
		}
	}
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
// prints the report (or the JSON), and saves the baseline — unless the run was
// interrupted, which leaves the previous baseline in place. Returns the exit
// code: 0, 1 when the ssh inventory failed (the report still prints), 2 for a
// bad flag.
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
			// Ctrl-C mid-run degrades every probe at once (ssh, the LAN
			// answers, the vendor all report the cancellation), and Diff
			// skips empty pairs — saved as the baseline, that hollow report
			// would make the next sweep say "nothing changed" after a real
			// OTA. Keep the last complete one.
			fmt.Fprintln(stderr, "lp10 sweep: interrupted — baseline left in place")
		} else if err := Save(path, r); err != nil {
			fmt.Fprintf(stderr, "lp10 sweep: baseline not saved: %v\n", err)
		}
	}
	if r.SSHErr != "" {
		return 1
	}
	return 0
}
