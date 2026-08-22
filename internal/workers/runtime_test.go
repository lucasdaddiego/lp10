package workers

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func TestRuntimeCloseStopsAndJoinsWorkers(t *testing.T) {
	t.Setenv("LP10_SSH", filepath.Join(t.TempDir(), "missing-ssh"))
	t.Setenv("LP10_TUNNEL_ADDR", "127.0.0.1:0")
	t.Setenv("LP10_LSSDP_HOST", "")
	t.Setenv("LP10_STATE_DIR", t.TempDir())

	st := protocol.NewState()
	r := StartRuntime(st, config.Config{Host: "127.0.0.1", Art: false})

	done := make(chan struct{})
	go func() {
		r.Close(100 * time.Millisecond)
		r.Close(100 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Runtime.Close did not join its workers")
	}
	if !r.control.stop.IsSet() {
		t.Error("Runtime.Close did not stop its workers")
	}
}

func TestRuntimePreloadsPersistedSnapshotBeforeStarting(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("LP10_STATE_DIR", stateDir)
	t.Setenv("LP10_SSH", filepath.Join(t.TempDir(), "missing-ssh"))
	t.Setenv("LP10_TUNNEL_ADDR", "127.0.0.1:0")
	cfg := config.Config{Host: "cached.local", Art: false}
	config.SaveSnapshot(config.SnapshotPath(cfg), config.CachedSnapshot{
		Track: &protocol.Track{TrackName: "Cached"}, Pos: 4200, Vol: 37,
		EQ: map[string]int{"BAS": 4},
	})

	st := protocol.NewState()
	r := StartRuntime(st, cfg)
	t.Cleanup(func() { r.Close(100 * time.Millisecond) })

	s := st.Snap()
	if s.Track == nil || s.Track.TrackName != "Cached" || s.Pos != 4200 || s.Vol != 37 || s.Playing != 2 {
		t.Fatalf("runtime preload = %+v", s)
	}
	if got, ok := st.EQValue("BAS"); !ok || got != 4 {
		t.Fatalf("runtime EQ preload = %d,%v, want 4,true", got, ok)
	}
}
