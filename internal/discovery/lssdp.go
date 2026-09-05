// LSSDP — the LibreWireless "LUCI SSDP" responder on UDP 1800. Unlike mDNS
// (which the AirPlay daemon answers) this is the device's own control stack
// answering, with no auth and no ssh in the loop: an M-SEARCH to the box — or
// to the SSDP multicast group — comes back as a plain "KEY:VALUE" header list
// (DeviceName, FWVERSION, State, NETMODE, USN, …). Two uses: a discovery
// fallback when mDNS is quiet, and a liveness probe that still works while
// the device's sshd is refusing lp10 (the rapid-reconnect lockout), so the
// "connecting…" state can say whether the box is there at all.

package discovery

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

// lssdpPort is the device's LSSDP responder port (UDP).
const lssdpPort = 1800

const lssdpMulticast = "239.255.255.250:1800"

// msearch is the discovery datagram; the device answers any ST.
const msearch = "M-SEARCH * HTTP/1.1\r\nHOST:239.255.255.250:1800\r\nMAN:\"ssdp:discover\"\r\nMX:1\r\nST:ssdp:all\r\n\r\n"

// LSSDPInfo is one responder's header list, trimmed to the fields lp10 uses.
// Every value is attacker-controllable LAN input: callers that render them
// must control-strip (protocol.Printable) first.
type LSSDPInfo struct {
	Name    string // DeviceName
	Model   string // CAST_MODEL, e.g. LP10 — the discovery fallback pins on it
	FW      string // FWVERSION, e.g. AR241CE_8530.23.2
	State   string // State, e.g. S
	NetMode string // NETMODE, e.g. ETH0 / WLAN0
	IP      net.IP // where the reply came from
}

// maxLSSDPField bounds one header value; the real ones are short.
const maxLSSDPField = 64

// parseLSSDP decodes a reply into its fields. A reply must look like an HTTP
// status line followed by "KEY:VALUE" headers; anything else yields false.
func parseLSSDP(b []byte) (LSSDPInfo, bool) {
	var info LSSDPInfo
	seen := 0
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if i == 0 {
			if !strings.HasPrefix(line, "HTTP/1.1 200") {
				return info, false
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) > maxLSSDPField {
			v = v[:maxLSSDPField]
		}
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "DEVICENAME":
			info.Name = v
		case "CAST_MODEL":
			info.Model = v
		case "FWVERSION":
			info.FW = v
		case "STATE":
			info.State = v
		case "NETMODE":
			info.NetMode = v
		default:
			continue
		}
		seen++
	}
	return info, seen > 0
}

// ProbeLSSDP sends one unicast M-SEARCH to host (a bare host uses the LSSDP
// port; an explicit host:port is honoured, for tests) and waits for its reply.
// timeout is the whole probe's budget, a hostname's lookup included: the
// .local name of an absent box can hold the resolver for seconds, and that
// wait must neither outlive the probe nor — ctx being the runtime's — a
// shutdown. One datagram each way; false on no/garbage answer.
func ProbeLSSDP(ctx context.Context, host string, timeout time.Duration) (LSSDPInfo, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		h, port = host, strconv.Itoa(lssdpPort)
	}
	raddr, ok := resolveUDP4(ctx, h, port)
	if !ok {
		return LSSDPInfo{}, false
	}
	c, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return LSSDPInfo{}, false
	}
	defer c.Close()
	// The deadline, or the runtime cancelling ctx, closes the socket — which
	// is what unblocks the read.
	defer context.AfterFunc(ctx, func() { c.Close() })()
	if _, err := c.Write([]byte(msearch)); err != nil {
		return LSSDPInfo{}, false
	}
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		return LSSDPInfo{}, false
	}
	info, ok := parseLSSDP(buf[:n])
	info.IP = raddr.IP
	return info, ok
}

// resolveUDP4 is net.ResolveUDPAddr("udp4") under a context: an IP literal is
// used as is, a name goes through the resolver bounded by ctx.
func resolveUDP4(ctx context.Context, host, port string) (*net.UDPAddr, bool) {
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return nil, false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return &net.UDPAddr{IP: ip4, Port: p}, true
		}
		return nil, false
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, false
	}
	for _, a := range addrs {
		if ip4 := a.IP.To4(); ip4 != nil {
			return &net.UDPAddr{IP: ip4, Port: p, Zone: a.Zone}, true
		}
	}
	return nil, false
}

// FindLP10LSSDP is the mDNS fallback: a multicast M-SEARCH out every
// interface (like FindLP10), collecting responders until timeout, returning
// the LP10 (CAST_MODEL — other Arylic units share the firmware prefix and the
// platform, and ssh-ing into an amp with the LP10's password is the one thing
// this must never do) whose DeviceName best matches nameHint, else the first
// LP10. An exact hinted match returns early; otherwise the window runs out so
// a slower named device isn't beaten by a faster wrong one.
func FindLP10LSSDP(nameHint string, timeout time.Duration) (Device, bool) {
	raddr, err := net.ResolveUDPAddr("udp4", lssdpMulticast)
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
	for _, c := range conns {
		_, _ = c.WriteToUDP([]byte(msearch), raddr)
	}
	packets := make(chan []byte, 64)
	done := make(chan struct{})
	spawnReadersFrom(conns, packets, done)

	var found []LSSDPInfo
	overall := time.NewTimer(timeout)
	defer overall.Stop()
	for {
		select {
		case p := <-packets:
			info, ok := parseTagged(p)
			if !ok {
				continue
			}
			found = append(found, info)
			if nameHint != "" && isLP10(info) && hintExact(info.Name, nameHint) {
				close(done)
				return lssdpDevice(info), true
			}
		case <-overall.C:
			close(done)
			return pickLSSDP(found, nameHint)
		}
	}
}

// parseTagged splits a reader packet (sender IP prefixed by spawnReadersFrom)
// into its LSSDPInfo.
func parseTagged(p []byte) (LSSDPInfo, bool) {
	if len(p) < 4 {
		return LSSDPInfo{}, false
	}
	info, ok := parseLSSDP(p[4:])
	if !ok {
		return LSSDPInfo{}, false
	}
	info.IP = net.IPv4(p[0], p[1], p[2], p[3])
	return info, true
}

// spawnReadersFrom is spawnReaders with the sender's IPv4 prefixed to each
// packet (4 bytes), since an LSSDP reply carries no address of its own.
func spawnReadersFrom(conns []*net.UDPConn, packets chan<- []byte, done <-chan struct{}) {
	for _, c := range conns {
		go func(c *net.UDPConn) {
			buf := make([]byte, 2048)
			for {
				n, from, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				ip4 := from.IP.To4()
				if ip4 == nil {
					continue
				}
				p := make([]byte, 0, 4+n)
				p = append(p, ip4...)
				p = append(p, buf[:n]...)
				select {
				case packets <- p:
				case <-done:
					return
				}
			}
		}(c)
	}
}

// isLP10 reports whether a responder is the LP10 itself, by its declared model.
func isLP10(info LSSDPInfo) bool {
	return strings.EqualFold(strings.TrimSpace(info.Model), "LP10")
}

func pickLSSDP(found []LSSDPInfo, hint string) (Device, bool) {
	var lp []LSSDPInfo
	for _, f := range found {
		if isLP10(f) {
			lp = append(lp, f)
		}
	}
	if len(lp) == 0 {
		return Device{}, false
	}
	if hint != "" {
		if i := bestHinted(len(lp), func(i int) string { return lp[i].Name }, hint); i >= 0 {
			return lssdpDevice(lp[i]), true
		}
	}
	return lssdpDevice(lp[0]), true
}

func lssdpDevice(info LSSDPInfo) Device {
	return Device{Name: info.Name, Model: "LP10", IP: info.IP}
}
