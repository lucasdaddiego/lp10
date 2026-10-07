// The :2018 tunnel: the read-only getters the sweep asks over one connection
// of its own, the reply parsing, and the facts made of the answers.

package sweep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// tunnelQueries are the only codes the sweep sends, each as a bare "CODE;"
// query, in this order. Every one is a getter. Never add a code without
// checking it on the Arylic UART API: several act — POP/NXT/PRE/STP change
// playback, SYS:/WRS/DEF:SAV/PMT/COE reboot or reset the box — and the tunnel
// relays whatever it gets straight to the MCU.
var tunnelQueries = []string{"VER", "STA", "SRC", "LST", "MXV", "PEQ", "EQE", "EQS", "BAS", "MID", "TRE", "VBS", "VBI", "BAL"}

// tunnelTiming is how the tunnel client waits: the connect, the first frame
// on a fresh connection, the pause before the one retry, the gap between
// queries, and each query's reply.
type tunnelTiming struct {
	dial, wake, retry, spacing, reply time.Duration
}

// liveTunnel is the timing the real box needs. The device drops a query sent
// right behind another, so they go out 150 ms apart (the TUI's seed spacing).
// A fresh connection can be accepted and never served (seen 2026-10-01), so
// the first frame gets 3 s, and a silent connection one more try 2 s later —
// never a burst: tcptunnelling serves one client at a time, and 26 quick
// connects in a row once left the next one hanging.
var liveTunnel = tunnelTiming{dial: 3 * time.Second, wake: 3 * time.Second, retry: 2 * time.Second,
	spacing: 150 * time.Millisecond, reply: 1500 * time.Millisecond}

const (
	maxTunnelCarry = 4 << 10   // a partial frame kept between reads; the real ones are under 100 bytes
	maxTunnelRead  = 256 << 10 // everything one connection may send before the sweep gives up on it
)

var (
	errSilentTunnel = errors.New("the tunnel accepted the connection but sent nothing")
	errTunnelFlood  = fmt.Errorf("the tunnel sent over %d KiB", maxTunnelRead>>10)
)

// readTunnel asks the tunnelQueries over one connection to addr and returns
// the answers by code. STA goes first, to wake the connection: with no frame
// back in t.wake, the connection is closed and, after t.retry, one more is
// tried. Then each code not answered yet is asked in turn, t.spacing after the
// last; one without an answer in t.reply is left out. The device's other
// frames — the now-playing title, a remote key — arrive in between and are
// dropped, as is any answer to a code the sweep did not ask. With an error,
// the answers are the ones that came before it.
func readTunnel(ctx context.Context, addr string, t tunnelTiming) (map[string]string, error) {
	got := map[string]string{}
	tc, err := wakeTunnel(ctx, addr, t, got)
	if err != nil && ctx.Err() == nil {
		if !pause(ctx, t.retry) {
			return got, ctx.Err()
		}
		if tc, err = wakeTunnel(ctx, addr, t, got); err != nil {
			return got, fmt.Errorf("after a retry: %w", err)
		}
	}
	if err != nil {
		return got, err
	}
	defer tc.close()
	for _, code := range tunnelQueries {
		if _, ok := got[code]; ok {
			continue
		}
		if err := tc.ask(ctx, code, t, got); err != nil {
			return got, err
		}
	}
	return got, nil
}

// tunnelConn is one held tunnel connection: what it carries between reads, what
// it has read in all, what the sweep has asked on it, and when it last asked.
type tunnelConn struct {
	conn  net.Conn
	stop  func() bool // ends the close-on-cancel hook
	buf   []byte
	skip  bool // a dropped run's tail is still arriving: discard up to the next ';'
	read  int
	asked map[string]bool
	last  time.Time
}

// wakeTunnel connects to addr, sends "STA;" and waits t.wake for any frame. A
// connection that sends nothing is closed and errSilentTunnel returned.
func wakeTunnel(ctx context.Context, addr string, t tunnelTiming, got map[string]string) (*tunnelConn, error) {
	d := net.Dialer{Timeout: t.dial}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// A read blocks until its deadline; a cancel (Ctrl-C) closes the socket
	// so the sweep stops at once.
	tc := &tunnelConn{conn: conn, asked: map[string]bool{}, stop: context.AfterFunc(ctx, func() { conn.Close() })}
	if err := tc.send("STA", t); err != nil {
		tc.close()
		return nil, err
	}
	served, err := tc.await("", time.Now().Add(t.wake), got)
	if err == nil && !served {
		err = errSilentTunnel
	}
	if err != nil {
		tc.close()
		return nil, err
	}
	return tc, nil
}

func (c *tunnelConn) close() {
	c.stop()
	c.conn.Close()
}

// ask sends one query, t.spacing after the last, and waits t.reply for its
// answer. A missing answer is not an error; a broken connection is.
func (c *tunnelConn) ask(ctx context.Context, code string, t tunnelTiming, got map[string]string) error {
	if !pause(ctx, t.spacing-time.Since(c.last)) {
		return ctx.Err()
	}
	if err := c.send(code, t); err != nil {
		return err
	}
	_, err := c.await(code, time.Now().Add(t.reply), got)
	return err
}

// send writes the bare query "CODE;" and notes the code as asked.
func (c *tunnelConn) send(code string, t tunnelTiming) error {
	c.asked[code] = true
	c.last = time.Now()
	if err := c.conn.SetWriteDeadline(c.last.Add(t.reply)); err != nil {
		return err
	}
	_, err := c.conn.Write([]byte(code + ";"))
	return err
}

// await reads frames until one answers want ("" for any frame at all) or the
// deadline passes: true when one did, false at the deadline. Every frame that
// answers an asked code lands in got, the first answer kept. A partial frame
// is carried to the next read, up to maxTunnelCarry: a longer run without a
// ';' is dropped, and so is the rest of it, up to the next ';' — kept, its
// tail would glue onto the next frame's code. A connection that sends more
// than maxTunnelRead in all is given up.
func (c *tunnelConn) await(want string, deadline time.Time, got map[string]string) (bool, error) {
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return false, err
	}
	var b [1024]byte
	for {
		n, err := c.conn.Read(b[:])
		c.read += n
		c.buf = append(c.buf, b[:n]...)
		hit := false
		for {
			i := bytes.IndexByte(c.buf, ';')
			if i < 0 {
				break
			}
			frame := strings.TrimSpace(string(c.buf[:i]))
			c.buf = c.buf[i+1:]
			if c.skip {
				c.skip = false
				continue
			}
			if frame != "" && want == "" {
				hit = true
			}
			code, val, ok := parseTunnelFrame(frame)
			if !ok || !c.asked[code] {
				continue
			}
			if _, dup := got[code]; !dup {
				got[code] = val
			}
			hit = hit || code == want
		}
		if len(c.buf) > maxTunnelCarry {
			c.buf, c.skip = nil, true
		}
		c.buf = slices.Clip(c.buf) // the next append starts a fresh array, not one under the read frames
		switch {
		case hit:
			return true, nil
		case c.read > maxTunnelRead:
			return false, errTunnelFlood
		case err == nil:
			continue
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return false, nil
		}
		return false, err
	}
}

// parseTunnelFrame splits "CODE:VALUE" into its code and its control-stripped,
// bounded value. A frame without a ':' (an echoed query) or with a code that
// is not 2–8 capitals and digits is not an answer.
func parseTunnelFrame(frame string) (code, val string, ok bool) {
	code, val, ok = strings.Cut(frame, ":")
	if !ok || len(code) < 2 || len(code) > 8 {
		return "", "", false
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", "", false
		}
	}
	return code, clip(val, maxField), true
}

// pause waits d (nothing for d ≤ 0); false when ctx ended first.
func pause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// tunnelFacts sorts the tunnel's answers into the report: VER, PEQ and LST
// are the firmware's, the rest the settings. Each value is stripped and
// bounded here, whichever probe read it.
func tunnelFacts(ans map[string]string, err error) TunnelFacts {
	t := TunnelFacts{OK: len(ans) > 0}
	if err != nil {
		t.Err = clip(err.Error(), maxField)
	} else if !t.OK {
		t.Err = "no answer to any query"
	}
	for _, code := range tunnelQueries {
		v, ok := ans[code]
		if !ok {
			t.Unanswered = append(t.Unanswered, code)
			continue
		}
		v = clip(v, maxField)
		switch code {
		case "VER":
			t.Ver = v
			t.MCU, _, _ = strings.Cut(v, "-")
		case "PEQ":
			t.Presets = v
		case "LST":
			t.Sources = v
		default:
			if t.Settings == nil {
				t.Settings = map[string]string{}
			}
			t.Settings[code] = v
		}
	}
	if !t.OK {
		t.Unanswered = nil // nothing was asked of a tunnel that never served
	}
	return t
}

// staFields name STA's comma-separated fields, in the device's order.
var staFields = []string{"source", "mute", "volume", "treble", "bass", "net", "internet", "playing", "led", "upgrading"}

// statusFact is STA's answer with its fields named — "source=NET mute=0
// volume=83 …" — or as sent when it does not have the ten fields.
func statusFact(sta string) string {
	parts := strings.Split(sta, ",")
	if len(parts) != len(staFields) {
		return sta
	}
	for i, p := range parts {
		parts[i] = staFields[i] + "=" + p
	}
	return strings.Join(parts, " ")
}

// settingCodes are the settings Write prints on one line, in this order.
var settingCodes = []string{"MXV", "EQE", "EQS", "BAS", "MID", "TRE", "VBS", "VBI", "BAL"}

// settingsFact is the user's settings as "MXV:100 EQE:0 …", the answered ones only.
func settingsFact(s map[string]string) string {
	var parts []string
	for _, code := range settingCodes {
		if v, ok := s[code]; ok {
			parts = append(parts, code+":"+v)
		}
	}
	return strings.Join(parts, " ")
}
