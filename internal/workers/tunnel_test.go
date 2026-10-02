package workers

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

// seedFrames is the connect seed as the box receives it: the player status
// first, then every EQ control, the preset names and the MCU build.
var seedFrames = []string{"STA", "MXV", "EQE", "EQS", "BAS", "MID", "TRE", "VBS", "VBI", "BAL", "PEQ", "VER"}

// tunnelRun is one tunnelWorker under test, stopped when the test ends.
type tunnelRun struct {
	st      *protocol.State
	control *runControl
	cmds    chan Command
	cancel  context.CancelFunc
	done    chan struct{}
}

// runTunnel starts tunnelWorker on ctx (cancelled by cancel) with the queue
// cmds; it dials LP10_TUNNEL_ADDR. The test's cleanup stops it and waits.
func runTunnel(t *testing.T, ctx context.Context, cancel context.CancelFunc, cmds chan Command) *tunnelRun {
	t.Helper()
	r := &tunnelRun{st: protocol.NewState(), control: newRunControl(), cmds: cmds, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		tunnelWorker(ctx, r.control, r.st, config.Config{Host: "127.0.0.1"}, r.cmds, "")
	}()
	t.Cleanup(func() {
		r.control.stop.Set()
		r.cancel()
		select {
		case <-r.done:
		case <-time.After(3 * time.Second):
			t.Error("the tunnel worker did not stop")
		}
	})
	return r
}

// TestTunnelWorkerAgainstALiveBox drives one worker through a whole connection
// against a fake that answers like the live box: the seed, the replies and
// pushes landing in State, one batch of commands (coalesced, allowlisted),
// the volume bridge, volume pacing, the status poll, and a reconnect when the
// box drops the link. It is one test because every step needs a seeded
// connection: the seed alone takes 12 × tunnelSpacing (1.8 s), and the first
// poll comes StatusEvery after it, so expect ~4.5 s.
func TestTunnelWorkerAgainstALiveBox(t *testing.T) {
	dev := newLiveBox()
	box := newFakeBox(t, dev.answer)
	t.Setenv("LP10_TUNNEL_ADDR", box.addr)

	// Queued while the seed goes out, so the write loop takes them as ONE
	// batch: the sets of a code collapse to the last at its position, actions
	// and queries keep every occurrence, and what Wire refuses never reaches
	// the box (WRS starts the wifi setup, SYS resets, STA is read-only, POP
	// is never a query).
	cmds := make(chan Command, 64)
	now := time.Now()
	for _, c := range []Command{
		{Code: "BAS", Val: 1}, {Code: "TRE", Val: 3}, {Code: "BAS", Val: 2},
		{Code: "POP"}, {Code: "WRS", Val: 1}, {Code: "POP"}, {Code: "MXV", Query: true},
		{Code: "STA", Val: 1}, {Code: "POP", Query: true}, {Code: "SYS", Val: 0},
		{Code: "BAS", Val: 5}, {Code: "NXT"},
	} {
		c.TS = now
		cmds <- c
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := runTunnel(t, ctx, cancel, cmds)
	st := r.st

	// The seed: every query, in order, paced by tunnelSpacing.
	ver := box.waitFrame(t, 3*time.Second, "the seed's last query", is("VER"))
	got := box.frames()
	if seed := texts(got[:len(seedFrames)]); !slices.Equal(seed, seedFrames) {
		t.Fatalf("seed = %q, want %q", seed, seedFrames)
	}
	if span, want := ver.at.Sub(got[0].at), time.Duration(len(seedFrames)-1)*tunnelSpacing-150*time.Millisecond; span < want {
		t.Errorf("the seed went out in %v, want it paced (≥ %v)", span, want)
	}

	// The replies: STA drives the player, the EQ getters the equalizer.
	eventually(t, "the seed replies applied", time.Second, func() bool {
		s := st.Snap()
		return s.Connected && s.VolLive && s.Source == "NET" && len(st.EQPresets()) == 6 && st.DiagnosticView().MCU != ""
	})
	if s := st.Snap(); s.Vol != 44 || s.Muted || !s.PlayKnown {
		t.Errorf("after the STA reply: vol %d muted %v playKnown %v, want 44 false true", s.Vol, s.Muted, s.PlayKnown)
	}
	if d := st.DiagnosticView(); d.MCU != "29-1d316f0c-10" || d.LastRx.IsZero() {
		t.Errorf("MCU %q lastRx %v, want the VER reply and a frame time", d.MCU, d.LastRx)
	}
	if p := st.EQPresets(); p[0] != "Flat" || p[5] != "Vocal" {
		t.Errorf("presets = %q", p)
	}
	for code, want := range map[string]int{"MXV": 100, "EQE": 1, "VBI": 50} {
		if v, ok := st.EQValue(code); !ok || v != want {
			t.Errorf("EQ %s = %d,%v, want %d", code, v, ok, want)
		}
	}

	// The batch, contiguous on the wire.
	box.waitFrame(t, 2*time.Second, "the batch's NXT", is("NXT"))
	got = box.frames()
	start := slices.IndexFunc(got, func(f rx) bool { return f.seq >= len(seedFrames) && f.frame == "TRE:3" })
	wantBatch := []string{"TRE:3", "POP", "POP", "MXV", "BAS:5", "NXT"}
	if start < 0 || start+len(wantBatch) > len(got) || !slices.Equal(texts(got[start:start+len(wantBatch)]), wantBatch) {
		t.Fatalf("after the seed the box received %q, want the batch %q in one run", texts(got[len(seedFrames):]), wantBatch)
	}
	pops := 0
	for _, f := range got {
		switch {
		case f.frame == "BAS:1", f.frame == "BAS:2":
			t.Errorf("a superseded set reached the box: %q", f.frame)
		case strings.HasPrefix(f.frame, "WRS"), strings.HasPrefix(f.frame, "SYS"), strings.HasPrefix(f.frame, "STA:"):
			t.Errorf("a command Wire refuses reached the box: %q", f.frame)
		case f.frame == "POP":
			pops++
		}
	}
	if pops != 2 {
		t.Errorf("the box got %d POP, want 2: two presses are two toggles, a POP query is never sent", pops)
	}
	eventually(t, "both toggles' pushes applied", time.Second, func() bool {
		s := st.Snap()
		return s.Service == "spotify" && !s.Playing
	})
	if v, _ := st.EQValue("BAS"); v != 5 {
		t.Errorf("BAS = %d, want the echo of the coalesced set (5)", v)
	}

	// The volume bridge: the connection's first reported level goes back once.
	box.waitFrame(t, time.Second, "the bridge of the seeded level", is("VOL:44"))

	// Pushes: the track, a resume, a mute, an EQ change, a source change.
	box.push("TIT:Big Bang;ART:Usted Señalemelo;ALB:Big Bang;")
	eventually(t, "the pushed track", time.Second, func() bool {
		tr := st.Snap().Track
		return tr != nil && *tr == protocol.Track{TrackName: "Big Bang", Artist: "Usted Señalemelo", Album: "Big Bang", Service: "spotify"}
	})
	dev.change(func(d *liveBox) { d.playing, d.muted, d.eq["BAS"] = true, true, -6 })
	box.push("PLA:1;VND:spotify;MUT:1;BAS:-6;")
	eventually(t, "the pushed resume, mute and BAS", time.Second, func() bool {
		v, _ := st.EQValue("BAS")
		s := st.Snap()
		return s.Playing && s.Muted && v == -6
	})
	dev.change(func(d *liveBox) { d.source = "BT" })
	box.push("SRC:BT;")
	eventually(t, "the pushed source", time.Second, func() bool {
		s := st.Snap()
		return s.Source == "BT" && s.Track == nil // a new input drops the old one's track
	})

	// A level changed outside lp10 (the phone app, the knob) reaches the next
	// STA, or arrives as a VOL frame of its own: either is bridged within a
	// tick or two.
	dev.change(func(d *liveBox) { d.vol = 30 })
	box.push(dev.status())
	box.waitFrame(t, 3*tunnelPoll, "the bridge of the level an STA reported", is("VOL:30"))
	if v := st.Snap().Vol; v != 30 {
		t.Errorf("vol = %d, want 30", v)
	}
	dev.change(func(d *liveBox) { d.vol = 35 })
	box.push("VOL:35;")
	box.waitFrame(t, 3*tunnelPoll, "the bridge of a pushed VOL", is("VOL:35"))

	// The tunnel has no framing: a title holding "VOL:100;" arrives as a
	// title and a VOL frame in one read. A read that carries a track field
	// applies only the track fields, so the room is not set to 100.
	box.push("TIT:Song;VOL:100;")
	eventually(t, "the title of the read", time.Second, func() bool {
		tr := st.Snap().Track
		return tr != nil && tr.TrackName == "Song"
	})
	if v := st.Snap().Vol; v != 35 {
		t.Errorf("vol = %d after a title carrying VOL:100, want 35", v)
	}

	// A local volume change (as the TUI makes one: SetVol arms the echo hold,
	// then the command) and a held key's repeats behind it: the worker paces
	// after VOL:10, the repeats pile up meanwhile, and only the newest goes.
	box.on("VOL:10", func(*boxConn) {
		for v := 11; v <= 20; v++ {
			cmds <- Command{Code: "VOL", Val: st.SetVol(v), TS: time.Now()}
		}
	})
	cmds <- Command{Code: "VOL", Val: st.SetVol(10), TS: time.Now()}
	v10 := box.waitFrame(t, time.Second, "the local VOL:10", is("VOL:10"))
	v20 := box.waitFrame(t, time.Second, "the newest repeat VOL:20", is("VOL:20"))
	if gap := v20.at.Sub(v10.at); gap < volumePace-30*time.Millisecond {
		t.Errorf("VOL:20 came %v after VOL:10, want the volume pace (%v)", gap, volumePace)
	}

	// The status poll: one STA query every StatusEvery once the seed is out.
	poll := box.waitFrame(t, StatusEvery+time.Second, "a status poll", func(f rx) bool { return f.frame == "STA" && f.seq > ver.seq })
	if gap := poll.at.Sub(ver.at); gap < StatusEvery-100*time.Millisecond {
		t.Errorf("the first poll came %v after the seed, want StatusEvery (%v)", gap, StatusEvery)
	}
	// Let a poll answer after VOL:20 (an unchanged level) and two more ticks
	// pass: nothing more is bridged — not the unchanged level, and not the
	// echoes of lp10's own sets, which land inside the echo hold.
	box.waitFrame(t, StatusEvery+time.Second, "a poll after VOL:20", func(f rx) bool { return f.frame == "STA" && f.seq > v20.seq })
	time.Sleep(2*tunnelPoll + 100*time.Millisecond)
	var vols []string
	for _, f := range box.frames() {
		if strings.HasPrefix(f.frame, "VOL") {
			vols = append(vols, f.frame)
		}
	}
	if want := []string{"VOL:44", "VOL:30", "VOL:35", "VOL:10", "VOL:20"}; !slices.Equal(vols, want) {
		t.Errorf("volume frames = %q, want %q (one bridge per outside change, none for an echo or a title's text)", vols, want)
	}

	// The box drops the link: the worker notices (its reader ends) and dials
	// again after InitialBackoff — the link was healthy, so the backoff
	// streak restarted.
	box.dropConns()
	eventually(t, "a reconnect", time.Second+InitialBackoff, func() bool { return box.accepted() == 2 })
	if a := st.Snap().Attempts; a != 2 {
		t.Errorf("attempts = %d, want 2", a)
	}
	cancel()
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the worker did not stop on cancel")
	}
	if !r.control.drained.IsSet() {
		t.Error("the worker ended without marking the drain done")
	}
}

// panicCtx is a context whose Deadline panics while armed. net.Dialer asks
// the context for its deadline at the top of every dial, so arming it makes
// the worker's next connection attempt panic inside the fenced lifecycle.
type panicCtx struct {
	context.Context
	armed atomic.Bool
}

func (c *panicCtx) Deadline() (time.Time, bool) {
	if c.armed.Load() {
		panic("boom")
	}
	return c.Context.Deadline()
}

// TestTunnelWorkerQuietBox runs the slow lifecycles that need a box which
// accepts and never answers, in parallel. LP10_TUNNEL_ADDR is process-wide,
// so they share one box: each subtest recognises its traffic by frames no
// other subtest sends, and asserts on its own State. The box also pushes an
// unknown frame (RAW:NEXT, what the live box sends after a skip) every
// 400 ms: noise that must neither mark the link live nor reset the silence
// clock.
func TestTunnelWorkerQuietBox(t *testing.T) {
	box := newFakeBox(t, nil)
	box.pushEvery(400*time.Millisecond, "RAW:NEXT;")
	t.Setenv("LP10_TUNNEL_ADDR", box.addr)

	// Slow by necessity: SilentAfter is a 6 s constant.
	t.Run("silence ends the connection", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		r := runTunnel(t, ctx, cancel, make(chan Command))
		noted := false
		for deadline := start.Add(SilentAfter + 3*time.Second); !noted && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if s := r.st.Snap(); s.Connected || !r.st.LastRx().IsZero() {
				t.Fatal("unknown frames marked the link live")
			}
			noted = r.st.Snap().Error == "the box stopped answering on :2018"
		}
		if !noted {
			t.Fatalf("error = %q, want the silence noted", r.st.Snap().Error)
		}
		if el := time.Since(start); el < SilentAfter {
			t.Errorf("the worker gave up after %v, before SilentAfter (%v)", el, SilentAfter)
		}
		eventually(t, "a fresh connection after the silence", time.Second+InitialBackoff, func() bool { return r.st.Snap().Attempts >= 2 })
	})

	// VBS:1 makes the box reset the link: the batch's next write (VBI:35)
	// fails and is carried into the next connection, where it goes out right
	// after the seed and ahead of BAL:15, queued meanwhile. ~4 s: two seeds.
	t.Run("a failed write is carried into the next connection", func(t *testing.T) {
		t.Parallel()
		cmds := make(chan Command, 8)
		box.on("VBS:1", func(c *boxConn) {
			c.reset()
			cmds <- Command{Code: "BAL", Val: 15}
		})
		// A zero TS never expires: two seeds pass before the carry goes out,
		// and this is not a deadline test.
		cmds <- Command{Code: "VBS", Val: 1}
		cmds <- Command{Code: "VBI", Val: 35}
		ctx, cancel := context.WithCancel(context.Background())
		runTunnel(t, ctx, cancel, cmds)
		trigger := box.waitFrame(t, 4*time.Second, "VBS:1", is("VBS:1"))
		carried := box.waitFrame(t, 5*time.Second, "the carried VBI:35", is("VBI:35"))
		if carried.conn == trigger.conn {
			t.Fatalf("VBI:35 went out on the reset connection")
		}
		seedEnd := box.waitFrame(t, time.Second, "the new connection's seed", func(f rx) bool { return f.conn == carried.conn && f.frame == "VER" })
		if seedEnd.seq > carried.seq {
			t.Error("the carry went out before the new connection's seed")
		}
		queued := box.waitFrame(t, time.Second, "BAL:15, queued during the outage", is("BAL:15"))
		if queued.conn != carried.conn || queued.seq < carried.seq {
			t.Errorf("BAL:15 (conn %d, #%d) should follow the carry (conn %d, #%d)", queued.conn, queued.seq, carried.conn, carried.seq)
		}
	})

	// BAS:1 makes the box reset the link (TRE:2 and MID:3 behind it are
	// carried) and arms a panic in the next dial. The fence notes it and drops
	// the carry: its delivery state is unknown, and a stale re-apply is worse
	// than a lost one. ~5.5 s: two seeds and the fence's 1 s hold.
	t.Run("a panic drops the carry", func(t *testing.T) {
		t.Parallel()
		cmds := make(chan Command, 8)
		ctx, cancel := context.WithCancel(context.Background())
		pctx := &panicCtx{Context: ctx}
		box.on("BAS:1", func(c *boxConn) {
			pctx.armed.Store(true)
			c.reset()
		})
		// Zero TS: carried commands that never expire, so only the fence can
		// keep them off the next connection.
		cmds <- Command{Code: "BAS", Val: 1}
		cmds <- Command{Code: "TRE", Val: 2}
		cmds <- Command{Code: "MID", Val: 3}
		r := runTunnel(t, pctx, cancel, cmds)
		eventually(t, "the panic noted", 5*time.Second, func() bool { return r.st.Snap().Error == "tunnel worker: boom" })
		pctx.armed.Store(false)
		eventually(t, "the connection after the fence", 2*time.Second, func() bool { return r.st.Snap().Attempts >= 3 })
		time.Sleep(time.Duration(len(seedFrames))*tunnelSpacing + 500*time.Millisecond) // its seed, then where a carry would go
		for _, f := range box.frames() {
			if f.frame == "TRE:2" || f.frame == "MID:3" {
				t.Errorf("the carry outlived the panic: %q reached the box", f.frame)
			}
		}
	})
}

// A refused dial is noted in words (the box is up but :2018 is closed) and
// doubles the backoff; the carry waits for the next connection untouched.
func TestTunnelDialRefusedNotesAndBacksOff(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("LP10_TUNNEL_ADDR", addr)
	if got := tunnelAddr(config.Config{Host: "192.0.2.1"}); got != addr {
		t.Fatalf("tunnelAddr = %q, want the override %q", got, addr)
	}

	st := protocol.NewState()
	control := newRunControl()
	carry := []Command{{Code: "BAS", Val: 3}}
	next, kept := tunnelOnceContext(context.Background(), control, st, config.Config{}, nil, 10*time.Millisecond, carry, "")
	if next != 20*time.Millisecond || !slices.Equal(kept, carry) {
		t.Errorf("refused dial = (%v, %+v), want (20ms, the carry kept)", next, kept)
	}
	if s := st.Snap(); !strings.HasPrefix(s.Error, "cannot reach :2018: ") || s.Connected || s.Attempts != 1 {
		t.Errorf("after a refused dial: error %q connected %v attempts %d", s.Error, s.Connected, s.Attempts)
	}
	if next, _ = tunnelOnceContext(context.Background(), control, st, config.Config{}, nil, next, carry, ""); next != 40*time.Millisecond {
		t.Errorf("second refused dial: backoff %v, want 40ms", next)
	}
}

// Quitting with the link up: the seed and the loop are skipped, the carry
// goes first (the expired part dropped with a note), then what was queued
// before q, coalesced; the drain is marked done and no backoff is slept.
func TestTunnelStopWritesCarryThenQueue(t *testing.T) {
	box := newFakeBox(t, newLiveBox().answer)
	t.Setenv("LP10_TUNNEL_ADDR", box.addr)
	st := protocol.NewState()
	control := newRunControl()
	control.stop.Set()
	old := time.Now().Add(-CommandDeadline - time.Second)
	carry := []Command{{Code: "MXV", Val: 40, TS: old}, {Code: "BAS", Val: 99, TS: time.Now()}, {Code: "MID", Val: 2, TS: old}}
	cmds := make(chan Command, 8)
	for _, c := range []Command{{Code: "TRE", Val: 3}, {Code: "TRE", Val: 4}, {Code: "POP"}} {
		c.TS = time.Now()
		cmds <- c
	}
	next, left := tunnelOnceContext(context.Background(), control, st, config.Config{}, cmds, 123*time.Millisecond, carry, "")
	if next != 123*time.Millisecond || len(left) != 0 {
		t.Errorf("quit = (%v, %+v), want the backoff untouched and nothing left to carry", next, left)
	}
	box.waitFrame(t, time.Second, "the drained POP", is("POP"))
	if got, want := texts(box.frames()), []string{"BAS:10", "TRE:4", "POP"}; !slices.Equal(got, want) {
		t.Errorf("the box received %q, want %q (no seed on quit, the carry clamped, the queue coalesced)", got, want)
	}
	if e := st.Snap().Error; e != "command not delivered" {
		t.Errorf("note = %q, want the expired carry noted", e)
	}
	if !control.drained.IsSet() || len(cmds) != 0 {
		t.Errorf("drained %v, %d left queued; want the queue written and the drain marked", control.drained.IsSet(), len(cmds))
	}
}

// A link reset during the seed never gets to the carry: it comes back whole
// for the next connection instead of being lost with the dead socket.
func TestTunnelResetDuringSeedKeepsCarry(t *testing.T) {
	box := newFakeBox(t, nil)
	box.resetOnAccept()
	t.Setenv("LP10_TUNNEL_ADDR", box.addr)
	st := protocol.NewState()
	carry := []Command{{Code: "BAL", Val: 20}}
	next, left := tunnelOnceContext(context.Background(), newRunControl(), st, config.Config{}, make(chan Command), time.Millisecond, carry, "")
	if next != 2*time.Millisecond || !slices.Equal(left, carry) {
		t.Errorf("reset during the seed = (%v, %+v), want (2ms, the carry back)", next, left)
	}
	for _, f := range box.frames() {
		if f.frame == "BAL:20" {
			t.Error("the carry was written to a dead connection")
		}
	}
	if st.Snap().Connected {
		t.Error("a reset connection reads as connected")
	}
}

// A cancelled context ends the attempt before any packet: no note (quitting
// is not a failure), the backoff and the carry unchanged.
func TestTunnelOnceCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := protocol.NewState()
	carry := []Command{{Code: "BAS", Val: 1}}
	next, left := tunnelOnceContext(ctx, newRunControl(), st, config.Config{Host: "127.0.0.1"}, nil, 77*time.Millisecond, carry, "")
	if next != 77*time.Millisecond || !slices.Equal(left, carry) {
		t.Errorf("cancelled = (%v, %+v), want (77ms, the carry)", next, left)
	}
	if s := st.Snap(); s.Error != "" || s.Connected || s.Attempts != 1 {
		t.Errorf("cancelled: error %q connected %v attempts %d", s.Error, s.Connected, s.Attempts)
	}
}

func TestTunnelAddrDefault(t *testing.T) {
	t.Parallel() // LP10_TUNNEL_ADDR is unset here (TestMain)
	for host, want := range map[string]string{"192.168.0.13": "192.168.0.13:2018", "lp10.local": "lp10.local:2018", "fe80::1": "[fe80::1]:2018"} {
		if got := tunnelAddr(config.Config{Host: host}); got != want {
			t.Errorf("tunnelAddr(%q) = %q, want %q", host, got, want)
		}
	}
}

// Sets queued behind one another for one code collapse to the last, at the
// last one's position (a held key queues one per repeat; the device needs
// the final value); queries and actions are never merged.
func TestCoalesce(t *testing.T) {
	t.Parallel()
	set := func(code string, v int) Command { return Command{Code: code, Val: v} }
	query := func(code string) Command { return Command{Code: code, Query: true} }
	act := func(code string) Command { return Command{Code: code} }
	for _, tc := range []struct {
		name    string
		in, out []Command
	}{
		{"empty", nil, nil},
		{"one", []Command{set("BAS", 1)}, []Command{set("BAS", 1)}},
		{"last set per code at its position", []Command{set("BAS", 1), set("TRE", 3), set("BAS", 2), query("MXV"), set("BAS", 5)},
			[]Command{set("TRE", 3), query("MXV"), set("BAS", 5)}},
		{"a query of the code stays between", []Command{set("BAS", 1), query("BAS"), set("BAS", 2)}, []Command{query("BAS"), set("BAS", 2)}},
		{"actions are never merged", []Command{act("POP"), act("POP"), act("NXT"), act("PRE"), act("NXT")},
			[]Command{act("POP"), act("POP"), act("NXT"), act("PRE"), act("NXT")}},
		{"a held volume key", []Command{set("VOL", 10), set("VOL", 11), set("MUT", 1), set("VOL", 12), set("MUT", 0)},
			[]Command{set("VOL", 12), set("MUT", 0)}},
	} {
		if got := coalesce(tc.in); !slices.Equal(got, tc.out) {
			t.Errorf("%s: coalesce = %+v, want %+v", tc.name, got, tc.out)
		}
	}
}

func TestGather(t *testing.T) {
	t.Parallel()
	cmds := make(chan Command, 4)
	if got := gather(Command{Code: "POP"}, cmds); len(got) != 1 {
		t.Errorf("gather with nothing queued = %+v", got)
	}
	cmds <- Command{Code: "BAS", Val: 1}
	cmds <- Command{Code: "TRE", Val: 2}
	if got := gather(Command{Code: "POP"}, cmds); len(got) != 3 || got[0].Code != "POP" || got[2].Code != "TRE" || len(cmds) != 0 {
		t.Errorf("gather = %+v (left %d), want the first then the queue in order", got, len(cmds))
	}
}

// commandWire is the wire boundary: the allowlist first (a refused code is
// never "not delivered", it was never deliverable), then the deadline, which
// drops an expired set visibly (stale) and an expired query silently.
func TestCommandWire(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := now.Add(-CommandDeadline - time.Second)
	for _, tc := range []struct {
		cmd   Command
		wire  string
		stale bool
	}{
		{Command{Code: "BAS", Val: 99, TS: now}, "BAS:10;", false}, // clamped
		{Command{Code: "BAS", Val: -3}, "BAS:-3;", false},          // a zero TS never expires
		{Command{Code: "BAS", Val: 1, TS: old}, "", true},
		{Command{Code: "MXV", Query: true, TS: now}, "MXV;", false},
		{Command{Code: "MXV", Query: true, TS: old}, "", false},
		{Command{Code: "VOL", Val: 150, TS: now}, "VOL:100;", false},
		{Command{Code: "MUT", Val: 1, TS: now}, "MUT:1;", false},
		{Command{Code: "POP", TS: now}, "POP;", false},
		{Command{Code: "POP", Query: true, TS: now}, "", false},
		{Command{Code: "STA", Query: true, TS: now}, "STA;", false},
		{Command{Code: "STA", Val: 1, TS: now}, "", false},
		{Command{Code: "WRS", Val: 1, TS: old}, "", false}, // refused before it is aged
		{Command{Code: "NOPE", Query: true, TS: now}, "", false},
	} {
		if wire, stale := commandWire(tc.cmd, now); wire != tc.wire || stale != tc.stale {
			t.Errorf("commandWire(%+v) = (%q, %v), want (%q, %v)", tc.cmd, wire, stale, tc.wire, tc.stale)
		}
	}
}

// scriptConn is a net.Conn that records its writes, and fails every write
// from the failAt-th on (0: never). Only Write is used by the code under test.
type scriptConn struct {
	net.Conn
	mu     sync.Mutex
	writes []string
	at     []time.Time
	tries  int
	failAt int
}

func (c *scriptConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tries++
	if c.failAt > 0 && c.tries >= c.failAt {
		return 0, errors.New("broken pipe")
	}
	c.writes = append(c.writes, string(b))
	c.at = append(c.at, time.Now())
	return len(b), nil
}

func (c *scriptConn) written() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.writes...)
}

// tunnelSend never silently loses user intent: a failed write hands the
// command back for the next connection, an expired one is noted, an expired
// refresh query is not, and a refused code never reaches the wire.
func TestTunnelSend(t *testing.T) {
	t.Parallel()
	old := time.Now().Add(-CommandDeadline - time.Second)
	t.Run("written", func(t *testing.T) {
		st, c := protocol.NewState(), &scriptConn{}
		if carry, dead := tunnelSend(st, c, Command{Code: "BAS", Val: 5, TS: time.Now()}); carry != nil || dead || !slices.Equal(c.written(), []string{"BAS:5;"}) {
			t.Errorf("= (%+v, %v), wrote %q", carry, dead, c.written())
		}
	})
	t.Run("failed write is carried", func(t *testing.T) {
		st, c := protocol.NewState(), &scriptConn{failAt: 1}
		carry, dead := tunnelSend(st, c, Command{Code: "BAS", Val: 5, TS: time.Now()})
		if carry == nil || !dead || carry.Code != "BAS" || carry.Val != 5 {
			t.Errorf("= (%+v, %v), want the command back and dead", carry, dead)
		}
	})
	t.Run("expired set is noted", func(t *testing.T) {
		st, c := protocol.NewState(), &scriptConn{}
		if carry, dead := tunnelSend(st, c, Command{Code: "BAS", Val: 5, TS: old}); carry != nil || dead || len(c.written()) != 0 {
			t.Errorf("= (%+v, %v), wrote %q", carry, dead, c.written())
		}
		if e := st.Snap().Error; e != "command not delivered" {
			t.Errorf("note = %q", e)
		}
	})
	t.Run("expired query is dropped silently", func(t *testing.T) {
		st, c := protocol.NewState(), &scriptConn{}
		if carry, dead := tunnelSend(st, c, Command{Code: "MXV", Query: true, TS: old}); carry != nil || dead || len(c.written()) != 0 || st.Snap().Error != "" {
			t.Errorf("= (%+v, %v), wrote %q, note %q", carry, dead, c.written(), st.Snap().Error)
		}
	})
	t.Run("refused code never reaches the wire", func(t *testing.T) {
		st, c := protocol.NewState(), &scriptConn{}
		for _, cmd := range []Command{{Code: "NOPE", Val: 1}, {Code: "WRS"}, {Code: "SYS", Val: 1}, {Code: "PLA", Val: 1}} {
			if carry, dead := tunnelSend(st, c, cmd); carry != nil || dead {
				t.Errorf("%+v = (%+v, %v)", cmd, carry, dead)
			}
		}
		if len(c.written()) != 0 || st.Snap().Error != "" {
			t.Errorf("wrote %q, note %q", c.written(), st.Snap().Error)
		}
	})
}

// sendBatchPaced spaces its writes, reports whether a volume SET went out
// (the caller then paces), and on a failed write hands back that command and
// everything behind it.
func TestSendBatchPaced(t *testing.T) {
	t.Parallel()
	vol := func(v int) Command { return Command{Code: "VOL", Val: v} }
	bas := Command{Code: "BAS", Val: 1}
	tre := Command{Code: "TRE", Val: 2}
	for _, tc := range []struct {
		name   string
		cmds   []Command
		failAt int
		wrote  []string
		carry  []Command
		vol    bool
	}{
		{"no volume", []Command{bas, tre}, 0, []string{"BAS:1;", "TRE:2;"}, nil, false},
		{"a volume set", []Command{bas, vol(30)}, 0, []string{"BAS:1;", "VOL:30;"}, nil, true},
		{"a volume query is no set", []Command{{Code: "VOL", Query: true}, {Code: "MUT", Val: 1}}, 0, []string{"VOL;", "MUT:1;"}, nil, false},
		{"fails at the second", []Command{vol(5), bas, tre}, 2, []string{"VOL:5;"}, []Command{bas, tre}, true},
		{"fails at the first", []Command{bas, vol(5)}, 1, nil, []Command{bas, vol(5)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &scriptConn{failAt: tc.failAt}
			carry, dead, v := sendBatchPaced(context.Background(), protocol.NewState(), c, tc.cmds)
			if !slices.Equal(c.written(), tc.wrote) || !slices.Equal(carry, tc.carry) || dead != (tc.carry != nil) || v != tc.vol {
				t.Errorf("wrote %q carry %+v dead %v vol %v; want %q %+v %v %v", c.written(), carry, dead, v, tc.wrote, tc.carry, tc.carry != nil, tc.vol)
			}
			for i := 1; i < len(c.at); i++ {
				if gap := c.at[i].Sub(c.at[i-1]); gap < tunnelSpacing/3-5*time.Millisecond {
					t.Errorf("writes %d and %d were %v apart, want ≥ %v", i-1, i, gap, tunnelSpacing/3)
				}
			}
		})
	}
}

// A command the sender drops (expired, or a code lp10 never sends) costs no
// gap: only two writes are spaced. A batch of stale sets behind a fresh one
// must not hold the sender 50ms per dropped command.
func TestSendBatchPacedSkipsTheGapForADroppedCommand(t *testing.T) {
	t.Parallel()
	old := time.Now().Add(-CommandDeadline - time.Second)
	cmds := []Command{{Code: "BAS", Val: 1, TS: time.Now()}}
	for v := range 10 {
		cmds = append(cmds, Command{Code: "TRE", Val: v, TS: old}, Command{Code: "NOPE", Val: v})
	}
	cmds = append(cmds, Command{Code: "MID", Val: 2, TS: time.Now()})
	c := &scriptConn{}
	start := time.Now()
	sendBatchPaced(context.Background(), protocol.NewState(), c, cmds)
	if took := time.Since(start); took > 2*tunnelSpacing {
		t.Errorf("the batch took %v for two writes, want one gap (%v)", took, tunnelSpacing/3)
	}
	if got := c.written(); !slices.Equal(got, []string{"BAS:1;", "MID:2;"}) {
		t.Errorf("wrote %q", got)
	}
	if gap := c.at[1].Sub(c.at[0]); gap < tunnelSpacing/3-5*time.Millisecond {
		t.Errorf("the two writes were %v apart, want ≥ %v", gap, tunnelSpacing/3)
	}
}

// drainOnStop writes what is queued at quit (coalesced) and never blocks on
// an empty queue.
func TestDrainOnStop(t *testing.T) {
	t.Parallel()
	c := &scriptConn{}
	cmds := make(chan Command, 4)
	drainOnStop(context.Background(), protocol.NewState(), c, cmds)
	if len(c.written()) != 0 {
		t.Fatalf("an empty queue wrote %q", c.written())
	}
	cmds <- Command{Code: "TRE", Val: 1}
	cmds <- Command{Code: "TRE", Val: 2}
	cmds <- Command{Code: "POP"}
	drainOnStop(context.Background(), protocol.NewState(), c, cmds)
	if got := c.written(); !slices.Equal(got, []string{"TRE:2;", "POP;"}) {
		t.Errorf("drained %q, want TRE:2; POP;", got)
	}
}

// ageCarry drops the carried sets that expired while the link was down with
// one note, keeps the rest in order, and leaves a fresh carry alone. An
// expired query is no lost intent: it stays, and tunnelSend drops it
// silently.
func TestAgeCarry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := now.Add(-CommandDeadline - time.Second)
	st := protocol.NewState()
	if got := ageCarry(st, nil, now); got != nil {
		t.Errorf("ageCarry(nil) = %+v", got)
	}
	fresh := []Command{{Code: "BAS", Val: 1, TS: now}, {Code: "TRE", Val: 2}, {Code: "MXV", Query: true, TS: old}}
	if got := ageCarry(st, fresh, now); !slices.Equal(got, fresh) || st.Snap().Error != "" {
		t.Errorf("ageCarry(fresh + an expired query) = %+v, note %q; want all kept, no note", got, st.Snap().Error)
	}
	mixed := []Command{{Code: "MXV", Val: 40, TS: old}, {Code: "BAS", Val: 1, TS: now}, {Code: "MID", Val: 2, TS: old}, {Code: "NOPE", Val: 1, TS: old}}
	if got := ageCarry(st, mixed, now); !slices.Equal(got, []Command{mixed[1], mixed[3]}) || st.Snap().Error != "command not delivered" {
		t.Errorf("ageCarry(mixed) = %+v, note %q", got, st.Snap().Error)
	}
}

// The reader parses frames split across reads, ignores what it does not know
// (RAW after a skip) without calling the link live, drops a separator-free
// flood but keeps the framing, and ends when the connection does.
func TestTunnelReader(t *testing.T) {
	t.Parallel()
	device, client := net.Pipe()
	st := protocol.NewState()
	done := make(chan struct{})
	go tunnelReader(st, client, done)
	write := func(s string) {
		t.Helper()
		if _, err := device.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	write("RAW:NEXT;")
	write("VO") // returns once the reader is back in Read, so RAW:NEXT was handled
	if s := st.Snap(); s.Connected || !st.LastRx().IsZero() {
		t.Error("an unknown frame marked the link live")
	}
	write("L:12;")
	write(strings.Repeat("x", 9000)) // past tunnelCarryMax with no ';'
	write("MUT:1;")
	eventually(t, "the split and post-flood frames", time.Second, func() bool {
		s := st.Snap()
		return s.Connected && s.Vol == 12 && s.Muted
	})
	// A read that carries a track field applies only the track fields: a
	// title can hold "VOL:100;" and the tunnel has no framing to tell.
	write("TIT:Song;VOL:100;MUT:0;")
	write("ART:Band;")
	eventually(t, "the track fields", time.Second, func() bool {
		tr := st.Snap().Track
		return tr != nil && tr.TrackName == "Song" && tr.Artist == "Band"
	})
	if s := st.Snap(); s.Vol != 12 || !s.Muted {
		t.Errorf("a track read moved the volume to %d / mute to %v, want 12 / true", s.Vol, s.Muted)
	}
	write("VOL:7;")
	write("MUT:0;")
	eventually(t, "a VOL read of its own", time.Second, func() bool { return st.Snap().Vol == 7 })
	device.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the reader did not end with the connection")
	}
}

// A panic while applying a frame ends the reader quietly (done closes, so the
// writer sees a dead link and reconnects) instead of killing the program.
func TestTunnelReaderRecovers(t *testing.T) {
	t.Parallel()
	device, client := net.Pipe()
	defer device.Close()
	done := make(chan struct{})
	go tunnelReader(nil, client, done) // a nil State panics on the first frame
	if _, err := device.Write([]byte("VOL:1;")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the reader did not end after the panic")
	}
}

// apply routes every kind of frame the box sends to its State call.
func TestApplyRoutesEveryFrame(t *testing.T) {
	t.Parallel()
	st := protocol.NewState()
	updates, rest := tunnel.ParseFrames("STA:NET,1,30,0,0,3,0,1,1,0;VOL:31;MUT:0;PLA:0;SRC:NET;VND:spotify;" +
		"TIT:Big Bang;ART:Usted Señalemelo;ALB:Big Bang;VER:29-1d316f0c-10;PEQ:0@Flat,1@Classical;BAS:-6;RAW:NEXT;")
	if rest != "" || len(updates) != 12 { // RAW:NEXT is no update at all
		t.Fatalf("ParseFrames = %d updates, rest %q", len(updates), rest)
	}
	apply(st, updates[0])
	if s := st.Snap(); s.Source != "NET" || !s.Muted || s.Vol != 30 || !s.Playing || !s.Connected {
		t.Fatalf("after STA: %+v", s)
	}
	for _, u := range updates[1:] {
		apply(st, u)
	}
	s := st.Snap()
	want := protocol.Track{TrackName: "Big Bang", Artist: "Usted Señalemelo", Album: "Big Bang", Service: "spotify"}
	if s.Vol != 31 || s.Muted || s.Playing || s.Service != "spotify" || s.Track == nil || *s.Track != want {
		t.Errorf("after the frames: %+v track %+v", s, s.Track)
	}
	if v, _ := st.EQValue("BAS"); v != -6 || st.DiagnosticView().MCU != "29-1d316f0c-10" || !slices.Equal(st.EQPresets(), []string{"Flat", "Classical"}) {
		t.Errorf("BAS %d MCU %q presets %q", v, st.DiagnosticView().MCU, st.EQPresets())
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// A dial that times out (the box is not on the LAN) reads differently from
// one that is refused (the box is up, :2018 closed).
func TestDialNote(t *testing.T) {
	t.Parallel()
	if got := dialNote(timeoutErr{}); got != "no answer on :2018" {
		t.Errorf("timeout: %q", got)
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if got := dialNote(refused); got != "cannot reach :2018: dial tcp: connection refused" {
		t.Errorf("refused: %q", got)
	}
	if a, b := time.Unix(1, 0), time.Unix(2, 0); !laterTime(a, b).Equal(b) || !laterTime(b, a).Equal(b) {
		t.Error("laterTime should pick the later time either way round")
	}
}

// Guard the brief's arithmetic: the fake-box tests budget their waits on
// these, and a change should be a conscious one.
func TestTunnelTimingConstants(t *testing.T) {
	t.Parallel()
	if StatusEvery != 2*time.Second || SilentAfter != 6*time.Second || CommandDeadline != 4*time.Second {
		t.Errorf("StatusEvery %v SilentAfter %v CommandDeadline %v", StatusEvery, SilentAfter, CommandDeadline)
	}
	if n := len(tunnel.SeedQueries()); n != len(seedFrames) {
		t.Errorf("the seed has %d queries, the tests expect %d", n, len(seedFrames))
	}
}

// TestTrackReadKeepsWhatNothingRereads: in a read that carries a track field,
// the values lp10 acts on are dropped (a title holding ";PLA:1" must not arm
// the sleep timer's toggle), but the service word and the seed's preset names
// and MCU build — which nothing reads again — are kept.
func TestTrackReadKeepsWhatNothingRereads(t *testing.T) {
	st := protocol.NewState()
	a, b := net.Pipe()
	done := make(chan struct{})
	go tunnelReader(st, a, done)
	go func() {
		b.Write([]byte("PLA:1;VOL:100;MXV:30;SRC:BT;VND:spotify;PEQ:0@Flat;VER:29-x-10;TIT:Song;"))
		b.Close()
	}()
	<-done
	s := st.Snap()
	if s.Track == nil || s.Track.TrackName != "Song" {
		t.Fatalf("track = %+v, want the title", s.Track)
	}
	if s.Playing || s.VolLive || s.Source != "" {
		t.Errorf("acted-on values applied from a track read: playing %v, vol live %v, source %q", s.Playing, s.VolLive, s.Source)
	}
	if _, ok := st.EQValue("MXV"); ok {
		t.Error("an EQ value from a track read was applied")
	}
	if s.Service != "spotify" || len(st.EQPresets()) == 0 || st.DiagnosticView().MCU != "29-x-10" {
		t.Errorf("kept frames lost: service %q, presets %v, mcu %q", s.Service, st.EQPresets(), st.DiagnosticView().MCU)
	}
}

func TestActedOn(t *testing.T) {
	for _, tc := range []struct {
		u    tunnel.Update
		want bool
	}{
		{tunnel.Update{Code: tunnel.StatusCode, Status: &tunnel.Status{}}, true},
		{tunnel.Update{Code: tunnel.VolumeCode}, true},
		{tunnel.Update{Code: tunnel.MuteCode}, true},
		{tunnel.Update{Code: tunnel.PlayCode}, true},
		{tunnel.Update{Code: tunnel.SourceCode}, true},
		{tunnel.Update{Code: "MXV"}, true},
		{tunnel.Update{Code: tunnel.VendorCode}, false},
		{tunnel.Update{Code: tunnel.VersionCode}, false},
		{tunnel.Update{Code: tunnel.PresetsCode, Names: []string{"Flat"}}, false},
		{tunnel.Update{Code: tunnel.TitleCode}, false},
	} {
		if got := actedOn(tc.u); got != tc.want {
			t.Errorf("actedOn(%s) = %v, want %v", tc.u.Code, got, tc.want)
		}
	}
}

// Close waits at most drain for the tunnel. Actions never coalesce, so a
// pasted line of play/skip keys gathers into one long batch; the send loop
// must stop when Close cancels ctx, not hold the quit 50ms per command.
func TestCloseIsBoundedByDrain(t *testing.T) {
	dev := newLiveBox()
	box := newFakeBox(t, dev.answer)
	t.Setenv("LP10_TUNNEL_ADDR", box.addr)
	disableProbes(t)
	st := protocol.NewState()
	r := StartRuntime(st, config.Config{Host: "lp10.local"})
	box.waitFrame(t, 5*time.Second, "the last seed query", is("VER"))
	eventually(t, "connected", 2*time.Second, func() bool { return st.Snap().Connected })
	time.Sleep(300 * time.Millisecond) // the write loop is up
	for range 200 {
		r.Commands <- Command{Code: "NXT", TS: time.Now()}
	}
	time.Sleep(20 * time.Millisecond) // the worker has gathered the batch
	start := time.Now()
	r.Close(DrainTimeout)
	if took := time.Since(start); took > DrainTimeout+time.Second {
		t.Fatalf("Close(%v) took %v: a gathered batch of actions held the quit past the drain budget", DrainTimeout, took)
	}
}

// A frame split by a read boundary belongs to the read it began in: when a
// track read ends inside "VOL:100;", the read that completes it is a track
// read too, so the album "Album;VOL:100" cannot set the room's volume.
func TestTrackInjectionAcrossReadBoundary(t *testing.T) {
	t.Parallel()
	device, client := net.Pipe()
	st := protocol.NewState()
	done := make(chan struct{})
	go tunnelReader(st, client, done)
	write := func(s string) {
		if _, err := device.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	write("VOL:12;")
	write("TIT:Song;ART:Band;ALB:Album;VO")
	write("L:100;")
	write("PLA:0;") // a read of its own, returned once the reader is back in Read
	device.Close()
	<-done
	if v := st.Snap().Vol; v != 12 {
		t.Fatalf("vol = %d: a VOL frame begun in a track read was applied", v)
	}
}
