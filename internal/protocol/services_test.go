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
		"airplay=on", "airplay.env=on", // .env not in the allowlist: dropped
		"dlna=on", "dlna.env=off", // likewise — its init script never reads it
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
	if got, want := ci.Env("tidal"), "on"; got != want {
		t.Errorf("Env(tidal) = %q, want %q", got, want)
	}
	// Configured on while nothing runs: a divergence worth flagging, because this
	// is a flag the service actually consults.
	if !ci.Divergent("tidal") {
		t.Error("Divergent(tidal) = false, want true")
	}
	// A flag whose init script never reads it is not carried at all, so it can
	// never be reported as a divergence — AirPlay and DLNA are in that position,
	// and marking whichever one happened to disagree read as a fault when nothing
	// was wrong. Unreadable and agreeing flags stay quiet for the same reason.
	for _, id := range []string{"airplay", "dlna", "bt", "cast", "qobuz", "usb"} {
		if ci.Env(id) != "" && id != "qobuz" {
			t.Errorf("Env(%s) = %q, want it not carried at all", id, ci.Env(id))
		}
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
	// MID 93 is a bare fetch naming the source (1 syslog, 2 the vendor app's
	// log): the severity filter is a laptop-side view.
	for _, d := range []string{"1", "2"} {
		if !ValidatePayload(93, d) {
			t.Errorf("ValidatePayload(93, %q) = false, want true", d)
		}
	}
	for _, d := range []string{"0", "3", "", "x", "12"} {
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
	lines, ok := parseLogs(Record{"l": {" Aug 25 15:28 I/x: hi", " @@E not a header"}}, "l")
	if !ok {
		t.Fatal("parseLogs reported no section")
	}
	if want := []string{"Aug 25 15:28 I/x: hi", "@@E not a header"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("parseLogs = %q, want %q", lines, want)
	}
	if l, ok := parseLogs(Record{"l": {}}, "l"); !ok || len(l) != 0 {
		t.Errorf("empty section = (%q, %v), want ([], true)", l, ok)
	}
	if _, ok := parseLogs(Record{}, "l"); ok {
		t.Error("absent section reported present")
	}
}

// A log tail reaches State only through an @@l section, and replaces the last
// one wholesale.
func TestStateLogView(t *testing.T) {
	st := NewState()
	if lines, at := st.LogView(LogSyslog); lines != nil || !at.IsZero() {
		t.Error("fresh State already holds a log tail")
	}
	ApplyRecord(st, Record{"l": {" one", " two"}})
	lines, at := st.LogView(LogSyslog)
	if len(lines) != 2 || at.IsZero() {
		t.Fatalf("LogView = (%q, %v)", lines, at)
	}
	ApplyRecord(st, Record{"l": {" three"}})
	if lines, _ := st.LogView(LogSyslog); len(lines) != 1 || lines[0] != "three" {
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

// The vendor app version rides @@i as vapp=. The loop cuts it out of
// /lsync/app-0.json with no shell-side guard, so a manifest without a
// "version" key hands over some other quoted token — only a version-shaped
// value may reach DevInfo.
func TestDevInfoVendorApp(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"32", "32"},
		{"1.4.2-rc1", "1.4.2-rc1"},
		{"", ""},
		{"rakoit_app", ""},             // the "name" value of a version-less manifest
		{"9aa7f360179db64ab10853", ""}, // an md5 is too long to be a version
		{"32; rm -rf", ""},
	} {
		st := NewState()
		for _, rec := range recordsFrom(splitLines("@@i\nnet=eth\nvapp=" + c.in + "\n@@E\n")) {
			ApplyRecord(st, rec)
		}
		di := st.DiagnosticView(time.Now()).DevInfo
		if di == nil || di.VendorApp != c.want {
			t.Errorf("vapp=%q -> %+v, want VendorApp %q", c.in, di, c.want)
		}
	}
}

// The two log tails are independent answers: an @@L never disturbs the held
// syslog tail and vice versa, and each is read back through its own source.
func TestStateTwoLogSources(t *testing.T) {
	st := NewState()
	ApplyRecord(st, Record{"l": {" Aug 25 15:28 I/x: sys"}})
	ApplyRecord(st, Record{"L": {" [2026-09-02 00:06:26.775] [DEBUG] [luci-rx] MB#112"}})
	if lines, at := st.LogView(LogSyslog); at.IsZero() || len(lines) != 1 || lines[0] != "Aug 25 15:28 I/x: sys" {
		t.Errorf("syslog tail = %q at %v", lines, at)
	}
	if lines, at := st.LogView(LogVendor); at.IsZero() || len(lines) != 1 || lines[0] != "[2026-09-02 00:06:26.775] [DEBUG] [luci-rx] MB#112" {
		t.Errorf("vendor tail = %q at %v", lines, at)
	}
	ApplyRecord(st, Record{"L": {}})
	if lines, _ := st.LogView(LogVendor); len(lines) != 0 {
		t.Errorf("an empty vendor answer must replace the tail, got %q", lines)
	}
	if lines, _ := st.LogView(LogSyslog); len(lines) != 1 {
		t.Errorf("the vendor answer disturbed the syslog tail: %q", lines)
	}
}

// The firmware-check request is a one-shot flag the overlay raises and the
// worker takes, carrying the build to ask about: the ssh stream's reg-5 build
// first, the LSSDP answer's as the fallback, "" before either has arrived.
func TestStateOTARequest(t *testing.T) {
	st := NewState()
	if b, pending := st.TakeOTARequest(); pending || b != "" {
		t.Errorf("nothing requested yet: %q %v", b, pending)
	}
	st.RequestOTA()
	if !st.OTAPending() || !st.DiagnosticView(time.Now()).OTAPending {
		t.Error("request not visible as pending")
	}
	if b, pending := st.TakeOTARequest(); !pending || b != "" {
		t.Errorf("no firmware known: %q %v", b, pending)
	}
	if st.OTAPending() {
		t.Error("taking the request must clear it")
	}
	st.SetLSSDP(&LSSDPInfo{FW: "AR241CE_8530.23.2"})
	st.RequestOTA()
	if b, _ := st.TakeOTARequest(); b != "AR241CE_8530" {
		t.Errorf("LSSDP fallback build = %q", b)
	}
	ApplyRecord(st, Record{"s": {"100 0.5 0.4 0.3 137000 215000 2 AR241CE_9243.16 Linux-5.15.137"}})
	st.RequestOTA()
	if b, _ := st.TakeOTARequest(); b != "AR241CE_9243" {
		t.Errorf("stream build should win = %q", b)
	}
	if firmwareBuild("nodots") != "nodots" {
		t.Error("a dotless string is its own build")
	}
	st.SetOTA(OTAInfo{At: time.Now(), Asked: "AR241CE_9243", Offered: "AR241CE_8530\x1b[31m", Err: "no\x07"})
	if v := st.DiagnosticView(time.Now()).OTA; v == nil || v.Offered != "AR241CE_8530[31m" || v.Err != "no" {
		t.Errorf("verdict not stored control-stripped: %+v", v)
	}
}
