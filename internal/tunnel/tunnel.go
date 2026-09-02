// Package tunnel speaks the device's plain-text audio-control protocol: the
// LibreWireless "tcptunnelling" channel on TCP 2018, which relays the MCU's
// Arylic UART command set (https://developer.arylic.com/uartapi/) to the LAN.
// The MCU (an MVSilicon BP10xx) is the DAC and the audio DSP on this box —
// tone, EQ presets, virtual bass, balance, and the output cap all live there —
// so this socket is the whole audio-settings surface. It is intentionally
// separate from the SSH player stream (transport/workers): playback rides one
// ssh connection; these device-config knobs ride this socket.
//
// Wire format: bare ASCII commands "CODE:VALUE;" (semicolon-terminated, no
// newline, no framing, no auth). Sending "CODE;" with no value is a QUERY — the
// device replies by broadcasting "CODE:VALUE;" to every connected client. A set
// from a network client reaches the MCU (verified write-through); the same
// string injected locally via `LUCI_local 112` does NOT, so this socket is the
// only way to drive these settings. The MCU answers ~450 codes; only the
// documented, side-effect-free ones below are used — several others are
// actions (WRS wifi-setup, SYS:RESET, DEF:SAV, PMT/COE reboot the box) and
// must never be sent blind.
package tunnel

import (
	"strconv"
	"strings"
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

// SeedQueries returns one query per known control plus the preset-name list,
// for reading current values on connect.
func SeedQueries() []string {
	out := make([]string, 0, len(Specs)+1)
	for _, s := range Specs {
		out = append(out, Query(s.Code))
	}
	return append(out, Query(PresetsCode))
}

// Update is one parsed "CODE:VALUE" from the device: a numeric control value,
// or — for PresetsCode only — the preset names by index (Names non-nil).
type Update struct {
	Code  string
	Val   int
	Names []string
}

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

func parseFrame(frame string) (Update, bool) {
	before, after, ok0 := strings.Cut(frame, ":")
	if !ok0 {
		return Update{}, false // bare "CODE" (our own query echo) — ignore
	}
	code := before
	if code == PresetsCode {
		names := parsePresets(after)
		if names == nil {
			return Update{}, false
		}
		return Update{Code: code, Names: names}, true
	}
	if _, known := specByCode[code]; !known {
		return Update{}, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(after))
	if err != nil {
		return Update{}, false
	}
	return Update{Code: code, Val: n}, true // raw: the readback must report what the device holds
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

// cleanName keeps printable, non-space-run runes of a preset label, clipped to
// maxPresetName runes.
func cleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= maxPresetName {
			break
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ';' || r == ',' || r == '@' {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
