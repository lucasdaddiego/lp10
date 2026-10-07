// The player rendering (view.go): the dashboard, its rows, the rail, the
// transport, the marquee and the footer.

package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestCov_stack(t *testing.T) {
	if stack(nil, nil, nil, 0) != nil {
		t.Error("stack(h<=0) should be nil")
	}
	out := stack([]string{"H"}, []string{"M"}, []string{"F"}, 3)
	if len(out) != 3 || out[0] != "H" || out[2] != "F" {
		t.Errorf("stack normal = %v", out)
	}
	// middle overflows the region and is trimmed from the bottom
	out = stack([]string{"H"}, []string{"m1", "m2", "m3"}, []string{"F"}, 3)
	if len(out) != 3 || out[0] != "H" || out[2] != "F" || out[1] != "m1" {
		t.Errorf("stack overflow-trim = %v", out)
	}
	// top + bottom exceed h: the region clamps to 0, tail still pins to the bottom
	out = stack([]string{"a", "b"}, []string{"m"}, []string{"y", "z"}, 3)
	if len(out) != 3 || out[2] != "z" {
		t.Errorf("stack region<0 = %v", out)
	}
}

func TestCov_frameBody(t *testing.T) {
	if frameBody(nil, nil, 0, false) != nil {
		t.Error("frameBody(h<=0) should be nil")
	}
	// tail >= h: only the last h tail lines survive
	out := frameBody([]string{"c"}, []string{"t1", "t2", "t3"}, 2, false)
	if len(out) != 2 || out[0] != "t2" || out[1] != "t3" {
		t.Errorf("frameBody tail>=h = %v", out)
	}
	// content overflows the room above the tail -> trimmed from the bottom
	out = frameBody([]string{"c1", "c2", "c3"}, []string{"F"}, 3, false)
	if len(out) != 3 || out[0] != "c1" || out[2] != "F" {
		t.Errorf("frameBody content-trim = %v", out)
	}
	// centred content
	out = frameBody([]string{"c"}, []string{"F"}, 4, true)
	if len(out) != 4 || out[3] != "F" || out[1] != "c" {
		t.Errorf("frameBody centred = %v", out)
	}
}

func TestCov_centreRows(t *testing.T) {
	if got := centreRows(nil, 3); got != nil {
		t.Errorf("centreRows(empty) = %v", got)
	}
	col := []string{"ab", "cd", "ef"}
	if got := centreRows(col, 2); !slices.Equal(got, col) {
		t.Errorf("a taller column comes back as is, got %v", got)
	}
	got := centreRows([]string{"ab"}, 4)
	if !slices.Equal(got, []string{"  ", "ab", "  ", "  "}) {
		t.Errorf("centreRows(1 in 4) = %q", got)
	}
}

func TestCov_sourceStyle(t *testing.T) {
	st := newTheme()
	cases := map[string]string{
		"Spotify":   "#1db954",
		"TIDAL":     "#4fd4d4",
		"AirPlay":   "#cfd6df",
		"Bluetooth": "#4a90d9",
	}
	for name, hex := range cases {
		if got := sourceStyle(st, name).GetForeground(); got != lipgloss.Color(hex) {
			t.Errorf("sourceStyle(%s) fg = %v, want %s", name, got, hex)
		}
	}
	// unknown falls back to the theme accent
	if sourceStyle(st, "Whatever").GetForeground() != st.sAcc.GetForeground() {
		t.Error("unknown source should fall back to the accent")
	}
}

// The full player's source line: "● Spotify" beside a named track, the input
// before any VND, nothing when no track is shown (metaLines names the source
// there) or nothing is named.
func TestCov_fullSourceLine(t *testing.T) {
	m, _, _ := makeModel(t)
	s := m.st.Snap()
	if got := stripANSI(m.fullSourceLine(s, 60)); got != "● Spotify" {
		t.Errorf("fullSourceLine wide = %q", got)
	}
	// too narrow -> a plain dim clip that still respects the width contract
	if got := stripANSI(m.fullSourceLine(s, 5)); DispW(got) > 5 || !strings.HasSuffix(got, GL["ell"]) {
		t.Errorf("fullSourceLine narrow = %q, want a clip within 5", got)
	}
	// a track before any VND: the input
	if got := stripANSI(m.fullSourceLine(protocol.Snapshot{Source: "BT", Track: &protocol.Track{TrackName: "x"}}, 60)); got != "● Bluetooth" {
		t.Errorf("fullSourceLine(BT) = %q", got)
	}
	// no track, or nothing named -> ""
	if got := m.fullSourceLine(protocol.Snapshot{Service: "spotify"}, 60); got != "" {
		t.Errorf("fullSourceLine(no track) = %q, want empty", got)
	}
	if got := m.fullSourceLine(protocol.Snapshot{Track: &protocol.Track{TrackName: "x"}}, 60); got != "" {
		t.Errorf("fullSourceLine(nothing named) = %q, want empty", got)
	}
}

func TestCov_controlsRow(t *testing.T) {
	m, _, _ := makeModel(t)
	// withVol == false returns just the transport cluster (no volume)
	noVol := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, false))
	if strings.Contains(noVol, "vol") || strings.Contains(noVol, "%") {
		t.Errorf("controlsRow(withVol=false) should omit volume: %q", noVol)
	}
	live := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, true))
	if !strings.Contains(live, "vol") || !strings.Contains(live, "44%") || !strings.Contains(live, " mute ") {
		t.Errorf("controlsRow live = %q", live)
	}
	if DispW(live) != 80 {
		t.Errorf("controlsRow width = %d, want 80", DispW(live))
	}
	// muted + withVol shows the MUTED badge and offers unmute
	m.do("mute")
	muted := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, true))
	if !strings.Contains(muted, "MUTED") || !strings.Contains(muted, "unmute") {
		t.Errorf("controlsRow muted = %q", muted)
	}
	// a flashing button lights up even when it is not focused
	m.flash["next"] = time.Now().Add(time.Second)
	if lit, plain := m.controlsRow(m.st.Snap(), time.Now(), 80, false), m.controlsRow(m.st.Snap(), time.Now().Add(2*time.Second), 80, false); lit == plain {
		t.Error("a flashing next should be styled differently from a settled one")
	}
}

// metaLines while disconnected with an error shows the friendly reason, not
// the raw dial error.
func TestCov_metaLinesDisconnectedError(t *testing.T) {
	st := protocol.NewState()
	st.Note("cannot reach :2018: dial tcp: lookup lp10.local: no such host")
	md, _, _ := modelWith(st)
	lines := md.metaLines(md.st.Snap(), 50)
	assertWithin(t, "metaLines", lines, 50)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "connecting to LP10") || !strings.Contains(joined, "can't find the device") {
		t.Errorf("metaLines disconnected+error = %q", joined)
	}
}

func TestCov_metaLinesTrackVariants(t *testing.T) {
	m, _, _ := makeModel(t)
	// empty title -> "—", artist + album joined on the second line
	s1 := protocol.Snapshot{Track: &protocol.Track{Artist: "A", Album: "Al"}}
	l1 := stripANSI(strings.Join(m.metaLines(s1, 40), "\n"))
	if !strings.Contains(l1, "—") || !strings.Contains(l1, "A · Al") {
		t.Errorf("metaLines empty-title = %q", l1)
	}
	// no artist but an album -> the album alone, linked to an album search
	s2 := protocol.Snapshot{Track: &protocol.Track{TrackName: "T", Album: "OnlyAlbum"}}
	l2 := m.metaLines(s2, 40)
	if got := clean(l2[1]); got != "OnlyAlbum" || !strings.Contains(l2[1], spotifySearch("OnlyAlbum")) {
		t.Errorf("metaLines album-only = %q", l2[1])
	}
	assertWithin(t, "metaLines", m.metaLines(s1, 40), 40)
	assertWithin(t, "metaLines", l2, 40)
	// a bare title: an empty, unlinked second line
	l3 := m.metaLines(protocol.Snapshot{Track: &protocol.Track{TrackName: "T"}}, 40)
	if clean(l3[1]) != "" || strings.Contains(l3[1], "\x1b]8") {
		t.Errorf("metaLines title-only second line = %q", l3[1])
	}
}

func TestCov_fullMetaVariants(t *testing.T) {
	m, _, _ := makeModel(t)
	// empty title -> "—", and with no artist/album there's a single line
	out := m.fullMeta(protocol.Snapshot{Track: &protocol.Track{}}, 40)
	if len(out) != 1 || clean(out[0]) != "—" {
		t.Errorf("fullMeta empty-title = %q", out)
	}
	// the three fields on their own lines
	full := m.fullMeta(m.st.Snap(), 40)
	assertWithin(t, "fullMeta", full, 40)
	assertWithin(t, "fullMeta narrow", m.fullMeta(m.st.Snap(), 8), 8)
	if len(full) != 3 || clean(full[0]) != "De Música Ligera" || clean(full[1]) != "Soda Stereo" || clean(full[2]) != "Canción Animal" {
		t.Errorf("fullMeta = %q", full)
	}
}

func TestCov_footerRowEQHint(t *testing.T) {
	m, _, _ := makeModel(t)
	m.view = viewEQ
	if got := stripANSI(m.footerRow(80)); strings.TrimSpace(got) != eqHint || DispW(got) != 80 {
		t.Errorf("footer EQ hint = %q", got)
	}
}

func TestCov_renderDashboardCompactAndErrors(t *testing.T) {
	// compact layout: cols between MiniCols and FullCols
	m, _, _ := makeModel(t)
	m.rows, m.cols = 20, 64
	out := clean(render(t, m))
	if strings.Contains(out, "Max volume") || !strings.Contains(out, GL["rew"]) {
		t.Errorf("compact dashboard should show the transport and nothing of the equalizer:\n%s", out)
	}

	// a connected error paints the red error line in the compact tail
	me, st, _ := makeModel(t)
	st.Note("dial tcp: connection refused")
	me.rows, me.cols = 20, 64
	if !strings.Contains(clean(render(t, me)), "the device refused the connection") {
		t.Error("compact errLine should show the friendly reason")
	}

	// and in the full layout, the idle screen included
	for _, stf := range []*protocol.State{playingState(), idleState()} {
		mf, _, _ := modelWith(stf)
		stf.Note("dial tcp: connection refused")
		mf.rows, mf.cols = 40, 120
		if !strings.Contains(clean(render(t, mf)), "the device refused the connection on :2018") {
			t.Error("full errLine should show the friendly reason")
		}
	}

	// an error older than ErrorDisplayDuration has left the player
	mo, sto, _ := makeModel(t)
	sto.Note("dial tcp: connection refused")
	mo.rows, mo.cols = 40, 120
	if got := mo.renderDashboard(sto.Snap(), time.Now().Add(ErrorDisplayDuration+time.Second), 114, true); strings.Contains(clean(strings.Join(got, "\n")), "refused") {
		t.Error("an aged error should leave the player")
	}
}

// TestCov_renderDashboardGeometry forces the full player's cover-sizing clamps
// by calling renderDashboard with full=true at a tiny width / short height and
// a known cell-pixel size, so coverH<6, the maxW reservation, coverW<8, and the
// measured-cell aspect branch all fire in one paint.
func TestCov_renderDashboardGeometry(t *testing.T) {
	m, _, _ := makeModel(t)
	m.cellW, m.cellH = 8, 16 // a real measured cell aspect (the m.cellW>0 branch)
	m.rows = 18              // short inner region -> coverH floors to 6
	if out := m.renderDashboard(m.st.Snap(), time.Now(), 40, true); len(out) != m.bodyRows() {
		t.Errorf("renderDashboard rows = %d, want %d", len(out), m.bodyRows())
	}
	// a very wide cell aspect caps the cover by width and floors its height
	m.cellW, m.cellH = 2, 40
	m.rows = 40
	if out := m.renderDashboard(m.st.Snap(), time.Now(), 60, true); len(out) != m.bodyRows() {
		t.Errorf("renderDashboard rows = %d, want %d", len(out), m.bodyRows())
	}
}

func TestCov_marqueeZeroWidth(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.marquee("anything", 0) != "" {
		t.Error("marquee(w<=0) should be empty")
	}
}

func TestCov_transportLayoutNarrow(t *testing.T) {
	// too narrow for inter-button gaps: falls back to a solid cluster (gap 0)
	pad, widths, gap := transportLayout(2)
	if gap != 0 || len(widths) != len(actions) {
		t.Errorf("transportLayout(2) = pad %d widths %v gap %d, want gap 0", pad, widths, gap)
	}
	sum := 0
	for _, w := range widths {
		sum += w
	}
	if sum != 2 {
		t.Errorf("narrow transport widths sum %d, want 2", sum)
	}
}

func TestCov_headerRowReconnectAndNarrow(t *testing.T) {
	st := protocol.NewState()
	st.StartConnection()
	st.StartConnection() // attempts -> 2, still disconnected
	m, _, _ := modelWith(st)
	// the attempt counter is this module's own behaviour, not an environment
	// capability: failing to establish the precondition IS the regression, so it
	// must fail rather than skip (a skip would report a green run).
	if m.st.Snap().Attempts <= 1 {
		t.Fatalf("setup: two StartConnection calls should bump attempts, got %d", m.st.Snap().Attempts)
	}
	if got := stripANSI(m.headerRow(m.st.Snap(), time.Now(), 80, false)); !strings.Contains(got, "● reconnecting (2)…") {
		t.Errorf("disconnected header should read reconnecting: %q", got)
	}
	// the first attempt reads plain "connecting…"
	first := protocol.NewState()
	first.StartConnection()
	if got := stripANSI(m.headerRow(first.Snap(), time.Now(), 80, false)); !strings.Contains(got, "● connecting…") {
		t.Errorf("first-attempt header = %q", got)
	}
	// a tiny width drives the device-name budget below its floor (nameMax clamp)
	_ = m.headerRow(m.st.Snap(), time.Now(), 12, false)
}

// The compact header names the source beside the strip; brand-tinted when it
// fits, a dim clip when only part of it does, and nothing below 8 columns of
// room. The full player keeps it off the header (it has a source line).
func TestCov_headerRowSource(t *testing.T) {
	m, st, _ := makeModel(t)
	now := time.Now()
	if got := stripANSI(m.headerRow(st.Snap(), now, 80, false)); !strings.HasSuffix(got, "Spotify  1 player  2 equalizer  3 diagnostics") {
		t.Errorf("compact header = %q", got)
	}
	if got := stripANSI(m.headerRow(st.Snap(), now, 114, true)); strings.Contains(got, "Spotify") || !strings.HasSuffix(got, "Vol  ") {
		t.Errorf("full header = %q, want Vol over the rail and no source", got)
	}
	// a long unknown service clipped into what the strip leaves
	st.ApplyVendor("a-very-long-vendor-word")
	got := stripANSI(m.headerRow(st.Snap(), now, 80, false))
	if !strings.Contains(got, "a-very-long-ven"+GL["ell"]+"  1 player") || DispW(got) != 80 {
		t.Errorf("clipped source header = %q (%d wide)", got, DispW(got))
	}
	// under 8 columns of room the source gives way to the strip
	if got := stripANSI(m.headerRow(st.Snap(), now, 64, false)); strings.Contains(got, "a-very") || DispW(got) != 64 {
		t.Errorf("crowded header = %q (%d wide)", got, DispW(got))
	}
	// muted: the rail label turns MUTED from the top
	st.ApplyMute(true)
	if got := stripANSI(m.headerRow(st.Snap(), now, 114, true)); !strings.Contains(got, "MUTED") {
		t.Errorf("muted full header = %q", got)
	}
}

// The muted volume rail is impossible to miss: a SOLID red column and a bold
// "MUTED" badge, distinct from a live rail showing a percentage.
func TestVolRailMuted(t *testing.T) {
	m, _, _ := makeModel(t)
	muted := m.volRail(protocol.Snapshot{Muted: true, Vol: 44}, 5)
	if len(muted) != 6 {
		t.Fatalf("a 5-high rail is %d rows, want 6 (bar + value)", len(muted))
	}
	joined := strings.Join(muted, "\n")
	if !strings.Contains(joined, "MUTED") || !strings.Contains(joined, "█") {
		t.Error("a muted rail should show the MUTED badge over a solid column")
	}
	live := strings.Join(m.volRail(protocol.Snapshot{Vol: 60}, 5), "\n")
	if strings.Contains(live, "MUTED") {
		t.Error("a live rail should not read muted")
	}
	if !strings.Contains(stripANSI(live), "60%") {
		t.Error("a live rail should show the percentage")
	}
	for _, rail := range [][]string{muted, m.volRail(protocol.Snapshot{Vol: 60}, 5)} {
		for i, ln := range rail {
			if w := lipgloss.Width(ln); w != volColW {
				t.Errorf("rail row %d width %d, want %d", i, w, volColW)
			}
		}
	}
}

// The rail is cached by what it shows: the same volume, mute and height reuse
// the block; a change of any rebuilds it.
func TestVolRailCache(t *testing.T) {
	m, _, _ := makeModel(t)
	a := m.volRail(protocol.Snapshot{Vol: 60}, 5)
	if b := m.volRail(protocol.Snapshot{Vol: 60}, 5); &a[0] != &b[0] {
		t.Error("an unchanged rail should come from the cache")
	}
	for _, s := range []struct {
		snap protocol.Snapshot
		h    int
	}{{protocol.Snapshot{Vol: 61}, 5}, {protocol.Snapshot{Vol: 61, Muted: true}, 5}, {protocol.Snapshot{Vol: 61, Muted: true}, 6}} {
		prev := m.volBlk
		if got := m.volRail(s.snap, s.h); &got[0] == &prev[0] {
			t.Errorf("%+v h=%d reused the stale rail", s.snap, s.h)
		}
	}
}

// On a wide column the transport buttons form a centred cluster (padded both
// sides) rather than stretching edge to edge — but still fill the column width.
func TestTransportClusterCentred(t *testing.T) {
	m, _, _ := makeModel(t)
	for _, w := range []int{60, 40, 10, 3} {
		row := m.transportSegments(protocol.Snapshot{Playing: true}, time.Now(), w)
		if lipgloss.Width(row) != w {
			t.Errorf("transport row width %d, want %d", lipgloss.Width(row), w)
		}
	}
	if row := m.transportSegments(protocol.Snapshot{}, time.Now(), 60); !strings.HasPrefix(row, " ") {
		t.Error("a wide transport cluster should be padded (centred), not edge-to-edge")
	}
	// the focused button lights up only on the player
	m.focus = 2
	onPlayer := m.transportSegments(protocol.Snapshot{}, time.Now(), 60)
	m.view = viewEQ
	if off := m.transportSegments(protocol.Snapshot{}, time.Now(), 60); off == onPlayer {
		t.Error("the transport focus should not light while another view has the keys")
	}
}

// Every footer width gets the widest hint that fits, right-aligned and exactly
// W wide; only below the narrowest hint is it clipped.
func TestFooterHintLadderFitsW(t *testing.T) {
	m, _, _ := makeModel(t)
	for _, page := range []int{0, 130} {
		m.scroll = page
		ladder := playerHints
		if page > 0 {
			ladder = playerHintsRare
		}
		narrowest := DispW(ladder[len(ladder)-1])
		for W := 1; W <= 130; W++ {
			got := m.footerRow(W)
			if visWidth(got) != W {
				t.Fatalf("page %d W=%d: footer width %d", page, W, visWidth(got))
			}
			hint := strings.TrimSpace(stripANSI(got))
			if W >= narrowest {
				if strings.Contains(hint, GL["ell"]) {
					t.Errorf("page %d W=%d: a hint that fits was clipped: %q", page, W, hint)
				}
				// the widest that fits
				for _, h := range ladder {
					if DispW(h) <= W {
						if hint != h {
							t.Errorf("page %d W=%d: %q, want %q", page, W, hint, h)
						}
						break
					}
				}
			} else if !strings.HasSuffix(hint, GL["ell"]) && W > 1 {
				t.Errorf("page %d W=%d: a too-narrow footer should be clipped: %q", page, W, hint)
			}
		}
	}
	// the help and diagnostics footers name no removed views or keys
	for _, h := range append(append(append([]string{}, playerHints...), playerHintsRare...), diagFooters...) {
		for _, gone := range []string{"services", "logs", "night", "bedtime", "remaining"} {
			if strings.Contains(h, gone) {
				t.Errorf("footer hint %q names the removed %q", h, gone)
			}
		}
	}
}

// A line that measures wider than the frame is clipped to it; lipgloss would
// have grown the box to the widest line and pushed every row, borders
// included, past the terminal.
func TestFrameLinesClipsOverWideLine(t *testing.T) {
	m := &model{sty: newTheme()}
	m.rows, m.cols = 9, 60
	W := m.cols - 6
	lines := make([]string, m.rows-2)
	lines[0] = between("♪ LP10", 6, "12:00", 5, W)
	lines[3] = strings.Repeat("x", W+15)
	lines[4] = m.sty.sBri.Render(strings.Repeat("y", W+3))
	orig := lines[3]
	for i, ln := range strings.Split(m.frameLines(lines, W), "\n") {
		if w := lipgloss.Width(ln); w != m.cols {
			t.Errorf("line %d width %d, want %d: %q", i, w, m.cols, clean(ln))
		}
	}
	if lines[3] != orig {
		t.Error("frameLines must not mutate the caller's lines")
	}
}
