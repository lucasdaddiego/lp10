// Package tunnel speaks the device's plain-text control protocol: the
// LibreWireless "tcptunnelling" channel on TCP 2018, which relays the MCU's
// Arylic UART command set (https://developer.arylic.com/uartapi/) to the LAN.
// The MCU (an MVSilicon BP10xx) is the DAC and the audio DSP on this box —
// tone, EQ presets, virtual bass, balance, the output cap, the volume and the
// mute all live there — and since firmware AR241CP_8747 removed ssh, this
// socket is lp10's only channel to the box: the player rides it as well as the
// equalizer.
//
// Wire format: bare ASCII commands "CODE:VALUE;" (semicolon-terminated, no
// newline, no framing, no auth). Sending "CODE;" with no value is a QUERY for
// most codes — the device replies by broadcasting "CODE:VALUE;" to every
// connected client — but POP, NXT and PRE are ACTIONS (play/pause, next,
// previous), so Wire only ever sends them on a keypress. The device also
// pushes some frames on its own (verified live 2026-10-01 on MCU 29): TIT,
// ART and ALB when the track changes, PLA and VND when playback starts or
// stops, RAW after a skip. It pushes nothing while a track plays (no ELP over
// TCP), so the player has no position. The MCU answers ~100 codes; only the
// side-effect-free getters and the actions below are ever sent — several
// others act blind (WRS wifi-setup, SYS:RESET, DEF:SAV, PMT/COE reboot the
// box).
package tunnel

import (
	"strconv"
	"strings"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// Port is the device's control-tunnel TCP port.
const Port = 2018

// Kind distinguishes a ranged control (a slider), a 0/1 toggle, and a choice
// among named options (the EQ preset index, named by the device's PEQ list).
type Kind int

const (
	Ranged Kind = iota
	Toggle
	Choice
)

// Spec describes one control: its wire code, kind, and value bounds. Bounds are
// the UI's working range; the device clamps authoritatively and echoes the
// applied value back, so a slightly-off Max here only limits the slider, it
// can't push an invalid value (the readback corrects the display). Outbound
// writes are clamped to these bounds; inbound readbacks are NOT (ParseFrames
// returns the device's real value), so a value set out-of-range by another
// client displays truthfully instead of hiding a multi-step jump behind the
// next relative keypress. The display label is NOT here: the equalizer's
// column is narrow, so the UI owns its own short labels (tui.eqShort) and this
// stays a pure wire description.
type Spec struct {
	Code     string
	Kind     Kind
	Min, Max int
	Step     int
}

// Specs is the control set. Codes verified live on FW AR241CE_9243 / MCU 16
// against Arylic's UART API doc (2026-08-22), and re-verified unchanged on
// AR241CE_8530 / MCU 23 (2026-09-02): the MCU's command table and the PEQ
// preset list are byte-identical across the two mcu.bin images, and every
// getter below answered with the same shape on the live box.
//   - MXV  max-volume cap (30..100 per the doc; the slider keeps 0 so a
//     device-set low cap still displays).
//   - EQE  the EQ enable — whether the selected preset is applied at all.
//   - EQS  the EQ preset INDEX into the device's PEQ list (0@Flat,1@Classical,
//     2@Pop,3@Jazz,4@Rock,5@Vocal on this box). NOT an on/off switch: earlier
//     lp10 versions toggled it 0/1, which selected Classical for "on".
//   - BAS/MID/TRE tone, −10..+10 dB — always live, regardless of EQE.
//   - VBS/VBI virtual-bass switch and intensity.
//   - BAL  balance −100..+100 (positive favours the right channel).
//
// Tone bounds are conservative (device clamps).
var Specs = []Spec{
	{Code: "MXV", Kind: Ranged, Min: 0, Max: 100, Step: 5},
	{Code: "EQE", Kind: Toggle, Min: 0, Max: 1, Step: 1},
	{Code: "EQS", Kind: Choice, Min: 0, Max: MaxPresets - 1, Step: 1},
	{Code: "BAS", Kind: Ranged, Min: -10, Max: 10, Step: 1},
	{Code: "MID", Kind: Ranged, Min: -10, Max: 10, Step: 1},
	{Code: "TRE", Kind: Ranged, Min: -10, Max: 10, Step: 1},
	{Code: "VBS", Kind: Toggle, Min: 0, Max: 1, Step: 1},
	{Code: "VBI", Kind: Ranged, Min: 0, Max: 100, Step: 5},
	{Code: "BAL", Kind: Ranged, Min: -100, Max: 100, Step: 5},
}

// MaxPresets bounds the EQS index lp10 will send: the device's PEQ list names
// six on this firmware, and the MCU image carries ten custom slots beyond
// them. The device clamps authoritatively; this only bounds the selector.
const MaxPresets = 16

// PresetsCode is the query whose reply names the EQ presets
// ("PEQ:0@Flat,1@Classical,…"). It is read once at connect alongside the
// control seeds; it is never set.
const PresetsCode = "PEQ"

var specByCode = func() map[string]Spec {
	m := make(map[string]Spec, len(Specs))
	for _, s := range Specs {
		m[s.Code] = s
	}
	return m
}()

// Lookup returns the Spec for a wire code (false if unknown).
func Lookup(code string) (Spec, bool) {
	s, ok := specByCode[code]
	return s, ok
}

// Clamp constrains v to a known code's [Min,Max]; an unknown code passes through.
func Clamp(code string, v int) int {
	s, ok := specByCode[code]
	if !ok {
		return v
	}
	return max(s.Min, min(s.Max, v))
}

// Set is the wire string that assigns a value, e.g. Set("MXV", 100) == "MXV:100;".
// The value is clamped to the code's range first.
func Set(code string, v int) string {
	return code + ":" + strconv.Itoa(Clamp(code, v)) + ";"
}

// Query is the wire string that reads a value, e.g. Query("MXV") == "MXV;".
func Query(code string) string { return code + ";" }

// The player's codes. StatusCode is the one poll: its reply carries the
// source, the mute, the volume and the play state in one frame. VOL and MUT
// are both read and set; POP, NXT and PRE are actions with no value.
const (
	StatusCode  = "STA" // "STA:NET,0,44,0,0,3,0,1,1,0;" — see Status
	VolumeCode  = "VOL" // 0..100, the output level
	MuteCode    = "MUT" // 0/1, a real mute in the MCU (not volume 0)
	PlayCode    = "PLA" // 0/1, network playback running — pushed on a change
	SourceCode  = "SRC" // the input: NET, BT, LINE-IN, USBPLAY
	VersionCode = "VER" // the MCU firmware: "29-1d316f0c-10" (version-commit-apilevel)
	TitleCode   = "TIT" // pushed on a track change, plain UTF-8 over TCP
	ArtistCode  = "ART"
	AlbumCode   = "ALB"
	VendorCode  = "VND" // the service playing ("spotify"), pushed with PLA:1
	ToggleCode  = "POP" // action: play/pause
	NextCode    = "NXT" // action: next track
	PrevCode    = "PRE" // action: previous track (Spotify restarts the track first)
)

// SeedQueries returns one query per known control plus the preset-name list,
// the MCU version and the player status, for reading current values on
// connect.
func SeedQueries() []string {
	out := make([]string, 0, len(Specs)+3)
	out = append(out, Query(StatusCode))
	for _, s := range Specs {
		out = append(out, Query(s.Code))
	}
	return append(out, Query(PresetsCode), Query(VersionCode))
}

// playerSpecs are the player's settable codes, kept out of Specs so the
// equalizer never lists them.
var playerSpecs = map[string]Spec{
	VolumeCode: {Code: VolumeCode, Kind: Ranged, Min: 0, Max: 100, Step: 1},
	MuteCode:   {Code: MuteCode, Kind: Toggle, Min: 0, Max: 1, Step: 1},
}

// queryOnly are the codes lp10 reads but never sets.
var queryOnly = map[string]bool{StatusCode: true, PlayCode: true, SourceCode: true, VersionCode: true, PresetsCode: true}

// actions are the codes that act when sent bare — never queries.
var actions = map[string]bool{ToggleCode: true, NextCode: true, PrevCode: true}

// Wire is the one allowlist for what goes on the socket: the frame for code
// with val (query true reads the code instead of setting it), or false when
// lp10 never sends that code in that form. An action takes no value and no
// query; a get-only code takes only a query; a control or a player setting
// takes both, and a set is clamped to its range first.
func Wire(code string, val int, query bool) (string, bool) {
	switch {
	case actions[code]:
		if query {
			return "", false
		}
		return code + ";", true
	case queryOnly[code]:
		if !query {
			return "", false
		}
		return Query(code), true
	}
	sp, ok := specByCode[code]
	if !ok {
		sp, ok = playerSpecs[code]
	}
	if !ok {
		return "", false
	}
	if query {
		return Query(code), true
	}
	return code + ":" + strconv.Itoa(max(sp.Min, min(sp.Max, val))) + ";", true
}

// IsAction reports whether code is one of the bare actions (POP, NXT, PRE).
func IsAction(code string) bool { return actions[code] }

// Status is one parsed STA reply: "source,mute,volume,treble,bass,net,
// internet,playing,led,upgrading" per the UART API. Only the fields the
// player shows are kept; the tone comes from its own codes.
type Status struct {
	Source  string
	Muted   bool
	Vol     int
	Playing bool
}

// Update is one parsed "CODE:VALUE" from the device: a numeric control or
// player value (Val), the preset names by index (Names, PresetsCode only), a
// text value (Text: the source, the version, a track field, the vendor), or a
// status reply (Status, StatusCode only).
type Update struct {
	Code   string
	Val    int
	Names  []string
	Text   string
	Status *Status
}

// numeric are the player codes whose value is a number.
var numeric = map[string]bool{VolumeCode: true, MuteCode: true, PlayCode: true}

// textCodes are the player codes whose value is text, each with its length
// bound: a track field is clipped for the frame, the others are short words.
var textCodes = map[string]int{
	SourceCode: 16, VersionCode: 32, VendorCode: 24,
	TitleCode: maxText, ArtistCode: maxText, AlbumCode: maxText,
}

// maxText bounds one track field. The now-playing column is far narrower;
// the marquee scrolls what is longer, so this only stops a hostile flood.
const maxText = 200

// ParseFrames consumes every complete ';'-terminated frame from buf and returns
// the recognized updates plus any trailing partial frame (carry it into the next
// read). Frames that are unknown codes, valueless, or non-numeric are skipped —
// a malformed burst can never panic or desync the stream.
func ParseFrames(buf string) (out []Update, rest string) {
	for {
		i := strings.IndexByte(buf, ';')
		if i < 0 {
			return out, buf // no terminator yet: keep the partial
		}
		frame := buf[:i]
		buf = buf[i+1:]
		if u, ok := parseFrame(frame); ok {
			out = append(out, u)
		}
	}
}

// parseFrame reads one frame. The value is everything after the FIRST colon,
// so a title holding a colon arrives whole; a title holding a semicolon is cut
// there — the device's own framing, which no reader can undo — and its
// remainder, with no known code in front, is dropped.
func parseFrame(frame string) (Update, bool) {
	code, after, ok0 := strings.Cut(frame, ":")
	if !ok0 {
		return Update{}, false // bare "CODE" (our own query echo) — ignore
	}
	switch {
	case code == PresetsCode:
		names := parsePresets(after)
		if names == nil {
			return Update{}, false
		}
		return Update{Code: code, Names: names}, true
	case code == StatusCode:
		st, ok := parseStatus(after)
		if !ok {
			return Update{}, false
		}
		return Update{Code: code, Status: &st}, true
	case textCodes[code] > 0:
		return Update{Code: code, Text: cleanText(after, textCodes[code])}, true
	}
	if _, known := specByCode[code]; !known && !numeric[code] {
		return Update{}, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(after))
	if err != nil {
		return Update{}, false
	}
	return Update{Code: code, Val: n}, true // raw: the readback must report what the device holds
}

// parseStatus reads a STA value. The source is control-stripped and bounded
// like any device word; mute, volume and the play flag must parse, or the
// whole frame is dropped rather than half-applied.
func parseStatus(v string) (Status, bool) {
	f := strings.Split(v, ",")
	if len(f) < 8 {
		return Status{}, false
	}
	mute, err1 := strconv.Atoi(strings.TrimSpace(f[1]))
	vol, err2 := strconv.Atoi(strings.TrimSpace(f[2]))
	playing, err3 := strconv.Atoi(strings.TrimSpace(f[7]))
	if err1 != nil || err2 != nil || err3 != nil {
		return Status{}, false
	}
	return Status{Source: cleanText(f[0], textCodes[SourceCode]), Muted: mute == 1, Vol: vol, Playing: playing == 1}, true
}

// cleanText reduces a device text value to printable runes, clipped to n
// runes: every one of them reaches the frame. The clipped, trimmed result is
// stripped once more: the clip or the trim can leave a zero-width joiner at
// an edge, which Printable keeps only between two runes.
func cleanText(s string, n int) string {
	var b strings.Builder
	i := 0
	for _, r := range protocol.Printable(s) {
		if i >= n {
			break
		}
		b.WriteRune(r)
		i++
	}
	return protocol.Printable(strings.TrimSpace(b.String()))
}

// maxPresetName bounds one preset label so a hostile reply can't widen the
// equalizer row past the frame; the device's own names are single words.
const maxPresetName = 16

// parsePresets decodes a PEQ list "0@Flat,1@Classical,…" into names by index
// (gaps stay ""; the list is bounded by MaxPresets). nil when nothing parses.
// Names are clipped and reduced to printable runes: they are rendered verbatim
// in the equalizer row.
func parsePresets(list string) []string {
	var names []string
	for item := range strings.SplitSeq(list, ",") {
		idxS, name, ok := strings.Cut(item, "@")
		if !ok {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(idxS))
		if err != nil || idx < 0 || idx >= MaxPresets {
			continue
		}
		name = cleanName(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		for len(names) <= idx {
			names = append(names, "")
		}
		names[idx] = name
	}
	return names
}

// cleanName reduces a preset label to printable runes, clipped to
// maxPresetName runes. The strip is protocol.Printable, the rule every other
// device string gets: a C0/C1-only filter let a bidi override, a line
// separator or a zero-width space through to the equalizer row.
//
// The result is stripped once more for the zero-width joiner a clip or the
// trim can leave at an edge.
func cleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range protocol.Printable(s) {
		if n >= maxPresetName {
			break
		}
		if r == ';' || r == ',' || r == '@' {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return protocol.Printable(strings.TrimSpace(b.String()))
}
