// The baseline and the report: loading, merging and saving the previous
// sweep, the diff against it, and the human-readable report.

package sweep

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lucasdaddiego/lp10/internal/atomicfile"
)

// Load reads the previous sweep from path ("" or a missing/garbled file: nil).
// A baseline from the ssh sweeps loads too: its ssh-side fields are ignored,
// and its LSSDP, ZeroConf, manifest and bundle facts still compare.
func Load(path string) *Report {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r Report
	if json.Unmarshal(b, &r) != nil || r.At.IsZero() {
		return nil
	}
	return &r
}

// Save writes r — the merge Main builds — as the new baseline (0600: the
// report holds nothing secret, but it is the user's device inventory). It goes
// through atomicfile, like the other persisted state: written in place, a kill
// mid-write would leave a truncated file that Load drops, and the next sweep
// would have nothing to compare with.
func Save(path string, r Report) error {
	if path == "" {
		return fmt.Errorf("no state directory")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'))
}

// A fact is one unit of the baseline merge: the fields one read fills
// together, under the key Carried dates it by. has says whether a report holds
// a reading of it (what Diff needs to compare it), and take copies that
// reading from src into dst.
type fact struct {
	key, label string
	has        func(r *Report) bool
	take       func(dst, src *Report)
}

// facts lists the baseline's facts in the report's order. The tunnel's
// answers are four facts, not one: the device drops a query now and then, and
// one missing answer must not take the others' last values with it.
func facts() []fact {
	return []fact{
		{"ports", "tcp ports", func(r *Report) bool { return r.Ports.OK },
			func(d, s *Report) { d.Ports = s.Ports }},
		{"tunnel.ver", "mcu", func(r *Report) bool { return r.Tunnel.Ver != "" },
			func(d, s *Report) { d.Tunnel.Ver, d.Tunnel.MCU = s.Tunnel.Ver, s.Tunnel.MCU }},
		{"tunnel.peq", "eq presets", func(r *Report) bool { return r.Tunnel.Presets != "" },
			func(d, s *Report) { d.Tunnel.Presets = s.Tunnel.Presets }},
		{"tunnel.lst", "sources", func(r *Report) bool { return r.Tunnel.Sources != "" },
			func(d, s *Report) { d.Tunnel.Sources = s.Tunnel.Sources }},
		{"tunnel.settings", "settings", func(r *Report) bool { return len(r.Tunnel.Settings) > 0 },
			func(d, s *Report) { d.Tunnel.Settings = maps.Clone(s.Tunnel.Settings) }},
		{"upnp", "upnp", func(r *Report) bool { return r.UPnP.OK },
			func(d, s *Report) { d.UPnP = s.UPnP }},
		{"lssdp", "lssdp", func(r *Report) bool { return r.LSSDP.OK },
			func(d, s *Report) { d.LSSDP = s.LSSDP }},
		{"zeroconf", "zeroconf", func(r *Report) bool { return r.ZeroConf.OK },
			func(d, s *Report) { d.ZeroConf = s.ZeroConf }},
		{"appIndex", "vendor app", func(r *Report) bool { return r.VendorApp.OK },
			func(d, s *Report) { d.VendorApp = s.VendorApp }},
		{"manifest", "manifest", func(r *Report) bool { return r.Manifest.Asked && r.Manifest.Err == "" && verdict(r.Manifest) != "" },
			func(d, s *Report) { d.Manifest = s.Manifest }},
		{"bundle", "newest bundle", func(r *Report) bool { return bundleState(r.Bundle) != "" },
			func(d, s *Report) { d.Bundle = s.Bundle }},
	}
}

// merge is the baseline a sweep leaves: cur, with every fact it did not read
// kept from prev — the baseline it was compared with — and dated in Carried to
// when that value was read, so a fact carried again keeps its first date. A
// fact neither sweep read stays unread. Without the merge, a sweep that lost
// one read (a tunnel that never answered, a vendor that timed out) would save
// a hollow baseline — Diff skips empty pairs, so the next sweep would call a
// real MCU update "nothing changed" — or keep the old one whole and drop this
// sweep's fresh answers.
func merge(prev *Report, cur Report) Report {
	out := cur
	out.Carried = nil
	if prev == nil {
		return out
	}
	for _, f := range facts() {
		if f.has(&cur) || !f.has(prev) {
			continue
		}
		f.take(&out, prev)
		if out.Carried == nil {
			out.Carried = map[string]time.Time{}
		}
		out.Carried[f.key] = prev.readAt(f.key)
	}
	return out
}

// readAt is when r's value for the fact key was read: its Carried date, or
// r's own sweep.
func (r *Report) readAt(key string) time.Time {
	if at, ok := r.Carried[key]; ok {
		return at
	}
	return r.At
}

// carriedNote names the facts a baseline holds from before its own sweep,
// and as of when: "mcu, eq presets as of Sep 22 10:00 · lssdp as of Sep 21
// 09:00". Facts kept from one sweep share a date and one entry. "" when every
// fact is the sweep's own.
func carriedNote(b Report) string {
	type group struct {
		at     time.Time
		labels []string
	}
	var groups []group
	for _, f := range facts() {
		at, ok := b.Carried[f.key]
		if !ok {
			continue
		}
		i := slices.IndexFunc(groups, func(g group) bool { return g.at.Equal(at) })
		if i < 0 {
			groups = append(groups, group{at: at})
			i = len(groups) - 1
		}
		groups[i].labels = append(groups[i].labels, f.label)
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = strings.Join(g.labels, ", ") + " as of " + g.at.Format("Jan 2 15:04")
	}
	return strings.Join(parts, " · ")
}

// ---- diff & report ----

// Change is one field that differs from the baseline.
type Change struct{ Field, Was, Now string }

// Diff lists what moved between two sweeps, in a stable order. Probes that did
// not answer this time (or last time) are not "changes" — only two answers that
// disagree are. The tunnel's settings are never compared: a change there is
// the user's, not an update.
func Diff(prev, cur Report) []Change {
	var out []Change
	cmp := func(field, was, now string) {
		if was != "" && now != "" && was != now {
			out = append(out, Change{field, was, now})
		}
	}
	cmp("tcp ports", portsState(prev.Ports), portsState(cur.Ports))
	cmp("mcu", prev.Tunnel.Ver, cur.Tunnel.Ver)
	cmp("eq presets", prev.Tunnel.Presets, cur.Tunnel.Presets)
	cmp("sources", prev.Tunnel.Sources, cur.Tunnel.Sources)
	if prev.UPnP.OK && cur.UPnP.OK {
		cmp("upnp model", prev.UPnP.ModelNumber, cur.UPnP.ModelNumber)
		cmp("upnp services", servicesFact(prev.UPnP.Services), servicesFact(cur.UPnP.Services))
	}
	if prev.LSSDP.OK && cur.LSSDP.OK {
		cmp("lssdp firmware", prev.LSSDP.FW, cur.LSSDP.FW)
		cmp("lssdp netmode", prev.LSSDP.NetMode, cur.LSSDP.NetMode)
	}
	if prev.ZeroConf.OK && cur.ZeroConf.OK {
		cmp("zeroconf port", strconv.Itoa(prev.ZeroConf.Port), strconv.Itoa(cur.ZeroConf.Port))
		cmp("spotify eSDK", prev.ZeroConf.LibraryVersion, cur.ZeroConf.LibraryVersion)
	}
	cmp("vendor app", prev.VendorApp.Version, cur.VendorApp.Version)
	cmp("vendor app md5", prev.VendorApp.MD5, cur.VendorApp.MD5)
	if prev.Manifest.Asked && cur.Manifest.Asked && prev.Manifest.Err == "" && cur.Manifest.Err == "" {
		cmp("vendor verdict", verdict(prev.Manifest), verdict(cur.Manifest))
	}
	cmp("newest bundle", bundleState(prev.Bundle), bundleState(cur.Bundle))
	if prev.Bundle.Err == "" && cur.Bundle.Err == "" {
		cmp("bundle etag", prev.Bundle.ETag, cur.Bundle.ETag)
	}
	return out
}

// servicesFact is a service list for display, the standard prefix shortened.
func servicesFact(s []string) string {
	names := make([]string, len(s))
	for i, urn := range s {
		names[i] = serviceName(urn)
	}
	return strings.Join(names, " ")
}

// bundleState is the newest-bundle answer as one comparable word: the build
// offered, "none offered", or "" when the probe failed. Only two answers make
// a change, so a failed probe never reads as one — but the vendor starting, or
// stopping, to offer a package does.
func bundleState(b BundleFacts) string {
	switch {
	case b.Err != "":
		return ""
	case b.None:
		return "none offered"
	}
	return b.Build
}

func verdict(m ManifestFacts) string {
	if m.UpToDate {
		return "up to date"
	}
	if m.Offered != "" {
		return m.Offered + " offered"
	}
	return ""
}

// Write prints the human report: the device, the vendor's view, the live
// surface, then the diff against prev (or that this is the first sweep).
func Write(w io.Writer, r Report, prev *Report, now time.Time) {
	f := func(label, val string) {
		if val != "" {
			fmt.Fprintf(w, "  %-14s %s\n", label, val)
		}
	}
	dash := func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	}
	fmt.Fprintf(w, "lp10 sweep · %s · %s\n\n", r.Host, r.At.Format("2006-01-02 15:04"))
	fmt.Fprintln(w, "device")
	if r.LSSDP.FW != "" || r.Tunnel.Ver != "" {
		f("firmware", dash(r.LSSDP.FW)+" · mcu "+dash(r.Tunnel.Ver))
	}
	if !r.Tunnel.OK {
		f("tunnel", dash(r.Tunnel.Err)+" (the mcu, its presets, sources and settings are unread)")
	}
	if r.Tunnel.Sources != "" {
		src := r.Tunnel.Sources
		if now := r.Tunnel.Settings["SRC"]; now != "" {
			src += " · now " + now
		}
		f("sources", src)
	}
	f("eq presets", r.Tunnel.Presets)
	f("status", statusFact(r.Tunnel.Settings["STA"]))
	f("settings", settingsFact(r.Tunnel.Settings))
	if r.Tunnel.OK && len(r.Tunnel.Unanswered) > 0 {
		missing := strings.Join(r.Tunnel.Unanswered, " ")
		if r.Tunnel.Err != "" {
			missing += " (" + r.Tunnel.Err + ")"
		}
		f("no answer", missing)
	}
	if r.UPnP.OK {
		f("upnp", joinSet(" · ", r.UPnP.FriendlyName, r.UPnP.Manufacturer,
			joinSet(" ", r.UPnP.ModelName, r.UPnP.ModelNumber), r.UPnP.ModelDescription))
		f("upnp services", servicesFact(r.UPnP.Services))
	} else {
		f("upnp", "no description · "+dash(r.UPnP.Err))
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "vendor")
	switch {
	case !r.Manifest.Asked && r.LSSDP.FW == "":
		f("manifest", "not asked (no build to ask about)")
	case !r.Manifest.Asked:
		f("manifest", "not asked")
	case r.Manifest.Err != "":
		f("manifest", "check failed · "+r.Manifest.Err)
	case r.Manifest.UpToDate:
		f("manifest", "no update for "+r.Manifest.Build)
	default:
		f("manifest", r.Manifest.Offered+" offered for "+r.Manifest.Build)
	}
	switch {
	case r.Bundle.Err != "":
		f("newest bundle", r.Bundle.Err)
	case r.Bundle.None:
		f("newest bundle", "none offered — the manifest names no package, even for an old build")
	case r.Bundle.URL != "":
		f("newest bundle", r.Bundle.Build+" · "+r.Bundle.URL)
		size := "size unknown" // the CDN sent no Content-Length (Size -1)
		if r.Bundle.Size >= 0 {
			size = fmt.Sprintf("%d bytes", r.Bundle.Size)
		}
		f("", size+" · "+dash(r.Bundle.LastModified)+" · etag "+dash(r.Bundle.ETag))
	}
	switch {
	case r.VendorApp.OK:
		f("vendor app", "the vendor serves "+r.VendorApp.Name+" v"+r.VendorApp.Version+" · md5 "+short(r.VendorApp.MD5))
	case r.VendorApp.Err != "":
		f("vendor app", "index unread · "+r.VendorApp.Err)
	default:
		f("vendor app", "not asked")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "live surface")
	if r.LSSDP.OK {
		f("lssdp", r.LSSDP.FW+" · "+r.LSSDP.State+" · "+r.LSSDP.NetMode+" · "+r.LSSDP.Name)
	} else {
		f("lssdp", "no answer")
	}
	switch {
	case r.ZeroConf.OK:
		f("spotify", fmt.Sprintf(":%d · eSDK %s · zeroconf %s", r.ZeroConf.Port, dash(r.ZeroConf.LibraryVersion), dash(r.ZeroConf.Version)))
	case r.ZeroConf.Port != 0:
		f("spotify", fmt.Sprintf(":%d advertised · getInfo unanswered", r.ZeroConf.Port))
	default:
		f("spotify", "not advertised (no engine running)")
	}
	writePorts(f, r.Ports)
	fmt.Fprintln(w)

	// Whether this run becomes the baseline is not known here: Main decides
	// after the report, and says what it did.
	if prev == nil {
		fmt.Fprintln(w, "first sweep — nothing to compare with yet")
		return
	}
	// The baseline can hold facts older than its sweep (kept because that
	// sweep could not read them): the header says what the diff is really
	// against.
	about := []string{prev.At.Format("2006-01-02 15:04") + ", " + fmtDur(now.Sub(prev.At)) + " ago"}
	if note := carriedNote(*prev); note != "" {
		about = append(about, note)
	}
	fmt.Fprintf(w, "since the last sweep (%s)\n", strings.Join(about, " · "))
	ch := Diff(*prev, r)
	if len(ch) == 0 {
		fmt.Fprintln(w, "  nothing changed")
	}
	for _, c := range ch {
		fmt.Fprintf(w, "  %-16s %s → %s\n", c.Field, c.Was, c.Now)
	}
	// A fact this sweep read and the baseline never held — every new probe,
	// against a baseline from the ssh sweeps — has nothing to differ from.
	var fresh []string
	for _, fa := range facts() {
		if fa.has(&r) && !fa.has(prev) {
			fresh = append(fresh, fa.label)
		}
	}
	if len(fresh) > 0 {
		fmt.Fprintf(w, "  %-16s %s (nothing to compare with yet)\n", "first read", strings.Join(fresh, ", "))
	}
}

// writePorts prints the scan: the fixed ports, the dynamic ones apart (they
// move with every app restart, so they are never compared), and whether the
// debug listeners the firmware removed answer again — said even for a scan
// cut off, as long as it found one, or heard from all four.
func writePorts(f func(label, val string), p PortFacts) {
	var fixed, dynamic []int
	for _, n := range p.Open {
		if isDynamic(n) {
			dynamic = append(dynamic, n)
		} else {
			fixed = append(fixed, n)
		}
	}
	switch {
	case !p.OK && len(p.Open) == 0:
		f("tcp", "scan failed · "+p.Err)
	case !p.OK:
		f("tcp", fmtPorts(p.Open)+" · so far: "+p.Err)
	default:
		if len(fixed) == 0 {
			f("tcp", "nothing fixed open")
		} else {
			f("tcp", fmtPorts(fixed))
		}
		if len(dynamic) > 0 {
			f("dynamic tcp", fmtPorts(dynamic)+" (they move with each app restart; not compared)")
		}
	}
	var again []string
	var all []string
	for _, d := range debugPorts {
		all = append(all, strconv.Itoa(d.port))
		if slices.Contains(p.Open, d.port) {
			again = append(again, fmt.Sprintf("%d (%s)", d.port, d.name))
		}
	}
	switch {
	case len(again) > 0:
		f("ssh/telnet/adb", "answer again: "+strings.Join(again, " · "))
	case p.DebugChecked:
		f("ssh/telnet/adb", "closed ("+strings.Join(all, " ")+")")
	}
}

// joinSet joins the parts that are set, so a missing one leaves no gap.
func joinSet(sep string, parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), sep)
}

// short cuts h to its first 12 runes, marking the cut.
func short(h string) string {
	if utf8.RuneCountInString(h) > 12 {
		return string([]rune(h)[:12]) + "…"
	}
	return h
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// Main is the `lp10 sweep` entry point: parses the flags, runs the sweep,
// prints the report (or the JSON), and saves the new baseline — this sweep
// merged over the last one, so a fact it could not read keeps its last value
// and date — unless the run was interrupted, which leaves the previous
// baseline in place; then says which it did. Returns the exit code: 0; 1 when
// the box's own inventory is incomplete — the port scan or the tunnel failed
// (the report still prints, and the baseline still takes what was read); 2 for
// a bad flag.
