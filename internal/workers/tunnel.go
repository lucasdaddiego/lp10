package workers

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

const (
	tunnelDialTimeout = 3 * time.Second
	// tunnelSeedSpacing paces the on-connect queries: the device only answers a
	// query reliably when they aren't sent back-to-back in one burst.
	//
	// The same goes for connections. tcptunnelling serialises its clients: a
	// burst of one-shot connect/query/close cycles (measured 2026-09-02 on
	// MCU 23: 26 in a row) left the NEXT connect hanging past 8 s, and it only
	// answered again once the backlog drained. This worker therefore holds ONE
	// connection for its whole life, seeds over it, and reconnects only through
	// waitBackoff — never a tight retry, never a second parallel socket.
	tunnelSeedSpacing = 150 * time.Millisecond
	tunnelPoll        = 250 * time.Millisecond // write-loop wake to re-check Stop
	tunnelCarryMax    = 8192                   // bound a separator-free read
	// EQCommandDeadline prevents a control change made while the tunnel is down
	// from applying minutes later after a reconnect. It mirrors the player
	// command deadline: brief Wi-Fi blips recover transparently; stale intent is
	// dropped visibly.
	EQCommandDeadline = 4 * time.Second
	// TCP keepalive on the control socket. The device only broadcasts on change,
	// so the tunnel is normally silent — a read deadline would false-fire. OS
	// keepalive instead probes a half-open link (flaky-WiFi: device off the LAN,
	// local socket still ESTABLISHED) and tears it down in ~Idle+Count*Interval
	// (~25s) so eqConnected drops and we reconnect, instead of the ~10min default
	// while EQ writes vanish behind a false "connected" indicator.
	tunnelKeepIdle  = 10 * time.Second
	tunnelKeepIntvl = 5 * time.Second
	tunnelKeepCount = 3
)

// EQCommand is a queued control write for the :2018 tunnel: a wire code, value,
// and enqueue time. The worker validates the code, clamps the value, and drops
// stale intent at the wire boundary. Query asks the device to re-broadcast the
// control's current value instead of setting one (the seed-loss self-heal).
type EQCommand struct {
	Code  string
	Val   int
	Query bool
	TS    time.Time
}

// tunnelAddr resolves the control-tunnel address; LP10_TUNNEL_ADDR overrides it
// for tests (mirroring LP10_SSH for the ssh transport).
func tunnelAddr(cfg config.Config) string {
	if a := os.Getenv("LP10_TUNNEL_ADDR"); a != "" {
		return a
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(tunnel.Port))
}

// tunnelWorker maintains the device's plain-text control connection (:2018): it
// reconnects with backoff, seeds current values on connect, applies the device's
// broadcasts to State, and writes queued EQ commands. It never dies — a dead
// tunnel only disables the EQ panel; the ssh player stream is unaffected.
func tunnelWorker(ctx context.Context, control *runControl, st *protocol.State, cfg config.Config, eqcmds <-chan EQCommand) {
	backoff := InitialBackoff
	var carry *EQCommand // a command whose write failed, retried on the next connection
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
			backoff, carry = tunnelOnceContext(ctx, control, st, cfg, eqcmds, backoff, carry)
		}()
	}
}

// tunnelOnce is one connection lifecycle, returning the next reconnect backoff.
func tunnelOnce(control *runControl, st *protocol.State, cfg config.Config, eqcmds <-chan EQCommand, backoff time.Duration) time.Duration {
	next, _ := tunnelOnceContext(context.Background(), control, st, cfg, eqcmds, backoff, nil)
	return next
}

func tunnelOnceContext(ctx context.Context, control *runControl, st *protocol.State, cfg config.Config, eqcmds <-chan EQCommand, backoff time.Duration, carry *EQCommand) (time.Duration, *EQCommand) {
	// A command carried from a dead connection ages like any queued one; while
	// the tunnel stays down, expired intent is dropped visibly here rather than
	// applying minutes later on a reconnect.
	if carry != nil {
		if _, stale := eqCommandWire(*carry, time.Now()); stale {
			st.Note("command not delivered")
			carry = nil
		}
	}
	dialer := net.Dialer{
		Timeout: tunnelDialTimeout,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     tunnelKeepIdle,
			Interval: tunnelKeepIntvl,
			Count:    tunnelKeepCount,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", tunnelAddr(cfg))
	if err != nil {
		st.SetEQConnected(false)
		if ctx.Err() != nil {
			return backoff, carry
		}
		return waitBackoff(control, backoff), carry
	}
	st.SetEQConnected(true)

	done := make(chan struct{})
	go tunnelReader(st, conn, done)
	// closeConn tears the connection down exactly once. It runs inline before
	// the backoff wait (the dead conn must not linger, nor the panel read
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
		st.SetEQConnected(false)
	}
	defer closeConn()

	// Seed: query each control so the panel shows the device's current values.
	dead := false
	for _, q := range tunnel.SeedQueries() {
		if control.stop.IsSet() || ctx.Err() != nil {
			break
		}
		if _, werr := conn.Write([]byte(q)); werr != nil {
			dead = true
			break
		}
		control.stop.Wait(tunnelSeedSpacing)
	}
	if !dead {
		select {
		case <-done:
			dead = true // peer died while the seed writes were in flight
		default:
			// Completing the seed with a live reader proves this was more than
			// an accept-and-immediately-reset endpoint, so it breaks the failure
			// streak. Without this reset, later disconnects inherited an old
			// MaxBackoff even after long healthy sessions.
			backoff = InitialBackoff
		}
	}

	// A carried command gets first claim on the fresh connection, ahead of the
	// queue it was consumed from.
	if !dead && carry != nil {
		cmd := *carry
		carry, dead = tunnelSend(st, conn, cmd)
	}

	// Write loop: drain queued commands until the connection dies or we stop. One
	// ticker (not a fresh time.After each iteration) gives the periodic wake that
	// lets us notice Stop with no commands in flight, without churning timers.
	poll := time.NewTicker(tunnelPoll)
	defer poll.Stop()
	for !control.stop.IsSet() && ctx.Err() == nil && !dead {
		select {
		case <-done:
			dead = true
		case <-ctx.Done():
		case cmd, ok := <-eqcmds:
			if !ok {
				eqcmds = nil // disable this select arm; a closed channel is always ready
				continue
			}
			carry, dead = tunnelSend(st, conn, cmd)
		case <-poll.C:
		}
	}

	closeConn()

	if control.stop.IsSet() || ctx.Err() != nil {
		return backoff, carry
	}
	return waitBackoff(control, backoff), carry
}

// tunnelSend validates, ages, and writes one EQ command. A write failure hands
// the command back for the next connection (the EQ analog of the player queue's
// pending carry-over) instead of silently dropping user intent.
func tunnelSend(st *protocol.State, conn net.Conn, cmd EQCommand) (carry *EQCommand, dead bool) {
	wire, stale := eqCommandWire(cmd, time.Now())
	if stale {
		st.Note("command not delivered")
		return nil, false
	}
	if wire == "" { // unknown code: never put an unallowlisted frame on the wire
		return nil, false
	}
	if _, err := conn.Write([]byte(wire)); err != nil {
		return &cmd, true
	}
	return nil, false
}

// eqCommandWire validates and ages one queued EQ command. A zero timestamp is
// accepted for internal callers/tests; the TUI timestamps every real user
// action. stale is distinct from an unknown code so only expired user intent
// produces the visible "not delivered" note.
func eqCommandWire(cmd EQCommand, now time.Time) (wire string, stale bool) {
	if _, ok := tunnel.Lookup(cmd.Code); !ok {
		return "", false
	}
	if !cmd.TS.IsZero() && now.Sub(cmd.TS) > EQCommandDeadline {
		return "", !cmd.Query // an expired refresh isn't lost user intent: drop silently
	}
	if cmd.Query {
		return tunnel.Query(cmd.Code), false
	}
	return tunnel.Set(cmd.Code, cmd.Val), false
}

// tunnelReader parses the device's broadcast frames into State until the
// connection fails (which the writer triggers on Stop by closing conn).
func tunnelReader(st *protocol.State, conn net.Conn, done chan struct{}) {
	defer close(done)
	defer func() { recover() }()
	buf := make([]byte, 4096)
	var carry string
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			updates, rest := tunnel.ParseFrames(carry + string(buf[:n]))
			if len(rest) > tunnelCarryMax {
				rest = "" // separator-free flood: drop, keep framing
			}
			carry = rest
			for _, u := range updates {
				if u.Names != nil {
					st.SetEQPresets(u.Names)
					continue
				}
				st.ApplyTunnel(u.Code, u.Val)
			}
		}
		if err != nil {
			return
		}
	}
}

// waitBackoff sleeps the current backoff (interruptible by Stop) and returns the
// doubled-and-capped next value, mirroring streamOnce's cadence.
func waitBackoff(control *runControl, backoff time.Duration) time.Duration {
	if !control.stop.Wait(backoff) {
		backoff *= 2
		if backoff > MaxBackoff {
			backoff = MaxBackoff
		}
	}
	return backoff
}
