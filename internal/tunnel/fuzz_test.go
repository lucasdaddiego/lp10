// Hostile-input fuzzing for the :2018 control-tunnel parser and the outbound
// allowlist. The stream is plain text from an unauthenticated LAN socket, so
// ParseFrames must hold its invariants for arbitrary garbage: never panic,
// only emit known codes with the payload their code implies, never let a
// control rune through to the terminal, and stay stream-consistent under any
// chunking. Wire must never put anything on the socket but a single
// allowlisted frame.

package tunnel

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// unsafeRune reports a rune that must never reach the terminal from a device
// string: a C0/C1 control, a bidi control, or any other control, format or
// separator rune but the ASCII space. The ZWJ is the one format rune
// protocol.Printable keeps (inside emoji sequences), and a clip may leave it
// at an edge, which is invisible but harmless.
func unsafeRune(r rune) bool {
	if r == '\u200d' || r == ' ' {
		return false
	}
	return r < 0x20 || (r >= 0x7f && r <= 0x9f) || unicode.In(r, unicode.C, unicode.Z)
}

func checkText(t *testing.T, what, s string, bound int) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("%s %+q is not valid UTF-8", what, s)
	}
	if n := utf8.RuneCountInString(s); n > bound {
		t.Fatalf("%s %+q holds %d runes, bound %d", what, s, n, bound)
	}
	if i := strings.IndexFunc(s, unsafeRune); i >= 0 {
		t.Fatalf("%s %+q keeps the control rune %U", what, s, []rune(s[i:])[0])
	}
	if s != strings.TrimSpace(s) {
		t.Fatalf("%s %+q is not trimmed", what, s)
	}
}

func FuzzParseFrames(f *testing.F) {
	f.Add("MXV:100;")
	f.Add("BAS:-10;MID:0;TRE:10;")
	f.Add("EQS:1;VBS:0;VBI:55;")
	f.Add("MXV;BAS;")                // query echoes (valueless)
	f.Add("MXV:9999;BAS:-9999;")     // out-of-range values
	f.Add("XXX:5;:;;;MXV: 42 ;par")  // unknown code, empties, spaces, partial tail
	f.Add("MXV:1e3;MXV:0x10;MXV:-;") // non-numeric values
	f.Add(strings.Repeat("MXV:1;", 64) + "MID:")
	f.Add("STA:NET,0,44,0,0,3,0,1,1,0;VOL:44;MUT:0;PLA:1;SRC:NET;VER:29-1d316f0c-10;")
	f.Add("TIT:Big Bang;ART:Usted Señalemelo;ALB:Big Bang;PLA:1;VND:spotify;RAW:NEXT;")
	f.Add("PEQ:0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal;")
	f.Add("TIT:\x1b]8;;http://x\x07click\x1b]8;;\x07;ART:\u202eevil\u2066;")
	f.Add("STA:N\x1bET,1,x,0;STA:,0,0,0,0,0,0,0;STA:" + strings.Repeat("A", 40) + ",0,1,0,0,0,0,1;")
	f.Add("TIT: \u200d\U0001f468\u200d\U0001f469 ;VND:" + strings.Repeat("\u0301", 40) + ";")
	f.Add("PEQ:0@ \u200dX,1@a@\u200db;SRC:LINE-IN:2;")
	f.Fuzz(func(t *testing.T, buf string) {
		out, rest := ParseFrames(buf)
		if strings.Contains(rest, ";") {
			t.Fatalf("rest still holds a complete frame: %q", rest)
		}
		if n := strings.Count(buf, ";"); len(out) > n {
			t.Fatalf("%d updates from %d frames", len(out), n)
		}
		for _, u := range out {
			// Every update has a known code and exactly the payload its code
			// implies. Numeric values are raw (readbacks report what the
			// device holds); text is stripped and bounded.
			_, control := Lookup(u.Code)
			bound, text := textCodes[u.Code]
			switch {
			case u.Code == PresetsCode:
				if u.Names == nil || u.Status != nil || u.Text != "" || u.Val != 0 {
					t.Fatalf("PEQ update with the wrong payload: %+v", u)
				}
				if len(u.Names) > MaxPresets {
					t.Fatalf("%d preset names, bound %d", len(u.Names), MaxPresets)
				}
				for _, n := range u.Names {
					checkText(t, "preset name", n, maxPresetName)
					if strings.ContainsAny(n, ";,@") {
						t.Fatalf("preset name %q keeps a list separator", n)
					}
				}
			case u.Code == StatusCode:
				if u.Status == nil || u.Names != nil || u.Text != "" || u.Val != 0 {
					t.Fatalf("STA update with the wrong payload: %+v", u)
				}
				checkText(t, "STA source", u.Status.Source, textCodes[SourceCode])
			case text:
				if u.Status != nil || u.Names != nil || u.Val != 0 {
					t.Fatalf("%s update with the wrong payload: %+v", u.Code, u)
				}
				checkText(t, u.Code+" text", u.Text, bound)
			case control || numeric[u.Code]:
				if u.Status != nil || u.Names != nil || u.Text != "" {
					t.Fatalf("%s update with the wrong payload: %+v", u.Code, u)
				}
			default:
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
			if !reflect.DeepEqual(u, out[i]) {
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

// FuzzWire holds the socket allowlist for any code, value and form: what
// Wire returns is one frame of an allowlisted code in the one form that code
// takes, a set lands inside its bounds and reads back as the value sent, and
// a refusal sends nothing.
func FuzzWire(f *testing.F) {
	for _, code := range []string{"MXV", "BAL", "VOL", "MUT", "STA", "PEQ", "POP", "NXT", "WRS", "SYS", "TIT", "VOL;SYS:RESET", ""} {
		f.Add(code, 0, true)
		f.Add(code, 200, false)
		f.Add(code, -200, false)
	}
	f.Fuzz(func(t *testing.T, code string, val int, query bool) {
		frame, ok := Wire(code, val, query)
		if !ok {
			if frame != "" {
				t.Fatalf("a refused Wire(%q) still returned %q", code, frame)
			}
			return
		}
		if strings.Count(frame, ";") != 1 || !strings.HasSuffix(frame, ";") || !strings.HasPrefix(frame, code) {
			t.Fatalf("Wire(%q, %d, %v) = %q is not one frame of that code", code, val, query, frame)
		}
		spec, control := Lookup(code)
		player, setting := playerSpecs[code]
		switch {
		case actions[code]:
			if query || frame != code+";" {
				t.Fatalf("action %q sent as %q (query=%v)", code, frame, query)
			}
		case queryOnly[code]:
			if !query || frame != code+";" {
				t.Fatalf("get-only %q sent as %q (query=%v)", code, frame, query)
			}
		case control || setting:
			if !control {
				spec = player
			}
			if query {
				if frame != code+";" {
					t.Fatalf("query of %q sent as %q", code, frame)
				}
				return
			}
			out, rest := ParseFrames(frame)
			if len(out) != 1 || rest != "" || out[0].Code != code {
				t.Fatalf("set %q does not read back: %+v rest %q", frame, out, rest)
			}
			if v := out[0].Val; v < spec.Min || v > spec.Max || v != max(spec.Min, min(spec.Max, val)) {
				t.Fatalf("set of %q to %d sent %d, bounds %d..%d", code, val, v, spec.Min, spec.Max)
			}
		default:
			t.Fatalf("Wire allowed the code %q", code)
		}
	})
}
