package e2e

import (
	"os"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/testutil"
)

// TestMain removes the lp10 binary testutil built for this package once every
// test has run (it is built once per test binary, so no test can own its
// lifetime).
func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}
