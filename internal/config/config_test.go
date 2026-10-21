package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig points XDG_CONFIG_HOME at a temp dir holding the given
// config.toml content (or none, when content == ""), and clears LP10_HOST.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("LP10_HOST", "")
	if content != "" {
		writeConfigUnder(t, base, content)
	}
}

// writeConfigUnder writes base/lp10/config.toml.
func writeConfigUnder(t *testing.T, base, content string) {
	t.Helper()
	dir := filepath.Join(base, "lp10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStateDirHonorsEnv(t *testing.T) {
	d := filepath.Join(t.TempDir(), "s")
	t.Setenv("LP10_STATE_DIR", d)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg")) // LP10_STATE_DIR wins
	got := StateDir()
	if got != d {
		t.Fatalf("StateDir = %q, want %q", got, d)
	}
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		t.Errorf("state dir not created")
	}
}

// XDG_STATE_HOME places the state dir like XDG_CONFIG_HOME places the
// config; unset, the dir is ~/.local/state/lp10.
func TestStateDirHonorsXDGStateHome(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", "")
	x := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_STATE_HOME", x)
	want := filepath.Join(x, "lp10")
	if got := StateDir(); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Errorf("state dir not created at %q", want)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	if got, want := StateDir(), filepath.Join(home, ".local", "state", "lp10"); got != want {
		t.Errorf("unset XDG_STATE_HOME: StateDir = %q, want the home default %q", got, want)
	}
}

func TestConfigDefaultsWhenNoFile(t *testing.T) {
	writeConfig(t, "")
	cfg := Load()
	want := Config{Host: "lp10.local", StateKey: "lp10.local", Name: DefaultName, VolStep: 2, Discover: true, Theme: "auto"}
	if cfg != want {
		t.Errorf("defaults = %+v, want %+v", cfg, want)
	}
}

func TestConfigFileAndEnvOverride(t *testing.T) {
	writeConfig(t, "host = \"lp10.local\"\nvol_step = 5\ndiscover = false\ntheme = \"dark\"\n")
	cfg := Load()
	if cfg.Host != "lp10.local" || cfg.VolStep != 5 || cfg.Discover || cfg.Theme != "dark" || cfg.Warn != "" {
		t.Errorf("file override wrong: %+v", cfg)
	}
	t.Setenv("LP10_HOST", "10.0.0.9")
	if Load().Host != "10.0.0.9" {
		t.Error("LP10_HOST should beat the file")
	}
}

// With XDG_CONFIG_HOME unset — most setups — the file is ~/.config/lp10/config.toml.
func TestConfigReadFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LP10_HOST", "")
	writeConfigUnder(t, filepath.Join(home, ".config"), "name = \"Den\"\n")
	if cfg := Load(); cfg.Name != "Den" || cfg.Warn != "" {
		t.Errorf("~/.config/lp10/config.toml not read: %+v", cfg)
	}
}

func TestMalformedConfigWarnsAndKeepsDefaults(t *testing.T) {
	writeConfig(t, "host = [broken\n")
	cfg := Load()
	if cfg.Host != "lp10.local" {
		t.Errorf("host = %q, want default", cfg.Host)
	}
	if !strings.Contains(cfg.Warn, "config.toml") {
		t.Errorf("warn = %q, want a config.toml warning", cfg.Warn)
	}
}

func TestVolStepClamped(t *testing.T) {
	writeConfig(t, "vol_step = 0\n")
	if Load().VolStep != 1 {
		t.Error("vol_step 0 should clamp to 1")
	}
	writeConfig(t, "vol_step = 200\n")
	if Load().VolStep != 100 {
		t.Error("vol_step 200 should clamp to 100")
	}
}

func TestStateDirFailureDegradesToNoPersistence(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(blocker, "sub"))
	if StateDir() != "" {
		t.Error("StateDir should be \"\" when it cannot be created")
	}
	cfg := Config{Host: defHost, Name: DefaultName, VolStep: defVolStep}
	if SnapshotPath(cfg) != "" || SweepPath(cfg) != "" {
		t.Error("paths should be empty with no state dir")
	}
	if LoadSnapshot("") != nil {
		t.Error("LoadSnapshot(\"\") should be nil")
	}
	SaveSnapshot("", CachedSnapshot{Vol: 3}) // a no-op, must not panic
}

func TestSnapshotRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	SaveSnapshot(p, CachedSnapshot{Vol: 44, EQ: map[string]int{"BAS": -3, "MXV": 90}})
	got := LoadSnapshot(p)
	if got == nil {
		t.Fatal("snapshot did not round-trip")
	}
	if got.Vol != 44 || len(got.EQ) != 2 || got.EQ["BAS"] != -3 || got.EQ["MXV"] != 90 {
		t.Errorf("round-trip = %+v, want vol 44 and the two EQ values", got)
	}
	if LoadSnapshot(filepath.Join(t.TempDir(), "missing.json")) != nil {
		t.Error("missing snapshot should be nil")
	}
}

// A snapshot written before firmware AR241CP_8747 also holds the track, its
// position and the play state. Upgrading must keep its volume and EQ (the
// first paint) and ignore the rest: a cached title could name something the
// box stopped playing long ago.
func TestLoadSnapshotOldFormatKeepsVolAndEQ(t *testing.T) {
	raw := `{
		"track": {
			"TrackName": "Legacy", "Artist": "Artist", "Album": "Album",
			"PlaybackSource": "Spotify", "PlayUrl": "spotify:track:x",
			"CoverArtUrl": "https://example.test/cover.jpg", "TotalTime": 180000,
			"Current Source": 4, "SampleRate": 44100, "Seek": true,
			"future-field": "ignored"
		},
		"pos": 1234,
		"playing": 0,
		"vol": 44,
		"eq": {"BAS": 3}
	}`
	p := filepath.Join(t.TempDir(), "snap.json")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadSnapshot(p)
	if got == nil || got.Vol != 44 || len(got.EQ) != 1 || got.EQ["BAS"] != 3 {
		t.Errorf("old-format snapshot = %+v, want vol 44 and eq BAS 3", got)
	}
}

// A corrupt snapshot — a non-object root, a wrong-typed field — rejects the
// cache rather than half-load it or crash-loop.
func TestLoadSnapshotRejectsCorruptFile(t *testing.T) {
	for _, raw := range []string{`["not","a","dict"]`, `{"vol":"44"}`} {
		p := filepath.Join(t.TempDir(), "snap.json")
		if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LoadSnapshot(p); got != nil {
			t.Errorf("LoadSnapshot(%s) = %+v, want nil", raw, got)
		}
	}
}

// Two hosts never share a state file. The slug wrote "fe80::1" and
// "fe80__1" (or "living room" and "living_room") to one name, so one box's
// snapshot and sweep baseline stood in for the other's. A host safe as a file
// name keeps its name; one whose name changed starts from a copy of the file
// under its old name, and that file stays for a host that still maps to it.
func TestStatePathsAreOnePerHost(t *testing.T) {
	d := t.TempDir()
	t.Setenv("LP10_STATE_DIR", d)
	seen := map[string]string{}
	for _, h := range []string{"fe80::1", "fe80__1", "fe80_:1", "living room", "living_room", "living%20room", "a/b", "a_b"} {
		for _, p := range []string{SnapshotPath(Config{Host: h}), SweepPath(Config{Host: h})} {
			if other, ok := seen[p]; ok {
				t.Errorf("%q and %q share %s", h, other, p)
			}
			seen[p] = h
		}
	}
	if got, want := SnapshotPath(Config{Host: "192.168.1.40"}), filepath.Join(d, "snapshot-192.168.1.40.json"); got != want {
		t.Errorf("a file-safe host moved: %q, want %q", got, want)
	}

	old := filepath.Join(d, "snapshot-fe80__1_en0.json") // slug("fe80::1%en0")
	if err := os.WriteFile(old, []byte(`{"vol":33}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Host: "fe80::1%en0"}
	if p := SnapshotPath(cfg); p == old {
		t.Fatalf("fe80::1%%en0 still keys on %s", p)
	}
	if got := LoadSnapshot(SnapshotPath(cfg)); got == nil || got.Vol != 33 {
		t.Fatalf("the old file's snapshot was lost: %+v", got)
	}
	if got := LoadSnapshot(SnapshotPath(Config{Host: "fe80__1_en0"})); got == nil || got.Vol != 33 {
		t.Errorf("the host still named by the old file lost it: %+v", got)
	}
}

// State files key on the host as configured, so discovery rewriting Host to
// the address the box holds today (a new DHCP lease) keeps the first-paint
// snapshot and the sweep baseline; a Config built without Load keys on Host.
func TestStatePathsKeyOnConfiguredHost(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	cfg := Config{Host: "192.168.0.27", StateKey: "lp10.local"}
	if !strings.HasSuffix(SnapshotPath(cfg), "snapshot-lp10.local.json") || !strings.HasSuffix(SweepPath(cfg), "sweep-lp10.local.json") {
		t.Errorf("paths = %q %q, want keyed on lp10.local", SnapshotPath(cfg), SweepPath(cfg))
	}
	if !strings.HasSuffix(SnapshotPath(Config{Host: "box"}), "snapshot-box.json") {
		t.Error("without a StateKey the host keys the files")
	}
	t.Setenv(HostEnv, "10.0.0.5")
	if got := Load(); got.StateKey != "10.0.0.5" || got.Host != "10.0.0.5" {
		t.Errorf("Load: StateKey %q Host %q, want the LP10_HOST value for both", got.StateKey, got.Host)
	}
}

// A typo in config.toml — an unknown key, a value of the wrong type, a theme
// that is not one of the three — used to keep the default silently; now it is
// the startup warning, while every valid key still applies.
func TestConfigTyposSurfaceInTheWarning(t *testing.T) {
	writeConfig(t, `
hots = "typo.local"
name = "Living"
vol_step = "2"
theme = "drak"
discover = "yes"
`)
	cfg := Load()
	if cfg.Name != "Living" || cfg.Host != defHost || cfg.VolStep != defVolStep || cfg.Theme != defTheme || !cfg.Discover {
		t.Errorf("valid keys must apply and typos keep defaults: %+v", cfg)
	}
	for _, want := range []string{`unknown key "hots"`, "vol_step ignored (want number)", `theme "drak" ignored (auto|light|dark)`, "discover ignored (want bool)"} {
		if !strings.Contains(cfg.Warn, want) {
			t.Errorf("warning %q lacks %q", cfg.Warn, want)
		}
	}
	writeConfig(t, "name = \"Den\"\nvol_step = 3.0\n")
	if cfg := Load(); cfg.Warn != "" || cfg.Name != "Den" || cfg.VolStep != 3 {
		t.Errorf("a clean file must not warn: %+v", cfg)
	}
}

// The ssh user, the ping target and the album-art keys were retired with ssh
// (firmware AR241CP_8747). A config that still sets one is told why the key
// no longer counts, not that it is unknown, and the key changes nothing. The
// keys beside it still apply.
func TestRetiredKeysComplainAndChangeNothing(t *testing.T) {
	writeConfig(t, "user = \"root\"\nname = \"Den\"\n")
	cfg := Load()
	want := "config.toml: user ignored (retired: lp10 has no ssh and no album art since firmware AR241CP_8747)"
	if cfg.Warn != want {
		t.Errorf("warn = %q, want %q", cfg.Warn, want)
	}
	cfg.Warn = ""
	if want := (Config{Host: defHost, StateKey: defHost, Name: "Den", VolStep: defVolStep, Discover: true, Theme: defTheme}); cfg != want {
		t.Errorf("a retired key changed the config: %+v, want %+v", cfg, want)
	}
}

// An empty host is refused with a complaint, keeping the default: it would
// dial ":2018", which Go dials on the local machine, when discovery is off
// or finds nothing (and the sweep would inventory this machine).
func TestEmptyHostIsRefused(t *testing.T) {
	t.Setenv(HostEnv, "")
	writeConfig(t, "host = \"\"\ndiscover = false\n")
	cfg := Load()
	if cfg.Host == "" {
		t.Errorf("host = \"\" accepted: the tunnel would dial %q", net.JoinHostPort(cfg.Host, "2018"))
	}
	if !strings.Contains(cfg.Warn, "host ignored (empty)") {
		t.Errorf("warning %q does not name the empty host", cfg.Warn)
	}
}
