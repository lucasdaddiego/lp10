package tui

// Run end to end, in process: a pty stands in for the terminal (Run and
// Bubble Tea read os.Stdin and write os.Stdout), every worker is pointed away
// from the LAN, and each test ends the program the way a user or the system
// would. internal/e2e drives the built binary the same way; these cover the
// package's own lifecycle code under `go test -cover`.

import (
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"

	"github.com/lucasdaddiego/lp10/internal/config"
)

// ptyScreen collects what the program writes to the pty.
type ptyScreen struct {
	mu   sync.Mutex
	out  strings.Builder
	done chan struct{}
}

func (s *ptyScreen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// waitFor polls (every 5 ms, up to 10 s) until the output contains want.
func (s *ptyScreen) waitFor(want string) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), want) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// refusedAddr is a loopback address nothing listens on: the tunnel worker's
// dials are refused, so the box never "connects" (and the media-key tap, if
// the host grants it, passes every key through).
func refusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// runInPty runs Run with a 30×100 pty as stdin and stdout. end is called once
// the first full frame is drawn — the signal handler is installed by then —
// and must make Run return. It reports Run's results and everything drawn;
// os.Stdin and os.Stdout are restored before it returns, so the caller
// asserts on a normal test output.
func runInPty(t *testing.T, end func(ptmx *os.File)) (code int, screen string, err error) {
	t.Helper()
	t.Setenv("LP10_TUNNEL_ADDR", refusedAddr(t))
	t.Setenv("LP10_LSSDP_HOST", "")
	t.Setenv("LP10_ZC_ADDR", "")
	t.Setenv("LP10_OTA_URL", "")
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	t.Setenv("TERM", "xterm-256color")

	ptmx, tty, perr := pty.Open()
	if perr != nil {
		t.Skipf("no pty on this host: %v", perr)
	}
	scr := &ptyScreen{done: make(chan struct{})}
	defer func() {
		tty.Close()
		ptmx.Close()
		select {
		case <-scr.done:
		case <-time.After(5 * time.Second):
			t.Error("the pty reader did not stop after the pty closed")
		}
	}()
	if perr := pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 100, X: 1000, Y: 600}); perr != nil {
		t.Fatal(perr)
	}
	go func() {
		defer close(scr.done)
		buf := make([]byte, 32<<10)
		for {
			n, rerr := ptmx.Read(buf)
			scr.mu.Lock()
			scr.out.Write(buf[:n])
			scr.mu.Unlock()
			if rerr != nil {
				return
			}
		}
	}()

	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = tty, tty
	type result struct {
		code int
		err  error
	}
	ran := make(chan result, 1)
	go func() {
		c, e := Run(config.Config{Host: "127.0.0.1", Name: "LP10 · Test", VolStep: 2, Theme: "dark",
			Warn: "config.toml: unknown key"})
		ran <- result{c, e}
	}()
	var res result
	framed := scr.waitFor("┗")
	if framed {
		end(ptmx)
	}
	select {
	case res = <-ran:
	case <-time.After(10 * time.Second):
		os.Stdin, os.Stdout = stdin, stdout
		t.Fatalf("Run did not return; drawn:\n%q", tail(scr.String()))
	}
	os.Stdin, os.Stdout = stdin, stdout
	if !framed {
		t.Fatalf("Run drew no frame; output:\n%q", tail(scr.String()))
	}
	// the title reset is Run's last write
	if !scr.waitFor("\x1b]0;\x07") {
		t.Errorf("Run did not reset the window title; output tail:\n%q", tail(scr.String()))
	}
	return res.code, scr.String(), res.err
}

// tail is the last 2 KiB of s, for failure messages.
func tail(s string) string {
	if len(s) > 2048 {
		return s[len(s)-2048:]
	}
	return s
}

// q on the player quits cleanly: exit 0, the frame drawn first, connecting.
func TestRunQuitsOnQ(t *testing.T) {
	code, screen, err := runInPty(t, func(ptmx *os.File) {
		if _, werr := io.WriteString(ptmx, "q"); werr != nil {
			t.Error(werr)
		}
	})
	if code != 0 || err != nil {
		t.Errorf("q: Run = %d, %v; want 0, nil", code, err)
	}
	if text := clean(screen); !strings.Contains(text, "LP10") || !strings.Contains(text, "connecting") {
		t.Errorf("the frame lacks the name or the connecting status:\n%q", tail(text))
	}
}

// Ctrl-C arrives as a key in raw mode: the model marks it, and Run exits 130
// (128 + SIGINT) as a shell expects.
func TestRunCtrlCExits130(t *testing.T) {
	code, _, err := runInPty(t, func(ptmx *os.File) {
		if _, werr := io.WriteString(ptmx, "\x03"); werr != nil {
			t.Error(werr)
		}
	})
	if code != 130 || err != nil {
		t.Errorf("ctrl-c: Run = %d, %v; want 130, nil", code, err)
	}
}

// SIGTERM and SIGHUP end the program through Run's own handler with exit 143,
// SIGINT with 130. The signal goes to this test process only after the first
// frame, when the handler is installed (signal.Notify precedes the program
// loop), and Run restores the default disposition before it returns.
func TestRunSignalExitCodes(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGTERM, 143}, {syscall.SIGHUP, 143}, {syscall.SIGINT, 130}} {
		code, _, err := runInPty(t, func(*os.File) {
			if kerr := syscall.Kill(syscall.Getpid(), c.sig); kerr != nil {
				t.Error(kerr)
			}
		})
		if code != c.code || err != nil {
			t.Errorf("%v: Run = %d, %v; want %d, nil", c.sig, code, err, c.code)
		}
	}
}

// cellPixelSize reads the cell size in device pixels from the terminal
// (TIOCGWINSZ on stdout), and reports (0, 0) when the terminal gives no pixel
// dimensions; a resize re-reads it, so the art box stays square.
func TestCellPixelSize(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty on this host: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	stdout := os.Stdout
	os.Stdout = tty
	defer func() { os.Stdout = stdout }()

	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 100, X: 1000, Y: 600}); err != nil {
		t.Fatal(err)
	}
	if w, h := cellPixelSize(); w != 10 || h != 20 {
		t.Errorf("1000×600 px over 100×30 cells = %d×%d, want 10×20", w, h)
	}
	m, _, _ := makeModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.cellW != 10 || m.cellH != 20 {
		t.Errorf("a resize read the cell as %d×%d, want 10×20", m.cellW, m.cellH)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	if w, h := cellPixelSize(); w != 0 || h != 0 {
		t.Errorf("a terminal with no pixel size = %d×%d, want 0×0", w, h)
	}
}
