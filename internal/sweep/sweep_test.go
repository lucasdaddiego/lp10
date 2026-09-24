package sweep

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// The device script's output as the box printed it on 2026-09-12, with the
// syslog history and the vendor app's MsgBox-223 report in the shape of
// 2026-09-23 (hashes shortened, one MAC-free line each). The rotated files come
// out of rotation order on purpose (the glob's order), with the box's stamps
// in its UTC-3: the oldest (Sep 1 19:00) only starts the history, one hour
// (Sep 4 02) spans a rotation, and three headers are dropped with their hours
// — a pre-NTP 1970 clock and two date -r failures. Every counted stamp falls
// days after the start in any zone, so the totals hold wherever the suite runs.
const deviceOut = `build=AR241CE_8530
build_date=2026-01-12
svn=318
fw=AR241CE_8530
mcu=23
kernel=5.15.137
vapp=32
vapp_md5=9aa7f360179db64ab10853182555d032
sha:luciserver=465c90d4bde741acbfed09c5b1d94de1ba4d6647cdf3c71817968a7caa6d0a35
sha:factoryEnv.conf=f11a5e6953ed64de04863d00b1221c4311babf4df34384d5751d7fe89531d988
sha:missing=
btime=1788544542
rboot=cold_boot
uptime=692428
tcp=22 23 80 2018 2345 5037 5555 7000 7777 9095 33719 49494
udp=68 123 1800 1900 3721 5353
SpotifyEnabled=0
SpotifyProEnabled=1
run=spotifymusicpro
run=airplaydemo
dirty=33
ota_last=[2026-09-23 14:56:15.634] [DEBUG] [luci-rx] normalized_kind=unknown normalized=None remote_id=0 command_type=2 command=223 command_status=0 crc=16846 data_length=9 payload="NO_UPDATE"
rot=1788890000
h=      5 Sep  4 02
h=     40 Sep  7 09
h=     11 Sep  8 14
rot=1788300000
h=    183 Sep  1 18
rot=1788500000
h=     60 Sep  3 08
h=     42 Sep  4 02
rot=5
h=      9 Dec 31 21
rot=abc
h=      3 Sep  5 10
rot=
h=      1 Sep  5 11
live=Sep 23 17:18:30
h=      2 Sep 23 17
h=x Sep 23 17
h=     -2 Sep 23 17
h=      4 Sep 99 17
junk line without an equals sign
end=1
`

// boxZone is the box's zone (it keeps the room's, UTC-3): the tests that read
// its hour stamps pin it, so they read the same wherever the suite runs.
var boxZone = time.FixedZone("-03", -3*60*60)

func TestParseDevice(t *testing.T) {
	r := Report{At: time.Date(2026, 9, 23, 17, 30, 0, 0, boxZone), Hashes: map[string]string{}}
	parseDevice(&r, deviceOut)
	if r.Build != "AR241CE_8530" || r.BuildDate != "2026-01-12" || r.SVN != "318" || r.Firmware != "AR241CE_8530" || r.MCU != "23" || r.Kernel != "5.15.137" {
		t.Errorf("identity = %+v", r)
	}
	if r.VendorApp != "32" || !strings.HasPrefix(r.VendorMD5, "9aa7f360") {
		t.Errorf("vendor app = %q %q", r.VendorApp, r.VendorMD5)
	}
	if len(r.Hashes) != 2 || r.Hashes["luciserver"] == "" || r.Hashes["missing"] != "" {
		t.Errorf("hashes = %v (an unreadable file must not record an empty hash)", r.Hashes)
	}
	if r.BootAt.Unix() != 1788544542 || r.Reboot != "cold_boot" || r.Uptime != 692428 {
		t.Errorf("boot = %v %q %d", r.BootAt, r.Reboot, r.Uptime)
	}
	if fmtPorts(r.TCP) != "22 23 80 2018 2345 5037 5555 7000 7777 9095 33719 49494" || fmtPorts(r.UDP) != "68 123 1800 1900 3721 5353" {
		t.Errorf("ports = %v / %v", r.TCP, r.UDP)
	}
	if r.SpotifyFlags != "0/1" {
		t.Errorf("flags = %q, want 0/1", r.SpotifyFlags)
	}
	if strings.Join(r.Running, " ") != "airplaydemo spotifymusicpro" {
		t.Errorf("running = %v (sorted)", r.Running)
	}
	if r.DirtyKeys != 33 {
		t.Errorf("dirty = %d", r.DirtyKeys)
	}
	// the oldest plausible rotation only starts the history; the later files
	// and the live one are counted, the hour across a rotation summed; a 1970
	// clock, a failed date -r and junk lines are dropped
	if r.Reconnects != 60+42+5+40+11+2 || r.SyslogFiles != 3 || !r.ReconnectsSince.Equal(time.Unix(1788300000, 0)) {
		t.Errorf("history = %d over %d files since %v", r.Reconnects, r.SyslogFiles, r.ReconnectsSince)
	}
	if r.Last24h != 2 {
		t.Errorf("last 24 h = %d, want the live file's 2", r.Last24h)
	}
	if len(r.ReconnectsByDay) != 23 || r.ReconnectsByDay["2026-09-01"] != 0 || r.ReconnectsByDay["2026-09-03"] != 60 ||
		r.ReconnectsByDay["2026-09-04"] != 47 || r.ReconnectsByDay["2026-09-07"] != 40 || r.ReconnectsByDay["2026-09-08"] != 11 ||
		r.ReconnectsByDay["2026-09-23"] != 2 {
		t.Errorf("by day = %v (Sep 1 through Sep 23, zeros included)", r.ReconnectsByDay)
	}
	if r.OTALast != "NO_UPDATE" || !r.OTAAt.Equal(time.Date(2026, 9, 23, 14, 56, 15, 0, time.Local)) {
		t.Errorf("box's own verdict = %q at %v", r.OTALast, r.OTAAt)
	}
}

// With no rotated file the live syslog alone is the history: its first stamp
// starts it — or the boot, when that stamp is the pre-NTP clock a boot's first
// lines carry, whose hour ("Dec 31 21", last December) is dropped. With no
// syslog at all nothing is claimed.
func TestHistoryLiveOnly(t *testing.T) {
	boot := time.Date(2026, 9, 4, 14, 55, 42, 0, time.Local)
	at := time.Date(2026, 9, 23, 17, 30, 0, 0, time.Local)
	r := Report{At: at, BootAt: boot}
	history(&r, nil, &logFile{stamp: "Sep 23 17:18:30", hours: []hourCount{{"Sep 23 17", 2}}})
	if r.Reconnects != 2 || r.Last24h != 2 || r.SyslogFiles != 0 || !r.ReconnectsSince.Equal(time.Date(2026, 9, 23, 17, 18, 30, 0, time.Local)) {
		t.Errorf("live only = %+v", r)
	}
	if got := reconnectFact(r); got != "2 since Sep 23 17:18 · the live syslog only" {
		t.Errorf("fact = %q (under an hour: no rate)", got)
	}
	if label, val := dayFact(r); label != "" || val != "" {
		t.Errorf("one day of history printed a day line: %q %q", label, val)
	}
	r = Report{At: at, BootAt: boot}
	history(&r, nil, &logFile{stamp: "Dec 31 21:00:08", hours: []hourCount{{"Dec 31 21", 1}, {"Sep 23 17", 1}}})
	if !r.ReconnectsSince.Equal(boot) || r.Reconnects != 1 || r.ReconnectsByDay["2026-09-23"] != 1 || len(r.ReconnectsByDay) != 20 {
		t.Errorf("pre-NTP stamp: since %v, %d reconnects by day %v; want the boot, the pre-NTP hour dropped", r.ReconnectsSince, r.Reconnects, r.ReconnectsByDay)
	}
	r = Report{At: at, BootAt: boot}
	history(&r, nil, nil)
	if !r.ReconnectsSince.IsZero() || r.ReconnectsByDay != nil || reconnectFact(r) != "syslog not read" {
		t.Errorf("no syslog = %+v / %q", r, reconnectFact(r))
	}
}

// Across a New Year the hours still land in the right year and day: summed
// across the rotation that splits one, kept when they end after the history
// starts (the oldest file's own hours never count), and split into the last
// 24 clock hours — the sweep's and the 23 before it — and one count per local
// day, the quiet days as zero. The per-day line shows the last seven.
func TestHistoryPerHourAndDay(t *testing.T) {
	at := time.Date(2027, 1, 2, 10, 20, 0, 0, boxZone)
	rots := []*logFile{
		{end: time.Date(2027, 1, 1, 3, 15, 0, 0, boxZone), hours: []hourCount{{"Dec 29 23", 1}, {"Dec 31 23", 6}, {"Jan  1 00", 9}, {"Jan  1 03", 2}}},
		{end: time.Date(2026, 12, 26, 9, 30, 0, 0, boxZone), hours: []hourCount{{"Dec 26 08", 7}, {"Dec 26 09", 2}}},
		{end: time.Date(2026, 12, 29, 23, 40, 0, 0, boxZone), hours: []hourCount{{"Dec 26 09", 3}, {"Dec 27 14", 20}, {"Dec 29 23", 4}}},
	}
	live := &logFile{stamp: "Jan  1 03:15:02", hours: []hourCount{{"Jan  1 03", 1}, {"Jan  1 10", 5}, {"Jan  1 11", 2}, {"Jan  2 09", 4}, {"Jan  2 10", 1}}}
	r := Report{At: at}
	history(&r, rots, live)
	if r.Reconnects != 3+20+4+1+6+9+2+1+5+2+4+1 || !r.ReconnectsSince.Equal(time.Date(2026, 12, 26, 9, 30, 0, 0, boxZone)) || r.SyslogFiles != 3 {
		t.Errorf("history = %d since %v over %d files", r.Reconnects, r.ReconnectsSince, r.SyslogFiles)
	}
	// Jan 1 10:00 is 24 hours before the sweep's hour: out; Jan 1 11:00 is in
	if r.Last24h != 2+4+1 {
		t.Errorf("last 24 h = %d, want 7", r.Last24h)
	}
	want := map[string]int{"2026-12-26": 3, "2026-12-27": 20, "2026-12-28": 0, "2026-12-29": 5, "2026-12-30": 0,
		"2026-12-31": 6, "2027-01-01": 9 + 3 + 5 + 2, "2027-01-02": 5}
	if !maps.Equal(r.ReconnectsByDay, want) {
		t.Errorf("by day = %v\nwant %v", r.ReconnectsByDay, want)
	}
	if label, val := dayFact(r); label != "last 7 days" || val != "20 0 5 0 6 19 5 (Dec 27 → Jan 2)" {
		t.Errorf("day line = %q %q", label, val)
	}
	if got := reconnectFact(r); got != "58 since Dec 26 09:30 · 0.3/h over 7d 0h · 7 in the last 24 h · 3 rotated files" {
		t.Errorf("fact = %q", got)
	}
	// a short history shows only the days it reaches
	r.ReconnectsByDay = map[string]int{"2027-01-01": 4, "2027-01-02": 9}
	if label, val := dayFact(r); label != "last 2 days" || val != "4 9 (Jan 1 → Jan 2)" {
		t.Errorf("two-day line = %q %q", label, val)
	}
}

// The syslog comes last in the device output, so an output cut at the ssh cap
// loses its tail: without the closing line the history is unread — never a
// short count over some of the files.
func TestHistoryCutOffIsUnread(t *testing.T) {
	cut := strings.Replace(deviceOut, "end=1\n", "", 1)
	r := Report{At: time.Date(2026, 9, 23, 17, 30, 0, 0, boxZone), Hashes: map[string]string{}}
	parseDevice(&r, cut)
	if r.Build != "AR241CE_8530" || !r.ReconnectsSince.IsZero() || r.Reconnects != 0 || r.ReconnectsByDay != nil {
		t.Errorf("a cut-off output = build %q, %d reconnects since %v", r.Build, r.Reconnects, r.ReconnectsSince)
	}
}

func TestReconnectFactRate(t *testing.T) {
	since := time.Date(2026, 9, 1, 22, 20, 0, 0, time.Local)
	r := Report{At: since.Add(100 * time.Hour), Reconnects: 150, ReconnectsSince: since, SyslogFiles: 49, Last24h: 41}
	if got := reconnectFact(r); got != "150 since Sep 1 22:20 · 1.5/h over 4d 4h · 41 in the last 24 h · 49 rotated files" {
		t.Errorf("fact = %q", got)
	}
	r.SyslogFiles = 1
	if got := reconnectFact(r); !strings.HasSuffix(got, " · 1 rotated file") {
		t.Errorf("one file = %q", got)
	}
	// under a day, the last 24 hours are the whole count: not repeated
	r.At = since.Add(20 * time.Hour)
	if got := reconnectFact(r); strings.Contains(got, "last 24 h") {
		t.Errorf("under a day = %q", got)
	}
}

func TestPortsRejectJunk(t *testing.T) {
	got := fmtPorts(ports("80 22 abc 70000 0 22 -5 443"))
	if got != "22 80 443" {
		t.Errorf("ports = %q", got)
	}
	if fmtPorts(nil) != "" {
		t.Error("no ports should format empty (Diff relies on it)")
	}
}

func TestDiffNamesWhatMoved(t *testing.T) {
	base := Report{Build: "AR241CE_8530", MCU: "23", VendorApp: "32",
		Hashes: map[string]string{"luciserver": "aaa", "rakoit_app": "bbb"},
		BootAt: time.Date(2026, 9, 4, 14, 55, 42, 0, time.Local), Reboot: "cold_boot",
		TCP: []int{22, 80, 9095}, SpotifyFlags: "0/1", Running: []string{"spotifymusicpro"},
		LSSDP:    LSSDPFacts{OK: true, FW: "AR241CE_8530.23.2", NetMode: "ETH0"},
		ZeroConf: ZCFacts{OK: true, Port: 9095, LibraryVersion: "3.211.130"},
		Manifest: ManifestFacts{Asked: true, UpToDate: true},
		Bundle:   BundleFacts{Build: "AR241CE_8530", ETag: "6a86"},
	}
	same := base
	if ch := Diff(base, same); len(ch) != 0 {
		t.Errorf("identical sweeps differ: %+v", ch)
	}
	next := base
	next.Build, next.MCU, next.VendorApp = "AR241CE_9000", "24", "33"
	next.Hashes = map[string]string{"luciserver": "ccc", "rakoit_app": "bbb", "new": "ddd"}
	next.BootAt = base.BootAt.Add(36 * time.Hour)
	next.Reboot = "normal"
	next.TCP = []int{22, 80, 9096}
	next.SpotifyFlags = "1/0"
	next.Running = []string{"newspotifyhifi"}
	next.LSSDP.FW, next.LSSDP.NetMode = "AR241CE_9000.24.2", "WLAN0"
	next.ZeroConf.Port, next.ZeroConf.LibraryVersion = 9096, "3.203.239"
	next.Manifest = ManifestFacts{Asked: true, Offered: "AR241CE_9100"}
	next.Bundle = BundleFacts{Build: "AR241CE_9100", ETag: "ffff"}
	ch := Diff(base, next)
	var fields []string
	for _, c := range ch {
		fields = append(fields, c.Field)
	}
	want := "firmware build mcu vendor app sha256 luciserver boot tcp listeners spotify flags running lssdp firmware lssdp netmode zeroconf port spotify eSDK vendor verdict newest bundle bundle etag"
	if got := strings.Join(fields, " "); got != want {
		t.Errorf("changed fields:\n got %s\nwant %s", got, want)
	}
	// a hash that only the new sweep has is not a change; a probe that did
	// not answer this time is not a change either
	unanswered := next
	unanswered.LSSDP.OK, unanswered.ZeroConf.OK = false, false
	unanswered.Manifest.Asked = false
	unanswered.Bundle.Err = "cdn unreachable"
	for _, c := range Diff(base, unanswered) {
		if strings.HasPrefix(c.Field, "lssdp") || strings.HasPrefix(c.Field, "zeroconf") || c.Field == "vendor verdict" || strings.HasPrefix(c.Field, "bundle") || c.Field == "newest bundle" {
			t.Errorf("unanswered probe reported as a change: %+v", c)
		}
	}
	// the boot moving by less than the clock's slop is not a reboot
	jitter := base
	jitter.BootAt = base.BootAt.Add(30 * time.Second)
	if ch := Diff(base, jitter); len(ch) != 0 {
		t.Errorf("boot-time jitter reported: %+v", ch)
	}
}

func TestBaselineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sweep-test.json")
	if Load(path) != nil {
		t.Error("missing baseline should load as nil")
	}
	r := Report{At: time.Date(2026, 9, 12, 15, 38, 0, 0, time.Local), Host: "h", Build: "AR241CE_8530",
		Hashes: map[string]string{"luciserver": "aaa"}, TCP: []int{22}, SpotifyFlags: "0/1"}
	if err := Save(path, r); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got == nil || !got.At.Equal(r.At) || got.Build != r.Build || got.Hashes["luciserver"] != "aaa" || got.SpotifyFlags != "0/1" {
		t.Errorf("round trip = %+v", got)
	}
	// garbage is nil, never a panic
	if err := Save(path, Report{}); err != nil {
		t.Fatal(err)
	}
	if Load(path) != nil {
		t.Error("a baseline with no timestamp should load as nil")
	}
	if Save("", r) == nil {
		t.Error("saving with no state dir should fail loudly")
	}
}

// Run with every probe faked: the ssh inventory parses, the LAN answers land,
// the vendor is asked twice (the running build, then an old one to learn the
// newest bundle) and the CDN HEAD fills the bundle facts.
func TestRunAssemblesTheReport(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodHead {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Length", "89751552")
		w.Header().Set("Last-Modified", "Thu, 20 Aug 2026 07:55:48 GMT")
		w.Header().Set("ETag", `"6a86b304-5598000"`)
	}))
	defer cdn.Close()
	var asked []string
	pr := Probes{
		SSH: func(context.Context, config.Config, string) (string, error) { return deviceOut, nil },
		LSSDP: func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{FW: "AR241CE_8530.23.2", State: "S", NetMode: "ETH0", Name: "Living\x1b[31m"}, true
		},
		FindZC: func(context.Context, string, net.IP, time.Duration) (discovery.SpotifyEndpoint, bool) {
			return discovery.SpotifyEndpoint{Host: "living.local.", Port: 9095}, true
		},
		ProbeZC: func(_ context.Context, addr string, _ time.Duration) (discovery.SpotifyZCInfo, bool) {
			if addr != "living.local:9095" {
				t.Errorf("getInfo addr = %q", addr)
			}
			return discovery.SpotifyZCInfo{Status: 101, LibraryVersion: "3.211.130-g110e3e03", Version: "2.10.0"}, true
		},
		Manifest: func(_ context.Context, url, build string) protocol.OTAInfo {
			asked = append(asked, build)
			if build == "AR241CE_8530" {
				return protocol.OTAInfo{Asked: build, UpToDate: true}
			}
			return protocol.OTAInfo{Asked: build, Offered: "AR241CE_8530", PackageURL: cdn.URL + "/lp10/x.swu"}
		},
		Head:      func(ctx context.Context, url string) (*http.Response, error) { return http.DefaultClient.Head(url) },
		Manifest0: "https://manifest.example/v1",
	}
	r := Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	if r.SSHErr != "" || r.Build != "AR241CE_8530" || r.MCU != "23" {
		t.Errorf("ssh side = %q %q %q", r.SSHErr, r.Build, r.MCU)
	}
	if !r.LSSDP.OK || r.LSSDP.FW != "AR241CE_8530.23.2" || r.LSSDP.Name != "Living[31m" {
		t.Errorf("lssdp = %+v (values must be control-stripped)", r.LSSDP)
	}
	if !r.ZeroConf.OK || r.ZeroConf.Port != 9095 || r.ZeroConf.Version != "2.10.0" {
		t.Errorf("zeroconf = %+v", r.ZeroConf)
	}
	if strings.Join(asked, ",") != "AR241CE_8530,AR241CE_1" {
		t.Errorf("manifest asked for %v", asked)
	}
	if !r.Manifest.Asked || !r.Manifest.UpToDate {
		t.Errorf("manifest = %+v", r.Manifest)
	}
	if r.Bundle.Err != "" || r.Bundle.Build != "AR241CE_8530" || r.Bundle.Size != 89751552 || r.Bundle.ETag != "6a86b304-5598000" || !strings.HasPrefix(r.Bundle.LastModified, "Thu, 20 Aug") {
		t.Errorf("bundle = %+v", r.Bundle)
	}

	// the report reads as prose and names the first sweep
	var out bytes.Buffer
	Write(&out, r, nil, time.Now())
	for _, want := range []string{
		"firmware       AR241CE_8530 · mcu 23",
		// btime renders in the reader's zone, so the expectation must too (CI runs in UTC)
		"boot           " + time.Unix(1788544542, 0).Format("Jan 2 15:04") + " · power-on · up 8d 0h",
		"vendor app     v32 · md5 9aa7f360179d…",
		"manifest       no update for AR241CE_8530",
		"newest bundle  AR241CE_8530 · " + cdn.URL,
		"89751552 bytes · Thu, 20 Aug 2026 07:55:48 GMT · etag 6a86b304-5598000",
		"lssdp          AR241CE_8530.23.2 · S · ETH0 · Living",
		"spotify        :9095 · eSDK 3.211.130-g110e3e03 · zeroconf 2.10.0",
		"spotify flags  0/1",
		"env store      33 keys set at runtime",
		"reconnects     160 since " + time.Unix(1788300000, 0).Format("Jan 2 15:04") + " · ",
		" · 3 rotated files\n  last 7 days    ",
		"box's own check no update · Sep 23 14:56 (it asks every 4 h)",
		"first sweep — nothing to compare with yet",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
		}
	}
	// against a baseline, the diff — or its absence — is the last section
	prev := r
	prev.At = r.At.Add(-49 * time.Hour)
	out.Reset()
	Write(&out, r, &prev, r.At)
	if !strings.Contains(out.String(), "since the last sweep") || !strings.Contains(out.String(), "nothing changed") || !strings.Contains(out.String(), "2d 1h ago") {
		t.Errorf("unchanged report:\n%s", out.String())
	}
	prev.Build, prev.VendorApp = "AR241CE_9243", "31"
	out.Reset()
	Write(&out, r, &prev, r.At)
	for _, want := range []string{"firmware build   AR241CE_9243 → AR241CE_8530", "vendor app       31 → 32"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("diff missing %q:\n%s", want, out.String())
		}
	}
}

// A dead ssh, a silent LAN and no vendor: the report still prints, says what
// is missing, and the exit code says the inventory is incomplete.
func TestRunDegradesWithoutSSHOrVendor(t *testing.T) {
	pr := Probes{
		SSH: func(context.Context, config.Config, string) (string, error) {
			return "", errors.New("ssh: Connection timed out")
		},
		LSSDP: func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{}, false
		},
		FindZC: func(context.Context, string, net.IP, time.Duration) (discovery.SpotifyEndpoint, bool) {
			return discovery.SpotifyEndpoint{}, false
		},
		ProbeZC: func(context.Context, string, time.Duration) (discovery.SpotifyZCInfo, bool) {
			t.Fatal("getInfo without an endpoint")
			return discovery.SpotifyZCInfo{}, false
		},
		Manifest: func(context.Context, string, string) protocol.OTAInfo {
			t.Fatal("the vendor was asked with no build to ask about")
			return protocol.OTAInfo{}
		},
		Manifest0: "https://manifest.example/v1",
	}
	r := Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	if r.SSHErr == "" || r.Manifest.Asked || r.LSSDP.OK || r.ZeroConf.OK {
		t.Errorf("degraded run = %+v", r)
	}
	var out bytes.Buffer
	Write(&out, r, nil, time.Now())
	for _, want := range []string{"ssh            ssh: Connection timed out", "lssdp          no answer", "spotify        not advertised", "manifest       not asked"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("degraded report missing %q:\n%s", want, out.String())
		}
	}
	// with LSSDP up but ssh down, the build comes from the responder and the
	// vendor IS asked
	pr.LSSDP = func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
		return discovery.LSSDPInfo{FW: "AR241CE_8530.23.2"}, true
	}
	var asked []string
	pr.Manifest = func(_ context.Context, _, build string) protocol.OTAInfo {
		asked = append(asked, build)
		return protocol.OTAInfo{Asked: build, Err: "vendor unreachable"}
	}
	r = Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	if strings.Join(asked, ",") != "AR241CE_8530,AR241CE_1" || r.Manifest.Err != "vendor unreachable" || r.Bundle.Err != "vendor unreachable" {
		t.Errorf("lssdp-only run asked %v, manifest %+v bundle %+v", asked, r.Manifest, r.Bundle)
	}
}

// Main: flags, the exit codes, and the baseline landing in LP10_STATE_DIR.
func TestMainFlagsAndExitCodes(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	// no network from a unit test: a dead ssh, a silent LAN, no vendor
	probesFor = func() Probes {
		return Probes{
			SSH: func(context.Context, config.Config, string) (string, error) {
				return "", errors.New("ssh: Connection timed out")
			},
			LSSDP: func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
				return discovery.LSSDPInfo{}, false
			},
			FindZC: func(context.Context, string, net.IP, time.Duration) (discovery.SpotifyEndpoint, bool) {
				return discovery.SpotifyEndpoint{}, false
			},
			ProbeZC: func(context.Context, string, time.Duration) (discovery.SpotifyZCInfo, bool) {
				return discovery.SpotifyZCInfo{}, false
			},
		}
	}
	t.Cleanup(func() { probesFor = DefaultProbes })
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), config.Config{Host: "192.0.2.13"}, []string{"--bogus"}, &stdout, &stderr); code != 2 {
		t.Errorf("bad flag exit = %d, want 2", code)
	}
	if code := Main(context.Background(), config.Config{Host: "192.0.2.13"}, []string{"extra"}, &stdout, &stderr); code != 2 {
		t.Errorf("positional arg exit = %d, want 2", code)
	}
	// a run against nothing: the report prints, exit 1, and --no-save leaves
	// no baseline
	stdout.Reset()
	ctx := context.Background()
	code := Main(ctx, config.Config{Host: "192.0.2.13", User: "root"}, []string{"--no-save"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "ssh ") {
		t.Errorf("unreachable box: exit %d, out:\n%s\nerr:\n%s", code, stdout.String(), stderr.String())
	}
	if Load(config.SweepPath(config.Config{Host: "192.0.2.13"})) != nil {
		t.Error("--no-save wrote a baseline")
	}
	// --json prints the baseline's shape; a run whose ssh inventory failed
	// still exits 1, and still saves what it did read — here, with nothing
	// before it and nothing else answering, a baseline with no ssh facts
	stdout.Reset()
	stderr.Reset()
	code = Main(ctx, config.Config{Host: "192.0.2.13", User: "root"}, []string{"--json"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), `"host": "192.0.2.13"`) {
		t.Errorf("--json: exit %d, out:\n%s", code, stdout.String())
	}
	if b := Load(config.SweepPath(config.Config{Host: "192.0.2.13"})); b == nil || b.Build != "" || b.SSHErr == "" || b.Carried != nil {
		t.Errorf("an ssh-failed first sweep should save as it is: %+v", b)
	}
	if stderr.Len() != 0 {
		t.Errorf("a saved sweep has nothing for stderr, got:\n%s", stderr.String())
	}
}

// An interrupted sweep (Ctrl-C during the run) degrades every probe at once;
// saved as the baseline, that hollow report would make the next sweep diff
// against nothing and print "nothing changed" after a real OTA. The previous
// baseline must survive it.
func TestMainInterruptedKeepsBaseline(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	probesFor = func() Probes {
		return Probes{
			SSH: func(ctx context.Context, _ config.Config, _ string) (string, error) {
				return "", ctx.Err()
			},
			LSSDP: func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
				return discovery.LSSDPInfo{}, false
			},
			FindZC: func(context.Context, string, net.IP, time.Duration) (discovery.SpotifyEndpoint, bool) {
				return discovery.SpotifyEndpoint{}, false
			},
			ProbeZC: func(context.Context, string, time.Duration) (discovery.SpotifyZCInfo, bool) {
				return discovery.SpotifyZCInfo{}, false
			},
		}
	}
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13", User: "root"}
	path := config.SweepPath(cfg)
	seed := Report{At: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Host: cfg.Host, Build: "AR241CE_8530", MCU: "23"}
	if err := Save(path, seed); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	Main(ctx, cfg, nil, &stdout, &stderr)
	got := Load(path)
	if got == nil || got.Build != seed.Build || got.MCU != seed.MCU || !got.At.Equal(seed.At) {
		t.Fatalf("interrupted sweep replaced the baseline: %+v\nstderr:\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "interrupted") {
		t.Errorf("stderr should say the baseline was left in place, got:\n%s", stderr.String())
	}
}

// quietLAN is a probe set with the given ssh and nothing else answering: no
// LSSDP, no ZeroConf, no vendor.
func quietLAN(ssh func(context.Context, config.Config, string) (string, error)) Probes {
	return Probes{
		SSH: ssh,
		LSSDP: func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{}, false
		},
		FindZC: func(context.Context, string, net.IP, time.Duration) (discovery.SpotifyEndpoint, bool) {
			return discovery.SpotifyEndpoint{}, false
		},
		ProbeZC: func(context.Context, string, time.Duration) (discovery.SpotifyZCInfo, bool) {
			return discovery.SpotifyZCInfo{}, false
		},
	}
}

// A sweep whose ssh failed (dropbear stalls rapid reconnects) has an empty ssh
// side, and Diff skips every empty pair: saved as it is, it would make the
// next good sweep miss what moved — a vendor-app update would read "nothing
// changed". Kept whole instead, the old baseline would drop this sweep's fresh
// LAN answers. The merge saves those answers and carries every ssh fact from
// the sweep that read it, dated to that sweep; the next report names the date
// in its header, and the update still shows.
func TestMainSSHFailureCarriesTheSSHFacts(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	out, sshErr, state := deviceOut, error(nil), "S"
	probesFor = func() Probes {
		pr := quietLAN(func(context.Context, config.Config, string) (string, error) {
			if sshErr != nil {
				return "", sshErr
			}
			return out, nil
		})
		pr.LSSDP = func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{FW: "AR241CE_8530.23.2", State: state, NetMode: "ETH0"}, true
		}
		return pr
	}
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13", User: "root"}
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), cfg, nil, &stdout, &stderr); code != 0 { // 1. full sweep → baseline
		t.Fatalf("full sweep exit %d:\n%s", code, stderr.String())
	}
	first := Load(config.SweepPath(cfg))
	if first == nil || first.Carried != nil {
		t.Fatalf("a full first sweep carries nothing: %+v", first)
	}
	sshErr, state = errors.New("ssh: Connection timed out"), "P"
	stdout.Reset()
	if code := Main(context.Background(), cfg, nil, &stdout, &stderr); code != 1 { // 2. sshd stalled
		t.Errorf("ssh-failed sweep exit %d, want 1", code)
	}
	b := Load(config.SweepPath(cfg))
	if b == nil || !b.At.After(first.At) || b.LSSDP.State != "P" || b.SSHErr == "" {
		t.Fatalf("the ssh-free answers should be this sweep's: %+v", b)
	}
	if b.Build != "AR241CE_8530" || b.MCU != "23" || b.VendorApp != "32" || b.Hashes["luciserver"] == "" || b.SpotifyFlags != "0/1" ||
		b.DirtyKeys != 33 || b.Reconnects != first.Reconnects || !maps.Equal(b.ReconnectsByDay, first.ReconnectsByDay) || b.OTALast != "NO_UPDATE" {
		t.Errorf("the ssh facts should be the first sweep's: %+v", b)
	}
	for _, k := range []string{"identity", "mcu", "vendorApp", "hashes.luciserver", "hashes.factoryEnv.conf", "spotifyFlags", "dirtyKeys", "reconnects", "otaLast"} {
		if at, ok := b.Carried[k]; !ok || !at.Equal(first.At) {
			t.Errorf("carried %s = %v, %v; want the first sweep's %v", k, at, ok, first.At)
		}
	}
	if len(b.Carried) != 9 {
		t.Errorf("carried %v: only the ssh facts", b.Carried)
	}
	asOf := "ssh facts as of " + first.At.Format("Jan 2 15:04")
	if !strings.Contains(stdout.String(), "\nbaseline saved (kept from earlier sweeps: "+asOf+"); run again") {
		t.Errorf("the save should say what it kept:\n%s", stdout.String())
	}
	sshErr, out = nil, strings.Replace(deviceOut, "vapp=32", "vapp=33", 1)
	stdout.Reset()
	Main(context.Background(), cfg, nil, &stdout, &stderr) // 3. the app loader updated rakoit_app
	if !strings.Contains(stdout.String(), "vendor app       32 → 33") {
		t.Errorf("the vendor-app update is invisible after an ssh-failed sweep:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), " ago · "+asOf+")\n") {
		t.Errorf("the header should say the ssh side is compared with older facts:\n%s", stdout.String())
	}
	if b := Load(config.SweepPath(cfg)); b == nil || b.Carried != nil || b.VendorApp != "33" {
		t.Errorf("read again, nothing is carried: %+v", b)
	}
}

// A sweep that reads the box but not all of it — getenv, sqlite3 and the
// syslog give nothing — keeps those three facts from the sweep before it,
// dated to that sweep, and takes everything else fresh. The next sweep
// compares the Spotify pair with the carried one, so a switch made while it
// was unreadable still shows.
func TestMainPartlyReadRunCarriesWhatItMissed(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	out := deviceOut
	probesFor = func() Probes {
		return quietLAN(func(context.Context, config.Config, string) (string, error) { return out, nil })
	}
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13", User: "root"}
	var stdout, stderr bytes.Buffer
	Main(context.Background(), cfg, nil, &stdout, &stderr)
	first := Load(config.SweepPath(cfg))
	var kept []string
	for ln := range strings.SplitSeq(deviceOut, "\n") {
		switch {
		case strings.HasPrefix(ln, "Spotify"):
			kept = append(kept, strings.SplitAfter(ln, "=")[0]) // getenv printed no value
		case strings.HasPrefix(ln, "dirty="):
			kept = append(kept, "dirty=") // no sqlite3
		case !strings.HasPrefix(ln, "rot=") && !strings.HasPrefix(ln, "h=") && !strings.HasPrefix(ln, "live="):
			kept = append(kept, ln) // the syslog unreadable: no files at all
		}
	}
	out = strings.Join(kept, "\n")
	stdout.Reset()
	if code := Main(context.Background(), cfg, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("a partly-read sweep exit %d:\n%s", code, stderr.String())
	}
	b := Load(config.SweepPath(cfg))
	if b == nil || b.SpotifyFlags != "0/1" || !b.DirtyKeysOK || b.DirtyKeys != 33 || b.Reconnects != first.Reconnects || !b.ReconnectsSince.Equal(first.ReconnectsSince) {
		t.Fatalf("the three unread facts should be the first sweep's: %+v", b)
	}
	if len(b.Carried) != 3 || !b.Carried["spotifyFlags"].Equal(first.At) || !b.Carried["dirtyKeys"].Equal(first.At) || !b.Carried["reconnects"].Equal(first.At) {
		t.Errorf("carried = %v, want the three, dated %v", b.Carried, first.At)
	}
	asOf := "spotify flags, env store, reconnects as of " + first.At.Format("Jan 2 15:04")
	if !strings.Contains(stdout.String(), "reconnects     syslog not read\n") || !strings.Contains(stdout.String(), "(kept from earlier sweeps: "+asOf+")") {
		t.Errorf("the report is this sweep's, the save names what it kept:\n%s", stdout.String())
	}
	out = strings.Replace(deviceOut, "SpotifyEnabled=0\nSpotifyProEnabled=1", "SpotifyEnabled=1\nSpotifyProEnabled=0", 1)
	stdout.Reset()
	Main(context.Background(), cfg, nil, &stdout, &stderr)
	if !strings.Contains(stdout.String(), " ago · "+asOf+")\n") || !strings.Contains(stdout.String(), "spotify flags    0/1 → 1/0") {
		t.Errorf("the switch made while getenv was unreadable is invisible:\n%s", stdout.String())
	}
}

// Listeners in the Linux ephemeral range move on every start — rakoit_app's
// second tcp listener went 43761 → 33719 → 46835, and three udp sockets do the
// same — so Diff compares the lists without them. A fixed port inside the
// range (dmr's tcp 49494) and every port below it still count, a read list of
// nothing but dynamic ports is still an answer, and the report still prints
// the whole list.
func TestDiffIgnoresDynamicListeners(t *testing.T) {
	before := Report{TCP: []int{22, 23, 80, 2018, 2345, 5037, 5555, 7000, 7777, 9095, 43761, 49494},
		UDP: []int{68, 123, 1800, 1900, 3721, 5353, 38001, 41234, 52000}}
	after := Report{TCP: []int{22, 23, 80, 2018, 2345, 5037, 5555, 7000, 7777, 9095, 33719, 49494},
		UDP: []int{68, 123, 1800, 1900, 3721, 5353, 33333, 44444, 60999}}
	if ch := Diff(before, after); len(ch) != 0 {
		t.Errorf("a per-boot dynamic port reads as a change: %+v", ch)
	}
	moved := after
	moved.TCP = []int{22, 23, 80, 2018, 2345, 5037, 5555, 7000, 7777, 9096, 33719}
	ch := Diff(before, moved)
	if len(ch) != 1 || ch[0].Field != "tcp listeners" || ch[0].Was != "22 23 80 2018 2345 5037 5555 7000 7777 9095 49494" || ch[0].Now != "22 23 80 2018 2345 5037 5555 7000 7777 9096" {
		t.Errorf("fixed ports moved = %+v", ch)
	}
	bare := after
	bare.UDP = []int{40000}
	if ch := Diff(before, bare); len(ch) != 1 || ch[0].Field != "udp listeners" || ch[0].Now != "none fixed" {
		t.Errorf("an all-dynamic list = %+v", ch)
	}
	var out bytes.Buffer
	Write(&out, after, &before, time.Now())
	if !strings.Contains(out.String(), "tcp            22 23 80 2018 2345 5037 5555 7000 7777 9095 33719 49494") {
		t.Errorf("the report must print the whole list:\n%s", out.String())
	}
}

// getenv answering for neither flag, or for one, is a missing read, not a
// state: the pair is left unset, so it cannot diff as a flag change.
func TestPartialSpotifyFlagsAreNotAChange(t *testing.T) {
	prev := Report{SpotifyFlags: "0/1"}
	for _, in := range []string{
		"SpotifyEnabled=\nSpotifyProEnabled=\n",
		"SpotifyEnabled=0\nSpotifyProEnabled=\n",
		"SpotifyEnabled=\nSpotifyProEnabled=1\n",
		"SpotifyProEnabled=1\n",
	} {
		cur := Report{Hashes: map[string]string{}}
		parseDevice(&cur, in)
		if ch := Diff(prev, cur); cur.SpotifyFlags != "" || len(ch) != 0 {
			t.Errorf("%q: flags %q diff as %+v", in, cur.SpotifyFlags, ch)
		}
	}
	cur := Report{Hashes: map[string]string{}}
	parseDevice(&cur, "SpotifyProEnabled=1\nSpotifyEnabled=0\n")
	if cur.SpotifyFlags != "0/1" {
		t.Errorf("flags in either order = %q, want 0/1", cur.SpotifyFlags)
	}
}

// Every outside string reaches the report control-stripped, as in the TUI
// (SetOTA, Note): ssh's stderr, the vendor's version, package URL and error,
// and the CDN's status line and headers — Go's HTTP client passes a status
// line's ESC and a header's bidi override through as-is. The HEAD still asks
// for the package as the vendor named it.
func TestVendorAndSSHStringsAreControlStripped(t *testing.T) {
	var reply, headed string
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(reply))
	}))
	defer manifest.Close()
	offer := `{"errorCode":1000,"errorString":"SUCCESS","version":"AR241CE_9999\u001b]8;;https://evil.example/\u0007","url":"https://cdn.example/x\u202e.swu"}`
	for _, c := range []struct {
		name  string
		reply string
		head  func(context.Context, string) (*http.Response, error)
	}{
		{"cdn down", offer, func(context.Context, string) (*http.Response, error) { return nil, errors.New("no cdn in a test") }},
		{"cdn status", offer, func(context.Context, string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found\x1b[31m", Body: http.NoBody}, nil
		}},
		{"cdn headers", offer, func(_ context.Context, url string) (*http.Response, error) {
			headed = url
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: http.NoBody, Header: http.Header{
				"Etag":          {"\"6a86\u202eb304\""},
				"Last-Modified": {"Thu, 20 Aug 2026\x1b[5m 07:55:48 GMT"},
			}}, nil
		}},
		{"vendor error", `{"errorCode":7,"errorString":"busy\u001b[2J\u2028"}`, nil},
	} {
		reply = c.reply
		pr := quietLAN(func(context.Context, config.Config, string) (string, error) {
			return "", errors.New("ssh: \x1b[2Jbanner")
		})
		pr.LSSDP = func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{FW: "AR241CE_8530.23.2"}, true
		}
		pr.Manifest, pr.Manifest0, pr.Head = workers.OTACheck, manifest.URL, c.head
		r := Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
		var out bytes.Buffer
		Write(&out, r, nil, time.Now())
		if strings.ContainsFunc(out.String(), func(r rune) bool { return r != '\n' && r != ' ' && unicode.In(r, unicode.C, unicode.Z) }) {
			t.Errorf("%s: the report carries raw control or format runes:\n%q", c.name, out.String())
		}
	}
	if headed != "https://cdn.example/x\u202e.swu" {
		t.Errorf("HEAD asked for %q, not the package the vendor named", headed)
	}
}

// The report never claims a save that did not happen: Main says "baseline
// saved" only after it wrote one — never under --no-save, an interrupt or a
// failed save — and never into --json's stdout. A sweep that lost ssh still
// saves, and its claim says which facts the baseline kept, as of when.
func TestMainClaimsOnlyTheSaveItMade(t *testing.T) {
	var sshErr error
	probesFor = func() Probes {
		return quietLAN(func(ctx context.Context, _ config.Config, _ string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if sshErr != nil {
				return "", sshErr
			}
			return deviceOut, nil
		})
	}
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13", User: "root"}
	run := func(ctx context.Context, args ...string) (string, string) {
		var stdout, stderr bytes.Buffer
		Main(ctx, cfg, args, &stdout, &stderr)
		return stdout.String(), stderr.String()
	}
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()

	t.Setenv("LP10_STATE_DIR", t.TempDir())
	for _, c := range []struct {
		name   string
		ctx    context.Context
		sshErr error
		args   []string
	}{
		{"--no-save", context.Background(), nil, []string{"--no-save"}},
		{"interrupted", interrupted, nil, nil},
	} {
		sshErr = c.sshErr
		stdout, _ := run(c.ctx, c.args...)
		if Load(config.SweepPath(cfg)) != nil {
			t.Fatalf("%s saved a baseline", c.name)
		}
		if strings.Contains(stdout, "baseline saved") {
			t.Errorf("%s claims a save:\n%s", c.name, stdout)
		}
	}

	sshErr = nil
	stdout, _ := run(context.Background())
	first := Load(config.SweepPath(cfg))
	if first == nil || !strings.HasSuffix(stdout, "first sweep — nothing to compare with yet\n\nbaseline saved; run again after a suspected update\n") {
		t.Errorf("a saved sweep should end by saying so:\n%s", stdout)
	}
	sshErr = errors.New("ssh: Connection timed out")
	stdout, _ = run(context.Background())
	if !strings.HasSuffix(stdout, "\n\nbaseline saved (kept from earlier sweeps: ssh facts as of "+first.At.Format("Jan 2 15:04")+"); run again after a suspected update\n") {
		t.Errorf("an ssh-failed sweep should save and say what it kept:\n%s", stdout)
	}
	sshErr = nil
	stdout, _ = run(context.Background(), "--json")
	var r Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || r.Build != "AR241CE_8530" {
		t.Errorf("--json stdout must stay the bare report (%v):\n%s", err, stdout)
	}

	// a state dir that cannot be made: the save fails, stderr says why
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(file, "state"))
	stdout, stderr := run(context.Background())
	if strings.Contains(stdout, "baseline saved") || !strings.Contains(stderr, "baseline not saved: no state directory") {
		t.Errorf("a failed save: stdout\n%s\nstderr\n%s", stdout, stderr)
	}
}

// With ssh down the build comes from LSSDP and the vendor IS asked; the
// verdict line names that build, not the report's empty ssh-side one.
func TestVerdictNamesTheBuildAsked(t *testing.T) {
	pr := quietLAN(func(context.Context, config.Config, string) (string, error) {
		return "", errors.New("ssh: Connection timed out")
	})
	pr.LSSDP = func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
		return discovery.LSSDPInfo{FW: "AR241CE_8530.23.2"}, true
	}
	pr.Manifest = func(_ context.Context, _, build string) protocol.OTAInfo {
		return protocol.OTAInfo{Asked: build, UpToDate: true}
	}
	pr.Manifest0 = "https://manifest.example/v1"
	r := Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	var out bytes.Buffer
	Write(&out, r, nil, time.Now())
	if r.Manifest.Build != "AR241CE_8530" || !strings.Contains(out.String(), "manifest       no update for AR241CE_8530\n") {
		t.Errorf("verdict line lost the build it asked about (%q):\n%s", r.Manifest.Build, out.String())
	}
	pr.Manifest = func(_ context.Context, _, build string) protocol.OTAInfo {
		return protocol.OTAInfo{Asked: build, Offered: "AR241CE_9000"}
	}
	r = Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	out.Reset()
	Write(&out, r, nil, time.Now())
	if !strings.Contains(out.String(), "manifest       AR241CE_9000 offered for AR241CE_8530\n") {
		t.Errorf("offer line lost the build it asked about:\n%s", out.String())
	}
}

// A count the box did not give prints as unread, never as a measured zero:
// with no sqlite3 the env-store count is empty, with no syslog the reconnect
// history is. A count the box did give — zero included — still prints.
func TestUnreadCountsAreNotZero(t *testing.T) {
	var kept []string
	for ln := range strings.SplitSeq(deviceOut, "\n") {
		switch {
		case strings.HasPrefix(ln, "dirty="):
			kept = append(kept, "dirty=")
		case !strings.HasPrefix(ln, "rot=") && !strings.HasPrefix(ln, "h=") && !strings.HasPrefix(ln, "live="):
			kept = append(kept, ln)
		}
	}
	r := Report{At: time.Now(), Hashes: map[string]string{}}
	parseDevice(&r, strings.Join(kept, "\n"))
	var out bytes.Buffer
	Write(&out, r, nil, time.Now())
	for _, want := range []string{"env store      not read\n", "reconnects     syslog not read\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "0 keys set at runtime") {
		t.Errorf("an unread count prints as zero:\n%s", out.String())
	}
	r = Report{At: time.Now(), Hashes: map[string]string{}}
	parseDevice(&r, "dirty=0\n")
	out.Reset()
	Write(&out, r, nil, time.Now())
	if !r.DirtyKeysOK || !strings.Contains(out.String(), "env store      0 keys set at runtime\n") {
		t.Errorf("a measured zero must still print:\n%s", out.String())
	}
}

// Save replaces the baseline by renaming a new file over it, never by
// rewriting it in place: a reader holding the old file still reads the old
// sweep whole — so a kill mid-save leaves the last baseline, not a truncated
// one that Load would drop — and nothing but the 0600 baseline is left behind.
func TestSaveReplacesTheBaselineAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sweep-test.json")
	old := Report{At: time.Date(2026, 9, 12, 15, 38, 0, 0, time.Local), Host: "h", Build: "AR241CE_8530"}
	if err := Save(path, old); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	next := old
	next.At, next.Build = old.At.Add(time.Hour), "AR241CE_9000"
	if err := Save(path, next); err != nil {
		t.Fatal(err)
	}
	var was Report
	if err := json.NewDecoder(held).Decode(&was); err != nil || was.Build != old.Build {
		t.Errorf("the old baseline was rewritten in place: %+v (%v)", was, err)
	}
	if got := Load(path); got == nil || got.Build != next.Build {
		t.Errorf("new baseline = %+v", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("baseline mode = %v, want 0600", fi.Mode().Perm())
	}
}

// fullReport is a sweep that read every fact once, at at, of a box that has
// been up since its Sep 4 power-on.
func fullReport(at time.Time) Report {
	return Report{At: at, Host: "192.0.2.13",
		Build: "AR241CE_8530", BuildDate: "2026-01-12", SVN: "318", Firmware: "AR241CE_8530", MCU: "23", Kernel: "5.15.137",
		VendorApp: "42", VendorMD5: "b1dadf70", Hashes: map[string]string{"luciserver": "465c", "rakoit_app": "6ab0"},
		BootAt: time.Unix(1788544542, 0), Reboot: "cold_boot", Uptime: int(at.Sub(time.Unix(1788544542, 0)).Seconds()),
		TCP: []int{22, 80, 9095}, UDP: []int{1800}, SpotifyFlags: "0/1", Running: []string{"spotifymusicpro"},
		DirtyKeys: 33, DirtyKeysOK: true,
		Reconnects: 950, ReconnectsSince: at.Add(-21 * 24 * time.Hour), SyslogFiles: 49, Last24h: 41,
		ReconnectsByDay: map[string]int{"2026-09-22": 42}, OTALast: "NO_UPDATE", OTAAt: at.Add(-time.Hour),
		LSSDP:    LSSDPFacts{OK: true, FW: "AR241CE_8530.23.2", State: "S", NetMode: "ETH0", Name: "Living"},
		ZeroConf: ZCFacts{OK: true, Port: 9095, LibraryVersion: "3.211.130", Version: "2.10.0"},
		Manifest: ManifestFacts{Asked: true, Build: "AR241CE_8530", UpToDate: true},
		Bundle:   BundleFacts{None: true},
	}
}

// Every fact a sweep did not read comes from the baseline, dated to the sweep
// that read it; every fact it did read is its own, and nothing is dated. A
// fact neither read stays unread.
func TestMergeKeepsWhatASweepCouldNotRead(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	prev := fullReport(t0)
	blank := Report{At: t0.Add(24 * time.Hour), Host: "192.0.2.13", SSHErr: "ssh: Connection timed out", Hashes: map[string]string{},
		ZeroConf: ZCFacts{Port: 9095}, Manifest: ManifestFacts{Asked: true, Err: "vendor unreachable"}, Bundle: BundleFacts{Err: "vendor unreachable"}}
	m := merge(&prev, blank)
	back := m
	back.At, back.SSHErr, back.Carried = prev.At, "", nil
	if b1, b2 := mustJSON(t, back), mustJSON(t, prev); b1 != b2 {
		t.Errorf("a sweep that read nothing should keep every fact:\n got %s\nwant %s", b1, b2)
	}
	want := []string{"bundle", "dirtyKeys", "hashes.luciserver", "hashes.rakoit_app", "identity", "lssdp", "manifest", "mcu",
		"otaLast", "reconnects", "spotifyFlags", "vendorApp", "zeroconf"}
	if got := strings.Join(slices.Sorted(maps.Keys(m.Carried)), " "); got != strings.Join(want, " ") {
		t.Errorf("carried %s\n   want %s", got, strings.Join(want, " "))
	}
	for k, at := range m.Carried {
		if !at.Equal(t0) {
			t.Errorf("carried %s dated %v, want %v", k, at, t0)
		}
	}
	if !m.At.Equal(blank.At) || m.SSHErr != blank.SSHErr {
		t.Errorf("the merge is this sweep's: at %v, ssh %q", m.At, m.SSHErr)
	}
	if len(blank.Hashes) != 0 {
		t.Errorf("merging wrote into the fresh sweep's hashes: %v", blank.Hashes)
	}
	// read again: all of it fresh, nothing dated
	next := fullReport(t0.Add(48 * time.Hour))
	next.VendorApp = "43"
	if m2 := merge(&m, next); m2.Carried != nil || m2.VendorApp != "43" || !m2.At.Equal(next.At) {
		t.Errorf("a full sweep carries nothing: %+v", m2.Carried)
	}
	// neither read it: nothing to carry
	if m3 := merge(&blank, blank); m3.Carried != nil || m3.Build != "" {
		t.Errorf("an unread fact came from nowhere: %+v", m3)
	}
	if m4 := merge(nil, blank); m4.Carried != nil || m4.Hashes == nil {
		t.Errorf("no baseline: %+v", m4)
	}
	if m5 := merge(nil, Report{At: t0}); m5.Hashes == nil {
		t.Error("a merged baseline always has a hash map")
	}
}

// mustJSON is r as the baseline file would hold it, for a whole-report comparison.
func mustJSON(t *testing.T, r Report) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A fact carried again keeps the date it was read, not the date of the sweep
// that last carried it — through the baseline file, as Main runs it — and once
// read again it is fresh and the date goes.
func TestCarriedTwiceKeepsItsFirstDate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sweep-test.json")
	t0 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	if err := Save(path, merge(nil, fullReport(t0))); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 2; day++ { // two sweeps in a row lose ssh; the LAN answers
		lost := Report{At: t0.Add(time.Duration(day) * 24 * time.Hour), SSHErr: "ssh: Connection timed out", Hashes: map[string]string{},
			LSSDP: LSSDPFacts{OK: true, FW: "AR241CE_8530.23.2", NetMode: "ETH0", State: []string{"", "P", "S"}[day]}}
		if err := Save(path, merge(Load(path), lost)); err != nil {
			t.Fatal(err)
		}
	}
	b := Load(path)
	if b == nil || !b.At.Equal(t0.Add(48*time.Hour)) || b.Build != "AR241CE_8530" || b.LSSDP.State != "S" {
		t.Fatalf("after two ssh-less sweeps: %+v", b)
	}
	for _, k := range []string{"identity", "dirtyKeys", "reconnects", "hashes.rakoit_app"} {
		if !b.Carried[k].Equal(t0) {
			t.Errorf("carried %s dated %v, want the sweep that read it, %v", k, b.Carried[k], t0)
		}
	}
	if _, ok := b.Carried["lssdp"]; ok {
		t.Error("lssdp was read each time: it is not carried")
	}
	if got := carriedNote(*b); got != "ssh facts, zeroconf, manifest, newest bundle as of Sep 20 10:00" {
		t.Errorf("note = %q", got)
	}
	if m := merge(b, fullReport(t0.Add(72*time.Hour))); m.Carried != nil {
		t.Errorf("read again, still carried: %v", m.Carried)
	}
}

// Diff compares the fresh sweep with the merged baseline, so what the last
// sweep could not read is compared with its last known value: a vendor-app
// update behind one lost ssh still shows, where the hollow report it merged
// over would hide it. The report's header says which facts are older.
func TestDiffAgainstMergedBaseline(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	read := fullReport(t0)
	lost := Report{At: t0.Add(24 * time.Hour), SSHErr: "ssh: Connection timed out", Hashes: map[string]string{},
		LSSDP: LSSDPFacts{OK: true, FW: "AR241CE_8530.23.2", NetMode: "WLAN0"}}
	base := merge(&read, lost)
	now := fullReport(t0.Add(48 * time.Hour))
	now.VendorApp, now.Hashes = "43", map[string]string{"luciserver": "465c", "rakoit_app": "9f1b"}
	now.LSSDP.NetMode = "WLAN0"
	var fields []string
	for _, c := range Diff(base, now) {
		fields = append(fields, c.Field+" "+c.Was+" → "+c.Now)
	}
	if got := strings.Join(fields, "; "); got != "vendor app 42 → 43; sha256 rakoit_app 6ab0 → 9f1b" {
		t.Errorf("diff against the merge = %q (lssdp was fresh: WLAN0 both times)", got)
	}
	if ch := Diff(lost, now); len(ch) != 0 {
		t.Errorf("against the hollow report itself the update should not show (the reason for the merge): %+v", ch)
	}
	var out bytes.Buffer
	Write(&out, now, &base, now.At)
	if !strings.Contains(out.String(), "since the last sweep (2026-09-22 10:00, 1d 0h ago · ssh facts, zeroconf, manifest, newest bundle as of Sep 21 10:00)\n") {
		t.Errorf("header:\n%s", out.String())
	}
}

// The header's note groups the older facts by the sweep that read them: the
// ssh-side ones sharing the identity's date fold into "ssh facts", the rest
// are named, in report order. A baseline with no ssh facts at all says so,
// since its "nothing changed" never saw the box's inside.
func TestCarriedNoteGroupsByDate(t *testing.T) {
	d1 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	b := Report{At: d2.Add(24 * time.Hour), Hashes: map[string]string{"airplaydemo": "a", "luciserver": "b"}, Carried: map[string]time.Time{
		"identity": d2, "mcu": d2, "hashes.luciserver": d2, "dirtyKeys": d1, "hashes.airplaydemo": d1, "lssdp": d2, "bundle": d1,
		"retired": d1, // a key this version does not know is ignored
	}}
	if got := carriedNote(b); got != "ssh facts, lssdp as of Sep 22 10:00 · sha256 airplaydemo, env store, newest bundle as of Sep 21 09:00" {
		t.Errorf("note = %q", got)
	}
	delete(b.Carried, "identity")
	if got := carriedNote(b); got != "mcu, sha256 luciserver, lssdp as of Sep 22 10:00 · sha256 airplaydemo, env store, newest bundle as of Sep 21 09:00" {
		t.Errorf("without the identity = %q", got)
	}
	if got := carriedNote(Report{At: d2}); got != "" {
		t.Errorf("nothing carried = %q", got)
	}
	var out bytes.Buffer
	prev := Report{At: d1, LSSDP: LSSDPFacts{OK: true, FW: "AR241CE_8530.23.2"}}
	Write(&out, fullReport(d2), &prev, d2)
	if !strings.Contains(out.String(), "since the last sweep (2026-09-21 09:00, 1d 1h ago · no ssh facts to compare with)\n") {
		t.Errorf("header:\n%s", out.String())
	}
}

// A baseline written before the merge — no carried dates, and the reconnects
// without the per-day series — still loads, merges and diffs: all of it dates
// from its own sweep.
func TestLoadAcceptsABaselineWithoutCarried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sweep-old.json")
	const old = `{
  "at": "2026-09-23T19:05:12.123456-03:00",
  "host": "192.168.0.13",
  "build": "AR241CE_8530",
  "buildDate": "2026-01-12",
  "svn": "318",
  "firmware": "AR241CE_8530",
  "mcu": "23",
  "kernel": "5.15.137",
  "vendorApp": "42",
  "vendorMd5": "b1dadf70",
  "hashes": {"luciserver": "465c90d4"},
  "bootAt": "2026-09-04T14:55:42-03:00",
  "reboot": "cold_boot",
  "uptime": 1656570,
  "tcp": [22, 80, 9095],
  "udp": [1800],
  "spotifyFlags": "0/1",
  "running": ["spotifymusicpro"],
  "dirtyKeys": 33,
  "dirtyKeysOk": true,
  "reconnects": 950,
  "reconnectsSince": "2026-09-01T22:20:00-03:00",
  "syslogFiles": 49,
  "otaLast": "NO_UPDATE",
  "otaAt": "2026-09-23T14:56:15-03:00",
  "lssdp": {"ok": true, "fw": "AR241CE_8530.23.2", "state": "S", "netMode": "ETH0", "name": "Living"},
  "zeroconf": {"ok": true, "port": 9095, "libraryVersion": "3.211.130", "version": "2.10.0"},
  "manifest": {"asked": true, "build": "AR241CE_8530", "upToDate": true, "offered": ""},
  "bundle": {"build": "", "url": "", "size": 0, "lastModified": "", "etag": "", "none": true}
}
`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	b := Load(path)
	if b == nil || b.Carried != nil || b.Build != "AR241CE_8530" || b.Reconnects != 950 || b.ReconnectsByDay != nil || !b.Bundle.None {
		t.Fatalf("an old baseline = %+v", b)
	}
	cur := Report{At: b.At.Add(time.Hour), SSHErr: "ssh: Connection timed out", Hashes: map[string]string{}}
	if m := merge(b, cur); !m.Carried["identity"].Equal(b.At) || m.Reconnects != 950 || m.VendorApp != "42" {
		t.Errorf("merged over an old baseline: %+v", m)
	}
	var out bytes.Buffer
	Write(&out, cur, b, cur.At)
	if !strings.Contains(out.String(), "since the last sweep (2026-09-23 19:05, 1h 0m ago)\n  nothing changed\n") {
		t.Errorf("the old baseline's header:\n%s", out.String())
	}
}

// The syslog half of the device script, run under sh against a fake /data and
// /var/log (zcat and date -r stubbed: the host's zcat may not read gzip, and
// BSD date -r takes seconds, not a file), gives parseDevice what the box
// would — and nothing but counts and stamps: the login-blob line never comes
// back, nor does any whole syslog line. The whole script must parse as shell.
func TestDeviceScriptSyslogLines(t *testing.T) {
	if err := exec.Command("sh", "-n", "-c", deviceScript).Run(); err != nil {
		t.Fatalf("the device script is not valid shell: %v", err)
	}
	dir := t.TempDir()
	rot := filepath.Join(dir, "rot")
	if err := os.Mkdir(rot, 0o700); err != nil {
		t.Fatal(err)
	}
	const lost = " Living user.info spotifymusicpro[811]: The connection to Spotify has been lost"
	gz := func(name string, lines ...string) {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write([]byte(strings.Join(lines, "\n") + "\n"))
		zw.Close()
		if err := os.WriteFile(filepath.Join(rot, name), b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// the oldest file, which only starts the history
	gz("messages432.log.gz", "Sep  1 18:30:00"+lost, "Sep  1 18:31:00"+lost)
	// a boot's first file: its first lines carry the pre-NTP clock
	gz("messages433.log.gz", "Dec 31 21:00:08 Living kern.info kernel: Booting Linux", "Dec 31 21:00:40"+lost,
		"Sep  3 08:15:00"+lost, "Sep  3 08:45:00"+lost)
	// the newest rotated file, with the engine's login line among the losses
	gz("messages434.log.gz", "Sep 22 20:10:00"+lost, "Sep 22 20:40:00 Living user.info spotifymusicpro[811]: SAME USERNAME IS THERE STORE THE BLOB c2VjcmV0",
		"Sep 22 20:59:59"+lost, "Sep 22 21:05:00"+lost)
	live := filepath.Join(dir, "messages.log")
	if err := os.WriteFile(live, []byte("Sep 22 21:10:02 Living syslog.info syslogd started\nSep 22 21:20:00"+lost+"\nSep 23 17:20:00"+lost+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(deviceScript, "for g in /data/log/syslog/")
	if start < 0 {
		t.Fatal("the device script lost its syslog loop")
	}
	script := strings.NewReplacer("/data/log/syslog", rot, "/var/log/syslog/messages.log", live).Replace(deviceScript[start:])
	// date -r gives each file's rotation time: Sep 1 19:00, Sep 3 09:00 and Sep 22 21:10 in the box's zone
	const stub = `zcat() { gzip -dc "$1"; }; date() { case "$2" in */messages432.log.gz) echo 1788300000;; ` +
		`*/messages433.log.gz) echo 1788436800;; *) echo 1790122200;; esac; }; `
	out, err := exec.Command("sh", "-c", stub+script).Output()
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	shape := regexp.MustCompile(`^(rot=\d+|live=.{0,15}|h= *\d+ [A-Z][a-z]{2} [ \d]\d \d\d|end=1)$`)
	for ln := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), "\n") {
		if !shape.MatchString(ln) {
			t.Errorf("the script printed more than a count or a stamp: %q", ln)
		}
	}
	r := Report{At: time.Date(2026, 9, 23, 17, 30, 0, 0, boxZone), Hashes: map[string]string{}}
	parseDevice(&r, string(out))
	// the oldest file only starts the history, the boot's pre-NTP hour is
	// dropped, and Sep 22 21:00 spans the last rotation into the live file
	if r.Reconnects != 2+2+1+1+1 || r.SyslogFiles != 3 || r.Last24h != 5 || !r.ReconnectsSince.Equal(time.Unix(1788300000, 0)) {
		t.Errorf("history = %d over %d files since %v, %d in 24 h; output:\n%s", r.Reconnects, r.SyslogFiles, r.ReconnectsSince, r.Last24h, out)
	}
	if len(r.ReconnectsByDay) != 23 || r.ReconnectsByDay["2026-09-03"] != 2 || r.ReconnectsByDay["2026-09-22"] != 4 || r.ReconnectsByDay["2026-09-23"] != 1 {
		t.Errorf("by day = %v", r.ReconnectsByDay)
	}
}

// A hashed file that an update removed reads "gone": that is an answer, not a
// missing read, so it shows once as a change and the merged baseline keeps it —
// where an unreadable file (no value at all) stays unread and is carried.
func TestHashedFileGoneIsAChangeOnce(t *testing.T) {
	r := Report{Hashes: map[string]string{}}
	parseDevice(&r, "sha:airplaydemo=gone\nsha:luciserver=\n")
	if r.Hashes["airplaydemo"] != "gone" {
		t.Fatalf("hashes = %v, want airplaydemo gone", r.Hashes)
	}
	if _, ok := r.Hashes["luciserver"]; ok {
		t.Errorf("an unreadable file recorded a value: %v", r.Hashes)
	}
	prev := Report{Hashes: map[string]string{"airplaydemo": "c63a32640502", "luciserver": "465c90d4bde7"}}
	ch := Diff(prev, r)
	if len(ch) != 1 || ch[0].Field != "sha256 airplaydemo" || ch[0].Now != "gone" {
		t.Fatalf("diff = %+v, want the one file gone", ch)
	}
	if again := Diff(Report{Hashes: map[string]string{"airplaydemo": "gone"}}, r); len(again) != 0 {
		t.Errorf("gone twice reads as a change: %+v", again)
	}
}
