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

// The whole loop rides one ssh exec-request string; dropbear rejects requests
// longer than MAX_CMD_LEN (9000 by default) with a connection-fatal error whose
// stderr classifies as transient — a silent infinite reconnect loop. Guard the
// headroom so growth is caught here, not on the device.
func TestRemoteLoopFitsDropbearCmdLen(t *testing.T) {
	if n := len(RemoteLoop("a-hostname-of-plausible-length.example.com")); n >= 8500 {
		t.Errorf("RemoteLoop is %d bytes — within 500 of dropbear's MAX_CMD_LEN (9000); trim the loop", n)
	}
}

// A ping_host that sanitizeHost would have to mangle falls back to the default
// target whole — a stripped remainder is a bogus hostname that fails every
// gated ping with no indication why (an IPv6 literal was the concrete case).
func TestSanitizeHostFallsBackWhenStripped(t *testing.T) {
	cases := map[string]string{
		"":                  "spotify.com",
		"2606:4700::1111":   "spotify.com", // IPv6 literal: colons are not embeddable
		"bad host'; rm -rf": "spotify.com", // quote breakout attempt
		"spotify.com":       "spotify.com",
		"1.1.1.1":           "1.1.1.1",
		"my-host.example":   "my-host.example",
	}
	for in, want := range cases {
		if got := sanitizeHost(in); got != want {
			t.Errorf("sanitizeHost(%q) = %q, want %q", in, got, want)
		}
	}
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
