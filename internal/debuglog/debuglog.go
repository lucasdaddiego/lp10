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
package debuglog

import (
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// Env names the file the frames go to; unset or empty means no log.
const Env = "LP10_DEBUG"

// Log is one open frame log. A nil *Log is a log that records nothing, so
// callers never test for it.
type Log struct {
	mu sync.Mutex
	f  *os.File
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
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil
	}
	if fi.Mode().Perm() != 0o600 {
		_ = f.Chmod(0o600) // a file that existed keeps its mode: this one is for one user
	}
	return &Log{f: f}
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
	if l.f != nil {
		_, _ = l.f.WriteString(line)
	}
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
