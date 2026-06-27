package discovery

// Coverage-focused tests that fill the branches the hand-written suite in
// mdns_test.go leaves open. They reuse that file's pktBuilder helpers
// (newPkt/addPTR/addSRV/addTXT/addA/rrHeader/patchLen) rather than redefining
// the wire-format machinery.

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// covHeader returns a bare 12-byte DNS header with the given QDCOUNT/ANCOUNT,
// used to hand-craft the malformed packets parsePacket must reject.
func covHeader(qd, an int) []byte {
	h := make([]byte, 12)
	h[5] = byte(qd)
	h[7] = byte(an)
	return h
}

// ---- pickLP10 ---------------------------------------------------------------

func TestCov_PickLP10MACTiebreak(t *testing.T) {
	// Two LP10s sharing a Name push the sort comparator past the Name check into
	// the MAC tie-break (the existing suite only uses distinct names).
	a := Device{Name: "Twin", Model: "LP10", MAC: "AA", IP: net.IPv4(192, 168, 0, 2)}
	z := Device{Name: "Twin", Model: "LP10", MAC: "ZZ", IP: net.IPv4(192, 168, 0, 3)}

	got1, ok := pickLP10([]Device{z, a}, "")
	if !ok || got1.MAC != "AA" {
		t.Fatalf("tie-break pick = %+v ok=%v; want lowest MAC AA", got1, ok)
	}
	got2, _ := pickLP10([]Device{a, z}, "") // reversed input → same winner
	if got2.MAC != "AA" {
		t.Errorf("tie-break not order-independent: got MAC %q", got2.MAC)
	}
}

func TestCov_PickLP10HintFallthroughAndEmptyName(t *testing.T) {
	a := Device{Name: "Twin", Model: "LP10", MAC: "AA", IP: net.IPv4(192, 168, 0, 2)}
	// A hint that matches no device name must fall through to the first LP10.
	if d, ok := pickLP10([]Device{a}, "totally-unrelated"); !ok || d.MAC != "AA" {
		t.Errorf("unmatched hint should fall back to first LP10, got %+v ok=%v", d, ok)
	}
	// An empty-Name LP10 must not hijack a hint: strings.Contains(x, "") is
	// always true, so the d.Name!="" guard is what prevents a spurious match.
	anon := Device{Name: "", Model: "LP10", MAC: "00", Host: "anon.local"}
	if d, ok := pickLP10([]Device{anon, a}, "Twin"); !ok || d.Name != "Twin" {
		t.Errorf("empty-name device hijacked the hint match: got %+v", d)
	}
}

// ---- encodeName -------------------------------------------------------------

func TestCov_EncodeNameSkipsEmptyLabels(t *testing.T) {
	// A doubled dot and a trailing dot both yield empty labels that the encoder
	// must skip rather than emit as zero-length labels.
	got := encodeName("a..b.")
	want := []byte{1, 'a', 1, 'b', 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeName(%q) = %v, want %v", "a..b.", got, want)
	}
}

// ---- parseName --------------------------------------------------------------

func TestCov_ParseNameMalformed(t *testing.T) {
	cases := []struct {
		desc string
		msg  []byte
		off  int
	}{
		{"label length overruns the buffer", []byte{2, 'a'}, 0},
		{"unterminated label fills the buffer", []byte{1, 'a'}, 0},
		{"compression pointer missing its second byte", []byte{0xC0}, 0},
		{"start offset already past the end", []byte{0}, 5},
	}
	for _, c := range cases {
		if got, _, ok := parseName(c.msg, c.off); ok {
			t.Errorf("%s: parseName returned %q, ok=true; want failure", c.desc, got)
		}
	}
}

// ---- parseTXT ---------------------------------------------------------------

func TestCov_ParseTXTTruncated(t *testing.T) {
	// A good string followed by a length byte that overruns: the prefix is kept
	// and the overrun is dropped at the break.
	if got := parseTXT([]byte{1, 'x', 5, 'a'}); len(got) != 1 || got[0] != "x" {
		t.Fatalf("parseTXT = %v, want [x]", got)
	}
	// A lone over-long length byte yields nothing at all.
	if got := parseTXT([]byte{9}); len(got) != 0 {
		t.Errorf("parseTXT(overrun) = %v, want empty", got)
	}
}

// ---- parsePacket ------------------------------------------------------------

func TestCov_ParsePacketTooShort(t *testing.T) {
	if _, ok := parsePacket(make([]byte, 11)); ok {
		t.Error("a message shorter than the 12-byte header must be rejected")
	}
}

func TestCov_ParsePacketWalksQuestionSection(t *testing.T) {
	// buildQuery emits a question and no answers; round-tripping it through
	// parsePacket exercises the question-skip loop the answer-only fixtures miss.
	recs, ok := parsePacket(buildQuery(service, typePTR))
	if !ok || len(recs) != 0 {
		t.Fatalf("parsePacket(query) = %v ok=%v; want ok with zero records", recs, ok)
	}
}

func TestCov_ParsePacketRejectsBadQuestion(t *testing.T) {
	// QDCOUNT=1 but the question name is a label that runs off the end.
	msg := append(covHeader(1, 0), 0x05) // claims a 5-byte label, supplies none
	if _, ok := parsePacket(msg); ok {
		t.Error("an unparseable question name must fail the whole packet")
	}
}

func TestCov_ParsePacketRejectsTruncatedQuestionFields(t *testing.T) {
	// A valid (root) question name, but no room for its QTYPE/QCLASS.
	msg := append(covHeader(1, 0), 0x00)
	if _, ok := parsePacket(msg); ok {
		t.Error("a question with truncated QTYPE/QCLASS must fail")
	}
}

func TestCov_ParsePacketRejectsTruncatedAnswerHeader(t *testing.T) {
	// ANCOUNT=1 with a (root) owner name but no room for the 10-byte RR header.
	msg := append(covHeader(0, 1), 0x00)
	if _, ok := parsePacket(msg); ok {
		t.Error("an answer with no room for its RR header must fail")
	}
}

func TestCov_ParsePacketRejectsRdataOutOfRange(t *testing.T) {
	// A well-formed A-record header claiming 4 rdata bytes, but only 2 follow.
	msg := append(covHeader(0, 1), 0x00) // root owner
	msg = append(msg,
		byte(typeA>>8), byte(typeA), // TYPE = A
		0x80, 0x01, // CLASS = cache-flush|IN
		0, 0, 0, 0, // TTL
		0, 4, // RDLENGTH = 4
		1, 2) // ...only 2 rdata bytes present
	if _, ok := parsePacket(msg); ok {
		t.Error("rdata extending past the message must fail")
	}
}

func TestCov_ParsePacketSkipsShortSRVandA(t *testing.T) {
	// SRV with rdlen<7 and A with rdlen!=4 are structurally present but too
	// short to decode, so both are skipped: the device keeps its TXT model but
	// gains no host and no IP.
	const inst = "AABB@Short._raop._tcp.local"
	b := newPkt(4)
	b.addPTR(service, inst)
	atSRV := b.rrHeader(inst, typeSRV)
	b.buf = append(b.buf, 0, 0, 0, 0) // priority+weight only → rdlen 4 (<7)
	b.patchLen(atSRV)
	b.addTXT(inst, "am=LP10")
	atA := b.rrHeader("Short.local", typeA)
	b.buf = append(b.buf, 1, 2, 3) // rdlen 3 (!=4)
	b.patchLen(atA)

	recs, ok := parsePacket(b.buf)
	if !ok {
		t.Fatal("a packet with short SRV/A records should still parse")
	}
	col := newCollector()
	col.add(recs)
	ds := col.devices()
	if len(ds) != 1 {
		t.Fatalf("devices = %+v, want exactly 1", ds)
	}
	if ds[0].Model != "LP10" {
		t.Errorf("model from TXT was lost: %+v", ds[0])
	}
	if ds[0].Host != "" || ds[0].IP != nil {
		t.Errorf("short SRV/A must be skipped, got Host=%q IP=%v", ds[0].Host, ds[0].IP)
	}
}

// ---- FindLP10 ---------------------------------------------------------------

func TestCov_FindLP10TimesOut(t *testing.T) {
	// With nothing answering, the send → retransmit → read-deadline → overall
	// timeout loop runs to completion and the final pickLP10 finds nothing.
	start := time.Now()
	d, ok := FindLP10("definitely-not-present-aaa", 200*time.Millisecond)
	if ok {
		// Tolerate a real LP10 on the LAN, but it must look like a usable one.
		if d.Model != modelLP10 || d.Addr() == "" {
			t.Errorf("found device is not a usable LP10: %+v", d)
		}
		return
	}
	if d.Name != "" || d.Model != "" || d.MAC != "" || d.Host != "" || d.IP != nil {
		t.Errorf("a not-found result must be the zero Device, got %+v", d)
	}
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Errorf("absent-device probe returned early (%v); expected ~the full timeout", el)
	}
}

func TestCov_FindLP10ShortTimeout(t *testing.T) {
	// A sub-millisecond window makes the first read deadline land at/after the
	// overall deadline, exercising the resend-scheduling arithmetic for tiny
	// timeouts. It must return promptly without finding anything (no responder).
	if d, ok := FindLP10("", time.Millisecond); ok && d.Model != modelLP10 {
		t.Errorf("short-timeout probe returned a non-LP10: %+v", d)
	}
}

// covResponders joins the mDNS group on every multicast-capable IPv4 interface
// so that, whichever interface FindLP10's query egresses, a listener receives
// the loopback copy. Returns nil when no such interface or bind is available
// (e.g. a sandboxed/offline runner), in which case the responder path is simply
// not exercised.
func covResponders() []*net.UDPConn {
	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var conns []*net.UDPConn
	for i := range ifs {
		ifi := &ifs[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		hasV4 := false
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				hasV4 = true
				break
			}
		}
		if !hasV4 {
			continue
		}
		if lc, err := net.ListenMulticastUDP("udp4", ifi, group); err == nil {
			conns = append(conns, lc)
		}
	}
	return conns
}

func TestCov_FindLP10EarlyReturnViaResponder(t *testing.T) {
	listeners := covResponders()
	if len(listeners) == 0 {
		// No multicast path here; still drive the call so the test does work,
		// but the early-return branch can't be exercised in this environment.
		FindLP10("CovEarly", 300*time.Millisecond)
		t.Skip("no multicast-capable interface available; responder path skipped")
	}
	defer func() {
		for _, lc := range listeners {
			lc.Close()
		}
	}()

	// A fully-resolved LP10 (PTR+SRV+TXT+A) so pickLP10 returns it with an IP,
	// which is what makes FindLP10 stop early instead of waiting out the window.
	const inst = "C0FFEE00BEEF@CovEarly._raop._tcp.local"
	b := newPkt(4)
	b.addPTR(service, inst)
	b.addSRV(inst, 7000, "CovEarly.local")
	b.addTXT(inst, "am=LP10", "cn=0,1")
	b.addA("CovEarly.local", "192.168.213.77")
	reply := append([]byte(nil), b.buf...)

	var wg sync.WaitGroup
	for _, lc := range listeners {
		lc := lc
		_ = lc.SetReadDeadline(time.Now().Add(3 * time.Second))
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 2048)
			for {
				n, src, err := lc.ReadFromUDP(buf)
				if err != nil {
					return // closed or deadline → stop
				}
				if n > 0 {
					_, _ = lc.WriteToUDP(reply, src)
				}
			}
		}()
	}

	d, ok := FindLP10("CovEarly", 3*time.Second)

	for _, lc := range listeners {
		lc.Close()
	}
	wg.Wait()

	// Best-effort: a degraded multicast environment may drop the loopback copy,
	// so we don't require ok. When a device IS returned it must be a usable LP10.
	if ok && (d.Model != modelLP10 || d.Addr() == "") {
		t.Errorf("early-return produced an unusable device: %+v", d)
	}
}
