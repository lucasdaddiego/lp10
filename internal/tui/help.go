// The help view (?): every key, grouped by the view it belongs to. A reference
// page, not a tutorial — the footers carry the two or three keys each view
// needs most, this carries all of them.

package tui

// helpGroups is the help page: a title and its key lines, in reading order.
var helpGroups = []struct {
	title string
	keys  [][2]string // key, meaning
}{
	{"views", [][2]string{
		{"1 … 5", "player · equalizer · services · logs · diagnostics"},
		{"tab", "next view"},
		{"esc", "back to the player (q does the same, then quits from the player)"},
		{"?", "this page · e c l i also open their view, and close it again"},
	}},
	{"player", [][2]string{
		{"space", "play / pause"},
		{"n · p", "next · previous track"},
		{"↑↓ · + −", "volume"},
		{"m", "mute (the level comes back on unmute)"},
		{"←→ enter", "pick a transport button and press it"},
		{"t", "right-hand time: remaining ⇄ total"},
		{"s · S", "sleep timer: 15 → 30 → 45 → 60 → 90 min, then off · S cancels"},
		{"b", "bedtime: one sleep step plus night mode, both restored when it ends"},
		{"d", "night mode (the device's multi-band compressor)"},
	}},
	{"equalizer", [][2]string{
		{"↑↓", "select a control"},
		{"←→", "adjust it — the device echoes the value it applied"},
		{"enter", "toggle a switch · step the preset"},
	}},
	{"services", [][2]string{
		{"↑↓ enter", "select a service · switch it (Spotify cycles off → HiFi → Pro)"},
		{"←→", "page the read-out under the rows, when it is taller than the terminal"},
	}},
	{"logs", [][2]string{
		{"↑↓ · ←→", "scroll · page"},
		{"s · f · r", "source (device syslog / vendor app) · filter · refresh"},
		{"F", "follow: refetch every 10 s while the view is open"},
	}},
	{"diagnostics", [][2]string{
		{"↑↓ · ←→", "scroll · page, when the read-out is taller than the terminal"},
		{"u", "ask the vendor's manifest about updates (the box asks by itself every 4 h)"},
	}},
	{"everywhere", [][2]string{
		{"playback keys", "work from every view that does not use the letter"},
		{"q", "quit, from the player"},
	}},
}

// renderHelp draws the help page into the body. The page is taller than a
// standard 80×24 terminal, so it scrolls the way the diagnostics do — ↑↓ by a
// row, ←→ by a page, through the same diagWindow — and the footer says how
// much is off-screen.
func (m *model) renderHelp(W int) []string {
	t := m.sty.pens()
	var content []string
	for i, g := range helpGroups {
		if i > 0 {
			content = append(content, "")
		}
		content = append(content, m.sectionHead(g.title, W))
		for _, k := range g.keys {
			key := t.bri.render(padDisp(k[0], 14))
			content = append(content, clipStyled(key+t.dmr.render(k[1]), W))
		}
	}
	foot := "esc · q · ? back to the player"
	tail := []string{"", spaces(W-DispW(foot)) + t.dmr.render(foot)}
	content, hint := m.diagWindow(content, m.bodyRows()-len(tail), "↑↓ scroll")
	switch {
	case hint == "":
	case DispW(hint)+2+DispW(foot) <= W:
		tail[len(tail)-1] = between(t.dim.render(hint), DispW(hint), t.dmr.render(foot), DispW(foot), W)
	default: // too narrow for both: how much is off-screen outranks the way out
		tail[len(tail)-1] = t.dim.render(hint)
	}
	return frameBody(content, tail, m.bodyRows(), false)
}
