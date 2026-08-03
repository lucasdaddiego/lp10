package protocol

// ParseMB42 turns a joined B-section into a sanitized Track, or signals idle.
// Returns (track, false) for a real track, (nil, true) for a definitive idle
// PlayView (clear the track now), and (nil, false) for unparseable garbage
// (debounce the clear). The register-read envelope is decoded by the shared
// regJSONStr; the "Window CONTENTS" shape check below is what actually
// discriminates a PlayView payload from any other register's JSON.
func ParseMB42(block string) (*Track, bool) {
	mp, ok := regJSONStr(block).(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := mp["Window CONTENTS"].(map[string]any)
	if !ok {
		return nil, false
	}
	t := SanitizeTrack(raw)
	name := t.TrackName
	total := t.TotalTime
	src := t.CurrentSource
	if name == "" && total <= 0 && src == 0 {
		return nil, true // definitive idle
	}
	return t, false
}
