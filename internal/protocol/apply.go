// Applying framed records to State: the lock-free decode of each @@-section
// (parseRecord and the section parsers) and the single locked mutation
// (ApplyRecord).

package protocol

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reNum = regexp.MustCompile(`Data:(-?\d+)`)

// joinLines is strings.Join with the single-line case — which is what a per-tick
// register read (@@p / @@t / @@v) always is — returning the line as-is instead of
// allocating a copy of it.
func joinLines(lines []string) string {
	if len(lines) == 1 {
		return lines[0]
	}
	return strings.Join(lines, "\n")
}

// SysInfo holds the device stats from the @@s section (all kept as raw strings;
// the TUI parses them lazily for health coloring). The trailing fields are
// optional extras appended by newer device loops; "" when the loop didn't send
// them (so older loops, or the test fixtures, stay compatible).
type SysInfo struct {
	Up, Load, Avail, Total, NCPU, FW string
	OS                               string // "" when absent
	TempmC                           string // SoC temperature, milli-°C
	RxBytes, TxBytes                 string // active-iface byte counters (cumulative)
	SignalDBm, LinkQ                 string // Wi-Fi only ("" on ethernet)
	PingClient, PingGw, PingNet      string // avg RTT ms: laptop / gateway / internet ("" unmeasured)
	// Newer diag-gated extras (audio chain + contention); "" when the device loop
	// or this hardware can't provide them (e.g. /proc/asound absent, fixed-clock CPU).
	PcmState string // ALSA playback state: RUNNING / SETUP / "" (no stream)
	BufAvail string // ALSA frames free in the ring buffer (status: avail)
	DacRate  string // actual DAC clock, Hz (vs the source's claimed rate)
	DacFmt   string // actual DAC sample format, e.g. S16_LE
	DacCh    string // actual DAC channel count
	BufSize  string // ALSA ring-buffer size, frames (hw_params: buffer_size)
	CpuKHz   string // current CPU frequency, kHz
	Procs    string // /proc/loadavg running/total, e.g. "2/118"
	NoiseDBm string // Wi-Fi noise floor, dBm (SNR = SignalDBm − NoiseDBm)
	// active-iface error/drop counters (cumulative since boot; the UI shows
	// session deltas so historical noise never reads as a live fault)
	RxErrs, TxErrs string
	RxDrop, TxDrop string
	// Softvol is the ALSA "Master" softvol level (0..99) the app keeps at vol−1 —
	// the real output level; sampled every third @@s ("" / "-" otherwise).
	Softvol string
}

// The @@s stats line is positional: these indices name the columns in the exact
// order the device loop emits them (the `echo "@@s ..."` in transport's
// remote_loop.src.sh — the two must change in lockstep; sysFieldCount is
// cross-checked against that emitter by TestSysStatsFieldOrder). Fields from
// sfOS on are optional extras newer loops append; "-" marks an unread value.
const (
	sfUp = iota
	sfLoad1
	sfLoad5
	sfLoad15
	sfAvail
	sfTotal
	sfNCPU
	sfFW
	sfOS
	sfTempmC
	sfRxBytes
	sfTxBytes
	sfSignalDBm
	sfLinkQ
	sfPingClient
	sfPingGw
	sfPingNet
	sfPcmState
	sfBufAvail
	sfDacRate
	sfDacFmt
	sfDacCh
	sfBufSize
	sfCpuKHz
	sfProcs
	sfNoiseDBm
	sfRxErrs
	sfTxErrs
	sfRxDrop
	sfTxDrop
	sfSoftvol
	sysFieldCount
)

// sysRequired is the mandatory positional prefix (through sfFW); a shorter @@s
// line is malformed and dropped whole.
const sysRequired = sfOS

// DevInfo holds the static device/network info from the one-shot @@i section
// (key=value lines), refreshed once per connection.
type DevInfo struct {
	Net, Iface           string // "eth"|"wifi" medium, and the active interface name
	IP, MAC, Gateway     string
	Speed, Duplex        string // ethernet link: Mbit/s, "full"|"half"
	SSID, Freq, Rate     string // Wi-Fi link: network name, MHz, tx Mbit/s
	Build, App, Platform string
	Name                 string // the device's FriendlyName (reg 90); "" when unread
	DataUsed, DataTotal  string // /lsync (data partition), KB
	DNS                  string // configured resolver (first nameserver); "" when absent
}

// confKeys is the allowlist of capability ids the one-shot @@c block may carry;
// any other key is dropped at the parse boundary (mirroring DevInfo's whitelist).
// The values are "on" / "off" / "" (unknown) — see the @@c emitter in
// transport's remote_loop.sh.
var confKeys = map[string]bool{
	"spotify": true, "airplay": true, "dlna": true, "bt": true,
	"cast": true, "tidal": true, "qobuz": true, "usb": true,
	// "<id>.env" is the CONFIGURED flag, next to the bare id's RUNNING state.
	// The two disagreeing is not an inconsistency to paper over — it is the
	// failure the device's own web page cannot see (it reports only the flag,
	// so it will happily show "Spotify: on" with no engine running at all).
	//
	// Only for services whose init script actually CONSULTS the flag. AirPlay and
	// DLNA start on every netready whatever theirs says, and Bluetooth and Cast
	// cannot be switched from here at all, so reading those four would cost a
	// getenv fork each (~40ms) to learn something no view is entitled to act on.
	"tidal.env": true, "qobuz.env": true,
	// Spotify ships two mutually exclusive engines: which one is live (.eng),
	// its Spotify eSDK build (.sdk), and the state of the env PAIR (.cfg —
	// hifi|pro|both|none, where "both" starts NEITHER).
	"spotify.eng": true, "spotify.sdk": true, "spotify.cfg": true,
	// unauthenticated listeners the LAN can reach (the loop's lp())
	"telnet": true, "adb": true, "web": true, "control": true,
}

// ConfInfo holds the device's streaming-capability state from the one-shot @@c
// section, refreshed once per connection. Svc maps a capability id (see confKeys)
// to "on" (env-enabled / daemon running), "off", or "" (unknown — the device
// couldn't read the flag). It feeds the config view; it carries no live metrics,
// so unlike @@s it is gathered unconditionally at connect, not gated on an overlay.
type ConfInfo struct {
	Svc map[string]string
}

// Env reports the service's CONFIGURED flag ("on"/"off"/"" unknown), as opposed
// to Svc[id], which is whether it is actually running. Spotify has no single
// .env key — it is gated by a pair; ask Cfg instead.
func (c *ConfInfo) Env(id string) string {
	if c == nil {
		return ""
	}
	return c.Svc[id+".env"]
}

// Engine reports which Spotify engine binary is live ("" when none is).
func (c *ConfInfo) Engine() string {
	if c == nil {
		return ""
	}
	return c.Svc["spotify.eng"]
}

// SDK reports the live Spotify engine's eSDK build ("" when unknown). The build
// is what separates an engine that can receive lossless from one that tops out
// at Ogg/AAC, so it is worth surfacing next to the codec.
func (c *ConfInfo) SDK() string {
	if c == nil {
		return ""
	}
	return c.Svc["spotify.sdk"]
}

// Cfg reports the configured Spotify engine: "hifi", "pro", "none", or "both".
// "both" is the broken pair — the vendor's two init scripts are each guarded on
// the OTHER flag being clear, so with both set neither engine ever starts.
func (c *ConfInfo) Cfg() string {
	if c == nil {
		return ""
	}
	return c.Svc["spotify.cfg"]
}

// Divergent reports a service whose configured flag and running state disagree
// — configured on but not running, or running while configured off. Services
// with no .env reading (unknown) never diverge, so an unreadable flag stays
// quiet rather than crying wolf.
func (c *ConfInfo) Divergent(id string) bool {
	env := c.Env(id)
	if env == "" {
		return false
	}
	run, ok := c.Svc[id]
	return ok && run != "" && run != env
}

// parsedRecord is the lock-free decode of one framed record, ready to be
// assigned under the State lock.
type parsedRecord struct {
	track *Track
	idle  bool
	hasB  bool // a content-free @@B header carries no track update

	pos, play, vol       int
	posOK, playOK, volOK bool
	hadData              bool

	sysinfo  *SysInfo
	devinfo  *DevInfo
	confinfo *ConfInfo
	details  *DevDetails
	mroom    *Multiroom

	night, nightOK bool // @@n: the multi-band DRC enable readback

	logs   []string // @@l: the device syslog tail, answering a MID-93 request
	hasLog bool
}

// regInt extracts the integer register value from a section's joined lines
// (the `Data:` field of a LUCI_local read).
func regInt(rec Record, tag string) (int, bool) {
	if lines := rec[tag]; len(lines) > 0 {
		if mm := reNum.FindStringSubmatch(joinLines(lines)); mm != nil {
			// drop an out-of-int64-range token rather than saturate to MaxInt
			if n, err := strconv.Atoi(mm[1]); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// parseRecord decodes every section of a framed record without touching State.
//
// Play state is taken from the per-tick reg-51 read (@@t), not the MID-42
// JSON's PlayState: both encode 0 = playing, but @@t arrives every tick while
// @@B is polled every few ticks and shipped only on change, so reg 51 is always
// at least as fresh. PlayState still crosses the parse boundary inside the
// Track but is deliberately not consumed.
func parseRecord(rec Record) parsedRecord {
	var p parsedRecord
	p.hasB = len(rec["B"]) > 0
	if p.hasB {
		p.track, p.idle = ParseMB42(joinLines(rec["B"]))
	}
	p.pos, p.posOK = regInt(rec, "p")
	p.play, p.playOK = regInt(rec, "t")
	p.vol, p.volOK = regInt(rec, "v")
	p.hadData = p.hasB || p.posOK || p.playOK || p.volOK
	p.sysinfo = parseSysInfo(rec["s"])
	p.devinfo = parseDevInfo(rec["i"])
	p.confinfo = parseConfInfo(rec["c"])
	p.details = parseDevDetails(rec["d"])
	p.mroom = parseMultiroom(rec["g"])
	p.night, p.nightOK = parseNight(rec["n"])
	p.logs, p.hasLog = parseLogs(rec)
	return p
}

// parseLogs decodes the @@l section: the device syslog tail. The device prefixes
// every line with a space so a log line beginning with "@@" cannot be mistaken
// for a section header, so that one space comes back off here. Present-but-empty
// is meaningful (the filter matched nothing) and must not read as "no answer",
// hence the comma-ok on the section rather than a length test.
func parseLogs(rec Record) ([]string, bool) {
	lines, ok := rec["l"]
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, printable(strings.TrimPrefix(ln, " ")))
	}
	return out, true
}

// parseNight decodes the @@n section — the raw `amixer cget` output for the
// multi-band DRC enable — into on/off. Only an explicit `values=on` /
// `values=off` line counts; an absent section, a failed read (no such
// control, amixer missing) or anything else is "unknown" so the UI never
// paints a state the device didn't report.
func parseNight(lines []string) (on, ok bool) {
	for _, ln := range lines {
		_, after, found := strings.Cut(printable(ln), "values=")
		if !found {
			continue
		}
		switch strings.TrimSpace(after) {
		case "on":
			return true, true
		case "off":
			return false, true
		}
	}
	return false, false
}

// ApplyRecord applies one framed record under the State lock and reports whether
// it carried usable player data. last_rx stamps on every framed record (link
// liveness for the watchdog); last_data/connected only on records carrying data.
// Track updates only when a B section is present.
func ApplyRecord(st *State, rec Record) bool {
	p := parseRecord(rec) // lock-free: the critical section below is pure assignment
	now := time.Now()

	st.mu.Lock()
	defer st.mu.Unlock()
	st.lastRx = now
	if p.sysinfo != nil {
		st.updateNet(p.sysinfo, now)
		st.updateLevel(p.sysinfo)
		st.sysinfo = p.sysinfo
	}
	if p.devinfo != nil {
		st.devinfo = p.devinfo
	}
	if p.confinfo != nil {
		st.confinfo = p.confinfo
	}
	if p.hasLog {
		st.logs, st.logsAt = p.logs, now
	}
	if p.details != nil {
		st.details = p.details
	}
	if p.mroom != nil {
		st.mroom = p.mroom
	}
	if p.nightOK {
		st.night, st.nightKnown = p.night, true
		if !st.nightOrigKnown {
			// The first readback of the State's lifetime is the value to put
			// back on quit. Later @@n sections are echoes of lp10's own sets
			// (or a reconnect's re-read of a value lp10 already changed), so
			// they must not move the baseline.
			st.nightOrig, st.nightOrigKnown = p.night, true
		}
	}
	if p.hasB {
		switch {
		case p.track != nil:
			st.track, st.trackAt = p.track, now
			st.garbageBs = 0
		case p.idle:
			st.track = nil // definitive idle: clear now
			st.garbageBs = 0
		default:
			// Garbage B (unparseable reg-42 read). The loop ships @@B on
			// change, so mid-song trackAt is stale and the time debounce alone
			// cannot tell one corrupt read from a real stop — a single bad
			// read must not blank now-playing. Clear only on the second
			// consecutive garbage B, and never inside the post-change window.
			st.garbageBs++
			if st.garbageBs >= 2 && (st.track == nil || now.Sub(st.trackAt) > DebounceWindow) {
				st.track = nil
			}
		}
	}
	if p.posOK {
		st.posMs, st.posAt = max(0, p.pos), now
	}
	if p.playOK && !now.Before(st.playHold) {
		if p.play == 0 && st.playing != 0 && !p.posOK {
			st.posAt = now // external resume: clock restarts at last position
		}
		st.playing = p.play
	}
	if p.volOK && !now.Before(st.volHold) {
		st.vol = clamp100(p.vol)
	}
	if p.hadData {
		st.lastData = now
		st.datalessDeaths = 0 // data proves the device reachable again
		if !st.connected {
			st.retryBase = st.attempts // badge counts per-outage
		}
		st.connected = true
		st.gotRecord = true
	}
	return p.hadData
}

// parseSysInfo parses the @@s positional stats line into a SysInfo (nil if the
// section is absent or shorter than the required prefix). See the sf* index
// constants for the column order shared with the device loop's emitter.
func parseSysInfo(lines []string) *SysInfo {
	if len(lines) == 0 {
		return nil
	}
	f := strings.Fields(printable(lines[0]))
	if len(f) < sysRequired {
		return nil
	}
	si := &SysInfo{
		Up:    f[sfUp],
		Load:  f[sfLoad1] + " " + f[sfLoad5] + " " + f[sfLoad15],
		Avail: f[sfAvail], Total: f[sfTotal], NCPU: f[sfNCPU], FW: f[sfFW],
	}
	// optional trailing extras (newer loops, diag-gated); older loops stop
	// short -> opt()="".
	opt := func(i int) string {
		if i < len(f) && f[i] != "-" {
			return f[i]
		}
		return ""
	}
	si.OS = opt(sfOS)
	si.TempmC = opt(sfTempmC)
	si.RxBytes, si.TxBytes = opt(sfRxBytes), opt(sfTxBytes)
	si.SignalDBm, si.LinkQ = opt(sfSignalDBm), opt(sfLinkQ)
	si.PingClient, si.PingGw, si.PingNet = opt(sfPingClient), opt(sfPingGw), opt(sfPingNet)
	si.PcmState, si.BufAvail = opt(sfPcmState), opt(sfBufAvail)
	si.DacRate, si.DacFmt, si.DacCh = opt(sfDacRate), opt(sfDacFmt), opt(sfDacCh)
	si.BufSize, si.CpuKHz = opt(sfBufSize), opt(sfCpuKHz)
	si.Procs, si.NoiseDBm = opt(sfProcs), opt(sfNoiseDBm)
	si.RxErrs, si.TxErrs = opt(sfRxErrs), opt(sfTxErrs)
	si.RxDrop, si.TxDrop = opt(sfRxDrop), opt(sfTxDrop)
	si.Softvol = opt(sfSoftvol)
	return si
}

// parseDevInfo parses the @@i static device/network key=value block (nil if absent).
func parseDevInfo(lines []string) *DevInfo {
	if len(lines) == 0 {
		return nil
	}
	di := &DevInfo{}
	for _, ln := range lines {
		k, v, ok := strings.Cut(printable(ln), "=")
		if !ok {
			continue
		}
		switch k {
		case "net":
			di.Net = v
		case "iface":
			di.Iface = v
		case "ip":
			di.IP = v
		case "mac":
			di.MAC = v
		case "gw":
			di.Gateway = v
		case "speed":
			di.Speed = v
		case "duplex":
			di.Duplex = v
		case "ssid":
			di.SSID = v
		case "freq":
			di.Freq = v
		case "rate":
			di.Rate = v
		case "build":
			di.Build = v
		case "app":
			di.App = v
		case "platform":
			di.Platform = v
		case "name":
			di.Name = v
		case "data":
			if ff := strings.Fields(v); len(ff) == 2 {
				di.DataUsed, di.DataTotal = ff[0], ff[1]
			}
		case "dns":
			di.DNS = v
		}
	}
	// An all-junk block (lines present, nothing recognisable) must not wipe a
	// previously good once-per-connection readout — same guard as parseDevDetails.
	if *di == (DevInfo{}) {
		return nil
	}
	return di
}

// parseConfInfo parses the @@c capability key=value block, keeping only the
// confKeys allowlist (nil if absent).
func parseConfInfo(lines []string) *ConfInfo {
	if len(lines) == 0 {
		return nil
	}
	ci := &ConfInfo{Svc: make(map[string]string, len(confKeys))}
	for _, ln := range lines {
		if k, v, ok := strings.Cut(printable(ln), "="); ok && confKeys[k] {
			ci.Svc[k] = v
		}
	}
	// Spotify's running state is not shipped as its own line: which engine is
	// live already carries it, and the loop is at dropbear's command-length
	// ceiling, so it is derived here instead of costing a second wire field.
	if eng, ok := ci.Svc["spotify.eng"]; ok {
		ci.Svc["spotify"] = "off"
		if eng != "" {
			ci.Svc["spotify"] = "on"
		}
	}
	// Same all-junk guard as parseDevInfo/parseDevDetails.
	if len(ci.Svc) == 0 {
		return nil
	}
	return ci
}
