package sweep

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The device script's output as the box printed it on 2026-09-12 (hashes
// shortened, one MAC-free line each).
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
reconnects=88
ota_last=Sep 12 14:56:15:624239 E/ota[923]: ota: OTA:error string =  No update available
junk line without an equals sign
`

func TestParseDevice(t *testing.T) {
	r := Report{Hashes: map[string]string{}}
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
	if r.DirtyKeys != 33 || r.Reconnects != 88 || !strings.Contains(r.OTALast, "No update available") {
		t.Errorf("counts = %d %d %q", r.DirtyKeys, r.Reconnects, r.OTALast)
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
		"boot           Sep 4 14:55 · power-on · up 8d 0h",
		"vendor app     v32 · md5 9aa7f360179d…",
		"manifest       no update for AR241CE_8530",
		"newest bundle  AR241CE_8530 · " + cdn.URL,
		"89751552 bytes · Thu, 20 Aug 2026 07:55:48 GMT · etag 6a86b304-5598000",
		"lssdp          AR241CE_8530.23.2 · S · ETH0 · Living",
		"spotify        :9095 · eSDK 3.211.130-g110e3e03 · zeroconf 2.10.0",
		"spotify flags  0/1",
		"env store      33 keys set at runtime",
		"reconnects     88 in the retained syslog",
		"first sweep — baseline saved",
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
	stdout.Reset()
	code = Main(ctx, config.Config{Host: "192.0.2.13", User: "root"}, []string{"--json"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), `"host": "192.0.2.13"`) {
		t.Errorf("--json: exit %d, out:\n%s", code, stdout.String())
	}
	if Load(config.SweepPath(config.Config{Host: "192.0.2.13"})) == nil {
		t.Error("baseline not saved")
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
