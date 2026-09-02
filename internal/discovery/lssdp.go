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
	"net"
	"strings"
	"time"
)

// LSSDPPort is the device's LSSDP responder port (UDP).
const LSSDPPort = 1800

const lssdpMulticast = "239.255.255.250:1800"

// msearch is the discovery datagram; the device answers any ST.
const msearch = "M-SEARCH * HTTP/1.1\r\nHOST:239.255.255.250:1800\r\nMAN:\"ssdp:discover\"\r\nMX:1\r\nST:ssdp:all\r\n\r\n"

// LSSDPInfo is one responder's header list, trimmed to the fields lp10 uses.
// Every value is attacker-controllable LAN input: callers that render them
// must control-strip (protocol.Printable) first.
type LSSDPInfo struct {
	Name    string // DeviceName
	FW      string // FWVERSION, e.g. AR241CE_8530.23.2
	State   string // State, e.g. S
	NetMode string // NETMODE, e.g. ETH0 / WLAN0
	Sources string // SOURCE_LIST, e.g. LS8::01000030 (carries the platform)
	USN     string // USN (the wlan MAC on this firmware)
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
		case "FWVERSION":
			info.FW = v
		case "STATE":
			info.State = v
		case "NETMODE":
			info.NetMode = v
		case "SOURCE_LIST":
			info.Sources = v
		case "USN":
			info.USN = v
		default:
			continue
		}
		seen++
	}
	return info, seen > 0
}

// ProbeLSSDP sends one unicast M-SEARCH to host (a bare host uses the LSSDP
// port; an explicit host:port is honoured, for tests) and waits up to timeout
// for its reply. One datagram each way; false on no/garbage answer.
func ProbeLSSDP(host string, timeout time.Duration) (LSSDPInfo, bool) {
	target := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		target = net.JoinHostPort(host, "1800")
	}
	raddr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return LSSDPInfo{}, false
	}
	c, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return LSSDPInfo{}, false
	}
	defer c.Close()
	if _, err := c.Write([]byte(msearch)); err != nil {
		return LSSDPInfo{}, false
	}
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		return LSSDPInfo{}, false
	}
	info, ok := parseLSSDP(buf[:n])
	info.IP = raddr.IP
	return info, ok
}

// FindLP10LSSDP is the mDNS fallback: a multicast M-SEARCH out every
// interface (like FindLP10), collecting responders until timeout, returning
// the one whose DeviceName matches nameHint, else the first Arylic-looking
// responder (an AR… firmware version or the LS8 platform in SOURCE_LIST). A
// hinted match returns early; otherwise the window runs out so a slower
// named device isn't beaten by a faster wrong one.
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
			if nameHint != "" && lssdpHintMatches(info, nameHint) {
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

func lssdpHintMatches(info LSSDPInfo, hint string) bool {
	return strings.Contains(strings.ToLower(info.Name), strings.ToLower(hint))
}

// isArylic reports whether a responder looks like this device family.
func isArylic(info LSSDPInfo) bool {
	return strings.HasPrefix(info.FW, "AR") || strings.Contains(info.Sources, "LS8")
}

func pickLSSDP(found []LSSDPInfo, hint string) (Device, bool) {
	if hint != "" {
		for _, f := range found {
			if lssdpHintMatches(f, hint) {
				return lssdpDevice(f), true
			}
		}
	}
	for _, f := range found {
		if isArylic(f) {
			return lssdpDevice(f), true
		}
	}
	return Device{}, false
}

func lssdpDevice(info LSSDPInfo) Device {
	return Device{Name: info.Name, Model: "LP10", IP: info.IP}
}
