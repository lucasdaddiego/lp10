// Package sweep is `lp10 sweep`: a one-shot, read-only inventory of the box —
// what the September 2026 re-sweep did by hand — diffed against the last one
// and kept as a baseline in the state directory, so "did it update?" is one
// command instead of an afternoon. The baseline is merged fact by fact: what a
// sweep could not read (a stalled ssh, a probe that did not answer) keeps its
// last known value, dated, so the next sweep still has it to compare with.
//
// Three sources, all read-only: the LAN answers that need no ssh (mDNS, the
// LSSDP responder, the Spotify engine's ZeroConf getInfo), the vendor's own
// manifest and CDN (the one thing here that leaves the LAN — this command asks
// on purpose, the TUI never does on its own), and one ssh session running a
// fixed script of reads: versions, the vendor app, hashes of the binaries an
// OTA would replace, the boot reason, the listeners, the Spotify pair, the
// eSDK's reconnects per hour over the syslog history the box keeps on flash,
// and its own last firmware verdict. Nothing is written to the device; the env
// store is queried for a count, never for values.
package sweep

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lucasdaddiego/lp10/internal/atomicfile"
	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/transport"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// Report is one sweep's findings — and the shape of the baseline file. Every
// field is a fact read this run; a probe that did not answer leaves its OK
// false (or its string empty) rather than inventing a value. A saved baseline
// is a merge (see merge): the facts Carried names were not read by its own
// sweep but kept from an earlier one.
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

	// sha256 of the files an OTA or the app loader would replace, by basename;
	// "gone" when the file no longer exists (a file that exists but cannot be
	// read gives no value: that is unread, and the merge carries the last hash)
	Hashes map[string]string `json:"hashes"`

	// boot (ssh)
	BootAt time.Time `json:"bootAt"`
	Reboot string    `json:"reboot"` // reboot_mode: cold_boot / normal / …
	Uptime int       `json:"uptime"` // seconds

	// live surface (ssh)
	TCP          []int    `json:"tcp"`
	UDP          []int    `json:"udp"`
	SpotifyFlags string   `json:"spotifyFlags"` // "SpotifyEnabled/SpotifyProEnabled", e.g. "0/1"; "" unless both were read
	Running      []string `json:"running"`      // streaming daemons found by pidof
	DirtyKeys    int      `json:"dirtyKeys"`    // env rows a runtime setenv wrote (count only)
	DirtyKeysOK  bool     `json:"dirtyKeysOk"`  // the count was read; false when the box gave none (no sqlite3)

	// the eSDK's Spotify link losses over the syslog history: the live file
	// plus the rotated ones on flash (SyslogFiles of them), since
	// ReconnectsSince; Last24h of them in the last 24 clock hours, and the
	// count for each local day of that stretch ("2026-09-22": 42; today's so
	// far) — see history
	Reconnects      int            `json:"reconnects"`
	ReconnectsSince time.Time      `json:"reconnectsSince"`
	SyslogFiles     int            `json:"syslogFiles"`
	Last24h         int            `json:"reconnectsLast24h"`
	ReconnectsByDay map[string]int `json:"reconnectsByDay,omitempty"`

	OTALast string    `json:"otaLast"` // the box's own last verdict: the MsgBox-223 payload ("NO_UPDATE")
	OTAAt   time.Time `json:"otaAt"`   // when the box asked
	SSHErr  string    `json:"sshErr,omitempty"`

	// ssh-free
	LSSDP    LSSDPFacts    `json:"lssdp"`
	ZeroConf ZCFacts       `json:"zeroconf"`
	Manifest ManifestFacts `json:"manifest"`
	Bundle   BundleFacts   `json:"bundle"`

	// Carried is set on a saved baseline only: for each fact its sweep did not
	// read, keyed as in facts ("identity", "mcu", "vendorApp", "hashes.<file>",
	// "spotifyFlags", "dirtyKeys", "reconnects", "otaLast", "lssdp",
	// "zeroconf", "manifest", "bundle"), when the value it holds was read.
	Carried map[string]time.Time `json:"carried,omitempty"`
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
// Build is the build it was asked about: the box's own, or the LSSDP answer's
// when the ssh inventory failed and the report has no Build of its own.
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
// so it names no package at all (the vendor's state since mid-September 2026).
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
	sshTimeout   = 60 * time.Second
	maxSSHOutput = 64 << 10
)

// deviceScript is the fixed read-only inventory the ssh session runs. It is a
// separate one-shot command, so unlike the streaming loop it has no byte
// budget — but it must stay read-only and must never print a secret: the env
// store is asked for a COUNT, the flags are 0/1, the vendor app's log gives
// one known line, its last MsgBox-223 report, and the syslog (whose lines can
// hold the Spotify login blob) gives only COUNTS and STAMPS. Each file is a
// header line — a rotated file on flash as "rot=<its rotation time>", the live
// one as "live=<its first 15 bytes, the stamp>" — then its reconnects per
// clock hour, "h=<count> <stamp cut to the hour>" ("h=     12 Sep 23 18"):
// uniq -c counts them on the box, so the output grows with the hours the
// history spans, not with the reconnects — some 25 KB even for 50 files of a
// whole day each, far under maxSSHOutput. The last line, "end=1", says the
// output was not cut at that cap. Counting the ~50 rotated files costs a few
// seconds of zcat on the box, which a one-shot inventory can afford and the
// loop cannot. The syslog reads send their errors to /dev/null: runSSH takes a
// "Permission denied" on stderr for a rejected password.
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
for p in /usr/bin/luciserver /usr/bin/tcptunnelling /usr/bin/newspotifyhifi /usr/bin/spotifymusicpro /usr/bin/airplaydemo /factory/custom/csys/bin/daemon /factory/custom/factoryEnv.conf /lsync/rakoit_app; do if [ -e $p ]; then h=$(sha256sum $p 2>/dev/null); h=${h%% *}; else h=gone; fi; echo "sha:${p##*/}=$h"; done
while read -r k v x; do [ "$k" = btime ] && echo "btime=$v"; done < /proc/stat
read -r c < /proc/cmdline; c=${c#*reboot_mode=}; echo "rboot=${c%% *}"
read -r up x < /proc/uptime; echo "uptime=${up%.*}"
echo "tcp=$(netstat -tln 2>/dev/null | awk 'NR>2{n=split($4,a,":"); print a[n]}' | sort -un | tr '\n' ' ')"
echo "udp=$(netstat -uln 2>/dev/null | awk 'NR>2{n=split($4,a,":"); print a[n]}' | sort -un | tr '\n' ' ')"
e=$(getenv SpotifyEnabled 2>/dev/null); echo "SpotifyEnabled=${e##*: }"
e=$(getenv SpotifyProEnabled 2>/dev/null); echo "SpotifyProEnabled=${e##*: }"
for d in newspotifyhifi spotifymusicpro airplaydemo dmr bluetoothd tidalConnect qobuzConnect librecast_lite; do pidof $d >/dev/null 2>&1 && echo "run=$d"; done
echo "dirty=$(sqlite3 /data/libre/env/env.db 'select count(*) from ENV_systemENV where dbit=1' 2>/dev/null)"
echo "ota_last=$(grep -aF 'command=223 ' /lsync/app.log 2>/dev/null | tail -1)"
for g in /data/log/syslog/messages*.log.gz; do [ -f "$g" ] && { echo "rot=$(date -r "$g" +%s 2>/dev/null)"; zcat "$g" 2>/dev/null | grep -a 'has been lost' | cut -c1-9 | uniq -c | sed 's/^/h=/'; }; done
m=/var/log/syslog/messages.log; [ -f $m ] && { echo "live=$(head -c 15 $m 2>/dev/null)"; grep -a 'has been lost' $m 2>/dev/null | cut -c1-9 | uniq -c | sed 's/^/h=/'; }
echo end=1
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

// Run performs one sweep against cfg.Host with the given probes. Every string
// from outside — ssh's stderr, the vendor's reply, the CDN's status line and
// headers — is control-stripped (protocol.Printable) before it lands in the
// report: Write prints every field as-is.
func Run(ctx context.Context, cfg config.Config, pr Probes) Report {
	r := Report{At: time.Now(), Host: cfg.Host, Hashes: map[string]string{}}

	// ssh first: the inventory everything else is compared against
	if out, err := pr.SSH(ctx, cfg, deviceScript); err != nil {
		r.SSHErr = protocol.Printable(err.Error())
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

// logFile is one syslog file in the device output: a rotated one on flash,
// with the time rsyslog rotated it (the end of the stretch it covers), or the
// live one, with its first stamp — and its reconnects per clock hour, as the
// box counted them.
type logFile struct {
	end   time.Time // a rotated file's rotation time
	stamp string    // the live file's first 15 bytes
	hours []hourCount
}

// hourCount is one "h=" line: n reconnects in the hour a syslog stamp cut to
// its first 9 bytes names ("Sep 23 18" — no year, like every syslog stamp).
type hourCount struct {
	hour string
	n    int
}

// minRotation is the earliest time a rotation may carry: a file rotated before
// the box's first NTP sync after a boot is stamped 1970.
var minRotation = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// parseDevice reads the device script's key=value output into r. Values are
// control-stripped; ports and counts must parse; unknown keys are ignored.
func parseDevice(r *Report, out string) {
	var (
		rots      []*logFile
		live      *logFile
		file      *logFile // the file the "h=" lines belong to; nil drops them
		complete  bool     // the closing "end=1" arrived: the output was not cut
		hifi, pro string   // the Spotify flag pair, "" when getenv gave no value
	)
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
			hifi = v
		case k == "SpotifyProEnabled":
			pro = v
		case k == "run":
			if v != "" {
				r.Running = append(r.Running, v)
			}
		case k == "dirty":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				r.DirtyKeys, r.DirtyKeysOK = n, true
			}
		case k == "rot":
			// A rotation time that does not parse (the file went between the
			// glob and date -r) or predates NTP drops the file, hours and all.
			file = nil
			if sec, err := strconv.ParseInt(v, 10, 64); err == nil && time.Unix(sec, 0).After(minRotation) {
				file = &logFile{end: time.Unix(sec, 0)}
				rots = append(rots, file)
			}
		case k == "live":
			live = &logFile{stamp: v}
			file = live
		case k == "h":
			c, hour, _ := strings.Cut(v, " ")
			if n, err := strconv.Atoi(c); err == nil && n > 0 && file != nil {
				file.hours = append(file.hours, hourCount{strings.TrimSpace(hour), n})
			}
		case k == "end":
			complete = true
		case k == "ota_last":
			if at, payload, ok := protocol.ParseOTAReport(v, time.Local); ok {
				r.OTALast, r.OTAAt = payload, at
			}
		}
	}
	// The pair is one fact: a half-read one ("0/", "/") would diff as a flag
	// change against the last sweep when it is only a missing read.
	if hifi != "" && pro != "" {
		r.SpotifyFlags = hifi + "/" + pro
	}
	sort.Strings(r.Running)
	// The syslog comes last, so an output cut at maxSSHOutput loses it first:
	// without the closing line the history is unread, never a short count.
	if complete {
		history(r, rots, live)
	}
}

// dayLayout keys ReconnectsByDay: one local calendar day.
const dayLayout = "2006-01-02"

// history turns the files' hourly counts into the reconnect figures and the
// start of the stretch they cover. The oldest rotated file only marks where
// the history begins — its own start is unknown, so its hours are left out —
// and each later file covers the time since the one before it; the live file
// runs to the sweep. With no rotated file, the live file's first stamp starts
// the stretch (the boot, when the stamp predates it: a boot's first lines
// carry the pre-NTP clock). A power loss drops the live lines since the last
// rotation, so across one the counts are a lower bound.
//
// The hours are summed across files — the rotation falls mid-hour, so one
// hour can span two — and each is placed in the year that keeps it at or
// before the sweep (protocol.ParseSyslogTime). An hour that ends before the
// stretch begins is dropped, which is where a pre-NTP stamp lands: "Dec 31
// 21", the 1970 clock in the box's zone, reads as last December (only a
// stretch across New Year's Eve could keep one, as a loss on that evening).
// Last24h is the hour of the sweep and the 23 before it; ReconnectsByDay has
// every local day from the stretch's start to the sweep, the days without one
// as zero.
func history(r *Report, rots []*logFile, live *logFile) {
	sort.Slice(rots, func(i, j int) bool { return rots[i].end.Before(rots[j].end) })
	var counted []*logFile
	switch {
	case len(rots) > 0:
		r.ReconnectsSince = rots[0].end
		counted = append(counted, rots[1:]...)
	case live != nil:
		r.ReconnectsSince = r.BootAt
		if t, ok := protocol.ParseSyslogTime(live.stamp, r.At); ok && !t.Before(r.BootAt) {
			r.ReconnectsSince = t
		}
	default:
		return // no syslog was read: nothing is known
	}
	if live != nil {
		counted = append(counted, live)
	}
	perHour := map[int64]int{} // by the hour's start, in Unix seconds
	for _, f := range counted {
		for _, h := range f.hours {
			if t, ok := protocol.ParseSyslogTime(h.hour+":00:00", r.At); ok && t.Add(time.Hour).After(r.ReconnectsSince) {
				perHour[t.Unix()] += h.n
			}
		}
	}
	loc := r.At.Location()
	thisHour := time.Date(r.At.Year(), r.At.Month(), r.At.Day(), r.At.Hour(), 0, 0, 0, loc)
	since := r.ReconnectsSince.In(loc)
	r.ReconnectsByDay = map[string]int{}
	for d := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, loc); !d.After(r.At); d = d.AddDate(0, 0, 1) {
		r.ReconnectsByDay[d.Format(dayLayout)] = 0
	}
	for start, n := range perHour {
		t := time.Unix(start, 0).In(loc)
		r.Reconnects += n
		r.ReconnectsByDay[t.Format(dayLayout)] += n
		if t.After(thisHour.Add(-24 * time.Hour)) {
			r.Last24h += n
		}
	}
	r.SyslogFiles = len(rots)
}

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
// together — one command on the box, or one probe — under the key Carried
// dates it by. has says whether a report holds a reading of it (what Diff
// needs to compare it), take copies that reading from src into dst, and ssh
// marks the facts the ssh inventory reads.
type fact struct {
	key, label string
	ssh        bool
	has        func(r *Report) bool
	take       func(dst, src *Report)
}

// facts lists the baseline's facts in the report's order, with one for each
// file hash either report names. The identity is what the box answers
// whenever the script runs; the LUCI registers (mcu) and the vendor app's own
// record, /lsync/app-0.json (vendorApp), can each come back empty on their
// own — luciserver restarting, the app loader rewriting its files — so they
// are facts of their own, and an empty read never replaces the version the
// next sweep compares.
func facts(a, b *Report) []fact {
	out := []fact{
		{"identity", "ssh facts", true, func(r *Report) bool { return r.Build != "" }, func(d, s *Report) {
			d.Build, d.BuildDate, d.SVN, d.Kernel = s.Build, s.BuildDate, s.SVN, s.Kernel
			d.BootAt, d.Reboot, d.Uptime = s.BootAt, s.Reboot, s.Uptime
			d.TCP, d.UDP, d.Running = s.TCP, s.UDP, s.Running
		}},
		{"mcu", "mcu", true, func(r *Report) bool { return r.MCU != "" },
			func(d, s *Report) { d.Firmware, d.MCU = s.Firmware, s.MCU }},
		{"vendorApp", "vendor app", true, func(r *Report) bool { return r.VendorApp != "" },
			func(d, s *Report) { d.VendorApp, d.VendorMD5 = s.VendorApp, s.VendorMD5 }},
	}
	names := map[string]bool{}
	for _, h := range []map[string]string{a.Hashes, b.Hashes} {
		for n := range h {
			names[n] = true
		}
	}
	for _, n := range slices.Sorted(maps.Keys(names)) {
		out = append(out, fact{"hashes." + n, "sha256 " + n, true,
			func(r *Report) bool { return r.Hashes[n] != "" },
			func(d, s *Report) { d.Hashes[n] = s.Hashes[n] }})
	}
	return append(out,
		fact{"spotifyFlags", "spotify flags", true, func(r *Report) bool { return r.SpotifyFlags != "" },
			func(d, s *Report) { d.SpotifyFlags = s.SpotifyFlags }},
		fact{"dirtyKeys", "env store", true, func(r *Report) bool { return r.DirtyKeysOK },
			func(d, s *Report) { d.DirtyKeys, d.DirtyKeysOK = s.DirtyKeys, s.DirtyKeysOK }},
		fact{"reconnects", "reconnects", true, func(r *Report) bool { return !r.ReconnectsSince.IsZero() }, func(d, s *Report) {
			d.Reconnects, d.ReconnectsSince, d.SyslogFiles = s.Reconnects, s.ReconnectsSince, s.SyslogFiles
			d.Last24h, d.ReconnectsByDay = s.Last24h, s.ReconnectsByDay
		}},
		fact{"otaLast", "box's own check", true, func(r *Report) bool { return r.OTALast != "" },
			func(d, s *Report) { d.OTALast, d.OTAAt = s.OTALast, s.OTAAt }},
		fact{"lssdp", "lssdp", false, func(r *Report) bool { return r.LSSDP.OK },
			func(d, s *Report) { d.LSSDP = s.LSSDP }},
		fact{"zeroconf", "zeroconf", false, func(r *Report) bool { return r.ZeroConf.OK },
			func(d, s *Report) { d.ZeroConf = s.ZeroConf }},
		fact{"manifest", "manifest", false, func(r *Report) bool { return r.Manifest.Asked && r.Manifest.Err == "" && verdict(r.Manifest) != "" },
			func(d, s *Report) { d.Manifest = s.Manifest }},
		fact{"bundle", "newest bundle", false, func(r *Report) bool { return bundleState(r.Bundle) != "" },
			func(d, s *Report) { d.Bundle = s.Bundle }},
	)
}

// merge is the baseline a sweep leaves: cur, with every fact it did not read
// kept from prev — the baseline it was compared with — and dated in Carried to
// when that value was read, so a fact carried again keeps its first date. A
// fact neither sweep read stays unread. Without the merge, a sweep that lost
// ssh (dropbear stalls rapid reconnects) or one read (no getenv, no sqlite3,
// no syslog) would save a hollow baseline — Diff skips empty pairs, so the
// next sweep would call a real vendor-app update "nothing changed" — or keep
// the old one whole and drop this sweep's fresh ssh-free answers.
func merge(prev *Report, cur Report) Report {
	out := cur
	out.Carried = nil
	out.Hashes = maps.Clone(cur.Hashes) // take writes into it; cur's stays as read
	if out.Hashes == nil {
		out.Hashes = map[string]string{}
	}
	if prev == nil {
		return out
	}
	for _, f := range facts(prev, &cur) {
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
// and as of when: "ssh facts as of Sep 22 10:00 · lssdp as of Sep 21 09:00".
// Facts kept from one sweep share a date and one entry, and the ssh-side facts
// that share the identity's date fold into its "ssh facts". "" when every
// fact is the sweep's own.
func carriedNote(b Report) string {
	type group struct {
		at     time.Time
		labels []string
	}
	var groups []group
	idAt, idCarried := b.Carried["identity"]
	for _, f := range facts(&b, &b) {
		at, ok := b.Carried[f.key]
		if !ok || (f.ssh && f.key != "identity" && idCarried && at.Equal(idAt)) {
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
	cmp("tcp listeners", fixedPorts(prev.TCP, dmrPort), fixedPorts(cur.TCP, dmrPort))
	cmp("udp listeners", fixedPorts(prev.UDP), fixedPorts(cur.UDP))
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
	cmp("newest bundle", bundleState(prev.Bundle), bundleState(cur.Bundle))
	if prev.Bundle.Err == "" && cur.Bundle.Err == "" {
		cmp("bundle etag", prev.Bundle.ETag, cur.Bundle.ETag)
	}
	return out
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

// The Linux ephemeral range (ip_local_port_range's default). A socket bound to
// port 0 gets a new port from it on every start: rakoit_app's second tcp
// listener went 43761 → 33719 → 46835 across app restarts and boots, and three
// udp sockets move the same way. A port in it says nothing about the box.
const ephemeralLo, ephemeralHi = 32768, 60999

// dmrPort is dmr's DLNA control listener: inside the ephemeral range, but
// fixed, so a change there is a real one.
const dmrPort = 49494

// fixedPorts is a listener list as Diff compares it: without the ports in the
// ephemeral range, except the fixed ones named in keep. The report still
// prints the whole list. "" only for an unread list, as fmtPorts; a list with
// nothing but dynamic ports is an answer, so it reads "none fixed".
func fixedPorts(p []int, keep ...int) string {
	if len(p) == 0 {
		return ""
	}
	var fixed []int
	for _, n := range p {
		if n < ephemeralLo || n > ephemeralHi || slices.Contains(keep, n) {
			fixed = append(fixed, n)
		}
	}
	if len(fixed) == 0 {
		return "none fixed"
	}
	return fmtPorts(fixed)
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
	if r.OTALast != "" {
		own := protocol.OTAWords(r.OTALast)
		if !r.OTAAt.IsZero() {
			own += " · " + r.OTAAt.Format("Jan 2 15:04") + " (it asks every 4 h)"
		}
		f("box's own check", own)
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
	f("spotify flags", r.SpotifyFlags)
	f("running", strings.Join(r.Running, " "))
	f("tcp", fmtPorts(r.TCP))
	f("udp", fmtPorts(r.UDP))
	if r.SSHErr == "" {
		if r.DirtyKeysOK {
			f("env store", fmt.Sprintf("%d keys set at runtime", r.DirtyKeys))
		} else {
			f("env store", "not read")
		}
		f("reconnects", reconnectFact(r))
		f(dayFact(r))
	}
	fmt.Fprintln(w)
	// Whether this run becomes the baseline is not known here: Main decides
	// after the report, and says what it did.
	switch {
	case prev == nil:
		fmt.Fprintln(w, "first sweep — nothing to compare with yet")
	default:
		ch := Diff(*prev, r)
		// The baseline can hold facts older than its sweep (kept because that
		// sweep could not read them), or none from ssh at all (every sweep so
		// far lost it): the header says what the diff is really against.
		about := []string{prev.At.Format("2006-01-02 15:04") + ", " + fmtDur(now.Sub(prev.At)) + " ago"}
		if prev.Build == "" {
			about = append(about, "no ssh facts to compare with")
		}
		if note := carriedNote(*prev); note != "" {
			about = append(about, note)
		}
		fmt.Fprintf(w, "since the last sweep (%s)\n", strings.Join(about, " · "))
		if len(ch) == 0 {
			fmt.Fprintln(w, "  nothing changed")
		}
		for _, c := range ch {
			fmt.Fprintf(w, "  %-16s %s → %s\n", c.Field, c.Was, c.Now)
		}
	}
}

// reconnectFact is the reconnect line: the count, where the stretch it covers
// begins, the hourly rate over it (once it spans an hour), the last 24 hours'
// share (once it spans a day), and how much of it came from the rotated files
// — "831 since Sep 1 22:20 · 1.6/h over 21d 19h · 41 in the last 24 h · 49
// rotated files".
func reconnectFact(r Report) string {
	if r.ReconnectsSince.IsZero() {
		return "syslog not read"
	}
	s := fmt.Sprintf("%d since %s", r.Reconnects, r.ReconnectsSince.Format("Jan 2 15:04"))
	if win := r.At.Sub(r.ReconnectsSince); win >= time.Hour {
		s += fmt.Sprintf(" · %.1f/h over %s", float64(r.Reconnects)/win.Hours(), fmtDur(win))
		if win >= 24*time.Hour {
			s += fmt.Sprintf(" · %d in the last 24 h", r.Last24h)
		}
	}
	switch r.SyslogFiles {
	case 0:
		s += " · the live syslog only"
	case 1:
		s += " · 1 rotated file"
	default:
		s += fmt.Sprintf(" · %d rotated files", r.SyslogFiles)
	}
	return s
}

// dayFact is the per-day line under it: the reconnects of each local day,
// oldest first, up to today's so far — "last 7 days", "51 42 36 10 32 20 26
// (Sep 17 → Sep 23)". It holds the days the history reaches, at most seven,
// and is left out under two, where the reconnect line already says it all.
func dayFact(r Report) (label, val string) {
	var counts []string
	day := time.Date(r.At.Year(), r.At.Month(), r.At.Day(), 0, 0, 0, 0, r.At.Location())
	last := day
	for range 7 {
		n, ok := r.ReconnectsByDay[day.Format(dayLayout)]
		if !ok {
			break
		}
		counts = append(counts, strconv.Itoa(n))
		day = day.AddDate(0, 0, -1)
	}
	if len(counts) < 2 {
		return "", ""
	}
	slices.Reverse(counts)
	first := last.AddDate(0, 0, 1-len(counts))
	return fmt.Sprintf("last %d days", len(counts)),
		strings.Join(counts, " ") + " (" + first.Format("Jan 2") + " → " + last.Format("Jan 2") + ")"
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
// baseline in place; then says which it did. Returns the exit code: 0, 1 when
// the ssh inventory failed (the report still prints, and the baseline still
// takes the ssh-free answers), 2 for a bad flag.
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
			// answers, the vendor all report the cancellation). The merge
			// would carry the old facts over those gaps, but they are the
			// user's doing, not the box's: a sweep called off records nothing.
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
	if r.SSHErr != "" {
		return 1
	}
	return 0
}
