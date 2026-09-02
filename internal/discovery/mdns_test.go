package discovery

import (
	"bytes"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// pktBuilder assembles a DNS message with name compression (repeated suffixes
// become 0xC0 pointers), so the round-trip test exercises the same decompression
// the real device's responder relies on.
type pktBuilder struct {
	buf  []byte
	offs map[string]int
}

func newPkt(ancount int) *pktBuilder {
	b := &pktBuilder{offs: map[string]int{}}
	b.buf = make([]byte, 12)
	b.buf[6], b.buf[7] = byte(ancount>>8), byte(ancount) // ANCOUNT
	return b
}

func (b *pktBuilder) name(n string) {
	n = strings.TrimSuffix(n, ".")
	for n != "" {
		if off, ok := b.offs[n]; ok {
			b.buf = append(b.buf, 0xC0|byte(off>>8), byte(off))
			return
		}
		if len(b.buf) < 0x3FFF {
			b.offs[n] = len(b.buf)
		}
		lbl := n
		if i := strings.IndexByte(n, '.'); i >= 0 {
			lbl, n = n[:i], n[i+1:]
		} else {
			n = ""
		}
		b.buf = append(b.buf, byte(len(lbl)))
		b.buf = append(b.buf, lbl...)
	}
	b.buf = append(b.buf, 0)
}

// rrHeader writes owner/type/class/ttl and a placeholder rdlength, returning the
// index to patch once the rdata is appended.
func (b *pktBuilder) rrHeader(owner string, typ uint16) int {
	b.name(owner)
	b.buf = append(b.buf, byte(typ>>8), byte(typ), 0x80, 0x01, 0, 0, 0, 0) // type, class(cache-flush|IN), ttl=0
	at := len(b.buf)
	b.buf = append(b.buf, 0, 0) // rdlength placeholder
	return at
}

func (b *pktBuilder) patchLen(at int) {
	n := len(b.buf) - at - 2
	b.buf[at], b.buf[at+1] = byte(n>>8), byte(n)
}

func (b *pktBuilder) addPTR(owner, target string) {
	at := b.rrHeader(owner, typePTR)
	b.name(target)
	b.patchLen(at)
}

func (b *pktBuilder) addSRV(owner string, port uint16, target string) {
	at := b.rrHeader(owner, typeSRV)
	b.buf = append(b.buf, 0, 0, 0, 0, byte(port>>8), byte(port)) // priority, weight, port
	b.name(target)
	b.patchLen(at)
}

func (b *pktBuilder) addTXT(owner string, kvs ...string) {
	at := b.rrHeader(owner, typeTXT)
	for _, kv := range kvs {
		b.buf = append(b.buf, byte(len(kv)))
		b.buf = append(b.buf, kv...)
	}
	b.patchLen(at)
}

func (b *pktBuilder) addA(owner string, ip string) {
	at := b.rrHeader(owner, typeA)
	b.buf = append(b.buf, net.ParseIP(ip).To4()...)
	b.patchLen(at)
}

func TestParseNameCompression(t *testing.T) {
	// "local" at offset 12, then "Living" + pointer-to-local at 18.
	msg := make([]byte, 12)
	msg = append(msg, 5, 'l', 'o', 'c', 'a', 'l', 0) // local. at 12
	start := len(msg)
	msg = append(msg, 6, 'L', 'i', 'v', 'i', 'n', 'g', 0xC0, 12) // Living + ->local
	got, next, ok := parseName(msg, start)
	if !ok || got != "Living.local" {
		t.Fatalf("parseName = %q, %v; want Living.local", got, ok)
	}
	if next != start+9 { // 1+6 label bytes + 2 pointer bytes
		t.Errorf("next = %d, want %d", next, start+9)
	}
	// a pointer that loops must be rejected, not hang
	loop := []byte{0xC0, 0x00}
	if _, _, ok := parseName(loop, 0); ok {
		t.Error("self-referential pointer should fail")
	}
	// Reserved 01xxxxxx/10xxxxxx label forms must not be accepted as a
	// 64/128-byte literal label.
	reserved := append([]byte{0x40}, bytes.Repeat([]byte{'a'}, 64)...)
	reserved = append(reserved, 0)
	if _, _, ok := parseName(reserved, 0); ok {
		t.Error("reserved DNS label form should fail")
	}
}

func TestParsePacketAndDevices(t *testing.T) {
	const inst = "AABBCCDDEEFF@Living._raop._tcp.local"
	b := newPkt(4)
	b.addPTR(service, inst)
	b.addSRV(inst, 7000, "Living.local")
	b.addTXT(inst, "cn=0,1", "am=LP10", "fv=p20.AR241CE_8530.23")
	b.addA("Living.local", "192.168.1.40")

	recs, ok := parsePacket(b.buf)
	if !ok {
		t.Fatal("parsePacket failed on a well-formed message")
	}
	col := newCollector()
	col.add(recs)
	ds := col.devices()
	if len(ds) != 1 {
		t.Fatalf("devices = %+v, want 1", ds)
	}
	d := ds[0]
	if d.Name != "Living" || d.Model != "LP10" || d.MAC != "AABBCCDDEEFF" ||
		d.Host != "Living.local" || d.Addr() != "192.168.1.40" {
		t.Errorf("device = %+v", d)
	}
}

func TestCollectorJoinsDNSNamesCaseInsensitively(t *testing.T) {
	const inst = "AABBCCDDEEFF@Living._raop._tcp.local."
	c := newCollector()
	c.add([]rr{
		{name: strings.ToUpper(service), typ: typePTR, target: inst},
		{name: strings.ToLower(inst), typ: typeSRV, target: "Living.LOCAL."},
		{name: strings.ToUpper(inst), typ: typeTXT, txt: []string{"am=LP10"}},
		{name: "living.local", typ: typeA, ip: net.IPv4(192, 168, 1, 40)},
	})
	ds := c.devices()
	if len(ds) != 1 {
		t.Fatalf("devices = %+v, want one case-insensitively joined device", ds)
	}
	d := ds[0]
	if d.Name != "Living" || d.Model != "LP10" || d.Host != "Living.LOCAL." || d.Addr() != "192.168.1.40" {
		t.Errorf("device = %+v", d)
	}
}

func TestCollectorIgnoresMalformedABeforeValidAddress(t *testing.T) {
	const inst = "AABB@Living._raop._tcp.local"
	c := newCollector()
	c.add([]rr{
		{name: service, typ: typePTR, target: inst},
		{name: inst, typ: typeSRV, target: "Living.local"},
		{name: inst, typ: typeTXT, txt: []string{"am=LP10"}},
		{name: "Living.local", typ: typeA}, // malformed A must not poison the first slot
		{name: "Living.local", typ: typeA, ip: net.IPv4(10, 0, 0, 9)},
	})
	ds := c.devices()
	if len(ds) != 1 || ds[0].Addr() != "10.0.0.9" {
		t.Errorf("devices = %+v, want valid A address after malformed record", ds)
	}
}

func TestParsePacketRejectsTruncated(t *testing.T) {
	b := newPkt(1)
	b.addSRV("x.local", 7000, "Living.local")
	for _, n := range []int{4, 8, len(b.buf) - 1} { // truncations must not panic
		if _, ok := parsePacket(b.buf[:n]); ok {
			t.Errorf("parsePacket(len %d) reported ok on truncated input", n)
		}
	}
}

func TestPickLP10(t *testing.T) {
	living := Device{Name: "Living", Model: "LP10", MAC: "AA", IP: net.IPv4(192, 168, 0, 40)}
	kitchen := Device{Name: "Kitchen", Model: "LP10", MAC: "BB", IP: net.IPv4(192, 168, 0, 41)}
	other := Device{Name: "Living Room", Model: "A31", MAC: "CC", IP: net.IPv4(192, 168, 0, 42)}

	if _, ok := pickLP10([]Device{other}, ""); ok {
		t.Error("a non-LP10 model must not be picked")
	}
	// name hint disambiguates among several LP10s
	if d, ok := pickLP10([]Device{kitchen, living, other}, "LP10 · Living"); !ok || d.Name != "Living" {
		t.Errorf("hinted pick = %+v, %v; want Living", d, ok)
	}
	// no hint → deterministic (alphabetical) pick, regardless of input order
	d1, _ := pickLP10([]Device{kitchen, living}, "")
	d2, _ := pickLP10([]Device{living, kitchen}, "")
	if d1.Name != "Kitchen" || d2.Name != "Kitchen" {
		t.Errorf("unhinted picks = %q / %q, want deterministic Kitchen", d1.Name, d2.Name)
	}
	// a host-only candidate (no A record yet) is still usable via its .local name
	hostOnly := Device{Name: "Living", Model: "LP10", Host: "Living.local"}
	if d, ok := pickLP10([]Device{hostOnly}, ""); !ok || d.Addr() != "Living.local" {
		t.Errorf("host-only pick = %+v, %v", d, ok)
	}
}

// A hinted search must not early-exit on the first complete LP10 that answers
// when that device is not the hinted one: with several LP10s on the LAN, the
// named device may answer later and must still win. Regression for the
// faster-wrong-device race — falling back to the first device is only for the
// timeout path, when the named device never answered at all. Best-effort like
// the other responder tests: a degraded multicast environment skips, not fails.
func TestFindLP10HintedWaitsForNamedDevice(t *testing.T) {
	listeners := covResponders()
	if len(listeners) == 0 {
		t.Skip("no multicast-capable interface available; responder path skipped")
	}
	defer func() {
		for _, lc := range listeners {
			lc.Close()
		}
	}()

	mk := func(mac, name, ip string) []byte {
		inst := mac + "@" + name + "." + service
		b := newPkt(4)
		b.addPTR(service, inst)
		b.addSRV(inst, 7000, name+".local")
		b.addTXT(inst, "am=LP10", "cn=0,1")
		b.addA(name+".local", ip)
		return append([]byte(nil), b.buf...)
	}
	// The wrong device answers every query instantly; the hinted one ~120ms
	// later — comfortably inside the window, so only a premature early exit
	// (the regression) can pick CovWrong before the timeout.
	wrong := mk("C0FFEE00AAAA", "CovWrong", "192.168.213.88")
	target := mk("C0FFEE00BBBB", "CovTarget", "192.168.213.89")

	var wg sync.WaitGroup
	for _, lc := range listeners {
		_ = lc.SetReadDeadline(time.Now().Add(5 * time.Second))
		wg.Go(func() {
			buf := make([]byte, 2048)
			for {
				n, src, err := lc.ReadFromUDP(buf)
				if err != nil {
					return // closed or deadline → stop
				}
				if n > 0 {
					_, _ = lc.WriteToUDP(wrong, src)
					time.AfterFunc(120*time.Millisecond, func() { _, _ = lc.WriteToUDP(target, src) })
				}
			}
		})
	}

	start := time.Now()
	d, ok := FindLP10("CovTarget", 3*time.Second)
	elapsed := time.Since(start)

	for _, lc := range listeners {
		lc.Close()
	}
	wg.Wait()

	if !ok {
		t.Skip("no reply made it back (degraded multicast); the race is not exercisable here")
	}
	// The regression assert: an early exit — well before the timeout — may only
	// be the hinted device. At the timeout the fallback legitimately picks over
	// whatever arrived (CovWrong included, or a real LP10 on the LAN).
	if elapsed < 2*time.Second && d.Name != "CovTarget" {
		t.Fatalf("hinted search early-exited on %q after %v; only the named device may short-circuit", d.Name, elapsed)
	}
	if d.Name == "CovTarget" && len(d.IP) == 0 {
		t.Errorf("named device returned without its IP: %+v", d)
	}
}

func TestBuildQueryShape(t *testing.T) {
	q := buildQuery(service, typePTR)
	if be16(q, 4) != 1 { // QDCOUNT
		t.Errorf("QDCOUNT = %d, want 1", be16(q, 4))
	}
	// trailing question: QTYPE=PTR, QCLASS = QU|IN
	if be16(q, len(q)-4) != typePTR {
		t.Errorf("qtype = %d, want PTR", be16(q, len(q)-4))
	}
	if be16(q, len(q)-2) != classQU|1 {
		t.Errorf("qclass = %#x, want QU|IN", be16(q, len(q)-2))
	}
}
