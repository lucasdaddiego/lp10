package workers

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// rx is one frame the fake box received: its text without the ';', its
// position in the box's log, the connection it came on (0 = the first one
// accepted) and when it arrived.
type rx struct {
	frame string
	seq   int
	conn  int
	at    time.Time
}

// fakeBox is a loopback stand-in for the LP10's :2018 control tunnel. It logs
// every frame a client sends, answers each through answer (nil: a box that
// accepts and never answers, like the accepted-but-never-served connection
// tcptunnelling sometimes hands out), and runs the hook a test registered for
// a frame before answering it.
type fakeBox struct {
	addr   string
	ln     net.Listener
	answer func(frame string) string

	mu       sync.Mutex
	conns    []*boxConn
	open     int
	log      []rx
	hooks    map[string]func(*boxConn)
	onAccept func(*boxConn)
	changed  chan struct{} // closed and replaced on every log or connection change
	closed   bool
	stop     chan struct{}
	wg       sync.WaitGroup
}

// boxConn is one accepted client connection.
type boxConn struct {
	id  int
	c   net.Conn
	wmu sync.Mutex // answers and pushes write from different goroutines
}

func newFakeBox(t *testing.T, answer func(string) string) *fakeBox {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBox{
		addr: ln.Addr().String(), ln: ln, answer: answer,
		hooks: map[string]func(*boxConn){}, changed: make(chan struct{}), stop: make(chan struct{}),
	}
	b.wg.Add(1)
	go b.accept()
	t.Cleanup(b.close)
	return b
}

func (b *fakeBox) accept() {
	defer b.wg.Done()
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			c.Close()
			continue
		}
		bc := &boxConn{id: len(b.conns), c: c}
		b.conns = append(b.conns, bc)
		b.open++
		hook := b.onAccept
		b.notifyLocked()
		b.mu.Unlock()
		if hook != nil {
			hook(bc)
		}
		b.wg.Add(1)
		go b.serve(bc)
	}
}

func (b *fakeBox) serve(bc *boxConn) {
	defer b.wg.Done()
	defer func() {
		bc.c.Close()
		b.mu.Lock()
		b.open--
		b.notifyLocked()
		b.mu.Unlock()
	}()
	buf := make([]byte, 1024)
	var partial string
	for {
		n, err := bc.c.Read(buf)
		if n > 0 {
			partial += string(buf[:n])
			for {
				frame, rest, ok := strings.Cut(partial, ";")
				if !ok {
					break
				}
				partial = rest
				b.handle(bc, frame)
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *fakeBox) handle(bc *boxConn, frame string) {
	b.mu.Lock()
	b.log = append(b.log, rx{frame: frame, seq: len(b.log), conn: bc.id, at: time.Now()})
	hook := b.hooks[frame]
	b.notifyLocked()
	b.mu.Unlock()
	if hook != nil {
		hook(bc)
	}
	if b.answer != nil {
		if reply := b.answer(frame); reply != "" {
			bc.send(reply)
		}
	}
}

func (b *fakeBox) notifyLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// close stops the box: the listener, every connection, every goroutine.
func (b *fakeBox) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.stop)
	b.ln.Close()
	for _, bc := range b.conns {
		bc.c.Close()
	}
	b.mu.Unlock()
	b.wg.Wait()
}

// on runs hook (in the box's reader, before the answer) whenever frame arrives.
func (b *fakeBox) on(frame string, hook func(*boxConn)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hooks[frame] = hook
}

// resetOnAccept makes the box reset every connection the moment it is
// accepted.
func (b *fakeBox) resetOnAccept() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onAccept = (*boxConn).reset
}

// push writes raw frames to every connection, as the device pushes a change.
func (b *fakeBox) push(frames string) {
	b.mu.Lock()
	conns := append([]*boxConn(nil), b.conns...)
	b.mu.Unlock()
	for _, bc := range conns {
		bc.send(frames)
	}
}

// pushEvery pushes frames to every connection every d until the box closes.
func (b *fakeBox) pushEvery(d time.Duration, frames string) {
	b.wg.Go(func() {
		tk := time.NewTicker(d)
		defer tk.Stop()
		for {
			select {
			case <-b.stop:
				return
			case <-tk.C:
				b.push(frames)
			}
		}
	})
}

// dropConns closes every connection from the box's side (a FIN, not a reset).
func (b *fakeBox) dropConns() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, bc := range b.conns {
		bc.c.Close()
	}
}

// accepted is how many connections the box has accepted; openConns how many
// are still open.
func (b *fakeBox) accepted() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.conns)
}

func (b *fakeBox) openConns() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

// frames is a copy of every frame received so far, in arrival order.
func (b *fakeBox) frames() []rx {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]rx(nil), b.log...)
}

// waitFrame waits up to timeout for the first logged frame match accepts,
// failing the test (with what did arrive) when none does.
func (b *fakeBox) waitFrame(t *testing.T, timeout time.Duration, what string, match func(rx) bool) rx {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		b.mu.Lock()
		for _, f := range b.log {
			if match(f) {
				b.mu.Unlock()
				return f
			}
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("fake box: no %s within %v; received %q", what, timeout, texts(b.frames()))
		}
	}
}

// is matches one exact frame.
func is(frame string) func(rx) bool {
	return func(f rx) bool { return f.frame == frame }
}

func texts(fs []rx) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.frame
	}
	return out
}

func (bc *boxConn) send(s string) {
	bc.wmu.Lock()
	defer bc.wmu.Unlock()
	_ = bc.c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = bc.c.Write([]byte(s))
}

// reset aborts the connection with a TCP RST (linger 0): the client's next
// write fails at once instead of landing in a buffer nobody reads.
func (bc *boxConn) reset() {
	if tc, ok := bc.c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	bc.c.Close()
}

// liveBox answers like the live box (MCU 29, verified 2026-10-01): the getters
// read one consistent device state, a set changes it and echoes the applied
// value, POP toggles play and pushes the new play state (with the vendor on a
// resume), NXT pushes RAW:NEXT. Unknown codes get no answer.
type liveBox struct {
	mu      sync.Mutex
	source  string
	vol     int
	muted   bool
	playing bool
	eq      map[string]int
}

func newLiveBox() *liveBox {
	return &liveBox{source: "NET", vol: 44, eq: map[string]int{
		"MXV": 100, "EQE": 1, "EQS": 0, "BAS": 0, "MID": 0, "TRE": 0, "VBS": 0, "VBI": 50, "BAL": 0,
	}}
}

const livePresets = "PEQ:0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal;"

func (d *liveBox) answer(frame string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	code, val, isSet := strings.Cut(frame, ":")
	if !isSet {
		switch code {
		case "STA":
			return d.statusLocked()
		case "VOL":
			return fmt.Sprintf("VOL:%d;", d.vol)
		case "MUT":
			return fmt.Sprintf("MUT:%d;", bit(d.muted))
		case "PLA":
			return fmt.Sprintf("PLA:%d;", bit(d.playing))
		case "SRC":
			return "SRC:" + d.source + ";"
		case "VER":
			return "VER:29-1d316f0c-10;"
		case "PEQ":
			return livePresets
		case "POP":
			d.playing = !d.playing
			if d.playing {
				return "PLA:1;VND:spotify;"
			}
			return "PLA:0;"
		case "NXT":
			return "RAW:NEXT;"
		}
		if v, ok := d.eq[code]; ok {
			return fmt.Sprintf("%s:%d;", code, v)
		}
		return ""
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return ""
	}
	switch code {
	case "VOL":
		d.vol = n
		return fmt.Sprintf("VOL:%d;", n)
	case "MUT":
		d.muted = n == 1
		return fmt.Sprintf("MUT:%d;", bit(d.muted))
	}
	if _, ok := d.eq[code]; ok {
		d.eq[code] = n
		return fmt.Sprintf("%s:%d;", code, n)
	}
	return ""
}

// change applies fn to the device state (a knob turn, the phone app), so the
// next STA reply reports it; the test then pushes what the box would.
func (d *liveBox) change(fn func(d *liveBox)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}

// status is the STA frame for the current device state.
func (d *liveBox) status() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statusLocked()
}

func (d *liveBox) statusLocked() string {
	return fmt.Sprintf("STA:%s,%d,%d,0,0,3,0,%d,1,0;", d.source, bit(d.muted), d.vol, bit(d.playing))
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}
