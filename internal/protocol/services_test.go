package protocol

import (
	"reflect"
	"testing"
	"time"
)

// The @@c block reports two independent things per service — what is RUNNING and
// what is CONFIGURED — because on this device they diverge, and the divergence is
// the whole point of the readout.
func TestParseConfInfoRunningAndConfigured(t *testing.T) {
	ci := parseConfInfo([]string{
		"spotify.eng=spotifymusicpro",
		"spotify.sdk=3.211.130-g110e3e03",
		"spotify.cfg=pro",
		"airplay=on", "airplay.env=on",
		"dlna=on", "dlna.env=off",
		"tidal=off", "tidal.env=on",
		"qobuz=off", "qobuz.env=",
		"usb=off",
		"telnet=on", "adb=off", "web=on", "control=on",
		"bogus=on", // outside the allowlist: dropped at the boundary
	})
	if ci == nil {
		t.Fatal("parseConfInfo returned nil for a good block")
	}
	if _, ok := ci.Svc["bogus"]; ok {
		t.Error("non-allowlisted key survived the parse")
	}
	// spotify's running state is derived from .eng — the loop cannot afford a
	// second wire field for it (dropbear's command-length ceiling).
	if got := ci.Svc["spotify"]; got != "on" {
		t.Errorf("derived spotify = %q, want on", got)
	}
	if got, want := ci.Engine(), "spotifymusicpro"; got != want {
		t.Errorf("Engine() = %q, want %q", got, want)
	}
	if got, want := ci.SDK(), "3.211.130-g110e3e03"; got != want {
		t.Errorf("SDK() = %q, want %q", got, want)
	}
	if got, want := ci.Cfg(), "pro"; got != want {
		t.Errorf("Cfg() = %q, want %q", got, want)
	}
	if got, want := ci.Env("dlna"), "off"; got != want {
		t.Errorf("Env(dlna) = %q, want %q", got, want)
	}
	// running while configured off, and configured on while not running: both are
	// divergences worth flagging.
	for _, id := range []string{"dlna", "tidal"} {
		if !ci.Divergent(id) {
			t.Errorf("Divergent(%s) = false, want true", id)
		}
	}
	// agreeing, and unreadable, must both stay quiet.
	for _, id := range []string{"airplay", "qobuz", "usb"} {
		if ci.Divergent(id) {
			t.Errorf("Divergent(%s) = true, want false", id)
		}
	}
}

// An engine line with no engine means Spotify is simply not running — still a
// definite answer, not an unknown.
func TestParseConfInfoNoEngineIsOff(t *testing.T) {
	ci := parseConfInfo([]string{"spotify.eng=", "spotify.cfg=both"})
	if got := ci.Svc["spotify"]; got != "off" {
		t.Errorf("spotify = %q, want off", got)
	}
	if got := ci.Cfg(); got != "both" {
		t.Errorf("Cfg() = %q, want both", got)
	}
}

// A nil ConfInfo is the pre-connect state; every accessor must answer it rather
// than panic, so a view can render before the first block lands.
func TestConfInfoNilAccessors(t *testing.T) {
	var ci *ConfInfo
	if ci.Env("spotify") != "" || ci.Engine() != "" || ci.SDK() != "" || ci.Cfg() != "" || ci.Divergent("spotify") {
		t.Error("nil ConfInfo did not answer empty")
	}
}

func TestValidatePayloadServiceToggle(t *testing.T) {
	ok := []string{"spotify off", "spotify hifi", "spotify pro",
		"airplay 0", "airplay 1", "dlna 1", "tidal 0", "qobuz 1", "usb 0"}
	for _, d := range ok {
		if !ValidatePayload(92, d) {
			t.Errorf("ValidatePayload(92, %q) = false, want true", d)
		}
	}
	bad := []string{
		"spotify 1",     // Spotify takes an engine, not a boolean
		"airplay hifi",  // and the others take only a boolean
		"bt 0",          // Bluetooth is the remote's transport: never offered
		"cast 1",        // gated in /etc/libre_ConfigureENV, unreachable by setenv
		"spotify",       // no state
		"spotify  hifi", // the split is a single space
		"", "usb 2", "roon 1", "spotify hifi; reboot",
	}
	for _, d := range bad {
		if ValidatePayload(92, d) {
			t.Errorf("ValidatePayload(92, %q) = true, want false", d)
		}
	}
	// MID 93 is a bare fetch: the severity filter is a laptop-side view.
	if !ValidatePayload(93, "1") {
		t.Error("ValidatePayload(93, 1) = false, want true")
	}
	for _, d := range []string{"0", "2", "", "x"} {
		if ValidatePayload(93, d) {
			t.Errorf("ValidatePayload(93, %q) = true, want false", d)
		}
	}
}

// Two services toggled in one batch are two intents and must both survive; the
// same service toggled twice should reach the device once, at its last value.
func TestReduceCommandsServiceTogglesCollapsePerService(t *testing.T) {
	now := time.Now()
	got := ReduceCommands([]Command{
		{Mid: 92, Data: "spotify hifi", TS: now},
		{Mid: 92, Data: "tidal 1", TS: now},
		{Mid: 92, Data: "spotify pro", TS: now},
		{Mid: 93, Data: "1", TS: now},
		{Mid: 93, Data: "1", TS: now},
	})
	want := []Command{
		{Mid: 92, Data: "tidal 1", TS: now},
		{Mid: 92, Data: "spotify pro", TS: now},
		{Mid: 93, Data: "1", TS: now},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReduceCommands =\n %+v\nwant\n %+v", got, want)
	}
}

// The device prefixes each log line with a space so a line that itself begins
// "@@" cannot be read as a section header; that space comes back off here. A
// present-but-empty section means the tail matched nothing, which is an answer
// and must not read as "never asked".
func TestParseLogs(t *testing.T) {
	lines, ok := parseLogs(Record{"l": {" Aug 25 15:28 I/x: hi", " @@E not a header"}})
	if !ok {
		t.Fatal("parseLogs reported no section")
	}
	if want := []string{"Aug 25 15:28 I/x: hi", "@@E not a header"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("parseLogs = %q, want %q", lines, want)
	}
	if l, ok := parseLogs(Record{"l": {}}); !ok || len(l) != 0 {
		t.Errorf("empty section = (%q, %v), want ([], true)", l, ok)
	}
	if _, ok := parseLogs(Record{}); ok {
		t.Error("absent section reported present")
	}
}

// A log tail reaches State only through an @@l section, and replaces the last
// one wholesale.
func TestStateLogView(t *testing.T) {
	st := NewState()
	if lines, at := st.LogView(); lines != nil || !at.IsZero() {
		t.Error("fresh State already holds a log tail")
	}
	ApplyRecord(st, Record{"l": {" one", " two"}})
	lines, at := st.LogView()
	if len(lines) != 2 || at.IsZero() {
		t.Fatalf("LogView = (%q, %v)", lines, at)
	}
	ApplyRecord(st, Record{"l": {" three"}})
	if lines, _ := st.LogView(); len(lines) != 1 || lines[0] != "three" {
		t.Errorf("second tail did not replace the first: %q", lines)
	}
}

// A non-92 command sitting between two toggles of the same service must not be
// mistaken for one of them.
func TestReduceCommandsServiceToggleSkipsOtherMids(t *testing.T) {
	now := time.Now()
	got := ReduceCommands([]Command{
		{Mid: 40, Data: "NEXT", TS: now},
		{Mid: 92, Data: "tidal 1", TS: now},
		{Mid: 64, Data: "30", TS: now},
		{Mid: 92, Data: "tidal 0", TS: now},
	})
	want := []Command{
		{Mid: 40, Data: "NEXT", TS: now},
		{Mid: 64, Data: "30", TS: now},
		{Mid: 92, Data: "tidal 0", TS: now},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReduceCommands =\n %+v\nwant\n %+v", got, want)
	}
}
