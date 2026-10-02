package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
