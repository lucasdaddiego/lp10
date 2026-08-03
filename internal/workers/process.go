package workers

import (
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// process is one live ssh child and its pipes. Done is closed by the single
// goroutine that owns Cmd.Wait(), so reap and teardown can both await exit
// without racing on Wait (which may run only once).
type process struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Done   chan struct{}
}

func (p *process) waitTimeout(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.Done:
		return true
	case <-t.C:
		return false
	}
}

// processSlot owns the current child handle and serializes stdin writes against
// close. Process mechanics stay in workers; protocol.State only carries domain
// and liveness data.
type processSlot struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	proc    *process
	spawned time.Time
}

func newProcessSlot() *processSlot {
	return &processSlot{}
}

func (s *processSlot) start(st *protocol.State, p *process) {
	now := time.Now()
	st.StartConnection()

	s.writeMu.Lock()
	s.mu.Lock()
	s.proc, s.spawned = p, now
	s.mu.Unlock()
	s.writeMu.Unlock()
}

func (s *processSlot) current() (*process, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc, s.spawned
}

// clear removes p only if it is still the active child. The identity check
// prevents a late cleanup from disconnecting a newer session.
func (s *processSlot) clear(p *process) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != p {
		return false
	}
	s.proc = nil
	s.spawned = time.Time{}
	return true
}

// closeStdin serializes close against command writes. A defensive recover keeps
// a broken writer implementation from taking down teardown.
func (s *processSlot) closeStdin(p *process) {
	defer func() { recover() }()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if p.Stdin != nil {
		p.Stdin.Close()
	}
}

func (s *processSlot) write(st *protocol.State, now time.Time, liveTimeout time.Duration, data string) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	p, spawned := s.current()
	if p == nil || p.Stdin == nil || !st.WriterLive(now, spawned, liveTimeout) {
		return false
	}
	_, err := io.WriteString(p.Stdin, data)
	return err == nil
}
