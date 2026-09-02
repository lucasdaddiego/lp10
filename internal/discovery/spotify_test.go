package discovery

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// spotifyPkt builds one advertiser's full answer: PTR → instance, SRV with the
// port, an A record for the SRV host.
func spotifyPkt(name, host, ip string, port uint16) []byte {
	inst := name + "." + spotifyService
	b := newPkt(3)
	b.addPTR(spotifyService, inst)
	b.addSRV(inst, port, host)
	b.addA(host, ip)
	return append([]byte(nil), b.buf...)
}

func TestSpotifyEndpointsAndPick(t *testing.T) {
	recs, ok := parsePacket(spotifyPkt("Living", "Living.local", "192.168.0.13", 9096))
	if !ok {
		t.Fatal("parsePacket failed")
	}
	more, _ := parsePacket(spotifyPkt("Kitchen", "Kitchen.local", "192.168.0.44", 9095))
	eps := spotifyEndpoints(append(recs, more...))
	if len(eps) != 2 || eps[0].Name != "Kitchen" || eps[1].Name != "Living" || eps[1].Port != 9096 ||
		eps[1].Addr() != "192.168.0.13:9096" || eps[1].Host != "Living.local" {
		t.Fatalf("endpoints = %+v", eps)
	}
	// by resolved IP
	if e, ok := pickSpotify(eps, "whatever", net.ParseIP("192.168.0.13").To4()); !ok || e.Name != "Living" {
		t.Errorf("pick by IP = %+v %v", e, ok)
	}
	// by host name, in its several spellings
	for _, h := range []string{"living.local", "Living", "LIVING.LOCAL."} {
		if e, ok := pickSpotify(eps, h, nil); !ok || e.Name != "Living" {
			t.Errorf("pick by host %q = %+v %v", h, e, ok)
		}
	}
	// two advertisers, no match: ambiguous, so nothing
	if _, ok := pickSpotify(eps, "192.168.0.99", net.ParseIP("192.168.0.99").To4()); ok {
		t.Error("two strangers must not be guessed between")
	}
	// one advertiser, no match: it is the box
	if e, ok := pickSpotify(eps[1:], "192.168.0.99", nil); !ok || e.Name != "Living" {
		t.Errorf("sole advertiser fallback = %+v %v", e, ok)
	}
	if _, ok := pickSpotify(nil, "x", nil); ok {
		t.Error("nothing advertised")
	}
	// an SRV without its PTR, or a PTR without its SRV, is not an endpoint
	orphan := newPkt(1)
	orphan.addSRV("Ghost."+spotifyService, 9096, "Ghost.local")
	orecs, _ := parsePacket(orphan.buf)
	ptrOnly := newPkt(1)
	ptrOnly.addPTR(spotifyService, "Half."+spotifyService)
	precs, _ := parsePacket(ptrOnly.buf)
	if eps := spotifyEndpoints(append(orecs, precs...)); len(eps) != 0 {
		t.Errorf("incomplete advertisers became endpoints: %+v", eps)
	}
	// a host-only endpoint addresses by name
	if (SpotifyEndpoint{Host: "Living.local.", Port: 9096}).Addr() != "Living.local:9096" {
		t.Error("Addr without an IP should use the host")
	}
}

func TestParseSpotifyZC(t *testing.T) {
	const live = `{"version":"2.9.0","libraryVersion":"3.203.239-g1d6bd565","deviceType":"SPEAKER","modelDisplayName":"LP10","brandDisplayName":"Arylic","productID":1,"status":101,"statusString":"OK","spotifyError":0,"activeUser":"","remoteName":"Living","deviceID":"5efc","groupStatus":"NONE"}`
	info, ok := parseSpotifyZC([]byte(live))
	if !ok || info.Status != 101 || info.StatusString != "OK" || info.LibraryVersion != "3.203.239-g1d6bd565" ||
		info.Version != "2.9.0" || info.RemoteName != "Living" || info.DeviceType != "SPEAKER" || info.ActiveUser != "" {
		t.Errorf("live answer parsed as %+v ok=%v", info, ok)
	}
	// control characters are stripped and fields capped; an active user survives
	long := strings.Repeat("x", 200)
	info, ok = parseSpotifyZC([]byte(`{"status":101,"activeUser":"lu\u001b[31mcas","remoteName":"` + long + `"}`))
	if !ok || info.ActiveUser != "lu[31mcas" || len(info.RemoteName) != maxZCField {
		t.Errorf("hardening: %+v ok=%v", info, ok)
	}
	for _, bad := range []string{``, `null`, `[]`, `"str"`, `{}`, `{"foo":1}`, `{"status":"101"}`, `{"status":-5}`, `{"status":1e9}`} {
		if _, ok := parseSpotifyZC([]byte(bad)); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	// a huge numeric status is dropped but a version still makes it an answer
	if info, ok := parseSpotifyZC([]byte(`{"status":1e9,"version":"2.9.0"}`)); !ok || info.Status != 0 {
		t.Errorf("out-of-range status: %+v ok=%v", info, ok)
	}
}

func TestProbeSpotifyZC(t *testing.T) {
	var body string
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zc" || r.URL.Query().Get("action") != "getInfo" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	body = `{"status":101,"statusString":"OK","libraryVersion":"3.203.239-g1d6bd565","activeUser":"someone"}`
	info, ok := ProbeSpotifyZC(addr, 2*time.Second)
	if !ok || info.ActiveUser != "someone" || info.Status != 101 {
		t.Fatalf("probe = %+v ok=%v", info, ok)
	}
	code = http.StatusServiceUnavailable
	if _, ok := ProbeSpotifyZC(addr, 2*time.Second); ok {
		t.Error("a non-200 answer must not count")
	}
	code, body = http.StatusOK, "not json"
	if _, ok := ProbeSpotifyZC(addr, 2*time.Second); ok {
		t.Error("a non-JSON answer must not count")
	}
	// an oversized body is cut at the cap and then fails to parse rather than
	// being buffered whole
	body = `{"status":101,"remoteName":"` + strings.Repeat("y", maxZCBody) + `"}`
	if _, ok := ProbeSpotifyZC(addr, 2*time.Second); ok {
		t.Error("an oversized body must be refused")
	}
	srv.Close()
	if _, ok := ProbeSpotifyZC(addr, 500*time.Millisecond); ok {
		t.Error("a dead endpoint must not count")
	}
	if _, ok := ProbeSpotifyZC("bad host:port:x", 500*time.Millisecond); ok {
		t.Error("an unparseable address must not count")
	}
}

// FindSpotifyZC on the real multicast path: a fake advertiser answers every
// query with two speakers; only the one at the resolved IP may be returned
// early, and it must carry its SRV port. Skips where multicast is unavailable.
func TestFindSpotifyZCViaResponder(t *testing.T) {
	listeners := covResponders()
	if len(listeners) == 0 {
		if _, ok := FindSpotifyZC("nowhere.invalid", nil, 150*time.Millisecond); ok {
			t.Error("found an endpoint with no advertiser")
		}
		t.Skip("no multicast-capable interface available; responder path skipped")
	}
	defer func() {
		for _, lc := range listeners {
			lc.Close()
		}
	}()
	wrong := spotifyPkt("CovWrong", "CovWrong.local", "192.168.213.88", 9095)
	target := spotifyPkt("CovTarget", "CovTarget.local", "192.168.213.89", 9096)
	for _, lc := range listeners {
		_ = lc.SetReadDeadline(time.Now().Add(5 * time.Second))
		go func(lc *net.UDPConn) {
			buf := make([]byte, 2048)
			for {
				n, src, err := lc.ReadFromUDP(buf)
				if err != nil {
					return
				}
				if n > 0 {
					_, _ = lc.WriteToUDP(wrong, src)
					time.AfterFunc(100*time.Millisecond, func() { _, _ = lc.WriteToUDP(target, src) })
				}
			}
		}(lc)
	}
	e, ok := FindSpotifyZC("CovTarget.local", net.ParseIP("192.168.213.89").To4(), 3*time.Second)
	if !ok {
		t.Skip("no reply made it back (degraded multicast); the path is not exercisable here")
	}
	if e.Name != "CovTarget" || e.Port != 9096 || e.Addr() != "192.168.213.89:9096" {
		t.Errorf("found %+v, want CovTarget at 192.168.213.89:9096", e)
	}
	// Two strangers and a host nothing advertises: the window runs out empty.
	if e, ok := FindSpotifyZC("nobody.local", net.ParseIP("192.168.213.90").To4(), 400*time.Millisecond); ok && e.Name == "CovWrong" {
		t.Errorf("guessed between strangers: %+v", e)
	}
}
