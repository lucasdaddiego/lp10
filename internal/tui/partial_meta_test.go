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
		name, artist, want string
	}{
		{"Song", "Band", "Song — Band"},
		{"Song", "", "Song"},
		{"", "Band", "Band"},
		{"", "", ""},
	}
	for _, c := range cases {
		got := trackTitle(&protocol.Track{TrackName: c.name, Artist: c.artist})
		if got != c.want {
			t.Errorf("trackTitle(%q, %q) = %q, want %q", c.name, c.artist, got, c.want)
		}
	}
}

func TestComputeTitlePartialMetadata(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.cfg.Name = "LP10"

	s := m.st.Snap()
	s.Track = &protocol.Track{TrackName: "Solo"}
	if got := m.computeTitle(s); got != GL["note"]+" Solo" {
		t.Errorf("artist-less title = %q, want %q", got, GL["note"]+" Solo")
	}
	// A track with no usable metadata falls back to the device name.
	s.Track = &protocol.Track{}
	if got := m.computeTitle(s); got != "LP10" {
		t.Errorf("empty-track title = %q, want LP10", got)
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
