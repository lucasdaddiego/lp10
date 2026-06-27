package tunnel

import (
	"reflect"
	"testing"
)

// TestCov_LookupKnown exercises the hit path of Lookup: a known code returns the
// exact Spec and ok==true.
func TestCov_LookupKnown(t *testing.T) {
	got, ok := Lookup("MXV")
	if !ok {
		t.Fatalf("Lookup(%q) ok=false, want true", "MXV")
	}
	want := Spec{Code: "MXV", Label: "Max Volume", Kind: Ranged, Min: 0, Max: 100, Step: 5}
	if got != want {
		t.Errorf("Lookup(%q)=%+v want %+v", "MXV", got, want)
	}

	// A toggle, to pin the Kind field as well as the bounds.
	got, ok = Lookup("EQS")
	if !ok {
		t.Fatalf("Lookup(%q) ok=false, want true", "EQS")
	}
	wantEQS := Spec{Code: "EQS", Label: "EQ", Kind: Toggle, Min: 0, Max: 1, Step: 1}
	if got != wantEQS {
		t.Errorf("Lookup(%q)=%+v want %+v", "EQS", got, wantEQS)
	}
}

// TestCov_LookupUnknown exercises the miss path of Lookup: an unknown code
// returns the zero Spec and ok==false.
func TestCov_LookupUnknown(t *testing.T) {
	got, ok := Lookup("ZZZ")
	if ok {
		t.Errorf("Lookup(%q) ok=true, want false", "ZZZ")
	}
	if got != (Spec{}) {
		t.Errorf("Lookup(%q)=%+v want zero Spec", "ZZZ", got)
	}
}

// TestCov_Clamp hits every branch: unknown passthrough, below Min, above Max,
// and an in-range value returned untouched.
func TestCov_Clamp(t *testing.T) {
	cases := []struct {
		name string
		code string
		in   int
		want int
	}{
		{"unknown passthrough", "ZZZ", 999, 999},
		{"unknown passthrough negative", "ZZZ", -999, -999},
		{"below min", "MXV", -5, 0},
		{"below min negative range", "BAS", -99, -10},
		{"above max", "MXV", 250, 100},
		{"above max toggle", "EQS", 7, 1},
		{"in range", "VBI", 73, 73},
		{"in range at min boundary", "BAS", -10, -10},
		{"in range at max boundary", "BAS", 10, 10},
	}
	for _, c := range cases {
		if got := Clamp(c.code, c.in); got != c.want {
			t.Errorf("%s: Clamp(%q,%d)=%d want %d", c.name, c.code, c.in, got, c.want)
		}
	}
}

// TestCov_Set confirms Set emits "CODE:VALUE;" and clamps the value first.
func TestCov_Set(t *testing.T) {
	if got := Set("MXV", 50); got != "MXV:50;" {
		t.Errorf("Set(MXV,50)=%q want %q", got, "MXV:50;")
	}
	if got := Set("MXV", 250); got != "MXV:100;" { // clamped to Max
		t.Errorf("Set(MXV,250)=%q want %q", got, "MXV:100;")
	}
	if got := Set("BAS", -99); got != "BAS:-10;" { // clamped to Min
		t.Errorf("Set(BAS,-99)=%q want %q", got, "BAS:-10;")
	}
}

// TestCov_Query confirms Query emits "CODE;".
func TestCov_Query(t *testing.T) {
	if got := Query("MXV"); got != "MXV;" {
		t.Errorf("Query(MXV)=%q want %q", got, "MXV;")
	}
	if got := Query("EQS"); got != "EQS;" {
		t.Errorf("Query(EQS)=%q want %q", got, "EQS;")
	}
}

// TestCov_SeedQueries confirms one query per control, in Specs order, with
// len(out) == len(Specs).
func TestCov_SeedQueries(t *testing.T) {
	got := SeedQueries()
	if len(got) != len(Specs) {
		t.Fatalf("SeedQueries len=%d want %d", len(got), len(Specs))
	}
	want := []string{"MXV;", "EQS;", "BAS;", "MID;", "TRE;", "VBS;", "VBI;"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SeedQueries=%v want %v", got, want)
	}
}

// TestCov_ParseFramesMultiple parses several complete frames and leaves no rest.
func TestCov_ParseFramesMultiple(t *testing.T) {
	out, rest := ParseFrames("MXV:100;EQS:0;BAS:-3;")
	want := []Update{{"MXV", 100}, {"EQS", 0}, {"BAS", -3}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out=%v want %v", out, want)
	}
	if rest != "" {
		t.Errorf("rest=%q want empty", rest)
	}
}

// TestCov_ParseFramesTrailingPartial returns the un-terminated tail (no ';') as
// rest, with the completed frame parsed.
func TestCov_ParseFramesTrailingPartial(t *testing.T) {
	out, rest := ParseFrames("MXV:100;BAS:7")
	want := []Update{{"MXV", 100}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out=%v want %v", out, want)
	}
	if rest != "BAS:7" {
		t.Errorf("rest=%q want %q", rest, "BAS:7")
	}
}

// TestCov_ParseFramesSkipUnknown skips a frame whose code is not a known control.
func TestCov_ParseFramesSkipUnknown(t *testing.T) {
	out, rest := ParseFrames("XYZ:5;MXV:10;")
	want := []Update{{"MXV", 10}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out=%v want %v", out, want)
	}
	if rest != "" {
		t.Errorf("rest=%q want empty", rest)
	}
}

// TestCov_ParseFramesSkipValueless skips a bare "CODE" frame with no ':'
// (our own query echo).
func TestCov_ParseFramesSkipValueless(t *testing.T) {
	out, rest := ParseFrames("MXV;MXV:10;")
	want := []Update{{"MXV", 10}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out=%v want %v", out, want)
	}
	if rest != "" {
		t.Errorf("rest=%q want empty", rest)
	}
}

// TestCov_ParseFramesSkipNonNumeric skips a frame whose value is not an integer.
func TestCov_ParseFramesSkipNonNumeric(t *testing.T) {
	out, rest := ParseFrames("MXV:abc;MXV:10;")
	want := []Update{{"MXV", 10}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out=%v want %v", out, want)
	}
	if rest != "" {
		t.Errorf("rest=%q want empty", rest)
	}
}

// TestCov_ParseFramesTrimsWhitespace confirms surrounding whitespace on the value
// is trimmed before the numeric parse.
func TestCov_ParseFramesTrimsWhitespace(t *testing.T) {
	out, rest := ParseFrames("MXV:  100  ;BAS:\t-3\t;")
	want := []Update{{"MXV", 100}, {"BAS", -3}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out=%v want %v", out, want)
	}
	if rest != "" {
		t.Errorf("rest=%q want empty", rest)
	}
}

// TestCov_ParseFramesEmpty: no terminator at all returns no updates and the whole
// buffer as rest.
func TestCov_ParseFramesEmpty(t *testing.T) {
	out, rest := ParseFrames("partial-no-semicolon")
	if len(out) != 0 {
		t.Errorf("out=%v want none", out)
	}
	if rest != "partial-no-semicolon" {
		t.Errorf("rest=%q want whole buffer", rest)
	}
}

// TestCov_parseFrame drives the unexported parseFrame directly for the happy path
// (incl. whitespace trimming) and each reject path.
func TestCov_parseFrame(t *testing.T) {
	// happy path
	if code, val, ok := parseFrame("MXV:100"); !ok || code != "MXV" || val != 100 {
		t.Errorf("parseFrame(MXV:100)=(%q,%d,%v) want (MXV,100,true)", code, val, ok)
	}
	// happy path with whitespace trimmed around the value
	if code, val, ok := parseFrame("BAS:  -3 "); !ok || code != "BAS" || val != -3 {
		t.Errorf("parseFrame(BAS:  -3 )=(%q,%d,%v) want (BAS,-3,true)", code, val, ok)
	}

	// reject: no ':' separator
	if code, val, ok := parseFrame("MXV"); ok || code != "" || val != 0 {
		t.Errorf("parseFrame(MXV)=(%q,%d,%v) want (\"\",0,false)", code, val, ok)
	}
	// reject: unknown code
	if code, val, ok := parseFrame("XYZ:5"); ok || code != "" || val != 0 {
		t.Errorf("parseFrame(XYZ:5)=(%q,%d,%v) want (\"\",0,false)", code, val, ok)
	}
	// reject: non-numeric value
	if code, val, ok := parseFrame("MXV:abc"); ok || code != "" || val != 0 {
		t.Errorf("parseFrame(MXV:abc)=(%q,%d,%v) want (\"\",0,false)", code, val, ok)
	}
}
