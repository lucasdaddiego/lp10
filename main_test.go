package main

import (
	"net"
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
	found := func(hint string, _ time.Duration) (discovery.Device, bool) {
		gotHint = hint
		return living, true
	}
	cfg := resolveDevice(base, found, nil)
	if gotHint != "" {
		t.Errorf("default name should discover unhinted, got hint %q", gotHint)
	}
	if cfg.Host != "192.168.1.40" || !cfg.Discovered || cfg.Name != "LP10 · Living" {
		t.Errorf("found device should rewrite host and label: %+v", cfg)
	}

	// A custom name IS the hint, and stays the label even after a find.
	custom := base
	custom.Name = "Bedroom"
	cfg = resolveDevice(custom, found, nil)
	if gotHint != "Bedroom" {
		t.Errorf("custom name should hint discovery, got %q", gotHint)
	}
	if cfg.Name != "Bedroom" || cfg.Host != "192.168.1.40" {
		t.Errorf("custom name must survive a find: %+v", cfg)
	}

	// Nothing found: the configured host stays the fallback, untouched.
	notFound := func(string, time.Duration) (discovery.Device, bool) { return discovery.Device{}, false }
	if cfg = resolveDevice(base, notFound, nil); cfg.Host != "lp10.local" || cfg.Discovered || cfg.Name != config.DefaultName {
		t.Errorf("a miss must leave cfg untouched: %+v", cfg)
	}

	// Both skip gates: a pinned LP10_HOST and discover=false must not probe at all.
	probed := func(string, time.Duration) (discovery.Device, bool) {
		t.Error("discovery ran despite a skip gate")
		return discovery.Device{}, false
	}
	t.Setenv(config.HostEnv, "10.0.0.9")
	resolveDevice(base, probed, nil)
	t.Setenv(config.HostEnv, "")
	off := base
	off.Discover = false
	resolveDevice(off, probed, nil)

	// A hostile advertised name carrying escape bytes is control-stripped before
	// it composes the header label (mDNS labels are attacker-controllable). The
	// ESC/BEL that would start an OSC-8 sequence are removed; the inert printable
	// remainder is harmless without them.
	evil := func(string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{Name: "Den\x1b]8;;http://evil\x07x", Model: "LP10", IP: net.IPv4(192, 168, 1, 41)}, true
	}
	if cfg = resolveDevice(base, evil, nil); cfg.Name != "LP10 · Den]8;;http://evilx" {
		t.Errorf("device name must strip the ESC/BEL, got %q", cfg.Name)
	}

	// The host gets the same strip: with no A record, Addr() falls back to the
	// raw SRV target — as attacker-controllable as the label — and it reaches
	// the diag overlay's host readout.
	evilHost := func(string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{Name: "Den", Model: "LP10", Host: "own\x1b]0;x\x07ed.local."}, true
	}
	if cfg = resolveDevice(base, evilHost, nil); cfg.Host != "own]0;xed.local" {
		t.Errorf("discovered host must strip the ESC/BEL, got %q", cfg.Host)
	}
}

// The LSSDP fallback runs only when mDNS came back empty, and its answer is
// used exactly like an mDNS one; a hit on mDNS never consults it.
func TestResolveDeviceLSSDPFallback(t *testing.T) {
	t.Setenv(config.HostEnv, "")
	base := config.Config{Host: "lp10.local", Name: config.DefaultName, Discover: true}
	notFound := func(string, time.Duration) (discovery.Device, bool) { return discovery.Device{}, false }
	calls := 0
	viaLSSDP := func(string, time.Duration) (discovery.Device, bool) {
		calls++
		return discovery.Device{Name: "Living", IP: net.IPv4(192, 168, 0, 13)}, true
	}
	cfg := resolveDevice(base, notFound, viaLSSDP)
	if cfg.Host != "192.168.0.13" || !cfg.Discovered || cfg.Name != "LP10 · Living" || calls != 1 {
		t.Errorf("fallback result = %+v (calls %d)", cfg, calls)
	}
	viaMDNS := func(string, time.Duration) (discovery.Device, bool) {
		return discovery.Device{Name: "Den", IP: net.IPv4(10, 0, 0, 2)}, true
	}
	calls = 0
	if cfg := resolveDevice(base, viaMDNS, viaLSSDP); cfg.Host != "10.0.0.2" || calls != 0 {
		t.Errorf("mDNS hit must not consult LSSDP: %+v (calls %d)", cfg, calls)
	}
	if cfg := resolveDevice(base, notFound, notFound); cfg.Host != "lp10.local" || cfg.Discovered {
		t.Errorf("both empty: configured host stays: %+v", cfg)
	}
}
