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
	cfg := resolveDevice(base, found)
	if gotHint != "" {
		t.Errorf("default name should discover unhinted, got hint %q", gotHint)
	}
	if cfg.Host != "192.168.1.40" || !cfg.Discovered || cfg.Name != "LP10 · Living" {
		t.Errorf("found device should rewrite host and label: %+v", cfg)
	}

	// A custom name IS the hint, and stays the label even after a find.
	custom := base
	custom.Name = "Bedroom"
	cfg = resolveDevice(custom, found)
	if gotHint != "Bedroom" {
		t.Errorf("custom name should hint discovery, got %q", gotHint)
	}
	if cfg.Name != "Bedroom" || cfg.Host != "192.168.1.40" {
		t.Errorf("custom name must survive a find: %+v", cfg)
	}

	// Nothing found: the configured host stays the fallback, untouched.
	notFound := func(string, time.Duration) (discovery.Device, bool) { return discovery.Device{}, false }
	if cfg = resolveDevice(base, notFound); cfg.Host != "lp10.local" || cfg.Discovered || cfg.Name != config.DefaultName {
		t.Errorf("a miss must leave cfg untouched: %+v", cfg)
	}

	// Both skip gates: a pinned LP10_HOST and discover=false must not probe at all.
	probed := func(string, time.Duration) (discovery.Device, bool) {
		t.Error("discovery ran despite a skip gate")
		return discovery.Device{}, false
	}
	t.Setenv(config.HostEnv, "10.0.0.9")
	resolveDevice(base, probed)
	t.Setenv(config.HostEnv, "")
	off := base
	off.Discover = false
	resolveDevice(off, probed)
}
