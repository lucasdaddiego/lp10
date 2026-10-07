package workers

import (
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/debuglog"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

const (
	tunnelDialTimeout = 3 * time.Second
	// tunnelSpacing paces the queries and writes: the device only answers a
	// query reliably when they aren't sent back-to-back in one burst.
	//
	// The same goes for connections. tcptunnelling serialises its clients: a
	// burst of one-shot connect/query/close cycles (measured 2026-09-02 on
	// MCU 23: 26 in a row) left the NEXT connect hanging past 8 s, and it only
	// answered again once the backlog drained. This worker therefore holds ONE
	// connection for its whole life, seeds over it, and reconnects only through
	// waitBackoff — never a tight retry, never a second parallel socket.
	tunnelSpacing  = 150 * time.Millisecond
	tunnelPoll     = 250 * time.Millisecond // write-loop wake to re-check Stop and the deadlines
	tunnelCarryMax = 8192                   // bound a separator-free read

	// StatusEvery is the player poll: one STA query, whose reply carries the
	// source, the mute, the volume and the play state. The device pushes the
	// play state, the track, and a volume set from the Spotify app on a
	// change (verified 2026-10-01); the knob, the remote and the mute were
	// not checked — the poll is what catches whatever is not pushed.
	StatusEvery = 2 * time.Second
	// SilentAfter ends a connection that has delivered no frame for this long,
	// counted from the dial or the latest frame. The poll gets an answer every
	// StatusEvery from a live box, so silence this long is a dead link — or the
	// accepted-but-never-served connection tcptunnelling sometimes hands out
	// (seen live 2026-10-01: the next connection answered at once).
	SilentAfter = 6 * time.Second

	// CommandDeadline prevents a command made while the tunnel is down from
	// applying minutes later after a reconnect: brief blips recover
	// transparently; stale intent is dropped visibly.
	CommandDeadline = 4 * time.Second

	// volumePace is the least time between two volume writes: a held key's
	// repeats pile up in the queue meanwhile, and the next batch writes only
	// the newest level (coalesce keeps the last set of each code — the levels
	// are absolute, so the ones between are superseded).
	volumePace = 150 * time.Millisecond

	// TCP keepalive on the socket. The poll's silence deadline catches a dead
	// box; keepalive additionally tears down a half-open link the OS would
	// otherwise hold for ~10 minutes.
	tunnelKeepIdle  = 10 * time.Second
	tunnelKeepIntvl = 5 * time.Second
	tunnelKeepCount = 3
)

// Command is a queued write for the tunnel: a wire code, a value, and the
// enqueue time. Query asks the device to re-send a value instead of setting
// one (the EQ seed-loss self-heal). The worker allowlists the code
// (tunnel.Wire), clamps the value, and drops stale intent at the wire
// boundary.
type Command struct {
	Code  string
	Val   int
	Query bool
	TS    time.Time
}

// tunnelAddr resolves the tunnel address; LP10_TUNNEL_ADDR overrides it for
// tests.
func tunnelAddr(cfg config.Config) string {
	if a := os.Getenv("LP10_TUNNEL_ADDR"); a != "" {
		return a
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(tunnel.Port))
}

// tunnelWorker maintains the device's control connection (:2018): it
// reconnects with backoff, seeds current values on connect, polls the player
// status, applies the device's frames to State, and writes queued commands.
// It never dies.
func tunnelWorker(ctx context.Context, control *runControl, st *protocol.State, cfg config.Config, cmds <-chan Command, snapshotPath string) {
	defer control.drained.Set() // whatever ends the worker ends the drain wait too
	backoff := InitialBackoff
	var carry []Command // commands whose write failed (and those queued behind it), retried on the next connection
	for !control.stop.IsSet() && ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					st.Note(fmt.Sprintf("tunnel worker: %v", r))
					// The panic pre-empted the carry hand-back, so its delivery
					// state is unknown: drop it rather than risk re-applying a
					// possibly-delivered (now stale) value next connection.
					carry = nil
					control.stop.Wait(time.Second)
				}
			}()
			backoff, carry = tunnelOnceContext(ctx, control, st, cfg, cmds, backoff, carry, snapshotPath)
		}()
	}
}

// tunnelOnceContext is one connection lifecycle, returning the next reconnect
// backoff and the commands to carry into the next connection.
func tunnelOnceContext(ctx context.Context, control *runControl, st *protocol.State, cfg config.Config, cmds <-chan Command, backoff time.Duration, carry []Command, snapshotPath string) (time.Duration, []Command) {
	carry = ageCarry(st, carry, time.Now())
	dialer := net.Dialer{
		Timeout: tunnelDialTimeout,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     tunnelKeepIdle,
			Interval: tunnelKeepIntvl,
			Count:    tunnelKeepCount,
		},
	}
	st.StartConnection()
	conn, err := dialer.DialContext(ctx, "tcp", tunnelAddr(cfg))
	if err != nil {
		st.Disconnect()
		if ctx.Err() != nil {
			return backoff, carry
		}
		st.Note(dialNote(err))
		return waitBackoff(control, backoff), carry
	}
	dialed := time.Now()
	// Not "connected" yet: a dial that succeeds proves only that something
	// accepted. The first parsed frame — the seed replies land within
	// milliseconds on a live link — is what marks the link live (Received).

	// LP10_DEBUG: every chunk either way goes to the frame log as well,
	// opened per connection (append) so the file follows a reconnect.
	frames := debuglog.Open()
	defer frames.Close()
	conn = debuglog.Wrap(conn, frames)

	done := make(chan struct{})
	go tunnelReader(st, conn, done)
	// closeConn tears the connection down exactly once. It runs inline before
	// the backoff wait (the dead conn must not linger, nor the screen read
	// "connected", through a wait), and is ALSO deferred: the enclosing worker
	// recovers and reconnects, so a panic anywhere in this lifecycle would
	// otherwise leak the conn and its reader goroutine for the process's life.
	closed := false
	closeConn := func() {
		if closed {
			return
		}
		closed = true
		conn.Close()
		<-done // reader exits once the closed conn fails its Read
		st.Disconnect()
	}
	defer closeConn()

	// Seed: the player status, every control, the preset names, the MCU build.
	dead := false
	for _, q := range tunnel.SeedQueries() {
		if control.stop.IsSet() || ctx.Err() != nil {
			break
		}
		if _, werr := conn.Write([]byte(q)); werr != nil {
			dead = true
			break
		}
		control.stop.Wait(tunnelSpacing)
	}

	// Carried commands get first claim on the fresh connection, ahead of the
	// queue they were consumed from.
	if !dead && len(carry) > 0 {
		carry, dead = sendBatch(ctx, st, conn, coalesce(carry))
	}

	// Write loop: drain queued commands, poll the status, and watch for
	// silence, until the connection dies or we stop. One ticker (not a fresh
	// time.After each iteration) gives the periodic wake.
	poll := time.NewTicker(tunnelPoll)
	defer poll.Stop()
	lastStatus, lastPersist := time.Now(), time.Now()
	healthy := false // a frame arrived on this connection
	for !control.stop.IsSet() && ctx.Err() == nil && !dead {
		select {
		case <-done:
			dead = true
		case <-ctx.Done():
		case cmd := <-cmds: // never closed: Runtime.Close stops the worker via ctx/stop
			batch := gather(cmd, cmds)
			var vol bool
			carry, dead, vol = sendBatchPaced(ctx, st, conn, coalesce(batch))
			if vol {
				control.stop.Wait(volumePace)
			}
		case now := <-poll.C:
			last := st.LastRx()
			if !last.IsZero() && !healthy {
				// A live link: the failure streak is over.
				healthy, backoff = true, InitialBackoff
			}
			if now.Sub(laterTime(dialed, last)) > SilentAfter {
				st.Note("the box stopped answering on :2018")
				dead = true
				break
			}
			if v, ok := st.TakeVolumeBridge(); ok {
				// the level the device reports but may not be playing at (see
				// protocol.State.TakeVolumeBridge). Not carried when the write
				// fails: the next connection's first reading bridges anew.
				if _, dead, _ = sendBatchPaced(ctx, st, conn, []Command{{Code: tunnel.VolumeCode, Val: v}}); dead {
					break
				}
			}
			if now.Sub(lastStatus) >= StatusEvery {
				if _, werr := conn.Write([]byte(tunnel.Query(tunnel.StatusCode))); werr != nil {
					dead = true
					break
				}
				lastStatus = now
			}
			if healthy && snapshotPath != "" && now.Sub(lastPersist) >= SnapshotPersistInterval {
				config.SaveSnapshot(snapshotPath, selfSnap(st))
				lastPersist = now
			}
		}
	}

	if control.stop.IsSet() {
		// Quit: write what was queued before it (a pause pressed just before q
		// still reaches the box), then let Close go on.
		if !dead {
			drainOnStop(ctx, st, conn, cmds)
		}
		control.drained.Set()
	}
	closeConn()

	if control.stop.IsSet() || ctx.Err() != nil {
		return backoff, carry
	}
	return waitBackoff(control, backoff), carry
}

// dialNote words a failed dial for the connecting screen: a refusal (the box
// is up but nothing listens on :2018) reads differently from a timeout (the
// box is not on the LAN at all).
func dialNote(err error) string {
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return "no answer on :2018"
	}
	return fmt.Sprintf("cannot reach :2018: %v", err)
}

func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ageCarry drops the carried commands that expired while the tunnel was down,
// noting the loss once: intent this old would apply minutes late.
func ageCarry(st *protocol.State, carry []Command, now time.Time) []Command {
	if len(carry) == 0 {
		return carry
	}
	fresh := carry[:0:0]
	for _, c := range carry {
		if _, stale := commandWire(c, now); !stale {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) < len(carry) {
		st.Note("command not delivered")
	}
	return fresh
}

// gather takes first plus everything already queued behind it.
func gather(first Command, cmds <-chan Command) []Command {
	batch := []Command{first}
	for {
		select {
		case c := <-cmds:
			batch = append(batch, c)
		default:
			return batch
		}
	}
}

// drainOnStop writes what is queued at quit, so a key pressed just before q is
// not lost. It never blocks: what is not already queued stays unsent.
func drainOnStop(ctx context.Context, st *protocol.State, conn net.Conn, cmds <-chan Command) {
	select {
	case c := <-cmds:
		sendBatch(ctx, st, conn, coalesce(gather(c, cmds)))
	default:
	}
}

// coalesce keeps, of several sets queued for one code, only the last — at the
// last one's position, so relative order across codes holds. A held key
// queues a set per key repeat; the device needs the final value, and every
// intermediate write came back as a broadcast echo the screen then had to
// hold off. Queries and actions pass through untouched: two play/pause
// presses are two toggles.
func coalesce(cmds []Command) []Command {
	if len(cmds) < 2 {
		return cmds
	}
	last := map[string]int{}
	for i, c := range cmds {
		if !c.Query && !tunnel.IsAction(c.Code) {
			last[c.Code] = i
		}
	}
	out := cmds[:0:0]
	for i, c := range cmds {
		if c.Query || tunnel.IsAction(c.Code) || last[c.Code] == i {
			out = append(out, c)
		}
	}
	return out
}

// sendBatch writes cmds in order. On a dead connection it hands back the
// command that failed and everything queued behind it, for the next
// connection.
func sendBatch(ctx context.Context, st *protocol.State, conn net.Conn, cmds []Command) (carry []Command, dead bool) {
	carry, dead, _ = sendBatchPaced(ctx, st, conn, cmds)
	return carry, dead
}

// sendBatchPaced is sendBatch that also reports whether a volume set went
// out, so the caller can pace the next batch (volumePace). It stops when ctx
// ends and hands the rest back unsent: Runtime.Close cancels ctx when its drain
// window is over, and a pasted line of play/skip keys (actions never coalesce)
// would otherwise hold the quit 50ms per command.
func sendBatchPaced(ctx context.Context, st *protocol.State, conn net.Conn, cmds []Command) (carry []Command, dead, vol bool) {
	wrote := false
	for i, c := range cmds {
		if ctx.Err() != nil {
			return cmds[i:], false, vol
		}
		// A short gap between two writes: back-to-back frames can be dropped.
		// A command tunnelSend will drop (expired or refused) gets none.
		wire, stale := commandWire(c, time.Now())
		writes := wire != "" && !stale
		if wrote && writes {
			time.Sleep(tunnelSpacing / 3)
		}
		failed, d := tunnelSend(st, conn, c)
		if d {
			return append([]Command{*failed}, cmds[i+1:]...), true, vol
		}
		wrote = wrote || writes
		vol = vol || (c.Code == tunnel.VolumeCode && !c.Query)
	}
	return nil, false, vol
}

// tunnelSend validates, ages, and writes one command. A write failure hands
// the command back for the next connection instead of silently dropping user
// intent.
func tunnelSend(st *protocol.State, conn net.Conn, cmd Command) (carry *Command, dead bool) {
	wire, stale := commandWire(cmd, time.Now())
	if stale {
		st.Note("command not delivered")
		return nil, false
	}
	if wire == "" { // not allowlisted: never put it on the wire
		return nil, false
	}
	if _, err := conn.Write([]byte(wire)); err != nil {
		return &cmd, true
	}
	return nil, false
}

// commandWire validates and ages one queued command. A zero timestamp is
// accepted for internal callers/tests; the TUI timestamps every real user
// action. stale is distinct from a refused code so only expired user intent
// produces the visible "not delivered" note.
func commandWire(cmd Command, now time.Time) (wire string, stale bool) {
	wire, ok := tunnel.Wire(cmd.Code, cmd.Val, cmd.Query)
	if !ok {
		return "", false
	}
	if !cmd.TS.IsZero() && now.Sub(cmd.TS) > CommandDeadline {
		return "", !cmd.Query // an expired refresh isn't lost user intent: drop silently
	}
	return wire, false
}

// tunnelReader parses the device's frames into State until the connection
// fails (which the writer triggers on Stop by closing conn).
//
// A read that carries a track field (TIT, ART, ALB) drops the frames lp10
// acts on: the volume, mute, play state, status, source and EQ values. The
// tunnel has no framing, so a title holding ";VOL:100" would parse as a real
// VOL frame — and the volume bridge would send that level to the box (an
// injected PLA could fire the sleep timer's toggle, an injected EQ value
// would seed the next relative step). Those values come back with the next
// status poll, or are the device's echo of lp10's own sets; what nothing
// re-reads — the VND service word, the PEQ preset names and the VER build of
// the seed — is kept, display-only.
func tunnelReader(st *protocol.State, conn net.Conn, done chan struct{}) {
	defer close(done)
	defer func() { recover() }()
	buf := make([]byte, 4096)
	var carry string
	carryTrack := false // the partial frame in carry began in a track read
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			updates, rest := tunnel.ParseFrames(carry + string(buf[:n]))
			if len(rest) > tunnelCarryMax {
				rest = "" // separator-free flood: drop, keep framing
			}
			carry = rest
			// a frame split by the read boundary belongs to the read it began in:
			// "ALB:Album;VO" + "L:100;" must not apply VOL:100 on the second read
			trackRead := carryTrack || slices.ContainsFunc(updates, isTrackField)
			carryTrack = trackRead && rest != ""
			for _, u := range updates {
				if trackRead && actedOn(u) {
					continue
				}
				apply(st, u)
			}
		}
		if err != nil {
			return
		}
	}
}

// isTrackField reports whether u is one of the track fields the device
// pushes on a track change — text the outside world (a track's metadata)
// controls.
func isTrackField(u tunnel.Update) bool {
	switch u.Code {
	case tunnel.TitleCode, tunnel.ArtistCode, tunnel.AlbumCode:
		return true
	}
	return false
}

// actedOn reports whether lp10 acts on u's value — the bridge, the sleep
// timer, a relative EQ step — so a read that may carry an injected frame must
// not apply it (see tunnelReader).
func actedOn(u tunnel.Update) bool {
	if u.Status != nil {
		return true
	}
	switch u.Code {
	case tunnel.VolumeCode, tunnel.MuteCode, tunnel.PlayCode, tunnel.SourceCode:
		return true
	}
	_, eq := tunnel.Lookup(u.Code)
	return eq
}

// apply records one parsed frame in State. Any recognised frame proves the
// link live.
func apply(st *protocol.State, u tunnel.Update) {
	st.Received()
	switch {
	case u.Names != nil:
		st.SetEQPresets(u.Names)
	case u.Status != nil:
		st.ApplyStatus(u.Status.Source, u.Status.Muted, u.Status.Vol, u.Status.Playing)
	case u.Code == tunnel.VolumeCode:
		st.ApplyVolume(u.Val)
	case u.Code == tunnel.MuteCode:
		st.ApplyMute(u.Val == 1)
	case u.Code == tunnel.PlayCode:
		st.ApplyPlaying(u.Val == 1)
	case u.Code == tunnel.SourceCode:
		st.ApplySource(u.Text)
	case u.Code == tunnel.TitleCode, u.Code == tunnel.ArtistCode, u.Code == tunnel.AlbumCode:
		st.ApplyTrackField(u.Code, u.Text)
	case u.Code == tunnel.VendorCode:
		st.ApplyVendor(u.Text)
	case u.Code == tunnel.VersionCode:
		st.ApplyVersion(u.Text)
	default:
		st.ApplyTunnel(u.Code, u.Val) // an EQ control: ParseFrames passes only known codes
	}
}
