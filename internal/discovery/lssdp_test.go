package discovery

import (
	"context"
	"net"
	"testing"
	"time"
)

const liveReply = "HTTP/1.1 200 OK\r\nUSN:d8f710710ad6\r\nHOST:239.255.255.250:1800\r\nVersion:LSSDP 1.0\r\nFN:1\r\nFWVERSION:AR241CE_8530.23.2\r\nCAST_FWVERSION:.\r\nCAST_TIMEZONE:\r\nCAST_MODEL:LP10\r\nPORT:7777\r\nDeviceName:Living\r\nState:S\r\nNETMODE:ETH0\r\nSPEAKERTYPE:Wireless Speaker\r\nTCPPORT:2020\r\nWIFIBAND:ETH\r\nSOURCE_LIST:LS8::01000030\r\nMRAMode:DDMS\r\n\r\n"

func TestParseLSSDP(t *testing.T) {
	info, ok := parseLSSDP([]byte(liveReply))
	if !ok || info.Name != "Living" || info.FW != "AR241CE_8530.23.2" || info.State != "S" ||
		info.NetMode != "ETH0" || info.Model != "LP10" {
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
	if !isLP10(info) || isLP10(LSSDPInfo{FW: "AR241CE_8530.23.2", Model: "A50"}) {
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
	ctx := context.Background()
	info, ok := ProbeLSSDP(ctx, c.LocalAddr().String(), 2*time.Second)
	if !ok || info.Name != "Living" || !info.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("probe = %+v ok=%v", info, ok)
	}
	// a hostname goes through the resolver (localhost: hosts file, no network)
	_, port, _ := net.SplitHostPort(c.LocalAddr().String())
	if info, ok := ProbeLSSDP(ctx, "localhost:"+port, 2*time.Second); !ok || info.Name != "Living" {
		t.Errorf("probe by name = %+v ok=%v", info, ok)
	}
	// an unanswered probe times out false (a port nothing listens on)
	dead, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	addr := dead.LocalAddr().String()
	dead.Close()
	if _, ok := ProbeLSSDP(ctx, addr, 300*time.Millisecond); ok {
		t.Error("a dead port must not answer")
	}
	if _, ok := ProbeLSSDP(ctx, "not a host", 100*time.Millisecond); ok {
		t.Error("an unresolvable host must not answer")
	}
	for _, bad := range []string{"127.0.0.1:x", "127.0.0.1:0", "[::1]:1800"} {
		if _, ok := ProbeLSSDP(ctx, bad, 100*time.Millisecond); ok {
			t.Errorf("%q must not probe", bad)
		}
	}
}

// The probe's wait is bounded by its own budget and by the caller's context:
// a runtime shutdown mid-probe must not sit out the read (nor, for a name, the
// resolver). A silent listener keeps the socket open so the read really blocks.
func TestProbeLSSDPHonoursContextAndBudget(t *testing.T) {
	silent, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	defer silent.Close()
	addr := silent.LocalAddr().String()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, ok := ProbeLSSDP(ctx, addr, 10*time.Second); ok {
		t.Error("a cancelled probe must not report the box alive")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("cancel took %v to unblock the probe", el)
	}

	start = time.Now()
	if _, ok := ProbeLSSDP(context.Background(), addr, 200*time.Millisecond); ok {
		t.Error("a silent box must not report alive")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("budget took %v to unblock the probe", el)
	}
}

func TestPickLSSDP(t *testing.T) {
	a := LSSDPInfo{Name: "Den", Model: "LP10", FW: "AR241CE_1", IP: net.IPv4(10, 0, 0, 1)}
	b := LSSDPInfo{Name: "Living", Model: "LP10", FW: "AR241CE_2", IP: net.IPv4(10, 0, 0, 2)}
	room := LSSDPInfo{Name: "Living Room", Model: "LP10", FW: "AR241CE_2", IP: net.IPv4(10, 0, 0, 3)}
	amp := LSSDPInfo{Name: "Amp", Model: "A50", FW: "AR241CE_3", IP: net.IPv4(10, 0, 0, 4)} // same family, not the box
	other := LSSDPInfo{Name: "TV", FW: "1.0"}
	if d, ok := pickLSSDP([]LSSDPInfo{other, amp, a, b}, "liv"); !ok || d.Name != "Living" || d.Model != "LP10" {
		t.Errorf("hinted pick = %+v ok=%v", d, ok)
	}
	if d, ok := pickLSSDP([]LSSDPInfo{other, amp, a, b}, ""); !ok || d.Name != "Den" {
		t.Errorf("unhinted pick = %+v ok=%v", d, ok)
	}
	if d, ok := pickLSSDP([]LSSDPInfo{other, a}, "kitchen"); !ok || d.Name != "Den" {
		t.Errorf("unmatched hint falls back to the first LP10: %+v ok=%v", d, ok)
	}
	if _, ok := pickLSSDP([]LSSDPInfo{other, amp}, ""); ok {
		t.Error("a responder that is not an LP10 — another Arylic unit included — is never picked")
	}
	// the hint names the longer of two related names: exact beats contained
	if d, ok := pickLSSDP([]LSSDPInfo{b, room}, "Living Room"); !ok || d.Name != "Living Room" {
		t.Errorf("exact name must win over a contained one: %+v", d)
	}
	if d, ok := pickLSSDP([]LSSDPInfo{room, b}, "LP10 · Living"); !ok || d.Name != "Living" {
		t.Errorf("a label carrying the name picks that name: %+v", d)
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
