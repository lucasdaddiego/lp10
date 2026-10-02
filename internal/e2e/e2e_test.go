// Package e2e holds end-to-end tests that drive the built lp10 binary — in a
// pty, against an in-process fake of the device's :2018 control tunnel
// (testutil.FakeTunnel): the argv contract, the first paint, keys reaching the
// device, a pushed track reaching the screen, the volume bridge, and the exit
// codes and teardown of the quit, interrupt and signal paths.
package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/testutil"
)

// Bounds for one session. The seed alone takes ~1.8 s (twelve queries,
// 150 ms apart), so frame waits get a generous margin over it for a loaded
// CI runner; none of them is a sleep — each returns the moment its condition
// holds.
const (
	waitFor    = 10 * time.Second // a frame, a paint, an exit
	drainFor   = 2 * time.Second  // the pty reader catching up after an exit
	maxOutput  = 16 << 20         // bytes of pty output kept per session
	usageIntro = "lp10: run `lp10` for the live TUI"
)

// coverEnv passes GOCOVERDIR through to the binary when LP10_COVERDIR is set,
// so the coverage-instrumented build (testutil.BuildMain under the same flag)
// writes its execution coverage there for the merged integration profile
// (`make cover`). Empty — a no-op — under a plain `go test`.
func coverEnv() []string {
	if d := os.Getenv("LP10_COVERDIR"); d != "" {
		return []string{"GOCOVERDIR=" + d}
	}
	return nil
}

// hermeticEnv is the binary's whole environment: built from scratch, not
// from os.Environ, so no ambient LP10_* override, TERM_PROGRAM, SSH_TTY or
// locale leaks in from the developer's shell. LP10_HOST pins the host, which
// skips mDNS/LSSDP discovery (it would otherwise find a real LP10 on the LAN);
// LP10_TUNNEL_ADDR points the tunnel at the fake; the three set-but-empty
// variables switch the LSSDP, Spotify ZeroConf and OTA workers off, so nothing
// leaves the laptop.
func hermeticEnv(root, tunnelAddr string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(root, "home"),
		"TMPDIR=" + os.TempDir(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LP10_HOST=127.0.0.1",
		"LP10_TUNNEL_ADDR=" + tunnelAddr,
		"LP10_LSSDP_HOST=",
		"LP10_ZC_ADDR=",
		"LP10_OTA_URL=",
		"LP10_STATE_DIR=" + filepath.Join(root, "state"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
	}
	return append(env, coverEnv()...)
}

// The argv contract: the TUI takes no arguments, so anything unknown is a
// usage error (exit 2, the usage on stderr, nothing on stdout); --version and
// --help are answers (exit 0 on stdout, nothing on stderr). A tunnel address
// nothing listens on keeps a regression that fell through to the TUI off the
// network.
func TestArgvContract(t *testing.T) {
	bin := testutil.BuildMain(t)
	cases := []struct {
		args       []string
		code       int
		stdoutHead string // the stdout must start with this ("" = must be empty)
		stderrHas  string // the stderr must contain this ("" = must be empty)
	}{
		{[]string{"status"}, 2, "", usageIntro},
		{[]string{"--bogus"}, 2, "", usageIntro},
		{[]string{"play", "now"}, 2, "", usageIntro},
		{[]string{"--version"}, 0, "lp10 ", ""},
		{[]string{"-V"}, 0, "lp10 ", ""},
		{[]string{"--help"}, 0, usageIntro, ""},
		{[]string{"-h"}, 0, usageIntro, ""},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), waitFor)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, tc.args...)
			cmd.Env = hermeticEnv(t.TempDir(), "127.0.0.1:1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("run: %v", err)
			}
			if code != tc.code {
				t.Errorf("exit = %d, want %d (stderr %q)", code, tc.code, stderr.String())
			}
			if (tc.stdoutHead == "" && stdout.Len() != 0) || !strings.HasPrefix(stdout.String(), tc.stdoutHead) {
				t.Errorf("stdout = %q, want it to start with %q", stdout.String(), tc.stdoutHead)
			}
			if (tc.stderrHas == "" && stderr.Len() != 0) || !strings.Contains(stderr.String(), tc.stderrHas) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.stderrHas)
			}
		})
	}
}

// termReplies answers the four queries the TUI writes at startup, as a real
// terminal would: synchronized output (DECRQM 2026) supported but off, unicode
// core (2027) unknown, a dark background (OSC 11), no kitty keyboard flags.
// bubbletea v2 does not block its first paint on them (the suite passes with
// no replies at all), but answered, the run takes a real terminal's path: the
// theme follows the reported background and synchronized output turns on.
// The OSC 11 prefix covers both of its terminators (BEL and ST).
var termReplies = []struct{ query, reply string }{
	{"\x1b[?2026$p", "\x1b[?2026;2$y"},
	{"\x1b[?2027$p", "\x1b[?2027;0$y"},
	{"\x1b]11;?", "\x1b]11;rgb:1010/1010/1010\x07"},
	{"\x1b[?u", "\x1b[?0u"},
}

// session is one run of the binary in a pty, wired to its own fake tunnel.
type session struct {
	cmd      *exec.Cmd
	ptmx     *os.File
	tun      *testutil.Tunnel
	stateDir string

	mu      sync.Mutex
	out     []byte
	changed chan struct{} // closed and replaced on every read

	readDone chan struct{} // the pty reader stopped (the child's side closed)
	exited   chan struct{} // cmd.Wait returned
}

// boot starts the binary in a 30×100 pty — the full dashboard — against a
// fresh fake tunnel and hermetic dirs, and waits for its first complete paint
// (the frame's bottom border is written after everything above it). A
// non-empty configTOML becomes the config.toml the binary reads at startup.
func boot(t *testing.T, configTOML string) *session {
	t.Helper()
	bin := testutil.BuildMain(t)
	tun := testutil.FakeTunnel(t)
	root := t.TempDir()
	if configTOML != "" {
		dir := filepath.Join(root, "config", "lp10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(configTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin)
	cmd.Env = hermeticEnv(root, tun.Addr)
	// Real pixel dims, so the binary's cellPixelSize() reads them via TIOCGWINSZ.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100, X: 1000, Y: 600})
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	s := &session{
		cmd: cmd, ptmx: ptmx, tun: tun, stateDir: filepath.Join(root, "state"),
		changed: make(chan struct{}), readDone: make(chan struct{}), exited: make(chan struct{}),
	}
	go s.read()
	go func() {
		_ = cmd.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			_ = cmd.Process.Kill()
			select {
			case <-s.exited:
			case <-time.After(waitFor):
				t.Errorf("lp10 (pid %d) survived SIGKILL", cmd.Process.Pid)
			}
		}
		ptmx.Close()
		select {
		case <-s.readDone:
		case <-time.After(drainFor):
			t.Error("the pty reader did not stop after the pty closed")
		}
	})
	s.waitRaw(t, 0, "┗")
	return s
}

// read copies the pty output into the session and answers the terminal
// queries in it. pending carries unmatched bytes across reads so a query that
// straddles two reads still gets its reply; each match is consumed, so no
// query is answered twice.
func (s *session) read() {
	defer close(s.readDone)
	b := make([]byte, 32<<10)
	var pending []byte
	for {
		n, err := s.ptmx.Read(b)
		if n > 0 {
			s.mu.Lock()
			if len(s.out)+n <= maxOutput {
				s.out = append(s.out, b[:n]...)
			}
			close(s.changed)
			s.changed = make(chan struct{})
			s.mu.Unlock()
			pending = append(pending, b[:n]...)
			for {
				at, which := -1, -1
				for i, q := range termReplies {
					if j := bytes.Index(pending, []byte(q.query)); j >= 0 && (at < 0 || j < at) {
						at, which = j, i
					}
				}
				if which < 0 {
					break
				}
				_, _ = s.ptmx.Write([]byte(termReplies[which].reply))
				pending = pending[at+len(termReplies[which].query):]
			}
			if keep := 16; len(pending) > keep { // longer than any query
				pending = pending[len(pending)-keep:]
			}
		}
		if err != nil {
			return
		}
	}
}

// mark is the current output length: a later wait from it sees only what the
// binary wrote after this point.
func (s *session) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.out)
}

// output returns everything the binary wrote from byte offset from on.
func (s *session) output(from int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.out[min(from, len(s.out)):])
}

// waitOut waits until cond holds for the output from offset from on, and fails
// the test with the (escaped) output tail if it does not within waitFor.
func (s *session) waitOut(t *testing.T, from int, what string, cond func(string) bool) {
	t.Helper()
	deadline := time.NewTimer(waitFor)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		out, changed := string(s.out[min(from, len(s.out)):]), s.changed
		s.mu.Unlock()
		if cond(out) {
			return
		}
		select {
		case <-changed:
		case <-s.readDone:
			if !cond(s.output(from)) {
				t.Fatalf("the binary closed the pty before %s; output:\n%q", what, tail(s.output(from)))
			}
			return
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s; output:\n%q", what, tail(s.output(from)))
		}
	}
}

// waitRaw waits for want in the raw output bytes.
func (s *session) waitRaw(t *testing.T, from int, want string) {
	t.Helper()
	s.waitOut(t, from, "output "+strconv.Quote(want), func(out string) bool { return strings.Contains(out, want) })
}

// waitScreen waits until every word shows in the output with its escape
// sequences stripped. The renderer writes only the cells that changed, with
// cursor moves between runs, so a phrase can arrive split around an escape:
// each word on its own is what reliably reaches the stream intact.
func (s *session) waitScreen(t *testing.T, from int, words ...string) {
	t.Helper()
	s.waitOut(t, from, "screen text "+strconv.Quote(strings.Join(words, " ")), func(out string) bool {
		text := ansi.Strip(out)
		for _, w := range words {
			if !strings.Contains(text, w) {
				return false
			}
		}
		return true
	})
}

// key types into the pty, as the user would.
func (s *session) key(t *testing.T, k string) {
	t.Helper()
	if _, err := s.ptmx.Write([]byte(k)); err != nil {
		t.Fatalf("type %q: %v", k, err)
	}
}

// waitExit waits for the binary to exit and for the pty reader to catch up
// with what it wrote on the way out, and returns the exit code (-1: killed by
// a signal it did not handle).
func (s *session) waitExit(t *testing.T) int {
	t.Helper()
	select {
	case <-s.exited:
	case <-time.After(waitFor):
		_ = s.cmd.Process.Kill()
		t.Fatalf("lp10 did not exit; output:\n%q", tail(s.output(0)))
	}
	select {
	case <-s.readDone:
	case <-time.After(drainFor):
	}
	return s.cmd.ProcessState.ExitCode()
}

// seeded waits for the app to have dialed the fake and sent its whole seed:
// STA first, VER last (tunnel.SeedQueries). Every reply before VER has been
// applied by then — the seed spaces its queries 150 ms apart — so the app
// holds the fake's volume (VolLive) and its EQ.
func (s *session) seeded(t *testing.T) int {
	t.Helper()
	_, i := s.tun.WaitFrame(t, 0, waitFor, testutil.Is("VER"))
	if fr := s.tun.Frames(); fr[0] != "STA" {
		t.Errorf("first frame = %q, want the STA status query first", fr[0])
	}
	return i + 1
}

func tail(s string) string {
	if len(s) > 4096 {
		return "…" + s[len(s)-4096:]
	}
	return s
}

// crashed reports a Go panic or fatal error in the output.
func crashed(out string) bool {
	return strings.Contains(out, "panic:") || strings.Contains(out, "goroutine ") || strings.Contains(out, "fatal error:")
}

// The smoke test: the binary paints its frame with the device name, dials the
// tunnel and seeds it (STA first, every EQ control, the presets, VER last),
// and a q from the player quits cleanly.
func TestSmokeUnderPTY(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	s.waitScreen(t, 0, "LP10")
	s.seeded(t)
	frames := s.tun.Frames()
	for _, code := range []string{"MXV", "EQE", "EQS", "BAS", "MID", "TRE", "VBS", "VBI", "BAL", "PEQ"} {
		if !slices.Contains(frames, code) {
			t.Errorf("seed never queried %s; frames %q", code, frames)
		}
	}
	s.key(t, "q")
	if code := s.waitExit(t); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if out := s.output(0); crashed(out) {
		t.Errorf("crash in output:\n%s", out)
	}
}

// Every player key the device must hear reaches the tunnel as its wire frame:
// space toggles (POP), n skips (NXT), + sets the volume one vol_step (2) above
// the fake's 44, m mutes. Each wait starts after the previous frame, so a
// frame sent earlier cannot satisfy it. The keys start after the volume
// bridge's first write (the fake's own VOL:44 sent back), so no bridge write
// lands among them.
func TestKeysReachTheDevice(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	from := s.seeded(t)
	_, i := s.tun.WaitFrame(t, from, waitFor, testutil.Is("VOL:44"))
	from = i + 1
	steps := []struct {
		key, frame string
	}{
		{" ", "POP"},
		{"n", "NXT"},
		{"+", "VOL:46"},
		{"m", "MUT:1"},
	}
	for _, step := range steps {
		s.key(t, step.key)
		_, at := s.tun.WaitFrame(t, from, waitFor, testutil.Is(step.frame))
		from = at + 1
	}
	s.key(t, "q")
	if code := s.waitExit(t); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// The volume bridge (firmware AR241CP_8747 broke the Spotify app's volume:
// the MCU register moves, the room does not): the first level a connection
// reads goes back out as a VOL set, and so does a later level the app did not
// set itself — here the fake's own volume moving behind the app's back, the
// way the Spotify app moves it, seen by the next STA poll.
func TestVolumeBridgeResendsTheDeviceLevel(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	from := s.seeded(t)
	_, i := s.tun.WaitFrame(t, from, waitFor, testutil.Is("VOL:44"))
	s.tun.SetVolume(30)
	s.tun.WaitFrame(t, i+1, waitFor, testutil.Is("VOL:30"))
	s.key(t, "q")
	if code := s.waitExit(t); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// A track the device pushes (plain UTF-8 TIT/ART/ALB, as the live box sends
// on a track change) reaches both the frame and the terminal's window title.
// The title is one escape sequence, so it arrives whole; the frame's words
// are checked one by one (see waitScreen).
func TestPushedTrackShowsOnScreen(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	s.tun.WaitFrame(t, 0, waitFor, testutil.Is("STA")) // a client is connected to push to
	at := s.mark()
	s.tun.Push(t, "TIT:Pajarito;ART:Señalemelo;ALB:Big Bang;")
	s.waitScreen(t, at, "Pajarito", "Señalemelo")
	s.waitOut(t, at, "the track in the window title", func(out string) bool {
		return slices.Contains(titles(out), "♪ Pajarito — Señalemelo")
	})
	s.key(t, "q")
	if code := s.waitExit(t); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// titleRe matches a window-title escape (OSC 0 or OSC 2), BEL- or
// ST-terminated, capturing the title.
var titleRe = regexp.MustCompile("\x1b\\][02];([^\x07\x1b]*)(?:\x07|\x1b\\\\)")

// titles returns every window title set in out, in order.
func titles(out string) []string {
	var ts []string
	for _, m := range titleRe.FindAllStringSubmatch(out, -1) {
		ts = append(ts, m[1])
	}
	return ts
}

// Quitting hands the terminal back as it was: the last window title set is
// the empty reset (after the frames titled it "LP10"), and the alternate
// screen is left.
func TestQuitResetsTerminal(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	s.key(t, "q")
	if code := s.waitExit(t); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	out := s.output(0)
	ts := titles(out)
	if !slices.Contains(ts, "LP10") {
		t.Errorf("the frames never titled the window LP10; titles %q", ts)
	}
	if len(ts) == 0 || ts[len(ts)-1] != "" {
		t.Errorf("the last window title is not the reset; titles %q", ts)
	}
	if i, j := strings.LastIndex(out, "\x1b[?1049h"), strings.LastIndex(out, "\x1b[?1049l"); j < 0 || j < i {
		t.Errorf("the alternate screen was not left on quit (enter at %d, leave at %d)", i, j)
	}
}

// Ctrl-C as a key (the pty is raw, so it reaches the TUI as a byte, not a
// signal) quits with the shell's code for an interrupt.
func TestCtrlCExits130(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	s.key(t, "\x03")
	if code := s.waitExit(t); code != 130 {
		t.Errorf("exit code = %d, want 130 on Ctrl-C", code)
	}
}

// SIGTERM ends the run through Run's own teardown, not a bare exit: the
// terminal title is reset (Run writes it after the workers close), and the
// runtime's Close persisted the snapshot with the device's own values — the
// fake's volume 44 and EQ, which no default carries.
func TestSigtermExits143AndCleansUp(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	s.seeded(t)
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := s.waitExit(t); code != 143 {
		t.Errorf("exit code = %d, want 143 on SIGTERM", code)
	}
	if !strings.Contains(s.output(0), "\x1b]0;\x07") {
		t.Error("Run's terminal-title reset did not run on SIGTERM")
	}
	snaps, _ := filepath.Glob(filepath.Join(s.stateDir, "snapshot-*.json"))
	if len(snaps) != 1 {
		t.Fatalf("snapshots in the state dir = %q, want one", snaps)
	}
	snap := config.LoadSnapshot(snaps[0])
	if snap == nil || snap.Vol != 44 || snap.EQ["MXV"] != 100 || snap.EQ["VBI"] != 50 {
		t.Errorf("persisted snapshot = %+v, want the fake's volume 44, MXV 100, VBI 50", snap)
	}
}

// A SIGINT delivered as a signal (not the Ctrl-C byte of TestCtrlCExits130)
// is caught by Run's signal goroutine and maps to 130.
func TestSigintSignalExits130(t *testing.T) {
	t.Parallel()
	s := boot(t, "")
	if err := s.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if code := s.waitExit(t); code != 130 {
		t.Errorf("exit code = %d, want 130 on SIGINT", code)
	}
}

// A config problem is shown, not swallowed: config.Load sets cfg.Warn and
// Run notes it before the first paint. A file that does not parse is ignored
// whole; a key retired with ssh (firmware AR241CP_8747) says so rather than
// reading as an unknown key.
func TestConfigWarningsSurface(t *testing.T) {
	cases := []struct {
		name, toml string
		words      []string
	}{
		{"broken", "not = valid = toml [", []string{"config.toml", "ignored"}},
		{"retired key", "user = \"root\"\n", []string{"user", "retired"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := boot(t, tc.toml)
			s.waitScreen(t, 0, tc.words...)
			s.key(t, "q")
			if code := s.waitExit(t); code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
		})
	}
}
