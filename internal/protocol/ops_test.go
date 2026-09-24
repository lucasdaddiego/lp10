package protocol

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// report223 is a real MsgBox-223 line from the vendor app's log (2026-09-23),
// with the stamp and payload swapped in.
func report223(stamp, payload string) string {
	return "[" + stamp + "] [DEBUG] [luci-rx] normalized_kind=unknown normalized=None remote_id=0 command_type=2 command=223 command_status=0 crc=16846 data_length=9 payload=\"" + payload + "\""
}

// The @@o digest: the loop ships three tagged lines — the eSDK's reconnect
// count and the first timestamp of the live syslog (the window), and the vendor
// app's last MsgBox-223 report of the box's own manifest check. Each is
// optional; the syslog stamp's year is inferred.
func TestParseOpsDigest(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.Local)
	o := parseOps([]string{
		"n=88",
		"t=Sep 11 01:47:58",
		"u=" + report223("2026-09-12 14:56:15.624", "NO_UPDATE"),
	}, now)
	if o == nil {
		t.Fatal("digest dropped")
	}
	if !o.ReconnectsOK || o.Reconnects != 88 {
		t.Errorf("reconnects = %d/%v, want 88", o.Reconnects, o.ReconnectsOK)
	}
	if !o.LogSinceOK || o.LogSince != time.Date(2026, 9, 11, 1, 47, 58, 0, time.Local) {
		t.Errorf("log since = %v/%v", o.LogSince, o.LogSinceOK)
	}
	if !o.OTAOK || !o.OTAUpToDate || o.OTAText != "" {
		t.Errorf("ota = %+v, want up to date", o)
	}
	if o.OTAAt != time.Date(2026, 9, 12, 14, 56, 15, 0, time.Local) {
		t.Errorf("ota at = %v", o.OTAAt)
	}
}

func TestOTAWords(t *testing.T) {
	for in, want := range map[string]string{"NO_UPDATE": "no update", "UPDATE_AVAILABLE": "update available", "fail": "fail"} {
		if got := OTAWords(in); got != want {
			t.Errorf("OTAWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseOpsOfferedAndPartial(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.Local)
	// any other report is carried in words, not read as up to date
	o := parseOps([]string{"u=" + report223("2026-09-12 02:56:15.000", "UPDATE_AVAILABLE")}, now)
	if o == nil || !o.OTAOK || o.OTAUpToDate || o.OTAText != "update available" {
		t.Errorf("offered: %+v", o)
	}
	// a long payload is bounded
	o = parseOps([]string{"u=" + report223("2026-09-12 02:56:15.000", strings.Repeat("X", 200))}, now)
	if o == nil || !o.OTAOK || !strings.HasSuffix(o.OTAText, "…") || len(o.OTAText) > 70 {
		t.Errorf("long payload: %+v", o)
	}
	// a report without a readable stamp keeps its verdict, with no time
	o = parseOps([]string{`u=junk command=223 payload="NO_UPDATE"`}, now)
	if o == nil || !o.OTAOK || !o.OTAUpToDate || !o.OTAAt.IsZero() {
		t.Errorf("stampless: %+v", o)
	}
	// a line that is not a MsgBox-223 report — the old syslog answer, another
	// MsgBox, a report with no payload — is no verdict at all
	for _, u := range []string{
		"u=Sep 12 14:56:15:624239 E/ota[923]: ota: OTA:error string =  No update available",
		"u=[2026-09-12 02:56:15.000] [DEBUG] [luci-rx] command=64 command_status=0 payload=\"63\"",
		"u=[2026-09-12 02:56:15.000] [DEBUG] [luci-rx] command=223 command_status=0",
		`u=[2026-09-12 02:56:15.000] command=223 payload=""`,
		`u=[2026-09-12 02:56:15.000] command=223 payload="NO_UPDATE`,
	} {
		if o := parseOps([]string{u}, now); o != nil {
			t.Errorf("%q read as a verdict: %+v", u, o)
		}
	}
	// a box with no syslog answers three empty tags: nothing is known, and the
	// previous digest is kept (nil)
	if o := parseOps([]string{"n=", "t=", "u="}, now); o != nil {
		t.Errorf("empty digest should be nil, got %+v", o)
	}
	// junk lines never panic and never count
	if o := parseOps([]string{"garbage", "n=-3", "n=abc", "t=not a time"}, now); o != nil {
		t.Errorf("junk digest should be nil, got %+v", o)
	}
	// absent section
	if parseOps(nil, now) != nil {
		t.Error("absent section should be nil")
	}
}

// clip bounds a string by bytes but cuts back to a rune start, so a long
// multi-byte report is never split into invalid UTF-8 — which the digest's
// lower-casing would otherwise paint as U+FFFD.
func TestClipKeepsUTF8Valid(t *testing.T) {
	for _, s := range []string{strings.Repeat("é", 40), strings.Repeat("€", 30), strings.Repeat("😀", 20)} {
		for n := range len(s) {
			if got := clip(s, n); !utf8.ValidString(got) || len(got) > n+len("…") {
				t.Fatalf("clip(%q, %d) = %q", s, n, got)
			}
		}
	}
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)
	u := report223("2026-09-23 14:56:15.624", "x"+strings.Repeat("é", 100))
	if _, payload, ok := ParseOTAReport(u, time.Local); !ok || !utf8.ValidString(payload) || !strings.HasSuffix(payload, "…") {
		t.Errorf("payload = %q (ok %v), want it clipped and valid", payload, ok)
	}
	o := parseOps([]string{"u=" + u}, now)
	if o == nil || !utf8.ValidString(o.OTAText) || strings.ContainsRune(o.OTAText, utf8.RuneError) {
		t.Errorf("OTAText is not clean UTF-8: %+v", o)
	}
}

// The syslog stamp has no year: it takes now's, unless that would put it in the
// future — a log that began in late December read in early January.
func TestParseSyslogTimeYearRollover(t *testing.T) {
	now := time.Date(2027, 1, 2, 10, 0, 0, 0, time.Local)
	got, ok := parseSyslogTime("Dec 31 23:10:00", now)
	if !ok || got.Year() != 2026 {
		t.Errorf("Dec 31 read on Jan 2 = %v/%v, want 2026", got, ok)
	}
	got, ok = parseSyslogTime("Jan  2 09:00:00", now)
	if !ok || got.Year() != 2027 {
		t.Errorf("same day = %v/%v, want 2027", got, ok)
	}
	if _, ok := parseSyslogTime("", now); ok {
		t.Error("empty stamp parsed")
	}
	// a full syslog line is cut to its stamp
	if got, ok := parseSyslogTime("Sep 11 01:47:58:582904 I/x: y", now); !ok || got.Month() != time.September {
		t.Errorf("long line = %v/%v", got, ok)
	}
}

// @@i carries the kernel's reboot reason; only a bare lower-case word passes,
// so a cmdline without reboot_mode= (whose first token the loop hands over
// instead) reads as unknown.
func TestDevInfoRebootMode(t *testing.T) {
	di := parseDevInfo([]string{"net=eth", "rboot=cold_boot"})
	if di == nil || di.Reboot != "cold_boot" {
		t.Fatalf("reboot = %+v", di)
	}
	di = parseDevInfo([]string{"net=eth", "rboot=init=/init"})
	if di == nil || di.Reboot != "" {
		t.Errorf("junk reboot kept: %+v", di)
	}
}

// @@c's spotify.proc and dirty lines and their accessors.
func TestConfInfoEngineProcessAndDirty(t *testing.T) {
	ci := parseConfInfo([]string{
		"spotify.eng=spotifymusicpro", "spotify.cfg=pro",
		"spotify.proc=638794 1",
		"dirty=FriendlyName SpotifyEnabled SpotifyProEnabled TidalEnabled ",
	})
	if ci == nil {
		t.Fatal("conf dropped")
	}
	if age, ok := ci.EngineUptime(); !ok || age != 638794*time.Second {
		t.Errorf("uptime = %v/%v", age, ok)
	}
	if by, known := ci.EngineBySSH(); !known || !by {
		t.Errorf("by ssh = %v/%v, want true/true", by, known)
	}
	if d, known := ci.Dirty("SpotifyProEnabled"); !known || !d {
		t.Errorf("SpotifyProEnabled dirty = %v/%v", d, known)
	}
	if d, known := ci.Dirty("QobuzConnectEnabled"); !known || d {
		t.Errorf("QobuzConnectEnabled dirty = %v/%v, want false/true", d, known)
	}
	// an init-started engine: age known, ssh count 0
	ci = parseConfInfo([]string{"spotify.eng=newspotifyhifi", "spotify.proc=120 0"})
	if by, known := ci.EngineBySSH(); !known || by {
		t.Errorf("init-started = %v/%v, want false/true", by, known)
	}
	// no engine: the loop ships an empty value; nothing is known
	ci = parseConfInfo([]string{"spotify.eng=", "spotify.proc=", "dirty="})
	if _, ok := ci.EngineUptime(); ok {
		t.Error("uptime known with no engine")
	}
	if _, known := ci.EngineBySSH(); known {
		t.Error("ssh known with no engine")
	}
	if _, known := ci.Dirty("SpotifyEnabled"); known {
		t.Error("dirty known with an empty list")
	}
	// vocabulary: a spoofed value is dropped at the boundary
	ci = parseConfInfo([]string{"spotify.eng=x", "spotify.proc=12 1; rm -rf", "dirty=Key;evil"})
	if _, ok := ci.Svc["spotify.proc"]; ok {
		t.Error("junk spotify.proc kept")
	}
	if _, ok := ci.Svc["dirty"]; ok {
		t.Error("junk dirty kept")
	}
	// nil receiver
	var none *ConfInfo
	if _, ok := none.EngineUptime(); ok {
		t.Error("nil uptime")
	}
	if _, known := none.EngineBySSH(); known {
		t.Error("nil ssh")
	}
	if _, known := none.Dirty("x"); known {
		t.Error("nil dirty")
	}
}

// The digest lands in State and the diagnostic view.
func TestApplyRecordOpsReachesTheView(t *testing.T) {
	st := NewState()
	rec := Record{"o": {"n=3", "t=Sep 11 01:47:58", "u="}, "v": {"MID-Read:64 Data:40 Length:2"}}
	ApplyRecord(st, rec)
	d := st.DiagnosticView(time.Now())
	if d.Ops == nil || d.Ops.Reconnects != 3 || d.OpsAt.IsZero() {
		t.Fatalf("ops = %+v at %v", d.Ops, d.OpsAt)
	}
	// a later all-empty digest does not erase the last good one
	ApplyRecord(st, Record{"o": {"n=", "t=", "u="}})
	if d := st.DiagnosticView(time.Now()); d.Ops == nil || d.Ops.Reconnects != 3 {
		t.Errorf("digest erased by an empty one: %+v", d.Ops)
	}
}
