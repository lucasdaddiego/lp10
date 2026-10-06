// The shared State: the lock-protected model the worker goroutines mutate and
// the TUI reads, its immutable Snapshot projection, and the accessor methods
// grouped by concern (player, volume/mute, EQ, connection, probes, diag view).

package protocol

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// State is the shared, lock-protected domain model the workers mutate and the
// TUI reads. The connection, its goroutines and the persistence paths belong
// to workers.Runtime instead.
type State struct {
	mu sync.Mutex

	// connected is the :2018 tunnel: up from its first parsed frame until the
	// socket closes or goes silent. It is the one link to the box, so the
	// player and the equalizer share it.
	connected bool
	lastRx    time.Time // the latest frame (zero: none this connection)
	attempts  int       // connections tried this run

	// the player, as the tunnel reports it
	track     *Track // the track the device announced this run (nil: none yet, or the source moved)
	service   string // the latest VND word ("spotify"), carried into the next track
	source    string // the input: NET, BT, LINE-IN, USBPLAY ("" until read)
	playing   bool
	playKnown bool // the device has reported a play state on this connection
	playHold  time.Time
	vol       int
	volHold   time.Time
	// volLive: a volume read has arrived this run, so vol is the device's
	// level and not the snapshot the previous run cached.
	volLive bool
	// the volume bridge (see TakeVolumeBridge): the level the device last
	// reported on this connection, and a level to re-send through the tunnel
	devVol        int
	devVolKnown   bool
	bridgeVol     int
	bridgePending bool
	// setVol is the level lp10 last set; setPending holds it until the first
	// reading after its hold, which bridges when the device holds another
	setVol     int
	setPending bool
	muted      bool
	muteHold   time.Time
	mcu        string // the MCU firmware as VER reports it, "29-1d316f0c-10"

	errMsg string
	errAt  time.Time

	// EQ / tone control state, keyed by wire code (MXV/EQE/EQS/BAS/MID/TRE/
	// VBS/VBI/BAL).
	eqVals    map[string]int       // wire code -> last-known value
	eqHold    map[string]time.Time // wire code -> echo-suppression deadline
	eqPresets []string             // EQ preset names by EQS index (the PEQ list), nil until read

	// LSSDP liveness: the device's UDP:1800 answer as last probed (nil when the
	// last probe went unanswered), and when a probe last ran / last succeeded.
	lssdp        *LSSDPInfo
	lssdpProbeAt time.Time
	lssdpOKAt    time.Time

	// Spotify ZeroConf (the engine's own unauthenticated HTTP endpoint, found
	// over mDNS): the last answer, the port it was found on, and the probe
	// bookkeeping mirroring LSSDP's.
	zc        *SpotifyZC
	zcPort    int
	zcProbeAt time.Time
	zcOKAt    time.Time

	// firmware update check: the TUI raises otaWant when u is pressed in the
	// diagnostics, the OTA worker takes it — otaBusy from then until its
	// answer lands — and answers with ota (the vendor manifest's verdict) —
	// see RequestOTA / TakeOTARequest / SetOTA. otaWake (one slot) is what
	// the worker sleeps on: a request, or a build landing for a held one.
	otaWant bool
	otaBusy bool
	ota     *OTAInfo
	otaWake chan struct{}

	// probeQuiet is raised by the TUI while no view shows what the LSSDP and
	// ZeroConf probes find (anything but the diagnostics); the probe workers
	// then skip their connected-cadence rounds — the box is asked nothing it
	// will not be shown. Disconnected, the probes always run: the connecting
	// screen is built on them. Default off, so a State without a TUI (tests)
	// probes as before.
	probeQuiet bool
}

// NewState returns an initialized State.
func NewState() *State {
	return &State{
		eqVals:  map[string]int{},
		eqHold:  map[string]time.Time{},
		otaWake: make(chan struct{}, 1),
	}
}

// LSSDPInfo is the device's UDP:1800 self-description (see discovery.ProbeLSSDP);
// the strings are control-stripped on the way in.
type LSSDPInfo struct {
	FW, State, NetMode string
}

// SpotifyZC is the Spotify engine's ZeroConf getInfo answer (see
// discovery.ProbeSpotifyZC): whether it is up, who is signed in, and the eSDK
// build. Strings are control-stripped on the way in.
type SpotifyZC struct {
	StatusString, ActiveUser, LibraryVersion string
}

// OTAInfo is the vendor manifest's verdict on the device's firmware, as last
// asked: up to date, a newer build on offer, or why the check failed.
type OTAInfo struct {
	At       time.Time // when the answer (or failure) landed
	Asked    string    // the firmware build the check was made for, e.g. "AR241CP_8747"
	UpToDate bool
	Offered  string // the build the manifest offers instead ("" when up to date / failed)
	Err      string // "" on a clean answer; else why there is no verdict
	// PackageURL is the bundle the manifest names with an offer (https only,
	// "" otherwise). Only `lp10 sweep` reads it; the overlay shows Offered.
	PackageURL string
}

// Snapshot is an immutable view of State for rendering.
type Snapshot struct {
	Connected bool
	// Track is what the device announced this run, nil before its first
	// announcement: the tunnel names a track only when it changes, so a run
	// that starts mid-track has none until the next one.
	Track   *Track
	Service string // the latest VND word, "" until the device names one
	Source  string // the input, "" until read
	Playing bool
	// PlayKnown is false until the device has reported its play state on
	// this connection: before that Playing is the zero value, or the state
	// from before the outage.
	PlayKnown bool
	Vol       int
	Muted     bool
	// VolLive is true once the device has reported its volume this run.
	// Until then Vol is the level the previous run cached, and a volume step
	// computed from it would land on the device as a wrong absolute level.
	VolLive  bool
	Error    string
	ErrorAt  time.Time
	Attempts int

	// LSSDPAlive is true when the device's UDP:1800 responder answered the
	// most recent probe — the box is up even if the tunnel isn't; LSSDPAt is
	// when that last answer arrived (zero: never this process).
	LSSDPAlive   bool
	LSSDPAt      time.Time
	LSSDPProbeAt time.Time // when a probe last ran (zero: none yet)
}

// Snap projects the current State.
func (st *State) Snap() Snapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.snapLocked()
}

// snapLocked projects State. The caller holds st.mu.
func (st *State) snapLocked() Snapshot {
	return Snapshot{
		Connected:    st.connected,
		Track:        st.track,
		Service:      st.service,
		Source:       st.source,
		Playing:      st.playing,
		PlayKnown:    st.playKnown,
		Vol:          st.vol,
		Muted:        st.muted,
		VolLive:      st.volLive,
		Error:        st.errMsg,
		ErrorAt:      st.errAt,
		Attempts:     st.attempts,
		LSSDPAlive:   st.lssdp != nil,
		LSSDPAt:      st.lssdpOKAt,
		LSSDPProbeAt: st.lssdpProbeAt,
	}
}

// ---- the player, as the tunnel reports it ----

// held reports whether a local change's echo window is still open at now.
func held(hold, now time.Time) bool { return now.Before(hold) }

// ApplyStatus records one STA reply: the source, the mute, the volume and the
// play state, each unless a local change of it is still inside its echo
// window. A change of source drops the track and the service (see
// applySourceLocked).
func (st *State) ApplyStatus(source string, muted bool, vol int, playing bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	st.applySourceLocked(printable(source))
	if !held(st.muteHold, now) {
		st.muted = muted
	}
	st.applyVolLocked(vol, now)
	st.applyPlayLocked(playing, now)
}

// ApplyVolume records a VOL reply or push.
func (st *State) ApplyVolume(vol int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.applyVolLocked(vol, time.Now())
}

// ApplyMute records a MUT reply or push.
func (st *State) ApplyMute(muted bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !held(st.muteHold, time.Now()) {
		st.muted = muted
	}
}

// ApplyPlaying records a PLA reply or push.
func (st *State) ApplyPlaying(playing bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.applyPlayLocked(playing, time.Now())
}

// ApplySource records a SRC reply.
func (st *State) ApplySource(source string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.applySourceLocked(printable(source))
}

func (st *State) applyVolLocked(vol int, now time.Time) {
	vol = clamp100(vol)
	if !held(st.volHold, now) {
		foreign := !st.devVolKnown || vol != st.devVol
		if st.setPending {
			foreign = foreign || vol != st.setVol
			st.setPending = false
		}
		if foreign {
			st.bridgeVol, st.bridgePending = vol, true
		}
	}
	st.devVol, st.devVolKnown = vol, true
	st.volLive = true
	if !held(st.volHold, now) {
		st.vol = vol
	}
}

// TakeVolumeBridge hands the tunnel worker a level to re-send as VOL:n, once.
// Firmware AR241CP_8747 broke the Spotify app's volume: the level the app
// sets reaches the MCU's register — the MCU pushes it to the tunnel's
// clients as VOL:n, and the status poll reads it — but the SoC's softvol
// never follows (its amixer call fails), so the room stays where it was. A
// VOL set through the tunnel takes the MCU's own path, which applies it
// (verified by ear 2026-10-01: the room followed the app's slider within a
// fraction of a second while lp10 ran). So a level the device reports that lp10 did not set (outside
// the local echo hold), and the first one of each connection, is sent back
// as it is. For a knob or remote change, already applied, the re-send is a
// no-op.
func (st *State) TakeVolumeBridge() (int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.bridgePending {
		return 0, false
	}
	st.bridgePending = false
	return st.bridgeVol, true
}

func (st *State) applyPlayLocked(playing bool, now time.Time) {
	st.playKnown = true
	if !held(st.playHold, now) {
		st.playing = playing
	}
}

// applySourceLocked records the input. A change drops the track and the
// service word: what the old input announced is not what the new one plays,
// and a VND comes again only when a service starts playing.
func (st *State) applySourceLocked(source string) {
	if source == "" {
		return
	}
	if st.source != "" && source != st.source {
		st.track, st.service = nil, ""
	}
	st.source = source
}

// Track fields, by the tunnel code that pushes each.
const (
	FieldTitle  = "TIT"
	FieldArtist = "ART"
	FieldAlbum  = "ALB"
)

// ApplyTrackField records one pushed track field. The device sends TIT, ART
// and ALB in that order on a track change, so a title starts a new track —
// the artist and album of the last one must not linger beside it — and an
// artist or album fills in the current one (or starts one, should a title
// ever be missing). A published Track is never mutated: each field makes a
// new value, so a Snapshot taken before it stays as it was.
func (st *State) ApplyTrackField(field, text string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	text = printable(text)
	var t Track
	if field != FieldTitle && st.track != nil {
		t = *st.track
	}
	t.Service = st.service
	switch field {
	case FieldTitle:
		t.TrackName = text
	case FieldArtist:
		t.Artist = text
	case FieldAlbum:
		t.Album = text
	default:
		return
	}
	st.track = &t
}

// ApplyVendor records a VND push — the service playing ("spotify"). It names
// the current track's service too, and every track after it.
func (st *State) ApplyVendor(service string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.service = printable(service)
	if st.track != nil && st.track.Service != st.service {
		t := *st.track
		t.Service = st.service
		st.track = &t
	}
}

// ApplyVersion records the MCU firmware as VER reports it.
func (st *State) ApplyVersion(v string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.mcu = printable(v)
}

// ---- optimistic UI ----

// ToggleOptimistic flips the local play state and arms the echo hold; it
// returns whether the player WAS playing. The device's POP is a toggle too,
// so the wire command is the same either way: this only keeps the screen a
// step ahead of the device's PLA push.
func (st *State) ToggleOptimistic() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	was := st.playing
	st.playing = !was
	st.playHold = time.Now().Add(PlayHoldDuration)
	return was
}

// PauseOptimistic is the toggle's one-way form, for the sleep timer: it
// pauses only if the device last said it is playing — decided under the
// lock, so a pause landing between a snapshot and the flip can never turn the
// timer's toggle into a resume — and reports whether a POP must be sent.
// known is false, and nothing changes, while this connection has not
// reported a play state yet: the one held is from before the outage, and
// the caller waits for the device's.
func (st *State) PauseOptimistic() (pause, known bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.playKnown {
		return false, false
	}
	if !st.playing {
		return false, true
	}
	st.playing = false
	st.playHold = time.Now().Add(PlayHoldDuration)
	return true, true
}

// ---- volume / mute ----

func clamp100(v int) int { return max(0, min(100, v)) }

// applyVol computes and applies the target under the lock and arms the echo
// hold. A local set replaces any level still waiting to be bridged: sent
// after the key's own write, that older level would undo the step.
func (st *State) applyVol(target func(cur int) int) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.vol = clamp100(target(st.vol))
	st.volHold = time.Now().Add(VolHoldDuration)
	st.bridgePending = false
	st.setVol, st.setPending = st.vol, true
	return st.vol
}

// SetVol sets an absolute volume and returns the applied value.
func (st *State) SetVol(v int) int {
	return st.applyVol(func(int) int { return v })
}

// AdjustVol changes the volume by delta and returns the applied value.
func (st *State) AdjustVol(delta int) int {
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

// ToggleMute flips the local mute, arms its echo hold, and returns the new
// state — the one the device is to be told.
func (st *State) ToggleMute() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.muted = !st.muted
	st.muteHold = time.Now().Add(MuteHoldDuration)
	return st.muted
}

// ---- EQ / tone control state ----

// ApplyTunnel records a device-reported control value, unless that control was
// changed locally within its echo-suppression window.
func (st *State) ApplyTunnel(code string, val int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if h, ok := st.eqHold[code]; ok && time.Now().Before(h) {
		return
	}
	st.eqVals[code] = val
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
// equalizer, before the tunnel has connected. It does NOT arm the echo hold,
// so the device's seed values overwrite these the moment the tunnel comes up.
func (st *State) PreloadEQ(vals map[string]int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	maps.Copy(st.eqVals, vals)
}

// SetEQPresets records the device's EQ preset names by index (its PEQ list).
func (st *State) SetEQPresets(names []string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.eqPresets = slices.Clone(names)
}

// EQPresets returns the preset names by EQS index (nil before the PEQ reply).
func (st *State) EQPresets() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.Clone(st.eqPresets)
}

// EQValue returns one control's last-known value and whether it is known yet.
func (st *State) EQValue(code string) (int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.eqVals[code]
	return v, ok
}

// EQView snapshots the link state and a copy of all known control values for
// rendering, in one locked read.
func (st *State) EQView() (connected bool, vals map[string]int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.connected, maps.Clone(st.eqVals)
}

// ---- errors ----

// Note records a transient error message. The text is control-stripped: a
// note can carry a dial error or a device word, which the error line renders
// at full width — an un-stripped ESC there could inject an escape sequence.
func (st *State) Note(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.errMsg, st.errAt = printable(msg), time.Now()
}

// ---- connection liveness ----

// StartConnection counts a fresh connection attempt. The volume bridge starts
// over: the first level the new connection reads is re-sent (see
// TakeVolumeBridge).
func (st *State) StartConnection() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.attempts++
	st.lastRx = time.Time{}
	st.devVolKnown, st.bridgePending = false, false
	st.playKnown = false // the room may have paused or resumed during the outage
}

// Received marks a parsed frame: the link is live, and when it last spoke.
func (st *State) Received() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.connected = true
	st.lastRx = time.Now()
}

// Disconnect marks the link dead (idempotent).
func (st *State) Disconnect() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.connected = false
}

// LastRx is when the current connection last delivered a frame (zero: none).
func (st *State) LastRx() time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastRx
}

// ---- preload ----

// Preload seeds the cached volume for an instant first paint. The play state
// and the track are not cached: the device reports the first at once, and
// the second only when the track changes — a cached title could name
// something the box stopped playing long ago.
func (st *State) Preload(vol int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.vol = clamp100(vol)
}

// ---- LSSDP liveness ----

// SetLSSDP records a probe result: the device's answer (control-stripped), or
// nil for an unanswered probe.
func (st *State) SetLSSDP(info *LSSDPInfo) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	st.lssdpProbeAt = now
	if info == nil {
		st.lssdp = nil
		return
	}
	st.lssdp = &LSSDPInfo{
		FW: printable(info.FW), State: printable(info.State), NetMode: printable(info.NetMode),
	}
	st.lssdpOKAt = now
	if st.otaWant {
		st.wakeOTA() // a held request: the build it waited for may have landed
	}
}

// SetSpotifyZC records a ZeroConf probe: the engine's answer (control-stripped)
// and the port it answered on, or nil for an unanswered probe (port 0 when the
// endpoint could not even be found over mDNS).
func (st *State) SetSpotifyZC(info *SpotifyZC, port int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	st.zcProbeAt = now
	st.zcPort = port
	if info == nil {
		st.zc = nil
		return
	}
	st.zc = &SpotifyZC{StatusString: printable(info.StatusString), ActiveUser: printable(info.ActiveUser),
		LibraryVersion: printable(info.LibraryVersion)}
	st.zcOKAt = now
}

// SetProbeQuiet tells the probe workers whether their answers are on screen
// (quiet = nobody is looking). See probeQuiet.
func (st *State) SetProbeQuiet(quiet bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.probeQuiet = quiet
}

// ProbeWanted reports whether a connected-cadence probe should run now: it
// should unless the TUI has said nobody is looking.
func (st *State) ProbeWanted() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return !st.probeQuiet
}

// ---- firmware update check ----

// RequestOTA asks for a firmware update check. Raised by the TUI's u key in
// the diagnostics — the check contacts the vendor, so it only ever runs on
// that explicit keystroke, never on a timer or on opening a view.
func (st *State) RequestOTA() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.otaWant = true
	st.wakeOTA()
}

// wakeOTA nudges the OTA worker without ever blocking: one nudge is enough
// for any number of requests, so a full slot is left as it is. Called with
// the lock held.
func (st *State) wakeOTA() {
	select {
	case st.otaWake <- struct{}{}:
	default:
	}
}

// OTAWake is the channel the OTA worker waits on: it receives when a request
// is raised, and when an LSSDP answer lands while one is held for its build.
// A wake is a hint — the worker then asks TakeOTARequest, which may still
// hold the request back.
func (st *State) OTAWake() <-chan struct{} { return st.otaWake }

// reBuild is the shape of a firmware build the vendor manifest is asked about
// ("AR241CP_8747"): the string is LAN input and lands in a request body.
var reBuild = regexp.MustCompile(`^[A-Z0-9]{2,12}_[0-9]{1,8}$`)

// ValidBuild reports whether build has the shape the manifest is asked about
// (reBuild). The OTA worker insists on it before the string goes out.
func ValidBuild(build string) bool { return reBuild.MatchString(build) }

// TakeOTARequest hands a pending request to the worker (clearing it, and
// marking the check in flight until SetOTA), with the firmware build to ask
// about: the LSSDP answer's, the one place the box names its build without
// ssh. Before an answer with a build has arrived the request is NOT handed
// over — the worker polls again, and the diagnostics keep saying "checking…"
// until one lands.
func (st *State) TakeOTARequest() (build string, pending bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.otaWant || st.lssdp == nil || !ValidBuild(FirmwareBuild(st.lssdp.FW)) {
		return "", false
	}
	st.otaWant, st.otaBusy = false, true
	return FirmwareBuild(st.lssdp.FW), true
}

// SetOTA records the worker's verdict (strings control-stripped), which ends
// the check in flight.
func (st *State) SetOTA(info OTAInfo) {
	st.mu.Lock()
	defer st.mu.Unlock()
	info.Asked, info.Offered, info.Err = printable(info.Asked), printable(info.Offered), printable(info.Err)
	info.PackageURL = printable(info.PackageURL)
	st.ota, st.otaBusy = &info, false
}

// FirmwareBuild is the manifest's fwVersion: the build before the first dot
// of a firmware string ("AR241CP_8747.29.2" → "AR241CP_8747").
func FirmwareBuild(fw string) string { return Before(fw, ".") }

// Before is s up to its first sep ("29-1d316f0c-10", "-" → "29"), or s whole:
// the first field of the dotted and dashed version strings the box reports.
func Before(s, sep string) string {
	before, _, _ := strings.Cut(s, sep)
	return before
}

// ---- diagnostics view ----

// DiagnosticSnapshot is the complete, point-in-time state consumed by the
// diagnostics view, read under one lock so a frame never combines values
// observed on opposite sides of a worker update.
type DiagnosticSnapshot struct {
	Snapshot Snapshot

	LastRx time.Time // the tunnel's latest frame (zero: none this connection)
	MCU    string    // the MCU firmware as VER reports it ("" until read)

	// LSSDP is the device's last UDP:1800 answer (nil: unanswered or never
	// probed); LSSDPProbeAt / LSSDPOKAt time the last probe and last answer.
	LSSDP                   *LSSDPInfo
	LSSDPProbeAt, LSSDPOKAt time.Time

	// SpotifyZC is the engine's last ZeroConf answer (nil: unanswered or never
	// probed); ZCPort the port it was found on (0: not found over mDNS).
	SpotifyZC         *SpotifyZC
	ZCPort            int
	ZCProbeAt, ZCOKAt time.Time

	// OTA is the last firmware-check verdict (nil: never asked this run);
	// OTAPending is a check u asked for that has not answered yet — still
	// waiting for the worker, or in flight to the vendor.
	OTA        *OTAInfo
	OTAPending bool
}

// DiagnosticView returns every value used by one diagnostics frame under one
// lock. The pointer values are safe to publish: the setters replace them
// wholesale and never mutate a published value.
func (st *State) DiagnosticView() DiagnosticSnapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	return DiagnosticSnapshot{
		Snapshot:     st.snapLocked(),
		LastRx:       st.lastRx,
		MCU:          st.mcu,
		LSSDP:        st.lssdp,
		LSSDPProbeAt: st.lssdpProbeAt,
		LSSDPOKAt:    st.lssdpOKAt,
		SpotifyZC:    st.zc,
		ZCPort:       st.zcPort,
		ZCProbeAt:    st.zcProbeAt,
		ZCOKAt:       st.zcOKAt,
		OTA:          st.ota,
		OTAPending:   st.otaWant || st.otaBusy,
	}
}
