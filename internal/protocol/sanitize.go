// Coercion and sanitization at the parse boundary: everything the device sends
// is whitelist-copied into known-typed fields, mirroring lp10lib's Python
// sanitizer semantics (str()/int() coercion, isprintable stripping).

package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Track is the typed, sanitized now-playing schema. JSON tags deliberately
// retain the device/cache wire names, including the legacy "PlayUrl",
// "CoverArtUrl", and spaced "Current Source" keys. Fields not represented here
// are dropped at the parse boundary.
//
// Repeat/Shuffle/Seek/Skip/Next/Prev and PlayState are retained even though the
// UI does not consume them yet: they document the supported wire contract and
// keep the typed boundary ready for future controls.
type Track struct {
	TrackName      string `json:"TrackName,omitempty"`
	Artist         string `json:"Artist,omitempty"`
	Album          string `json:"Album,omitempty"`
	PlaybackSource string `json:"PlaybackSource,omitempty"`
	PlayURL        string `json:"PlayUrl,omitempty"`
	MIME           string `json:"Mime,omitempty"`
	CoverArtURL    string `json:"CoverArtUrl,omitempty"`

	TotalTime     int `json:"TotalTime,omitempty"`
	CurrentSource int `json:"Current Source,omitempty"`
	SampleRate    int `json:"SampleRate,omitempty"`
	Repeat        int `json:"Repeat,omitempty"`
	Shuffle       int `json:"Shuffle,omitempty"`
	PlayState     int `json:"PlayState,omitempty"`
	ChannelCount  int `json:"ChannelCount,omitempty"`

	Seek bool `json:"Seek,omitempty"`
	Next bool `json:"Next,omitempty"`
	Prev bool `json:"Prev,omitempty"`
	Skip bool `json:"Skip,omitempty"`
}

// Empty reports whether t contains no sanitized track data.
func (t *Track) Empty() bool {
	return t == nil || *t == (Track{})
}

// Int coerces a value to an int the way protocol._int does: bool -> not an int,
// int/float truncate, NaN/Inf rejected, numeric strings parsed.
func Int(v any) (int, bool) {
	switch x := v.(type) {
	case bool:
		return 0, false
	case int:
		return x, true
	case int64:
		n := int(x)
		if int64(n) != x {
			return 0, false
		}
		return n, true
	case float64:
		return floatInt(x)
	case float32:
		return floatInt(float64(x))
	case json.Number: // from UseNumber decoding (int or float literal)
		// Try an integer parse first so large integers keep full int64
		// precision (Python's int is arbitrary precision); fall back to float
		// for non-integer literals, dropping NaN/Inf (e.g. 1e999).
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return Int(i)
		}
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return 0, false
		}
		return floatInt(f)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// floatInt truncates a finite float like Go/Python int conversion, but rejects
// values outside the native int range before converting. A direct conversion
// of an out-of-range float is implementation-specific and can wrap a huge
// duration/volume into MinInt. The power-of-two bound is exact in float64 even
// on 64-bit hosts (unlike float64(MaxInt), which rounds up to 2^63).
func floatInt(f float64) (int, bool) {
	limit := float64(uint64(1) << (strconv.IntSize - 1))
	if math.IsNaN(f) || math.IsInf(f, 0) || f < -limit || f >= limit {
		return 0, false
	}
	return int(f), true
}

// printable strips control/separator characters the way CPython's
// str.isprintable does: non-printable == category Other (C*) or Separator (Z*),
// except the ASCII space. Using the category test (rather than Go's
// unicode.IsPrint) keeps characters that are assigned in a newer Unicode version
// than Go's tables, matching Python more closely.
func printable(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == ' ' || !unicode.In(c, unicode.C, unicode.Z) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// SanitizeTrack whitelist-copies device track JSON into Track. It returns a
// possibly-empty Track for an object and nil when obj is not an object.
func SanitizeTrack(obj any) *Track {
	m, ok := obj.(map[string]any)
	if !ok {
		return nil
	}

	str := func(key string) string {
		v, present := m[key]
		if !present || v == nil {
			return ""
		}
		s, isStr := v.(string)
		if !isStr {
			s = pyStr(v)
		}
		return printable(s)
	}
	integer := func(key string) int {
		n, _ := Int(m[key])
		return n
	}
	boolean := func(key string) bool {
		b, _ := m[key].(bool)
		return b
	}

	return &Track{
		TrackName:      str("TrackName"),
		Artist:         str("Artist"),
		Album:          str("Album"),
		PlaybackSource: str("PlaybackSource"),
		PlayURL:        str("PlayUrl"),
		MIME:           str("Mime"),
		CoverArtURL:    str("CoverArtUrl"),
		TotalTime:      integer("TotalTime"),
		CurrentSource:  integer("Current Source"),
		SampleRate:     integer("SampleRate"),
		Repeat:         integer("Repeat"),
		Shuffle:        integer("Shuffle"),
		PlayState:      integer("PlayState"),
		ChannelCount:   integer("ChannelCount"),
		Seek:           boolean("Seek"),
		Next:           boolean("Next"),
		Prev:           boolean("Prev"),
		Skip:           boolean("Skip"),
	}
}

// SanitizeCached is the SanitizeTrack pass for a Track that was decoded straight
// into its typed fields rather than whitelist-copied — today the on-disk snapshot
// cache, which the program does not control at read time (it may be truncated,
// hand-edited, or written by another build). The typed decode already covers the
// str()/int() coercion SanitizeTrack does; only the printable() stripping is left
// to apply. It returns a sanitized copy, so the caller's value is untouched.
func SanitizeCached(t *Track) *Track {
	if t == nil {
		return nil
	}
	c := *t
	c.TrackName = printable(c.TrackName)
	c.Artist = printable(c.Artist)
	c.Album = printable(c.Album)
	c.PlaybackSource = printable(c.PlaybackSource)
	c.PlayURL = printable(c.PlayURL)
	c.MIME = printable(c.MIME)
	c.CoverArtURL = printable(c.CoverArtURL)
	return &c
}

// pyStr mirrors Python's str() for the non-string values that may land in a
// string field (Python str(True) == "True", str(1.5) == "1.5").
func pyStr(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case json.Number:
		return x.String()
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// parseJSON decodes a JSON document into a generic value, returning nil on any
// error (matching json.loads -> ValueError -> None at the call sites). It uses
// UseNumber so that out-of-range literals like 1e999 parse losslessly (Python's
// json.loads accepts them as inf); the conversion to int happens later in Int,
// which then drops them, matching the Python sanitizer.
func parseJSON(s string) any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	// json.Decoder accepts one valid value followed by another unless asked for
	// EOF explicitly. Python's json.loads (the behavior this ports) rejects
	// trailing non-whitespace, and accepting it here could turn a corrupt LUCI
	// payload into a seemingly-valid track/details update.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil
	}
	return v
}
