// Package debuglog is the opt-in frame log for field diagnosis. With
// LP10_DEBUG=<path> the :2018 tunnel appends every chunk it reads or writes
// to that file — one line each: the time, "<" for bytes the box sent or ">"
// for bytes lp10 wrote, and the chunk Go-quoted, so a control byte in a track
// title stays visible and one chunk stays one line. Unset, nothing is opened
// and the connection is not wrapped.
//
// The file is created 0600 (and put back to it when it already existed) and
// holds the tunnel traffic only: status polls, the track, EQ values, lp10's
// own queries and sets. No path, address, config value or environment
// variable goes in — nothing on the tunnel is a credential, and the file
// must stay safe to attach to a bug report as it is.
//
// The log is capped: before a line would take the file past MaxBytes, the
// file is renamed to "<path>.1" (replacing an older one) and a new file
// starts, so a box that flaps for days holds the disk to two files of
// MaxBytes at most.
package debuglog

import (
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Env names the file the frames go to; unset or empty means no log.
const Env = "LP10_DEBUG"

// MaxBytes is the size at which the file rotates to "<path>.1". At one STA
// poll every 2 s a steady session writes about 1 MiB an hour, so 8 MiB keeps
// the last several hours in the two files. A variable for the tests.
var MaxBytes int64 = 8 << 20

// Log is one open frame log. A nil *Log is a log that records nothing, so
// callers never test for it.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	name string // the LP10_DEBUG path, for the rotation
	size int64  // bytes in f
}

// Open opens the file LP10_DEBUG names for appending, creating it 0600, or
// returns nil when the variable is unset or the file cannot be used — a log
// that cannot be opened must not stop the player. Only a regular file is
// accepted: a device or a pipe is not a file to attach to a report.
func Open() *Log {
	p := os.Getenv(Env)
	if p == "" {
		return nil
	}
	f, size, ok := openRegular(p)
	if !ok {
		return nil
	}
	l := &Log{f: f, name: p, size: size}
	if size >= MaxBytes {
		l.rotate() // a file left full by an earlier session
		if l.f == nil {
			return nil
		}
	}
	return l
}

// openRegular opens p for appending (created 0600) and returns it with its
// size, or ok false when p is not a regular file. O_NONBLOCK makes a FIFO
// with no reader fail at once (ENXIO) instead of blocking the tunnel worker
// in the open, where neither quit nor the stop flag could reach it; on a
// regular file the flag changes nothing.
func openRegular(p string) (f *os.File, size int64, ok bool) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, 0, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, 0, false
	}
	if fi.Mode().Perm() != 0o600 {
		_ = f.Chmod(0o600) // a file that existed keeps its mode: this one is for one user
	}
	return f, fi.Size(), true
}

// rotate moves the full file to "<name>.1" and opens a new one. Called with
// mu held. When any step fails the log stops (f nil) rather than grow past
// the cap.
func (l *Log) rotate() {
	l.f.Close()
	l.f = nil
	if err := os.Rename(l.name, l.name+".1"); err != nil {
		return
	}
	if f, size, ok := openRegular(l.name); ok {
		l.f, l.size = f, size
	}
}

// Close closes the file. Frames after it are dropped.
func (l *Log) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// Frame records one chunk of tunnel traffic: dir is "<" for bytes the box
// sent and ">" for bytes lp10 wrote.
func (l *Log) Frame(dir string, b []byte) {
	if l == nil || len(b) == 0 {
		return
	}
	line := time.Now().UTC().Format(time.RFC3339Nano) + " " + dir + " " + strconv.Quote(string(b)) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil && l.size > 0 && l.size+int64(len(line)) > MaxBytes {
		l.rotate() // before the write: each file stays within the cap
	}
	if l.f == nil {
		return
	}
	n, _ := l.f.WriteString(line)
	l.size += int64(n)
}

// Wrap returns c with every Read and Write recorded in l, or c itself when l
// is nil.
func Wrap(c net.Conn, l *Log) net.Conn {
	if l == nil {
		return c
	}
	return &conn{Conn: c, log: l}
}

// conn is a net.Conn whose traffic goes to a Log as well.
type conn struct {
	net.Conn
	log *Log
}

func (c *conn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.log.Frame("<", b[:n])
	}
	return n, err
}

func (c *conn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.log.Frame(">", b[:n])
	}
	return n, err
}
