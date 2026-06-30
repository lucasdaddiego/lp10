// Package transport handles SSH: Keychain/askpass auth, the ssh argv, the
// on-device streaming loop, and stderr classification. Port of
// lp10lib/transport.py.
//
// The surface is split by concern: keychain.go (secret-store lookup + askpass
// child), secret_{darwin,linux}.go (per-OS store integration), remote_loop.{go,sh}
// (the on-device streaming script), and this file (the askpass/error vocabulary,
// the ssh argv/env, and stderr classification).
package transport

import (
	"cmp"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
)

const (
	AskpassEnv   = "LP10_ASKPASS"
	MarkerNoItem = "lp10-askpass: no-item"
	MarkerLocked = "lp10-askpass: keychain-locked"
	MarkerBroken = "lp10-askpass: security-failed"
	// The askpass markers are an internal wire protocol between the re-exec'd
	// askpass child (keychain.go) and the parent (matched by ClassifyStderr); their
	// literal text is opaque, so it stays put across OSes. The per-OS store
	// integration (lookup argv, the not-found rule, StoreHint, and the backend
	// nouns) lives in secret_darwin.go / secret_linux.go.
)

// TransportError carries a fatal flag and a retry cadence, mirroring the Python
// TransportError raised below main().
type TransportError struct {
	Msg     string
	Fatal   bool
	Cadence time.Duration
}

func (e *TransportError) Error() string { return e.Msg }

// SSHArgv builds the ssh command (binary overridable via LP10_SSH for tests).
func SSHArgv(cfg config.Config) []string {
	binary := cmp.Or(os.Getenv("LP10_SSH"), "ssh")
	// Host-key verification is intentionally disabled: the LP10 regenerates its ssh
	// host key from a ramfs on every boot, so TOFU/pinning is pointless and would
	// only nag. UserKnownHostsFile=/dev/null + StrictHostKeyChecking=no is therefore
	// by design — the one deliberate security tradeoff (it forgoes MITM protection
	// on the trusted-LAN path; see the README "Security & threat model"). A static
	// analyzer flagging these two options is a false positive in this context.
	return []string{binary, "-F", "/dev/null", "-T",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=3",
		"-o", "ServerAliveInterval=20",
		"-o", "ServerAliveCountMax=3",
		"-o", "NumberOfPasswordPrompts=1",
		"-o", "PreferredAuthentications=password",
		"-o", "IdentityAgent=none",
		fmt.Sprintf("%s@%s", cfg.User, cfg.Host)}
}

// SpawnEnv returns the child environment: ssh re-execs this binary as
// SSH_ASKPASS on every connection attempt, with LP10_ASKPASS=1 set so it takes
// the askpass hot path.
func SpawnEnv() []string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	overrides := map[string]string{
		"SSH_ASKPASS":         exe,
		"SSH_ASKPASS_REQUIRE": "force",
		AskpassEnv:            "1",
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		k := kv
		if before, _, ok := strings.Cut(kv, "="); ok {
			k = before
		}
		if _, ok := overrides[k]; !ok {
			env = append(env, kv)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// ClassifyStderr maps residual ssh/askpass stderr to a fatal TransportError, or
// nil for transient (network) failures.
func ClassifyStderr(text string) *TransportError {
	if text == "" {
		return nil
	}
	switch {
	case strings.Contains(text, MarkerBroken):
		return &TransportError{fmt.Sprintf("askpass cannot run %s — check PATH/sandboxing (lp10 retries every minute)", secretToolName), true, 60 * time.Second}
	case strings.Contains(text, MarkerLocked):
		return &TransportError{fmt.Sprintf("%s is locked — unlock it (lp10 retries every minute)", secretStoreName), true, 60 * time.Second}
	case strings.Contains(text, MarkerNoItem):
		return &TransportError{"no saved password — run: " + StoreHint, true, 10 * time.Second}
	case strings.Contains(text, "Permission denied"):
		return &TransportError{"SSH password rejected — update the saved password: " + StoreHint, true, 10 * time.Second}
	}
	return nil
}
