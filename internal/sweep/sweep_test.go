package sweep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/discovery"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// liveAnswers are the :2018 getters' answers as the box gave them on
// 2026-10-01, on AR241CP_8747 / MCU 29.
var liveAnswers = map[string]string{
	"VER": "29-1d316f0c-10", "STA": "NET,0,83,0,0,3,0,0,1,0", "SRC": "NET", "LST": "NET,BT,LINE-IN,USBPLAY",
	"MXV": "100", "PEQ": "0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal", "EQE": "0", "EQS": "0",
	"BAS": "0", "MID": "0", "TRE": "0", "VBS": "0", "VBI": "50", "BAL": "0",
}

// appIndexJSON is the vendor's app-0.json as its CDN served it on 2026-10-01.
const appIndexJSON = `[{"name":"rakoit_app","md5":"b1dadf706b06ee96e73eee90a65d53b2","param":"","version":"42"}]`

// upnpXML is a DLNA renderer's description in the usual shape: the default
// UPnP namespace, a DLNA one beside it, and one embedded device whose services
// repeat one of the root's.
const upnpXML = `<?xml version="1.0" encoding="UTF-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0" xmlns:dlna="urn:schemas-dlna-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>Living</friendlyName>
    <manufacturer>Arylic</manufacturer>
    <modelDescription>Wireless Audio Streamer</modelDescription>
    <modelName>LP10</modelName>
    <modelNumber>AR241CP</modelNumber>
    <dlna:X_DLNADOC>DMR-1.50</dlna:X_DLNADOC>
    <serviceList>
      <service><serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType></service>
      <service><serviceType>urn:schemas-upnp-org:service:ConnectionManager:1</serviceType></service>
      <service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType></service>
    </serviceList>
    <deviceList>
      <device>
        <friendlyName>embedded</friendlyName>
        <serviceList>
          <service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType></service>
          <service><serviceType>urn:schemas-wiimu-com:service:PlayQueue:1</serviceType></service>
        </serviceList>
      </device>
    </deviceList>
  </device>
</root>`

// livePorts is the scan of the box on 2026-10-01: no ssh, telnet or adb, and
// rakoit_app's second listener on a dynamic port.
var livePorts = []int{80, 2018, 2345, 7000, 7777, 9095, 44317, 49494}

// ---- the :2018 tunnel ----

// fastTunnel is the tunnel client's timing in tests: the live one's shape,
// scaled down so a silent connection costs half a second, not three.
var fastTunnel = tunnelTiming{dial: time.Second, wake: 500 * time.Millisecond, retry: 50 * time.Millisecond,
	spacing: 20 * time.Millisecond, reply: 200 * time.Millisecond}

// fakeTunnel is a :2018 stand-in on 127.0.0.1. serve runs once per accepted
// connection, numbered from 1; every query a connection sends is recorded,
// with when it arrived.
type fakeTunnel struct {
	addr    string
	mu      sync.Mutex
	queries []string
	at      []time.Time
	conns   []net.Conn
}

func newFakeTunnel(t *testing.T, serve func(ft *fakeTunnel, n int, c net.Conn)) *fakeTunnel {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTunnel{addr: ln.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		ln.Close()
		ft.mu.Lock()
		for _, c := range ft.conns {
			c.Close()
		}
		ft.mu.Unlock()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			ft.mu.Lock()
			ft.conns = append(ft.conns, c)
			n := len(ft.conns)
			ft.mu.Unlock()
			wg.Go(func() {
				defer c.Close()
				serve(ft, n, c)
			})
		}
	})
	return ft
}

// eachQuery reads "CODE;" queries from c until it closes, records each, and
// hands it to answer.
func (ft *fakeTunnel) eachQuery(c net.Conn, answer func(q string)) {
	var carry []byte
	b := make([]byte, 256)
	for {
		n, err := c.Read(b)
		carry = append(carry, b[:n]...)
		for {
			i := bytes.IndexByte(carry, ';')
			if i < 0 {
				break
			}
			q := string(carry[:i])
			carry = carry[i+1:]
			ft.mu.Lock()
			ft.queries, ft.at = append(ft.queries, q), append(ft.at, time.Now())
			ft.mu.Unlock()
			answer(q)
		}
		if err != nil {
			return
		}
	}
}

func (ft *fakeTunnel) stats() (queries []string, at []time.Time, conns int) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return slices.Clone(ft.queries), slices.Clone(ft.at), len(ft.conns)
}

// boxLike answers like the box: every query it has an answer for, as
// "CODE:VALUE;" — STA behind two unsolicited frames, PEQ split across two
// writes, LST followed by a remote key, the streaming source and an MXV the
// sweep has not asked for yet.
func boxLike(answers map[string]string) func(ft *fakeTunnel, n int, c net.Conn) {
	return func(ft *fakeTunnel, _ int, c net.Conn) {
		ft.eachQuery(c, func(q string) {
			v, ok := answers[q]
			if !ok {
				return // the device drops a query now and then
			}
			frame := q + ":" + v + ";"
			switch q {
			case "STA":
				c.Write([]byte("TIT:Song\x1b[31m;PLA:1;" + frame))
			case "PEQ":
				c.Write([]byte(frame[:9]))
				time.Sleep(5 * time.Millisecond)
				c.Write([]byte(frame[9:]))
			case "LST":
				c.Write([]byte(frame + "RAW:NEXT;VND:spotify;MXV:5;"))
			default:
				c.Write([]byte(frame))
			}
		})
	}
}

// silent accepts and reads, and never answers: the live box's stuck connection.
func silent(ft *fakeTunnel, _ int, c net.Conn) { ft.eachQuery(c, func(string) {}) }

// The sweep reads the box's getters over one connection, one query at a
// time, and sends nothing else: no set, no action code, nothing twice. The
// answers come back whole, however the device interleaves its own frames or
// splits one across writes; a query the device drops is left out.
func TestTunnelReadsOnlyTheGetters(t *testing.T) {
	answers := maps.Clone(liveAnswers)
	delete(answers, "MID")
	ft := newFakeTunnel(t, boxLike(answers))
	start := time.Now()
	got, err := readTunnel(context.Background(), ft.addr, fastTunnel)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, answers) {
		t.Errorf("answers = %v\nwant %v", got, answers)
	}
	queries, at, conns := ft.stats()
	// STA wakes the connection and is answered then; every other getter once,
	// in order
	want := []string{"STA", "VER", "SRC", "LST", "MXV", "PEQ", "EQE", "EQS", "BAS", "MID", "TRE", "VBS", "VBI", "BAL"}
	if !slices.Equal(queries, want) || conns != 1 {
		t.Errorf("sent %v over %d connections, want %v over one", queries, conns, want)
	}
	for _, q := range queries {
		if !slices.Contains(tunnelQueries, q) {
			t.Errorf("sent %q: not a getter the sweep may send", q)
		}
	}
	// the device drops back-to-back queries: they go out spacing apart (the
	// first arrival can lag its send, so the bound allows for that once)
	if span := at[len(at)-1].Sub(at[0]); span < time.Duration(len(at)-1)*fastTunnel.spacing-10*time.Millisecond {
		t.Errorf("%d queries in %v: closer than %v apart", len(at), span, fastTunnel.spacing)
	}
	if took := time.Since(start); took < fastTunnel.reply {
		t.Errorf("took %v: the missing MID answer was not waited for", took)
	}
	tf := tunnelFacts(got, nil)
	if !tf.OK || tf.Err != "" || tf.MCU != "29" || tf.Ver != "29-1d316f0c-10" || tf.Presets != liveAnswers["PEQ"] ||
		tf.Sources != "NET,BT,LINE-IN,USBPLAY" || len(tf.Settings) != 10 || tf.Settings["VBI"] != "50" ||
		!slices.Equal(tf.Unanswered, []string{"MID"}) {
		t.Errorf("facts = %+v", tf)
	}
}

// A fresh connection can be accepted and never served (the live box,
// 2026-10-01): after the wake wait the client closes it, pauses, and tries
// once more — and only once.
func TestTunnelRetriesASilentConnectionOnce(t *testing.T) {
	ft := newFakeTunnel(t, func(ft *fakeTunnel, n int, c net.Conn) {
		if n == 1 {
			silent(ft, n, c)
			return
		}
		boxLike(liveAnswers)(ft, n, c)
	})
	start := time.Now()
	got, err := readTunnel(context.Background(), ft.addr, fastTunnel)
	if err != nil || !maps.Equal(got, liveAnswers) {
		t.Fatalf("after a silent first connection: %v, %v", got, err)
	}
	if took := time.Since(start); took < fastTunnel.wake+fastTunnel.retry {
		t.Errorf("took %v: the silent connection was not waited for, or the retry not paused", took)
	}
	if _, _, conns := ft.stats(); conns != 2 {
		t.Errorf("%d connections, want 2", conns)
	}

	dead := newFakeTunnel(t, silent)
	got, err = readTunnel(context.Background(), dead.addr, fastTunnel)
	if !errors.Is(err, errSilentTunnel) || !strings.HasPrefix(err.Error(), "after a retry: ") || len(got) != 0 {
		t.Errorf("two silent connections: %v, %v", got, err)
	}
	if queries, _, conns := dead.stats(); conns != 2 || !slices.Equal(queries, []string{"STA", "STA"}) {
		t.Errorf("against a dead tunnel: %d connections, sent %v; want 2 and one STA each", conns, queries)
	}
	tf := tunnelFacts(got, err)
	if tf.OK || !strings.Contains(tf.Err, "sent nothing") || tf.Unanswered != nil {
		t.Errorf("facts of a dead tunnel = %+v", tf)
	}
}

// A connection the device closes halfway keeps what it answered, and says why
// the rest is missing; one it refuses is retried once, then is the error.
func TestTunnelLostHalfway(t *testing.T) {
	ft := newFakeTunnel(t, func(ft *fakeTunnel, _ int, c net.Conn) {
		ft.eachQuery(c, func(q string) {
			c.Write([]byte(q + ":" + liveAnswers[q] + ";"))
			if q == "LST" {
				c.Close()
			}
		})
	})
	got, err := readTunnel(context.Background(), ft.addr, fastTunnel)
	if err == nil || len(got) != 4 || got["LST"] == "" {
		t.Fatalf("a connection closed after LST = %v, %v", got, err)
	}
	tf := tunnelFacts(got, err)
	if !tf.OK || tf.Err == "" || tf.Ver == "" || tf.Presets != "" || len(tf.Unanswered) != 10 {
		t.Errorf("facts = %+v", tf)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now: a refused connect
	if _, err := readTunnel(context.Background(), addr, fastTunnel); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("a refused tunnel = %v", err)
	}
	if tf := tunnelFacts(nil, nil); tf.OK || tf.Err != "no answer to any query" {
		t.Errorf("a tunnel that answered nothing = %+v", tf)
	}
}

// Ctrl-C ends a tunnel read at once, even one blocked waiting on a silent
// connection, and even in the pause before the retry.
func TestTunnelStopsOnCancel(t *testing.T) {
	ft := newFakeTunnel(t, silent)
	for _, c := range []struct{ wake, after time.Duration }{
		{5 * time.Second, 50 * time.Millisecond},         // during the wake wait
		{100 * time.Millisecond, 300 * time.Millisecond}, // during the pause before the retry
	} {
		slow := fastTunnel
		slow.wake, slow.retry = c.wake, 5*time.Second
		ctx, cancel := context.WithTimeout(context.Background(), c.after)
		start := time.Now()
		_, err := readTunnel(ctx, ft.addr, slow)
		cancel()
		if err == nil || time.Since(start) > c.after+time.Second {
			t.Errorf("cancelled after %v: %v, took %v", c.after, err, time.Since(start))
		}
	}
}

// A device that floods without a ';' never grows the carry past its bound,
// and a connection that sends more than maxTunnelRead is given up.
func TestTunnelFloodIsBounded(t *testing.T) {
	ft := newFakeTunnel(t, func(ft *fakeTunnel, _ int, c net.Conn) {
		ft.eachQuery(c, func(string) {
			junk := bytes.Repeat([]byte("A"), 16<<10)
			for range (maxTunnelRead >> 14) + 2 {
				if _, err := c.Write(junk); err != nil {
					return
				}
			}
		})
	})
	start := time.Now()
	got, err := readTunnel(context.Background(), ft.addr, fastTunnel)
	if !errors.Is(err, errTunnelFlood) || len(got) != 0 {
		t.Errorf("a flood = %v, %v", got, err)
	}
	if took := time.Since(start); took > 2*fastTunnel.wake+fastTunnel.retry {
		t.Errorf("a flood took %v: the read cap did not end it", took)
	}

	// the carry itself, over a pipe: a run without ';' is dropped at the
	// bound, its tail up to the next ';' with it, and the next frame still
	// parses whole
	a, b := net.Pipe()
	defer a.Close()
	tc := &tunnelConn{conn: a, asked: map[string]bool{"VER": true}, stop: func() bool { return true }}
	go func() {
		b.Write(bytes.Repeat([]byte("A"), 10<<10))
		b.Write([]byte("AAAA;VER:29-1d316f0c-10;"))
		b.Write(bytes.Repeat([]byte("B"), 3<<10))
	}()
	got = map[string]string{}
	if ok, err := tc.await("VER", time.Now().Add(time.Second), got); !ok || err != nil || got["VER"] != "29-1d316f0c-10" {
		t.Fatalf("the frame after a flood = %v, %v, %v", ok, err, got)
	}
	if ok, err := tc.await("PEQ", time.Now().Add(100*time.Millisecond), got); ok || err != nil || len(tc.buf) > maxTunnelCarry {
		t.Errorf("a partial run: %v, %v, carry %d bytes (bound %d)", ok, err, len(tc.buf), maxTunnelCarry)
	}
	b.Close()
	if _, err := tc.await("PEQ", time.Now().Add(100*time.Millisecond), got); err == nil {
		t.Error("a closed pipe read as a timeout")
	}
}

// A frame is "CODE:VALUE" with a code of capitals and digits; the value is
// control-stripped and bounded. Anything else — an echoed bare query, a code
// in lower case or too long — is not an answer.
func TestParseTunnelFrame(t *testing.T) {
	code, val, ok := parseTunnelFrame("PEQ:\x1b[31m0@Flat\u202e," + strings.Repeat("x", 500))
	if !ok || code != "PEQ" || strings.ContainsAny(val, "\x1b\u202e") || utf8.RuneCountInString(val) != maxField+1 || !strings.HasSuffix(val, "…") {
		t.Errorf("a hostile PEQ = %q %q %v", code, val, ok)
	}
	for _, f := range []string{"VER", "ver:29", "V:1", "TOOLONGCODE:1", "VE R:1", ":29", ""} {
		if _, _, ok := parseTunnelFrame(f); ok {
			t.Errorf("%q parsed as an answer", f)
		}
	}
	if code, val, ok := parseTunnelFrame("MXV:"); !ok || code != "MXV" || val != "" {
		t.Errorf("an empty value = %q %q %v", code, val, ok)
	}
}

// ---- the port scan ----

// fakeDial answers a connect by port: what returns is what the box would.
func fakeDial(answer func(ctx context.Context, port int) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		_, p, _ := net.SplitHostPort(addr)
		var port int
		fmt.Sscan(p, &port)
		return answer(ctx, port)
	}
}

func openConn() net.Conn {
	a, b := net.Pipe()
	b.Close()
	return a
}

// A real scan of loopback listeners: the open ports, sorted; a port nothing
// listens on is not one of them.
func TestScanPortsFindsTheListeners(t *testing.T) {
	var ls []net.Listener
	var p int
	for try := 0; try < 20 && ls == nil; try++ {
		l0, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p = l0.Addr().(*net.TCPAddr).Port
		run := []net.Listener{l0}
		for i := 1; i <= 3 && p+3 <= 65535; i++ {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p+i))
			if err != nil {
				break
			}
			run = append(run, l)
		}
		if len(run) == 4 {
			ls = run
			continue
		}
		for _, l := range run {
			l.Close()
		}
	}
	if ls == nil {
		t.Skip("no run of four free loopback ports")
	}
	ls[2].Close() // p+2: free, and nothing listens there
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()
	open, _, err := scanPorts(context.Background(), "127.0.0.1", scanSpec{lo: p, hi: p + 3, workers: 2, timeout: time.Second, budget: 5 * time.Second})
	if err != nil || !slices.Equal(open, []int{p, p + 1, p + 3}) {
		t.Errorf("scan of %d..%d = %v, %v; want %d %d %d", p, p+3, open, err, p, p+1, p+3)
	}
}

// A refused or timed-out connect is a closed port; any other failure says
// nothing about the port and stops the scan with that error; a scan cut off
// by its budget keeps what it found and says how far it got. The debug ports
// go first, so a cut-off scan can still say they all answered.
func TestScanPortsAnswers(t *testing.T) {
	var mu sync.Mutex
	var order []int
	spec := scanSpec{lo: 1, hi: 40, workers: 1, timeout: time.Second, budget: 5 * time.Second}
	spec.dial = fakeDial(func(_ context.Context, port int) (net.Conn, error) {
		mu.Lock()
		order = append(order, port)
		mu.Unlock()
		switch {
		case port == 22 || port == 7:
			return openConn(), nil
		case port%2 == 0:
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		}
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	})
	open, debug, err := scanPorts(context.Background(), "192.0.2.13", spec)
	if err != nil || !debug || !slices.Equal(open, []int{7, 22}) {
		t.Errorf("refused and dropped ports = %v, %v, %v; want 7 22, the debug ports checked", open, debug, err)
	}
	sorted := slices.Sorted(slices.Values(order))
	if len(order) != 40 || order[0] != 22 || order[1] != 23 || order[2] != 1 || len(slices.Compact(sorted)) != 40 {
		t.Errorf("connect order = %v; want 22 23 first, then the range without them", order)
	}

	dials := 0
	spec.hi, spec.workers = 5000, 4
	spec.dial = fakeDial(func(context.Context, int) (net.Conn, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
	})
	if _, debug, err := scanPorts(context.Background(), "192.0.2.13", spec); !errors.Is(err, syscall.EHOSTUNREACH) || debug {
		t.Errorf("no route to host = %v, debug checked %v", err, debug)
	}
	if dials > 100 {
		t.Errorf("%d connects after the first no-route: the scan did not stop", dials)
	}

	spec.hi, spec.budget = 50, 100*time.Millisecond
	spec.dial = fakeDial(func(ctx context.Context, port int) (net.Conn, error) {
		switch {
		case port == 5:
			return openConn(), nil
		case port <= 10 || port == 22 || port == 23:
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		}
		<-ctx.Done() // a box that drops the rest, past the budget
		return nil, ctx.Err()
	})
	open, debug, err = scanPorts(context.Background(), "192.0.2.13", spec)
	if !slices.Equal(open, []int{5}) || !debug || err == nil || err.Error() != "scan cut off: 12 of 50 ports answered in 100ms" {
		t.Errorf("a cut-off scan = %v, %v, %v", open, debug, err)
	}
	// the debug ports themselves unanswered: not checked
	spec.dial = fakeDial(func(ctx context.Context, port int) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, debug, err := scanPorts(context.Background(), "192.0.2.13", spec); debug || err == nil {
		t.Errorf("a scan that heard nothing = debug %v, %v", debug, err)
	}
}

// The host resolves once, to an IPv4 address when it has one.
func TestResolveHost(t *testing.T) {
	for host, want := range map[string]string{"127.0.0.1": "127.0.0.1", "::1": "::1", "localhost": "127.0.0.1"} {
		if got, err := resolveHost(context.Background(), host); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %s", host, got, err, want)
		}
	}
}

// Listeners in the Linux ephemeral range move on every start — rakoit_app's
// second listener went 46835 → 44317 — so Diff compares the scans without
// them. dmr's 49494 is fixed inside the range, and every port below it
// counts; a scan with nothing fixed is an answer, a failed one is not.
func TestDiffIgnoresDynamicPorts(t *testing.T) {
	before := Report{Ports: PortFacts{OK: true, Open: []int{80, 2018, 2345, 7000, 7777, 9095, 46835, 49494}}}
	after := Report{Ports: PortFacts{OK: true, Open: livePorts}}
	if ch := Diff(before, after); len(ch) != 0 {
		t.Errorf("a dynamic port reads as a change: %+v", ch)
	}
	moved := Report{Ports: PortFacts{OK: true, Open: []int{22, 80, 2018, 2345, 7000, 7777, 9096, 44317}}}
	ch := Diff(after, moved)
	if len(ch) != 1 || ch[0].Field != "tcp ports" || ch[0].Was != "80 2018 2345 7000 7777 9095 49494" || ch[0].Now != "22 80 2018 2345 7000 7777 9096" {
		t.Errorf("fixed ports moved = %+v", ch)
	}
	bare := Report{Ports: PortFacts{OK: true, Open: []int{40000}}}
	if ch := Diff(after, bare); len(ch) != 1 || ch[0].Now != "none" {
		t.Errorf("a scan with nothing fixed = %+v", ch)
	}
	failed := Report{Ports: PortFacts{Err: "scan cut off", Open: []int{80}}}
	if ch := Diff(after, failed); len(ch) != 0 {
		t.Errorf("a failed scan reads as a change: %+v", ch)
	}
}

// The scan's lines: the fixed ports, the dynamic ones apart, and the debug
// listeners' state — "answer again" whenever one is open, even in a scan cut
// off; "closed" only when all four answered.
func TestWritePortsCallsOutDebugPorts(t *testing.T) {
	for _, c := range []struct {
		name  string
		p     PortFacts
		want  []string
		never string
	}{
		{"live", PortFacts{OK: true, Open: livePorts, DebugChecked: true}, []string{
			"tcp            80 2018 2345 7000 7777 9095 49494\n",
			"dynamic tcp    44317 (they move with each app restart; not compared)\n",
			"ssh/telnet/adb closed (22 23 5037 5555)\n"}, "answer again"},
		{"back", PortFacts{OK: true, Open: []int{22, 80, 5555}, DebugChecked: true}, []string{
			"tcp            22 80 5555\n", "ssh/telnet/adb answer again: 22 (ssh) · 5555 (adb)\n"}, "closed"},
		{"cut off", PortFacts{Open: []int{23, 80}, Err: "scan cut off: 900 of 65535 ports answered in 30s"}, []string{
			"tcp            23 80 · so far: scan cut off", "ssh/telnet/adb answer again: 23 (telnet)\n"}, "closed"},
		{"cut off, debug heard", PortFacts{Open: []int{80}, DebugChecked: true, Err: "scan cut off: 900 of 65535 ports answered in 30s"}, []string{
			"tcp            80 · so far: scan cut off", "ssh/telnet/adb closed (22 23 5037 5555)\n"}, "answer again"},
		{"failed", PortFacts{Err: "connect: no route to host"}, []string{"tcp            scan failed · connect: no route to host\n"}, "ssh/telnet/adb"},
		{"dynamic only", PortFacts{OK: true, Open: []int{44317}}, []string{"tcp            nothing fixed open\n"}, "answer again"},
	} {
		var out bytes.Buffer
		writePorts(func(label, val string) {
			if val != "" {
				fmt.Fprintf(&out, "  %-14s %s\n", label, val)
			}
		}, c.p)
		for _, w := range c.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s: missing %q:\n%s", c.name, w, out.String())
			}
		}
		if strings.Contains(out.String(), c.never) {
			t.Errorf("%s: says %q:\n%s", c.name, c.never, out.String())
		}
	}
}

// ---- the UPnP description, the app index, bounded GETs ----

// The description's names and services come out whatever namespaces it
// declares; the services of embedded devices count too, once each, sorted.
func TestParseUPnP(t *testing.T) {
	u := parseUPnP([]byte(upnpXML))
	want := []string{"urn:schemas-upnp-org:service:AVTransport:1", "urn:schemas-upnp-org:service:ConnectionManager:1",
		"urn:schemas-upnp-org:service:RenderingControl:1", "urn:schemas-wiimu-com:service:PlayQueue:1"}
	if !u.OK || u.FriendlyName != "Living" || u.Manufacturer != "Arylic" || u.ModelName != "LP10" || u.ModelNumber != "AR241CP" ||
		u.ModelDescription != "Wireless Audio Streamer" || !slices.Equal(u.Services, want) {
		t.Errorf("description = %+v", u)
	}
	if got := servicesFact(u.Services); got != "AVTransport:1 ConnectionManager:1 RenderingControl:1 urn:schemas-wiimu-com:service:PlayQueue:1" {
		t.Errorf("display = %q", got)
	}
	// XML forbids C0 controls (Go's decoder refuses the document); a bidi
	// override or a line separator is legal XML and is stripped here
	hostile := strings.Replace(upnpXML, "<friendlyName>Living", "<friendlyName>Living\u202e[31m\u2028"+strings.Repeat("y", 300), 1)
	if u := parseUPnP([]byte(hostile)); !u.OK || strings.ContainsAny(u.FriendlyName, "\u202e\u2028") || utf8.RuneCountInString(u.FriendlyName) != maxField+1 {
		t.Errorf("a hostile name = %q", u.FriendlyName)
	}
	var many strings.Builder
	many.WriteString(`<root><device><friendlyName>x</friendlyName><serviceList>`)
	for i := range 100 {
		fmt.Fprintf(&many, "<service><serviceType>urn:x:service:S%03d:1</serviceType></service>", i)
	}
	many.WriteString(`</serviceList></device></root>`)
	if u := parseUPnP([]byte(many.String())); !u.OK || len(u.Services) != maxServices {
		t.Errorf("100 services kept %d, want %d", len(u.Services), maxServices)
	}
	for in, err := range map[string]string{
		`<root><device><friendlyName>x`:                                        "unreadable description",
		"<root><device><friendlyName>a\x1b[2Jb</friendlyName></device></root>": "unreadable description",
		`garbage`: "unreadable description",
		``:        "unreadable description",
		`<html><device><friendlyName>x</friendlyName></device></html>`: "not a device description",
		`<root xmlns="urn:schemas-upnp-org:device-1-0"></root>`:        "not a device description",
	} {
		if u := parseUPnP([]byte(in)); u.OK || u.Err != err {
			t.Errorf("%q = %+v, want %q", in, u, err)
		}
	}
}

// The app index's rakoit_app entry, wherever it is in the list; anything else
// is an error fact.
func TestParseAppIndex(t *testing.T) {
	if v := parseAppIndex([]byte(appIndexJSON)); !v.OK || v.Name != "rakoit_app" || v.Version != "42" || v.MD5 != "b1dadf706b06ee96e73eee90a65d53b2" {
		t.Errorf("live index = %+v", v)
	}
	two := `[{"name":"other","version":"9"},{"name":"rakoit_app","md5":"aa\u001b[2J","version":"43\u202e` + strings.Repeat("9", 300) + `"}]`
	if v := parseAppIndex([]byte(two)); !v.OK || v.MD5 != "aa[2J" || strings.ContainsRune(v.Version, '\u202e') || utf8.RuneCountInString(v.Version) != maxField+1 {
		t.Errorf("second entry = %+v", v)
	}
	for in, err := range map[string]string{
		`[{"name":"other","version":"9"}]`: "no rakoit_app entry",
		`[]`:                               "no rakoit_app entry",
		`{"name":"rakoit_app"}`:            "unexpected reply",
		`<html>`:                           "unexpected reply",
	} {
		if v := parseAppIndex([]byte(in)); v.OK || v.Err != err {
			t.Errorf("%q = %+v, want %q", in, v, err)
		}
	}
}

// A bounded GET: the body of a 200, an error for anything else — another
// status, a body over the limit (cut, it would parse as a different answer),
// and, from the box, a redirect, which is never followed.
func TestGetBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/ok":
			w.Write([]byte(appIndexJSON))
		case "/full":
			w.Write(bytes.Repeat([]byte("x"), maxBody))
		case "/over":
			w.Write(bytes.Repeat([]byte("x"), maxBody+1))
		case "/away":
			http.Redirect(w, req, "/ok", http.StatusFound)
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if b, err := getBounded(ctx, http.DefaultClient, srv.URL+"/ok", maxBody); err != nil || string(b) != appIndexJSON {
		t.Errorf("ok = %q, %v", b, err)
	}
	if b, err := getBounded(ctx, http.DefaultClient, srv.URL+"/full", maxBody); err != nil || len(b) != maxBody {
		t.Errorf("a body at the limit = %d bytes, %v", len(b), err)
	}
	if _, err := getBounded(ctx, http.DefaultClient, srv.URL+"/over", maxBody); err == nil || err.Error() != "reply over 64 KiB" {
		t.Errorf("a body over the limit = %v", err)
	}
	if _, err := getBounded(ctx, http.DefaultClient, srv.URL+"/gone", maxBody); err == nil || err.Error() != "answered 404 Not Found" {
		t.Errorf("a 404 = %v", err)
	}
	if _, err := getBounded(ctx, lanClient, srv.URL+"/away", maxBody); err == nil || err.Error() != "answered 302 Found" {
		t.Errorf("the box's redirect = %v", err)
	}
	if _, err := getBounded(ctx, http.DefaultClient, "http://\x7f", maxBody); err == nil {
		t.Error("a bad url fetched")
	}
	if got := upnpURL("192.168.0.13"); got != "http://192.168.0.13:49494/description.xml" {
		t.Errorf("upnp url = %q", got)
	}
	if got := upnpURL("fe80::1"); got != "http://[fe80::1]:49494/description.xml" {
		t.Errorf("upnp url for v6 = %q", got)
	}
}

// The default probes: the tunnel client with the live timing, the UPnP GET
// against dmr's port, and the vendor switched off by LP10_OTA_URL="".
func TestDefaultProbes(t *testing.T) {
	ft := newFakeTunnel(t, boxLike(liveAnswers))
	pr := DefaultProbes()
	if got, err := pr.Tunnel(context.Background(), ft.addr); err != nil || !maps.Equal(got, liveAnswers) {
		t.Errorf("tunnel = %v, %v", got, err)
	}
	if _, err := pr.UPnP(context.Background(), "127.0.0.1"); err == nil {
		t.Error("a description from a port nothing listens on") // nothing serves dmr's port on a test host
	}
	if pr.Ports == nil || pr.AppIndex == nil || pr.Head == nil || pr.Manifest == nil || pr.LSSDP == nil || pr.FindZC == nil || pr.ProbeZC == nil {
		t.Errorf("a default probe is missing: %+v", pr)
	}
	t.Setenv("LP10_OTA_URL", "")
	if DefaultProbes().Manifest0 != "" {
		t.Error("LP10_OTA_URL=\"\" should switch the vendor probes off")
	}
}

// ---- Run, Write, Main ----

// quietLAN is a probe set where nothing answers: the tunnel silent, the scan
// with no route, no description, no LSSDP, no ZeroConf, and no vendor.
func quietLAN() Probes {
	return Probes{
		Ports: func(context.Context, string) ([]int, bool, error) {
			return nil, false, errors.New("dial tcp 192.0.2.13:1: connect: no route to host")
		},
		Tunnel: func(context.Context, string) (map[string]string, error) { return nil, errSilentTunnel },
		UPnP:   func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") },
		LSSDP: func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{}, false
		},
		FindZC: func(context.Context, string, net.IP, time.Duration) (discovery.SpotifyEndpoint, bool) {
			return discovery.SpotifyEndpoint{}, false
		},
		ProbeZC: func(context.Context, string, time.Duration) (discovery.SpotifyZCInfo, bool) {
			return discovery.SpotifyZCInfo{}, false
		},
		AppIndex: func(context.Context) ([]byte, error) { return nil, errors.New("cdn unreachable") },
	}
}

// boxProbes is the box as it answered on 2026-10-01, and a vendor that says
// AR241CP_8747 is current and offers it to an old build. answers is what the
// tunnel gives (nil: it is silent); the vendor is off without a manifest URL.
func boxProbes(answers map[string]string, manifest string) Probes {
	pr := quietLAN()
	pr.Ports = func(context.Context, string) ([]int, bool, error) { return livePorts, true, nil }
	pr.Tunnel = func(ctx context.Context, _ string) (map[string]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if answers == nil {
			return nil, errSilentTunnel
		}
		return maps.Clone(answers), nil
	}
	pr.UPnP = func(context.Context, string) ([]byte, error) { return []byte(upnpXML), nil }
	pr.LSSDP = func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
		return discovery.LSSDPInfo{FW: "AR241CP_8747.29.2", State: "S", NetMode: "ETH0", Name: "Living"}, true
	}
	pr.FindZC = func(context.Context, string, net.IP, time.Duration) (discovery.SpotifyEndpoint, bool) {
		return discovery.SpotifyEndpoint{Host: "living.local.", Port: 9095}, true
	}
	pr.ProbeZC = func(context.Context, string, time.Duration) (discovery.SpotifyZCInfo, bool) {
		return discovery.SpotifyZCInfo{Status: 101, LibraryVersion: "3.211.130-g110e3e03", Version: "2.10.0"}, true
	}
	pr.AppIndex = func(context.Context) ([]byte, error) { return []byte(appIndexJSON), nil }
	pr.Manifest = func(_ context.Context, _, build string) protocol.OTAInfo {
		if build == "AR241CP_8747" {
			return protocol.OTAInfo{Asked: build, UpToDate: true}
		}
		return protocol.OTAInfo{Asked: build, Offered: "AR241CP_8747", PackageURL: "https://cdn.example/lp10_AR241CP_8747_29_6701c857.swu"}
	}
	pr.Head = func(context.Context, string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: http.NoBody, ContentLength: 89527296,
			Header: http.Header{"Etag": {`"6abcc3dc-5561400"`}, "Last-Modified": {"Wed, 30 Sep 2026 08:10:00 GMT"}}}, nil
	}
	pr.Manifest0 = manifest
	return pr
}

// Run with every probe faked: each source lands in its facts, the vendor is
// asked twice (the build LSSDP reports, then an old one to learn the newest
// bundle) and the CDN HEAD fills the bundle facts; the report reads as prose.
func TestRunAssemblesTheReport(t *testing.T) {
	pr := boxProbes(liveAnswers, "https://manifest.example/v1")
	var asked []string
	manifest := pr.Manifest
	pr.Manifest = func(ctx context.Context, url, build string) protocol.OTAInfo {
		asked = append(asked, build)
		return manifest(ctx, url, build)
	}
	tunnel := pr.Tunnel
	pr.Tunnel = func(ctx context.Context, addr string) (map[string]string, error) {
		if addr != "192.0.2.13:2018" {
			t.Errorf("tunnel addr = %q", addr)
		}
		return tunnel(ctx, addr)
	}
	r := Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	if !r.Ports.OK || !slices.Equal(r.Ports.Open, livePorts) {
		t.Errorf("ports = %+v", r.Ports)
	}
	if !r.Tunnel.OK || r.Tunnel.MCU != "29" || r.Tunnel.Ver != "29-1d316f0c-10" || r.Tunnel.Unanswered != nil {
		t.Errorf("tunnel = %+v", r.Tunnel)
	}
	if !r.UPnP.OK || r.UPnP.ModelNumber != "AR241CP" || len(r.UPnP.Services) != 4 {
		t.Errorf("upnp = %+v", r.UPnP)
	}
	if !r.VendorApp.OK || r.VendorApp.Version != "42" {
		t.Errorf("vendor app = %+v", r.VendorApp)
	}
	if strings.Join(asked, ",") != "AR241CP_8747,AR241CP_1" || !r.Manifest.UpToDate || r.Manifest.Build != "AR241CP_8747" {
		t.Errorf("manifest asked for %v: %+v", asked, r.Manifest)
	}
	if r.Bundle.Err != "" || r.Bundle.Build != "AR241CP_8747" || r.Bundle.Size != 89527296 || r.Bundle.ETag != "6abcc3dc-5561400" {
		t.Errorf("bundle = %+v", r.Bundle)
	}

	var out bytes.Buffer
	Write(&out, r, nil, time.Now())
	for _, want := range []string{
		"device\n  firmware       AR241CP_8747.29.2 · mcu 29-1d316f0c-10\n",
		"  sources        NET,BT,LINE-IN,USBPLAY · now NET\n",
		"  eq presets     0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal\n",
		"  status         source=NET mute=0 volume=83 treble=0 bass=0 net=3 internet=0 playing=0 led=1 upgrading=0\n",
		"  settings       MXV:100 EQE:0 EQS:0 BAS:0 MID:0 TRE:0 VBS:0 VBI:50 BAL:0\n",
		"  upnp           Living · Arylic · LP10 AR241CP · Wireless Audio Streamer\n",
		"  upnp services  AVTransport:1 ConnectionManager:1 RenderingControl:1 urn:schemas-wiimu-com:service:PlayQueue:1\n",
		"  manifest       no update for AR241CP_8747\n",
		"  newest bundle  AR241CP_8747 · https://cdn.example/lp10_AR241CP_8747_29_6701c857.swu\n",
		"89527296 bytes · Wed, 30 Sep 2026 08:10:00 GMT · etag 6abcc3dc-5561400\n",
		"  vendor app     the vendor serves rakoit_app v42 · md5 b1dadf706b06…\n",
		"  lssdp          AR241CP_8747.29.2 · S · ETH0 · Living\n",
		"  spotify        :9095 · eSDK 3.211.130-g110e3e03 · zeroconf 2.10.0\n",
		"  tcp            80 2018 2345 7000 7777 9095 49494\n",
		"  ssh/telnet/adb closed (22 23 5037 5555)\n",
		"first sweep — nothing to compare with yet",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
		}
	}
	for _, never := range []string{"tunnel ", "no answer", "ssh "} {
		if strings.Contains(out.String(), never) {
			t.Errorf("a full report says %q:\n%s", never, out.String())
		}
	}
	// against a baseline, the diff — or its absence — is the last section
	prev := r
	prev.At = r.At.Add(-49 * time.Hour)
	out.Reset()
	Write(&out, r, &prev, r.At)
	if !strings.HasSuffix(out.String(), "since the last sweep ("+prev.At.Format("2006-01-02 15:04")+", 2d 1h ago)\n  nothing changed\n") {
		t.Errorf("unchanged report:\n%s", out.String())
	}
	prev.Tunnel.Ver, prev.VendorApp.Version = "23-4ef47210-9", "41"
	prev.Tunnel.Settings = map[string]string{"MXV": "60"} // a setting: never a change
	out.Reset()
	Write(&out, r, &prev, r.At)
	for _, want := range []string{"  mcu              23-4ef47210-9 → 29-1d316f0c-10\n", "  vendor app       41 → 42\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("diff missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "MXV:60") || strings.Contains(out.String(), "nothing changed") {
		t.Errorf("a setting reads as a change:\n%s", out.String())
	}
}

// Nothing answers: the report still prints and says what is missing — no
// claim about the debug ports — the vendor is not asked with no build, and
// the exit code says the inventory is incomplete.
func TestRunDegrades(t *testing.T) {
	pr := quietLAN()
	pr.Manifest0 = "https://manifest.example/v1"
	pr.Manifest = func(context.Context, string, string) protocol.OTAInfo {
		t.Fatal("the vendor was asked with no build to ask about")
		return protocol.OTAInfo{}
	}
	r := Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	if r.Tunnel.OK || r.Ports.OK || r.UPnP.OK || r.LSSDP.OK || r.ZeroConf.OK || r.VendorApp.OK || r.Manifest.Asked {
		t.Errorf("degraded run = %+v", r)
	}
	var out bytes.Buffer
	Write(&out, r, nil, time.Now())
	for _, want := range []string{
		"  tunnel         the tunnel accepted the connection but sent nothing (the mcu, its presets, sources and settings are unread)\n",
		"  upnp           no description · connection refused\n",
		"  manifest       not asked (no build to ask about)\n",
		"  vendor app     index unread · cdn unreachable\n",
		"  lssdp          no answer\n",
		"  spotify        not advertised",
		"  tcp            scan failed · dial tcp 192.0.2.13:1: connect: no route to host\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("degraded report missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "ssh/telnet/adb") || strings.Contains(out.String(), "firmware") {
		t.Errorf("a degraded report claims what it did not read:\n%s", out.String())
	}
	// no vendor at all: neither the manifest nor the app index is asked
	pr = boxProbes(liveAnswers, "")
	pr.AppIndex = func(context.Context) ([]byte, error) {
		t.Fatal("the app index was fetched with the vendor switched off")
		return nil, nil
	}
	r = Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
	out.Reset()
	Write(&out, r, nil, time.Now())
	if r.Manifest.Asked || r.VendorApp.OK || !strings.Contains(out.String(), "  vendor app     not asked\n") {
		t.Errorf("vendor off: %+v\n%s", r.VendorApp, out.String())
	}
	// a tunnel that answered some: the missing codes are named, with the reason
	r.Tunnel = tunnelFacts(map[string]string{"VER": "29-1d316f0c-10", "STA": "NET,0"}, errors.New("EOF"))
	out.Reset()
	Write(&out, r, nil, time.Now())
	if !strings.Contains(out.String(), "  no answer      SRC LST MXV PEQ EQE EQS BAS MID TRE VBS VBI BAL (EOF)\n") ||
		!strings.Contains(out.String(), "  status         NET,0\n") {
		t.Errorf("a half-read tunnel:\n%s", out.String())
	}
}

// Main: flags, the exit codes, and the baseline landing in LP10_STATE_DIR.
func TestMainFlagsAndExitCodes(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	box := false
	probesFor = func() Probes {
		if box {
			return boxProbes(liveAnswers, "")
		}
		return quietLAN()
	}
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13"}
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), cfg, []string{"--bogus"}, &stdout, &stderr); code != 2 {
		t.Errorf("bad flag exit = %d, want 2", code)
	}
	if code := Main(context.Background(), cfg, []string{"extra"}, &stdout, &stderr); code != 2 {
		t.Errorf("positional arg exit = %d, want 2", code)
	}
	// a run against nothing: the report prints, exit 1, and --no-save leaves
	// no baseline
	stdout.Reset()
	if code := Main(context.Background(), cfg, []string{"--no-save"}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "tunnel ") {
		t.Errorf("unreachable box: exit %d, out:\n%s\nerr:\n%s", code, stdout.String(), stderr.String())
	}
	if Load(config.SweepPath(cfg)) != nil {
		t.Error("--no-save wrote a baseline")
	}
	// --json prints the baseline's shape; an incomplete run still exits 1 and
	// still saves what it read — here nothing, with nothing before it
	stdout.Reset()
	stderr.Reset()
	if code := Main(context.Background(), cfg, []string{"--json"}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), `"host": "192.0.2.13"`) {
		t.Errorf("--json: exit %d, out:\n%s", code, stdout.String())
	}
	if b := Load(config.SweepPath(cfg)); b == nil || b.Tunnel.OK || b.Tunnel.Err == "" || b.Carried != nil {
		t.Errorf("an empty first sweep should save as it is: %+v", b)
	}
	if stderr.Len() != 0 {
		t.Errorf("a saved sweep has nothing for stderr, got:\n%s", stderr.String())
	}
	// the box answers: exit 0
	box = true
	if code := Main(context.Background(), cfg, []string{"--no-save"}, &stdout, &stderr); code != 0 {
		t.Errorf("a full sweep exit %d", code)
	}
}

// An interrupted sweep (Ctrl-C during the run) degrades every probe at once;
// saved as the baseline, that hollow report would make the next sweep diff
// against nothing and print "nothing changed" after a real OTA. The previous
// baseline must survive it.
func TestMainInterruptedKeepsBaseline(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	probesFor = func() Probes { return boxProbes(liveAnswers, "") }
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13"}
	path := config.SweepPath(cfg)
	seed := fullReport(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	seed.Tunnel.Ver = "23-4ef47210-9"
	if err := Save(path, seed); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	Main(ctx, cfg, nil, &stdout, &stderr)
	got := Load(path)
	if got == nil || got.Tunnel.Ver != seed.Tunnel.Ver || !got.At.Equal(seed.At) {
		t.Fatalf("interrupted sweep replaced the baseline: %+v\nstderr:\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "interrupted") {
		t.Errorf("stderr should say the baseline was left in place, got:\n%s", stderr.String())
	}
}

// A sweep whose tunnel never answered has no MCU version, and Diff skips every
// empty pair: saved as it is, it would make the next good sweep miss what
// moved — an MCU update would read "nothing changed". The merge saves this
// sweep's fresh answers and carries the tunnel's facts from the sweep that
// read them, dated; the next report names the date in its header, and the
// update still shows.
func TestMainTunnelFailureCarriesTheTunnelFacts(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	answers := liveAnswers
	state := "S"
	probesFor = func() Probes {
		pr := boxProbes(answers, "")
		pr.LSSDP = func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{FW: "AR241CP_8747.29.2", State: state, NetMode: "ETH0"}, true
		}
		return pr
	}
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13"}
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), cfg, nil, &stdout, &stderr); code != 0 { // 1. full sweep → baseline
		t.Fatalf("full sweep exit %d:\n%s", code, stderr.String())
	}
	first := Load(config.SweepPath(cfg))
	if first == nil || first.Carried != nil || first.Tunnel.MCU != "29" || first.VendorApp.Version != "" {
		t.Fatalf("a full first sweep carries nothing: %+v", first)
	}
	answers, state = nil, "P"
	stdout.Reset()
	if code := Main(context.Background(), cfg, nil, &stdout, &stderr); code != 1 { // 2. the tunnel stays silent
		t.Errorf("tunnel-less sweep exit %d, want 1", code)
	}
	b := Load(config.SweepPath(cfg))
	if b == nil || !b.At.After(first.At) || b.LSSDP.State != "P" || b.Tunnel.OK || b.Tunnel.Err == "" {
		t.Fatalf("the other answers should be this sweep's: %+v", b)
	}
	if b.Tunnel.Ver != "29-1d316f0c-10" || b.Tunnel.MCU != "29" || b.Tunnel.Presets == "" || b.Tunnel.Sources == "" || b.Tunnel.Settings["MXV"] != "100" {
		t.Errorf("the tunnel's facts should be the first sweep's: %+v", b.Tunnel)
	}
	for _, k := range []string{"tunnel.ver", "tunnel.peq", "tunnel.lst", "tunnel.settings"} {
		if at, ok := b.Carried[k]; !ok || !at.Equal(first.At) {
			t.Errorf("carried %s = %v, %v; want the first sweep's %v", k, at, ok, first.At)
		}
	}
	if len(b.Carried) != 4 {
		t.Errorf("carried %v: only the tunnel's facts", b.Carried)
	}
	asOf := "mcu, eq presets, sources, settings as of " + first.At.Format("Jan 2 15:04")
	if !strings.Contains(stdout.String(), "\nbaseline saved (kept from earlier sweeps: "+asOf+"); run again") {
		t.Errorf("the save should say what it kept:\n%s", stdout.String())
	}
	answers = maps.Clone(liveAnswers)
	answers["VER"] = "30-9a8b7c6d-10"
	stdout.Reset()
	Main(context.Background(), cfg, nil, &stdout, &stderr) // 3. the MCU took an update
	if !strings.Contains(stdout.String(), "  mcu              29-1d316f0c-10 → 30-9a8b7c6d-10\n") {
		t.Errorf("the MCU update is invisible after a tunnel-less sweep:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), " ago · "+asOf+")\n") {
		t.Errorf("the header should say the tunnel's facts are older:\n%s", stdout.String())
	}
	if b := Load(config.SweepPath(cfg)); b == nil || b.Carried != nil || b.Tunnel.MCU != "30" {
		t.Errorf("read again, nothing is carried: %+v", b)
	}
}

// A tunnel that drops one query keeps that one fact from the sweep before,
// and only that one: the other answers are this sweep's.
func TestMainPartlyReadTunnelCarriesWhatItMissed(t *testing.T) {
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	answers := liveAnswers
	probesFor = func() Probes { return boxProbes(answers, "") }
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13"}
	var stdout, stderr bytes.Buffer
	Main(context.Background(), cfg, nil, &stdout, &stderr)
	first := Load(config.SweepPath(cfg))
	answers = maps.Clone(liveAnswers)
	delete(answers, "PEQ")
	answers["MXV"] = "80"
	stdout.Reset()
	if code := Main(context.Background(), cfg, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("a partly-read sweep exit %d:\n%s", code, stderr.String())
	}
	b := Load(config.SweepPath(cfg))
	if b == nil || b.Tunnel.Presets != liveAnswers["PEQ"] || b.Tunnel.Settings["MXV"] != "80" || len(b.Carried) != 1 || !b.Carried["tunnel.peq"].Equal(first.At) {
		t.Fatalf("only PEQ should be carried: %+v", b)
	}
	if !strings.Contains(stdout.String(), "  no answer      PEQ\n") || !strings.Contains(stdout.String(), "  nothing changed\n") {
		t.Errorf("the report is this sweep's, and a setting is not a change:\n%s", stdout.String())
	}
}

// Every outside string reaches the report control-stripped: the tunnel's
// answers, the description, the app index, the scan's error, the vendor's
// version, package URL and error, and the CDN's status line and headers — Go's
// HTTP client passes a status line's ESC and a header's bidi override through
// as-is. The HEAD still asks for the package as the vendor named it.
func TestOutsideStringsAreControlStripped(t *testing.T) {
	var reply, headed string
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(reply))
	}))
	defer manifest.Close()
	offer := `{"errorCode":1000,"errorString":"SUCCESS","version":"AR241CP_9999\u001b]8;;https://evil.example/\u0007","url":"https://cdn.example/x\u202e.swu"}`
	hostile := map[string]string{}
	for code, v := range liveAnswers {
		hostile[code] = v + "\x1b[2J\u202e\u2028"
	}
	for _, c := range []struct {
		name  string
		reply string
		head  func(context.Context, string) (*http.Response, error)
	}{
		{"cdn down", offer, func(context.Context, string) (*http.Response, error) { return nil, errors.New("no cdn in a test") }},
		{"cdn status", offer, func(context.Context, string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found\x1b[31m", Body: http.NoBody}, nil
		}},
		{"cdn headers", offer, func(_ context.Context, url string) (*http.Response, error) {
			headed = url
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: http.NoBody, Header: http.Header{
				"Etag":          {"\"6a86\u202eb304\""},
				"Last-Modified": {"Thu, 20 Aug 2026\x1b[5m 07:55:48 GMT"},
			}}, nil
		}},
		{"vendor error", `{"errorCode":7,"errorString":"busy\u001b[2J\u2028"}`, nil},
	} {
		reply = c.reply
		pr := boxProbes(hostile, manifest.URL)
		pr.Ports = func(context.Context, string) ([]int, bool, error) {
			return []int{22}, false, errors.New("cut \x1b[2J off\u2028")
		}
		pr.UPnP = func(context.Context, string) ([]byte, error) {
			return []byte(strings.Replace(upnpXML, "<manufacturer>Arylic", "<manufacturer>Arylic\u202e[31m\u2028", 1)), nil
		}
		pr.AppIndex = func(context.Context) ([]byte, error) {
			return []byte(`[{"name":"rakoit_app","md5":"b1\u001b[2J","version":"42\u202e"}]`), nil
		}
		pr.LSSDP = func(context.Context, string, time.Duration) (discovery.LSSDPInfo, bool) {
			return discovery.LSSDPInfo{FW: "AR241CP_8747.29.2", Name: "Living\x1b[31m"}, true
		}
		pr.Manifest, pr.Head = workers.OTACheck, c.head
		r := Run(context.Background(), config.Config{Host: "192.0.2.13"}, pr)
		var out bytes.Buffer
		Write(&out, r, nil, time.Now())
		if strings.ContainsFunc(out.String(), func(r rune) bool { return r != '\n' && r != ' ' && unicode.In(r, unicode.C, unicode.Z) }) {
			t.Errorf("%s: the report carries raw control or format runes:\n%q", c.name, out.String())
		}
	}
	if headed != "https://cdn.example/x\u202e.swu" {
		t.Errorf("HEAD asked for %q, not the package the vendor named", headed)
	}
}

// The report never claims a save that did not happen: Main says "baseline
// saved" only after it wrote one — never under --no-save, an interrupt or a
// failed save — and never into --json's stdout. A sweep that lost a read
// still saves, and its claim says which facts the baseline kept, as of when.
func TestMainClaimsOnlyTheSaveItMade(t *testing.T) {
	answers := liveAnswers
	probesFor = func() Probes { return boxProbes(answers, "") }
	t.Cleanup(func() { probesFor = DefaultProbes })
	cfg := config.Config{Host: "192.0.2.13"}
	run := func(ctx context.Context, args ...string) (string, string) {
		var stdout, stderr bytes.Buffer
		Main(ctx, cfg, args, &stdout, &stderr)
		return stdout.String(), stderr.String()
	}
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()

	t.Setenv("LP10_STATE_DIR", t.TempDir())
	for _, c := range []struct {
		name string
		ctx  context.Context
		args []string
	}{
		{"--no-save", context.Background(), []string{"--no-save"}},
		{"interrupted", interrupted, nil},
	} {
		stdout, _ := run(c.ctx, c.args...)
		if Load(config.SweepPath(cfg)) != nil {
			t.Fatalf("%s saved a baseline", c.name)
		}
		if strings.Contains(stdout, "baseline saved") {
			t.Errorf("%s claims a save:\n%s", c.name, stdout)
		}
	}

	stdout, _ := run(context.Background())
	first := Load(config.SweepPath(cfg))
	if first == nil || !strings.HasSuffix(stdout, "first sweep — nothing to compare with yet\n\nbaseline saved; run again after a suspected update\n") {
		t.Errorf("a saved sweep should end by saying so:\n%s", stdout)
	}
	answers = nil
	stdout, _ = run(context.Background())
	if !strings.HasSuffix(stdout, "\n\nbaseline saved (kept from earlier sweeps: mcu, eq presets, sources, settings as of "+first.At.Format("Jan 2 15:04")+"); run again after a suspected update\n") {
		t.Errorf("a tunnel-less sweep should save and say what it kept:\n%s", stdout)
	}
	answers = liveAnswers
	stdout, _ = run(context.Background(), "--json")
	var r Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || r.Tunnel.MCU != "29" {
		t.Errorf("--json stdout must stay the bare report (%v):\n%s", err, stdout)
	}

	// a state dir that cannot be made: the save fails, stderr says why
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(file, "state"))
	stdout, stderr := run(context.Background())
	if strings.Contains(stdout, "baseline saved") || !strings.Contains(stderr, "baseline not saved: no state directory") {
		t.Errorf("a failed save: stdout\n%s\nstderr\n%s", stdout, stderr)
	}
}

// ---- baseline ----

func TestBaselineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sweep-test.json")
	if Load(path) != nil {
		t.Error("missing baseline should load as nil")
	}
	r := fullReport(time.Date(2026, 10, 1, 15, 38, 0, 0, time.Local))
	if err := Save(path, r); err != nil {
		t.Fatal(err)
	}
	if got := Load(path); got == nil || mustJSON(t, *got) != mustJSON(t, r) {
		t.Errorf("round trip = %+v", got)
	}
	// garbage is nil, never a panic
	if err := Save(path, Report{}); err != nil {
		t.Fatal(err)
	}
	if Load(path) != nil {
		t.Error("a baseline with no timestamp should load as nil")
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Load(path) != nil || Load("") != nil {
		t.Error("a garbled baseline should load as nil")
	}
	if Save("", r) == nil {
		t.Error("saving with no state dir should fail loudly")
	}
}

// Save replaces the baseline by renaming a new file over it, never by
// rewriting it in place: a reader holding the old file still reads the old
// sweep whole — so a kill mid-save leaves the last baseline, not a truncated
// one that Load would drop — and nothing but the 0600 baseline is left behind.
func TestSaveReplacesTheBaselineAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sweep-test.json")
	old := fullReport(time.Date(2026, 9, 12, 15, 38, 0, 0, time.Local))
	if err := Save(path, old); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	next := old
	next.At, next.Tunnel.Ver = old.At.Add(time.Hour), "30-9a8b7c6d-10"
	if err := Save(path, next); err != nil {
		t.Fatal(err)
	}
	var was Report
	if err := json.NewDecoder(held).Decode(&was); err != nil || was.Tunnel.Ver != old.Tunnel.Ver {
		t.Errorf("the old baseline was rewritten in place: %+v (%v)", was, err)
	}
	if got := Load(path); got == nil || got.Tunnel.Ver != next.Tunnel.Ver {
		t.Errorf("new baseline = %+v", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("baseline mode = %v, want 0600", fi.Mode().Perm())
	}
}

// fullReport is a sweep that read every fact once, at at.
func fullReport(at time.Time) Report {
	return Report{At: at, Host: "192.0.2.13",
		Ports: PortFacts{OK: true, Open: livePorts, DebugChecked: true},
		Tunnel: TunnelFacts{OK: true, MCU: "29", Ver: "29-1d316f0c-10", Presets: liveAnswers["PEQ"], Sources: liveAnswers["LST"],
			Settings: map[string]string{"STA": liveAnswers["STA"], "SRC": "NET", "MXV": "100"}},
		UPnP: UPnPFacts{OK: true, FriendlyName: "Living", Manufacturer: "Arylic", ModelName: "LP10", ModelNumber: "AR241CP",
			Services: []string{"urn:schemas-upnp-org:service:AVTransport:1"}},
		LSSDP:     LSSDPFacts{OK: true, FW: "AR241CP_8747.29.2", State: "S", NetMode: "ETH0", Name: "Living"},
		ZeroConf:  ZCFacts{OK: true, Port: 9095, LibraryVersion: "3.211.130", Version: "2.10.0"},
		VendorApp: VendorAppFacts{OK: true, Name: "rakoit_app", Version: "42", MD5: "b1dadf706b06ee96e73eee90a65d53b2"},
		Manifest:  ManifestFacts{Asked: true, Build: "AR241CP_8747", UpToDate: true},
		Bundle:    BundleFacts{Build: "AR241CP_8747", ETag: "6abcc3dc-5561400"},
	}
}

// blankReport is a sweep at at that read nothing: every probe failed.
func blankReport(at time.Time) Report {
	return Report{At: at, Host: "192.0.2.13",
		Ports:     PortFacts{Err: "connect: no route to host"},
		Tunnel:    TunnelFacts{Err: errSilentTunnel.Error()},
		UPnP:      UPnPFacts{Err: "connection refused"},
		ZeroConf:  ZCFacts{Port: 9095},
		VendorApp: VendorAppFacts{Err: "cdn unreachable"},
		Manifest:  ManifestFacts{Asked: true, Err: "vendor unreachable"},
		Bundle:    BundleFacts{Err: "vendor unreachable"}}
}

// Every fact a sweep did not read comes from the baseline, dated to the sweep
// that read it; every fact it did read is its own, and nothing is dated. A
// fact neither read stays unread.
func TestMergeKeepsWhatASweepCouldNotRead(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	prev := fullReport(t0)
	blank := blankReport(t0.Add(24 * time.Hour))
	m := merge(&prev, blank)
	// the tunnel's status is the sweep's own; every fact is the baseline's
	back := m
	back.At, back.Carried = prev.At, nil
	back.Tunnel.OK, back.Tunnel.Err = true, ""
	if b1, b2 := mustJSON(t, back), mustJSON(t, prev); b1 != b2 {
		t.Errorf("a sweep that read nothing should keep every fact:\n got %s\nwant %s", b1, b2)
	}
	want := "appIndex bundle lssdp manifest ports tunnel.lst tunnel.peq tunnel.settings tunnel.ver upnp zeroconf"
	if got := strings.Join(slices.Sorted(maps.Keys(m.Carried)), " "); got != want {
		t.Errorf("carried %s\n   want %s", got, want)
	}
	for k, at := range m.Carried {
		if !at.Equal(t0) {
			t.Errorf("carried %s dated %v, want %v", k, at, t0)
		}
	}
	if !m.At.Equal(blank.At) || m.Tunnel.OK || m.Tunnel.Err == "" {
		t.Errorf("the merge is this sweep's: at %v, tunnel %+v", m.At, m.Tunnel)
	}
	m.Tunnel.Settings["MXV"] = "5"
	if prev.Tunnel.Settings["MXV"] != "100" {
		t.Error("the merge shares its settings with the baseline it came from")
	}
	// read again: all of it fresh, nothing dated
	next := fullReport(t0.Add(48 * time.Hour))
	next.VendorApp.Version = "43"
	if m2 := merge(&m, next); m2.Carried != nil || m2.VendorApp.Version != "43" || !m2.At.Equal(next.At) {
		t.Errorf("a full sweep carries nothing: %+v", m2.Carried)
	}
	// neither read it: nothing to carry
	if m3 := merge(&blank, blank); m3.Carried != nil || m3.Tunnel.Ver != "" {
		t.Errorf("an unread fact came from nowhere: %+v", m3)
	}
	if m4 := merge(nil, blank); m4.Carried != nil {
		t.Errorf("no baseline: %+v", m4)
	}
}

// mustJSON is r as the baseline file would hold it, for a whole-report comparison.
func mustJSON(t *testing.T, r Report) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A fact carried again keeps the date it was read, not the date of the sweep
// that last carried it — through the baseline file, as Main runs it — and once
// read again it is fresh and the date goes.
func TestCarriedTwiceKeepsItsFirstDate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sweep-test.json")
	t0 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	if err := Save(path, merge(nil, fullReport(t0))); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 2; day++ { // two sweeps in a row lose all but LSSDP
		lost := blankReport(t0.Add(time.Duration(day) * 24 * time.Hour))
		lost.LSSDP = LSSDPFacts{OK: true, FW: "AR241CP_8747.29.2", NetMode: "ETH0", State: []string{"", "P", "S"}[day]}
		if err := Save(path, merge(Load(path), lost)); err != nil {
			t.Fatal(err)
		}
	}
	b := Load(path)
	if b == nil || !b.At.Equal(t0.Add(48*time.Hour)) || b.Tunnel.MCU != "29" || b.LSSDP.State != "S" {
		t.Fatalf("after two blank sweeps: %+v", b)
	}
	for _, k := range []string{"ports", "tunnel.ver", "upnp", "appIndex"} {
		if !b.Carried[k].Equal(t0) {
			t.Errorf("carried %s dated %v, want the sweep that read it, %v", k, b.Carried[k], t0)
		}
	}
	if _, ok := b.Carried["lssdp"]; ok {
		t.Error("lssdp was read each time: it is not carried")
	}
	if got := carriedNote(*b); got != "tcp ports, mcu, eq presets, sources, settings, upnp, zeroconf, vendor app, manifest, newest bundle as of Sep 20 10:00" {
		t.Errorf("note = %q", got)
	}
	if m := merge(b, fullReport(t0.Add(72*time.Hour))); m.Carried != nil {
		t.Errorf("read again, still carried: %v", m.Carried)
	}
}

// Diff compares the fresh sweep with the merged baseline, so what the last
// sweep could not read is compared with its last known value: a vendor-app
// update behind one failed fetch still shows, where the hollow report it
// merged over would hide it. The report's header says which facts are older.
func TestDiffAgainstMergedBaseline(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	read := fullReport(t0)
	lost := fullReport(t0.Add(24 * time.Hour))
	lost.VendorApp = VendorAppFacts{Err: "cdn unreachable"}
	lost.LSSDP.NetMode = "WLAN0"
	base := merge(&read, lost)
	now := fullReport(t0.Add(48 * time.Hour))
	now.VendorApp.Version, now.VendorApp.MD5 = "43", "9f1b"
	now.LSSDP.NetMode = "WLAN0"
	var fields []string
	for _, c := range Diff(base, now) {
		fields = append(fields, c.Field+" "+c.Was+" → "+c.Now)
	}
	if got := strings.Join(fields, "; "); got != "vendor app 42 → 43; vendor app md5 b1dadf706b06ee96e73eee90a65d53b2 → 9f1b" {
		t.Errorf("diff against the merge = %q (lssdp was fresh: WLAN0 both times)", got)
	}
	if ch := Diff(lost, now); len(ch) != 0 {
		t.Errorf("against the hollow report itself the update should not show (the reason for the merge): %+v", ch)
	}
	var out bytes.Buffer
	Write(&out, now, &base, now.At)
	if !strings.Contains(out.String(), "since the last sweep (2026-09-22 10:00, 1d 0h ago · vendor app as of Sep 21 10:00)\n") {
		t.Errorf("header:\n%s", out.String())
	}
}

// Diff names every firmware-side fact that moved, in report order, and none
// that a probe failed to read.
func TestDiffNamesWhatMoved(t *testing.T) {
	base := fullReport(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	if ch := Diff(base, base); len(ch) != 0 {
		t.Errorf("identical sweeps differ: %+v", ch)
	}
	next := fullReport(base.At.Add(time.Hour))
	next.Ports.Open = []int{22, 80, 2018}
	next.Tunnel.Ver, next.Tunnel.Presets, next.Tunnel.Sources = "30-9a8b7c6d-10", "0@Flat", "NET,BT"
	next.Tunnel.Settings = map[string]string{"MXV": "60"}
	next.UPnP.ModelNumber, next.UPnP.Services = "AR241CQ", []string{"urn:schemas-upnp-org:service:RenderingControl:1"}
	next.LSSDP.FW, next.LSSDP.NetMode = "AR241CP_9000.30.2", "WLAN0"
	next.ZeroConf.Port, next.ZeroConf.LibraryVersion = 9096, "3.203.239"
	next.VendorApp.Version, next.VendorApp.MD5 = "43", "9f1b"
	next.Manifest = ManifestFacts{Asked: true, Offered: "AR241CP_9100"}
	next.Bundle = BundleFacts{Build: "AR241CP_9100", ETag: "ffff"}
	var fields []string
	for _, c := range Diff(base, next) {
		fields = append(fields, c.Field)
	}
	want := "tcp ports mcu eq presets sources upnp model upnp services lssdp firmware lssdp netmode zeroconf port spotify eSDK vendor app vendor app md5 vendor verdict newest bundle bundle etag"
	if got := strings.Join(fields, " "); got != want {
		t.Errorf("changed fields:\n got %s\nwant %s", got, want)
	}
	unanswered := next
	unanswered.Ports, unanswered.Tunnel = PortFacts{Err: "x"}, TunnelFacts{Err: "x"}
	unanswered.UPnP.OK, unanswered.LSSDP.OK, unanswered.ZeroConf.OK = false, false, false
	unanswered.VendorApp = VendorAppFacts{Err: "x"}
	unanswered.Manifest.Asked = false
	unanswered.Bundle.Err = "cdn unreachable"
	if ch := Diff(base, unanswered); len(ch) != 0 {
		t.Errorf("unanswered probes reported as changes: %+v", ch)
	}
}

// The header's note groups the older facts by the sweep that read them, in
// report order; a key this version does not know — one the ssh sweeps wrote —
// is ignored.
func TestCarriedNoteGroupsByDate(t *testing.T) {
	d1 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	b := Report{At: d2.Add(24 * time.Hour), Carried: map[string]time.Time{
		"tunnel.ver": d2, "tunnel.peq": d2, "upnp": d1, "lssdp": d2, "bundle": d1,
		"identity": d1, "hashes.luciserver": d2, // the ssh sweeps' keys
	}}
	if got := carriedNote(b); got != "mcu, eq presets, lssdp as of Sep 22 10:00 · upnp, newest bundle as of Sep 21 09:00" {
		t.Errorf("note = %q", got)
	}
	if got := carriedNote(Report{At: d2}); got != "" {
		t.Errorf("nothing carried = %q", got)
	}
}

// The baseline the ssh sweeps saved on 2026-09-23 still loads — its
// "vendorApp" and "mcu" were strings, which the new report must not try to
// read into a struct — and still compares: the facts both sweeps have
// (LSSDP, ZeroConf, the vendor's manifest and bundle) diff, the new ones are
// named as read for the first time, and nothing is invented for them.
func TestLoadAcceptsTheSSHEraBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sweep-old.json")
	const old = `{
  "at": "2026-09-23T19:05:12.123456-03:00",
  "host": "192.168.0.13",
  "build": "AR241CE_8530",
  "buildDate": "2026-01-12",
  "svn": "318",
  "firmware": "AR241CE_8530",
  "mcu": "23",
  "kernel": "5.15.137",
  "vendorApp": "42",
  "vendorMd5": "b1dadf706b06ee96e73eee90a65d53b2",
  "hashes": {"luciserver": "465c90d4"},
  "bootAt": "2026-09-04T14:55:42-03:00",
  "reboot": "cold_boot",
  "uptime": 1656570,
  "tcp": [22, 23, 80, 2018, 2345, 5037, 5555, 7000, 7777, 9095, 46835, 49494],
  "udp": [68, 123, 1800, 1900, 3721, 5353],
  "spotifyFlags": "0/1",
  "running": ["airplaydemo", "bluetoothd", "dmr", "spotifymusicpro"],
  "dirtyKeys": 33,
  "dirtyKeysOk": true,
  "reconnects": 950,
  "reconnectsSince": "2026-09-01T22:20:00-03:00",
  "syslogFiles": 49,
  "reconnectsLast24h": 41,
  "reconnectsByDay": {"2026-09-22": 42},
  "otaLast": "NO_UPDATE",
  "otaAt": "2026-09-23T14:56:15-03:00",
  "lssdp": {"ok": true, "fw": "AR241CE_8530.23.2", "state": "S", "netMode": "ETH0", "name": "Living"},
  "zeroconf": {"ok": true, "port": 9095, "libraryVersion": "3.211.130", "version": "2.10.0"},
  "manifest": {"asked": true, "build": "AR241CE_8530", "upToDate": true, "offered": ""},
  "bundle": {"build": "", "url": "", "size": 0, "lastModified": "", "etag": "", "none": true},
  "carried": {"identity": "2026-09-22T10:00:00-03:00"}
}
`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	b := Load(path)
	if b == nil || !b.LSSDP.OK || b.LSSDP.FW != "AR241CE_8530.23.2" || !b.ZeroConf.OK || !b.Manifest.UpToDate || !b.Bundle.None {
		t.Fatalf("the ssh-era baseline = %+v", b)
	}
	if b.Ports.OK || b.Tunnel.MCU != "" || b.VendorApp.Version != "" || b.UPnP.OK {
		t.Errorf("the old ssh facts leaked into the new ones: %+v", b)
	}
	cur := fullReport(b.At.Add(8*24*time.Hour + 2*time.Hour))
	var out bytes.Buffer
	Write(&out, cur, b, cur.At)
	want := "since the last sweep (2026-09-23 19:05, 8d 2h ago)\n" +
		"  lssdp firmware   AR241CE_8530.23.2 → AR241CP_8747.29.2\n" +
		"  newest bundle    none offered → AR241CP_8747\n" +
		"  first read       tcp ports, mcu, eq presets, sources, settings, upnp, vendor app (nothing to compare with yet)\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Errorf("the first sweep against the ssh-era baseline ends:\n%s\nwant the suffix:\n%s", out.String(), want)
	}
	if m := merge(b, cur); m.Carried != nil {
		t.Errorf("a full sweep over the old baseline carries %v", m.Carried)
	}
	m := merge(b, blankReport(cur.At))
	if got := strings.Join(slices.Sorted(maps.Keys(m.Carried)), " "); got != "bundle lssdp manifest zeroconf" {
		t.Errorf("a blank sweep over the old baseline carries %s, want the four facts both shapes share", got)
	}
}

// TestScanRechecksKnownPorts: a dropped SYN on a known listener during the
// fast pass must not make it "closed" — the recheck finds it; a known port
// that refuses every time stays closed, and an unknown one gets no recheck.
func TestScanRechecksKnownPorts(t *testing.T) {
	var mu sync.Mutex
	tries := map[int]int{}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, ps, _ := net.SplitHostPort(addr)
		p, _ := strconv.Atoi(ps)
		mu.Lock()
		tries[p]++
		n := tries[p]
		mu.Unlock()
		switch {
		case p == 9095 && n >= 2: // dropped once, answers on the recheck
			a, b := net.Pipe()
			b.Close()
			return a, nil
		case p == 9095, p == 31000: // a dropped SYN looks like a timeout
			return nil, &net.OpError{Op: "dial", Err: timeoutErr{}}
		}
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	}
	s := scanSpec{lo: 9000, hi: 31000, workers: 64, timeout: time.Second, budget: 10 * time.Second,
		recheck: []int{9095, 9096, 22}, recheckTimeout: time.Second, recheckTries: 2, dial: dial}
	open, _, err := scanPorts(context.Background(), "127.0.0.1", s)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(open, []int{9095}) {
		t.Errorf("open = %v, want [9095]", open)
	}
	mu.Lock()
	defer mu.Unlock()
	if tries[9096] != 1+2 || tries[31000] != 1 || tries[22] != 0 {
		t.Errorf("dials: 9096 %d (want 3: the pass + 2 rechecks), 31000 %d (want 1: not known), 22 %d (want 0: out of range)",
			tries[9096], tries[31000], tries[22])
	}
}

// timeoutErr is a net.Error that says it timed out, as a dropped SYN does.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// The live recheck must dial with recheckTimeout, not the fast pass's
// timeout: a net.Dialer stops at the earlier of its Timeout and the ctx
// deadline, so reusing the fast dialer ended each recheck try at 400 ms,
// before the ~1 s SYN retransmit that gets through a dropped first SYN.
// 127.0.0.2 on macOS lo0 swallows SYNs, a local stand-in for a drop.
func TestRecheckHonoursRecheckTimeout(t *testing.T) {
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	if _, err := d.Dial("tcp", "127.0.0.2:9"); err == nil || errors.Is(err, syscall.ECONNREFUSED) || !portClosed(err) {
		t.Skipf("127.0.0.2 does not drop SYNs here (%v)", err)
	}
	s := scanSpec{lo: 9, hi: 9, workers: 1, timeout: 100 * time.Millisecond, budget: 10 * time.Second,
		recheck: []int{9}, recheckTimeout: time.Second, recheckTries: 1} // dial nil: the live dialer
	start := time.Now()
	if _, _, err := scanPorts(context.Background(), "127.0.0.2", s); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 900*time.Millisecond { // fast pass 100 ms + one 1 s recheck try
		t.Errorf("scan with a 1 s recheck took %v: the recheck gave up at %v", took, s.timeout)
	}
}

// The budget can expire while the recheck runs. The fast pass answered every
// port, so the scan must still not claim a complete answer: the known port
// it missed was never confirmed, and saving it as closed makes a false change.
func TestRecheckCutByBudgetIsNotOK(t *testing.T) {
	var mu sync.Mutex
	tries := map[int]int{}
	dial := func(ctx context.Context, _, addr string) (net.Conn, error) {
		_, ps, _ := net.SplitHostPort(addr)
		p, _ := strconv.Atoi(ps)
		mu.Lock()
		tries[p]++
		n := tries[p]
		mu.Unlock()
		if n == 1 { // the fast pass: a slow LAN, 9095's SYN dropped
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if p == 9095 {
				return nil, &net.OpError{Op: "dial", Err: timeoutErr{}}
			}
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}
		select { // the recheck: 9095 is open but answers after the budget
		case <-time.After(100 * time.Millisecond):
			a, b := net.Pipe()
			b.Close()
			return a, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s := scanSpec{lo: 9090, hi: 9099, workers: 10, timeout: time.Second, budget: 300 * time.Millisecond,
		recheck: []int{9095}, recheckTimeout: time.Second, recheckTries: 2, dial: dial}
	open, _, err := scanPorts(context.Background(), "192.0.2.13", s)
	if err == nil && !slices.Contains(open, 9095) {
		t.Errorf("scan = %v with no error: the budget cut the recheck, yet the scan claims a complete answer", open)
	}
}
