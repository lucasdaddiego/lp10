// Command lp10 is a terminal player for the Arylic LP10 (LibreWireless LUCI
// over SSH). Run `lp10` (no arguments) for the live TUI; there are no other
// commands.
//
// Transport: ONE direct ssh connection to root@LP10 (password from the OS
// secret store — the macOS Keychain item service=lp10 account=root, or
// secret-tool on Linux — delivered via SSH_ASKPASS self-exec).
// The remote shell loop streams state snapshots and evals nothing: its stdin
// accepts whitelisted `<mid> <data>` lines only. When this process dies — however
// it dies — ssh exits, the session closes, and the loop EOF-exits within ~1 s.
// Host keys are deliberately not verified (LAN device, ramfs host keys).
//
// Config: ~/.config/lp10/config.toml (optional) — host, user, name, vol_step,
// ping_host, discover, art, art_mode. Unless discover=false or LP10_HOST is
// set, a startup mDNS query finds the LP10 on the LAN (am=LP10) — the device's
// own LSSDP responder (UDP:1800) gets a window when mDNS is quiet — and uses
// its current address, with host as the fallback. State: ~/.local/state/lp10/.
// First-run: security add-generic-password -U -a root -s lp10 -w
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/transport"
	"github.com/lucasdaddiego/lp10/internal/tui"
)

// discoverTimeout bounds the startup mDNS probe. A present device answers in well
// under this — the first reply early-exits (~tens of ms), and discovery.FindLP10
// retransmits within the window to ride out UDP loss — so it only bites as a brief
// delay before the cached first paint when nothing is on the LAN. Kept short for
// that reason; the configured host is the fallback.
const discoverTimeout = 1 * time.Second

const usage = "lp10: takes no arguments — run `lp10` for the live TUI; `lp10 --version` prints the build"

// versionString is what --version prints: the module version (a tag when
// installed as a module, "(devel)" from a checkout), the commit and its time
// from the VCS stamp, "(modified)" for a dirty tree, and the Go toolchain —
// the build identity a firmware or OTA bug report needs.
func versionString(bi *debug.BuildInfo) string {
	if bi == nil {
		return "lp10 (unknown build)"
	}
	v := bi.Main.Version
	if v == "" {
		v = "(devel)"
	}
	rev, at, dirty := "", "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	out := "lp10 " + v
	if rev != "" {
		out += " " + rev[:min(12, len(rev))]
	}
	if at != "" {
		out += " " + at
	}
	if dirty {
		out += " (modified)"
	}
	if bi.GoVersion != "" {
		out += " " + bi.GoVersion
	}
	return out
}

// finder is the injectable signature of discovery.FindLP10 / FindLP10LSSDP.
type finder func(context.Context, string, time.Duration) (discovery.Device, bool)

// signalContext is the discovery window's context: Ctrl-C or SIGTERM during
// the startup search ends it at once — and the run, with the shell's code for
// that signal — instead of after the window's timeout. finish stops listening
// and reports that code (0 when nothing arrived).
func signalContext() (ctx context.Context, finish func() int) {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	var code atomic.Int32
	go func() {
		if s, ok := <-sigs; ok {
			c := int32(130)
			if s == syscall.SIGTERM {
				c = 143
			}
			code.Store(c)
			cancel()
		}
	}()
	return ctx, func() int {
		signal.Stop(sigs)
		close(sigs)
		cancel()
		return int(code.Load())
	}
}

// resolveDevice applies best-effort discovery to cfg, so a changed DHCP lease
// never needs a config edit: find the LP10 on the LAN and use its current
// address. find is the mDNS search (discovery.FindLP10); when it comes back
// empty, fallback (discovery.FindLP10LSSDP, the device's own UDP:1800
// responder — which answers even while its AirPlay daemon or sshd don't) gets
// one more window. Pinning the host (LP10_HOST) or `discover = false` skips
// both; the configured host is the fallback when nothing answers, so startup
// never blocks on a missing device.
func resolveDevice(ctx context.Context, cfg config.Config, find, fallback finder) config.Config {
	if !cfg.Discover || os.Getenv(config.HostEnv) != "" {
		return cfg
	}
	// Hint discovery with the user's custom name only: the default label is
	// not a room name, and a hint no device answers to would hold FindLP10
	// to its full timeout (it only early-exits on a hint match) — losing
	// the fast startup path for every un-configured setup.
	hint := ""
	if cfg.Name != config.DefaultName {
		hint = cfg.Name
	}
	dev, ok := find(ctx, hint, discoverTimeout)
	if !ok && fallback != nil && ctx.Err() == nil {
		dev, ok = fallback(ctx, hint, discoverTimeout)
	}
	if ok {
		// Addr() can fall back to the raw SRV target when no A record arrived,
		// which is as attacker-controllable as the label below — strip it the
		// same way (it reaches the diag overlay's host readout).
		cfg.Host, cfg.Discovered = protocol.Printable(dev.Addr()), true
		// Label the UI with the device's own advertised name ("LP10 · Living")
		// when the user hasn't set a custom name — so no room name is hardcoded.
		// The mDNS label is attacker-controllable and reaches the header
		// unfiltered (unlike the @@-section device strings), so control-strip it
		// here — a raw ESC in it could otherwise inject an escape sequence.
		if name := protocol.Printable(dev.Name); cfg.Name == config.DefaultName && name != "" {
			cfg.Name = config.DefaultName + " · " + name
		}
	}
	return cfg
}

func main() {
	// Askpass hot path first: ssh re-execs this binary as SSH_ASKPASS on every
	// connection attempt, so it must stay cheap and run before anything else.
	if os.Getenv(transport.AskpassEnv) == "1" {
		transport.AskpassMain() // exits the process
		return
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-version", "-V":
			bi, _ := debug.ReadBuildInfo()
			fmt.Println(versionString(bi))
			return
		case "--help", "-help", "-h":
			fmt.Println(usage)
			return
		}
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	ctx, finish := signalContext()
	cfg := resolveDevice(ctx, config.Load(), discovery.FindLP10, discovery.FindLP10LSSDP)
	if code := finish(); code != 0 {
		os.Exit(code) // interrupted during discovery: no TUI was ever up
	}

	// tui.Run handles SIGTERM/SIGHUP and Ctrl-C cooperatively and returns the
	// exit code (0 clean, 130 interrupt, 143 signal) after running teardown and
	// restoring the terminal.
	code, err := tui.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lp10: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}
