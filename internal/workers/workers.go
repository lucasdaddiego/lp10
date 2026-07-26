// Package workers runs the background goroutines: the stream reader/reconnect
// loop, the command writer, the watchdog, and teardown. Port of
// lp10lib/workers.py (Python threads -> goroutines).
package workers

import (
	"bufio"
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

// classify maps residual ssh stderr to a fatal/transient verdict. It is a var
// so tests can shorten the fatal retry cadence (mirroring the Python suite's
// fast_fatal monkeypatch of workers.classify_stderr).
var classify = transport.ClassifyStderr

// boundedLines yields lines from r, each at most maxLine bytes (mirroring
// readline(_MAX_LINE)): a line ends at '\n' or once the cap is hit. Returns
// ("", false) at EOF with nothing buffered. ReadSlice serves a whole line from
// bufio's buffer in one call (vs a byte at a time); the buffer is sized to
// maxLine so a newline-free run comes back in maxLine chunks via ErrBufferFull,
// bounding memory exactly as before. string(line) copies out of the buffer, so
// the lines IterRecords retains stay valid across later reads.
func boundedLines(r io.Reader) func() (string, bool) {
	br := bufio.NewReaderSize(r, maxLine)
	return func() (string, bool) {
		// The error needs no inspection: ReadSlice returns nil error only when
		// the delimiter was found, so an empty slice always means EOF/failure.
		line, _ := br.ReadSlice('\n')
		if len(line) > 0 {
			return string(line), true
		}
		return "", false
	}
}

// StreamWorker is the reconnect loop; it never dies.
func StreamWorker(st *protocol.State, cfg config.Config) {
	backoff := InitialBackoff
	for !st.Stop.IsSet() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					st.Note(fmt.Sprintf("stream worker: %v", r))
					st.Stop.Wait(time.Second)
				}
			}()
			backoff = streamOnce(st, cfg, backoff)
		}()
	}
}

// streamOnce is one connection lifecycle, returning the next reconnect backoff.
// stderr goes to a temp file, not a pipe, so ssh can never block on a full
// stderr buffer; the residual is read post-mortem.
func streamOnce(st *protocol.State, cfg config.Config, backoff time.Duration) time.Duration {
	// failStart notes a spawn failure, releases whatever pipes exist so far,
	// and holds the retry cadence — the shared tail of every pre-launch error.
	failStart := func(err error, closers ...io.Closer) time.Duration {
		for _, c := range closers {
			c.Close()
		}
		st.Note(fmt.Sprintf("cannot start ssh: %v", err))
		st.Stop.Wait(3 * time.Second)
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

	argv := append(transport.SSHArgv(cfg), transport.RemoteLoop(cfg.PingHost))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = transport.SpawnEnv()
	cmd.Stderr = errf

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

	proc := &protocol.Proc{Cmd: cmd, Stdin: inW, Stdout: outR, Done: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(proc.Done)
	}()
	st.StartProc(proc)

	if !st.Stop.IsSet() {
		var lastPersist time.Time
		nextLine := boundedLines(outR)
		for rec := range protocol.IterRecords(nextLine) {
			if st.Stop.IsSet() {
				break
			}
			hadData, ok := applyRecordSafe(st, rec)
			if !ok || !hadData {
				continue
			}
			backoff = InitialBackoff
			st.ClearFatalOnData()
			now := time.Now()
			if st.SnapshotFile != "" && now.Sub(lastPersist) > SnapshotPersistInterval && !st.Stop.IsSet() {
				config.SaveSnapshot(st.SnapshotFile, selfSnap(st))
				lastPersist = now
			}
		}
	}
	reap(st, proc)

	if st.Stop.IsSet() {
		return backoff
	}
	errf.Seek(0, io.SeekStart)
	rb, _ := io.ReadAll(errf)
	residual := string(rb)

	if terr := classify(residual); terr != nil {
		st.SetFatal(terr.Error())
		st.Stop.Wait(terr.Cadence)
		return backoff
	}
	if trimmed := strings.TrimSpace(residual); trimmed != "" {
		lines := strings.Split(trimmed, "\n")
		st.Note(clip160(lines[len(lines)-1]))
	}
	return waitBackoff(st, backoff)
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
// reporting false so the caller skips this record's bookkeeping (Python's
// except ...: continue). hadData distinguishes a valid metadata-only/dataless
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

// closeStdinLocked closes the child's stdin while holding WLock, so the close can't
// race a concurrent write; an already-closed/raced Close panic is swallowed.
func closeStdinLocked(st *protocol.State, proc *protocol.Proc) {
	defer func() { recover() }()
	st.WLock.Lock()
	defer st.WLock.Unlock()
	if proc.Stdin != nil {
		proc.Stdin.Close()
	}
}

// reap closes stdin, awaits/kills the child, and releases every pipe.
// closeStdinLocked swallows a double-close panic itself, and WaitTimeout /
// Kill (nil-guarded) cannot panic, so no extra recover wrapper is needed.
func reap(st *protocol.State, proc *protocol.Proc) {
	closeStdinLocked(st, proc)
	if !proc.WaitTimeout(1 * time.Second) {
		if proc.Cmd.Process != nil {
			proc.Cmd.Process.Kill()
		}
		proc.WaitTimeout(2 * time.Second)
	}
	st.Reap()
	if proc.Stdout != nil {
		proc.Stdout.Close()
	}
}

// selfSnap is the persisted subset of a snapshot: the player state plus the
// last-known EQ/tone values, so both panes paint instantly on the next launch.
func selfSnap(st *protocol.State) map[string]any {
	s := st.Snap()
	_, eq := st.EQView()
	return map[string]any{
		"track": s.Track, "pos": s.Pos, "playing": s.Playing, "vol": s.Vol, "eq": eq,
	}
}

// CommandWorker drains the command queue, reduces and writes commands, and
// holds undeliverable ones in order for the next live session. A nil value is
// the teardown drain sentinel. It never dies. One ticker (not a fresh
// time.After each cycle) paces the idle poll, mirroring TunnelWorker, so the
// 2 Hz wait doesn't churn a timer allocation per cycle for the process life.
func CommandWorker(st *protocol.State, cmds <-chan *protocol.Command, deadline time.Duration) {
	tick := time.NewTicker(CommandGetTimeout)
	defer tick.Stop()
	var pending []protocol.Command
	for !st.Stop.IsSet() {
		if commandOnce(st, cmds, tick.C, deadline, &pending) {
			return
		}
	}
}

// commandOnce runs one batch cycle; returns true to break the worker loop.
func commandOnce(st *protocol.State, cmds <-chan *protocol.Command, tick <-chan time.Time, deadline time.Duration, pending *[]protocol.Command) (brk bool) {
	defer func() {
		if r := recover(); r != nil {
			st.Note(fmt.Sprintf("command worker: %v", r))
			st.Stop.Wait(time.Second)
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
	for _, c := range batch {
		if now.Sub(c.TS) <= deadline {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) < len(batch) {
		st.Note("command not delivered")
	}

	sent := true
	if len(fresh) > 0 {
		reduced := protocol.ReduceCommands(fresh)
		var sb strings.Builder
		for _, c := range reduced {
			if protocol.ValidatePayload(c.Mid, c.Data) {
				fmt.Fprintf(&sb, "%d %s\n", c.Mid, c.Data)
			}
		}
		lines := sb.String()
		proc, live := st.WriterTarget(now, LiveSessionTimeout)
		if lines != "" {
			sent = false
			if proc != nil && proc.Stdin != nil && live {
				st.WLock.Lock()
				_, werr := io.WriteString(proc.Stdin, lines)
				st.WLock.Unlock()
				if werr == nil {
					sent = true
				}
			}
			if !sent { // carry over in order; each keeps its own timestamp
				*pending = reduced
			}
		}
	}

	if flush {
		if len(*pending) > 0 {
			st.Note("command not delivered")
		}
		st.Drained.Set()
		return true
	}
	if !sent && st.Stop.Wait(200*time.Millisecond) {
		return true
	}
	return false
}

// Watchdog kills a connection that never proved itself, went silent, or wedged
// data-silent mid-stream.
func Watchdog(st *protocol.State, silentAfter, connectWindow, datalessAfter time.Duration) {
	for !st.Stop.Wait(500 * time.Millisecond) {
		func() {
			defer func() { recover() }()
			proc, spawned, lastRx, lastData, got := st.WatchdogView()
			if proc == nil {
				return
			}
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
				if proc.Cmd.Process != nil {
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
func Teardown(st *protocol.State, cmds chan<- *protocol.Command, drain time.Duration) {
	if st.SnapshotFile != "" {
		config.SaveSnapshot(st.SnapshotFile, selfSnap(st))
	}
	defer func() {
		st.Stop.Set()
		proc := st.Sproc()
		if proc == nil {
			return
		}
		closeStdinLocked(st, proc)
		if proc.WaitTimeout(300 * time.Millisecond) {
			return
		}
		if proc.Cmd.Process != nil {
			proc.Cmd.Process.Signal(syscall.SIGTERM)
		}
		if proc.WaitTimeout(1500 * time.Millisecond) {
			return
		}
		if proc.Cmd.Process != nil {
			proc.Cmd.Process.Kill()
		}
		proc.WaitTimeout(1 * time.Second)
	}()
	cmds <- nil
	st.Drained.Wait(drain)
}
