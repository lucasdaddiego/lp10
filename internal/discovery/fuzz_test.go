// Hostile-input fuzzing for the raw mDNS message parser: replies arrive as
// unauthenticated UDP from anything on the LAN (or a spoofed responder), so
// parsePacket must never panic and must keep its output bounded by the
// arithmetic the wire format allows. The parsed records are then run through
// the collector/selection path with the same no-panic expectation.

package discovery

import (
	"testing"
)

func fuzzSeedPacket() []byte {
	b := newPkt(4)
	inst := "AABBCCDDEEFF@Living." + service
	b.addPTR(service, inst)
	b.addSRV(inst, 7000, "Living.local")
	b.addTXT(inst, "am=LP10", "tp=UDP")
	b.addA("Living.local", "192.168.1.13")
	return b.buf
}

func FuzzParsePacket(f *testing.F) {
	f.Add(fuzzSeedPacket())
	f.Add(buildQuery(service, typePTR))
	// self-referential compression pointer at the first answer name
	f.Add(append(append([]byte(nil), 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0), 0xC0, 12, 0, 12, 0, 1, 0, 0, 0, 0, 0, 0))
	f.Add([]byte{0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) // huge counts, no data
	f.Fuzz(func(t *testing.T, data []byte) {
		recs, ok := parsePacket(data)
		if !ok {
			if recs != nil {
				t.Fatal("failed parse still returned records")
			}
			return
		}
		// each record consumes at least 11 wire bytes (1-byte root name + 10-byte
		// fixed header), so the record count is bounded by the packet size
		if len(recs) > len(data)/11+1 {
			t.Fatalf("%d records from a %d-byte packet", len(recs), len(data))
		}
		for _, r := range recs {
			// a decompressed name is bounded by the 64-jump cap: each jump can
			// traverse at most the whole message once
			if len(r.name) > 64*len(data) {
				t.Fatalf("name blew past the jump-cap bound: %d bytes from %d", len(r.name), len(data))
			}
			if len(r.target) > 64*len(data) {
				t.Fatalf("target blew past the jump-cap bound: %d bytes", len(r.target))
			}
			if r.typ == typeA && r.ip != nil && r.ip.To4() == nil {
				t.Fatalf("A record produced a non-IPv4 address: %v", r.ip)
			}
			var txtTotal int
			for _, s := range r.txt {
				txtTotal += len(s)
			}
			if txtTotal > len(data) {
				t.Fatalf("TXT strings exceed packet size: %d > %d", txtTotal, len(data))
			}
		}
		// the collector and selection must digest whatever parsed without panicking
		col := newCollector()
		col.add(recs)
		ds := col.devices()
		_, _ = pickLP10(ds, "Living")
		_, _ = pickLP10(ds, "")
		for _, d := range ds {
			_ = d.Addr()
		}
	})
}
