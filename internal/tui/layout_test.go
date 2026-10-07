package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/sweep"
)

// TestLayoutInvariants asserts the width contract (render: every frame line
// exactly cols wide, exactly rows lines, no renderer row wider than the
// content) across a matrix of sizes, player states and views, and dumps clean
// renders to LP10_DUMP_DIR for review.
func TestLayoutInvariants(t *testing.T) {
	dir := os.Getenv("LP10_DUMP_DIR")
	dump := func(name, view string) {
		if dir != "" {
			if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(clean(view)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	type scene struct {
		name string
		st   func() *protocol.State
	}
	scenes := []scene{
		{"play", playingState},
		{"idle", idleState},
		{"untitled", untitledState},
		{"disc", func() *protocol.State {
			st := protocol.NewState()
			st.StartConnection()
			st.StartConnection()
			st.Note("cannot reach :2018: dial tcp 192.0.2.40:2018: connect: connection refused")
			st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2", State: "S", NetMode: "ETH0"})
			return st
		}},
		{"muted", func() *protocol.State {
			st := playingState()
			st.ToggleMute() // the solid red rail + the MUTED header flag must still fit
			return st
		}},
		{"long", func() *protocol.State {
			// device-supplied text far wider than any column: the marquee,
			// the clipped source and the diagnostics rows must all hold
			st := playingState()
			st.ApplyVendor("a-very-long-vendor-word-nobody-maps")
			st.ApplyTrackField(protocol.FieldTitle, strings.Repeat("Everything In Its Right Place ", 6))
			st.ApplyTrackField(protocol.FieldArtist, strings.Repeat("Radiohead ", 12))
			st.ApplyTrackField(protocol.FieldAlbum, strings.Repeat("漢字 ❤️ Kid A ", 10))
			st.SetLSSDP(&protocol.LSSDPInfo{FW: "AR241CP_8747.29.2", State: "S", NetMode: strings.Repeat("ETH0", 20)})
			st.SetSpotifyZC(&protocol.SpotifyZC{StatusString: "OK", ActiveUser: strings.Repeat("someone", 20), LibraryVersion: "3.216.31"}, 9095)
			st.ApplyVersion("29-1d316f0c-10")
			st.SetEQPresets([]string{"Flat", "Classical", "Pop", "Jazz", "Rock", "Vocal"})
			st.ApplyTunnel("EQE", 1)
			st.ApplyTunnel("EQS", 5)
			st.ApplyTunnel("MXV", 100)
			st.SetOTA(protocol.OTAInfo{At: st.LastRx(), Err: strings.Repeat("vendor unreachable ", 10)})
			return st
		}},
		{"error", func() *protocol.State {
			st := playingState()
			st.Note("command not delivered")
			return st
		}},
	}
	// 40×58: tall-narrow — the stacked diagnostics render ALL sections.
	sizes := [][2]int{{25, 70}, {27, 72}, {30, 90}, {32, 100}, {40, 120}, {48, 160}, {22, 64}, {20, 58}, {40, 58}, {18, 60}, {9, 58}, {8, 50}}
	views := []view{viewPlayer, viewEQ, viewDiag, viewHelp}
	baseline := &sweep.Report{}
	baseline.LSSDP.FW = "AR241CP_8530.23.2"
	baseline.Tunnel.MCU = "23"

	for _, sc := range scenes {
		for _, sz := range sizes {
			for _, v := range views {
				m, _, _ := modelWith(sc.st())
				m.baseline = baseline
				m.rows, m.cols = sz[0], sz[1]
				m.view = v
				out := render(t, m)
				dump(fmt.Sprintf("%s_%s_%02dx%03d", sc.name, viewNames[v], sz[0], sz[1]), out)
			}
		}
	}
}

func TestCov_padHelpers(t *testing.T) {
	if got := padDisp("ab", 5); got != "ab   " {
		t.Errorf("padDisp(ab,5) = %q", got)
	}
	if got := padDisp("abcde", 3); got != "abcde" {
		t.Errorf("padDisp wide no-op = %q", got)
	}
	if got := padVis("ab", 5); got != "ab   " {
		t.Errorf("padVis(ab,5) = %q", got)
	}
	if got := padVis("abcde", 3); got != "abcde" {
		t.Errorf("padVis wide no-op = %q", got)
	}
	if got := labelGap("ab", 5); got != "   " {
		t.Errorf("labelGap(ab,5) = %q (len %d)", got, len(got))
	}
	if got := labelGap("abcdef", 3); got != "" {
		t.Errorf("labelGap overflow should be empty, got %q", got)
	}
}

func TestCov_betweenSplitCcell(t *testing.T) {
	if got := between("L", 1, "R", 1, 10); got != "L"+strings.Repeat(" ", 8)+"R" {
		t.Errorf("between normal = %q", got)
	}
	// gap < 1 clamps to a single space
	if got := between("L", 5, "R", 5, 8); got != "L R" {
		t.Errorf("between clamp = %q, want %q", got, "L R")
	}
	if got := splitWidth(10, 3); len(got) != 3 || got[0] != 4 || got[1] != 3 || got[2] != 3 {
		t.Errorf("splitWidth(10,3) = %v, want [4 3 3]", got)
	}
	if got := splitWidth(9, 3); got[0] != 3 || got[1] != 3 || got[2] != 3 {
		t.Errorf("splitWidth(9,3) = %v, want [3 3 3]", got)
	}
	if w := lipgloss.Width(ccell("x", 5)); w != 5 {
		t.Errorf("ccell width = %d, want 5", w)
	}
}

// A tall cell aspect makes the square-in-pixels cover short (its height floors
// at 6), shorter than the now-playing block beside it; the block used to take
// the cover's height and trim the transport row off its bottom. It now takes
// the taller of the two.
func TestShortCoverKeepsTransportRow(t *testing.T) {
	m, _, _ := makeModel(t)
	m.cellW, m.cellH = 4, 16 // cells four times taller than wide
	m.rows, m.cols = FullRows, FullCols
	out := clean(render(t, m))
	for _, want := range []string{GL["rew"], GL["ff"], "Playing", "● Spotify", "De Música Ligera"} {
		if !strings.Contains(out, want) {
			t.Errorf("full player lacks %q with a short cover:\n%s", want, out)
		}
	}
}

// Emoji in a title (a presentation selector, a ZWJ family) measure two cells
// each on the terminal; the frame, the compact layout and the mini line all
// stay inside the window while the marquee windows through them.
func TestEmojiTitleKeepsEveryLayoutInsideTheWindow(t *testing.T) {
	for _, sz := range [][2]int{{25, 70}, {20, 58}, {40, 120}, {8, 50}} {
		m, st, _ := makeModel(t)
		st.ApplyTrackField(protocol.FieldTitle, strings.Repeat("❤️", 18)+" 1️⃣")
		st.ApplyTrackField(protocol.FieldArtist, "👨‍👩‍👧‍👦 "+strings.Repeat("❤️", 30))
		st.ApplyTrackField(protocol.FieldAlbum, "漢字 ❤️ album")
		m.rows, m.cols = sz[0], sz[1]
		for range 40 { // the marquee windows through the title
			m.scroll++
			render(t, m)
		}
	}
}

// At 9–10 rows the compact frame cannot hold everything: the blank separators
// yield before the player's own status and transport rows. From 11 rows up
// there is room for all of it, and all of it shows.
func TestCompactShortFrameKeepsTransport(t *testing.T) {
	for _, rows := range []int{9, 10, 11, 12, 13} {
		m, _, _ := makeModel(t)
		m.rows, m.cols = rows, 64
		out := clean(render(t, m))
		if !strings.Contains(out, GL["rew"]) || !strings.Contains(out, GL["ff"]) {
			t.Errorf("%d rows: transport row missing:\n%s", rows, out)
		}
		if !strings.Contains(out, "Playing") {
			t.Errorf("%d rows: status row missing:\n%s", rows, out)
		}
	}
	m, _, _ := makeModel(t)
	m.rows, m.cols = 20, 64
	if out := clean(render(t, m)); !strings.Contains(out, GL["rew"]) || !strings.Contains(out, "Playing") || !strings.Contains(out, "vol") {
		t.Errorf("20 rows: the status, transport and volume must all show:\n%s", out)
	}
}

// A section rule narrower than its own title must not produce a negative
// repeat; at any width that fits the title it fills the row exactly.
func TestSectionHeadNarrow(t *testing.T) {
	m, _, _ := makeModel(t)
	if got := clean(m.sectionHead("a very long section title", 4)); got != "── a very long section title " {
		t.Errorf("narrow section head = %q", got)
	}
	for _, W := range []int{20, 52, 114} {
		if got := DispW(clean(m.sectionHead("equalizer", W))); got != W {
			t.Errorf("section head at W=%d is %d wide", W, got)
		}
	}
	// the diagnostics' section rule is the same idea, with the title lit
	sec := m.sectionRows(diagSection{title: "connection", rows: []kv{{"host", "x"}}}, 40)
	assertWithin(t, "sectionRows", sec, 40)
	if got := clean(sec[0]); got != "─ connection "+strings.Repeat("─", 40-3-len("connection")) {
		t.Errorf("section rule = %q", got)
	}
	if got := clean(m.sectionRows(diagSection{title: "connection"}, 5)[0]); got != "─ connection " {
		t.Errorf("narrow section rule = %q", got)
	}
}
