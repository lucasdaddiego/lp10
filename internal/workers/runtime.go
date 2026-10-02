package workers

import (
	"context"
	"sync"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

// Runtime owns the long-lived background workers and the command queue. Close
// lets the tunnel write what is still queued, persists the snapshot, and then
// joins every worker, so Run never returns while a worker still references
// State or its persistence paths.
type Runtime struct {
	Commands chan Command

	st        *protocol.State
	snapshot  string
	control   *runControl
	cancel    context.CancelFunc
	closeOnce sync.Once
	wg        sync.WaitGroup
}

type runControl struct {
	stop    *runSignal
	drained *runSignal // the tunnel worker has written (or dropped) what was queued at stop
}

func newRunControl() *runControl {
	return &runControl{stop: newRunSignal(), drained: newRunSignal()}
}

// runSignal is the runtime-owned, one-shot broadcast used for worker shutdown
// and the drain handshake.
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

// StartRuntime starts the tunnel, LSSDP, Spotify ZeroConf, and OTA workers as
// one owned unit.
func StartRuntime(st *protocol.State, cfg config.Config) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	snapshot := config.SnapshotPath(cfg)
	PreloadSnapshot(st, config.LoadSnapshot(snapshot))
	r := &Runtime{
		// Deep: the tunnel drains only while connected, and a held key during
		// a reconnect (~30 presses/s) fills a shallow queue in seconds — the
		// drop-oldest send would then evict an unrelated earlier command with
		// no note. Expired intent is still dropped downstream
		// (CommandDeadline), so depth costs nothing.
		Commands: make(chan Command, 1024),
		st:       st,
		snapshot: snapshot,
		control:  newRunControl(),
		cancel:   cancel,
	}
	r.wg.Go(func() { tunnelWorker(ctx, r.control, st, cfg, r.Commands, r.snapshot) })
	r.wg.Go(func() { lssdpWorker(ctx, r.control, st, cfg) })
	r.wg.Go(func() { zcWorker(ctx, r.control, st, cfg) })
	r.wg.Go(func() { otaWorker(ctx, r.control, st) })
	return r
}

// PreloadSnapshot seeds the domain state from the last persisted volume and
// EQ values. The cache is only a first-paint hint: live device reads remain
// authoritative.
func PreloadSnapshot(st *protocol.State, cached *config.CachedSnapshot) {
	if cached == nil {
		return
	}
	st.Preload(cached.Vol)

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

// Close stops the workers — the tunnel first writes what is still queued,
// waiting at most drain for it — persists the snapshot, and waits for every
// worker. It is safe to call more than once.
func (r *Runtime) Close(drain time.Duration) {
	r.closeOnce.Do(func() {
		r.control.stop.Set()
		r.control.drained.Wait(drain)
		r.cancel()
		r.wg.Wait()
		if r.snapshot != "" {
			config.SaveSnapshot(r.snapshot, selfSnap(r.st))
		}
	})
}
