package transport

// Regression tests for the on-device loop's guards (2026-09-23): the login-blob
// filter, a vanished engine, the digest and metadata triggers, the log framing
// and the drain bound. Each runs the fragment the loop actually ships (loopSlice).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The Spotify engine logs "SAME USERNAME IS THERE STORE THE BLOB <blob>" to the
// syslog on every start (TEARDOWN §8.1, §15): the reusable login blob in clear.
// Both MID-93 tails — the syslog (@@l) and the vendor app's log (@@L) — must
// drop every line naming a blob or a username, in any case, ON THE BOX, and
// keep the rest.
func TestLogTailDropsSpotifyLoginBlob(t *testing.T) {
	dir := t.TempDir()
	ml := filepath.Join(dir, "messages.log")
	if err := os.WriteFile(ml, []byte(
		"Sep 12 15:06:58:000001 I/spotifymusicpro[5673]: SPOTIFY: SAME USERNAME IS THERE STORE THE BLOB AQBxSECRETBLOB IN ENV !!\n"+
			"Sep 12 15:06:59:000001 I/spotifymusicpro[5673]: SPOTIFY: DIFF USER LOGGED IN STORE USERNAME someuser AND THE BLOB AQBx IN ENV !!\n"+
			"Sep 12 15:07:00:000001 W/dmr[200]: an ordinary line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	al := filepath.Join(dir, "app.log")
	if err := os.WriteFile(al, []byte(
		"[INFO] zeroconf: addUser username=someuser blob=AQBxSECRET\n"+
			"[DEBUG] spotify: stored Blob for the session\n"+
			"[INFO] tunnel: MCU+PAS+RAKOIT:EQS:1&\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := loopSlice(t, "tl() {", "};")
	for _, c := range []struct{ name, script, keep string }{
		{"@@l syslog", "ml=" + ml + "; " + tl + loopSlice(t, "lg() {", "| tl; };") + " lg", "W/dmr"},
		{"@@L app.log", tl + " tl < " + al, "EQS:1"},
	} {
		out, err := exec.Command("sh", "-c", c.script).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: sh: %v\n%s", c.name, err, out)
		}
		if low := strings.ToLower(string(out)); strings.Contains(low, "blob") || strings.Contains(low, "username") {
			t.Errorf("the %s tail ships the Spotify login off the box:\n%s", c.name, out)
		}
		if !strings.Contains(string(out), c.keep) {
			t.Errorf("the %s tail dropped the ordinary line too:\n%s", c.name, out)
		}
	}
}

// sy() reads the live engine's /proc/<pid>/stat AFTER the comm scan found it.
// If the engine exits in between (a toggle, a netdown/netup relaunch), cat
// prints nothing, ${22} is empty, and $((up-/100)) is an arithmetic syntax
// error: ash aborts the WHOLE loop mid-@@c, which ends the ssh session.
func TestCapabilityBlockSurvivesVanishedEngine(t *testing.T) {
	snip := loopSlice(t, "gv() {", "};ct;")
	dir := t.TempDir()
	// pid 1: comm says spotifymusicpro, but its stat/environ are already gone.
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "1", "comm"), []byte("spotifymusicpro\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tcp"), []byte("  sl  local_address rem_address   st\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tcp6"), []byte("  sl  local_address rem_address   st\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "uptime"), []byte("68180.00 135000.00\n"), 0o644)
	local := strings.NewReplacer(
		"/proc/[0-9]*/comm", filepath.Join(dir, "[0-9]*", "comm"),
		"/proc/net/tcp6", filepath.Join(dir, "tcp6"),
		"/proc/net/tcp", filepath.Join(dir, "tcp"),
		"/proc/uptime", filepath.Join(dir, "uptime"),
	).Replace(snip)
	const stub = `E() { echo @@E; }; getenv() { echo " [ $1 ]: 0"; }; sqlite3() { :; }; `
	out, err := exec.Command("sh", "-c", stub+local).CombinedOutput()
	if err != nil || !strings.HasSuffix(string(out), "@@E\n") {
		t.Errorf("the loop died inside @@c when the engine exited after the comm scan (err=%v):\n%s", err, out)
	}
}

// While the diagnostics overlay stays open the TUI re-asserts "90 1" every
// StatsReassertTicks (~3 s). The digest (ot: three log reads, ~5 forks) belongs
// to the overlay OPENING — the first "90 1" — not to every keep-alive.
func TestStatsReassertDoesNotRerunDigest(t *testing.T) {
	disp := loopSlice(t, `while :; do case "$mid" in`, "read -r -t 1 mid data || break;done;")
	for _, c := range []struct {
		dg   string
		want int
	}{
		{"0", 1}, // the overlay opens: the digest runs once
		{"1", 0}, // already open: this "90 1" is the keep-alive
	} {
		script := `ot() { echo OT; }; nm() { :; }; tg() { :; }; lg() { :; }; E() { :; }; tl() { :; }; ` +
			`dg=` + c.dg + `; read -r mid data; ` + disp
		cmd := exec.Command("sh", "-c", script)
		cmd.Stdin = strings.NewReader("90 1\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sh: %v\n%s", err, out)
		}
		if n := strings.Count(string(out), "OT"); n != c.want {
			t.Errorf("\"90 1\" with dg=%s ran the digest %d time(s), want %d", c.dg, n, c.want)
		}
	}
}

// /lsync/app.log is written live by the vendor app; when its last line has no
// newline yet, tail|sed keeps it that way and the following E() prints "@@E"
// glued onto it. The laptop then never sees the terminator line: the @@L tail
// merges into the next tick's record and its last line shows a stray "@@E".
func TestAppLogTailKeepsFraming(t *testing.T) {
	dir := t.TempDir()
	al := filepath.Join(dir, "app.log")
	os.WriteFile(al, []byte("[INFO] line one\n[INFO] half-written li"), 0o644)
	script := loopSlice(t, "E() {", "};") + loopSlice(t, "tl() {", "};") +
		` echo @@L; tl 2>/dev/null < ` + al + `; E`
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v\n%s", err, out)
	}
	if !strings.HasSuffix(string(out), "\n@@E\n") {
		t.Errorf("the @@L section's terminator is not on its own line: %q", out)
	}
}

// Off the player the loop re-reads metadata only on the 15-tick fallback, and
// the backward-jump detector cannot see a track change that happened while the
// position was not polled (the new track's position is past the old one). On
// "94 1" nothing forced a re-read: the player showed the previous track for up
// to 15 more ticks (measured 9.1 s in a BusyBox run).
func TestReturnToPlayerForcesMetadataRead(t *testing.T) {
	disp := loopSlice(t, `while :; do case "$mid" in`, "read -r -t 1 mid data || break;done;")
	script := `ot() { :; }; nm() { :; }; tg() { :; }; lg() { :; }; E() { :; }; tl() { :; }; ` +
		`i=9; pv=0; read -r mid data; ` + disp + ` echo "pv=$pv i=$i"`
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdin = strings.NewReader("94 1\n")
	// stdout alone is the answer: dash (the Linux CI runner's sh) has no read -t
	// and says so on stderr before the drain breaks.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh: %v\n%s%s", err, out, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != "pv=1 i=0" {
		t.Errorf("after 94 1: %s, want the metadata countdown forced to 0 (pv=1 i=0)\nstderr: %s", got, stderr.String())
	}
}

// A key held on a fast repeat (NEXT, or volume sets the laptop has not yet
// coalesced) can queue commands faster than LUCI_local runs them. The burst
// drain must yield after 8 so the tick reaches E(): a drain that never ends
// emits no @@E, and the laptop's 8 s watchdog kills a healthy session.
func TestCommandDrainYieldsToTheTick(t *testing.T) {
	disp := loopSlice(t, `while :; do case "$mid" in`, "read -r -t 1 mid data || break;done;")
	// The flood: another command is always queued. The drain runs as it ships
	// wherever sh's read -t 0 reports queued input and leaves it for the next
	// read, as BusyBox ash does on the box and under `make busybox`. The hosts'
	// sh cannot run it (bash 3.2 fails -t 0 outright, dash has no -t at all), so
	// there the two drain reads become their portable equivalents.
	if !readPollsQueuedInput(t) {
		disp = strings.NewReplacer("read -r -t 0", ":", "read -r -t 1 mid data", "read -r mid data").Replace(disp)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	script := `LUCI_local() { echo "$1 $2" >> ` + ran + `; }; ot() { :; }; nm() { :; }; tg() { :; }; lg() { :; }; E() { :; }; tl() { :; }; ` +
		`read -r mid data; ` + disp + ` cat`
	var in strings.Builder
	for n := range 20 {
		in.WriteString("64 " + strconv.Itoa(n) + "\n")
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdin = strings.NewReader(in.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(ran)
	if n := strings.Count(string(b), "\n"); n != 8 {
		t.Errorf("one drain ran %d queued commands, want 8 before yielding to the tick", n)
	}
	if left := strings.Count(string(out), "\n"); left != 12 {
		t.Errorf("%d commands left queued for the next tick, want 12:\n%s", left, out)
	}
}

// readPollsQueuedInput reports whether sh's `read -t 0` succeeds on queued
// input and leaves it for the next read, the behaviour the burst drain is
// written for. The input is a file, not a pipe, so it is in place before sh
// starts and the answer never depends on scheduling.
func readPollsQueuedInput(t *testing.T) bool {
	t.Helper()
	q := filepath.Join(t.TempDir(), "queued")
	if err := os.WriteFile(q, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(q)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	cmd := exec.Command("sh", "-c", `read -r -t 0 2>/dev/null && read -r v && [ "$v" = x ]`)
	cmd.Stdin = in
	return cmd.Run() == nil
}
