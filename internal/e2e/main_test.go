package e2e

import (
	"os"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/testutil"
)

// TestMain removes the helper binaries testutil built for this package once
// every test has run (they are built once per test binary, so no test can own
// their lifetime).
func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}
