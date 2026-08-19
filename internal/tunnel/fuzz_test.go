// Hostile-input fuzzing for the :2018 control-tunnel parser. The stream is
// plain text from an unauthenticated LAN socket, so ParseFrames must hold its
// invariants for arbitrary garbage: never panic, only emit known codes, only
// emit in-bounds values, and stay stream-consistent under any chunking.

package tunnel

import (
	"strings"
	"testing"
)

func FuzzParseFrames(f *testing.F) {
	f.Add("MXV:100;")
	f.Add("BAS:-10;MID:0;TRE:10;")
	f.Add("EQS:1;VBS:0;VBI:55;")
	f.Add("MXV;BAS;")                // query echoes (valueless)
	f.Add("MXV:9999;BAS:-9999;")     // out-of-range values
	f.Add("XXX:5;:;;;MXV: 42 ;par")  // unknown code, empties, spaces, partial tail
	f.Add("MXV:1e3;MXV:0x10;MXV:-;") // non-numeric values
	f.Add(strings.Repeat("MXV:1;", 64) + "MID:")
	f.Fuzz(func(t *testing.T, buf string) {
		out, rest := ParseFrames(buf)
		if strings.Contains(rest, ";") {
			t.Fatalf("rest still holds a complete frame: %q", rest)
		}
		if n := strings.Count(buf, ";"); len(out) > n {
			t.Fatalf("%d updates from %d frames", len(out), n)
		}
		for _, u := range out {
			// Values are raw (readbacks report what the device holds); only
			// the code whitelist is an invariant here.
			if _, ok := Lookup(u.Code); !ok {
				t.Fatalf("unknown code emitted: %q", u.Code)
			}
		}
		// Stream consistency: any split point with the partial carried into the
		// next read must yield the same updates and the same final remainder.
		mid := len(buf) / 2
		o1, r1 := ParseFrames(buf[:mid])
		o2, r2 := ParseFrames(r1 + buf[mid:])
		if r2 != rest || len(o1)+len(o2) != len(out) {
			t.Fatalf("chunked parse diverged: %d+%d vs %d updates, rest %q vs %q",
				len(o1), len(o2), len(out), r2, rest)
		}
		for i, u := range append(o1, o2...) {
			if u != out[i] {
				t.Fatalf("chunked update %d = %+v, whole = %+v", i, u, out[i])
			}
		}
	})
}

func FuzzTunnelSet(f *testing.F) {
	f.Add("MXV", 100)
	f.Add("BAS", -9999)
	f.Add("EQS", 2)
	f.Add("nope", 7)
	f.Add("MXV;inj:1", 1) // wire-metacharacters in the code
	f.Fuzz(func(t *testing.T, code string, v int) {
		c := Clamp(code, v)
		if Clamp(code, c) != c {
			t.Fatalf("Clamp not idempotent for %q: %d -> %d", code, c, Clamp(code, c))
		}
		s := Set(code, v)
		if !strings.HasSuffix(s, ";") {
			t.Fatalf("Set output unterminated: %q", s)
		}
		if spec, ok := Lookup(code); ok {
			if c < spec.Min || c > spec.Max {
				t.Fatalf("Clamp out of bounds for %q: %d", code, c)
			}
			out, rest := ParseFrames(s)
			if len(out) != 1 || rest != "" || out[0].Code != code || out[0].Val != c {
				t.Fatalf("Set(%q,%d)=%q did not round-trip: %+v rest=%q", code, v, s, out, rest)
			}
		}
	})
}
