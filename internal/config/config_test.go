package config

import (
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
		dir := filepath.Join(base, "lp10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStateDirHonorsEnv(t *testing.T) {
	d := filepath.Join(t.TempDir(), "s")
	t.Setenv("LP10_STATE_DIR", d)
	got := StateDir()
	if got != d {
		t.Fatalf("StateDir = %q, want %q", got, d)
	}
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		t.Errorf("state dir not created")
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

func TestConfigRejectsBoolForInt(t *testing.T) {
	writeConfig(t, "vol_step = true\n")
	if Load().VolStep != 2 {
		t.Error("bool for int field should keep the default")
	}
}

func TestMissingConfigIsSilent(t *testing.T) {
	writeConfig(t, "")
	if Load().Warn != "" {
		t.Error("missing config should not warn")
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

func TestNonUTF8ConfigWarnsNotCrashes(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("LP10_HOST", "")
	dir := filepath.Join(base, "lp10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte{0xff, 0xfe, 0x00, 'b', 'r', 'o', 'k', 'e', 'n'}, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load()
	if cfg.Host != "lp10.local" || cfg.Warn == "" {
		t.Errorf("non-utf8 config should warn and keep defaults: %+v", cfg)
	}
}

func TestVolStepClamped(t *testing.T) {
	writeConfig(t, "vol_step = 0\n")
	if Load().VolStep != 1 {
		t.Error("vol_step 0 should clamp to 1")
	}
}

func TestConfigIntFloatCoercion(t *testing.T) {
	writeConfig(t, "vol_step = 2.0\n")
	if Load().VolStep != 2 {
		t.Error("vol_step 2.0 should coerce to 2")
	}
}

func TestConfigHugeFloatVolStepRejected(t *testing.T) {
	writeConfig(t, "vol_step = 1e19\n")
	if got := Load().VolStep; got != 2 {
		t.Errorf("out-of-range float vol_step should keep default 2, got %d", got)
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
// first paint) and ignore the rest, whatever shape the retired fields have:
// a cached title could name something the box stopped playing long ago.
func TestLoadSnapshotOldFormatKeepsVolAndEQ(t *testing.T) {
	cases := map[string]string{
		"full legacy track": `{
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
		}`,
		"junk track":        `{"track":"junk-string","pos":"x","playing":true,"vol":44,"eq":{"BAS":3}}`,
		"non-object track":  `{"track":["not","a","dict"],"vol":44,"eq":{"BAS":3}}`,
		"wrong-typed field": `{"track":{"SampleRate":"44100"},"vol":44,"eq":{"BAS":3}}`,
		"null track":        `{"track":null,"pos":0,"playing":2,"vol":44,"eq":{"BAS":3}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "snap.json")
			if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			got := LoadSnapshot(p)
			if got == nil || got.Vol != 44 || len(got.EQ) != 1 || got.EQ["BAS"] != 3 {
				t.Errorf("old-format snapshot = %+v, want vol 44 and eq BAS 3", got)
			}
		})
	}
}

// The fields the snapshot still carries must have their own type: a
// wrong-typed vol or EQ value rejects the cache rather than half-load it.
func TestLoadSnapshotRejectsWrongTypedKnownField(t *testing.T) {
	for _, raw := range []string{`{"vol":"44"}`, `{"vol":44,"eq":{"BAS":"3"}}`, `{"vol":44,"eq":[3]}`} {
		p := filepath.Join(t.TempDir(), "snap.json")
		if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LoadSnapshot(p); got != nil {
			t.Errorf("LoadSnapshot(%s) = %+v, want nil", raw, got)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"192.168.1.40":      "192.168.1.40",
		"lp10.local":        "lp10.local",
		"host:with:colons":  "host_with_colons",
		"host with spaces":  "host_with_spaces",
		"Host-With-Dashes":  "Host-With-Dashes",
		"host/with/slashes": "host_with_slashes",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveSnapshotWithIOErrorIsSwallowed(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "nonexistent", "snap.json")
	SaveSnapshot(badPath, CachedSnapshot{Vol: 50}) // must not panic
	if _, err := os.Stat(badPath); err == nil {
		t.Error("nothing should be written to a bad path")
	}
}

func TestLoadSnapshotWithNonDictRoot(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	if err := os.WriteFile(p, []byte(`["not","a","dict"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if LoadSnapshot(p) != nil {
		t.Error("non-dict root should reject the snapshot")
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
// (firmware AR241CP_8747). A config that still sets one — with any value
// type — is told why the key no longer counts, not that it is unknown, and
// the key changes nothing. The keys beside it still apply.
func TestRetiredKeysComplainAndChangeNothing(t *testing.T) {
	for _, tc := range []struct{ key, toml string }{
		{"user", `user = "root"`},
		{"ping_host", `ping_host = "1.1.1.1"`},
		{"art", `art = false`},
		{"art_mode", `art_mode = "kitty"`},
		{"art_mode", `art_mode = 3`},
	} {
		t.Run(tc.toml, func(t *testing.T) {
			writeConfig(t, tc.toml+"\nname = \"Den\"\n")
			cfg := Load()
			want := "config.toml: " + tc.key + " ignored (retired: lp10 has no ssh and no album art since firmware AR241CP_8747)"
			if cfg.Warn != want {
				t.Errorf("warn = %q, want %q", cfg.Warn, want)
			}
			if strings.Contains(cfg.Warn, "unknown key") {
				t.Errorf("a retired key reads as unknown: %q", cfg.Warn)
			}
			cfg.Warn = ""
			if want := (Config{Host: defHost, StateKey: defHost, Name: "Den", VolStep: defVolStep, Discover: true, Theme: defTheme}); cfg != want {
				t.Errorf("a retired key changed the config: %+v, want %+v", cfg, want)
			}
		})
	}
}

// Every key the parser does not know still complains by name, retired keys
// beside it included, in a stable (sorted) order.
func TestUnknownKeysStillComplain(t *testing.T) {
	cfg := Config{Host: defHost, Name: DefaultName, VolStep: defVolStep, Discover: true, Theme: defTheme}
	before := cfg
	got := applyTOML(&cfg, map[string]any{"zzz": 1, "user": "pi", "art_cache": "x", "ping": "y"})
	want := []string{
		`unknown key "art_cache"`,
		`unknown key "ping"`,
		"user ignored (retired: lp10 has no ssh and no album art since firmware AR241CP_8747)",
		`unknown key "zzz"`,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("complaints = %q, want %q", got, want)
	}
	if cfg != before {
		t.Errorf("unknown and retired keys changed the config: %+v", cfg)
	}
}
