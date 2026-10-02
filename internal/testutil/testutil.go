// Package testutil provides shared helpers for the test suite: an env-isolation
// fixture, a builder for the lp10 command binary, and an in-process fake of
// the device's :2018 control tunnel. Imported only from _test.go files.
package testutil

import (
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// envVars are the ambient LP10_* overrides a test must not inherit.
var envVars = []string{
	"LP10_HOST", "LP10_STATE_DIR", "LP10_TUNNEL_ADDR",
	"LP10_LSSDP_HOST", "LP10_ZC_ADDR", "LP10_OTA_URL",
}

// Isolate clears ambient LP10_* env and points state + config at temp dirs, so
// no test touches the real state dir or config. Set-but-empty is what each
// variable reads as here: config.Load and the tunnel address treat an empty
// LP10_HOST / LP10_TUNNEL_ADDR as unset, and an empty LP10_LSSDP_HOST,
// LP10_ZC_ADDR or LP10_OTA_URL switches that probe off — so nothing leaves the
// laptop from an isolated test.
func Isolate(t *testing.T) {
	t.Helper()
	for _, v := range envVars {
		t.Setenv(v, "")
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
}

// Cleanup removes the lp10 binary's temp dir. Call it from the importing
// package's TestMain after m.Run: the binary is built once per test binary
// into an os.MkdirTemp dir that nothing else removes, and every `go test ./...`
// would otherwise leave another (~13 MB) behind in $TMPDIR.
func Cleanup() {
	if mainDir != "" {
		os.RemoveAll(mainDir)
	}
}

// goBuildArgs returns the `go build` args for the lp10 binary, adding coverage
// instrumentation when LP10_COVERDIR is set so the e2e subprocess execution of
// main/tui.Run/the workers counts toward the merged integration-coverage
// profile (the `make cover` flow). Without it, the binary builds plain.
func goBuildArgs(bin, pkg string) []string {
	args := []string{"build"}
	if os.Getenv("LP10_COVERDIR") != "" {
		// Module-absolute pattern (not ./...): this build runs with the test's CWD
		// (e.g. internal/e2e), where ./... would match nothing in the binary.
		args = append(args, "-cover", "-coverpkg=github.com/lucasdaddiego/lp10/...")
	}
	return append(args, "-o", bin, pkg)
}

var (
	mainOnce sync.Once
	mainPath string
	mainDir  string
	mainErr  error
)

// BuildMain builds (once per test binary) and returns the path to the lp10
// command binary, for end-to-end tests.
func BuildMain(t *testing.T) string {
	t.Helper()
	mainOnce.Do(func() {
		tmp, e := os.MkdirTemp("", "lp10-bin-")
		if e != nil {
			mainErr = &buildError{e, ""}
			return
		}
		mainDir = tmp
		bin := filepath.Join(tmp, "lp10")
		out, e := exec.Command("go", goBuildArgs(bin,
			"github.com/lucasdaddiego/lp10")...).CombinedOutput()
		if e != nil {
			mainErr = &buildError{e, string(out)}
			return
		}
		mainPath = bin
	})
	if mainErr != nil {
		t.Fatalf("build lp10: %v", mainErr)
	}
	return mainPath
}

type buildError struct {
	err error
	out string
}

func (b *buildError) Error() string { return b.err.Error() + "\n" + b.out }

// Tunnel bounds: a fake device must not grow without limit whatever a broken
// client sends it.
const (
	maxTunnelConns  = 8    // simultaneous client connections; more are refused
	maxTunnelFrames = 4096 // frames kept in the log; later ones are answered, not kept
	maxTunnelCarry  = 4096 // bytes of a ';'-free partial frame before it is dropped
)

// fakePresets is the PEQ reply of the live box (MCU 29).
const fakePresets = "0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal"

// fakeEQ is the fake's starting equalizer: each code the app seeds, at a
// value the live box could hold.
var fakeEQ = map[string]int{
	"MXV": 100, "EQE": 1, "EQS": 0, "BAS": 0, "MID": 0, "TRE": 0, "VBS": 0, "VBI": 50, "BAL": 0,
}

// Tunnel is an in-process fake of the LP10's control tunnel (TCP :2018, the
// Arylic UART API): a loopback listener that answers the getters the app
// seeds and polls from one consistent fake state, applies the sets it
// receives and echoes them back, toggles play on POP, and logs every frame a
// client sent. Point the app at it with LP10_TUNNEL_ADDR=<Addr>. Like the
// device, it broadcasts each reply to every connected client.
type Tunnel struct {
	Addr string // host:port of the listener (127.0.0.1, an ephemeral port)

	ln net.Listener
	wg sync.WaitGroup

	mu      sync.Mutex
	conns   map[net.Conn]bool
	frames  []string      // every frame received (up to maxTunnelFrames), in arrival order, without the ';'
	changed chan struct{} // closed and replaced whenever frames or conns change
	closed  bool

	// the fake device state, guarded by mu
	vol     int
	muted   bool
	playing bool
	eq      map[string]int
}

// FakeTunnel starts a fake device on a loopback port. Its starting state is
// the live box's as read on 2026-10-01 (source NET, volume 44, unmuted, MCU
// 29, the six stock presets), except that it starts paused, so the player
// opens on its idle screen. The test's cleanup closes the listener and every
// connection and waits for all of its goroutines.
func FakeTunnel(t testing.TB) *Tunnel {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake tunnel: listen: %v", err)
	}
	f := &Tunnel{
		Addr:    ln.Addr().String(),
		ln:      ln,
		conns:   map[net.Conn]bool{},
		changed: make(chan struct{}),
		vol:     44,
		eq:      maps.Clone(fakeEQ),
	}
	f.wg.Add(1)
	go f.accept()
	t.Cleanup(f.Close)
	return f
}

// Close stops the fake: the listener, every connection, every goroutine.
// Safe to call more than once.
func (f *Tunnel) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.ln.Close()
	for c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

func (f *Tunnel) accept() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return // closed
		}
		f.mu.Lock()
		if f.closed || len(f.conns) >= maxTunnelConns {
			f.mu.Unlock()
			c.Close()
			continue
		}
		f.conns[c] = true
		f.notifyLocked()
		f.mu.Unlock()
		f.wg.Add(1)
		go f.serve(c)
	}
}

// serve reads one client's frames until it goes away, answering each.
func (f *Tunnel) serve(c net.Conn) {
	defer f.wg.Done()
	defer func() {
		c.Close()
		f.mu.Lock()
		delete(f.conns, c)
		f.notifyLocked()
		f.mu.Unlock()
	}()
	buf := make([]byte, 1024)
	var carry string
	for {
		n, err := c.Read(buf)
		if n > 0 {
			carry += string(buf[:n])
			for {
				frame, rest, ok := strings.Cut(carry, ";")
				if !ok {
					break
				}
				carry = rest
				f.handle(frame)
			}
			if len(carry) > maxTunnelCarry {
				carry = ""
			}
		}
		if err != nil {
			return
		}
	}
}

// handle logs one frame and answers it the way the device does.
func (f *Tunnel) handle(frame string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) < maxTunnelFrames {
		f.frames = append(f.frames, frame)
		f.notifyLocked()
	}
	if reply := f.answerLocked(frame); reply != "" {
		f.broadcastLocked(reply)
	}
}

// answerLocked is the device's reply to one frame ("" for none): a getter
// reads the fake state, a set changes it and echoes the applied value, POP
// toggles play and pushes the new play state (with the vendor on a resume, as
// the live box does), NXT pushes what the live box pushes after a skip.
// Anything else gets no answer, as an unknown code gets none on the device.
func (f *Tunnel) answerLocked(frame string) string {
	code, val, isSet := strings.Cut(frame, ":")
	if !isSet {
		switch code {
		case "STA":
			return "STA:NET," + bit(f.muted) + "," + strconv.Itoa(f.vol) + ",0,0,3,0," + bit(f.playing) + ",1,0;"
		case "VOL":
			return "VOL:" + strconv.Itoa(f.vol) + ";"
		case "MUT":
			return "MUT:" + bit(f.muted) + ";"
		case "PLA":
			return "PLA:" + bit(f.playing) + ";"
		case "SRC":
			return "SRC:NET;"
		case "VER":
			return "VER:29-1d316f0c-10;"
		case "PEQ":
			return "PEQ:" + fakePresets + ";"
		case "POP":
			f.playing = !f.playing
			if f.playing {
				return "PLA:1;VND:spotify;"
			}
			return "PLA:0;"
		case "NXT":
			return "RAW:NEXT;"
		}
		if v, ok := f.eq[code]; ok {
			return code + ":" + strconv.Itoa(v) + ";"
		}
		return ""
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return ""
	}
	switch code {
	case "VOL":
		f.vol = max(0, min(100, n))
		return "VOL:" + strconv.Itoa(f.vol) + ";"
	case "MUT":
		f.muted = n == 1
		return "MUT:" + bit(f.muted) + ";"
	}
	if _, ok := f.eq[code]; ok {
		f.eq[code] = n
		return code + ":" + strconv.Itoa(n) + ";"
	}
	return ""
}

func bit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// broadcastLocked writes s to every connected client, as the device does. A
// write is bounded by a deadline so a client that stopped reading cannot
// wedge the fake; a client whose write fails is closed (its serve loop then
// removes it).
func (f *Tunnel) broadcastLocked(s string) int {
	n := 0
	for c := range f.conns {
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write([]byte(s)); err != nil {
			c.Close()
			continue
		}
		n++
	}
	return n
}

func (f *Tunnel) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// Push writes raw frames (e.g. "TIT:Song;ART:Artist;") to every connected
// client, as the device pushes a track change. It fails the test when no
// client is connected: a push nobody receives proves nothing.
func (f *Tunnel) Push(t testing.TB, frames string) {
	t.Helper()
	f.mu.Lock()
	n := f.broadcastLocked(frames)
	f.mu.Unlock()
	if n == 0 {
		t.Fatalf("fake tunnel: push %q reached no client", frames)
	}
}

// SetVolume changes the fake's volume behind the clients' backs and pushes
// nothing — as the Spotify app's volume does on the live box: the MCU
// register moves, and only the next status poll shows it.
func (f *Tunnel) SetVolume(v int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vol = max(0, min(100, v))
}

// Frames returns a copy of every frame the clients sent so far, in arrival
// order, each without its ';' terminator (e.g. "STA", "VOL:46", "POP").
func (f *Tunnel) Frames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.frames...)
}

// WaitFrame waits up to timeout for a received frame that match accepts, at
// index from or later in Frames (so a test can wait for a frame sent after a
// keypress, not an earlier one), and returns it with its index. It fails the
// test on timeout, listing what did arrive.
func (f *Tunnel) WaitFrame(t testing.TB, from int, timeout time.Duration, match func(string) bool) (string, int) {
	t.Helper()
	frame, i, ok := f.waitFrame(from, timeout, match)
	if !ok {
		got := f.Frames()
		full := ""
		if len(got) >= maxTunnelFrames {
			full = " (the log is full: later frames are not kept)"
		}
		t.Fatalf("fake tunnel: timed out after %v waiting for a frame from index %d; received%s %q", timeout, from, full, got)
	}
	return frame, i
}

func (f *Tunnel) waitFrame(from int, timeout time.Duration, match func(string) bool) (string, int, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		f.mu.Lock()
		for i := max(from, 0); i < len(f.frames); i++ {
			if match(f.frames[i]) {
				fr := f.frames[i]
				f.mu.Unlock()
				return fr, i, true
			}
		}
		changed := f.changed
		f.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			return "", -1, false
		}
	}
}

// Is returns a WaitFrame matcher for one exact frame (without the ';').
func Is(frame string) func(string) bool {
	return func(s string) bool { return s == frame }
}
