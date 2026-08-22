package discovery

import (
	"net"
	"testing"
	"time"
)

const liveReply = "HTTP/1.1 200 OK\r\nPORT:7777\r\nTCPPORT:2020\r\nFWVERSION:AR241CE_9243.16.2\r\nDeviceName:Living\r\nState:S\r\nNETMODE:ETH0\r\nSPEAKERTYPE:0\r\nSOURCE_LIST:LS8::01000030\r\nWIFIBAND:ETH\r\nMRAMode:DDMS\r\nUSN:02:e0:3c:10:07:e0\r\n\r\n"

func TestParseLSSDP(t *testing.T) {
	info, ok := parseLSSDP([]byte(liveReply))
	if !ok || info.Name != "Living" || info.FW != "AR241CE_9243.16.2" || info.State != "S" ||
		info.NetMode != "ETH0" || info.Sources != "LS8::01000030" || info.USN != "02:e0:3c:10:07:e0" {
		t.Errorf("parsed %+v ok=%v", info, ok)
	}
	if _, ok := parseLSSDP([]byte("NOTIFY * HTTP/1.1\r\nNT:upnp:rootdevice\r\n")); ok {
		t.Error("a NOTIFY is not an answer")
	}
	if _, ok := parseLSSDP([]byte("HTTP/1.1 200 OK\r\n\r\n")); ok {
		t.Error("an answer with no known fields is rejected")
	}
	if _, ok := parseLSSDP(nil); ok {
		t.Error("empty input")
	}
	long, _ := parseLSSDP([]byte("HTTP/1.1 200 OK\r\ndevicename: " + string(make([]byte, 300)) + "\r\n"))
	if len(long.Name) > maxLSSDPField {
		t.Errorf("field not bounded: %d", len(long.Name))
	}
	if !isArylic(info) || isArylic(LSSDPInfo{FW: "XY1", Sources: "other"}) {
		t.Error("isArylic should key off the AR firmware prefix / LS8 platform")
	}
}

func TestProbeLSSDPAgainstLoopback(t *testing.T) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	defer c.Close()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n > 0 {
				c.WriteToUDP([]byte(liveReply), from)
			}
		}
	}()
	info, ok := ProbeLSSDP(c.LocalAddr().String(), 2*time.Second)
	if !ok || info.Name != "Living" || !info.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("probe = %+v ok=%v", info, ok)
	}
	// an unanswered probe times out false (a port nothing listens on)
	dead, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	addr := dead.LocalAddr().String()
	dead.Close()
	if _, ok := ProbeLSSDP(addr, 300*time.Millisecond); ok {
		t.Error("a dead port must not answer")
	}
	if _, ok := ProbeLSSDP("not a host", 100*time.Millisecond); ok {
		t.Error("an unresolvable host must not answer")
	}
}

func TestPickLSSDP(t *testing.T) {
	a := LSSDPInfo{Name: "Den", FW: "AR241CE_1", IP: net.IPv4(10, 0, 0, 1)}
	b := LSSDPInfo{Name: "Living", FW: "AR241CE_2", IP: net.IPv4(10, 0, 0, 2)}
	other := LSSDPInfo{Name: "TV", FW: "1.0", Sources: "x"}
	if d, ok := pickLSSDP([]LSSDPInfo{other, a, b}, "liv"); !ok || d.Name != "Living" || d.Model != "LP10" {
		t.Errorf("hinted pick = %+v ok=%v", d, ok)
	}
	if d, ok := pickLSSDP([]LSSDPInfo{other, a, b}, ""); !ok || d.Name != "Den" {
		t.Errorf("unhinted pick = %+v ok=%v", d, ok)
	}
	if d, ok := pickLSSDP([]LSSDPInfo{other, a}, "kitchen"); !ok || d.Name != "Den" {
		t.Errorf("unmatched hint falls back to the first Arylic: %+v ok=%v", d, ok)
	}
	if _, ok := pickLSSDP([]LSSDPInfo{other}, ""); ok {
		t.Error("a non-Arylic responder is never picked")
	}
	if _, ok := parseTagged([]byte{1, 2}); ok {
		t.Error("a short tagged packet is rejected")
	}
	if info, ok := parseTagged(append([]byte{10, 0, 0, 9}, []byte(liveReply)...)); !ok || !info.IP.Equal(net.IPv4(10, 0, 0, 9)) {
		t.Errorf("tagged packet = %+v ok=%v", info, ok)
	}
}

// FindLP10LSSDP on a LAN with nothing answering returns false within the window.
func TestFindLP10LSSDPTimesOut(t *testing.T) {
	start := time.Now()
	if _, ok := FindLP10LSSDP("nothing-here-zz", 200*time.Millisecond); ok {
		t.Skip("an LSSDP device answered on this LAN")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("took too long to give up")
	}
}
