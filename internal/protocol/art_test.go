package protocol

import (
	"image"
	"image/color"
	"testing"
)

// Snap must expose the decoded cover (and its precomputed dominant hue) only
// while it matches the playing track's CoverArtUrl, so art from a previous track
// never lingers after a change.
func TestSnapArtStaleness(t *testing.T) {
	st := NewState()
	st.Preload(Track{"TrackName": "x", "CoverArtUrl": "http://a/1"}, 0, 50)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	dom := color.RGBA{200, 30, 30, 255}

	if s := st.Snap(); s.CoverURL != "http://a/1" || s.Art != nil || s.DominantOK {
		t.Fatalf("before SetArt: CoverURL=%q Art=%v DominantOK=%v", s.CoverURL, s.Art, s.DominantOK)
	}

	st.SetArt("http://a/1", img, dom, true) // matches current cover
	if s := st.Snap(); s.Art == nil || !s.DominantOK || s.Dominant != dom {
		t.Errorf("current cover should expose art+dominant: Art=%v DominantOK=%v Dominant=%v", s.Art, s.DominantOK, s.Dominant)
	}

	st.SetArt("http://a/0", img, dom, true) // art for a different (older) cover
	if s := st.Snap(); s.Art != nil || s.DominantOK {
		t.Error("stale art (mismatched url) must not be exposed")
	}
}
