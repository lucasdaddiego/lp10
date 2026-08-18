// The shared State: the lock-protected model the worker goroutines mutate and
// the TUI reads, its immutable Snapshot projection, and the accessor methods
// grouped by concern (volume/mute, EQ tunnel, connection liveness, diag views).

package protocol

import (
	"image"
	"image/color"
	"maps"
	"math"
	"sync"
	"time"
)

// State is the shared, lock-protected domain model the workers mutate and the
// TUI reads. Child processes, shutdown coordination, and persistence paths are
// deliberately owned by workers.Runtime instead.
type State struct {
	mu sync.Mutex

	connected bool
	track     *Track
	trackAt   time.Time
	sysinfo   *SysInfo
	devinfo   *DevInfo    // static device/network info (@@i, once per connection)
	confinfo  *ConfInfo   // streaming-capability state (@@c, once per connection)
	details   *DevDetails // device-details JSON readout (@@d, once per connection)
	mroom     *Multiroom  // multiroom-group readout (@@g, once per connection)

	posMs    int
	posAt    time.Time
	playing  int // MID 51: 0=playing, anything else not
	vol      int
	volHold  time.Time
	playHold time.Time
	premute  int // 0 == none (Python None)

	errMsg string
	errAt  time.Time
	fatal  bool

	gotRecord bool
	lastRx    time.Time // zero == never (this connection)
	lastData  time.Time // zero == never (this connection)
	attempts  int
	retryBase int // attempts at last successful connect

	// datalessDeaths counts consecutive connections that died without any
	// player data (reset by the next data record); deathCounted keeps a repeat
	// Disconnect from double-counting one connection. A running streak withholds
	// WriterLive's young-spawn grace — see WriterLive.
	datalessDeaths int
	deathCounted   bool

	// network throughput + latency for the diagnostics overlay, over the active
	// interface. Rates derive from the cumulative byte counters against the prior
	// @@s; the ping rings hold recent RTTs (ms) for laptop/gateway/internet.
	netPrevRx, netPrevTx int64
	netPrevAt            time.Time
	netRxRate, netTxRate float64
	netRatesOK           bool
	pingRing             [3][]float64

	// cumulative interface error/drop counters: the connection's first sample
	// baselines the session, so boot-lifetime noise (e.g. a powerline link's
	// historical drops) never reads as a live fault.
	errBase, errCur [4]int64 // rx_errors, tx_errors, rx_dropped, tx_dropped
	errsOK          bool

	// EQ / tone control state from the :2018 tunnel (separate from the ssh
	// player stream). Keyed by wire code (MXV/EQS/BAS/MID/TRE/VBS/VBI).
	eqConnected bool
	eqVals      map[string]int       // wire code -> last-known value
	eqHold      map[string]time.Time // wire code -> echo-suppression deadline

	// album art: the decoded cover and the CoverArtUrl it was loaded for, set
	// by the art worker. Snap exposes the image only while artURL still matches
	// the playing track, so a stale cover never lingers across a track change.
	artURL   string
	artImg   image.Image
	artDom   color.RGBA // cover's representative hue (computed by the art worker)
	artDomOK bool       // false for a greyscale cover (keep the theme default)
}

// NewState returns an initialized State, mirroring the Python constructor
// defaults (playing starts at 2 = "not playing", posAt = now).
func NewState() *State {
	return &State{
		playing: 2,
		posAt:   time.Now(),
		eqVals:  map[string]int{},
		eqHold:  map[string]time.Time{},
	}
}

// Snapshot is an immutable view of State for rendering.
type Snapshot struct {
	Connected bool
	Track     *Track
	Pos       int
	Playing   int
	Vol       int
	Muted     bool
	Error     string
	ErrorAt   time.Time
	Fatal     bool
	Attempts  int

	CoverURL string      // current track's cover art URL ("" if none)
	Art      image.Image // decoded cover for CoverURL, or nil if not yet loaded
	// Dominant is the cover's representative hue, precomputed by the art worker so
	// the renderer never scans pixels; DominantOK is false for a greyscale cover or
	// before the cover loads. Valid only while Art is non-nil.
	Dominant   color.RGBA
	DominantOK bool

	// LastArt is the most-recently-decoded cover and the URL it came from,
	// retained across idle so the idle screen can show a dimmed "ghost" of the
	// last thing played. Unlike Art, it is not gated on the current track.
	LastArt      image.Image
	LastCoverURL string
}

// SetArt stores the decoded cover image for url (a track's CoverArtUrl) plus its
// precomputed dominant hue (dom/domOK), computed by the art worker off the render
// path. The art worker calls this; Snap only surfaces them while url is still the
// playing track's cover.
func (st *State) SetArt(url string, img image.Image, dom color.RGBA, domOK bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.artURL = url
	st.artImg = img
	st.artDom, st.artDomOK = dom, domOK
}

// Snap projects the current State, advancing the position clock while playing.
func (st *State) Snap() Snapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.snapLocked(time.Now())
}

// snapLocked projects State at now. The caller holds st.mu.
func (st *State) snapLocked(now time.Time) Snapshot {
	pos := st.posMs
	t := st.track
	if st.playing == 0 && t != nil && st.connected {
		if elapsed := now.Sub(st.posAt).Milliseconds(); elapsed > 0 {
			if elapsed > int64(math.MaxInt-pos) {
				pos = math.MaxInt
			} else {
				pos += int(elapsed)
			}
		}
	}
	if t != nil && t.TotalTime > 0 && pos > t.TotalTime {
		pos = t.TotalTime
	}
	cover := ""
	if t != nil {
		cover = t.CoverArtURL
	}
	var art image.Image
	var dom color.RGBA
	var domOK bool
	if cover != "" && cover == st.artURL {
		art = st.artImg
		dom, domOK = st.artDom, st.artDomOK
	}
	return Snapshot{
		Connected:    st.connected,
		Track:        t,
		Pos:          pos,
		Playing:      st.playing,
		Vol:          st.vol,
		Muted:        st.connected && st.vol == 0,
		Error:        st.errMsg,
		ErrorAt:      st.errAt,
		Fatal:        st.fatal,
		Attempts:     st.attempts - st.retryBase,
		CoverURL:     cover,
		Art:          art,
		Dominant:     dom,
		DominantOK:   domOK,
		LastArt:      st.artImg,
		LastCoverURL: st.artURL,
	}
}

// DiagnosticSnapshot is the complete, point-in-time state consumed by the
// diagnostics overlay. One State lock supplies the player, liveness, device,
// capability, network, and tunnel fields, preventing a frame from combining
// values observed on opposite sides of a worker update.
type DiagnosticSnapshot struct {
	Snapshot Snapshot

	LastRx, LastData time.Time
	ConnectAttempts  int
	SysInfo          *SysInfo
	DevInfo          *DevInfo
	ConfInfo         *ConfInfo
	Details          *DevDetails
	Multiroom        *Multiroom
	Net              NetStat
	EQConnected      bool
}

// ---- volume / mute ----

func clamp100(v int) int { return max(0, min(100, v)) }

// setVolLocked sets the volume and arms the echo-suppression hold, capturing a
// pre-mute level to persist (returned) when transitioning into mute.
func (st *State) setVolLocked(v int) (int, int) {
	v = clamp100(v)
	persist := 0
	if st.vol > 0 && v == 0 {
		st.premute = st.vol
		persist = st.vol
	}
	if v > 0 {
		st.premute = 0
	}
	st.vol = v
	st.volHold = time.Now().Add(VolHoldDuration)
	return v, persist
}

// applyVol computes and applies the target under the lock. persist is the
// pre-mute level a controller should save when this change enters mute; State
// reports the value but performs no persistence itself.
func (st *State) applyVol(target func(cur int) int) (value, persist int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.setVolLocked(target(st.vol))
}

// SetVol sets an absolute volume and returns the applied value plus any
// pre-mute level the controller should persist.
func (st *State) SetVol(v int) (value, persist int) {
	return st.applyVol(func(int) int { return v })
}

// AdjustVol changes the volume by delta and returns the applied value plus any
// pre-mute level the controller should persist.
func (st *State) AdjustVol(delta int) (value, persist int) {
	return st.applyVol(func(cur int) int {
		// Preserve the 0..100 invariant without performing an addition that can
		// overflow when a caller supplies an extreme delta.
		switch {
		case delta > 0 && delta >= 100-cur:
			return 100
		case delta < 0 && delta <= -cur:
			return 0
		default:
			return cur + delta
		}
	})
}

// VolAndPremute reads the current volume and pre-mute level atomically (used by
// the mute toggle).
func (st *State) VolAndPremute() (int, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.vol, st.premute
}

// ---- EQ / tone control state (the :2018 tunnel) ----

// ApplyTunnel records a device-reported control value, unless that control was
// changed locally within its echo-suppression window. Marks the tunnel live.
func (st *State) ApplyTunnel(code string, val int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.eqConnected = true
	if h, ok := st.eqHold[code]; ok && time.Now().Before(h) {
		return
	}
	st.eqVals[code] = val
}

// SetEQConnected sets the tunnel link state (false on disconnect/reconnect).
func (st *State) SetEQConnected(b bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.eqConnected = b
}

// SetEQLocal optimistically records a user change and arms the echo hold so the
// device's broadcast echo doesn't fight a rapid adjustment.
func (st *State) SetEQLocal(code string, val int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.eqVals[code] = val
	st.eqHold[code] = time.Now().Add(EQHoldDuration)
}

// PreloadEQ seeds cached EQ/tone values for an instant first paint of the
// equalizer, before the :2018 tunnel has connected. It does NOT arm the echo
// hold or mark the tunnel connected, so the device's authoritative seed values
// overwrite these the moment the tunnel comes up (mirroring Preload for the
// player snapshot).
func (st *State) PreloadEQ(vals map[string]int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	maps.Copy(st.eqVals, vals)
}

// EQValue returns one control's last-known value and whether it is known yet.
func (st *State) EQValue(code string) (int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.eqVals[code]
	return v, ok
}

// EQView snapshots the tunnel link state and a copy of all known control values
// for rendering, in one locked read.
func (st *State) EQView() (connected bool, vals map[string]int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.eqConnected, maps.Clone(st.eqVals)
}

// ---- errors ----

// Note records a transient error message (no-op once fatal).
func (st *State) Note(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.fatal {
		st.errMsg, st.errAt = msg, time.Now()
	}
}

// ClearFatalOnData clears a fatal error once data flows again (self-healing).
func (st *State) ClearFatalOnData() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.fatal {
		st.fatal = false
		st.errMsg = ""
	}
}

// SetFatal latches a fatal error with its timestamp.
func (st *State) SetFatal(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.errMsg, st.errAt, st.fatal = msg, time.Now(), true
}

// ---- connection liveness (used by the workers and the TUI) ----

// StartConnection resets per-connection liveness and counts a fresh attempt.
// Process ownership remains with the worker runtime.
func (st *State) StartConnection() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.gotRecord = false
	st.lastRx = time.Time{}
	st.lastData = time.Time{}
	st.netPrevAt = time.Time{} // re-baseline throughput; latency rings start fresh
	st.netRatesOK = false
	st.pingRing = [3][]float64{}
	st.errsOK = false // error counters re-baseline on the next sample
	st.deathCounted = false
	st.attempts++
}

// Disconnect marks the player connection dead (idempotent). A connection that
// dies without ever delivering player data extends the dataless-death streak
// that withholds WriterLive's young-spawn grace.
func (st *State) Disconnect() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.connected = false
	if !st.deathCounted {
		st.deathCounted = true
		if st.lastData.IsZero() {
			st.datalessDeaths++
		}
	}
}

// LivenessView snapshots the fields the watchdog needs in one locked read.
func (st *State) LivenessView() (lastRx, lastData time.Time, got bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastRx, st.lastData, st.gotRecord
}

// WriterLive reports whether a process spawned at spawned is live enough to
// accept a command: a young connection may still be handshaking (ssh buffers
// stdin), while a session that went data-silent is treated as wedged. The
// young-spawn grace is withheld while a dataless-death streak is running:
// during an outage every respawn is young, and the grace would keep swallowing
// commands into a doomed stdin pipe with no "command not delivered" note.
func (st *State) WriterLive(now, spawned time.Time, liveTimeout time.Duration) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.lastData.IsZero() && now.Sub(st.lastData) <= liveTimeout {
		return true
	}
	return st.datalessDeaths == 0 && now.Sub(spawned) <= liveTimeout
}

// ---- diagnostics views ----

// DiagnosticView returns every value used by one diagnostics frame under one
// lock. The one-shot pointer values are safe to publish because ApplyRecord
// replaces them wholesale and never mutates a published value.
func (st *State) DiagnosticView(now time.Time) DiagnosticSnapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	return DiagnosticSnapshot{
		Snapshot:        st.snapLocked(now),
		LastRx:          st.lastRx,
		LastData:        st.lastData,
		ConnectAttempts: st.attempts,
		SysInfo:         st.sysinfo,
		DevInfo:         st.devinfo,
		ConfInfo:        st.confinfo,
		Details:         st.details,
		Multiroom:       st.mroom,
		Net:             st.netViewLocked(),
		EQConnected:     st.eqConnected,
	}
}

// ConfView returns the streaming-capability state (or nil before the first @@c
// block arrives). The returned ConfInfo is owned by the caller's read: the worker
// only ever replaces st.confinfo wholesale (never mutates a published map), so the
// map is safe to range without copying.
func (st *State) ConfView() *ConfInfo {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.confinfo
}

// ---- preload / optimistic UI ----

// Preload seeds the cached track/pos/vol for an instant first paint. The clock
// never resumes from a cached position, so playing starts at 2 (not playing)
// and trackAt is the zero time (any garbage B immediately clears a stale track).
func (st *State) Preload(track *Track, pos, vol int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.track = track
	st.trackAt = time.Time{}
	st.posMs = max(0, pos)
	st.playing = 2
	st.vol = clamp100(vol)
}

// ToggleOptimistic flips the local play state, arms the echo-suppression hold,
// and restarts the position clock; it returns whether the player WAS playing
// (so the caller sends PAUSE vs RESUME).
func (st *State) ToggleOptimistic() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	playing := st.playing == 0
	if playing {
		st.playing = 2
	} else {
		st.playing = 0
	}
	now := time.Now()
	st.playHold = now.Add(PlayHoldDuration)
	st.posAt = now
	return playing
}

// RawPos returns the un-extrapolated position (the last position the device
// reported), used by tests to distinguish a parsed update from clock drift.
func (st *State) RawPos() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.posMs
}

// RawAttempts returns the total connection-attempt counter.
func (st *State) RawAttempts() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.attempts
}
