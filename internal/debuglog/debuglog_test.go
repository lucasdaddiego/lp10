package debuglog

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Unset, Open is nil and Wrap hands the connection back untouched; a nil Log
// takes every call.
func TestUnsetIsNoLog(t *testing.T) {
	t.Setenv(Env, "")
	if l := Open(); l != nil {
		t.Fatal("Open with LP10_DEBUG unset returned a log")
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if w := Wrap(a, nil); w != a {
		t.Error("Wrap with a nil log did not return the connection itself")
	}
	var l *Log
	l.Frame(">", []byte("STA;"))
	l.Close()
}

// Set, every chunk read or written through the wrapped connection lands in
// the file as one quoted line, the file is 0600, and a control byte in the
// traffic stays visible.
func TestFramesLandQuotedInA0600File(t *testing.T) {
	p := filepath.Join(t.TempDir(), "frames.log")
	t.Setenv(Env, p)
	l := Open()
	if l == nil {
		t.Fatal("Open returned nil for a writable path")
	}
	defer l.Close()
	a, b := net.Pipe()
	w := Wrap(a, l)
	go func() { // the box: takes the query, pushes a track whose title holds an ESC
		buf := make([]byte, 64)
		_, _ = b.Read(buf)
		_, _ = b.Write([]byte("TIT:Big\x1bBang;VOL:44;"))
	}()
	if _, err := w.Write([]byte("STA;")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := w.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("read %d, %v", n, err)
	}
	w.Close()
	b.Close()
	l.Close()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 0600", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q, want one per chunk", lines)
	}
	if !strings.HasSuffix(lines[0], ` > "STA;"`) {
		t.Errorf("the write: %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], ` < "TIT:Big\x1bBang;VOL:44;"`) {
		t.Errorf("the read: %q, want the chunk quoted with its control byte visible", lines[1])
	}
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "20") || !strings.Contains(ln, "T") {
			t.Errorf("no timestamp on %q", ln)
		}
	}
}

// A file that already existed is put back to 0600; a path that cannot be
// opened, or is not a regular file, is no log — the player goes on.
func TestExistingFileAndBadPaths(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wide.log")
	if err := os.WriteFile(p, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(Env, p)
	l := Open()
	if l == nil {
		t.Fatal("an existing file was refused")
	}
	l.Frame(">", []byte("STA;"))
	l.Close()
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %o after Open, want 0600", fi.Mode().Perm())
	}
	if raw, _ := os.ReadFile(p); !strings.HasPrefix(string(raw), "old\n") || !strings.HasSuffix(string(raw), ` > "STA;"`+"\n") {
		t.Errorf("appended file = %q", raw)
	}
	t.Setenv(Env, filepath.Join(t.TempDir(), "missing", "dir", "x.log"))
	if l := Open(); l != nil {
		t.Error("an unwritable path returned a log")
	}
	t.Setenv(Env, t.TempDir())
	if l := Open(); l != nil {
		t.Error("a directory returned a log")
	}
}

// At MaxBytes the file moves to "<path>.1" and a new one starts, so a long
// session holds two files of about MaxBytes at most; a second rotation
// replaces the old ".1". An existing file already over the cap rotates at
// Open, so the cap holds across reconnects too.
func TestRotatesAtTheCap(t *testing.T) {
	old := MaxBytes
	MaxBytes = 200
	t.Cleanup(func() { MaxBytes = old })
	p := filepath.Join(t.TempDir(), "frames.log")
	t.Setenv(Env, p)
	l := Open()
	if l == nil {
		t.Fatal("Open returned nil")
	}
	chunk := []byte(strings.Repeat("x", 40)) // one line is ~80 bytes with the stamp
	for range 20 {
		l.Frame("<", chunk)
	}
	l.Close()
	for _, f := range []string{p, p + ".1"} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if fi.Size() > MaxBytes {
			t.Errorf("%s is %d bytes, want at most the %d cap", f, fi.Size(), MaxBytes)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %o, want 0600", f, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(p + ".2"); err == nil {
		t.Error("a third generation was kept")
	}

	if err := os.WriteFile(p, []byte(strings.Repeat("y", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	l = Open()
	if l == nil {
		t.Fatal("Open refused a file over the cap instead of rotating it")
	}
	l.Frame(">", []byte("STA;"))
	l.Close()
	if raw, _ := os.ReadFile(p); strings.Contains(string(raw), "y") || !strings.Contains(string(raw), "STA;") {
		t.Errorf("after Open over the cap the file = %q, want only the new frame", raw)
	}
	if raw, _ := os.ReadFile(p + ".1"); len(raw) != 300 {
		t.Errorf(".1 holds %d bytes, want the 300 rotated out", len(raw))
	}
}

// A FIFO with no reader is refused at once: a blocking open would hang the
// tunnel worker, and quit would then wait on it for ever.
func TestFIFOIsRefusedWithoutBlocking(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	t.Setenv(Env, p)
	got := make(chan *Log, 1)
	go func() { got <- Open() }()
	select {
	case l := <-got:
		if l != nil {
			l.Close()
			t.Error("a FIFO returned a log")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open blocked on a FIFO with no reader")
	}
}
