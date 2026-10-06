package tunnel

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lucasdaddiego/lp10/internal/protocol"
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
	// the player's settable codes clamp to their own range too
	for in, want := range map[int]string{150: "VOL:100;", -5: "VOL:0;", 44: "VOL:44;"} {
		if got := Set("VOL", in); got != want {
			t.Errorf("Set VOL %d = %q, want %q", in, got, want)
		}
	}
	if got := Set("MUT", 3); got != "MUT:1;" {
		t.Errorf("Set MUT 3 = %q, want MUT:1;", got)
	}
	if got := Set("BAS", -99); got != "BAS:-10;" {
		t.Errorf("Set BAS -99 = %q (want clamp to -10)", got)
	}
	if got := Query("EQS"); got != "EQS;" {
		t.Errorf("Query EQS = %q", got)
	}
}

// ---- what goes on the socket ------------------------------------------------

// The seed reads the player status first (the player paints from its one
// reply), then every EQ control in Specs order, the preset names, and the MCU
// version last. Every seed is a query Wire itself allows: the seed can never
// send what the allowlist refuses.
func TestSeedQueries(t *testing.T) {
	want := []string{"STA;", "MXV;", "EQE;", "EQS;", "BAS;", "MID;", "TRE;", "VBS;", "VBI;", "BAL;", "PEQ;", "VER;"}
	got := SeedQueries()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SeedQueries = %q, want %q", got, want)
	}
	// generated from Specs: a control added there is seeded too
	for i, s := range Specs {
		if got[i+1] != s.Code+";" {
			t.Errorf("seed %d = %q, want %q (Specs order)", i+1, got[i+1], s.Code+";")
		}
	}
	for _, q := range got {
		code := strings.TrimSuffix(q, ";")
		if w, ok := Wire(code, 0, true); !ok || w != q {
			t.Errorf("seed %q is not a query Wire allows: (%q, %v)", q, w, ok)
		}
	}
}

// Wire is the one allowlist for the socket. An EQ control or a player
// setting is read or set (a set clamped to its range), a get-only code is
// only read, an action is only sent bare, and everything else is refused —
// the MCU answers ~100 codes and several act blind (WRS wifi-setup,
// SYS:RESET, DEF:SAV, PMT/COE reboot), so the default is no.
func TestWire(t *testing.T) {
	cases := []struct {
		code  string
		val   int
		query bool
		want  string // "" = refused
	}{
		// EQ controls: query, set, clamp at both ends
		{"MXV", 0, true, "MXV;"},
		{"MXV", 50, false, "MXV:50;"},
		{"MXV", 250, false, "MXV:100;"},
		{"MXV", -1, false, "MXV:30;"}, // the write floor, not the display's 0
		{"EQE", 7, false, "EQE:1;"},
		{"EQS", 2, false, "EQS:2;"},
		{"EQS", 99, false, "EQS:15;"},
		{"BAS", -99, false, "BAS:-10;"},
		{"MID", 4, false, "MID:4;"},
		{"TRE", 11, false, "TRE:10;"},
		{"VBS", -1, false, "VBS:0;"},
		{"VBI", 105, false, "VBI:100;"},
		{"BAL", -500, false, "BAL:-100;"},
		{"BAL", math.MaxInt, false, "BAL:100;"},
		{"BAL", math.MinInt, false, "BAL:-100;"},
		// player settings
		{"VOL", 0, true, "VOL;"},
		{"VOL", 43, false, "VOL:43;"},
		{"VOL", 150, false, "VOL:100;"},
		{"VOL", -5, false, "VOL:0;"},
		{"VOL", math.MaxInt, false, "VOL:100;"},
		{"VOL", math.MinInt, false, "VOL:0;"},
		{"MUT", 0, true, "MUT;"},
		{"MUT", 1, false, "MUT:1;"},
		{"MUT", 0, false, "MUT:0;"},
		{"MUT", 7, false, "MUT:1;"},
		{"MUT", -3, false, "MUT:0;"},
		// get-only: a query, never a set
		{"STA", 0, true, "STA;"},
		{"STA", 1, false, ""},
		{"PLA", 0, true, "PLA;"},
		{"PLA", 1, false, ""},
		{"SRC", 0, true, "SRC;"},
		{"SRC", 1, false, ""},
		{"VER", 0, true, "VER;"},
		{"VER", 1, false, ""},
		{"PEQ", 0, true, "PEQ;"},
		{"PEQ", 1, false, ""},
		// actions: bare, whatever the value, never a query
		{"POP", 0, false, "POP;"},
		{"POP", 99, false, "POP;"},
		{"POP", 0, true, ""},
		{"NXT", 1, false, "NXT;"},
		{"NXT", 0, true, ""},
		{"PRE", -1, false, "PRE;"},
		{"PRE", 0, true, ""},
	}
	for _, c := range cases {
		got, ok := Wire(c.code, c.val, c.query)
		if ok != (c.want != "") || got != c.want {
			t.Errorf("Wire(%q, %d, query=%v) = (%q, %v), want (%q, %v)",
				c.code, c.val, c.query, got, ok, c.want, c.want != "")
		}
	}
}

// Every code lp10 has no reason to send is refused in both forms — the
// dangerous ones first, then the push-only codes (a "TIT;" query is not
// something lp10 needs), the device's own noise, and near-misses of allowed
// codes that must not slip through on case or padding.
func TestWireRefusesEverythingElse(t *testing.T) {
	for _, code := range []string{
		"WRS", "SYS", "DEF", "PMT", "COE", "STP", "PST", // act blind or reboot
		"TIT", "ART", "ALB", "VND", "RAW", "ELP", // pushed, never asked
		"", "vol", "pop", "Vol", " VOL", "VOL ", "VOL;", "VOL:1", "VOL;SYS", "SYS:RESET", "POP;WRS",
	} {
		for _, query := range []bool{true, false} {
			if got, ok := Wire(code, 1, query); ok || got != "" {
				t.Errorf("Wire(%q, 1, query=%v) = (%q, %v), want refused", code, query, got, ok)
			}
		}
	}
}

// The code classes Wire switches on must not overlap: a code in two of them
// would be sent in whichever form the switch happens to test first.
func TestWireClassesAreDisjoint(t *testing.T) {
	seen := map[string]string{}
	add := func(class, code string) {
		if prev, dup := seen[code]; dup {
			t.Errorf("%s is both %s and %s", code, prev, class)
		}
		seen[code] = class
	}
	for _, s := range Specs {
		add("an EQ control", s.Code)
	}
	for code := range playerSpecs {
		add("a player setting", code)
	}
	for code := range queryOnly {
		add("get-only", code)
	}
	for code := range actions {
		add("an action", code)
	}
}

func TestIsAction(t *testing.T) {
	for code, want := range map[string]bool{
		"POP": true, "NXT": true, "PRE": true,
		"VOL": false, "MUT": false, "STA": false, "PLA": false, "TIT": false, "MXV": false,
		"": false, "pop": false, "WRS": false,
	} {
		if got := IsAction(code); got != want {
			t.Errorf("IsAction(%q) = %v, want %v", code, got, want)
		}
	}
	if !IsAction(ToggleCode) || !IsAction(NextCode) || !IsAction(PrevCode) {
		t.Error("the exported action codes are not actions")
	}
}

// ---- what comes back --------------------------------------------------------

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

// One STA reply carries the source, the mute, the volume and the play state.
// Mute, volume and the play flag must all parse or the frame is dropped
// whole (never half-applied); the other fields are not read at all.
func TestParseStatus(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  *Status // nil = dropped
	}{
		{"live reply", "STA:NET,0,44,0,0,3,0,1,1,0;", &Status{Source: "NET", Vol: 44, Playing: true}},
		{"muted, paused", "STA:BT,1,0,0,0,3,0,0,1,0;", &Status{Source: "BT", Muted: true}},
		{"exactly 8 fields", "STA:NET,0,44,0,0,3,0,1;", &Status{Source: "NET", Vol: 44, Playing: true}},
		{"unread fields are not parsed", "STA:NET,0,44,x,x,x,x,1,x,x;", &Status{Source: "NET", Vol: 44, Playing: true}},
		{"spaces", "STA: LINE-IN , 1 , 44 ,0,0,3,0, 1 ,1,0;", &Status{Source: "LINE-IN", Muted: true, Vol: 44, Playing: true}},
		{"only 1 means on", "STA:NET,2,44,0,0,3,0,2,1,0;", &Status{Source: "NET", Vol: 44}},
		{"raw volume", "STA:NET,0,250,0,0,3,0,1,1,0;", &Status{Source: "NET", Vol: 250, Playing: true}},
		{"negative volume", "STA:NET,0,-3,0,0,3,0,1,1,0;", &Status{Source: "NET", Vol: -3, Playing: true}},
		{"empty source", "STA:,0,44,0,0,3,0,1,1,0;", &Status{Vol: 44, Playing: true}},
		{"source stripped", "STA:N\x1bE\u202eT\u0085,0,44,0,0,3,0,1,1,0;", &Status{Source: "NET", Vol: 44, Playing: true}},
		{"source bounded", "STA:" + strings.Repeat("A", 40) + ",0,44,0,0,3,0,1,1,0;",
			&Status{Source: strings.Repeat("A", 16), Vol: 44, Playing: true}},
		{"7 fields", "STA:NET,0,44,0,0,3,0;", nil},
		{"empty", "STA:;", nil},
		{"mute not a number", "STA:NET,x,44,0,0,3,0,1,1,0;", nil},
		{"volume not a number", "STA:NET,0,4x,0,0,3,0,1,1,0;", nil},
		{"volume empty", "STA:NET,0,,0,0,3,0,1,1,0;", nil},
		{"playing not a number", "STA:NET,0,44,0,0,3,0,yes,1,0;", nil},
		{"query echo", "STA;", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, rest := ParseFrames(c.frame)
			if rest != "" {
				t.Errorf("rest = %q", rest)
			}
			if c.want == nil {
				if len(out) != 0 {
					t.Errorf("ParseFrames(%q) = %+v, want dropped", c.frame, out)
				}
				return
			}
			if len(out) != 1 || out[0].Code != StatusCode || out[0].Status == nil {
				t.Fatalf("ParseFrames(%q) = %+v, want one STA update", c.frame, out)
			}
			if u := out[0]; *u.Status != *c.want || u.Val != 0 || u.Text != "" || u.Names != nil {
				t.Errorf("ParseFrames(%q) = %+v status %+v, want status %+v only", c.frame, u, *u.Status, *c.want)
			}
		})
	}
	// a dropped STA does not take its neighbours with it
	out, _ := ParseFrames("STA:NET,0;VOL:44;")
	if !reflect.DeepEqual(out, []Update{{Code: VolumeCode, Val: 44}}) {
		t.Errorf("neighbour of a short STA = %+v", out)
	}
}

func TestParsePlayerNumbers(t *testing.T) {
	out, rest := ParseFrames("VOL:44;MUT:0;PLA:1;VOL: 43 ;VOL:250;VOL:-1;")
	want := []Update{
		{Code: VolumeCode, Val: 44}, {Code: MuteCode, Val: 0}, {Code: PlayCode, Val: 1},
		{Code: VolumeCode, Val: 43}, {Code: VolumeCode, Val: 250}, {Code: VolumeCode, Val: -1},
	}
	if !reflect.DeepEqual(out, want) || rest != "" {
		t.Errorf("ParseFrames = %+v rest %q, want %+v", out, rest, want)
	}
	// non-numbers and our own query echoes are dropped
	if out, _ := ParseFrames("VOL:abc;MUT:;PLA:1.5;VOL:0x10;VOL;MUT;PLA;"); len(out) != 0 {
		t.Errorf("junk numbers produced %+v", out)
	}
}

// The text codes arrive as Text: control-stripped, trimmed, clipped to their
// rune bound. The value is everything after the FIRST colon, so a title
// holding a colon arrives whole.
func TestParseTextFrames(t *testing.T) {
	cases := []struct {
		frame string
		want  Update
	}{
		{"SRC:NET;", Update{Code: SourceCode, Text: "NET"}},
		{"SRC:USBPLAY;", Update{Code: SourceCode, Text: "USBPLAY"}},
		{"VER:29-1d316f0c-10;", Update{Code: VersionCode, Text: "29-1d316f0c-10"}},
		{"VND:spotify;", Update{Code: VendorCode, Text: "spotify"}},
		{"TIT:Big Bang;", Update{Code: TitleCode, Text: "Big Bang"}},
		{"ART:Usted Señalemelo;", Update{Code: ArtistCode, Text: "Usted Señalemelo"}},
		{"ALB:Big Bang;", Update{Code: AlbumCode, Text: "Big Bang"}},
		{"TIT:Live: At Wembley: 1986;", Update{Code: TitleCode, Text: "Live: At Wembley: 1986"}},
		{"TIT:  padded  ;", Update{Code: TitleCode, Text: "padded"}},
		{"TIT:\x1b[31mRed\x07\u009b\u202e!;", Update{Code: TitleCode, Text: "[31mRed!"}},
		{"ART:\u2066Isolated\u2069\u2028;", Update{Code: ArtistCode, Text: "Isolated"}},
		{"VND:spot\x00ify;", Update{Code: VendorCode, Text: "spotify"}},
		{"VER:29\r\n;", Update{Code: VersionCode, Text: "29"}},
	}
	for _, c := range cases {
		out, rest := ParseFrames(c.frame)
		if len(out) != 1 || !reflect.DeepEqual(out[0], c.want) || rest != "" {
			t.Errorf("ParseFrames(%q) = %+v rest %q, want %+v", c.frame, out, rest, c.want)
		}
	}
}

// Each text code has its own rune bound: the device's words are short, and a
// track field stops a hostile flood well past what the marquee scrolls. The
// bound counts what reaches the frame, so stripped bytes do not use it up,
// and a multi-byte rune counts once.
func TestParseTextBounds(t *testing.T) {
	bounds := map[string]int{
		SourceCode: 16, VersionCode: 32, VendorCode: 24,
		TitleCode: 200, ArtistCode: 200, AlbumCode: 200,
	}
	for code, n := range bounds {
		for _, unit := range []string{"x", "ñ", "\U0001f3b5"} {
			for _, k := range []int{n - 1, n, n + 1, n + 500} {
				out, _ := ParseFrames(code + ":" + strings.Repeat(unit, k) + ";")
				if len(out) != 1 {
					t.Fatalf("%s with %d×%q: %d updates", code, k, unit, len(out))
				}
				if got, want := utf8.RuneCountInString(out[0].Text), min(k, n); got != want {
					t.Errorf("%s with %d×%q kept %d runes, want %d", code, k, unit, got, want)
				}
			}
		}
		// controls between the runes do not count against the bound
		out, _ := ParseFrames(code + ":" + strings.Repeat("\x07a", n+10) + ";")
		if len(out) != 1 || out[0].Text != strings.Repeat("a", n) {
			t.Errorf("%s: stripped bytes used up the bound: %d runes", code, utf8.RuneCountInString(out[0].Text))
		}
	}
}

// A title holding a semicolon is cut there — the device's own framing, which
// no reader can undo — and the remainder, with no known code in front, is
// dropped without disturbing the frames after it.
func TestParseTitleWithSemicolon(t *testing.T) {
	out, rest := ParseFrames("TIT:Part 1; Part 2;ART:X;ALB:Y;")
	want := []Update{
		{Code: TitleCode, Text: "Part 1"},
		{Code: ArtistCode, Text: "X"},
		{Code: AlbumCode, Text: "Y"},
	}
	if !reflect.DeepEqual(out, want) || rest != "" {
		t.Errorf("ParseFrames = %+v rest %q, want %+v", out, rest, want)
	}
}

// The pushes verified live on MCU 29, as the box sends them.
func TestParseLivePushes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Update
	}{
		{"track change", "TIT:Big Bang;ART:Usted Señalemelo;ALB:Big Bang;", []Update{
			{Code: TitleCode, Text: "Big Bang"},
			{Code: ArtistCode, Text: "Usted Señalemelo"},
			{Code: AlbumCode, Text: "Big Bang"},
		}},
		{"resume", "PLA:1;VND:spotify;", []Update{{Code: PlayCode, Val: 1}, {Code: VendorCode, Text: "spotify"}}},
		{"pause", "PLA:0;", []Update{{Code: PlayCode, Val: 0}}},
		{"after NXT", "RAW:NEXT;", nil},
		{"set echoes", "VOL:43;MUT:1;", []Update{{Code: VolumeCode, Val: 43}, {Code: MuteCode, Val: 1}}},
		{"getters", "SRC:NET;VER:29-1d316f0c-10;", []Update{
			{Code: SourceCode, Text: "NET"}, {Code: VersionCode, Text: "29-1d316f0c-10"},
		}},
	}
	for _, c := range cases {
		out, rest := ParseFrames(c.in)
		if !reflect.DeepEqual(out, c.want) || rest != "" {
			t.Errorf("%s: ParseFrames(%q) = %+v rest %q, want %+v", c.name, c.in, out, rest, c.want)
		}
	}
}

// Unknown codes are dropped, whatever their value: the device's own noise
// (RAW after a skip, ELP should it ever come over TCP) and anything else.
// Codes are exact: a lowercase or mixed-case look-alike is unknown too.
func TestParseFramesDropsUnknownCodes(t *testing.T) {
	out, rest := ParseFrames("RAW:NEXT;XYZ:5;ELP:1234;WRS:1;tit:lower;Tit:x;vol:5;SYS:RESET;:empty;")
	if len(out) != 0 || rest != "" {
		t.Errorf("unknown codes produced %+v rest %q", out, rest)
	}
}

// The reader carries a partial frame into the next read: a frame split at ANY
// byte — inside a code, at the colon, inside a multi-byte rune — parses as if
// it had arrived whole.
func TestParseFramesSplitAnywhere(t *testing.T) {
	stream := "STA:NET,0,44,0,0,3,0,1,1,0;TIT:Big Bang;ART:Usted Señalemelo;ALB:Big Bang;" +
		"PLA:1;VND:spotify;RAW:NEXT;PEQ:0@Flat,1@Classical;VER:29-1d316f0c-10;BAS:-3;"
	whole, rest := ParseFrames(stream)
	if rest != "" || len(whole) != 9 {
		t.Fatalf("whole stream: %d updates rest %q", len(whole), rest)
	}
	for k := range len(stream) + 1 {
		o1, r1 := ParseFrames(stream[:k])
		o2, r2 := ParseFrames(r1 + stream[k:])
		if got := append(o1, o2...); !reflect.DeepEqual(got, whole) || r2 != "" {
			t.Fatalf("split at %d: %+v rest %q, want %+v", k, got, r2, whole)
		}
	}
	// the split inside "ñ" in particular: the first read ends mid-rune
	k := strings.Index(stream, "ñ") + 1
	_, r1 := ParseFrames(stream[:k])
	if utf8.ValidString(r1) {
		t.Fatalf("the split at %d did not cut the rune: %q", k, r1)
	}
	o2, _ := ParseFrames(r1 + stream[k:])
	if len(o2) == 0 || o2[0].Text != "Usted Señalemelo" {
		t.Errorf("the rune cut across reads arrived as %+v", o2)
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

// A preset label gets the strip every other device string gets
// (protocol.Printable, all of C* and Z* but the space): a bidi override, a line
// separator, a zero-width space or a soft hyphen never reaches the equalizer row.
func TestParsePresetsUseTheSharedStrip(t *testing.T) {
	for _, raw := range []string{"Fl\u202eat", "Po\u2028p", "Ja\u200bzz", "Ro\u00adck"} {
		names := parsePresets("0@" + raw)
		if len(names) != 1 {
			t.Fatalf("parsePresets(%q) = %q", raw, names)
		}
		if want := protocol.Printable(raw); names[0] != want {
			t.Errorf("preset %q kept %q, the shared strip gives %q", raw, names[0], want)
		}
		if strings.ContainsAny(names[0], "\u202e\u2028\u200b\u00ad") {
			t.Errorf("format/separator rune survived: %q", names[0])
		}
	}
	// the list's own "@" separator is dropped from inside a label
	if names := parsePresets("0@Fl@at"); len(names) != 1 || names[0] != "Flat" {
		t.Errorf(`parsePresets("0@Fl@at") = %q, want [Flat]`, names)
	}
}

// Every device text is trimmed (FuzzParseFrames' checkText). A ZWJ beside a
// space shielded the space from the trim; the final strip then dropped the
// edge ZWJ and exposed it: " \u200d Big Bang" became " Big Bang".
func TestZWJBesideASpaceLeavesNoEdgeSpace(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"TIT, leading", "TIT: \u200d Big Bang;"},
		{"SRC, trailing at the 16-rune clip", "SRC:" + strings.Repeat("A", 14) + " \u200dB;"},
		{"TIT, trailing at the 200-rune clip", "TIT:" + strings.Repeat("x", maxText-2) + " \u200dB;"},
	} {
		out, _ := ParseFrames(c.in)
		if len(out) != 1 {
			t.Fatalf("%s: updates = %+v", c.name, out)
		}
		if s := out[0].Text; s != strings.TrimSpace(s) {
			t.Errorf("%s: text %q is not trimmed", c.name, s[max(0, len(s)-20):])
		}
	}
	out, _ := ParseFrames("STA:" + strings.Repeat("A", 14) + " \u200dB,0,30,0,0,0,0,1,1,0;")
	if s := out[0].Status.Source; s != strings.TrimSpace(s) {
		t.Errorf("STA source = %q: not trimmed", s)
	}
	out, _ = ParseFrames("PEQ:0@Flat,1@ \u200d Pop;")
	if s := out[0].Names[1]; s != strings.TrimSpace(s) {
		t.Errorf("preset name = %q: not trimmed", s)
	}
}

// MXV's documented range starts at 30: a set below it stops at the floor on
// the wire (Set, Wire, ClampWrite), while the display range (Clamp) keeps 0
// so a cap another client set below it still shows. No other code has a
// floor: its write range is its display range.
func TestMXVWriteFloor(t *testing.T) {
	if got := Set("MXV", 10); got != "MXV:30;" {
		t.Errorf("Set MXV 10 = %q, want MXV:30;", got)
	}
	if got, ok := Wire("MXV", 0, false); !ok || got != "MXV:30;" {
		t.Errorf("Wire MXV 0 = (%q, %v), want (MXV:30;, true)", got, ok)
	}
	for _, c := range []struct {
		code             string
		in, write, shown int
	}{
		{"MXV", 25, 30, 25}, {"MXV", -5, 30, 0}, {"MXV", 30, 30, 30}, {"MXV", 250, 100, 100},
		{"BAS", -99, -10, -10}, {"VOL", -5, 0, 0}, {"ZZZ", 7, 7, 7},
	} {
		if got := ClampWrite(c.code, c.in); got != c.write {
			t.Errorf("ClampWrite(%q, %d) = %d, want %d", c.code, c.in, got, c.write)
		}
		if got := Clamp(c.code, c.in); got != c.shown {
			t.Errorf("Clamp(%q, %d) = %d, want %d", c.code, c.in, got, c.shown)
		}
	}
}
