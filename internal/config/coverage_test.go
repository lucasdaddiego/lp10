package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestCov_HomeDir exercises both reachable branches of homeDir: the
// os.UserHomeDir success path (HOME set), and the fallback to user.Current()
// when HOME is empty (on darwin os.UserHomeDir then returns an error).
func TestCov_HomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := homeDir(); got != home {
		t.Fatalf("homeDir with HOME=%q = %q, want %q", home, got, home)
	}

	// Empty HOME -> os.UserHomeDir errors -> user.Current() resolves the home.
	t.Setenv("HOME", "")
	if got := homeDir(); got == "" {
		t.Fatal("homeDir fallback returned empty; expected a home via user.Current()")
	}
}

// TestCov_LoadClampsHighVolStep covers the upper clamp branch (vol_step > 100)
// in Load, which the existing suite's lower clamp (0 -> 1) does not reach.
func TestCov_LoadClampsHighVolStep(t *testing.T) {
	writeConfig(t, "vol_step = 200\n")
	if got := Load().VolStep; got != 100 {
		t.Errorf("vol_step 200 should clamp to 100, got %d", got)
	}
}

// TestCov_LoadDerivesBaseFromHome covers the base == "" branch of Load: with
// XDG_CONFIG_HOME unset, the config base is derived from the home dir. Pointing
// HOME at an empty temp dir keeps the real user config out of the picture and
// yields a missing file (defaults, no warning).
func TestCov_LoadDerivesBaseFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LP10_HOST", "")
	cfg := Load()
	if cfg.Host != defHost {
		t.Errorf("Host = %q, want default %q when base is derived from HOME", cfg.Host, defHost)
	}
	if cfg.Warn != "" {
		t.Errorf("no config under the derived base should not warn, got %q", cfg.Warn)
	}
}

// TestCov_ApplyTOMLAllKeys drives every recognized key through applyTOML,
// with vol_step as an int64, and checks a clean set makes no complaint.
func TestCov_ApplyTOMLAllKeys(t *testing.T) {
	cfg := Config{Host: defHost, Name: DefaultName, VolStep: defVolStep, Discover: true, Theme: defTheme}
	complaints := applyTOML(&cfg, map[string]any{
		"host":     "10.0.0.1",
		"name":     "Kitchen",
		"discover": false,
		"theme":    "light",
		"vol_step": int64(7),
	})
	want := Config{Host: "10.0.0.1", Name: "Kitchen", VolStep: 7, Discover: false, Theme: "light"}
	if cfg != want || len(complaints) != 0 {
		t.Fatalf("applyTOML = %+v (complaints %q), want every key applied: %+v", cfg, complaints, want)
	}
}

// TestCov_ApplyTOMLIntegralFloatVolStep covers the float64 case of vol_step:
// an integral float (9.0) within int range is accepted.
func TestCov_ApplyTOMLIntegralFloatVolStep(t *testing.T) {
	cfg := Config{VolStep: defVolStep}
	applyTOML(&cfg, map[string]any{"vol_step": float64(9.0)})
	if cfg.VolStep != 9 {
		t.Errorf("integral float vol_step should apply as 9, got %d", cfg.VolStep)
	}
}

func TestCov_ApplyTOMLRejectsFloatAtIntBoundary(t *testing.T) {
	cfg := Config{VolStep: defVolStep}
	limit := float64(uint64(1) << (strconv.IntSize - 1))
	applyTOML(&cfg, map[string]any{"vol_step": limit})
	if cfg.VolStep != defVolStep {
		t.Errorf("float at first invalid int value should be ignored, got %d", cfg.VolStep)
	}
}

// TestCov_ApplyTOMLRejectsBadValues exercises the rejecting (false) branches:
// an invalid theme and a wrong-typed one keep the default, a non-integral
// float and an out-of-range float are ignored, and a wrong-typed string field
// is ignored.
func TestCov_ApplyTOMLRejectsBadValues(t *testing.T) {
	cfg := Config{Theme: defTheme, VolStep: defVolStep, Host: defHost, Name: DefaultName}
	complaints := applyTOML(&cfg, map[string]any{
		"theme":    "sixel",      // not in themes -> ignored
		"vol_step": float64(2.5), // non-integral float -> ignored
		"host":     int64(123),   // wrong type for a string field -> ignored
		"name":     true,         // wrong type for a string field -> ignored
	})
	if cfg.Theme != defTheme {
		t.Errorf("invalid theme should be ignored, got %q", cfg.Theme)
	}
	if cfg.Name != DefaultName {
		t.Errorf("wrong-typed name should be ignored, got %q", cfg.Name)
	}
	if len(complaints) != 4 {
		t.Errorf("complaints = %q, want one per bad value", complaints)
	}
	if applyTOML(&cfg, map[string]any{"theme": int64(1)}); cfg.Theme != defTheme {
		t.Errorf("wrong-typed theme should be ignored, got %q", cfg.Theme)
	}
	if cfg.VolStep != defVolStep {
		t.Errorf("non-integral float vol_step should be ignored, got %d", cfg.VolStep)
	}
	if cfg.Host != defHost {
		t.Errorf("wrong-typed host should be ignored, got %q", cfg.Host)
	}

	// An integral but out-of-range float must also be rejected (no overflow).
	cfg2 := Config{VolStep: defVolStep}
	applyTOML(&cfg2, map[string]any{"vol_step": 1e19})
	if cfg2.VolStep != defVolStep {
		t.Errorf("out-of-range float vol_step should be ignored, got %d", cfg2.VolStep)
	}
}

// TestCov_StateDirDerivesFromHome covers the LP10_STATE_DIR-unset branch where
// the state dir is built under a present home and created on demand.
func TestCov_StateDirDerivesFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LP10_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	got := StateDir()
	want := filepath.Join(home, ".local", "state", "lp10")
	if got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("state dir not created at %q", got)
	}
}

// TestCov_SnapshotAndSweepPaths covers the non-empty (StateDir != "") return
// of SnapshotPath and SweepPath.
func TestCov_SnapshotAndSweepPaths(t *testing.T) {
	d := t.TempDir()
	t.Setenv("LP10_STATE_DIR", d)
	cfg := Config{Host: "lp10.local"}
	if got, want := SweepPath(cfg), filepath.Join(d, "sweep-lp10.local.json"); got != want {
		t.Errorf("SweepPath = %q, want %q", got, want)
	}
	if got, want := SnapshotPath(cfg), filepath.Join(d, "snapshot-lp10.local.json"); got != want {
		t.Errorf("SnapshotPath = %q, want %q", got, want)
	}
}

// TestCov_PathsEmptyWithoutStateDir covers the "" returns of SnapshotPath and
// SweepPath when StateDir cannot be created (its target's parent is a regular
// file).
func TestCov_PathsEmptyWithoutStateDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(blocker, "sub"))
	cfg := Config{Host: "h"}
	if got := SnapshotPath(cfg); got != "" {
		t.Errorf("SnapshotPath = %q, want empty", got)
	}
	if got := SweepPath(cfg); got != "" {
		t.Errorf("SweepPath = %q, want empty", got)
	}
}

// TestCov_LoadSnapshotInvalidJSON covers the json.Unmarshal-failure branch:
// malformed JSON yields nil.
func TestCov_LoadSnapshotInvalidJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	if err := os.WriteFile(p, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadSnapshot(p); got != nil {
		t.Errorf("invalid JSON snapshot should be nil, got %v", got)
	}
}

// TestCov_LoadSnapshotVariants covers the typed decoder's accepting paths: a
// snapshot with no "eq" key, one with an explicit null eq, and an empty object
// all load.
func TestCov_LoadSnapshotVariants(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	for raw, vol := range map[string]int{`{"vol":12}`: 12, `{"vol":3,"eq":null}`: 3, `{}`: 0} {
		if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LoadSnapshot(p); got == nil || got.Vol != vol || len(got.EQ) != 0 {
			t.Errorf("LoadSnapshot(%s) = %+v, want vol %d and no EQ", raw, got, vol)
		}
	}
	if got := LoadSnapshot(filepath.Join(t.TempDir(), "null.json")); got != nil {
		t.Errorf("a missing file loaded as %+v", got)
	}
	if err := os.WriteFile(p, []byte(`null`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadSnapshot(p); got != nil {
		t.Errorf("a JSON null root loaded as %+v", got)
	}
}

// TestCov_ClampVol covers clampVol directly across its three branches.
func TestCov_ClampVol(t *testing.T) {
	cases := map[int]int{-7: 1, 0: 1, 1: 1, 50: 50, 100: 100, 250: 100}
	for in, want := range cases {
		if got := clampVol(in); got != want {
			t.Errorf("clampVol(%d) = %d, want %d", in, got, want)
		}
	}
}
