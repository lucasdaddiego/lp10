package tui

import (
	"image/color"
	"math"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// Every pen in the penSet must reproduce its source style byte-for-byte — that
// is the entire contract that lets the render path use two concats instead of a
// Style.Render. Under lipgloss v2 Style.Render is profile-independent (the
// program's renderer downsamples for the terminal), so a single sweep pins it.
// Samples include the empty string, unicode glyphs, and text with spaces (the
// padded button labels).
func TestPenMatchesStyleRender(t *testing.T) {
	samples := []string{"", "x", "play", " padded ", "●", "━━━", "▶ Playing", "Everything In Its Right Place", "漢字"}
	th := newTheme()
	ps := th.pens()
	pairs := []struct {
		name string
		st   lipgloss.Style
		p    pen
	}{
		{"acc", th.sAcc, ps.acc},
		{"accB", th.sAcc.Bold(true), ps.accB},
		{"bri", th.sBri, ps.bri},
		{"txt", th.sTxt, ps.txt},
		{"dim", th.sDim, ps.dim},
		{"dmr", th.sDmr, ps.dmr},
		{"warn", stWarn, ps.warn},
		{"warnB", stWarn.Bold(true), ps.warnB},
		{"red", stRed, ps.red},
		{"segOn", th.segOn, ps.segOn},
		{"segOff", th.segOff, ps.segOff},
		{"btnOn", th.btnOn, ps.btnOn},
		{"btnOff", th.btnOff, ps.btnOff},
		{"warmKnob", th.warmKnob, ps.warmKnob},
		{"coolKnob", th.coolKnob, ps.coolKnob},
		{"border", lipgloss.NewStyle().Foreground(th.border), ps.border},
	}
	for _, pr := range pairs {
		for _, s := range samples {
			if got, want := pr.p.render(s), pr.st.Render(s); got != want {
				t.Errorf("pen %s(%q): %q != Render %q", pr.name, s, got, want)
			}
		}
	}
	// brand pens against sourceStyle, plus the accent fallback
	for _, n := range brandNames {
		if got, want := ps.brandPen(n).render(n), sourceStyle(th, n).Render(n); got != want {
			t.Errorf("brand %s: %q != %q", n, got, want)
		}
	}
	if got, want := ps.brandPen("Nope").render("z"), th.sAcc.Render("z"); got != want {
		t.Errorf("brand fallback: %q != %q", got, want)
	}
	// the pre-rendered meter cells
	if got, want := ps.mHead, th.head.Render("●"); got != want {
		t.Errorf("mHead %q != %q", got, want)
	}
	if got, want := ps.mTrack, th.track.Render("─"); got != want {
		t.Errorf("mTrack %q != %q", got, want)
	}
	for i := range th.fill {
		if got, want := ps.mFill[i], th.fill[i].Render("━"); got != want {
			t.Errorf("mFill[%d] %q != %q", i, got, want)
		}
	}
	// and the cache is stable: same set every call
	if th.pens() != ps {
		t.Error("pens() should return the cached set")
	}
}

// The ambient tint's cached cells must match rendering its styles directly.
func TestAmbientTintCellsMatchStyles(t *testing.T) {
	th := newTheme()
	at := th.tint(color.RGBA{R: 200, G: 80, B: 40, A: 0xff})
	at.ensure()
	if got, want := at.mHead, at.head.Render("●"); got != want {
		t.Errorf("amb head %q != %q", got, want)
	}
	if got, want := at.framePen.render("│"), at.frame.Render("│"); got != want {
		t.Errorf("amb frame %q != %q", got, want)
	}
	for i := range at.fill {
		if got, want := at.mFill[i], at.fill[i].Render("━"); got != want {
			t.Errorf("amb fill[%d] %q != %q", i, got, want)
		}
	}
}

// refLineMeter is the ORIGINAL meter implementation — one Style.Render per cell
// — kept as the independent reference the cached-cell paths are pinned against.
func refLineMeter(th *theme, frac float64, cells int, fill []lipgloss.Style, head lipgloss.Style) string {
	if cells <= 0 {
		return ""
	}
	frac = clampF(frac)
	h := int(math.Round(frac * float64(cells)))
	var b strings.Builder
	for i := range cells {
		switch {
		case i == h-1 || (h == 0 && i == 0):
			b.WriteString(head.Render("●"))
		case i < h-1:
			b.WriteString(fill[rampIdx(len(fill), i, h)].Render("━"))
		default:
			b.WriteString(th.track.Render("─"))
		}
	}
	return b.String()
}

// lineMeter (cached cells) and the ambient-tinted seek meter must both equal
// the per-cell-Render reference — the cached paths can never drift from what a
// fresh render would paint.
func TestLineMeterMatchesPerCellReference(t *testing.T) {
	th := newTheme()
	at := th.tint(color.RGBA{R: 200, G: 80, B: 40, A: 0xff})
	at.ensure()
	for _, cells := range []int{0, 1, 2, 5, 24, 60} {
		for _, frac := range []float64{0, 0.01, 0.42, 0.99, 1} {
			if got, want := th.lineMeter(frac, cells), refLineMeter(th, frac, cells, th.fill, th.head); got != want {
				t.Errorf("lineMeter(%v,%d) diverged from the per-cell reference", frac, cells)
			}
			amb := lineMeterCells(frac, cells, at.mFill, at.mHead, th.pens().mTrack)
			if want := refLineMeter(th, frac, cells, at.fill, at.head); amb != want {
				t.Errorf("ambient meter(%v,%d) diverged from the per-cell reference", frac, cells)
			}
		}
	}
}

// The pad-run helpers must agree with strings.Repeat on both sides of their
// const-slice fast path.
func TestPadRunHelpers(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 7, 512, 513, 700} {
		if got, want := spaces(n), strings.Repeat(" ", max(n, 0)); got != want {
			t.Errorf("spaces(%d) = %d chars, want %d", n, len(got), len(want))
		}
		if got, want := dashes(n), strings.Repeat("─", max(n, 0)); got != want {
			t.Errorf("dashes(%d) mismatch", n)
		}
	}
}

// frameLines must reproduce the lipgloss pipeline byte-for-byte: ThickBorder
// + BorderForeground + Padding(0,2,0,2) + Render, then Place(cols, rows) — which
// is a geometric no-op because the framed block is already exactly cols×rows.
func TestFrameMatchesLipgloss(t *testing.T) {
	th := newTheme()
	m := &model{sty: th}
	for _, tc := range []struct{ rows, cols int }{{9, 60}, {20, 90}, {44, 150}} {
		m.rows, m.cols = tc.rows, tc.cols
		W := tc.cols - 6
		// a realistic body: a full-width header, styled lines, empties,
		// short lines — exactly rows-2 of them like the renderers emit.
		lines := make([]string, tc.rows-2)
		lines[0] = between(th.sAcc.Render("♪ LP10"), 6, th.sDim.Render("● 12:00"), 7, W)
		for i := 1; i < len(lines); i++ {
			switch i % 4 {
			case 0: // full-width dim rule
				lines[i] = th.sDmr.Render(strings.Repeat("─", W))
			case 1:
				lines[i] = ""
			case 2:
				lines[i] = th.sBri.Render("Song Title") + " " + th.sDim.Render("Artist")
			default:
				lines[i] = "plain text"
			}
		}
		want := lipgloss.Place(tc.cols, tc.rows, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(th.border).
				Padding(0, 2, 0, 2).
				Render(strings.Join(lines, "\n")))
		got := m.frameLines(lines, W)
		if got != want {
			gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
			for i := range min(len(gl), len(wl)) {
				if gl[i] != wl[i] {
					t.Fatalf("%dx%d line %d:\n got %q\nwant %q", tc.cols, tc.rows, i, gl[i], wl[i])
				}
			}
			t.Fatalf("%dx%d: line count %d vs %d", tc.cols, tc.rows, len(gl), len(wl))
		}
	}
}

// joinCols must reproduce JoinHorizontal + Split for the three player columns:
// uniform art/vol columns, ragged mid lines with at least one exact-midW line
// (the seek row guarantees that in the real layout).
func TestJoinColsMatchesJoinHorizontal(t *testing.T) {
	th := newTheme()
	const midW = 40
	art := []string{"AAAA", "AAAA", "AAAA", "AAAA"}
	vol := []string{"  ██  ", "  ██  ", "  ██  ", " 42%  "}
	mid := []string{
		th.sBri.Render("Title"),
		"",
		th.sDim.Render(strings.Repeat("━", midW)), // the exact-width seek row
		"short",
	}
	gap := spaces(artGap)
	want := strings.Split(lipgloss.JoinHorizontal(lipgloss.Top,
		strings.Join(art, "\n"), gap,
		strings.Join(mid, "\n"), gap,
		strings.Join(vol, "\n")), "\n")
	got := joinCols(art, mid, vol, midW)
	if len(got) != len(want) {
		t.Fatalf("line count %d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}
