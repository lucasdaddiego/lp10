// Command lp10 is a terminal player for the Arylic LP10 (LibreWireless LUCI
// over SSH). Run `lp10` (no arguments) for the live TUI; there are no other
// commands.
//
// Transport: ONE direct ssh connection to root@LP10 (password from the macOS
// Keychain item service=lp10 account=root, delivered via SSH_ASKPASS self-exec).
// The remote shell loop streams state snapshots and evals nothing: its stdin
// accepts whitelisted `<mid> <data>` lines only. When this process dies — however
// it dies — ssh exits, the session closes, and the loop EOF-exits within ~1 s.
// Host keys are deliberately not verified (LAN device, ramfs host keys).
//
// Config: ~/.config/lp10/config.toml (optional) — host, user, name, vol_step,
// ping_host, discover. Unless discover=false or LP10_HOST is set, a startup mDNS
// query finds the LP10 on the LAN (am=LP10) and uses its current address, with
// host as the fallback. State: ~/.local/state/lp10/.
// First-run: security add-generic-password -U -a root -s lp10 -w
package main

import (
	"fmt"
	"os"
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

const usage = "lp10: takes no arguments — run `lp10` for the live TUI"

// resolveDevice applies best-effort mDNS discovery to cfg, so a changed DHCP
// lease never needs a config edit: find the LP10 on the LAN (via find — the
// injectable discovery.FindLP10) and use its current address. Pinning the host
// (LP10_HOST) or `discover = false` skips it; the configured host is the
// fallback when nothing answers, so startup never blocks on a missing device.
func resolveDevice(cfg config.Config, find func(string, time.Duration) (discovery.Device, bool)) config.Config {
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
	if dev, ok := find(hint, discoverTimeout); ok {
		cfg.Host, cfg.Discovered = dev.Addr(), true
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
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	cfg := resolveDevice(config.Load(), discovery.FindLP10)

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
