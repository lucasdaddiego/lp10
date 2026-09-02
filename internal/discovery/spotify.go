// Spotify Connect ZeroConf — the second ssh-free window into the box.
//
// The running Spotify engine advertises _spotify-connect._tcp over mDNS and
// serves a small unauthenticated HTTP endpoint at the advertised port
// (GET /zc?action=getInfo). Its answer carries what no other surface does
// without a root shell: whether the engine is up at all, which eSDK build it is
// running, and (when it says so) who is signed in. The port is per engine —
// the new engine answers on 9095, the legacy one on 9096 — which is exactly why
// it is taken from the SRV record rather than remembered.

package discovery

import (
	"cmp"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const spotifyService = "_spotify-connect._tcp.local"

// SpotifyEndpoint is one advertised ZeroConf HTTP endpoint.
type SpotifyEndpoint struct {
	Name string // the instance label, e.g. "Living"
	Host string // SRV target (.local host)
	Port int    // SRV port (9096 on firmware 8530; 9095 before)
	IP   net.IP // the host's first IPv4 A record, when one arrived
}

// Addr is the host:port to GET: the IP when known, else the .local host.
func (e SpotifyEndpoint) Addr() string {
	h := strings.TrimSuffix(e.Host, ".")
	if len(e.IP) > 0 {
		h = e.IP.String()
	}
	return net.JoinHostPort(h, strconv.Itoa(e.Port))
}

// spotifyEndpoints assembles the endpoints in a batch of records: PTRs for the
// service name instances, SRVs give host+port, As give the host's address.
func spotifyEndpoints(recs []rr) []SpotifyEndpoint {
	insts := map[string]string{} // canonical instance -> advertised spelling
	srv := map[string]rr{}
	a := map[string]net.IP{}
	for _, r := range recs {
		switch r.typ {
		case typePTR:
			if strings.EqualFold(strings.TrimSuffix(r.name, "."), spotifyService) && r.target != "" {
				if _, seen := insts[dnsKey(r.target)]; !seen {
					insts[dnsKey(r.target)] = r.target
				}
			}
		case typeSRV:
			if r.target != "" && r.port != 0 {
				srv[dnsKey(r.name)] = r
			}
		case typeA:
			if ip := r.ip.To4(); ip != nil {
				if _, seen := a[dnsKey(r.name)]; !seen {
					a[dnsKey(r.name)] = ip
				}
			}
		}
	}
	var out []SpotifyEndpoint
	for key, inst := range insts {
		s, ok := srv[key]
		if !ok {
			continue
		}
		label := strings.TrimSuffix(inst, ".")
		if i := strings.Index(strings.ToLower(label), "."+spotifyService); i >= 0 {
			label = label[:i]
		}
		out = append(out, SpotifyEndpoint{Name: label, Host: s.target, Port: int(s.port), IP: a[dnsKey(s.target)]})
	}
	slices.SortFunc(out, func(x, y SpotifyEndpoint) int {
		return cmp.Or(strings.Compare(x.Name, y.Name), strings.Compare(x.Host, y.Host))
	})
	return out
}

// matchSpotify finds the endpoint that is positively the device at host: by A
// record when the caller resolved host to ip, else by SRV host name.
func matchSpotify(eps []SpotifyEndpoint, host string, ip net.IP) (SpotifyEndpoint, bool) {
	want := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range eps {
		if len(ip) > 0 && e.IP.Equal(ip) {
			return e, true
		}
		h := strings.ToLower(strings.TrimSuffix(e.Host, "."))
		if want != "" && (h == want || h == want+".local" || strings.TrimSuffix(h, ".local") == want) {
			return e, true
		}
	}
	return SpotifyEndpoint{}, false
}

// pickSpotify is matchSpotify with one fallback: exactly one advertiser and no
// match takes that one — a single Spotify speaker on the LAN is the box.
// Several advertisers with no match is a genuine ambiguity and yields nothing
// rather than a guess at someone else's speaker.
func pickSpotify(eps []SpotifyEndpoint, host string, ip net.IP) (SpotifyEndpoint, bool) {
	if e, ok := matchSpotify(eps, host, ip); ok {
		return e, true
	}
	if len(eps) == 1 {
		return eps[0], true
	}
	return SpotifyEndpoint{}, false
}

// FindSpotifyZC queries mDNS for the Spotify Connect service on every interface
// (like FindLP10) and returns the endpoint belonging to host — matched by the
// address host resolves to, or by name — waiting up to timeout for a match and
// falling back to a sole advertiser. ip may be nil when the caller could not
// resolve the host.
func FindSpotifyZC(host string, ip net.IP, timeout time.Duration) (SpotifyEndpoint, bool) {
	raddr, err := net.ResolveUDPAddr("udp4", mdnsAddr)
	if err != nil {
		return SpotifyEndpoint{}, false
	}
	conns := openQuerySockets()
	if len(conns) == 0 {
		return SpotifyEndpoint{}, false
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	query := buildQuery(spotifyService, typePTR)
	sendAll := func() {
		for _, c := range conns {
			_, _ = c.WriteToUDP(query, raddr)
		}
	}
	sendAll()
	packets := make(chan []byte, 64)
	done := make(chan struct{})
	spawnReaders(conns, packets, done)

	var recs []rr
	overall := time.NewTimer(timeout)
	defer overall.Stop()
	resend := time.NewTicker(timeout/3 + time.Millisecond)
	defer resend.Stop()
	for {
		select {
		case p := <-packets:
			if rs, ok := parsePacket(p); ok {
				recs = append(recs, rs...)
				// Early exit only on a positive match; a sole advertiser is
				// accepted at the timeout, once nothing else has had its say.
				if e, ok := matchSpotify(spotifyEndpoints(recs), host, ip); ok {
					close(done)
					return e, true
				}
			}
		case <-resend.C:
			sendAll()
		case <-overall.C:
			close(done)
			return pickSpotify(spotifyEndpoints(recs), host, ip)
		}
	}
}

// SpotifyZCInfo is the getInfo answer, trimmed to what lp10 shows. Every string
// is LAN input: control-stripped and length-capped here, once.
type SpotifyZCInfo struct {
	Status         int    // 101 = OK on the device
	StatusString   string // "OK", "ERROR-…"
	ActiveUser     string // the signed-in Spotify user id ("" when nobody is)
	LibraryVersion string // the eSDK build, e.g. "3.203.239-g1d6bd565"
	Version        string // the ZeroConf API version, e.g. "2.9.0"
	RemoteName     string // the speaker's advertised name
	DeviceType     string // "SPEAKER"
}

const (
	maxZCField = 64
	maxZCBody  = 64 << 10
)

// zcField control-strips and caps one string field.
func zcField(v any) string {
	s, _ := v.(string)
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len() >= maxZCField {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// parseSpotifyZC decodes a getInfo body. false for anything that is not a JSON
// object carrying at least a status or a version.
func parseSpotifyZC(body []byte) (SpotifyZCInfo, bool) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return SpotifyZCInfo{}, false
	}
	info := SpotifyZCInfo{
		StatusString:   zcField(m["statusString"]),
		ActiveUser:     zcField(m["activeUser"]),
		LibraryVersion: zcField(m["libraryVersion"]),
		Version:        zcField(m["version"]),
		RemoteName:     zcField(m["remoteName"]),
		DeviceType:     zcField(m["deviceType"]),
	}
	if f, ok := m["status"].(float64); ok && f >= 0 && f < 1e6 {
		info.Status = int(f)
	}
	if info.Status == 0 && info.StatusString == "" && info.Version == "" {
		return SpotifyZCInfo{}, false
	}
	return info, true
}

// ProbeSpotifyZC GETs the endpoint's getInfo (addr is host:port) and parses the
// answer. One request; false on any transport, status or shape failure.
func ProbeSpotifyZC(addr string, timeout time.Duration) (SpotifyZCInfo, bool) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://" + addr + "/zc?action=getInfo")
	if err != nil {
		return SpotifyZCInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SpotifyZCInfo{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxZCBody))
	if err != nil {
		return SpotifyZCInfo{}, false
	}
	return parseSpotifyZC(body)
}
