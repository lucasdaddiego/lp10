// Hostile-input fuzzing for the record pipeline: everything here arrives over
// the ssh stream from the device, which must be treated as untrusted (a
// compromised device or spoofed endpoint can send arbitrary bytes). Each
// target asserts no panic plus the cheap boundary invariants: whitelisted
// section tags, the per-section line cap, printable-only sanitized strings,
// and a State that stays usable after applying garbage.

package protocol

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/fixtures"
)

// lineFeeder turns a raw fuzz input into the nextLine func IterRecords consumes.
func lineFeeder(data string) func() (string, bool) {
	lines := strings.Split(data, "\n")
	i := 0
	return func() (string, bool) {
		if i >= len(lines) {
			return "", false
		}
		l := lines[i]
		i++
		return l, true
	}
}

func FuzzIterRecordsApply(f *testing.F) {
	for _, fx := range []string{
		"playing_record.txt", "playing_record_real.txt", "idle_record.txt",
		"heartbeat_record.txt", "dataless_record.txt", "device_record.txt",
		"config_record.txt",
	} {
		f.Add(fixtures.Get(fx))
	}
	f.Add("@@B\n@@B\n" + strings.Repeat("x\n", 250) + "@@E\n") // section flood
	f.Add("@@\n@@Z\njunk\n@@E\n@@E\n@@E\n")
	f.Add("@@s\n" + strings.Repeat("-1 ", 40) + "\n@@E\n@@s\n9e99 nan inf - - - - fw\n@@E\n")
	f.Add("@@i\nnet=\x00\xff\ndata=1 2 3\n=v\nk=\n@@E\n")
	f.Fuzz(func(t *testing.T, data string) {
		st := NewState()
		eCount := 0
		for _, l := range strings.Split(data, "\n") {
			l = strings.TrimRight(l, "\n")
			if strings.HasPrefix(l, "@@") && len(l) >= 3 && l[2] == 'E' {
				eCount++
			}
		}
		got := 0
		for rec := range IterRecords(lineFeeder(data)) {
			got++
			for k, lines := range rec {
				if len(k) != 1 || !tags[k[0]] {
					t.Fatalf("record carries non-whitelisted section %q", k)
				}
				if len(lines) > maxRecLines {
					t.Fatalf("section %q exceeds maxRecLines: %d lines", k, len(lines))
				}
				for _, ln := range lines {
					if ln == "" || strings.HasSuffix(ln, "\n") {
						t.Fatalf("section %q holds unframed line %q", k, ln)
					}
				}
			}
			// Same State across the input's records: a second @@s sample reaches
			// updateNet's rate/ring/counter paths with a prior sample present.
			ApplyRecord(st, rec)
		}
		if got != eCount {
			t.Fatalf("%d records from %d @@E terminators", got, eCount)
		}
		// The state must remain fully viewable after arbitrary garbage.
		snap := st.Snap()
		if snap.Vol < 0 || snap.Vol > 100 {
			t.Fatalf("volume out of range after apply: %d", snap.Vol)
		}
		if snap.Track != nil {
			checkPrintableTrack(t, snap.Track)
		}
		_ = st.DiagnosticView(time.Now())
	})
}

func checkPrintableTrack(t *testing.T, tr *Track) {
	t.Helper()
	for name, s := range map[string]string{
		"TrackName": tr.TrackName, "Artist": tr.Artist, "Album": tr.Album,
		"PlaybackSource": tr.PlaybackSource, "PlayURL": tr.PlayURL,
		"MIME": tr.MIME, "CoverArtURL": tr.CoverArtURL,
	} {
		if printable(s) != s {
			t.Fatalf("%s not printable-clean: %q", name, s)
		}
	}
}

func FuzzParseMB42(f *testing.F) {
	f.Add(fixtures.Get("playing_record.txt"))
	f.Add(`MID-Read:42 Data:{"Window CONTENTS":{"TrackName":"x","TotalTime":1}} Length:1`)
	f.Add(`MID-Read:42 Data:{"Window CONTENTS":{"TrackName":1e999,"TotalTime":"999999999999999999999"}} Length:1`)
	f.Add("MID-Read:42 Data:{\"Window CONTENTS\":{\"TrackName\":\" \u202e evil\",\"Artist\":true,\"Seek\":1}} Length:1")
	f.Add(`MID-Read:42 Data:{"Window CONTENTS":{}} Length:0`)
	f.Add(`MID-Read:42 Data:[1,2,3] Length:7`)
	f.Add(`MID-Read:42 Data:{} Length:2 {"x":1}`)
	f.Fuzz(func(t *testing.T, block string) {
		tr, idle := ParseMB42(block)
		if tr != nil && idle {
			t.Fatal("ParseMB42 returned both a track and idle")
		}
		if tr != nil {
			checkPrintableTrack(t, tr)
			// an idle-shaped payload must take the (nil, true) branch, never
			// surface as a live track
			if tr.TrackName == "" && tr.TotalTime <= 0 && tr.CurrentSource == 0 {
				t.Fatalf("idle-shaped payload returned as track: %+v", tr)
			}
		}
	})
}

func FuzzSanitizeCached(f *testing.F) {
	f.Add("Name", "Artist", "Album", "src", "http://u", "audio/ogg", "http://c")
	f.Add("\x00\x1b[31m", "a\u202eb", "\u202e\u2028", "\tx", " ", "\xff\xfe", "ok")
	f.Fuzz(func(t *testing.T, name, artist, album, src, purl, mime, cover string) {
		in := &Track{TrackName: name, Artist: artist, Album: album,
			PlaybackSource: src, PlayURL: purl, MIME: mime, CoverArtURL: cover}
		orig := *in
		out := SanitizeCached(in)
		if *in != orig {
			t.Fatal("SanitizeCached mutated its input")
		}
		checkPrintableTrack(t, out)
		// printable never grows a string beyond 3x: it only drops runes, except
		// that an invalid byte ranges as U+FFFD (kept, 3 bytes)
		if len(out.TrackName) > 3*len(name) || len(out.PlayURL) > 3*len(purl) {
			t.Fatal("sanitized string grew beyond the U+FFFD bound")
		}
		again := SanitizeCached(out)
		if *again != *out {
			t.Fatal("SanitizeCached not idempotent")
		}
	})
}

// FuzzSections drives the remaining section parsers directly: the positional
// @@s stats line, the @@i and @@c key=value blocks, and the raw register JSON
// of @@d / @@g.
func FuzzSections(f *testing.F) {
	f.Add("12345.6 0.44 0.36 0.42 139000 221064 2 AR241CE_9243.16 Linux 52000 1 2 - - 2.1 14.3 31.4 RUNNING 4834 44100 S16_LE 2 22050 1200000 2/237 - 0 0 256 0",
		"net=eth\nip=192.168.1.13\ndata=1258291 7340032",
		"spotify=on\nbt=off",
		`MID-Read:92 Data:{"macaddress":{"bt":"aa"},"serialnumber":{"device_serialnumber":9},"versioninfo":{"devicefwversion":null}} Length:1`,
		`MID-Read:39 Data:{"devices":[{},{}]} Length:1`)
	f.Add("9e999 -1 nan inf 0 0 0 fw", "k=v=w\n\x00=\x01\nname=\u202e", "spotify=\x00\nzzz=on",
		`MID-Read:92 Data:{"versioninfo":{"mcuversion":1e999}} Length:0`,
		`MID-Read:39 Data:{"devices":"not-a-list"} Length:0`)
	f.Fuzz(func(t *testing.T, sys, dev, conf, det, mroom string) {
		if si := parseSysInfo([]string{sys}); si != nil {
			for _, s := range []string{si.Up, si.Load, si.FW, si.Procs, si.DacFmt, si.PcmState} {
				if printable(s) != s {
					t.Fatalf("SysInfo field not printable-clean: %q", s)
				}
			}
		}
		if di := parseDevInfo(strings.Split(dev, "\n")); di != nil {
			for _, s := range []string{di.Net, di.IP, di.MAC, di.SSID, di.Name, di.DataUsed, di.DNS} {
				if printable(s) != s {
					t.Fatalf("DevInfo field not printable-clean: %q", s)
				}
			}
		}
		if ci := parseConfInfo(strings.Split(conf, "\n")); ci != nil {
			for k := range ci.Svc {
				if !confKeys[k] {
					t.Fatalf("non-whitelisted capability key %q", k)
				}
			}
		}
		if dd := parseDevDetails([]string{det}); dd != nil {
			for _, s := range []string{dd.Serial, dd.BTMAC, dd.MCU, dd.FW} {
				if printable(s) != s {
					t.Fatalf("DevDetails field not printable-clean: %q", s)
				}
			}
			if *dd == (DevDetails{}) {
				t.Fatal("parseDevDetails returned an all-empty struct instead of nil")
			}
		}
		if mr := parseMultiroom([]string{mroom}); mr != nil && mr.Devices < 0 {
			t.Fatalf("negative multiroom device count: %d", mr.Devices)
		}
	})
}
