package workers

import (
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// disableProbes turns the LSSDP, ZeroConf and OTA workers off for a Runtime
// test (set-but-empty is the off switch), so only the tunnel runs.
func disableProbes(t *testing.T) {
	t.Helper()
	t.Setenv("LP10_LSSDP_HOST", "")
	t.Setenv("LP10_ZC_ADDR", "")
	t.Setenv("LP10_OTA_URL", "")
}

// The runtime paints the last session's volume and EQ before any frame
// arrives, and Close is the quit path: a command pressed just before q
// reaches the box, every worker is joined, the snapshot is written, and a
// second Close is a no-op.
func TestRuntimeStartsFromSnapshotDrainsAndPersistsOnClose(t *testing.T) {
	dev := newLiveBox()
	gate := make(chan struct{})
	box := newFakeBox(t, func(frame string) string {
		<-gate // hold the replies until the preload has been checked
		return dev.answer(frame)
	})
	t.Setenv("LP10_TUNNEL_ADDR", box.addr)
	disableProbes(t)
	t.Setenv("LP10_STATE_DIR", t.TempDir())
	cfg := config.Config{Host: "lp10.local"}
	path := config.SnapshotPath(cfg)
	config.SaveSnapshot(path, config.CachedSnapshot{Vol: 37, EQ: map[string]int{"BAS": 4, "MXV": 250, "ZZZ": 9}})

	st := protocol.NewState()
	r := StartRuntime(st, cfg)
	released := false
	defer func() {
		if !released {
			close(gate)
		}
		r.Close(0)
	}()
	if s := st.Snap(); s.Vol != 37 || s.VolLive {
		t.Errorf("preloaded vol %d live %v, want 37 from the snapshot, not live", s.Vol, s.VolLive)
	}
	_, eq := st.EQView()
	if want := map[string]int{"BAS": 4, "MXV": 100}; !maps.Equal(eq, want) {
		t.Errorf("preloaded EQ = %v, want %v (clamped, unknown codes dropped)", eq, want)
	}
	close(gate)
	released = true

	box.waitFrame(t, 2*time.Second, "the tunnel's first query", is("STA"))
	r.Commands <- Command{Code: "POP", TS: time.Now()}
	r.Close(DrainTimeout)
	done := make(chan struct{})
	go func() { r.Close(DrainTimeout); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a second Close did not return at once")
	}
	box.waitFrame(t, time.Second, "the POP queued before Close", is("POP"))
	if !r.control.stop.IsSet() || !r.control.drained.IsSet() {
		t.Errorf("after Close: stop %v drained %v", r.control.stop.IsSet(), r.control.drained.IsSet())
	}
	eventually(t, "the tunnel's connection closed", time.Second, func() bool { return box.openConns() == 0 })

	got := config.LoadSnapshot(path)
	want := selfSnap(st)
	if got == nil || got.Vol != want.Vol || !maps.Equal(got.EQ, want.EQ) {
		t.Errorf("persisted snapshot = %+v, want the closing state %+v", got, want)
	}
}

// With no usable state dir and the box unreachable, the runtime still runs
// (no preload, no persistence) and Close does not sit out the drain budget:
// nothing is connected, so there is nothing to drain.
func TestRuntimeWithoutStateDirOrBox(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("a file, not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(blocker, "state"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_TUNNEL_ADDR", ln.Addr().String())
	ln.Close()
	disableProbes(t)

	st := protocol.NewState()
	r := StartRuntime(st, config.Config{Host: "box"})
	if r.snapshot != "" {
		t.Errorf("snapshot path = %q, want none without a state dir", r.snapshot)
	}
	eventually(t, "the refused dial noted", 2*time.Second, func() bool { return strings.HasPrefix(st.Snap().Error, "cannot reach :2018") })
	start := time.Now()
	r.Close(DrainTimeout)
	if el := time.Since(start); el >= DrainTimeout {
		t.Errorf("Close took %v with nothing connected, want well under the %v drain budget", el, DrainTimeout)
	}
}

// PreloadSnapshot is a first-paint hint: the volume (clamped) and the EQ
// codes lp10 knows (clamped), nothing else, and no echo hold — the device's
// first reading still wins.
func TestPreloadSnapshot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		cached *config.CachedSnapshot
		vol    int
		eq     map[string]int
	}{
		{"nothing cached", nil, 0, map[string]int{}},
		{"volume only, clamped", &config.CachedSnapshot{Vol: 150}, 100, map[string]int{}},
		{"only unknown codes", &config.CachedSnapshot{Vol: 20, EQ: map[string]int{"ZZZ": 1, "VOL": 9}}, 20, map[string]int{}},
		{"known codes clamped", &config.CachedSnapshot{Vol: 20, EQ: map[string]int{"BAS": -50, "MXV": 250, "EQS": 3, "ZZZ": 1}},
			20, map[string]int{"BAS": -10, "MXV": 100, "EQS": 3}},
	} {
		st := protocol.NewState()
		PreloadSnapshot(st, tc.cached)
		_, eq := st.EQView()
		if s := st.Snap(); s.Vol != tc.vol || s.VolLive || !maps.Equal(eq, tc.eq) {
			t.Errorf("%s: vol %d live %v eq %v, want %d false %v", tc.name, s.Vol, s.VolLive, eq, tc.vol, tc.eq)
		}
	}
	st := protocol.NewState()
	PreloadSnapshot(st, &config.CachedSnapshot{Vol: 20, EQ: map[string]int{"BAS": 4}})
	st.ApplyTunnel("BAS", -2)
	st.ApplyVolume(31)
	if v, _ := st.EQValue("BAS"); v != -2 || st.Snap().Vol != 31 {
		t.Errorf("the device's reading did not replace the preload: BAS %d vol %d", v, st.Snap().Vol)
	}
}

// selfSnap persists exactly the volume and the known EQ values.
func TestSelfSnap(t *testing.T) {
	t.Parallel()
	st := protocol.NewState()
	st.SetVol(33)
	st.SetEQLocal("TRE", 4)
	st.SetEQLocal("BAL", -20)
	if got := selfSnap(st); got.Vol != 33 || !maps.Equal(got.EQ, map[string]int{"TRE": 4, "BAL": -20}) {
		t.Errorf("selfSnap = %+v", got)
	}
}

func TestRunSignal(t *testing.T) {
	t.Parallel()
	s := newRunSignal()
	if s.IsSet() || s.Wait(10*time.Millisecond) {
		t.Fatal("a fresh signal reads as set")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Set()
	}()
	if !s.Wait(5 * time.Second) {
		t.Fatal("Wait missed a Set")
	}
	s.Set() // idempotent
	start := time.Now()
	if !s.Wait(time.Hour) || time.Since(start) > 100*time.Millisecond {
		t.Error("Wait on a set signal should return at once")
	}
}

// waitBackoff doubles the backoff after sleeping it, caps it at MaxBackoff,
// and leaves it alone when Stop cuts the sleep short. The cap case sleeps
// MaxBackoff/2 (1.5 s): it runs in parallel.
func TestWaitBackoff(t *testing.T) {
	t.Parallel()
	c := newRunControl()
	start := time.Now()
	if got := waitBackoff(c, 10*time.Millisecond); got != 20*time.Millisecond || time.Since(start) < 10*time.Millisecond {
		t.Errorf("waitBackoff(10ms) = %v after %v, want 20ms after sleeping it", got, time.Since(start))
	}
	if got := waitBackoff(c, MaxBackoff/2+time.Millisecond); got != MaxBackoff {
		t.Errorf("waitBackoff past half the cap = %v, want MaxBackoff %v", got, MaxBackoff)
	}
	c.stop.Set()
	start = time.Now()
	if got := waitBackoff(c, MaxBackoff); got != MaxBackoff || time.Since(start) > 100*time.Millisecond {
		t.Errorf("waitBackoff on stop = %v after %v, want it unchanged at once", got, time.Since(start))
	}
}

// fence turns a panic into a note and a hold (cut short by Stop); a clean
// run notes nothing.
func TestFence(t *testing.T) {
	t.Parallel()
	st := protocol.NewState()
	c := newRunControl()
	ran := false
	fence(st, c, "probe", func() { ran = true })
	if !ran || st.Snap().Error != "" {
		t.Fatalf("clean run: ran %v note %q", ran, st.Snap().Error)
	}
	c.stop.Set()
	start := time.Now()
	fence(st, c, "probe", func() { panic("parse \x1b[31mboom") })
	if e := st.Snap().Error; e != "probe: parse [31mboom" {
		t.Errorf("note = %q, want the panic, control-stripped", e)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("the hold after a panic ignored Stop")
	}
}
