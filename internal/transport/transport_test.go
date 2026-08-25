package transport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
)

// withRunSecurity swaps the injectable security(1) runner for one test.
func withRunSecurity(t *testing.T, fn func() secOutcome) {
	t.Helper()
	orig := runSecurity
	runSecurity = fn
	t.Cleanup(func() { runSecurity = orig })
}

func TestKeychainPasswordSuccess(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{stdout: "hunter2\n"} })
	pw, err := KeychainPassword()
	if err != nil || pw != "hunter2" {
		t.Fatalf("KeychainPassword = (%q, %v), want (hunter2, nil)", pw, err)
	}
}

// The OS-specific no-item / locked classification (which depends on each tool's
// exit code and stderr) is exercised in transport_darwin_test.go and
// transport_linux_test.go.

func TestKeychainPasswordTimeoutMeansLocked(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{timeout: true} })
	_, err := KeychainPassword()
	te, ok := err.(*TransportError)
	if !ok || !strings.Contains(te.Msg, MarkerLocked) || te.Cadence != 60*time.Second {
		t.Fatalf("err = %v, want locked w/ 60s cadence", err)
	}
}

func TestKeychainPasswordOSErrorIsMarkedNotRaisedRaw(t *testing.T) {
	withRunSecurity(t, func() secOutcome {
		return secOutcome{runErr: &exec.Error{Name: "security", Err: exec.ErrNotFound}}
	})
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerBroken) {
		t.Fatalf("err = %v, want %s", err, MarkerBroken)
	}
	terr := ClassifyStderr(err.Error())
	if terr == nil || terr.Cadence != 60*time.Second {
		t.Fatalf("classify = %v, want fatal w/ 60s cadence", terr)
	}
}

// A clean exit (rc 0) with empty stdout is treated as no-item, not an empty
// password — the platform-independent guard in KeychainPassword (secret-tool can
// legitimately exit 0 with no output; security(1) wouldn't, but the guard is
// defensive). The OS-specific exit-code/stderr classification is covered in the
// per-OS test files; this pins the shared empty-output rule on every platform.
func TestKeychainPasswordEmptyStdoutMeansNoItem(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{rc: 0, stdout: ""} })
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerNoItem) {
		t.Fatalf("err = %v, want %s", err, MarkerNoItem)
	}
}

func TestAskpassFailureRoundtripsToFatalClass(t *testing.T) {
	// marker-drift guard: whatever the askpass markers are, classify_stderr
	// must map them to a fatal class.
	for _, marker := range []string{MarkerNoItem, MarkerLocked, MarkerBroken} {
		terr := ClassifyStderr(marker)
		if terr == nil {
			t.Errorf("classify(%q) = %v, want fatal", marker, terr)
		}
	}
}

func TestRemoteLoopIsValidShellAndWhitelistsMids(t *testing.T) {
	body := RemoteLoop("spotify.com")
	if r := exec.Command("sh", "-n", "-c", body).Run(); r != nil {
		t.Fatalf("remote loop is not valid shell: %v", r)
	}
	if !strings.Contains(body, `case "$mid" in 40|64)`) {
		t.Error("missing command whitelist")
	}
	if !strings.Contains(body, "read -r -t 0 || break") {
		t.Error("missing two-step burst drain idiom")
	}
	if strings.Contains(body, "eval") {
		t.Error("remote loop must not contain eval")
	}
	for _, tag := range []string{"@@B", "@@p", "@@t", "@@v", "@@s", "@@i", "@@c", "@@E"} {
		if !strings.Contains(body, tag) {
			t.Errorf("missing wire tag %q", tag)
		}
	}
	// the @@c capability block reads services read-only (pidof / getenv); pr/gv
	// print "key=value" directly (one exec per service, no capturing subshell), so
	// the keys are emitted at runtime rather than literal in the source. The table
	// is id:envkey:daemon — a service with a daemon reports what is RUNNING next to
	// what is CONFIGURED, because on this box those two disagree (the device's own
	// web page only ever reports the flag, and cannot see a dead daemon).
	for _, want := range []string{
		"echo @@c", `echo "$1=on"`, "getenv", "/proc/[0-9]*/comm",
		"pr bt bluetoothd", "pr cast cast_sample_app", "gv usb USBEnable",
		"gv tidal.env TidalEnabled", "gv qobuz.env QobuzConnectEnabled",
		"spotify.eng=", "spotify.sdk=", "spotify.cfg=",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing @@c capability probe %q", want)
		}
	}
	// The CONFIGURED flag is read only where the init script consults it. AirPlay
	// and DLNA start on every netready whatever theirs says, and Bluetooth and Cast
	// are not switchable at all, so reading those costs a getenv fork (~40ms each)
	// for a value no view may act on — and reporting it invited the wrong reading,
	// that a service whose inert flag disagrees is somehow broken.
	for _, absent := range []string{"airplay.env", "dlna.env", "bt.env", "cast.env"} {
		if strings.Contains(body, absent) {
			t.Errorf("@@c reads %q, a flag its init script never consults", absent)
		}
	}
	// Running daemons come from one pass over /proc, not a fork per service.
	if strings.Contains(body, "pidof") {
		t.Error("@@c still forks pidof; the /proc comm scan replaced it")
	}
	// getenv prints " [ KEY ]: value", so gv must cut the value off the tail before
	// comparing. Matching the raw line against a bare "1" answers "off" for every
	// flag whatever its real value — a silent, total misreport.
	if !strings.Contains(body, "v=${v##*: }") {
		t.Error("gv must strip getenv's \" [ KEY ]: \" prefix before comparing")
	}
	// The two config-writing commands and the log fetch.
	for _, want := range []string{`92) tg "$data"`, "93) echo @@l; lg;", "@@l"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing service/log command %q", want)
		}
	}
	// LibreWireless reference-image baggage the LP10 doesn't market is not probed.
	for _, absent := range []string{"alexa=", "matter=", "roon=", "AVSEnabled", "MatterEnabled", "RoonEnable"} {
		if strings.Contains(body, absent) {
			t.Errorf("@@c should not probe non-marketed capability %q", absent)
		}
	}
}

func TestRemoteLoopSplicesMidWhitelist(t *testing.T) {
	// The __MIDS__ placeholder must be replaced by the fixed transport+volume
	// whitelist — a surviving placeholder would break the device-side case arm.
	body := RemoteLoop("")
	if strings.Contains(body, "__MIDS__") {
		t.Error("__MIDS__ placeholder not spliced")
	}
	if !strings.Contains(body, `case "$mid" in 40|64)`) {
		t.Error("mid whitelist should be spliced into the case arm")
	}
}

func TestRemoteLoopInjectsSanitizedPingHost(t *testing.T) {
	if !strings.Contains(RemoteLoop("open.spotify.com"), `ph='open.spotify.com';`) {
		t.Error("ping host should be injected as ph")
	}
	// metacharacters must not escape the single-quoted assignment — a value
	// that needed stripping is replaced by the default whole
	if got := RemoteLoop("evil';reboot;'"); !strings.Contains(got, `ph='spotify.com';`) {
		t.Errorf("ping host not sanitized: missing default ph in %q", got[:40])
	}
	// an empty / fully-stripped host falls back to the default target
	if !strings.Contains(RemoteLoop(""), `ph='spotify.com';`) {
		t.Error("empty ping host should fall back to spotify.com")
	}
}

func TestSanitizeHost(t *testing.T) {
	// Anything that needed stripping falls back to the default whole — the
	// stripped remainder would be a bogus target (see sanitizeHost).
	cases := map[string]string{
		"spotify.com": "spotify.com", "1.1.1.1": "1.1.1.1",
		"a b;c": "spotify.com", "": "spotify.com", "$(reboot)": "spotify.com", ";|&": "spotify.com",
	}
	for in, want := range cases {
		if got := sanitizeHost(in); got != want {
			t.Errorf("sanitizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRemoteLoopParsesDeviceOutput pulls the actual device-output parse snippets
// out of the loop body and runs them under sh against representative iw/ip-route/
// fwVersion samples, so a future edit that breaks the shell parameter-expansion
// parsing fails here rather than silently on the device (which CI can't run).
func TestRemoteLoopParsesDeviceOutput(t *testing.T) {
	body := RemoteLoop("spotify.com")
	// extractCase returns the marker's case block, balancing nested case/esac (the
	// gateway block nests a "dev" case) so the snippet is self-contained shell.
	extractCase := func(marker string) string {
		i := strings.Index(body, marker)
		if i < 0 {
			t.Fatalf("parse snippet %q not found in remote loop", marker)
		}
		rest := body[i:]
		depth := 0
		for k := 0; k < len(rest); {
			switch {
			case strings.HasPrefix(rest[k:], "case "):
				depth++
				k += len("case ")
			case strings.HasPrefix(rest[k:], "esac"):
				depth--
				if depth == 0 {
					return rest[:k+len("esac")]
				}
				k += len("esac")
			default:
				k++
			}
		}
		t.Fatalf("unbalanced case for %q", marker)
		return ""
	}
	const nlDef = "nl=$(printf '\\nx'); nl=${nl%x}; " // newline sentinel, as the loop builds it
	const route = "ir='default via 192.168.1.1 dev eth0\n192.168.1.0/24 dev eth0'; "
	cases := []struct{ name, setup, marker, result, want string }{
		{"gateway", route + "gw=; dv=",
			`case "$ir" in *"default via "*`, "gw", "192.168.1.1"},
		{"route interface", route + "gw=; dv=",
			`case "$ir" in *"default via "*`, "dv", "eth0"},
		{"ssid with spaces", "wl='Connected\n\tSSID: MyWiFi 5G\n\tfreq: 5180'; ss=",
			`case "$wl" in *"SSID: "*`, "ss", "MyWiFi 5G"},
		{"freq", "wl='\tSSID: x\n\tfreq: 5180\n\tsignal: -43 dBm'; fq=",
			`case "$wl" in *"freq: "*`, "fq", "5180"},
		{"rate", "wl='\tfreq: 5180\n\ttx bitrate: 780.0 MBit/s\n\tbss flags'; rt=",
			`case "$wl" in *"tx bitrate: "*`, "rt", "780.0"},
		{"ping avg", "o='round-trip min/avg/max = 31.460/31.461/31.462 ms'",
			`case "$o" in *"min/avg/max = "*`, "o", "31.461"},
		{"ping failure", "o='1 packets transmitted, 0 received, 100% loss'",
			`case "$o" in *"min/avg/max = "*`, "o", "-"},
		{"fwVersion line", `ln='build_date = "2025-12-24"'; bd=; ap=; pf=`,
			`case "$ln" in *build_date`, "bd", "2025-12-24"},
	}
	for _, c := range cases {
		script := nlDef + c.setup + "; " + extractCase(c.marker) + `; printf '%s' "$` + c.result + `"`
		out, err := exec.Command("sh", "-c", script).Output()
		if err != nil {
			t.Errorf("%s: sh error: %v", c.name, err)
			continue
		}
		if string(out) != c.want {
			t.Errorf("%s: parsed %q, want %q", c.name, string(out), c.want)
		}
	}
}

// TestRemoteLoopStructuralContract guards invariants the Go parser and the
// on-demand-stats protocol depend on: the positional @@s field order, the
// diag-gated stat block, the MID-90 toggle, the playback-only side-effects, and
// the early-break stat scans.
func TestRemoteLoopStructuralContract(t *testing.T) {
	body := RemoteLoop("spotify.com")
	for _, want := range []string{
		// the positional @@s line (new fields are appended at the END so older
		// parsers and fixtures stay compatible; order is also cross-checked by
		// protocol's TestSysStatsFieldOrder)
		`echo "$up $la $lb $lc $ma $mt $nc $fw.$fv $kt-$kr ${tp:--} ${rxb:--} ${txb:--} $sg $lq $pcl $pgw $pnt ${as:--} ${ab:--} ${ar:--} ${af:--} ${ac:--} ${bs:--} ${cf:--} ${r1:--} ${ns:--} ${rxe:--} ${txe:--} ${rxd:--} ${txd:--} ${sv:--}"`,
		// the one-shot raw register ships (device details + multiroom group)
		`echo @@d; LUCI_local -r 92 2>/dev/null; echo @@E;`,
		`echo @@g; LUCI_local -r 39 2>/dev/null; echo @@E;`,
		// the FriendlyName read keeps spaces (suffix-strip, not first-word)
		`case "$fn" in *Data:*) fn=${fn#*Data:}; fn=${fn% Length:*};; *) fn=;; esac;`,
		// the new diag-gated gathers (all default to "-" so absent paths don't break the line)
		`for ad in /proc/asound/card*/pcm*p/sub*; do`,
		`buffer_size) bs=$av;;`,
		`2>/dev/null < /sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq`,
		`ns=${nz%.}`, // Wi-Fi noise floor (for SNR)
		// the @@i block is ONE printf: seventeen of them cost ~230 bytes of the
		// ssh command-length budget, and the loop sits at dropbear's ceiling.
		`printf 'net=%s\niface=%s\nip=%s\n`,
		`\nname=%s\ndata=%s %s\ndns=%s\n' "$net"`,
		`if [ "$dg" = 1 ]; then`,
		`90) case "$data" in 1) dg=1;; *) dg=0;; esac;;`,
		`[ $pc = 1 ] && { i=0; bw=4; idl=0; pc49=0; }`,
		`MemAvailable:) ma=$v; break;;`,
		// position poll gated to every 3rd tick, with the read-flag that keeps the
		// track-skip detector working across skip ticks
		`pc49=$((pc49-1)); if [ $idl -lt 5 ] && [ $pc49 -le 0 ]; then`,
		`if [ $rd -eq 1 ]; then case "$pn" in`,
		// latency ping poll gated to every 3rd tick (mirrors pc49) so an unreachable
		// target can't stall every stats tick; skipped ticks emit "-" (no sample)
		`pgc=$((pgc-1)); if [ $pgc -le 0 ]; then pg "$cip"; pcl=$o;`,
		// pg returns through the shared $o (no capturing subshell) so each ping
		// target costs one fork, not two
		`o=${o%%/*};; *) o=-;; esac; };`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("remote loop missing structural invariant:\n  %s", want)
		}
	}
}

// TestRemoteLoopAudioChainParses proves the ALSA gather (the verbatim snippet from
// the loop) reads state / avail / rate / format / channels / buffer_size correctly
// against real /proc/asound-style files — covering both colon styles, the no-stream
// case, and the multi-PCM glob where only one sub is RUNNING (the closed ones must
// not clobber it) — so a future edit to that hand-written POSIX-sh fails here, not
// silently on the device (which CI can't reach). The sample matches the on-device
// probe of the AR241CE: status has `avail` but no `xruns` line.
func TestRemoteLoopAudioChainParses(t *testing.T) {
	const snip = `as=-; ab=-; ar=-; af=-; ac=-; bs=-; for ad in /proc/asound/card*/pcm*p/sub*; do while read -r ak av ar2; do k=${ak%:}; [ "$av" = ":" ] && av=$ar2; case "$k" in state) as=$av;; avail) ab=$av;; esac; done 2>/dev/null < "$ad/status"; while read -r ak av ar2; do k=${ak%:}; [ "$av" = ":" ] && av=$ar2; case "$k" in rate) ar=$av;; format) af=$av;; channels) ac=$av;; buffer_size) bs=$av;; esac; done 2>/dev/null < "$ad/hw_params"; done`
	if !strings.Contains(RemoteLoop("spotify.com"), snip) {
		t.Fatal("audio-chain gather snippet not found verbatim in the loop")
	}
	// realStatus/realHW mirror the probed AR241CE: aligned colons, avail/avail_max,
	// no xruns line; hw_params carries buffer_size.
	const realStatus = "state: RUNNING\nowner_pid   : 14748\ntrigger_time: 237278.20\ntstamp      : 0.0\ndelay       : 17216\navail       : 4834\navail_max   : 27490\n-----\nhw_ptr      : 2231488\nappl_ptr    : 2248704\n"
	const realHW = "access: MMAP_INTERLEAVED\nformat: S16_LE\nsubformat: STD\nchannels: 2\nrate: 44100 (44100/1)\nperiod_size: 5513\nbuffer_size: 22050\n"
	mk := func(t *testing.T, pcms map[string][2]string) string {
		dir := t.TempDir()
		for pcm, sh := range pcms {
			sub := filepath.Join(dir, "asound", "card0", pcm, "sub0")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(sub, "status"), []byte(sh[0]), 0o644)
			os.WriteFile(filepath.Join(sub, "hw_params"), []byte(sh[1]), 0o644)
		}
		return dir
	}
	run := func(t *testing.T, dir string) string {
		s := strings.Replace(snip, "/proc/asound", filepath.Join(dir, "asound"), 1)
		out, err := exec.Command("sh", "-c", s+`; printf '%s %s %s %s %s %s' "$as" "$ab" "$ar" "$af" "$ac" "$bs"`).Output()
		if err != nil {
			t.Fatalf("sh: %v", err)
		}
		return string(out)
	}
	cases := []struct {
		name string
		pcms map[string][2]string
		want string
	}{
		{"real AR241CE multi-pcm (only pcm1p running, closed ones must not clobber)",
			map[string][2]string{
				"pcm0p": {"closed\n", "closed\n"},
				"pcm1p": {realStatus, realHW},
				"pcm3p": {"closed\n", "closed\n"},
			}, "RUNNING 4834 44100 S16_LE 2 22050"},
		{"attached colons, hi-res",
			map[string][2]string{"pcm0p": {"state: DRAINING\navail: 100\n", "format: S24_LE\nchannels: 2\nrate: 96000 (96000/1)\nbuffer_size: 8192\n"}},
			"DRAINING 100 96000 S24_LE 2 8192"},
		{"no stream open",
			map[string][2]string{"pcm0p": {"closed\n", "closed\n"}}, "- - - - - -"},
	}
	for _, c := range cases {
		if got := run(t, mk(t, c.pcms)); got != c.want {
			t.Errorf("%s: parsed %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRemoteLoopCapabilityProbeParses runs the @@c gather (the verbatim snippet
// from the loop) under sh with pidof/getenv stubbed as shell functions, so a future
// edit to that hand-written POSIX-sh fails here rather than silently on the device.
// pr() keys off a running daemon (pidof), gv() off an env flag (getenv); both print
// "key=value" directly — one exec per service, no capturing subshell.
//
// The getenv stub answers in the device's REAL format (" [ KEY ]: value"). An
// earlier stub echoed a bare value, which let a gv() that never matched anything
// pass: on the device every env-gated service was reported "off" regardless.
func TestRemoteLoopCapabilityProbeParses(t *testing.T) {
	const snip = `gv() { v=$(getenv "$2" 2>/dev/null); v=${v##*: }; case "$v" in 1|true|TRUE|True|on|ON|yes|YES) echo "$1=on";; '') echo "$1=";; *) echo "$1=off";; esac; }; pz() { pl=" "; for pf in /proc/[0-9]*/comm; do read -r pc < $pf 2>/dev/null && pl="$pl$pc "; done; }; pr() { case "$pl" in *" $2 "*) echo "$1=on";; *) echo "$1=off";; esac; }; sy() { eng=; case "$pl" in *" spotifymusicpro "*) eng=spotifymusicpro;; *" newspotifyhifi "*) eng=newspotifyhifi;; esac; echo "spotify.eng=$eng"; sl=; case "$eng" in spotifymusicpro) sl=pro;; newspotifyhifi) sl=hifi;; esac; [ "$eng" = "$sle" ] || { sle=$eng; sk=; [ -n "$sl" ] && sk=$(grep -aom1 'esdk:[0-9.]*-g[0-9a-f]*' /usr/lib/libspotify$sl.so 2>/dev/null | head -1); }; echo "spotify.sdk=${sk#esdk:}"; se=$(getenv SpotifyEnabled 2>/dev/null); sr=$(getenv SpotifyProEnabled 2>/dev/null); case "${se##*: }/${sr##*: }" in 0/1) sc=pro;; 1/0) sc=hifi;; 1/1) sc=both;; *) sc=none;; esac; echo "spotify.cfg=$sc"; }; lp() { xp=; for f in /proc/net/tcp /proc/net/tcp6; do while read -r sl2 la ra stt rest; do [ "$stt" = 0A ] && xp="$xp${la##*:} "; done 2>/dev/null < $f; done; for e in telnet:0017 adb:15B3 web:0050 control:07E2; do case "$xp" in *"${e#*:} "*) echo "${e%%:*}=on";; *) echo "${e%%:*}=off";; esac; done; }; ct() { pz; echo @@c; sy; pr airplay airplaydemo; pr dlna dmr; pr bt bluetoothd; pr cast cast_sample_app; pr tidal tidalConnect; gv tidal.env TidalEnabled; pr qobuz qobuzConnect; gv qobuz.env QobuzConnectEnabled; gv usb USBEnable; lp; echo @@E; }; ct;`
	if !strings.Contains(RemoteLoop("spotify.com"), snip) {
		t.Fatal("capability-probe snippet not found verbatim in the loop")
	}
	// The running-daemon check is now one pass over /proc/<pid>/comm rather than a
	// pidof fork per service, so the scan is pointed at a fake /proc: two "running"
	// daemons (the legacy Spotify engine and bluetoothd) and one unrelated process.
	// /proc/net/tcp{,6} are faked the same way — telnet LISTENs on v4, the web page
	// on v6, and a :2018 socket that is ESTABLISHED (01) must not count as open.
	//
	// getenv answers in the device's REAL format (" [ KEY ]: value"). An earlier
	// stub echoed a bare value, which let a gv() that never matched anything pass:
	// on the device every env-gated service was reported "off" regardless.
	dir := t.TempDir()
	for pid, comm := range map[string]string{"1": "newspotifyhifi", "2": "bluetoothd", "3": "sh"} {
		if err := os.MkdirAll(filepath.Join(dir, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, pid, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "tcp"), []byte(
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
			"   0: 00000000:0017 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1234 1 0 100 0 0 10 0\n"+
			"   1: 0D00A8C0:07E2 1500A8C0:C3A1 01 00000000:00000000 00:00000000 00000000     0        0 1235 1 0 20 4 30 10 -1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tcp6"), []byte(
		"  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
			"   0: 00000000000000000000000000000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1236 1 0 100 0 0 10 0\n"), 0o644)
	local := strings.NewReplacer(
		"/proc/[0-9]*/comm", filepath.Join(dir, "[0-9]*", "comm"),
		"/proc/net/tcp6", filepath.Join(dir, "tcp6"),
		"/proc/net/tcp", filepath.Join(dir, "tcp"),
	).Replace(snip)
	const stub = `getenv() { case "$1" in ` +
		`TidalEnabled) echo " [ TidalEnabled ]: 0";; USBEnable) echo " [ USBEnable ]: off";; ` +
		`SpotifyEnabled) echo " [ SpotifyEnabled ]: 1";; SpotifyProEnabled) echo " [ SpotifyProEnabled ]: 0";; ` +
		`esac; }; `
	out, err := exec.Command("sh", "-c", stub+local).Output()
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	// spotify has no running= line of its own: .eng carries it and the laptop
	// derives the boolean (parseConfInfo), which keeps the loop under the ceiling.
	// .sdk is empty because the stub host has no libspotifyhifi.so to grep, and
	// qobuz.env is empty because that getenv answers nothing at all (unknown, not
	// off). Only tidal and qobuz report a configured flag — the other four are
	// never read.
	const want = "@@c\n" +
		"spotify.eng=newspotifyhifi\nspotify.sdk=\nspotify.cfg=hifi\n" +
		"airplay=off\ndlna=off\nbt=on\ncast=off\n" +
		"tidal=off\ntidal.env=off\nqobuz=off\nqobuz.env=\nusb=off\n" +
		"telnet=on\nadb=off\nweb=on\ncontrol=off\n@@E\n"
	if string(out) != want {
		t.Errorf("capability probe output:\n%q\nwant:\n%q", string(out), want)
	}
}

func TestSSHArgvContract(t *testing.T) {
	t.Setenv("LP10_SSH", "")
	cfg := config.Config{Host: "192.168.1.40", User: "root", Name: "LP10 · Living", VolStep: 2}
	argv := SSHArgv(cfg)
	flat := strings.Join(argv, " ")
	if argv[0] != "ssh" {
		t.Errorf("argv[0] = %q, want ssh", argv[0])
	}
	for _, want := range []string{
		"-F /dev/null", "-T", "UserKnownHostsFile=/dev/null",
		"StrictHostKeyChecking=no", "ConnectTimeout=3",
		"ServerAliveInterval=20", "NumberOfPasswordPrompts=1",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("argv missing %q", want)
		}
	}
	target := cfg.User + "@" + cfg.Host
	found := false
	for _, a := range argv {
		if a == target {
			found = true
		}
	}
	if !found {
		t.Errorf("argv missing target %q", target)
	}
	t.Setenv("LP10_SSH", "/tmp/fake-ssh")
	if SSHArgv(cfg)[0] != "/tmp/fake-ssh" {
		t.Error("LP10_SSH should override the ssh binary")
	}
}

func TestClassifyStderr(t *testing.T) {
	err := ClassifyStderr("root@x: Permission denied (publickey,password).")
	if err == nil || err.Cadence != 10*time.Second {
		t.Errorf("permission-denied: %v", err)
	}
	err = ClassifyStderr("lp10-askpass: keychain-locked\nroot@x: Permission denied.")
	if err == nil || err.Cadence != 60*time.Second {
		t.Errorf("locked: %v", err)
	}
	err = ClassifyStderr("lp10-askpass: no-item\nroot@x: Permission denied.")
	if err == nil || !strings.Contains(err.Error(), StoreHint) {
		t.Errorf("no-item: %v", err)
	}
	if ClassifyStderr("ssh: connect to host x: Operation timed out") != nil {
		t.Error("transient network failure should classify as nil")
	}
	if ClassifyStderr("") != nil {
		t.Error("empty stderr should classify as nil")
	}
}
