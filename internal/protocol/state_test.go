package protocol

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

// ---- helpers ----------------------------------------------------------------

// locked runs f with st.mu held, for the white-box reads and rewinds below.
func locked(st *State, f func()) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f()
}

// armedFor checks that a hold armed between before and after ends d later.
func armedFor(t *testing.T, hold, before, after time.Time, d time.Duration) {
	t.Helper()
	if hold.Before(before.Add(d)) || hold.After(after.Add(d)) {
		t.Errorf("hold ends %v after the change, want %v", hold.Sub(before), d)
	}
}

// assertClean fails when s holds a rune printable must have stripped: any
// control, format or separator rune but the ASCII space (a ZWJ is legal only
// between two kept runes, which none of these inputs have).
func assertClean(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range s {
		if r != ' ' && unicode.In(r, unicode.C, unicode.Z) {
			t.Errorf("%s = %+q keeps %U", what, s, r)
		}
	}
}

// ---- a fresh State ----------------------------------------------------------

func TestNewStateIsEmpty(t *testing.T) {
	st := NewState()
	if s := st.Snap(); s != (Snapshot{}) {
		t.Errorf("fresh Snap = %+v, want the zero Snapshot", s)
	}
	if d := st.DiagnosticView(); d != (DiagnosticSnapshot{}) {
		t.Errorf("fresh DiagnosticView = %+v, want zero", d)
	}
	if p := st.EQPresets(); p != nil {
		t.Errorf("EQPresets = %q before any PEQ reply, want nil", p)
	}
	connected, vals := st.EQView()
	if connected || vals == nil || len(vals) != 0 {
		t.Errorf("EQView = (%v, %v), want (false, empty non-nil map)", connected, vals)
	}
	if v, ok := st.EQValue("BAS"); ok || v != 0 {
		t.Errorf("EQValue(BAS) = (%d, %v) before any read, want (0, false)", v, ok)
	}
	if !st.LastRx().IsZero() {
		t.Error("LastRx is set before any frame")
	}
	if !st.ProbeWanted() {
		t.Error("a State without a TUI must probe (probeQuiet defaults off)")
	}
	if v, ok := st.TakeVolumeBridge(); ok {
		t.Errorf("a fresh State has a volume bridge pending: %d", v)
	}
}

// ---- the player, as the tunnel reports it -----------------------------------

func TestApplyStatusAppliesEveryField(t *testing.T) {
	st := NewState()
	st.ApplyStatus("NET", true, 44, true)
	s := st.Snap()
	if s.Source != "NET" || !s.Muted || s.Vol != 44 || !s.Playing {
		t.Errorf("after STA: source=%q muted=%v vol=%d playing=%v, want NET true 44 true",
			s.Source, s.Muted, s.Vol, s.Playing)
	}
	if !s.PlayKnown || !s.VolLive {
		t.Errorf("PlayKnown=%v VolLive=%v, want both true after a STA reply", s.PlayKnown, s.VolLive)
	}
	// the next reply moves every field back together
	st.ApplyStatus("NET", false, 12, false)
	s = st.Snap()
	if s.Muted || s.Vol != 12 || s.Playing {
		t.Errorf("second STA: muted=%v vol=%d playing=%v, want false 12 false", s.Muted, s.Vol, s.Playing)
	}
}

// The STA volume is raw on the wire (tunnel.ParseFrames never clamps a
// readback), so the State is where it meets the 0..100 invariant.
func TestApplyVolumeClamps(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{44, 44}, {0, 0}, {100, 100}, {250, 100}, {-5, 0}, {math.MaxInt, 100}, {math.MinInt, 0},
	} {
		st := NewState()
		st.ApplyStatus("NET", false, c.in, false)
		if got := st.Snap().Vol; got != c.want {
			t.Errorf("ApplyStatus vol %d -> %d, want %d", c.in, got, c.want)
		}
		st = NewState()
		st.ApplyVolume(c.in)
		if got := st.Snap().Vol; got != c.want {
			t.Errorf("ApplyVolume(%d) -> %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSingleFieldReadings(t *testing.T) {
	st := NewState()
	st.ApplyVolume(43)
	st.ApplyMute(true)
	st.ApplyPlaying(true)
	st.ApplySource("BT")
	s := st.Snap()
	if s.Vol != 43 || !s.VolLive || !s.Muted || !s.Playing || !s.PlayKnown || s.Source != "BT" {
		t.Errorf("snapshot = %+v, want vol 43 live, muted, playing known, source BT", s)
	}
	st.ApplyMute(false)
	st.ApplyPlaying(false)
	if s := st.Snap(); s.Muted || s.Playing || !s.PlayKnown {
		t.Errorf("after MUT:0 PLA:0: muted=%v playing=%v known=%v", s.Muted, s.Playing, s.PlayKnown)
	}
}

// An empty source (a STA reply with no first field, or "SRC:") says nothing
// about the input: it must neither blank the source nor drop the track.
func TestEmptySourceKeepsTheCurrentOne(t *testing.T) {
	st := NewState()
	st.ApplySource("NET")
	st.ApplyTrackField(FieldTitle, "Big Bang")
	st.ApplyStatus("", false, 40, true)
	st.ApplySource("")
	st.ApplySource("\x1b\x07") // strips to ""
	s := st.Snap()
	if s.Source != "NET" {
		t.Errorf("source = %q, want NET kept", s.Source)
	}
	if s.Track == nil || s.Track.TrackName != "Big Bang" {
		t.Errorf("track = %+v, want Big Bang kept", s.Track)
	}
}

// A source change drops the track — what NET announced is not what BT plays
// — but the first source of the run (from "") and a repeat of the same one
// keep it. The comparison runs on the stripped value, so an injected control
// byte cannot fake a change.
func TestSourceChangeClearsTheTrack(t *testing.T) {
	cases := []struct {
		name      string
		first     string // "" = no source before the track
		next      string
		via       func(st *State, src string)
		wantTrack bool
	}{
		{"first source keeps", "", "NET", func(st *State, s string) { st.ApplySource(s) }, true},
		{"same source keeps", "NET", "NET", func(st *State, s string) { st.ApplySource(s) }, true},
		{"same after strip keeps", "NET", "N\x1bE\u202eT", func(st *State, s string) { st.ApplySource(s) }, true},
		{"SRC change clears", "NET", "BT", func(st *State, s string) { st.ApplySource(s) }, false},
		{"STA change clears", "NET", "LINE-IN", func(st *State, s string) { st.ApplyStatus(s, false, 30, true) }, false},
		{"STA same keeps", "NET", "NET", func(st *State, s string) { st.ApplyStatus(s, false, 30, true) }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := NewState()
			if c.first != "" {
				st.ApplySource(c.first)
			}
			st.ApplyTrackField(FieldTitle, "Big Bang")
			c.via(st, c.next)
			s := st.Snap()
			if got := s.Track != nil; got != c.wantTrack {
				t.Errorf("track kept = %v, want %v (track %+v)", got, c.wantTrack, s.Track)
			}
			if want := printable(c.next); s.Source != want {
				t.Errorf("source = %q, want %q", s.Source, want)
			}
		})
	}
}

// ---- echo holds -------------------------------------------------------------

// Every local change arms a hold for its documented duration; inside it the
// device's reading of the same value (a poll answered before the change
// landed) is ignored, and once it ends the device's reading applies again.
// The test rewinds the deadline instead of sleeping through the window.
func TestEchoHolds(t *testing.T) {
	type reading struct {
		name string
		do   func(st *State)
	}
	cases := []struct {
		name     string
		dur      time.Duration
		setup    func(st *State)
		local    func(st *State)
		deadline func(st *State) time.Time // read under st.mu
		expire   func(st *State)           // run under st.mu
		readings []reading
		read     func(st *State) any
		local0   any // the value right after the local change
		device   any // the device's reading, once the hold is over
	}{
		{
			name: "volume", dur: VolHoldDuration,
			local:    func(st *State) { st.SetVol(20) },
			deadline: func(st *State) time.Time { return st.volHold },
			expire:   func(st *State) { st.volHold = time.Now().Add(-time.Millisecond) },
			readings: []reading{
				{"VOL", func(st *State) { st.ApplyVolume(44) }},
				{"STA", func(st *State) { st.ApplyStatus("NET", false, 44, false) }},
			},
			read:   func(st *State) any { return st.Snap().Vol },
			local0: 20, device: 44,
		},
		{
			name: "volume step", dur: VolHoldDuration,
			setup:    func(st *State) { st.ApplyVolume(50) },
			local:    func(st *State) { st.AdjustVol(-10) },
			deadline: func(st *State) time.Time { return st.volHold },
			expire:   func(st *State) { st.volHold = time.Now().Add(-time.Millisecond) },
			readings: []reading{{"VOL", func(st *State) { st.ApplyVolume(50) }}},
			read:     func(st *State) any { return st.Snap().Vol },
			local0:   40, device: 50,
		},
		{
			name: "mute", dur: MuteHoldDuration,
			local:    func(st *State) { st.ToggleMute() },
			deadline: func(st *State) time.Time { return st.muteHold },
			expire:   func(st *State) { st.muteHold = time.Now().Add(-time.Millisecond) },
			readings: []reading{
				{"MUT", func(st *State) { st.ApplyMute(false) }},
				{"STA", func(st *State) { st.ApplyStatus("NET", false, 30, false) }},
			},
			read:   func(st *State) any { return st.Snap().Muted },
			local0: true, device: false,
		},
		{
			name: "play toggle", dur: PlayHoldDuration,
			local:    func(st *State) { st.ToggleOptimistic() },
			deadline: func(st *State) time.Time { return st.playHold },
			expire:   func(st *State) { st.playHold = time.Now().Add(-time.Millisecond) },
			readings: []reading{
				{"PLA", func(st *State) { st.ApplyPlaying(false) }},
				{"STA", func(st *State) { st.ApplyStatus("NET", false, 30, false) }},
			},
			read:   func(st *State) any { return st.Snap().Playing },
			local0: true, device: false,
		},
		{
			name: "pause", dur: PlayHoldDuration,
			setup:    func(st *State) { st.ApplyPlaying(true) },
			local:    func(st *State) { st.PauseOptimistic() },
			deadline: func(st *State) time.Time { return st.playHold },
			expire:   func(st *State) { st.playHold = time.Now().Add(-time.Millisecond) },
			readings: []reading{
				{"PLA", func(st *State) { st.ApplyPlaying(true) }},
				{"STA", func(st *State) { st.ApplyStatus("NET", false, 30, true) }},
			},
			read:   func(st *State) any { return st.Snap().Playing },
			local0: false, device: true,
		},
		{
			name: "EQ control", dur: EQHoldDuration,
			local:    func(st *State) { st.SetEQLocal("BAS", 3) },
			deadline: func(st *State) time.Time { return st.eqHold["BAS"] },
			expire:   func(st *State) { st.eqHold["BAS"] = time.Now().Add(-time.Millisecond) },
			readings: []reading{{"BAS", func(st *State) { st.ApplyTunnel("BAS", -2) }}},
			read: func(st *State) any {
				v, _ := st.EQValue("BAS")
				return v
			},
			local0: 3, device: -2,
		},
	}
	for _, c := range cases {
		for _, r := range c.readings {
			t.Run(c.name+"/"+r.name, func(t *testing.T) {
				st := NewState()
				if c.setup != nil {
					c.setup(st)
				}
				before := time.Now()
				c.local(st)
				after := time.Now()
				locked(st, func() { armedFor(t, c.deadline(st), before, after, c.dur) })
				if got := c.read(st); got != c.local0 {
					t.Fatalf("after the local change: %v, want %v", got, c.local0)
				}
				r.do(st)
				if got := c.read(st); got != c.local0 {
					t.Errorf("inside the hold the device's %s overrode the local change: %v, want %v", r.name, got, c.local0)
				}
				locked(st, func() { c.expire(st) })
				r.do(st)
				if got := c.read(st); got != c.device {
					t.Errorf("after the hold the device's %s was ignored: %v, want %v", r.name, got, c.device)
				}
			})
		}
	}
}

// A held reading still proves the device answered: VolLive and PlayKnown
// turn true even when the value itself is suppressed, so a volume step is
// never computed from a cached level once the device has spoken.
func TestHeldReadingStillMarksLive(t *testing.T) {
	st := NewState()
	st.Preload(30)
	st.SetVol(20)
	st.ToggleOptimistic()
	st.ApplyStatus("NET", false, 44, false)
	s := st.Snap()
	if !s.VolLive || !s.PlayKnown {
		t.Errorf("VolLive=%v PlayKnown=%v, want both true after a held STA", s.VolLive, s.PlayKnown)
	}
	if s.Vol != 20 || !s.Playing {
		t.Errorf("vol=%d playing=%v, want the held 20/true", s.Vol, s.Playing)
	}
}

// The holds are per value: a held mute does not stop the same STA reply from
// applying the volume, the play state and the source.
func TestHoldsAreIndependent(t *testing.T) {
	st := NewState()
	st.ToggleMute() // muted, held
	st.ApplyStatus("BT", false, 31, true)
	s := st.Snap()
	if !s.Muted {
		t.Error("the mute hold was ignored")
	}
	if s.Vol != 31 || !s.Playing || s.Source != "BT" {
		t.Errorf("vol=%d playing=%v source=%q, want 31 true BT (not held)", s.Vol, s.Playing, s.Source)
	}
	// and an EQ hold is per control
	st.SetEQLocal("BAS", 3)
	st.ApplyTunnel("BAS", 0)
	st.ApplyTunnel("TRE", 5)
	if v, _ := st.EQValue("BAS"); v != 3 {
		t.Errorf("BAS = %d, want the held 3", v)
	}
	if v, ok := st.EQValue("TRE"); !ok || v != 5 {
		t.Errorf("TRE = (%d, %v), want (5, true): another control's hold must not block it", v, ok)
	}
}

// ---- the volume bridge ------------------------------------------------------

// expireVolHold ends the local volume hold as if VolHoldDuration had passed.
func expireVolHold(st *State) {
	locked(st, func() { st.volHold = time.Now().Add(-time.Millisecond) })
}

// takeBridge asserts what TakeVolumeBridge hands over, and that it hands it
// over once.
func takeBridge(t *testing.T, st *State, want int, wantOK bool) {
	t.Helper()
	v, ok := st.TakeVolumeBridge()
	if ok != wantOK || (ok && v != want) {
		if wantOK {
			t.Errorf("TakeVolumeBridge = (%d, %v), want (%d, true)", v, ok, want)
		} else {
			t.Errorf("TakeVolumeBridge = (%d, %v), want nothing pending", v, ok)
		}
	}
	if ok {
		if v2, ok2 := st.TakeVolumeBridge(); ok2 {
			t.Errorf("the bridge was handed over twice (second: %d)", v2)
		}
	}
}

// On AR241CP_8747 a level the device reports but lp10 did not set is re-sent
// through the tunnel (see TakeVolumeBridge): the first reading of each
// connection, and every later reading that differs from the last one —
// unless it lands inside lp10's own echo hold.
func TestVolumeBridge(t *testing.T) {
	type step struct {
		do     func(st *State)
		want   int
		wantOK bool
	}
	vol := func(v int) func(st *State) { return func(st *State) { st.ApplyVolume(v) } }
	sta := func(v int) func(st *State) { return func(st *State) { st.ApplyStatus("NET", false, v, true) } }
	cases := []struct {
		name  string
		steps []step
	}{
		{"nothing read", []step{{func(*State) {}, 0, false}}},
		{"first VOL bridges", []step{{vol(30), 30, true}}},
		{"first STA bridges", []step{{sta(44), 44, true}}},
		{"same reading again does not", []step{{vol(30), 30, true}, {vol(30), 0, false}, {sta(30), 0, false}}},
		{"a different reading bridges", []step{{vol(30), 30, true}, {vol(35), 35, true}, {sta(20), 20, true}}},
		{"the latest pending wins", []step{{func(st *State) { st.ApplyVolume(30); st.ApplyVolume(31) }, 31, true}}},
		{"clamped high", []step{{vol(250), 100, true}, {vol(100), 0, false}, {sta(150), 0, false}}},
		{"clamped low", []step{{vol(-5), 0, true}, {vol(0), 0, false}}},
		{"clamped extreme", []step{{vol(math.MinInt), 0, true}, {vol(math.MaxInt), 100, true}}},
		{"a preload is not a reading", []step{{func(st *State) { st.Preload(30) }, 0, false}, {vol(30), 30, true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := NewState()
			for _, s := range c.steps {
				s.do(st)
				takeBridge(t, st, s.want, s.wantOK)
			}
		})
	}
}

// A reading inside the local hold is lp10's own level coming back: it does
// not bridge, but it is still the device's latest level, so the same echo
// after the hold does not bridge either.
func TestVolumeBridgeSkipsTheLocalHold(t *testing.T) {
	for _, local := range []struct {
		name string
		do   func(st *State)
	}{
		{"SetVol", func(st *State) { st.SetVol(40) }},
		{"AdjustVol", func(st *State) { st.AdjustVol(10) }},
	} {
		t.Run(local.name, func(t *testing.T) {
			st := NewState()
			st.ApplyVolume(30)
			takeBridge(t, st, 30, true)
			local.do(st) // 40, held
			st.ApplyVolume(40)
			takeBridge(t, st, 0, false)
			st.ApplyStatus("NET", false, 40, true)
			takeBridge(t, st, 0, false)
			expireVolHold(st)
			st.ApplyVolume(40)
			takeBridge(t, st, 0, false)
			if got := st.Snap().Vol; got != 40 {
				t.Errorf("Vol = %d, want 40", got)
			}
			// a change made elsewhere after the hold bridges again
			st.ApplyVolume(25)
			takeBridge(t, st, 25, true)
		})
	}
}

// A local set replaces a level still waiting to be bridged: the worker takes
// the bridge after the key's own VOL write, so an older device level sent
// then would undo the step the user just made.
func TestLocalVolumeCancelsAPendingBridge(t *testing.T) {
	for _, local := range []struct {
		name string
		do   func(st *State)
	}{
		{"SetVol", func(st *State) { st.SetVol(35) }},
		{"AdjustVol", func(st *State) { st.AdjustVol(5) }},
		{"AdjustVol at a bound", func(st *State) { st.AdjustVol(math.MinInt) }},
	} {
		t.Run(local.name, func(t *testing.T) {
			st := NewState()
			st.ApplyVolume(30) // pending, not yet taken
			local.do(st)
			takeBridge(t, st, 0, false)
		})
	}
}

// A stale reading inside the hold updates the device's last level too, so the
// level the device settles on after the hold differs from it and bridges —
// a harmless re-send of lp10's own level.
func TestVolumeBridgeAfterAStaleEchoInTheHold(t *testing.T) {
	st := NewState()
	st.ApplyVolume(30)
	takeBridge(t, st, 30, true)
	st.SetVol(40)
	st.ApplyVolume(30) // answered before the set landed
	takeBridge(t, st, 0, false)
	expireVolHold(st)
	st.ApplyVolume(40)
	takeBridge(t, st, 40, true)
}

// A new connection starts the bridge over: the pending level of the old one
// is dropped (it may never have been written), and the first reading of the
// new one bridges even when it repeats the old level.
func TestVolumeBridgeRestartsPerConnection(t *testing.T) {
	st := NewState()
	st.StartConnection()
	st.ApplyVolume(30)
	st.StartConnection()
	takeBridge(t, st, 0, false)
	st.ApplyVolume(30)
	takeBridge(t, st, 30, true)
	st.ApplyVolume(30)
	takeBridge(t, st, 0, false)
	st.Disconnect() // only a new attempt restarts it
	st.ApplyVolume(30)
	takeBridge(t, st, 0, false)
}

// ---- the track --------------------------------------------------------------

func TestApplyTrackField(t *testing.T) {
	st := NewState()
	st.ApplyTrackField(FieldTitle, "Big Bang")
	st.ApplyTrackField(FieldArtist, "Usted Señalemelo")
	st.ApplyTrackField(FieldAlbum, "Big Bang")
	want := Track{TrackName: "Big Bang", Artist: "Usted Señalemelo", Album: "Big Bang"}
	if got := st.Snap().Track; got == nil || *got != want {
		t.Fatalf("track = %+v, want %+v", got, want)
	}
	// a title starts a new track: the last one's artist and album must not
	// linger beside it
	st.ApplyTrackField(FieldTitle, "Otra")
	if got := st.Snap().Track; got == nil || *got != (Track{TrackName: "Otra"}) {
		t.Errorf("after a new TIT: %+v, want only the title", got)
	}
	st.ApplyTrackField(FieldAlbum, "Disco")
	if got := st.Snap().Track; got == nil || *got != (Track{TrackName: "Otra", Album: "Disco"}) {
		t.Errorf("ALB did not fill in the current track: %+v", got)
	}
}

// Should a title ever be missing, an artist or an album still starts a track.
func TestArtistOrAlbumStartsATrack(t *testing.T) {
	for _, field := range []string{FieldArtist, FieldAlbum} {
		st := NewState()
		st.ApplyTrackField(field, "X")
		got := st.Snap().Track
		if got == nil {
			t.Fatalf("%s with no track: still nil", field)
		}
		if (field == FieldArtist && got.Artist != "X") || (field == FieldAlbum && got.Album != "X") || got.TrackName != "" {
			t.Errorf("%s started %+v", field, got)
		}
	}
}

func TestUnknownTrackFieldIgnored(t *testing.T) {
	st := NewState()
	st.ApplyTrackField("XYZ", "junk")
	st.ApplyTrackField("RAW", "NEXT")
	if got := st.Snap().Track; got != nil {
		t.Fatalf("an unknown field started a track: %+v", got)
	}
	st.ApplyTrackField(FieldTitle, "Big Bang")
	before := st.Snap().Track
	st.ApplyTrackField("tit", "lowercase") // codes are case-sensitive
	if after := st.Snap().Track; after != before || *after != (Track{TrackName: "Big Bang"}) {
		t.Errorf("an unknown field changed the track: %+v -> %+v", before, after)
	}
}

// A published *Track is never mutated: the TUI may still be rendering a
// Snapshot taken before the next field arrived, without the lock.
func TestPublishedTrackIsNeverMutated(t *testing.T) {
	st := NewState()
	st.ApplyTrackField(FieldTitle, "Big Bang")
	old := st.Snap()
	st.ApplyTrackField(FieldArtist, "Usted Señalemelo")
	st.ApplyTrackField(FieldAlbum, "Big Bang")
	st.ApplyVendor("spotify")
	st.ApplyTrackField(FieldTitle, "Otra")
	if *old.Track != (Track{TrackName: "Big Bang"}) {
		t.Errorf("an earlier Snapshot's track changed: %+v", *old.Track)
	}
	if st.Snap().Track == old.Track {
		t.Error("the State still publishes the old pointer")
	}
}

func TestApplyVendorNamesTheService(t *testing.T) {
	st := NewState()
	st.ApplyTrackField(FieldTitle, "Big Bang")
	old := st.Snap().Track
	st.ApplyVendor("spotify")
	s := st.Snap()
	if s.Service != "spotify" {
		t.Errorf("Service = %q, want spotify", s.Service)
	}
	if s.Track == nil || s.Track.Service != "spotify" || s.Track.TrackName != "Big Bang" {
		t.Errorf("current track = %+v, want Big Bang on spotify", s.Track)
	}
	if old.Service != "" {
		t.Errorf("ApplyVendor mutated the published track: %+v", old)
	}
	// the same word again republishes nothing
	cur := st.Snap().Track
	st.ApplyVendor("spotify")
	if st.Snap().Track != cur {
		t.Error("an unchanged vendor replaced the track")
	}
	// the next tracks carry it, a title included
	st.ApplyTrackField(FieldTitle, "Otra")
	st.ApplyTrackField(FieldArtist, "Alguien")
	if got := st.Snap().Track; got.Service != "spotify" {
		t.Errorf("next track's service = %q, want spotify", got.Service)
	}
	// a new vendor renames the current one
	st.ApplyVendor("tidal")
	if got := st.Snap().Track; got.Service != "tidal" || got.Artist != "Alguien" {
		t.Errorf("after VND:tidal: %+v", got)
	}
}

// A vendor before any track names the service only; the first track then
// carries it.
func TestApplyVendorWithoutATrack(t *testing.T) {
	st := NewState()
	st.ApplyVendor("spotify")
	s := st.Snap()
	if s.Track != nil || s.Service != "spotify" {
		t.Fatalf("track=%+v service=%q, want nil and spotify", s.Track, s.Service)
	}
	st.ApplyTrackField(FieldArtist, "X")
	if got := st.Snap().Track; got.Service != "spotify" {
		t.Errorf("first track's service = %q, want spotify", got.Service)
	}
}

func TestApplyVersion(t *testing.T) {
	st := NewState()
	st.ApplyVersion("29-1d316f0c-10")
	if got := st.DiagnosticView().MCU; got != "29-1d316f0c-10" {
		t.Errorf("MCU = %q", got)
	}
}

// ---- control-stripping at every device-string entry -------------------------

// Every string the device (or the vendor) supplies passes printable on its way
// into State: an ESC, a BEL, a C1 CSI (U+009B) or a bidi override in any of
// them must never reach the terminal.
func TestEveryDeviceStringIsStripped(t *testing.T) {
	const hostile = "a\x1b[2J\x07\u009b\u202eb\u2066c\u2028"
	const want = "a[2Jbc"
	cases := []struct {
		name  string
		apply func(st *State, s string)
		read  func(st *State) string
	}{
		{"STA source", func(st *State, s string) { st.ApplyStatus(s, false, 1, false) },
			func(st *State) string { return st.Snap().Source }},
		{"SRC", func(st *State, s string) { st.ApplySource(s) },
			func(st *State) string { return st.Snap().Source }},
		{"TIT", func(st *State, s string) { st.ApplyTrackField(FieldTitle, s) },
			func(st *State) string { return st.Snap().Track.TrackName }},
		{"ART", func(st *State, s string) { st.ApplyTrackField(FieldArtist, s) },
			func(st *State) string { return st.Snap().Track.Artist }},
		{"ALB", func(st *State, s string) { st.ApplyTrackField(FieldAlbum, s) },
			func(st *State) string { return st.Snap().Track.Album }},
		{"VND", func(st *State, s string) { st.ApplyVendor(s) },
			func(st *State) string { return st.Snap().Service }},
		{"VND on the track", func(st *State, s string) { st.ApplyTrackField(FieldTitle, "t"); st.ApplyVendor(s) },
			func(st *State) string { return st.Snap().Track.Service }},
		{"VND carried", func(st *State, s string) { st.ApplyVendor(s); st.ApplyTrackField(FieldTitle, "t") },
			func(st *State) string { return st.Snap().Track.Service }},
		{"VER", func(st *State, s string) { st.ApplyVersion(s) },
			func(st *State) string { return st.DiagnosticView().MCU }},
		{"Note", func(st *State, s string) { st.Note(s) },
			func(st *State) string { return st.Snap().Error }},
		{"LSSDP FW", func(st *State, s string) { st.SetLSSDP(&LSSDPInfo{FW: s}) },
			func(st *State) string { return st.DiagnosticView().LSSDP.FW }},
		{"LSSDP State", func(st *State, s string) { st.SetLSSDP(&LSSDPInfo{State: s}) },
			func(st *State) string { return st.DiagnosticView().LSSDP.State }},
		{"LSSDP NetMode", func(st *State, s string) { st.SetLSSDP(&LSSDPInfo{NetMode: s}) },
			func(st *State) string { return st.DiagnosticView().LSSDP.NetMode }},
		{"ZC StatusString", func(st *State, s string) { st.SetSpotifyZC(&SpotifyZC{StatusString: s}, 1) },
			func(st *State) string { return st.DiagnosticView().SpotifyZC.StatusString }},
		{"ZC ActiveUser", func(st *State, s string) { st.SetSpotifyZC(&SpotifyZC{ActiveUser: s}, 1) },
			func(st *State) string { return st.DiagnosticView().SpotifyZC.ActiveUser }},
		{"ZC LibraryVersion", func(st *State, s string) { st.SetSpotifyZC(&SpotifyZC{LibraryVersion: s}, 1) },
			func(st *State) string { return st.DiagnosticView().SpotifyZC.LibraryVersion }},
		{"OTA Asked", func(st *State, s string) { st.SetOTA(OTAInfo{Asked: s}) },
			func(st *State) string { return st.DiagnosticView().OTA.Asked }},
		{"OTA Offered", func(st *State, s string) { st.SetOTA(OTAInfo{Offered: s}) },
			func(st *State) string { return st.DiagnosticView().OTA.Offered }},
		{"OTA Err", func(st *State, s string) { st.SetOTA(OTAInfo{Err: s}) },
			func(st *State) string { return st.DiagnosticView().OTA.Err }},
		{"OTA PackageURL", func(st *State, s string) { st.SetOTA(OTAInfo{PackageURL: s}) },
			func(st *State) string { return st.DiagnosticView().OTA.PackageURL }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := NewState()
			c.apply(st, hostile)
			got := c.read(st)
			if got != want {
				t.Errorf("stored %+q, want %+q", got, want)
			}
			assertClean(t, c.name, got)
		})
	}
}

// ---- optimistic play/pause --------------------------------------------------

func TestToggleOptimistic(t *testing.T) {
	st := NewState()
	if was := st.ToggleOptimistic(); was {
		t.Error("a fresh State reported it WAS playing")
	}
	if !st.Snap().Playing {
		t.Error("the toggle did not start playback on screen")
	}
	if was := st.ToggleOptimistic(); !was {
		t.Error("the second toggle reported it was NOT playing")
	}
	if st.Snap().Playing {
		t.Error("the second toggle did not pause on screen")
	}
}

// PauseOptimistic is one-way: a paused player is never "paused" again (that
// would send POP, a toggle, and resume it) and arms no hold. With no play
// state read on this connection it decides nothing and says so.
func TestPauseOptimisticIsOneWay(t *testing.T) {
	st := NewState()
	if pause, known := st.PauseOptimistic(); pause || known {
		t.Fatalf("no play state read: (%v, %v), want (false, false)", pause, known)
	}
	st.ApplyPlaying(false)
	if pause, known := st.PauseOptimistic(); pause || !known {
		t.Fatal("a paused player must not be paused again (POP would resume it)")
	}
	locked(st, func() {
		if !st.playHold.IsZero() {
			t.Error("a refused pause armed the play hold")
		}
	})
	if st.Snap().Playing {
		t.Fatal("a refused pause changed the play state")
	}
	st.ApplyPlaying(true)
	if pause, _ := st.PauseOptimistic(); !pause {
		t.Fatal("a playing player must pause")
	}
	if st.Snap().Playing {
		t.Error("Playing is still true after the pause")
	}
	if pause, _ := st.PauseOptimistic(); pause {
		t.Error("the second call must be a no-op: one-shot, never a resume")
	}
}

// A new connection forgets the play state as a basis for the pause: the
// room may have paused during the outage, and "playing" from before it would
// make the timer's POP a resume.
func TestPauseOptimisticWaitsForTheNewConnection(t *testing.T) {
	st := NewState()
	st.StartConnection()
	st.ApplyStatus("NET", false, 44, true)
	st.Disconnect()
	st.StartConnection()
	st.Received()
	if pause, known := st.PauseOptimistic(); pause || known || !st.Snap().Playing || st.Snap().PlayKnown {
		t.Errorf("before the new link's play state: (%v, %v), snap %+v", pause, known, st.Snap())
	}
	st.ApplyStatus("NET", false, 44, true)
	if pause, known := st.PauseOptimistic(); !pause || !known {
		t.Errorf("after the new link's STA: (%v, %v), want (true, true)", pause, known)
	}
}

// ---- volume / mute ----------------------------------------------------------

func TestSetVolClampsAndReturns(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{44, 44}, {0, 0}, {100, 100}, {150, 100}, {-5, 0}, {math.MaxInt, 100}, {math.MinInt, 0},
	} {
		st := NewState()
		if got := st.SetVol(c.in); got != c.want {
			t.Errorf("SetVol(%d) = %d, want %d", c.in, got, c.want)
		}
		if got := st.Snap().Vol; got != c.want {
			t.Errorf("SetVol(%d): Snap().Vol = %d, want %d", c.in, got, c.want)
		}
	}
}

// AdjustVol never computes cur+delta when that could overflow: an extreme
// delta from any level lands on the bound, not on a wrapped negative.
func TestAdjustVolGuardsOverflow(t *testing.T) {
	cases := []struct{ cur, delta, want int }{
		{50, 2, 52},
		{50, -4, 46},
		{50, 0, 50},
		{50, 50, 100}, // exactly to the top
		{50, -50, 0},  // exactly to the bottom
		{50, 51, 100},
		{50, -51, 0},
		{99, 5, 100},
		{1, -5, 0},
		{0, math.MaxInt, 100},
		{50, math.MaxInt, 100},
		{100, math.MaxInt, 100},
		{0, math.MinInt, 0},
		{50, math.MinInt, 0},
		{100, math.MinInt, 0},
		{100, -1, 99},
		{0, 1, 1},
	}
	for _, c := range cases {
		st := NewState()
		st.SetVol(c.cur)
		if got := st.AdjustVol(c.delta); got != c.want {
			t.Errorf("from %d AdjustVol(%d) = %d, want %d", c.cur, c.delta, got, c.want)
		}
		if got := st.Snap().Vol; got != c.want {
			t.Errorf("from %d AdjustVol(%d): Snap().Vol = %d, want %d", c.cur, c.delta, got, c.want)
		}
	}
}

func TestClamp100(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{-1, 0}, {0, 0}, {1, 1}, {99, 99}, {100, 100}, {101, 100}, {math.MinInt, 0}, {math.MaxInt, 100},
	} {
		if got := clamp100(c.in); got != c.want {
			t.Errorf("clamp100(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestToggleMute(t *testing.T) {
	st := NewState()
	if !st.ToggleMute() {
		t.Error("the first toggle must return the new state, muted")
	}
	if !st.Snap().Muted {
		t.Error("Snap().Muted is false after muting")
	}
	if st.ToggleMute() {
		t.Error("the second toggle must return unmuted")
	}
	if st.Snap().Muted {
		t.Error("Snap().Muted is true after unmuting")
	}
}

// Preload seeds the cached level for the first paint: clamped, and not live
// — the device has not spoken yet.
func TestPreloadClampsAndIsNotLive(t *testing.T) {
	for _, c := range []struct{ in, want int }{{44, 44}, {150, 100}, {-3, 0}, {math.MinInt, 0}} {
		st := NewState()
		st.Preload(c.in)
		s := st.Snap()
		if s.Vol != c.want {
			t.Errorf("Preload(%d): Vol = %d, want %d", c.in, s.Vol, c.want)
		}
		if s.VolLive {
			t.Errorf("Preload(%d) marked the volume live", c.in)
		}
	}
	// it arms no hold: the device's first reading replaces it at once
	st := NewState()
	st.Preload(30)
	st.ApplyVolume(44)
	if got := st.Snap().Vol; got != 44 {
		t.Errorf("the device's reading after a preload: %d, want 44", got)
	}
}

// ---- EQ / tone control state ------------------------------------------------

func TestEQValuesAndPreload(t *testing.T) {
	st := NewState()
	seed := map[string]int{"BAS": 2, "TRE": -1}
	st.PreloadEQ(seed)
	seed["BAS"] = 9 // the caller's map is copied, not kept
	if v, ok := st.EQValue("BAS"); !ok || v != 2 {
		t.Errorf("EQValue(BAS) = (%d, %v), want (2, true)", v, ok)
	}
	// a preload arms no hold: the device's seed overwrites it at once
	st.ApplyTunnel("TRE", 4)
	if v, _ := st.EQValue("TRE"); v != 4 {
		t.Errorf("TRE = %d after the device's reading, want 4", v)
	}
	if v, ok := st.EQValue("MID"); ok {
		t.Errorf("EQValue(MID) = (%d, true), never read", v)
	}
	// a preload merges: a known value it does not name stays
	st.PreloadEQ(map[string]int{"MID": 1})
	if v, _ := st.EQValue("TRE"); v != 4 {
		t.Errorf("PreloadEQ dropped TRE: %d", v)
	}
}

// EQView and EQPresets hand out copies: a caller mutating the result (the TUI
// rendering it) must never change the State the workers write.
func TestEQReadersReturnCopies(t *testing.T) {
	st := NewState()
	st.ApplyTunnel("BAS", 3)
	st.Received()
	connected, vals := st.EQView()
	if !connected || vals["BAS"] != 3 {
		t.Fatalf("EQView = (%v, %v), want (true, BAS:3)", connected, vals)
	}
	vals["BAS"] = 99
	vals["NEW"] = 1
	if v, _ := st.EQValue("BAS"); v != 3 {
		t.Errorf("mutating EQView's map changed BAS to %d", v)
	}
	if _, ok := st.EQValue("NEW"); ok {
		t.Error("mutating EQView's map added a control")
	}

	names := []string{"Flat", "Classical", "Pop"}
	st.SetEQPresets(names)
	names[0] = "Mutated" // the caller's slice is copied, not kept
	got := st.EQPresets()
	if !slices.Equal(got, []string{"Flat", "Classical", "Pop"}) {
		t.Fatalf("EQPresets = %q", got)
	}
	got[1] = "Mutated"
	if again := st.EQPresets(); again[1] != "Classical" {
		t.Errorf("mutating EQPresets' result changed the State: %q", again)
	}
	st.SetEQPresets(nil)
	if st.EQPresets() != nil {
		t.Error("SetEQPresets(nil) left names behind")
	}
}

// ---- errors -----------------------------------------------------------------

func TestNoteRecordsTheMessageAndTime(t *testing.T) {
	st := NewState()
	before := time.Now()
	st.Note("dial tcp: refused")
	after := time.Now()
	s := st.Snap()
	if s.Error != "dial tcp: refused" {
		t.Errorf("Error = %q", s.Error)
	}
	if s.ErrorAt.Before(before) || s.ErrorAt.After(after) {
		t.Errorf("ErrorAt = %v, want within the call", s.ErrorAt)
	}
}

// ---- connection liveness ----------------------------------------------------

func TestConnectionLiveness(t *testing.T) {
	st := NewState()
	st.StartConnection()
	if s := st.Snap(); s.Attempts != 1 || s.Connected {
		t.Errorf("after the first attempt: attempts=%d connected=%v, want 1 false", s.Attempts, s.Connected)
	}
	before := time.Now()
	st.Received()
	after := time.Now()
	if !st.Snap().Connected {
		t.Error("a parsed frame did not mark the link connected")
	}
	if c, _ := st.EQView(); !c {
		t.Error("EQView does not share the link state")
	}
	rx := st.LastRx()
	if rx.Before(before) || rx.After(after) {
		t.Errorf("LastRx = %v, want within the Received call", rx)
	}
	if d := st.DiagnosticView().LastRx; !d.Equal(rx) {
		t.Errorf("DiagnosticView().LastRx = %v, want %v", d, rx)
	}

	st.Disconnect()
	st.Disconnect() // idempotent
	if st.Snap().Connected {
		t.Error("Disconnect left the link connected")
	}
	if c, _ := st.EQView(); c {
		t.Error("EQView still connected after Disconnect")
	}
	if !st.LastRx().Equal(rx) {
		t.Error("Disconnect reset LastRx; only a new attempt does")
	}

	// a new attempt counts and forgets the last connection's latest frame
	st.StartConnection()
	if s := st.Snap(); s.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", s.Attempts)
	}
	if !st.LastRx().IsZero() {
		t.Error("StartConnection kept the old connection's LastRx")
	}
}

// ---- probes -----------------------------------------------------------------

func TestSetLSSDP(t *testing.T) {
	st := NewState()
	t0 := time.Now()
	st.SetLSSDP(nil) // an unanswered probe
	d := st.DiagnosticView()
	if d.LSSDP != nil || d.Snapshot.LSSDPAlive {
		t.Errorf("an unanswered probe reads alive: %+v", d.LSSDP)
	}
	if d.LSSDPProbeAt.Before(t0) || !d.LSSDPOKAt.IsZero() || !d.Snapshot.LSSDPAt.IsZero() {
		t.Errorf("probeAt=%v okAt=%v, want a probe time and no answer time", d.LSSDPProbeAt, d.LSSDPOKAt)
	}

	in := &LSSDPInfo{FW: "AR241CP_8747.29.2", State: "0", NetMode: "1"}
	st.SetLSSDP(in)
	in.FW = "mutated" // the caller's struct is copied, not kept
	d = st.DiagnosticView()
	if d.LSSDP == nil || *d.LSSDP != (LSSDPInfo{FW: "AR241CP_8747.29.2", State: "0", NetMode: "1"}) {
		t.Fatalf("LSSDP = %+v", d.LSSDP)
	}
	if !d.Snapshot.LSSDPAlive || d.LSSDPOKAt.IsZero() || !d.LSSDPOKAt.Equal(d.LSSDPProbeAt) {
		t.Errorf("an answer: alive=%v okAt=%v probeAt=%v, want alive and equal times",
			d.Snapshot.LSSDPAlive, d.LSSDPOKAt, d.LSSDPProbeAt)
	}
	if s := st.Snap(); !s.LSSDPAt.Equal(d.LSSDPOKAt) || !s.LSSDPProbeAt.Equal(d.LSSDPProbeAt) {
		t.Errorf("Snap's LSSDP times %v/%v differ from the diagnostics' %v/%v",
			s.LSSDPAt, s.LSSDPProbeAt, d.LSSDPOKAt, d.LSSDPProbeAt)
	}
	okAt := d.LSSDPOKAt

	// a later miss drops the answer but keeps when the last one landed
	st.SetLSSDP(nil)
	d = st.DiagnosticView()
	if d.LSSDP != nil || d.Snapshot.LSSDPAlive {
		t.Error("a missed probe kept the old answer")
	}
	if !d.LSSDPOKAt.Equal(okAt) || d.LSSDPProbeAt.Before(okAt) {
		t.Errorf("okAt=%v probeAt=%v, want the last answer's time kept and a newer probe", d.LSSDPOKAt, d.LSSDPProbeAt)
	}
}

func TestSetSpotifyZC(t *testing.T) {
	st := NewState()
	st.SetSpotifyZC(nil, 0) // not even found over mDNS
	d := st.DiagnosticView()
	if d.SpotifyZC != nil || d.ZCPort != 0 || d.ZCProbeAt.IsZero() || !d.ZCOKAt.IsZero() {
		t.Errorf("unfound: zc=%+v port=%d probeAt=%v okAt=%v", d.SpotifyZC, d.ZCPort, d.ZCProbeAt, d.ZCOKAt)
	}

	in := &SpotifyZC{StatusString: "OK", ActiveUser: "lucas", LibraryVersion: "3.240.0"}
	st.SetSpotifyZC(in, 4070)
	in.ActiveUser = "mutated"
	d = st.DiagnosticView()
	if d.SpotifyZC == nil || *d.SpotifyZC != (SpotifyZC{StatusString: "OK", ActiveUser: "lucas", LibraryVersion: "3.240.0"}) {
		t.Fatalf("SpotifyZC = %+v", d.SpotifyZC)
	}
	if d.ZCPort != 4070 || d.ZCOKAt.IsZero() || !d.ZCOKAt.Equal(d.ZCProbeAt) {
		t.Errorf("answered: port=%d okAt=%v probeAt=%v", d.ZCPort, d.ZCOKAt, d.ZCProbeAt)
	}
	okAt := d.ZCOKAt

	// found over mDNS but the endpoint did not answer
	st.SetSpotifyZC(nil, 4070)
	d = st.DiagnosticView()
	if d.SpotifyZC != nil || d.ZCPort != 4070 || !d.ZCOKAt.Equal(okAt) {
		t.Errorf("unanswered: zc=%+v port=%d okAt=%v (want nil, 4070, %v)", d.SpotifyZC, d.ZCPort, d.ZCOKAt, okAt)
	}
}

func TestProbeQuiet(t *testing.T) {
	st := NewState()
	if !st.ProbeWanted() {
		t.Fatal("probes are wanted by default")
	}
	st.SetProbeQuiet(true)
	if st.ProbeWanted() {
		t.Error("a quiet State still wants probes")
	}
	st.SetProbeQuiet(false)
	if !st.ProbeWanted() {
		t.Error("probes stay off after quiet is lowered")
	}
}

// ---- firmware update check --------------------------------------------------

func TestTakeOTARequestWithoutARequest(t *testing.T) {
	st := NewState()
	st.SetLSSDP(&LSSDPInfo{FW: "AR241CP_8747.29.2"})
	if build, ok := st.TakeOTARequest(); ok || build != "" {
		t.Errorf("TakeOTARequest with nothing asked = (%q, %v)", build, ok)
	}
	if st.DiagnosticView().OTAPending {
		t.Error("pending with nothing asked")
	}
}

// The request is handed over only once an LSSDP answer names a build of the
// shape the manifest is asked about; until then it stays pending (the view
// keeps saying "checking…") instead of a failure nothing retries.
func TestTakeOTARequestNeedsABuildShapedFirmware(t *testing.T) {
	cases := []struct {
		name  string
		lssdp *LSSDPInfo // nil: no answer
		build string     // "" = held back
	}{
		{"no LSSDP answer", nil, ""},
		{"empty FW", &LSSDPInfo{}, ""},
		{"garbage", &LSSDPInfo{FW: "garbage"}, ""},
		{"lowercase", &LSSDPInfo{FW: "ar241cp_8747.29.2"}, ""},
		{"no build number", &LSSDPInfo{FW: "AR241CP_.29.2"}, ""},
		{"prefix too short", &LSSDPInfo{FW: "A_8747.29"}, ""},
		{"prefix too long", &LSSDPInfo{FW: "ABCDEFGHIJKLM_8747.29"}, ""},
		{"build too long", &LSSDPInfo{FW: "AR241CP_123456789.29"}, ""},
		{"letters in the build", &LSSDPInfo{FW: "AR241CP_87a7.29"}, ""},
		{"leading dot", &LSSDPInfo{FW: ".AR241CP_8747"}, ""},
		{"space", &LSSDPInfo{FW: "AR241CP 8747.29"}, ""},
		{"full FW", &LSSDPInfo{FW: "AR241CP_8747.29.2"}, "AR241CP_8747"},
		{"build only", &LSSDPInfo{FW: "AR241CP_8747"}, "AR241CP_8747"},
		{"older box", &LSSDPInfo{FW: "AR241CE_8530.23.2"}, "AR241CE_8530"},
		{"stripped control", &LSSDPInfo{FW: "AR241CP_87\x1b47.29"}, "AR241CP_8747"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := NewState()
			st.SetLSSDP(c.lssdp)
			st.RequestOTA()
			build, ok := st.TakeOTARequest()
			if c.build == "" {
				if ok || build != "" {
					t.Errorf("handed over (%q, %v), want held back", build, ok)
				}
				if !st.DiagnosticView().OTAPending {
					t.Error("the held request is no longer pending")
				}
				// it stays pending, and goes out once a good answer lands
				st.SetLSSDP(&LSSDPInfo{FW: "AR241CP_8747.29.2"})
				if build, ok := st.TakeOTARequest(); !ok || build != "AR241CP_8747" {
					t.Errorf("after a good answer: (%q, %v)", build, ok)
				}
				return
			}
			if !ok || build != c.build {
				t.Errorf("TakeOTARequest = (%q, %v), want (%q, true)", build, ok, c.build)
			}
		})
	}
}

// A request stays pending until the vendor's answer lands, not only until the
// worker takes it; SetOTA ends the check in flight, and a u pressed during
// the check survives that answer.
func TestOTAHandshake(t *testing.T) {
	st := NewState()
	st.SetLSSDP(&LSSDPInfo{FW: "AR241CP_8747.29.2"})
	st.RequestOTA()
	if !st.DiagnosticView().OTAPending {
		t.Error("a raised request is not pending")
	}
	if build, ok := st.TakeOTARequest(); !ok || build != "AR241CP_8747" {
		t.Fatalf("TakeOTARequest = (%q, %v)", build, ok)
	}
	if _, ok := st.TakeOTARequest(); ok {
		t.Error("the request was handed over twice")
	}
	if !st.DiagnosticView().OTAPending {
		t.Error("taken but unanswered: the check is in flight and must read pending")
	}
	at := time.Now()
	st.SetOTA(OTAInfo{At: at, Asked: "AR241CP_8747", UpToDate: false, Offered: "AR241CP_8800",
		PackageURL: "https://example.invalid/b.zip"})
	d := st.DiagnosticView()
	if d.OTAPending {
		t.Error("answered: nothing is pending any more")
	}
	want := OTAInfo{At: at, Asked: "AR241CP_8747", Offered: "AR241CP_8800", PackageURL: "https://example.invalid/b.zip"}
	if d.OTA == nil || *d.OTA != want {
		t.Errorf("OTA = %+v, want %+v", d.OTA, want)
	}

	// a second u during a check survives the first answer
	st.RequestOTA()
	if _, ok := st.TakeOTARequest(); !ok {
		t.Fatal("the second request was not handed over")
	}
	st.RequestOTA()
	st.SetOTA(OTAInfo{At: time.Now(), Err: "timeout"})
	if !st.DiagnosticView().OTAPending {
		t.Error("a request raised mid-check was lost with the answer")
	}
}

func TestFirmwareBuild(t *testing.T) {
	for in, want := range map[string]string{
		"AR241CP_8747.29.2": "AR241CP_8747",
		"AR241CP_8747":      "AR241CP_8747",
		"":                  "",
		".29":               "",
		"a.b.c":             "a",
	} {
		if got := firmwareBuild(in); got != want {
			t.Errorf("firmwareBuild(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- diagnostics view -------------------------------------------------------

func TestDiagnosticViewCarriesEveryField(t *testing.T) {
	st := NewState()
	st.StartConnection()
	st.Received()
	st.ApplyStatus("NET", true, 44, true)
	st.ApplyTrackField(FieldTitle, "Big Bang")
	st.ApplyVendor("spotify")
	st.ApplyVersion("29-1d316f0c-10")
	st.Note("hello")
	st.SetLSSDP(&LSSDPInfo{FW: "AR241CP_8747.29.2", State: "0", NetMode: "1"})
	st.SetSpotifyZC(&SpotifyZC{StatusString: "OK"}, 4070)
	st.SetOTA(OTAInfo{Asked: "AR241CP_8747", UpToDate: true})
	st.RequestOTA()

	d := st.DiagnosticView()
	if s := st.Snap(); d.Snapshot != s {
		t.Errorf("DiagnosticView().Snapshot = %+v, want Snap() %+v", d.Snapshot, s)
	}
	if !d.LastRx.Equal(st.LastRx()) || d.LastRx.IsZero() {
		t.Errorf("LastRx = %v", d.LastRx)
	}
	if d.MCU != "29-1d316f0c-10" {
		t.Errorf("MCU = %q", d.MCU)
	}
	if d.LSSDP == nil || d.LSSDP.FW != "AR241CP_8747.29.2" || d.LSSDPProbeAt.IsZero() || d.LSSDPOKAt.IsZero() {
		t.Errorf("LSSDP = %+v at %v/%v", d.LSSDP, d.LSSDPProbeAt, d.LSSDPOKAt)
	}
	if d.SpotifyZC == nil || d.SpotifyZC.StatusString != "OK" || d.ZCPort != 4070 || d.ZCProbeAt.IsZero() || d.ZCOKAt.IsZero() {
		t.Errorf("SpotifyZC = %+v port %d at %v/%v", d.SpotifyZC, d.ZCPort, d.ZCProbeAt, d.ZCOKAt)
	}
	if d.OTA == nil || !d.OTA.UpToDate || !d.OTAPending {
		t.Errorf("OTA = %+v pending=%v, want the verdict and the new request pending", d.OTA, d.OTAPending)
	}
	if s := d.Snapshot; !s.Connected || s.Attempts != 1 || s.Track == nil || s.Service != "spotify" ||
		s.Error != "hello" || !s.LSSDPAlive {
		t.Errorf("Snapshot = %+v", s)
	}
}

// ---- Track ------------------------------------------------------------------

func TestTrackEmpty(t *testing.T) {
	var nilTrack *Track
	cases := []struct {
		t    *Track
		want bool
	}{
		{nilTrack, true},
		{&Track{}, true},
		{&Track{TrackName: "x"}, false},
		{&Track{Artist: "x"}, false},
		{&Track{Album: "x"}, false},
		{&Track{Service: "spotify"}, false},
	}
	for _, c := range cases {
		if got := c.t.Empty(); got != c.want {
			t.Errorf("(%+v).Empty() = %v, want %v", c.t, got, c.want)
		}
	}
}

// ---- concurrency ------------------------------------------------------------

// Every setter races every reader. Run under -race: a setter that writes
// outside the lock, or mutates a value a reader already holds (a published
// Track, LSSDPInfo, SpotifyZC, OTAInfo), fails here. The readers also check
// the invariants a frame relies on.
func TestStateIsSafeForConcurrentUse(t *testing.T) {
	const rounds = 300
	st := NewState()
	var wg sync.WaitGroup
	run := func(f func(i int)) {
		wg.Go(func() {
			for i := range rounds {
				f(i)
			}
		})
	}
	// the tunnel worker
	run(func(i int) {
		st.StartConnection()
		st.Received()
		st.ApplyStatus([]string{"NET", "BT"}[i%2], i%3 == 0, i%120, i%2 == 0)
		st.ApplyVolume(i - 50)
		st.ApplyMute(i%2 == 1)
		st.ApplyPlaying(i%4 == 1)
		st.ApplySource("NET")
		st.ApplyTrackField(FieldTitle, fmt.Sprint("t", i))
		st.ApplyTrackField(FieldArtist, fmt.Sprint("a", i))
		st.ApplyTrackField(FieldAlbum, fmt.Sprint("b", i))
		st.ApplyVendor([]string{"spotify", "tidal"}[i%2])
		st.ApplyVersion(fmt.Sprint(i))
		st.ApplyTunnel("BAS", i%21-10)
		st.SetEQPresets([]string{"Flat", fmt.Sprint(i)})
		st.Note(fmt.Sprint("note ", i))
		if i%7 == 0 {
			st.Disconnect()
		}
		if v, ok := st.TakeVolumeBridge(); ok && (v < 0 || v > 100) {
			t.Errorf("bridge level %d outside 0..100", v)
		}
	})
	// the keyboard
	run(func(i int) {
		st.ToggleOptimistic()
		st.PauseOptimistic()
		st.SetVol(i)
		st.AdjustVol([]int{math.MaxInt, -3, 5, math.MinInt}[i%4])
		st.ToggleMute()
		st.SetEQLocal("TRE", i%21-10)
		st.SetProbeQuiet(i%2 == 0)
		st.RequestOTA()
		st.Preload(i)
		st.PreloadEQ(map[string]int{"MID": i % 21})
	})
	// the probes and the OTA worker
	run(func(i int) {
		if i%3 == 0 {
			st.SetLSSDP(nil)
		} else {
			st.SetLSSDP(&LSSDPInfo{FW: "AR241CP_8747.29.2", State: fmt.Sprint(i)})
		}
		st.SetSpotifyZC(&SpotifyZC{ActiveUser: fmt.Sprint(i)}, i)
		if _, ok := st.TakeOTARequest(); ok {
			st.SetOTA(OTAInfo{At: time.Now(), Asked: "AR241CP_8747", Err: fmt.Sprint(i)})
		}
		_ = st.ProbeWanted()
	})
	// the TUI: two readers, each checking what it holds stays as it was
	for range 2 {
		run(func(int) {
			s := st.Snap()
			if s.Vol < 0 || s.Vol > 100 {
				t.Errorf("Vol %d outside 0..100", s.Vol)
			}
			var held Track
			if s.Track != nil {
				held = *s.Track
			}
			d := st.DiagnosticView()
			var lssdp LSSDPInfo
			if d.LSSDP != nil {
				lssdp = *d.LSSDP
			}
			var zc SpotifyZC
			if d.SpotifyZC != nil {
				zc = *d.SpotifyZC
			}
			var ota OTAInfo
			if d.OTA != nil {
				ota = *d.OTA
			}
			_, vals := st.EQView()
			for k := range vals {
				vals[k]++
			}
			names := st.EQPresets()
			for i := range names {
				names[i] = strings.ToUpper(names[i])
			}
			_, _ = st.EQValue("BAS")
			_ = st.LastRx()
			// the published values a renderer holds never change under it
			if s.Track != nil && *s.Track != held {
				t.Errorf("a published Track changed: %+v -> %+v", held, *s.Track)
			}
			if d.LSSDP != nil && *d.LSSDP != lssdp {
				t.Error("a published LSSDPInfo changed")
			}
			if d.SpotifyZC != nil && *d.SpotifyZC != zc {
				t.Error("a published SpotifyZC changed")
			}
			if d.OTA != nil && *d.OTA != ota {
				t.Error("a published OTAInfo changed")
			}
		})
	}
	wg.Wait()
}

// TestSourceChangeDropsTheService: after the input moves (NET → BT) the latest
// VND word no longer names what plays, so the player must not keep saying
// "Spotify".
func TestSourceChangeDropsTheService(t *testing.T) {
	st := NewState()
	st.ApplySource("NET")
	st.ApplyVendor("spotify")
	st.ApplyTrackField(FieldTitle, "Song")
	st.ApplyStatus("BT", false, 40, true)
	if s := st.Snap(); s.Service != "" || s.Track != nil || s.Source != "BT" {
		t.Errorf("after NET → BT: service %q, track %+v, source %q", s.Service, s.Track, s.Source)
	}
}

// A level another app sets inside lp10's own volume hold updates the
// register only (firmware 8747); the first reading after the hold must still
// bridge it, though the held reading already moved devVol to that level.
func TestForeignLevelInsideTheHoldIsBridged(t *testing.T) {
	st := NewState()
	st.ApplyVolume(30)
	takeBridge(t, st, 30, true)
	st.SetVol(40)      // lp10's key: VOL:40 goes out and reaches the room
	st.ApplyVolume(40) // its echo, held
	st.ApplyVolume(25) // the Spotify slider 1s later: register only, held
	takeBridge(t, st, 0, false)
	expireVolHold(st)
	st.ApplyVolume(25) // the next poll after the hold
	if got := st.Snap().Vol; got != 25 {
		t.Fatalf("Vol = %d, want 25 on screen", got)
	}
	takeBridge(t, st, 25, true) // the room is at 40 until 25 goes through the MCU
	st.ApplyVolume(25)
	takeBridge(t, st, 0, false) // once
}
