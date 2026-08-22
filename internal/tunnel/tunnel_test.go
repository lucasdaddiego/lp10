package tunnel

import (
	"reflect"
	"strings"
	"testing"
)

func TestClamp(t *testing.T) {
	cases := []struct {
		code string
		in   int
		want int
	}{
		{"MXV", 50, 50},
		{"MXV", -5, 0},
		{"MXV", 250, 100},
		{"BAS", -99, -10},
		{"BAS", 99, 10},
		{"EQE", 7, 1},
		{"EQS", 99, MaxPresets - 1},
		{"BAL", -500, -100},
		{"VBI", 73, 73},
		{"ZZZ", 12345, 12345}, // unknown code passes through
	}
	for _, c := range cases {
		if got := Clamp(c.code, c.in); got != c.want {
			t.Errorf("Clamp(%q,%d)=%d want %d", c.code, c.in, got, c.want)
		}
	}
}

func TestSetAndQuery(t *testing.T) {
	if got := Set("MXV", 100); got != "MXV:100;" {
		t.Errorf("Set MXV 100 = %q", got)
	}
	if got := Set("MXV", 250); got != "MXV:100;" { // clamped
		t.Errorf("Set MXV 250 = %q (want clamp to 100)", got)
	}
	if got := Set("BAS", -99); got != "BAS:-10;" {
		t.Errorf("Set BAS -99 = %q (want clamp to -10)", got)
	}
	if got := Query("EQS"); got != "EQS;" {
		t.Errorf("Query EQS = %q", got)
	}
}

func TestSeedQueriesCoversEveryControl(t *testing.T) {
	got := SeedQueries()
	if len(got) != len(Specs)+1 {
		t.Fatalf("SeedQueries len=%d want %d", len(got), len(Specs)+1)
	}
	for i, q := range got[:len(Specs)] {
		if q != Specs[i].Code+";" {
			t.Errorf("SeedQueries[%d]=%q want %q", i, q, Specs[i].Code+";")
		}
	}
	if got[len(Specs)] != "PEQ;" {
		t.Errorf("last seed = %q, want the PEQ preset-name query", got[len(Specs)])
	}
}

func TestParseFrames(t *testing.T) {
	// the exact 7-control snapshot captured from the device
	in := "MXV:100;EQS:0;VBS:1;VBI:15;BAS:3;MID:0;TRE:3;"
	got, rest := ParseFrames(in)
	want := []Update{
		{Code: "MXV", Val: 100}, {Code: "EQS", Val: 0}, {Code: "VBS", Val: 1}, {Code: "VBI", Val: 15},
		{Code: "BAS", Val: 3}, {Code: "MID", Val: 0}, {Code: "TRE", Val: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseFrames updates=%v want %v", got, want)
	}
	if rest != "" {
		t.Errorf("ParseFrames rest=%q want empty", rest)
	}
}

func TestParseFramesPartialCarry(t *testing.T) {
	got, rest := ParseFrames("MXV:100;BAS:")
	if len(got) != 1 || !reflect.DeepEqual(got[0], Update{Code: "MXV", Val: 100}) {
		t.Errorf("got=%v want one MXV:100", got)
	}
	if rest != "BAS:" {
		t.Errorf("rest=%q want %q", rest, "BAS:")
	}
	// feeding the carry + the remainder completes the frame
	got2, rest2 := ParseFrames(rest + "7;")
	if len(got2) != 1 || got2[0].Code != "BAS" || got2[0].Val != 7 {
		t.Errorf("got2=%v want one BAS:7", got2)
	}
	if rest2 != "" {
		t.Errorf("rest2=%q want empty", rest2)
	}
}

func TestParseFramesSkipsJunk(t *testing.T) {
	// negative tone value, a duplicated broadcast, an unknown code, a valueless
	// query echo, and a non-numeric payload
	got, _ := ParseFrames("TRE:-7;TRE:-7;XYZ:5;MXV;MXV:abc;VBS:1;")
	want := []Update{{Code: "TRE", Val: -7}, {Code: "TRE", Val: -7}, {Code: "VBS", Val: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want %v", got, want)
	}
}

// Inbound readbacks are deliberately NOT clamped: the display must report what
// the device actually holds (another client can set values past the UI's
// conservative bounds), and only outbound writes clamp (see Set).
func TestParseFramesKeepsRawDeviceValues(t *testing.T) {
	got, rest := ParseFrames("MXV:999;BAS:-99;VBS:8;")
	want := []Update{{Code: "MXV", Val: 999}, {Code: "BAS", Val: -99}, {Code: "VBS", Val: 8}}
	if !reflect.DeepEqual(got, want) || rest != "" {
		t.Errorf("ParseFrames out-of-range = %v, %q; want raw %v, empty", got, rest, want)
	}
}

func TestParsePresetsList(t *testing.T) {
	out, rest := ParseFrames("PEQ:0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal;EQS:1;")
	if rest != "" || len(out) != 2 {
		t.Fatalf("out=%v rest=%q", out, rest)
	}
	want := []string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"}
	if out[0].Code != "PEQ" || !reflect.DeepEqual(out[0].Names, want) {
		t.Errorf("PEQ update = %+v, want names %v", out[0], want)
	}
	if out[1].Code != "EQS" || out[1].Val != 1 {
		t.Errorf("EQS update = %+v", out[1])
	}
	// gaps, junk, bounds, hostile names
	out, _ = ParseFrames("PEQ:3@Rock,x@Bad,1@Cl\x1bassical,20@Far,-1@Neg,2@,5@" + strings.Repeat("A", 40) + ";")
	if len(out) != 1 {
		t.Fatalf("out=%v", out)
	}
	n := out[0].Names
	if len(n) != 6 || n[0] != "" || n[1] != "Classical" || n[2] != "" || n[3] != "Rock" || len([]rune(n[5])) != 16 {
		t.Errorf("names = %q", n)
	}
	// an empty / all-junk list is dropped, not an empty update
	if out, _ := ParseFrames("PEQ:;PEQ:junk;"); len(out) != 0 {
		t.Errorf("junk PEQ produced %v", out)
	}
}
