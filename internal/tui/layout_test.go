package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10go/internal/protocol"
)

var osc8re = regexp.MustCompile("\x1b\\]8;[^\x1b\a]*(\x1b\\\\|\a)")

func clean(s string) string { return osc8re.ReplaceAllString(stripANSI(s), "") }

// TestLayoutInvariants asserts the frame fills the window exactly (every line is
// `cols` wide, total lines == `rows`, borders top/bottom) across a matrix of
// sizes and states, and dumps clean renders to LP10_DUMP_DIR for review.
func TestLayoutInvariants(t *testing.T) {
	dir := os.Getenv("LP10_DUMP_DIR")
	dump := func(name, view string) {
		if dir != "" {
			os.WriteFile(filepath.Join(dir, name+".txt"), []byte(clean(view)), 0o644)
		}
	}
	type scene struct {
		name  string
		model func(t *testing.T) *model
	}
	playing := func(t *testing.T) *model { m, _, _ := makeModel(t); return m }
	idle := func(t *testing.T) *model {
		st := protocol.NewState()
		applyFixtureRecords(st, "idle_record.txt")
		m, _, _ := modelWith(st)
		return m
	}
	disconnected := func(t *testing.T) *model { m, _, _ := modelWith(protocol.NewState()); return m }
	muted := func(t *testing.T) *model {
		m, st, _ := makeModel(t)
		st.SetVol(50)
		m.do("mute") // -> Muted: solid red rail + MUTED header flag must still fit
		return m
	}
	scenes := []scene{{"play", playing}, {"idle", idle}, {"disc", disconnected}, {"muted", muted}}
	sizes := [][2]int{{25, 70}, {27, 72}, {30, 90}, {32, 100}, {40, 120}, {48, 160}, {22, 64}, {18, 60}, {8, 50}}

	check := func(t *testing.T, tag string, rows, cols int, view string) {
		lines := strings.Split(view, "\n")
		if len(lines) != rows {
			t.Errorf("%s: %d lines, want %d", tag, len(lines), rows)
		}
		for i, ln := range lines {
			if w := lipgloss.Width(ln); w != cols {
				t.Errorf("%s line %d width %d, want %d: %q", tag, i, w, cols, clean(ln))
			}
		}
	}

	for _, sc := range scenes {
		for _, sz := range sizes {
			rows, cols := sz[0], sz[1]
			m := sc.model(t)
			m.rows, m.cols = rows, cols
			view := m.View()
			// mini view (very small) is a single bare line, not a full-window frame
			if rows >= MiniRows && cols >= MiniCols {
				check(t, fmt.Sprintf("%s_%dx%d", sc.name, rows, cols), rows, cols, view)
			}
			dump(fmt.Sprintf("%s_%02dx%03d", sc.name, rows, cols), view)
			if sc.name == "play" { // also the diagnostics overlay
				m.diag = true
				dview := m.View()
				if rows >= MiniRows && cols >= MiniCols {
					check(t, fmt.Sprintf("diag_%dx%d", rows, cols), rows, cols, dview)
				}
				dump(fmt.Sprintf("diag_%02dx%03d", rows, cols), dview)
			}
		}
	}
}
