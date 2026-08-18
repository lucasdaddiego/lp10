package tui

// partial_meta_test.go pins the partial-metadata rendering: Bluetooth/AirPlay
// tracks often carry only a name or only an artist, and the one-line renders
// (window title, mini line, diag signal detail) must not show dangling
// separators around the missing half.

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
		Track: &protocol.Track{TrackName: "Solo", TotalTime: 200000},
		Vol:   30,
	}))
	if !strings.Contains(out, "Solo") || strings.Contains(out, "—") {
		t.Errorf("mini artist-less = %q, want the name with no dangling em-dash", out)
	}
}

func TestDiagStackedSignalRowDetailSeparators(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()

	if _, ok := m.diagStackedSignalRow(protocol.DiagnosticSnapshot{
		DevInfo: &protocol.DevInfo{Net: "wifi"},
		SysInfo: &protocol.SysInfo{SignalDBm: "n/a"},
	}, 70, 20); ok {
		t.Error("a non-numeric dBm should suppress the signal row")
	}

	d := protocol.DiagnosticSnapshot{
		DevInfo: &protocol.DevInfo{Net: "wifi"},
		SysInfo: &protocol.SysInfo{SignalDBm: "-55", LinkQ: "62"},
	}
	row, ok := m.diagStackedSignalRow(d, 70, 20)
	if !ok {
		t.Fatal("signal row should render on wifi with a numeric dBm")
	}
	out := stripANSI(row)
	if !strings.Contains(out, "link 62/70") || strings.Contains(out, "·") {
		t.Errorf("rate-less detail = %q, want link quality with no dangling separator", out)
	}

	// Both halves present keeps the original joined form.
	d.DevInfo.Rate = "144"
	row, _ = m.diagStackedSignalRow(d, 70, 20)
	if out := stripANSI(row); !strings.Contains(out, "144 Mbit/s  · link 62/70") {
		t.Errorf("full detail = %q, want the rate · link join", out)
	}
}

func TestBrandSourceFallback(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	if out := stripANSI(m.brandSource("Ogg 320k", "")); out != "Ogg 320k" {
		t.Errorf("brandSource without a name = %q, want the text dimmed as-is", out)
	}
	if out := stripANSI(m.brandSource("Spotify · Ogg", "Spotify")); out != "Spotify · Ogg" {
		t.Errorf("brandSource tinted = %q, want the full text", out)
	}
}
