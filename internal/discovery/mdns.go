// Package discovery finds the LP10 on the LAN with a one-shot multicast-DNS
// query — no dependency, and nothing bound to :5353 (which the OS responder
// already owns). The query sets the unicast-response (QU) bit, so responders
// reply straight to our ephemeral port; we never join the multicast group.
//
// The device's AirPlay daemon advertises _raop._tcp with the model in the TXT
// (am=LP10) and "<MAC>@<FriendlyName>" as the instance, so one query yields the
// fingerprint that distinguishes our LP10 from other speakers, plus the SRV
// target (its .local host) and the A record (its current IP).
package discovery

import (
	"cmp"
	"encoding/binary"
	"net"
	"slices"
	"strings"
	"time"
)

const (
	mdnsAddr  = "224.0.0.251:5353"
	service   = "_raop._tcp.local" // RAOP: TXT am=<model>, instance <MAC>@<name>
	modelLP10 = "LP10"

	typeA   uint16 = 1
	typePTR uint16 = 12
	typeTXT uint16 = 16
	typeSRV uint16 = 33

	classQU uint16 = 0x8000 // unicast-response-requested, OR'd into the question class
)

// Device is a discovered LinkPlay/Arylic endpoint.
type Device struct {
	Name  string // friendly name (after '@' in the RAOP instance), e.g. "Living"
	Model string // TXT am= value, e.g. "LP10"
	MAC   string // before '@' in the instance, e.g. "AABBCCDDEEFF"
	Host  string // SRV target (.local host), e.g. "Living.local"
	IP    net.IP // first IPv4 A record, when one arrived
}

// Addr is the address to connect to: the IP when known, else the .local host
// (which the OS resolver handles on macOS).
func (d Device) Addr() string {
	if len(d.IP) > 0 {
		return d.IP.String()
	}
	return strings.TrimSuffix(d.Host, ".")
}

// FindLP10 sends mDNS queries and watches for replies up to timeout, returning
// the LP10 whose friendly name matches nameHint (a substring match against, say,
// the config "name"), or the sole/first LP10 otherwise. It returns early the
// moment a fully-resolved candidate (with an IP) arrives that the hint endorses
// — an unhinted search accepts any LP10, but a non-matching device never
// short-circuits a hinted one, since the named device may simply be slower to
// answer. So a present device is usually found in well under 100ms; absence —
// or a hinted name that nothing on the LAN answers to — costs the full timeout,
// after which the non-matching devices come back into play as the fallback.
//
// The query is sent out EVERY up, multicast-capable interface — each from its own
// IPv4 source address — not just the OS default route. That is what makes it work
// on a multi-homed Mac: docked Ethernet, an active VPN (utun), or a Wi-Fi that was
// just switched to and isn't the default route yet would all otherwise swallow a
// single INADDR_ANY query out the wrong NIC, so a device living on another
// interface was missed. Each socket is retransmitted within the window (mDNS is
// lossy UDP); a present device's first unicast reply early-exits. The configured
// host stays the fallback when nothing answers.
func FindLP10(nameHint string, timeout time.Duration) (Device, bool) {
	raddr, err := net.ResolveUDPAddr("udp4", mdnsAddr)
	if err != nil {
		return Device{}, false
	}
	conns := openQuerySockets()
	if len(conns) == 0 {
		return Device{}, false
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	query := buildQuery(service, typePTR)
	sendAll := func() {
		for _, c := range conns {
			_, _ = c.WriteToUDP(query, raddr)
		}
	}
	sendAll()

	// One reader goroutine per socket funnels raw packets to the collector, which
	// only this goroutine touches (so no lock is needed).
	packets := make(chan []byte, 64)
	done := make(chan struct{})
	spawnReaders(conns, packets, done)

	col := newCollector()
	overall := time.NewTimer(timeout)
	defer overall.Stop()
	// Retransmit a couple of times within the window; a present device almost
	// always answers the first query, so this only matters under packet loss.
	resend := time.NewTicker(timeout/3 + time.Millisecond)
	defer resend.Stop()

	for {
		select {
		case p := <-packets:
			if recs, ok := parsePacket(p); ok {
				col.add(recs)
				// Early exit only on a candidate the hint actually endorses: with
				// several LP10s on the LAN the named one may answer later, and
				// pickLP10's first-device fallback must not let a faster wrong
				// device hijack the race. Unhinted, any complete LP10 wins.
				if d, ok := pickLP10(col.devices(), nameHint); ok && len(d.IP) > 0 &&
					(nameHint == "" || hintMatches(d, nameHint)) {
					close(done)
					return d, true // complete, endorsed candidate — stop early
				}
			}
		case <-resend.C:
			sendAll()
		case <-overall.C:
			close(done)
			return pickLP10(col.devices(), nameHint) // timed out: accept a host-only match too
		}
	}
}

// spawnReaders starts one goroutine per socket, funneling each raw reply packet
// into packets. Closing done unblocks a reader parked on the channel send when
// the caller stops early; closing the sockets unblocks one parked in
// ReadFromUDP — so no reader leaks either way.
func spawnReaders(conns []*net.UDPConn, packets chan<- []byte, done <-chan struct{}) {
	for _, c := range conns {
		go func(c *net.UDPConn) {
			buf := make([]byte, 9000)
			for {
				n, _, rerr := c.ReadFromUDP(buf)
				if n > 0 {
					p := append([]byte(nil), buf[:n]...)
					select {
					case packets <- p:
					case <-done:
						return
					}
				}
				if rerr != nil {
					return
				}
			}
		}(c)
	}
}

// openQuerySockets opens one UDP socket per up, non-loopback, multicast-capable
// interface, each bound to that interface's IPv4 address so the query egresses
// that specific NIC (the kernel picks the multicast egress interface from the
// bound source address). It falls back to a single INADDR_ANY socket — the OS
// default multicast route, the original behaviour — when no interface address is
// usable, so discovery never regresses.
func openQuerySockets() []*net.UDPConn {
	var conns []*net.UDPConn
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue // want a routable IPv4, not ::1/127/169.254
			}
			if c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip4, Port: 0}); err == nil {
				conns = append(conns, c)
				break // the first bindable IPv4 on this interface is enough
			}
			// An interface can carry multiple IPv4 addresses. If the first is
			// stale/unbindable, try the next instead of silently discarding the
			// whole interface.
		}
	}
	if len(conns) == 0 {
		if c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0}); err == nil {
			conns = append(conns, c)
		}
	}
	return conns
}

// ---- selection --------------------------------------------------------------

func pickLP10(ds []Device, nameHint string) (Device, bool) {
	var lp []Device
	for _, d := range ds {
		if strings.EqualFold(d.Model, modelLP10) && (len(d.IP) > 0 || d.Host != "") {
			lp = append(lp, d)
		}
	}
	if len(lp) == 0 {
		return Device{}, false
	}
	// stable order so an unhinted pick among several LP10s is deterministic
	slices.SortFunc(lp, func(a, b Device) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.MAC, b.MAC))
	})
	if nameHint != "" {
		for _, d := range lp {
			if hintMatches(d, nameHint) {
				return d, true
			}
		}
	}
	return lp[0], true
}

// hintMatches reports whether d's advertised name appears in the user's name
// hint (e.g. hint "LP10 · Living" matches a device named "Living"). An empty
// device name never matches — strings.Contains(hint, "") is always true.
func hintMatches(d Device, hint string) bool {
	return d.Name != "" && strings.Contains(strings.ToLower(hint), strings.ToLower(d.Name))
}

// ---- record collection ------------------------------------------------------

type collector struct {
	instances map[string]string            // canonical PTR target -> original spelling
	srv       map[string]string            // canonical instance -> SRV target host
	txt       map[string]map[string]string // canonical instance -> TXT key/value
	a         map[string][]net.IP          // canonical host -> A records
}

func newCollector() *collector {
	return &collector{
		instances: map[string]string{},
		srv:       map[string]string{},
		txt:       map[string]map[string]string{},
		a:         map[string][]net.IP{},
	}
}

// dnsKey canonicalizes a DNS name for joins. DNS names are case-insensitive,
// and responders are allowed to vary case (or a trailing root dot) between the
// PTR target and the SRV/TXT owner. Treating the wire spellings as map keys
// verbatim could leave a complete reply looking unresolved.
func dnsKey(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func (c *collector) add(recs []rr) {
	for _, r := range recs {
		switch r.typ {
		case typePTR:
			if strings.EqualFold(strings.TrimSuffix(r.name, "."), service) && r.target != "" {
				key := dnsKey(r.target)
				if _, exists := c.instances[key]; !exists {
					c.instances[key] = r.target
				}
			}
		case typeSRV:
			if r.target != "" {
				c.srv[dnsKey(r.name)] = r.target
			}
		case typeTXT:
			key := dnsKey(r.name)
			m := c.txt[key]
			if m == nil {
				m = map[string]string{}
				c.txt[key] = m
			}
			for _, kv := range r.txt {
				if k, v, ok := strings.Cut(kv, "="); ok {
					m[strings.ToLower(k)] = v
				}
			}
		case typeA:
			if ip := r.ip.To4(); ip != nil {
				key := dnsKey(r.name)
				c.a[key] = append(c.a[key], ip)
			}
		}
	}
}

func (c *collector) devices() []Device {
	var ds []Device
	for key, inst := range c.instances {
		label := strings.TrimSuffix(inst, ".")
		suffix := "." + service
		if len(label) >= len(suffix) && strings.EqualFold(label[len(label)-len(suffix):], suffix) {
			label = label[:len(label)-len(suffix)] // "<MAC>@<name>", preserving advertised case
		}
		mac, name := label, ""
		if i := strings.LastIndex(label, "@"); i >= 0 {
			mac, name = label[:i], label[i+1:]
		}
		d := Device{Name: name, MAC: mac}
		if m, ok := c.txt[key]; ok {
			d.Model = m["am"]
		}
		if h, ok := c.srv[key]; ok {
			d.Host = h
			if ips := c.a[dnsKey(h)]; len(ips) > 0 {
				d.IP = ips[0]
			}
		}
		ds = append(ds, d)
	}
	return ds
}

// ---- DNS wire format --------------------------------------------------------

type rr struct {
	name   string
	typ    uint16
	target string   // PTR/SRV target name
	port   uint16   // SRV port
	txt    []string // TXT strings
	ip     net.IP   // A address
}

func be16(b []byte, i int) uint16 { return binary.BigEndian.Uint16(b[i:]) }

func encodeName(name string) []byte {
	var b []byte
	for lbl := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if lbl == "" || len(lbl) > 63 { // 63 = max DNS label; a longer one would corrupt the length byte
			continue
		}
		b = append(b, byte(len(lbl)))
		b = append(b, lbl...)
	}
	return append(b, 0)
}

func buildQuery(name string, qtype uint16) []byte {
	msg := make([]byte, 12) // header: id=0, flags=0, counts=0…
	msg[5] = 1              // QDCOUNT = 1
	msg = append(msg, encodeName(name)...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	return binary.BigEndian.AppendUint16(msg, classQU|1) // QCLASS: QU | IN
}

// parseName decodes a (possibly compressed) name starting at off, returning the
// dotted name and the offset of the byte after the name in the wire stream.
func parseName(msg []byte, off int) (string, int, bool) {
	var sb strings.Builder
	next, jumped, jumps := -1, false, 0
	for {
		if off < 0 || off >= len(msg) {
			return "", 0, false
		}
		l := int(msg[off])
		switch {
		case l == 0:
			off++
			if !jumped {
				next = off
			}
			return sb.String(), next, true
		case l&0xC0 == 0xC0: // compression pointer
			if off+1 >= len(msg) {
				return "", 0, false
			}
			if !jumped {
				next = off + 2
			}
			jumped = true
			if jumps++; jumps > 64 {
				return "", 0, false
			}
			off = (l&0x3F)<<8 | int(msg[off+1])
		case l&0xC0 != 0:
			// 01xxxxxx and 10xxxxxx are reserved DNS label forms, not literal
			// lengths. Reject them instead of interpreting 0x40..0xbf as a
			// giant label and potentially accepting malformed packets.
			return "", 0, false
		default:
			off++
			if off+l > len(msg) {
				return "", 0, false
			}
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.Write(msg[off : off+l])
			off += l
		}
	}
}

func parseTXT(rd []byte) []string {
	var out []string
	for i := 0; i < len(rd); {
		l := int(rd[i])
		i++
		if i+l > len(rd) {
			break
		}
		out = append(out, string(rd[i:i+l]))
		i += l
	}
	return out
}

// parsePacket extracts the resource records (PTR/SRV/TXT/A) from one mDNS
// message, across the answer, authority, and additional sections.
func parsePacket(msg []byte) ([]rr, bool) {
	if len(msg) < 12 {
		return nil, false
	}
	qd, an, ns, ar := int(be16(msg, 4)), int(be16(msg, 6)), int(be16(msg, 8)), int(be16(msg, 10))
	off := 12
	for range qd { // skip questions
		_, no, ok := parseName(msg, off)
		if !ok {
			return nil, false
		}
		off = no + 4 // QTYPE + QCLASS
		if off > len(msg) {
			return nil, false
		}
	}
	var out []rr
	for i := 0; i < an+ns+ar; i++ {
		name, no, ok := parseName(msg, off)
		if !ok || no+10 > len(msg) {
			return nil, false
		}
		typ := be16(msg, no)
		rdlen := int(be16(msg, no+8))
		rdStart := no + 10
		if rdStart+rdlen > len(msg) {
			return nil, false
		}
		r := rr{name: name, typ: typ}
		switch typ {
		case typePTR:
			if target, end, ok := parseName(msg, rdStart); ok && end <= rdStart+rdlen {
				r.target = target
			}
		case typeSRV:
			if rdlen >= 7 { // priority(2) weight(2) port(2) target
				if target, end, ok := parseName(msg, rdStart+6); ok && end <= rdStart+rdlen {
					r.target = target
					r.port = be16(msg, rdStart+4)
				}
			}
		case typeTXT:
			r.txt = parseTXT(msg[rdStart : rdStart+rdlen])
		case typeA:
			if rdlen == 4 {
				r.ip = net.IPv4(msg[rdStart], msg[rdStart+1], msg[rdStart+2], msg[rdStart+3])
			}
		}
		out = append(out, r)
		off = rdStart + rdlen
	}
	return out, true
}
