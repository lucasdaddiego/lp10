// Package workers owns the background runtime: child processes, shutdown and
// drain coordination, snapshot persistence, stream reconnects, command writes,
// watchdogs, the EQ tunnel, artwork loading, and the ssh-free probes (LSSDP,
// Spotify ZeroConf, the on-demand OTA check).
package workers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/transport"
)

const (
	maxLine            = 65536 // bound a line so a newline-free stream can't grow
	InitialBackoff     = 250 * time.Millisecond
	MaxBackoff         = 3 * time.Second
	CommandGetTimeout  = 500 * time.Millisecond
	CommandDeadline    = 4 * time.Second
	LiveSessionTimeout = 8 * time.Second
	ConnectWindow      = 20 * time.Second
	SilentAfter        = 8 * time.Second
	DatalessAfter      = 30 * time.Second
	DrainTimeout       = 1 * time.Second

	// SnapshotPersistInterval bounds how often the instant-first-paint snapshot
	// is rewritten mid-session. Its consumer only seeds the next launch's first
	// frame, and Teardown persists a final fresh copy on quit, so a generous
	// interval is fine — 2s here meant an atomic create+rename pair every 2s
	// for the whole session (~1800 writes/hour) for no visible benefit.
	SnapshotPersistInterval = 30 * time.Second
)

// fatalEscalateAfter is the run of consecutive fatal verdicts after which the
// retry cadence is stretched (×6, capped at a minute). The device's sshd
// lockout after rapid reconnects presents as "Permission denied" exactly like
// a wrong password, and every 10 s retry is one more failed auth feeding it —
// while a genuinely wrong password never fixes itself in 10 s either.
const fatalEscalateAfter = 3

// classify maps residual ssh stderr to a fatal/transient verdict. It is a var
// so tests can shorten the fatal retry cadence.
var classify = transport.ClassifyStderr

// backoffResetAfter is how long a session must have been DELIVERING (measured
// from its first data record, not its spawn) before a data record resets the
// reconnect backoff. Resetting on the first record let a session that dies
// right after one tick reconnect at InitialBackoff forever — sustained ssh
// churn the device's sshd punishes with a lockout — and spawn-relative aging
// would readmit that churn whenever the handshake itself ate the window. (The
// tunnel's analog: a completed seed with a live reader.) A var so tests can
// shorten it.
var backoffResetAfter = 8 * time.Second

// Lockout insurance. The box's sshd refuses password auth for minutes once
// logins come too fast (roughly the fifth within a few minutes), and the
// reconnect backoff cannot see a stall that recurs every session: a session
// that delivered for backoffResetAfter resets it, and the watchdog ends the
// stall SilentAfter later, so a stall like the DNS-dead loop's paced a fresh
// login every ~15 s. stallStreak holds the next spawn off instead. Vars so
// tests can shorten them.
var (
	// stallHold is the least wait before the next spawn once two stalls come
	// in a row; each further stall doubles it, up to stallHoldMax.
	stallHold    = 30 * time.Second
	stallHoldMax = 2 * time.Minute
	// stallResetAfter is how long a session must deliver to end a streak: one
	// stall in a long healthy session is not a pattern, and reconnects at once.
	stallResetAfter = 2 * time.Minute
)

// stallStreak counts the logins in a row that ended before they had delivered
// player data for stallResetAfter — however they ended: the watchdog killing a
// stalled or wedged session, or the loop exiting on its own right after the
// login, as a loop that dies at startup does every session. A record of any
// kind proves the login, not only player data (a loop whose LUCI reads wedge
// sends empty frames until the watchdog's dataless kill), and so does a
// watchdog kill before the first record: ssh's own ConnectTimeout and a refused
// password end a session long before the connect window, so a session still
// there to be killed had connected, and its loop printed nothing. The first
// short login reconnects on the normal backoff — the user is watching the
// player — and each one after it holds the next spawn for at least stallHold,
// doubling up to stallHoldMax. A session that delivers for stallResetAfter ends
// the streak however it ends, so a long session lost once is not a streak. A
// refused login or a spawn failure leaves the streak as it is: no login
// happened, and only a long session proves the stall gone.
type stallStreak struct {
	n    int           // short stalls in a row
	hold time.Duration // the hold the latest of them earned
}

// after records how a session ended — whether it had logged in, and how long
// it delivered player data — and returns the least wait before the next spawn,
// or 0 when the backoff alone applies.
func (s *stallStreak) after(loggedIn bool, delivered time.Duration) time.Duration {
	switch {
	case delivered >= stallResetAfter:
		*s = stallStreak{}
		return 0
	case !loggedIn:
		return 0
	}
	s.n++
	switch s.n {
	case 1:
		return 0
	case 2:
		s.hold = stallHold
	default:
		s.hold = min(2*s.hold, stallHoldMax)
	}
	return s.hold
}

// stallNote says why the next spawn holds: the stalls in a row, and the wait.
func stallNote(n int, wait time.Duration) string {
	secs := int((wait + time.Second - 1) / time.Second) // whole seconds, rounded up
	return fmt.Sprintf("%d short sessions in a row · waiting %d s — the box's sshd locks out rapid logins", n, secs)
}

// boundedLines yields lines from r, each at most maxLine bytes: a line ends at
// '\n' or once the cap is hit. Returns ("", false) at EOF with nothing
// buffered. ReadSlice serves a whole line from bufio's buffer in one call (vs
// a byte at a time); the buffer is sized to maxLine so a newline-free run
// comes back in maxLine chunks via ErrBufferFull, bounding memory. Only the
// FIRST chunk of an over-long line is yielded (it holds the true line start);
// the continuations are discarded, so an arbitrary 64KB byte boundary can
// never fabricate a line start — i.e. a `@@` landing there can't open a fake
// record section. string(line) copies out of the buffer, so the lines
// IterRecords retains stay valid across later reads.
func boundedLines(r io.Reader) func() (string, bool) {
	br := bufio.NewReaderSize(r, maxLine)
	midLine := false // last yield ended at the cap, not at '\n'
	return func() (string, bool) {
		for {
			// The error needs no inspection beyond ErrBufferFull: ReadSlice
			// returns nil error only when the delimiter was found, so an empty
			// slice always means EOF/failure.
			line, err := br.ReadSlice('\n')
			if len(line) == 0 {
				return "", false
			}
			capped := err == bufio.ErrBufferFull
			if midLine {
				midLine = capped
				continue // still inside the over-long line: drop
			}
			midLine = capped
			return string(line), true
		}
	}
}

// fence runs fn under a recover that notes the panic and holds the worker a
// second, so a deterministic failure cannot spin. The stream worker runs every
// connection under it, and so does each worker that parses what the LAN sends
// (the ZeroConf and LSSDP probes, the cover art): one parser bug there must
// cost a noted error, not the program — a panic that escapes a worker
// goroutine kills the process with the terminal still in raw mode.
func fence(st *protocol.State, control *runControl, name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			st.Note(fmt.Sprintf("%s: %v", name, r))
			control.stop.Wait(time.Second)
		}
	}()
	fn()
}

// streamWorker is the reconnect loop; it never dies. Each spawn first resolves
// the diagnostics ping target on the laptop (transport.PingTarget): the loop
// only ever pings an address, and a failed lookup keeps the last good one. The
// backoff and the stall streak carry from one session to the next.
func streamWorker(ctx context.Context, st *protocol.State, cfg config.Config, snapshotPath string, procs *processSlot, control *runControl) {
	backoff := InitialBackoff
	var stalls stallStreak
	pingIP := ""
	for !control.stop.IsSet() {
		fence(st, control, "stream worker", func() {
			pingIP = transport.PingTarget(ctx, cfg.PingHost, pingIP)
			backoff = streamOnceWithSnapshot(st, cfg, pingIP, backoff, &stalls, snapshotPath, procs, control)
		})
	}
}

// streamOnceWithSnapshot is one connection lifecycle, returning the next
// reconnect backoff; it records the session's end in stalls. pingIP is the
// loop's internet-ping target (an IPv4, or "" for none). stderr goes to a temp
// file, not a pipe, so ssh can never block on a full stderr buffer; the
// residual is read post-mortem.
func streamOnceWithSnapshot(st *protocol.State, cfg config.Config, pingIP string, backoff time.Duration, stalls *stallStreak, snapshotPath string, procs *processSlot, control *runControl) time.Duration {
	// failStart notes a spawn failure, releases whatever pipes exist so far,
	// and holds the retry cadence — the shared tail of every pre-launch error.
	failStart := func(err error, closers ...io.Closer) time.Duration {
		for _, c := range closers {
			c.Close()
		}
		st.Note(fmt.Sprintf("cannot start ssh: %v", err))
		control.stop.Wait(3 * time.Second)
		return backoff
	}

	errf, err := os.CreateTemp("", "lp10-ssh-stderr-*")
	if err != nil {
		return failStart(err)
	}
	defer func() {
		errf.Close()
		os.Remove(errf.Name())
	}()

	argv := append(transport.SSHArgv(cfg), transport.RemoteLoop(pingIP))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = transport.SpawnEnv()
	cmd.Stderr = errf
	// New session: detach ssh from the controlling terminal so it can never
	// open /dev/tty for a password prompt. SSH_ASKPASS_REQUIRE=force achieves
	// that on OpenSSH ≥ 8.4 (2020), but an older ssh ignores the variable and
	// would prompt invisibly under the alt screen — hanging every attempt with
	// a transient-looking stderr. Teardown signalling is direct-to-process
	// (Signal/Kill on the Cmd), so no group semantics change.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Manual pipes (not Std*Pipe): keep a single owner of Cmd.Wait() and keep
	// reads safe from Wait closing the pipe out from under them.
	inR, inW, err := os.Pipe()
	if err != nil {
		return failStart(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return failStart(err, inR, inW)
	}
	cmd.Stdin = inR
	cmd.Stdout = outW

	if err := cmd.Start(); err != nil {
		return failStart(err, inR, inW, outR, outW)
	}
	// Parent drops the child's ends so EOF semantics work both ways.
	inR.Close()
	outW.Close()

	proc := &process{Cmd: cmd, Stdin: inW, Stdout: outR, Done: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(proc.Done)
	}()
	procs.start(st, proc)

	// The read loop runs under a deferred reap: every call inside it is
	// panic-fenced today, but the enclosing worker recovers and RESPAWNS — an
	// unfenced panic here with a plain post-loop reap would orphan the live ssh
	// child (holding one of the device's scarce sshd slots) for the rest of the
	// process. The defer keeps the reap-before-stderr-read ordering: the child
	// must be dead before the residual is read as complete. What the session
	// delivered outlives it: the stall streak below reads it.
	loggedIn := false                 // a record arrived: ssh logged in and the loop ran
	var firstData, lastData time.Time // the session's first and latest player data
	func() {
		defer reap(st, procs, proc)
		if control.stop.IsSet() {
			return
		}
		var lastPersist time.Time
		nextLine := boundedLines(outR)
		for rec := range protocol.IterRecords(nextLine) {
			if control.stop.IsSet() {
				break
			}
			loggedIn = true
			hadData, ok := applyRecordSafe(st, rec)
			if !ok || !hadData {
				continue
			}
			now := time.Now()
			if firstData.IsZero() {
				firstData = now
			}
			lastData = now
			if now.Sub(firstData) >= backoffResetAfter {
				backoff = InitialBackoff
			}
			st.ClearFatalOnData()
			if snapshotPath != "" && now.Sub(lastPersist) > SnapshotPersistInterval && !control.stop.IsSet() {
				config.SaveSnapshot(snapshotPath, selfSnap(st))
				lastPersist = now
			}
		}
	}()

	if control.stop.IsSet() {
		return backoff
	}
	// Recorded before the stderr verdict, so a long session ends the streak
	// whatever ended it.
	hold := stalls.after(loggedIn || proc.killed.Load(), lastData.Sub(firstData))
	errf.Seek(0, io.SeekStart)
	rb, _ := io.ReadAll(errf)
	residual := string(rb)

	if terr := classify(residual); terr != nil {
		wait := terr.Cadence
		if st.SetFatal(terr.Error()) >= fatalEscalateAfter {
			wait = min(wait*6, time.Minute)
		}
		control.stop.Wait(max(wait, hold)) // a fatal cadence never undercuts a stall's hold
		return backoff
	}
	if trimmed := strings.TrimSpace(residual); trimmed != "" {
		lines := strings.Split(trimmed, "\n")
		st.Note(clip160(lines[len(lines)-1]))
	}
	if hold > 0 {
		// The hold only lengthens the wait: it tops it up to hold, and the
		// backoff still runs (and doubles) inside it.
		hold = max(hold, backoff)
		st.Note(stallNote(stalls.n, hold))
		if control.stop.Wait(hold - backoff) {
			return backoff
		}
	}
	return waitBackoff(control, backoff)
}

// clip160 truncates an ssh-stderr line for a UI Note, dropping any trailing partial
// rune the byte cut would otherwise leave (ToValidUTF8 strips the invalid tail).
func clip160(s string) string {
	if len(s) > 160 {
		return strings.ToValidUTF8(s[:160], "")
	}
	return s
}

// applyRecordSafe applies one record, swallowing a panic as a noted error and
// reporting false so the caller skips this record's bookkeeping. hadData distinguishes a valid metadata-only/dataless
// frame from the player data that may clear a fatal error and reset backoff.
func applyRecordSafe(st *protocol.State, rec protocol.Record) (hadData, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			st.Note(fmt.Sprintf("stream: %v", r))
			hadData = false
			ok = false
		}
	}()
	return protocol.ApplyRecord(st, rec), true
}

// reap closes stdin, awaits/kills the child, and releases every pipe.
// processSlot.closeStdin swallows a double-close panic itself, and waitTimeout /
// Kill (nil-guarded) cannot panic, so no extra recover wrapper is needed.
func reap(st *protocol.State, procs *processSlot, proc *process) {
	procs.closeStdin(proc)
	if !proc.waitTimeout(1 * time.Second) {
		if proc.Cmd != nil && proc.Cmd.Process != nil {
			proc.Cmd.Process.Kill()
		}
		proc.waitTimeout(2 * time.Second)
	}
	if procs.clear(proc) {
		st.Disconnect()
	}
	// A command written on the young-spawn grace alone went into a pipe, not
	// to the device: if this session died before its first data record, ssh
	// never connected and the command is gone — say so, as every other lost
	// path does (the phone-reboot case: a clean EOF, a respawn stuck in TCP
	// connect, a PAUSE that silently vanished).
	if proc.graceWrite.Load() && !st.DataSince(proc.spawned) {
		st.Note("command not delivered")
	}
	if proc.Stdout != nil {
		proc.Stdout.Close()
	}
}

// selfSnap is the persisted subset of a snapshot: the player state plus the
// last-known EQ/tone values, so both panes paint instantly on the next launch.
func selfSnap(st *protocol.State) config.CachedSnapshot {
	s := st.Snap()
	_, eq := st.EQView()
	return config.CachedSnapshot{
		Track: s.Track, Pos: s.Pos, Playing: s.Playing, Vol: s.Vol, EQ: eq,
	}
}

// CommandWorker drains the command queue, reduces and writes commands, and
// holds undeliverable ones in order for the next live session. A nil value is
// the teardown drain sentinel. It never dies. One ticker (not a fresh
// time.After each cycle) paces the idle poll, mirroring TunnelWorker, so the
// 2 Hz wait doesn't churn a timer allocation per cycle for the process life.
func commandWorker(st *protocol.State, procs *processSlot, control *runControl, cmds <-chan *protocol.Command, deadline time.Duration) {
	tick := time.NewTicker(CommandGetTimeout)
	defer tick.Stop()
	var pending []protocol.Command
	for !control.stop.IsSet() {
		if commandOnce(st, procs, control, cmds, tick.C, deadline, &pending) {
			return
		}
	}
}

// commandOnce runs one batch cycle; returns true to break the worker loop.
func commandOnce(st *protocol.State, procs *processSlot, control *runControl, cmds <-chan *protocol.Command, tick <-chan time.Time, deadline time.Duration, pending *[]protocol.Command) (brk bool) {
	defer func() {
		if r := recover(); r != nil {
			st.Note(fmt.Sprintf("command worker: %v", r))
			control.stop.Wait(time.Second)
			brk = false
		}
	}()

	var batch []protocol.Command
	flush := false

	// First item, blocking up to the ticker cadence (~CommandGetTimeout).
	got := false
	select {
	case c := <-cmds:
		got = true
		if c == nil {
			flush = true
		} else {
			batch = append(batch, *c)
		}
	case <-tick:
	}

	if !got {
		if len(*pending) == 0 {
			return false // queue empty, nothing pending -> spin
		}
		batch = append([]protocol.Command(nil), *pending...)
	} else {
		batch = append(append([]protocol.Command(nil), *pending...), batch...)
	}
	*pending = nil

	// Drain everything else already queued.
drain:
	for {
		select {
		case c := <-cmds:
			if c == nil {
				flush = true
			} else {
				batch = append(batch, *c)
			}
		default:
			break drain
		}
	}

	now := time.Now()
	var fresh []protocol.Command
	lost := false
	for _, c := range batch {
		switch {
		case now.Sub(c.TS) <= deadline:
			fresh = append(fresh, c)
		case userCommand(c.Mid):
			lost = true
		}
	}
	if lost {
		st.Note("command not delivered")
	}

	sent, volume := true, false
	if len(fresh) > 0 {
		reduced := protocol.ReduceCommands(fresh)
		var sb strings.Builder
		for _, c := range reduced {
			if protocol.ValidatePayload(c.Mid, c.Data) {
				fmt.Fprintf(&sb, "%d %s\n", c.Mid, c.Data)
				volume = volume || c.Mid == 64
			}
		}
		lines := sb.String()
		if lines != "" {
			sent = procs.write(st, now, LiveSessionTimeout, lines)
			if !sent { // carry over in order; each keeps its own timestamp
				*pending = reduced
			}
		}
	}

	if flush {
		if len(*pending) > 0 {
			st.Note("command not delivered")
		}
		control.drained.Set()
		return true
	}
	switch {
	case !sent:
		return control.stop.Wait(200 * time.Millisecond)
	case volume:
		// A volume set just went out: pause before the next batch, so a held
		// volume key's repeats pile up in the queue and the next drain writes
		// only the newest level (ReduceCommands keeps the last 64 — the levels
		// are absolute, so the ones between are superseded). Written one per
		// repeat, a 30 ms key repeat queued sets on the box faster than
		// LUCI_local runs them, and the loop's burst drain never ended.
		return control.stop.Wait(volumePace)
	}
	return false
}

// volumePace is the least time between two volume writes (see commandOnce):
// a held key reaches the device as a few sets a second, each the newest
// level, and a single press still goes out at once.
const volumePace = 150 * time.Millisecond

// userCommand reports whether mid is something the user asked for — transport,
// volume, night mode, a service toggle, a log fetch — as opposed to the view
// flags (90 diagnostics, 94 player-visible) the TUI re-asserts on its own every
// few seconds. Only lost user intent earns the "command not delivered" note: an
// expired view flag, noted, would overwrite the real reason the session is
// down with a message about a command nobody typed.
func userCommand(mid int) bool {
	switch mid {
	case 40, 64, 91, 92, 93:
		return true
	}
	return false
}

// Watchdog kills a connection that never proved itself, went silent, or wedged
// data-silent mid-stream. It marks the process before the kill, so the stream
// worker can tell a stall from a session that ended on its own (stallStreak).
func watchdog(st *protocol.State, procs *processSlot, control *runControl, silentAfter, connectWindow, datalessAfter time.Duration) {
	for !control.stop.Wait(500 * time.Millisecond) {
		func() {
			defer func() { recover() }()
			proc, spawned := procs.current()
			if proc == nil {
				return
			}
			lastRx, lastData, got := st.LivenessView()
			now := time.Now()
			base := laterTime(spawned, lastRx)
			limit := connectWindow
			if got {
				limit = silentAfter
			}
			// framed-but-empty records keep last_rx fresh but must not keep a
			// dataless connection alive
			wedged := now.Sub(laterTime(spawned, lastData)) > datalessAfter
			if now.Sub(base) > limit || wedged {
				if proc.Cmd != nil && proc.Cmd.Process != nil {
					proc.killed.Store(true)
					proc.Cmd.Process.Kill()
				}
			}
		}()
	}
}

func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Teardown is the quit path: a sentinel through the queue is a real flush
// handshake (once popped, everything before it has been written or visibly
// dropped); the short wait + SIGTERM ladder is just a fast-path so quit never
// waits on the remote side. It also persists one final snapshot, so the next
// launch's first paint is exactly the state the user quit on regardless of
// how coarse the mid-session SnapshotPersistInterval is.
func teardown(st *protocol.State, procs *processSlot, control *runControl, cmds chan<- *protocol.Command, drain time.Duration, snapshotPath string) {
	if snapshotPath != "" {
		config.SaveSnapshot(snapshotPath, selfSnap(st))
	}
	defer func() {
		control.stop.Set()
		proc, _ := procs.current()
		if proc == nil {
			return
		}
		procs.closeStdin(proc)
		if proc.waitTimeout(300 * time.Millisecond) {
			return
		}
		if proc.Cmd != nil && proc.Cmd.Process != nil {
			proc.Cmd.Process.Signal(syscall.SIGTERM)
		}
		if proc.waitTimeout(1500 * time.Millisecond) {
			return
		}
		if proc.Cmd != nil && proc.Cmd.Process != nil {
			proc.Cmd.Process.Kill()
		}
		proc.waitTimeout(1 * time.Second)
	}()
	cmds <- nil
	control.drained.Wait(drain)
}
