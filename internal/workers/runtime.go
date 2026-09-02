package workers

import (
	"context"
	"sync"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

// Runtime owns the long-lived background workers and their command queues.
// Close performs the existing command-drain/process teardown handshake and then
// joins every worker, so Run never returns while a worker still references
// State or its persistence paths.
type Runtime struct {
	Commands   chan *protocol.Command
	EQCommands chan EQCommand

	st        *protocol.State
	snapshot  string
	procs     *processSlot
	control   *runControl
	cancel    context.CancelFunc
	closeOnce sync.Once
	wg        sync.WaitGroup
}

type runControl struct {
	stop    *runSignal
	drained *runSignal
}

func newRunControl() *runControl {
	return &runControl{stop: newRunSignal(), drained: newRunSignal()}
}

// runSignal is the runtime-owned, one-shot broadcast used for worker shutdown
// and the command-drain handshake.
type runSignal struct {
	mu  sync.Mutex
	ch  chan struct{}
	set bool
}

func newRunSignal() *runSignal {
	return &runSignal{ch: make(chan struct{})}
}

func (s *runSignal) Set() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		s.set = true
		close(s.ch)
	}
}

func (s *runSignal) IsSet() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set
}

func (s *runSignal) Wait(d time.Duration) bool {
	if s.IsSet() {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.ch:
		return true
	case <-t.C:
		return s.IsSet()
	}
}

// StartRuntime starts the stream, command, watchdog, tunnel, and artwork
// workers as one owned unit.
func StartRuntime(st *protocol.State, cfg config.Config) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	snapshot := config.SnapshotPath(cfg)
	PreloadSnapshot(st, config.LoadSnapshot(snapshot))
	r := &Runtime{
		Commands:   make(chan *protocol.Command, 1024),
		EQCommands: make(chan EQCommand, 64),
		st:         st,
		snapshot:   snapshot,
		procs:      newProcessSlot(),
		control:    newRunControl(),
		cancel:     cancel,
	}
	r.wg.Go(func() { streamWorker(st, cfg, r.snapshot, r.procs, r.control) })
	r.wg.Go(func() { commandWorker(st, r.procs, r.control, r.Commands, CommandDeadline) })
	r.wg.Go(func() { watchdog(st, r.procs, r.control, SilentAfter, ConnectWindow, DatalessAfter) })
	r.wg.Go(func() { tunnelWorker(ctx, r.control, st, cfg, r.EQCommands) })
	r.wg.Go(func() { artWorker(ctx, r.control, st, cfg) })
	r.wg.Go(func() { lssdpWorker(ctx, r.control, st, cfg) })
	r.wg.Go(func() { zcWorker(ctx, r.control, st, cfg) })
	r.wg.Go(func() { otaWorker(ctx, r.control, st) })
	return r
}

// PreloadSnapshot seeds the domain state from the last persisted player and EQ
// values. The cache is only a first-paint hint: playback always starts paused,
// and live device reads remain authoritative.
func PreloadSnapshot(st *protocol.State, cached *config.CachedSnapshot) {
	if cached == nil {
		return
	}
	track := protocol.SanitizeCached(cached.Track)
	if track.Empty() {
		track = nil
	}
	st.Preload(track, cached.Pos, cached.Vol)

	if len(cached.EQ) == 0 {
		return
	}
	vals := make(map[string]int, len(cached.EQ))
	for code, value := range cached.EQ {
		if _, known := tunnel.Lookup(code); !known {
			continue
		}
		vals[code] = tunnel.Clamp(code, value)
	}
	if len(vals) > 0 {
		st.PreloadEQ(vals)
	}
}

// Close drains player commands, stops the device processes and interruptible
// network work, then waits for all workers. It is safe to call more than once.
func (r *Runtime) Close(drain time.Duration) {
	r.closeOnce.Do(func() {
		teardown(r.st, r.procs, r.control, r.Commands, drain, r.snapshot)
		r.cancel()
		r.wg.Wait()
	})
}
