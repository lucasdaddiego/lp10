// Package testutil provides shared helpers for the test suite: an env-isolation
// fixture (mirroring conftest.isolated_state) and a builder for the fake ssh
// transport binary. Imported only from _test.go files.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// envVars are the ambient LP10_* / config overrides a test must not inherit.
var envVars = []string{
	"LP10_HOST", "LP10_SSH", "LP10_ASKPASS", "LP10_FAKE_SCENARIO",
	"LP10_FAKE_CMDLOG", "LP10_FAKE_DIR", "LP10_FAKE_HEAL_AFTER",
	"LP10_STATE_DIR",
}

// Isolate clears ambient LP10_* env and points state + config at temp dirs, so
// no test touches the real state dir, config, or Keychain-adjacent files.
func Isolate(t *testing.T) {
	t.Helper()
	for _, v := range envVars {
		t.Setenv(v, "") // empty is treated as unset by config.Load and fakessh
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
}

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

// goBuildArgs returns the `go build` args for a helper binary, adding coverage
// instrumentation when LP10_COVERDIR is set so the e2e subprocess execution of
// main/Run/fakessh counts toward the merged integration-coverage profile (the
// `make cover` flow). Without it, the binaries build plain, as before.
func goBuildArgs(bin, pkg string) []string {
	args := []string{"build"}
	if os.Getenv("LP10_COVERDIR") != "" {
		// Module-absolute pattern (not ./...): this build runs with the test's CWD
		// (e.g. internal/e2e), where ./... would match nothing in the binary.
		args = append(args, "-cover", "-coverpkg=github.com/lucasdaddiego/lp10go/...")
	}
	return append(args, "-o", bin, pkg)
}

// FakeSSH builds (once per test binary, into a process-stable temp dir) and
// returns the path to the fake ssh transport, for use as $LP10_SSH.
func FakeSSH(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		tmp, e := os.MkdirTemp("", "lp10-fakessh-")
		if e != nil {
			fakeErr = &buildError{e, ""}
			return
		}
		bin := filepath.Join(tmp, "fakessh")
		out, e := exec.Command("go", goBuildArgs(bin,
			"github.com/lucasdaddiego/lp10go/cmd/fakessh")...).CombinedOutput()
		if e != nil {
			fakeErr = &buildError{e, string(out)}
			return
		}
		fakePath = bin
	})
	if fakeErr != nil {
		t.Fatalf("build fakessh: %v", fakeErr)
	}
	return fakePath
}

var (
	mainOnce sync.Once
	mainPath string
	mainErr  error
)

// BuildMain builds (once per test binary) and returns the path to the lp10
// command binary, for end-to-end tests.
func BuildMain(t *testing.T) string {
	t.Helper()
	mainOnce.Do(func() {
		tmp, e := os.MkdirTemp("", "lp10-bin-")
		if e != nil {
			mainErr = &buildError{e, ""}
			return
		}
		bin := filepath.Join(tmp, "lp10")
		out, e := exec.Command("go", goBuildArgs(bin,
			"github.com/lucasdaddiego/lp10go")...).CombinedOutput()
		if e != nil {
			mainErr = &buildError{e, string(out)}
			return
		}
		mainPath = bin
	})
	if mainErr != nil {
		t.Fatalf("build lp10: %v", mainErr)
	}
	return mainPath
}

type buildError struct {
	err error
	out string
}

func (b *buildError) Error() string { return b.err.Error() + "\n" + b.out }
