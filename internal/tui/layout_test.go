package tui

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/lucasdaddiego/lp10/internal/protocol"
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
	longplay := func(t *testing.T) *model {
		// A >100-minute track ("-100:05" is 7 wide) plus a 16:9 cover, which
		// pins the middle column at its 24-column floor at 25×70 — the exact
		// state where the seek row's old 1-cell meter floor pushed every frame
		// line 1–2 cells past the window and wrapped the whole UI.
		m, st, _ := makeModel(t)
		tr := *st.Snap().Track
		tr.TotalTime = 6_005_000 // 100:05
		st.Preload(&tr, 0, 44)
		st.SetArt(tr.CoverArtURL, image.NewRGBA(image.Rect(0, 0, 1280, 720)), color.RGBA{}, false)
		return m
	}
	wifi := func(t *testing.T) *model {
		// A long device-supplied SSID: the stacked diag overlay used to append
		// its rows unclipped, so this row sized contentW past the window and
		// wrapped every overlay line at narrow widths.
		m, st, _ := makeModel(t)
		feed := "@@i\nnet=wifi\niface=wlan0\nip=192.168.1.20\nmac=aa:bb:cc:dd:ee:f1\n" +
			"gw=192.168.1.1\nssid=MyHomeNetwork_5GHz_Extended_Long\nfreq=5745\nrate=780\ndata=1 2\n@@E\n"
		for rec := range protocol.IterRecords(feeder(strings.Split(strings.TrimSuffix(feed, "\n"), "\n"))) {
			protocol.ApplyRecord(st, rec)
		}
		return m
	}
	scenes := []scene{{"play", playing}, {"idle", idle}, {"disc", disconnected}, {"muted", muted},
		{"longplay", longplay}, {"wifi", wifi}}
	// 40×58: tall-narrow — the stacked diag overlay renders ALL sections (a
	// short window trims the network section away before its rows can misbehave).
	sizes := [][2]int{{25, 70}, {27, 72}, {30, 90}, {32, 100}, {40, 120}, {48, 160}, {22, 64}, {20, 58}, {40, 58}, {18, 60}, {8, 50}}

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
			view := m.viewContent()
			// mini view (very small) is a single bare line, not a full-window frame
			if rows >= MiniRows && cols >= MiniCols {
				check(t, fmt.Sprintf("%s_%dx%d", sc.name, rows, cols), rows, cols, view)
			}
			dump(fmt.Sprintf("%s_%02dx%03d", sc.name, rows, cols), view)
			if sc.name == "play" || sc.name == "wifi" { // also the diagnostics overlay
				m.diag = true
				dview := m.viewContent()
				if rows >= MiniRows && cols >= MiniCols {
					check(t, fmt.Sprintf("diag_%s_%dx%d", sc.name, rows, cols), rows, cols, dview)
				}
				dump(fmt.Sprintf("diag_%s_%02dx%03d", sc.name, rows, cols), dview)
			}
		}
	}
}
