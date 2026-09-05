package main

import (
	"context"
	"net"
	"os"
	"runtime/debug"
	"syscall"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
)

// resolveDevice carries main's only logic — the discovery gate, the hint rule,
// and the host/label rewrite — exercised here with a faked finder so no real
// mDNS query leaves the test.
func TestResolveDevice(t *testing.T) {
	t.Setenv(config.HostEnv, "")
	living := discovery.Device{Name: "Living", Model: "LP10", IP: net.IPv4(192, 168, 1, 40)}
	base := config.Config{Host: "lp10.local", Name: config.DefaultName, Discover: true}

	// Default name: discovery runs UNHINTED (the generic label is not a room
	// name and must not defeat the early exit), and a found device rewrites the
	// host and augments the UI label with its advertised name.
	var gotHint string
	found := func(_ context.Context, hint string, _ time.Duration) (discovery.Device, bool) {
		gotHint = hint
		return living, true
	}
	cfg := resolveDevice(context.Background(), base, found, nil)
	if gotHint != "" {
		t.Errorf("default name should discover unhinted, got hint %q", gotHint)
	}
	if cfg.Host != "192.168.1.40" || !cfg.Discovered || cfg.Name != "LP10 · Living" {
		t.Errorf("found device should rewrite host and label: %+v", cfg)
	}

	// A custom name IS the hint, and stays the label even after a find.
	custom := base
	custom.Name = "Bedroom"
	cfg = resolveDevice(context.Background(), custom, found, nil)
	if gotHint != "Bedroom" {
		t.Errorf("custom name should hint discovery, got %q", gotHint)
	}
	if cfg.Name != "Bedroom" || cfg.Host != "192.168.1.40" {
		t.Errorf("custom name must survive a find: %+v", cfg)
	}

	// Nothing found: the configured host stays the fallback, untouched.
	notFound := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{}, false
	}
	if cfg = resolveDevice(context.Background(), base, notFound, nil); cfg.Host != "lp10.local" || cfg.Discovered || cfg.Name != config.DefaultName {
		t.Errorf("a miss must leave cfg untouched: %+v", cfg)
	}

	// Both skip gates: a pinned LP10_HOST and discover=false must not probe at all.
	probed := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		t.Error("discovery ran despite a skip gate")
		return discovery.Device{}, false
	}
	t.Setenv(config.HostEnv, "10.0.0.9")
	resolveDevice(context.Background(), base, probed, nil)
	t.Setenv(config.HostEnv, "")
	off := base
	off.Discover = false
	resolveDevice(context.Background(), off, probed, nil)

	// A hostile advertised name carrying escape bytes is control-stripped before
	// it composes the header label (mDNS labels are attacker-controllable). The
	// ESC/BEL that would start an OSC-8 sequence are removed; the inert printable
	// remainder is harmless without them.
	evil := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{Name: "Den\x1b]8;;http://evil\x07x", Model: "LP10", IP: net.IPv4(192, 168, 1, 41)}, true
	}
	if cfg = resolveDevice(context.Background(), base, evil, nil); cfg.Name != "LP10 · Den]8;;http://evilx" {
		t.Errorf("device name must strip the ESC/BEL, got %q", cfg.Name)
	}

	// The host gets the same strip: with no A record, Addr() falls back to the
	// raw SRV target — as attacker-controllable as the label — and it reaches
	// the diag overlay's host readout.
	evilHost := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{Name: "Den", Model: "LP10", Host: "own\x1b]0;x\x07ed.local."}, true
	}
	if cfg = resolveDevice(context.Background(), base, evilHost, nil); cfg.Host != "own]0;xed.local" {
		t.Errorf("discovered host must strip the ESC/BEL, got %q", cfg.Host)
	}
}

// The LSSDP fallback runs only when mDNS came back empty, and its answer is
// used exactly like an mDNS one; a hit on mDNS never consults it.
func TestResolveDeviceLSSDPFallback(t *testing.T) {
	t.Setenv(config.HostEnv, "")
	base := config.Config{Host: "lp10.local", Name: config.DefaultName, Discover: true}
	notFound := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{}, false
	}
	calls := 0
	viaLSSDP := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		calls++
		return discovery.Device{Name: "Living", IP: net.IPv4(192, 168, 0, 13)}, true
	}
	cfg := resolveDevice(context.Background(), base, notFound, viaLSSDP)
	if cfg.Host != "192.168.0.13" || !cfg.Discovered || cfg.Name != "LP10 · Living" || calls != 1 {
		t.Errorf("fallback result = %+v (calls %d)", cfg, calls)
	}
	viaMDNS := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{Name: "Den", IP: net.IPv4(10, 0, 0, 2)}, true
	}
	calls = 0
	if cfg := resolveDevice(context.Background(), base, viaMDNS, viaLSSDP); cfg.Host != "10.0.0.2" || calls != 0 {
		t.Errorf("mDNS hit must not consult LSSDP: %+v (calls %d)", cfg, calls)
	}
	if cfg := resolveDevice(context.Background(), base, notFound, notFound); cfg.Host != "lp10.local" || cfg.Discovered {
		t.Errorf("both empty: configured host stays: %+v", cfg)
	}
}

// --version is the build identity a bug report needs: module version, commit,
// commit time, a dirty marker and the toolchain — from the VCS stamp the Go
// linker embeds, with sane output when a piece is missing.
func TestVersionString(t *testing.T) {
	if got := versionString(nil); got != "lp10 (unknown build)" {
		t.Errorf("nil build info = %q", got)
	}
	bi := &debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "983ee68f9218acb8a44c7fc2932bb433ac95b0a1"},
		{Key: "vcs.time", Value: "2026-09-05T19:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}}
	if got, want := versionString(bi), "lp10 (devel) 983ee68f9218 2026-09-05T19:00:00Z (modified) go1.27.1"; got != want {
		t.Errorf("versionString = %q, want %q", got, want)
	}
	if got := versionString(&debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}}); got != "lp10 v1.4.0" {
		t.Errorf("tagged, no vcs = %q", got)
	}
}

// A signal during the discovery window ends it at once and carries the shell's
// code for that signal out of main; nothing arriving reports 0.
func TestSignalContextEndsDiscoveryWindow(t *testing.T) {
	ctx, finish := signalContext()
	if code := finish(); code != 0 || ctx.Err() == nil {
		t.Fatalf("quiet window: code %d, ctx err %v", code, ctx.Err())
	}
	ctx, finish = signalContext()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not end the window")
	}
	if code := finish(); code != 143 {
		t.Errorf("code = %d, want 143", code)
	}
	// and resolveDevice leaves the config alone once the window is gone
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	quiet := func(context.Context, string, time.Duration) (discovery.Device, bool) {
		calls++
		return discovery.Device{}, false
	}
	base := config.Config{Host: "lp10.local", Name: config.DefaultName, Discover: true}
	if cfg := resolveDevice(cancelled, base, quiet, quiet); cfg.Host != "lp10.local" || calls != 1 {
		t.Errorf("cancelled window: cfg %+v calls %d (the fallback must not get a window of its own)", cfg, calls)
	}
}
