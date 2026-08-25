package transport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shRun executes a shell fragment, surfacing sh's own stderr on failure.
func shRun(script string) error {
	cmd := exec.Command("sh", "-c", script)
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return &shError{err: err, stderr: errb.String()}
	}
	return nil
}

type shError struct {
	err    error
	stderr string
}

func (e *shError) Error() string { return e.err.Error() + ": " + e.stderr }

// TestRemoteLoopServiceToggleParses runs tg() — the MID-92 service toggle, the
// one command in the loop that writes device CONFIG rather than playback — under
// sh with setenv/killall/setsid/ct stubbed, and asserts the exact env writes and
// init-script kick for every accepted state.
//
// The Spotify arm is the reason this test exists. The vendor ships two engines
// whose init scripts are each guarded on the OTHER flag being clear, with no
// both-set fallback, so a toggle that wrote one flag and left the other alone
// would land the box in the state where NEITHER engine starts — exactly what the
// AR241CE_8530 OTA did to this device. tg() must always write the pair.
func TestRemoteLoopServiceToggleParses(t *testing.T) {
	const snip = `sn() { setenv "$1" "$2" >/dev/null; }; tg() { vid=${1%% *}; vst=${1##* }; vk=; vs=; case "$vid" in spotify) killall -9 newspotifyhifi spotifymusicpro >/dev/null 2>&1; va=0; vb=0; case "$vst" in hifi) va=1; vs=S99newspotifyhifi;; pro) vb=1; vs=S99spotifymusicpro;; esac; sn SpotifyEnabled $va; sn SpotifyProEnabled $vb;; tidal) vk=TidalEnabled; vs=S99tidalConnect;; qobuz) vk=QobuzConnectEnabled; vs=S99qobuzConnect;; usb) vk=USBEnable;; airplay) vs=S99airplay_v2;; dlna) vs=S99dmr;; *) return;; esac; [ -n "$vk" ] && sn "$vk" "$vst"; if [ -n "$vs" ]; then vc=netready; case "$vst" in 0|off) vc=netdown;; esac; setsid /etc/init.d/$vs $vc </dev/null >/dev/null 2>&1 & fi; ct; cq=8; };`
	if !strings.Contains(RemoteLoop("spotify.com"), snip) {
		t.Fatal("service-toggle snippet not found verbatim in the loop")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "acts")
	// Stub every side effect to an append-only log, so the test sees the exact
	// sequence the device would perform. ct is stubbed out: the re-read is covered
	// by the capability-probe test and would need the whole gather here.
	stub := `A=` + log + `; ` +
		`setenv() { echo "setenv $1=$2" >> $A; }; ` +
		`killall() { echo "killall $*" >> $A; }; ` +
		`setsid() { echo "kick $1 $2" >> $A; }; ` +
		`ct() { :; }; `
	run := func(t *testing.T, arg string) []string {
		t.Helper()
		os.Remove(log)
		if err := shRun(stub + snip + ` tg "` + arg + `"; wait`); err != nil {
			t.Fatalf("sh %q: %v", arg, err)
		}
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	}
	cases := []struct {
		arg  string
		want []string
	}{
		// Both flags written every time, and only the chosen engine's script kicked.
		{"spotify hifi", []string{
			"killall -9 newspotifyhifi spotifymusicpro",
			"setenv SpotifyEnabled=1", "setenv SpotifyProEnabled=0",
			"kick /etc/init.d/S99newspotifyhifi netready"}},
		{"spotify pro", []string{
			"killall -9 newspotifyhifi spotifymusicpro",
			"setenv SpotifyEnabled=0", "setenv SpotifyProEnabled=1",
			"kick /etc/init.d/S99spotifymusicpro netready"}},
		// "off" clears BOTH and kicks nothing — the killall is the whole action.
		{"spotify off", []string{
			"killall -9 newspotifyhifi spotifymusicpro",
			"setenv SpotifyEnabled=0", "setenv SpotifyProEnabled=0"}},
		{"tidal 1", []string{"setenv TidalEnabled=1", "kick /etc/init.d/S99tidalConnect netready"}},
		{"tidal 0", []string{"setenv TidalEnabled=0", "kick /etc/init.d/S99tidalConnect netdown"}},
		{"qobuz 1", []string{"setenv QobuzConnectEnabled=1", "kick /etc/init.d/S99qobuzConnect netready"}},
		// usb has no daemon to kick: the flag is read at boot, nothing else.
		{"usb 1", []string{"setenv USBEnable=1"}},
		// airplay/dlna have no env gate at all — their init scripts ignore the flag —
		// so the only honest action is stop/start, and no setenv is written.
		{"airplay 0", []string{"kick /etc/init.d/S99airplay_v2 netdown"}},
		{"dlna 1", []string{"kick /etc/init.d/S99dmr netready"}},
		// an id the loop does not know must do nothing at all
		{"bt 1", nil},
		{"nonsense 1", nil},
	}
	for _, c := range cases {
		got := run(t, c.arg)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("tg %q:\n got %q\nwant %q", c.arg, got, c.want)
		}
	}
}
