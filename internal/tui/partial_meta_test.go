package tui

// partial_meta_test.go pins the partial-metadata rendering: Bluetooth/AirPlay
// tracks often carry only a name or only an artist, and the one-line renders
// (window title, mini line) must not show dangling separators around the
// missing half.

import (
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestTrackTitlePartialMetadata(t *testing.T) {
	cases := []struct {
		name, artist, album, want string
	}{
		{"Song", "Band", "", "Song — Band"},
		{"Song", "", "", "Song"},
		{"", "Band", "", "Band"},
		{"", "", "Big Bang", "Big Bang"}, // nothing but the album reads as the album
		{"", "", "", ""},
	}
	for _, c := range cases {
		got := trackTitle(&protocol.Track{TrackName: c.name, Artist: c.artist, Album: c.album})
		if got != c.want {
			t.Errorf("trackTitle(%q, %q, %q) = %q, want %q", c.name, c.artist, c.album, got, c.want)
		}
	}
}

func TestRenderMiniArtistless(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	m.cols = 56
	out := stripANSI(m.renderMini(protocol.Snapshot{
		Connected: true,
		Track:     &protocol.Track{TrackName: "Solo"},
		Vol:       30,
	}))
	if !strings.Contains(out, "Solo") || strings.Contains(out, "—") {
		t.Errorf("mini artist-less = %q, want the name with no dangling em-dash", out)
	}
}
