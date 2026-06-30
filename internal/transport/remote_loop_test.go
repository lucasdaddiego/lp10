package transport

import (
	"os"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/transport/loopgen"
)

// TestEmbeddedLoopMatchesSource fails if remote_loop.sh (the embedded, minified
// device loop) is stale relative to remote_loop.src.sh — someone edited the source
// without running `go generate ./internal/transport`, or hand-edited the generated
// file. Byte-identity here is also what guarantees the readable-source split changed
// no device behavior: the bytes shipped to the LP10 are exactly what they were.
func TestEmbeddedLoopMatchesSource(t *testing.T) {
	src, err := os.ReadFile("remote_loop.src.sh")
	if err != nil {
		t.Fatal(err)
	}
	got := loopgen.Minify(string(src))
	if got == remoteLoopScript {
		return
	}
	i := firstDiff(got, remoteLoopScript)
	t.Errorf("remote_loop.sh is stale — run `go generate ./internal/transport`\n"+
		"  minify(src) len=%d  embedded len=%d  first diff at byte %d\n"+
		"  got : …%q…\n  want: …%q…",
		len(got), len(remoteLoopScript), i, window(got, i), window(remoteLoopScript, i))
}

// firstDiff returns the index of the first differing byte, or -1 if equal.
func firstDiff(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// window returns up to 40 bytes of s centred on i, for diff context.
func window(s string, i int) string {
	if i < 0 {
		return ""
	}
	return s[max(0, i-20):min(len(s), i+20)]
}
