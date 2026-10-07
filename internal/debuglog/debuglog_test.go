package debuglog

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
