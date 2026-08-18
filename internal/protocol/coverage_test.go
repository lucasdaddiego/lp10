package protocol

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"
)

// ---- pyStr ------------------------------------------------------------------

// pyStr mirrors Python str() for non-string values landing in a string field.
func TestCov_pyStr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{true, "True"},                           // bool true
		{false, "False"},                         // bool false
		{1.5, "1.5"},                             // float64
		{json.Number("42"), "42"},                // json.Number
		{"already a string", "already a string"}, // string passthrough
		{7, "7"},                                 // default: fmt.Sprintf("%v") on an int
		{nil, "<nil>"},                           // default: fmt.Sprintf("%v") on nil
	}
	for _, c := range cases {
		if got := pyStr(c.in); got != c.want {
			t.Errorf("pyStr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- Int (every case) -------------------------------------------------------

func TestCov_Int(t *testing.T) {
	// bool is never an int (matching Python's isinstance(bool) exclusion).
	if _, ok := Int(true); ok {
		t.Error("Int(true) should be not-ok")
	}
	if _, ok := Int(false); ok {
		t.Error("Int(false) should be not-ok")
	}
	// int / int64
	if v, ok := Int(5); !ok || v != 5 {
		t.Errorf("Int(int 5) = (%d, %v), want (5, true)", v, ok)
	}
	if v, ok := Int(int64(7)); !ok || v != 7 {
		t.Errorf("Int(int64 7) = (%d, %v), want (7, true)", v, ok)
	}
	// float64: truncates; NaN/Inf rejected
	if v, ok := Int(3.9); !ok || v != 3 {
		t.Errorf("Int(3.9) = (%d, %v), want (3, true)", v, ok)
	}
	if _, ok := Int(math.NaN()); ok {
		t.Error("Int(float64 NaN) should be not-ok")
	}
	if _, ok := Int(math.Inf(1)); ok {
		t.Error("Int(float64 +Inf) should be not-ok")
	}
	// float32: truncates; NaN/Inf rejected
	if v, ok := Int(float32(4.9)); !ok || v != 4 {
		t.Errorf("Int(float32 4.9) = (%d, %v), want (4, true)", v, ok)
	}
	if _, ok := Int(float32(math.NaN())); ok {
		t.Error("Int(float32 NaN) should be not-ok")
	}
	if _, ok := Int(float32(math.Inf(1))); ok {
		t.Error("Int(float32 +Inf) should be not-ok")
	}
	// json.Number: integer literal, non-integer float literal, out-of-range, garbage
	if v, ok := Int(json.Number("123")); !ok || v != 123 {
		t.Errorf("Int(json.Number 123) = (%d, %v), want (123, true)", v, ok)
	}
	if v, ok := Int(json.Number("2.9")); !ok || v != 2 {
		t.Errorf("Int(json.Number 2.9) = (%d, %v), want (2, true)", v, ok)
	}
	if _, ok := Int(json.Number("1e999")); ok {
		t.Error("Int(json.Number 1e999) should be not-ok (overflows to Inf)")
	}
	if _, ok := Int(json.Number("9223372036854775808")); ok {
		t.Error("Int(json.Number 2^63) should be not-ok (outside native int range)")
	}
	if _, ok := Int(1e30); ok {
		t.Error("Int(float64 1e30) should be not-ok (outside native int range)")
	}
	if _, ok := Int(json.Number("abc")); ok {
		t.Error("Int(json.Number abc) should be not-ok (unparseable)")
	}
	// string: valid + invalid
	if v, ok := Int("  42  "); !ok || v != 42 {
		t.Errorf("Int(\"  42  \") = (%d, %v), want (42, true)", v, ok)
	}
	if _, ok := Int("nope"); ok {
		t.Error("Int(\"nope\") should be not-ok")
	}
	// default: an unknown type
	if _, ok := Int([]string{"x"}); ok {
		t.Error("Int([]string) should be not-ok (unknown type)")
	}
}

func TestCov_ParseJSONRequiresOneCompleteValue(t *testing.T) {
	if got := parseJSON("{\"ok\":true}\n\t"); got == nil {
		t.Error("one JSON value plus trailing whitespace should parse")
	}
	for _, raw := range []string{
		`{"ok":true}{"extra":true}`,
		`{"ok":true} trailing`,
	} {
		if got := parseJSON(raw); got != nil {
			t.Errorf("parseJSON(%q) = %#v, want nil for trailing data", raw, got)
		}
	}
}

// ---- IterRecords early-break (yield returns false) --------------------------

func TestCov_IterRecordsEarlyBreak(t *testing.T) {
	// Two well-formed records; breaking after the first makes yield return false,
	// so IterRecords stops without draining the second.
	lines := []string{"@@p", "MID-Read:49 Data:1", "@@E", "@@v", "MID-Read:64 Data:2", "@@E"}
	count := 0
	for range IterRecords(feeder(lines)) {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("consumed %d records before break, want 1", count)
	}
}

// ---- ApplyRecord: @@i line without '=' is skipped ---------------------------

func TestCov_ApplyRecordDevInfoSkipsNonKVLine(t *testing.T) {
	st := NewState()
	// A line lacking '=' makes strings.Cut report !ok -> continue; the surrounding
	// key=value lines still parse.
	feed := "@@i\nthis-line-has-no-equals\nnet=eth\nip=10.0.0.5\n@@E\n"
	for _, rec := range recordsFrom(splitLines(feed)) {
		ApplyRecord(st, rec)
	}
	di := st.DiagnosticView(time.Now()).DevInfo
	if di == nil || di.Net != "eth" || di.IP != "10.0.0.5" {
		t.Errorf("devinfo = %+v, want Net=eth IP=10.0.0.5 (junk line skipped)", di)
	}
}

// ---- EQ / tunnel state ------------------------------------------------------

func TestCov_ApplyTunnelAndLocalHold(t *testing.T) {
	st := NewState()
	st.ApplyTunnel("BAS", 5)
	if v, ok := st.EQValue("BAS"); !ok || v != 5 {
		t.Errorf("EQValue(BAS) = (%d, %v), want (5, true)", v, ok)
	}
	if conn, _ := st.EQView(); !conn {
		t.Error("ApplyTunnel should mark the tunnel connected")
	}

	// A local change arms the echo-suppression hold; a device echo arriving inside
	// the window is dropped (value stays at the optimistic local one).
	st.SetEQLocal("TRE", 9)
	st.ApplyTunnel("TRE", 3)
	if v, ok := st.EQValue("TRE"); !ok || v != 9 {
		t.Errorf("EQValue(TRE) = (%d, %v), want (9, true) — echo suppressed within hold", v, ok)
	}
}

func TestCov_EQAccessors(t *testing.T) {
	st := NewState()
	// Unknown control before any data.
	if _, ok := st.EQValue("MXV"); ok {
		t.Error("EQValue for an unseen control should be not-ok")
	}
	// PreloadEQ seeds values WITHOUT marking the tunnel connected.
	st.PreloadEQ(map[string]int{"MXV": 80, "VBS": 1})
	if v, ok := st.EQValue("MXV"); !ok || v != 80 {
		t.Errorf("EQValue(MXV) after preload = (%d, %v), want (80, true)", v, ok)
	}
	if conn, vals := st.EQView(); conn || vals["MXV"] != 80 || vals["VBS"] != 1 {
		t.Errorf("after PreloadEQ: EQView = (%v, %v), want (false, MXV=80 VBS=1)", conn, vals)
	}
	// SetEQConnected flips the link flag both ways.
	st.SetEQConnected(true)
	if conn, vals := st.EQView(); !conn || vals["MXV"] != 80 {
		t.Errorf("after SetEQConnected(true): EQView = (%v, %v)", conn, vals)
	}
	st.SetEQConnected(false)
	if conn, _ := st.EQView(); conn {
		t.Error("SetEQConnected(false) should clear the link state")
	}
}

func TestCov_StateInputInvariants(t *testing.T) {
	st := NewState()
	st.Preload(nil, -500, 999)
	if s := st.Snap(); s.Pos != 0 || s.Vol != 100 {
		t.Errorf("Preload should clamp state, got pos=%d vol=%d", s.Pos, s.Vol)
	}

	// Extreme adjustments saturate without overflowing before the volume clamp.
	if got, _ := st.AdjustVol(math.MaxInt); got != 100 {
		t.Errorf("AdjustVol(MaxInt) = %d, want 100", got)
	}
	if got, _ := st.AdjustVol(math.MinInt); got != 0 {
		t.Errorf("AdjustVol(MinInt) = %d, want 0", got)
	}

	deviceState := NewState()
	hadData := ApplyRecord(deviceState, Record{
		"p": {"MID-Read:49 Data:-20 Length:3"},
		"v": {"MID-Read:64 Data:500 Length:3"},
	})
	if !hadData {
		t.Error("valid position/volume sections should report usable data")
	}
	if s := deviceState.Snap(); s.Pos != 0 || s.Vol != 100 {
		t.Errorf("device values should preserve state invariants, got pos=%d vol=%d", s.Pos, s.Vol)
	}
	if ApplyRecord(deviceState, Record{"p": {}}) {
		t.Error("an empty framed section should not report usable player data")
	}

	deviceState.mu.Lock()
	deviceState.track = &Track{TrackName: "overflow guard"}
	deviceState.posMs = math.MaxInt
	deviceState.posAt = time.Now().Add(-time.Second)
	deviceState.playing = 0
	deviceState.connected = true
	deviceState.mu.Unlock()
	if pos := deviceState.Snap().Pos; pos != math.MaxInt {
		t.Errorf("extrapolated position overflowed to %d, want saturated MaxInt", pos)
	}
}

// ---- Note / SetFatal / ClearFatalOnData -------------------------------------

func TestCov_NoteAndFatal(t *testing.T) {
	st := NewState()
	st.Note("transient")
	if msg := st.Snap().Error; msg != "transient" {
		t.Errorf("errMsg = %q, want \"transient\"", msg)
	}

	// Note/SetFatal control-strip: ssh stderr / a device banner can carry raw
	// escapes, and the error line renders at full width. The ESC/BEL bytes that
	// would START a sequence are removed (the leftover printable "]8;;" is inert
	// without its ESC), so no OSC-8 link or SGR run can survive to the terminal.
	st2 := NewState()
	st2.Note("bad\x1b]8;;http://evil\x07line")
	if msg := st2.Snap().Error; msg != "bad]8;;http://evilline" {
		t.Errorf("Note must strip the ESC/BEL, got %q", msg)
	}
	st2.SetFatal("fatal\x1b[31mred")
	if msg := st2.Snap().Error; msg != "fatal[31mred" {
		t.Errorf("SetFatal must strip the ESC, got %q", msg)
	}

	// SetFatal latches; Note is a no-op while fatal.
	st.SetFatal("boom")
	st.Note("ignored while fatal")
	if msg := st.Snap().Error; msg != "boom" {
		t.Errorf("errMsg = %q, want \"boom\" (Note no-ops once fatal)", msg)
	}
	if !st.Snap().Fatal {
		t.Error("Snap().Fatal should be true after SetFatal")
	}

	// ClearFatalOnData self-heals once data flows again.
	st.ClearFatalOnData()
	if st.Snap().Fatal {
		t.Error("ClearFatalOnData should clear the fatal flag")
	}
	if msg := st.Snap().Error; msg != "" {
		t.Errorf("errMsg = %q, want cleared after ClearFatalOnData", msg)
	}
}

// ---- connection-liveness accessors -----------------------------------------

func TestCov_ConnectionLivenessAccessors(t *testing.T) {
	st := NewState()
	if st.RawAttempts() != 0 {
		t.Errorf("RawAttempts = %d, want 0", st.RawAttempts())
	}

	spawned := time.Now()
	st.StartConnection()
	if st.RawAttempts() != 1 {
		t.Errorf("RawAttempts after StartConnection = %d, want 1", st.RawAttempts())
	}

	lastRx, lastData, got := st.LivenessView()
	if !lastRx.IsZero() || !lastData.IsZero() || got {
		t.Errorf("LivenessView = (rx=%v data=%v got=%v), want (zero, zero, false)",
			lastRx, lastData, got)
	}

	// A fresh spawn is writable within the live window.
	if !st.WriterLive(time.Now(), spawned, 5*time.Second) {
		t.Error("WriterLive should accept a fresh connection")
	}
	// Far past the live window with no data -> not live.
	if st.WriterLive(time.Now().Add(time.Hour), spawned, time.Second) {
		t.Error("WriterLive should reject a long-silent connection")
	}

	ApplyRecord(st, Record{"v": {"Data:44"}})
	st.Disconnect()
	if st.Snap().Connected {
		t.Error("Disconnect should mark the connection dead")
	}
}

// ---- VolAndPremute ----------------------------------------------------------

func TestCov_VolAndPremute(t *testing.T) {
	st := NewState()
	st.SetVol(60)
	st.SetVol(0) // mute: captures pre-mute level 60
	if v, pre := st.VolAndPremute(); v != 0 || pre != 60 {
		t.Errorf("VolAndPremute = (%d, %d), want (0, 60)", v, pre)
	}
}

// ---- ToggleOptimistic -------------------------------------------------------

func TestCov_ToggleOptimistic(t *testing.T) {
	st := NewState()

	// From playing (0): reports wasPlaying=true and flips to not-playing (2).
	st.mu.Lock()
	st.playing = 0
	st.mu.Unlock()
	if wasPlaying := st.ToggleOptimistic(); !wasPlaying {
		t.Error("ToggleOptimistic from playing should report wasPlaying=true")
	}
	if p := st.Snap().Playing; p != 2 {
		t.Errorf("playing = %d, want 2 after toggling off", p)
	}

	// From not-playing: reports wasPlaying=false and flips to playing (0).
	if wasPlaying := st.ToggleOptimistic(); wasPlaying {
		t.Error("ToggleOptimistic from not-playing should report wasPlaying=false")
	}
	if p := st.Snap().Playing; p != 0 {
		t.Errorf("playing = %d, want 0 after toggling on", p)
	}
}

// ---- pingStat decreasing-sample (jitter abs) --------------------------------

func TestCov_PingStatNegativeDelta(t *testing.T) {
	// A decreasing successive sample exercises the abs branch (d < 0 -> d = -d).
	ps := pingStat([]float64{30, 10, 25})
	if want := 65.0 / 3.0; ps.Avg != want {
		t.Errorf("Avg = %v, want %v", ps.Avg, want)
	}
	// jitter = mean(|10-30|, |25-10|) = mean(20, 15) = 17.5
	if ps.Jitter != 17.5 {
		t.Errorf("Jitter = %v, want 17.5", ps.Jitter)
	}
	if ps.Peak != 30 {
		t.Errorf("Peak = %v, want 30", ps.Peak)
	}
	if !ps.OK {
		t.Error("a populated ring should read OK")
	}
}

// ---- updateNet latency-ring trim --------------------------------------------

func TestCov_UpdateNetRingTrim(t *testing.T) {
	st := NewState()
	t0 := time.Now()
	// Push 35 latency samples; the ring caps at pingRingMax (30), keeping newest.
	for i := range 35 {
		st.updateNet(&SysInfo{PingClient: strconv.Itoa(i)}, t0.Add(time.Duration(i)*time.Second))
	}
	ps := st.DiagnosticView(time.Now()).Net.Ping[0]
	// 35 pushed (0..34), the newest pingRingMax (30) retained -> 5..34: the
	// average proves the trim (untrimmed 0..34 would read 17), the peak the tail.
	if want := 19.5; ps.Avg != want {
		t.Errorf("Avg = %v, want %v (ring trimmed to the newest %d)", ps.Avg, want, pingRingMax)
	}
	if ps.Peak != 34 {
		t.Errorf("Peak = %v, want 34", ps.Peak)
	}
}

// TestCov_WriterLiveDatalessStreak: during an outage every respawn is young, so
// the young-spawn grace must lapse once a connection dies dataless — otherwise
// commands are swallowed into a doomed stdin pipe with no "not delivered" note.
func TestCov_WriterLiveDatalessStreak(t *testing.T) {
	st := NewState()
	st.StartConnection()
	if !st.WriterLive(time.Now(), time.Now(), 5*time.Second) {
		t.Fatal("first young spawn should have the handshake grace")
	}
	st.Disconnect() // died without data
	st.Disconnect() // idempotent: must not double-count the same connection
	st.StartConnection()
	if st.WriterLive(time.Now(), time.Now(), 5*time.Second) {
		t.Error("young-spawn grace should be withheld while the dataless streak runs")
	}

	// Data clears the streak: the writer is live again, and after a later
	// (data-ful) death the next young spawn gets the grace back.
	ApplyRecord(st, Record{"v": {"Data:44"}})
	if !st.WriterLive(time.Now(), time.Time{}, 5*time.Second) {
		t.Error("fresh data should make the writer live")
	}
	st.Disconnect()
	st.StartConnection()
	if !st.WriterLive(time.Now(), time.Now(), 5*time.Second) {
		t.Error("a young spawn after a data-ful session should have the grace")
	}
}

// TestCov_StaleDataDeathArmsStreak: a session that HAD data but died with it
// stale (the mid-outage watchdog kill) must arm the streak immediately, so
// even the first respawn of an outage refuses the young-spawn grace.
func TestCov_StaleDataDeathArmsStreak(t *testing.T) {
	orig := staleDeathAfter
	staleDeathAfter = 0 // any age counts as stale
	defer func() { staleDeathAfter = orig }()

	st := NewState()
	st.StartConnection()
	ApplyRecord(st, Record{"v": {"Data:44"}})
	st.Disconnect() // data present but stale at death
	st.StartConnection()
	if st.WriterLive(time.Now(), time.Now(), 5*time.Second) {
		t.Error("first respawn after a stale-data death should not have the grace")
	}
}
