package tui

// The shared test harness: State builders that drive protocol.State through
// the same public calls the tunnel worker makes, a sized model wired to a
// command channel, the key-event shorthands, the ANSI stripping, and the width
// contract every render test asserts.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// ---- State builders ------------------------------------------------------------

// connect marks st connected the way the tunnel worker does: a connection
// attempt, then a first parsed frame.
func connect(st *protocol.State) {
	st.StartConnection()
	st.Received()
}

// playingState is the box mid-track: connected, the first status read (NET,
// 44 %, playing), Spotify named by its VND push, and a track announced by the
// TIT/ART/ALB pushes of a track change.
func playingState() *protocol.State {
	st := protocol.NewState()
	connect(st)
	st.ApplyStatus("NET", false, 44, true)
	st.ApplyVendor("spotify")
	st.ApplyTrackField(protocol.FieldTitle, "De Música Ligera")
	st.ApplyTrackField(protocol.FieldArtist, "Soda Stereo")
	st.ApplyTrackField(protocol.FieldAlbum, "Canción Animal")
	return st
}

// untitledState is a run that started mid-track: the status says playing and
// the resume pushed VND, but the tunnel names a track only when it changes, so
// there is none yet.
func untitledState() *protocol.State {
	st := protocol.NewState()
	connect(st)
	st.ApplyStatus("NET", false, 44, true)
	st.ApplyVendor("spotify")
	return st
}

// idleState is a connected box with nothing playing and no track this run.
func idleState() *protocol.State {
	st := protocol.NewState()
	connect(st)
	st.ApplyStatus("NET", false, 44, false)
	return st
}

// ---- the model -------------------------------------------------------------------

// testHost is a documentation address (RFC 5737): no test opens a socket to it.
const testHost = "192.0.2.40"

func defaultCfg() config.Config {
	return config.Config{Host: testHost, Name: "LP10 · Living", VolStep: 2}
}

// makeModel returns a model over playingState, plus the State and a collector
// that drains the command channel.
func makeModel(t *testing.T) (*model, *protocol.State, func() []workers.Command) {
	t.Helper()
	return modelWith(playingState())
}

// modelWith wires a model to st with a buffered command channel. The model is
// sized (FullRows × FullCols) and themed: before the first WindowSizeMsg
// miniMode() gates every view switch shut, and most tests exercise the
// dashboard. Tests for the mini/unsized paths set their own rows/cols.
func modelWith(st *protocol.State) (*model, *protocol.State, func() []workers.Command) {
	cmds := make(chan workers.Command, 64)
	m := newModel(st, defaultCfg(), cmds)
	m.rows, m.cols = FullRows, FullCols
	m.ensureTheme()
	collect := func() []workers.Command {
		var out []workers.Command
		for {
			select {
			case c := <-cmds:
				out = append(out, c)
			default:
				return out
			}
		}
	}
	return m, st, collect
}

func kr(r rune) keyEvent    { return keyEvent{kind: kRune, r: r} }
func ke(k keyKind) keyEvent { return keyEvent{kind: k} }

// wire spells queued commands the way the tunnel writes them: an action by its
// code ("POP"), a query with a "?" ("MXV?"), a set as CODE:VAL ("VOL:46").
func wire(cs []workers.Command) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		switch {
		case c.Query:
			out = append(out, c.Code+"?")
		case tunnel.IsAction(c.Code):
			out = append(out, c.Code)
		default:
			out = append(out, fmt.Sprintf("%s:%d", c.Code, c.Val))
		}
	}
	return out
}

// ---- text ------------------------------------------------------------------------

var (
	ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")
	osc8re = regexp.MustCompile("\x1b\\]8;[^\x1b\a]*(\x1b\\\\|\a)")
)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// clean is a frame as the eye reads it: no colour, no hyperlinks.
func clean(s string) string { return osc8re.ReplaceAllString(stripANSI(s), "") }

// hasRow reports whether some line of flat contains every one of subs.
func hasRow(flat string, subs ...string) bool {
	for ln := range strings.SplitSeq(flat, "\n") {
		ok := true
		for _, s := range subs {
			if !strings.Contains(ln, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// frameRowWith is the first line of a frame that contains sub, with the
// frame's border and padding trimmed ("" when no line does).
func frameRowWith(frame, sub string) string {
	for ln := range strings.SplitSeq(frame, "\n") {
		if strings.Contains(ln, sub) {
			return strings.TrimSpace(strings.Trim(ln, "┃"))
		}
	}
	return ""
}

// footerOf is the footer row of a rendered frame: the last row inside the
// border, trimmed.
func footerOf(frame string) string {
	ls := strings.Split(frame, "\n")
	if len(ls) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.Trim(ls[len(ls)-2], "┃"))
}

// ---- the width contract ----------------------------------------------------------

// render draws one frame and asserts the width contract on it twice over.
// The frame itself: exactly rows lines of exactly cols columns (the mini line:
// one line inside the window). And every row the renderers produced before
// framing: no wider than the content width W — frameLines clips an over-wide
// row as a backstop, so only the unframed rows show a renderer that
// mis-sized something.
func render(t *testing.T, m *model) string {
	t.Helper()
	out := m.viewContent()
	if m.rows == 0 || m.cols == 0 {
		return out
	}
	if m.miniMode() {
		if strings.Contains(out, "\n") || lipgloss.Width(out) >= m.cols {
			t.Errorf("%dx%d mini line is %d wide (%d lines), want one line under %d: %q",
				m.cols, m.rows, lipgloss.Width(out), strings.Count(out, "\n")+1, m.cols, clean(out))
		}
		return out
	}
	lines := strings.Split(out, "\n")
	if len(lines) != m.rows {
		t.Errorf("%dx%d %s: %d lines, want %d", m.cols, m.rows, viewNames[m.view], len(lines), m.rows)
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != m.cols {
			t.Errorf("%dx%d %s line %d: width %d, want %d: %q", m.cols, m.rows, viewNames[m.view], i, w, m.cols, clean(ln))
		}
	}
	assertWithin(t, fmt.Sprintf("%dx%d %s body", m.cols, m.rows, viewNames[m.view]), m.unframed(), m.cols-6)
	return out
}

// unframed renders the rows viewContent hands frameLines — the header, the
// notice line and the view's body — without the frame. It mirrors
// viewContent's dispatch for a dashboard-sized model.
func (m *model) unframed() []string {
	W := m.cols - 6
	s := m.st.Snap()
	now := time.Now()
	full := m.rows >= FullRows && m.cols >= FullCols
	lines := []string{m.headerRow(s, now, W, full && m.view == viewPlayer && !idle(s)), m.noticeRow(now, W)}
	switch m.view {
	case viewEQ:
		return append(lines, m.renderEQ(W)...)
	case viewDiag:
		return append(lines, m.renderDiagnostic(m.st.DiagnosticView(), now, W)...)
	case viewHelp:
		return append(lines, m.renderHelp(W)...)
	}
	return append(lines, m.renderDashboard(s, now, W, full)...)
}

// assertWithin fails for every line wider than w columns.
func assertWithin(t *testing.T, what string, lines []string, w int) {
	t.Helper()
	for i, ln := range lines {
		if got := visWidth(ln); got > w {
			t.Errorf("%s line %d: width %d > %d: %q", what, i, got, w, clean(ln))
		}
	}
}
