//go:build darwin

package mediakey

/*
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics -framework CoreFoundation
#include "mediakey_darwin.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"time"
)

// Handlers set once by Start and read by the C tap callback via goMediaKey.
// There is a single process-wide tap, so a singleton is sufficient; they are
// written before the tap is installed and only read thereafter.
var (
	onKey     func(Key)
	connected func() bool
)

// retryInterval is how often Start re-attempts installing the tap while it is
// denied, so granting Accessibility takes effect without restarting lp10.
const retryInterval = 3 * time.Second

// nxKeyDown is the data1 key-state nibble for a press (0x0B is a release). It
// lives here rather than in mediakey.go because goMediaKey is its only user and
// that function is darwin-only — declared in the portable file it reads as dead
// code on every other GOOS, which staticcheck flags as U1000.
const nxKeyDown = 0x0A

// goMediaKey is called from the C tap callback (mediakey_darwin.m) for each
// system-defined aux-button event, on the tap's run-loop thread. It returns 1 to
// consume the event and 0 to pass it through.
//
//export goMediaKey
func goMediaKey(keyCode C.int, keyState C.int) C.int {
	k := classify(int(keyCode))
	if k == KeyNone {
		// Not a transport key. Volume/brightness ride the same NX_SYSDEFINED
		// mask, so this path sits on the event-delivery latency of every aux
		// button system-wide — return before connected() takes the State lock
		// and allocates a snapshot for an event we always pass through
		// (decide(KeyNone, …) is unconditionally act=false, swallow=false).
		return 0
	}
	conn := false
	if connected != nil {
		conn = connected()
	}
	act, swallow := decide(k, int(keyState) == nxKeyDown, conn)
	if act && onKey != nil {
		onKey(k)
	}
	if swallow {
		return 1
	}
	return 0
}

// Start installs the media-key tap and runs its CFRunLoop on a dedicated locked
// OS thread. It returns a stop function and an error: the error is non-nil when
// the FIRST install attempt fails — almost always because Accessibility has not
// been granted to the host terminal. Start then keeps retrying every retryInterval
// in the background, so once the permission is granted the tap comes up on its own
// with no restart (firing cfg.OnActive on that recovery). stop() cancels the retry
// loop and tears down a running tap.
func Start(cfg Config) (func(), error) {
	onKey = cfg.OnKey
	connected = cfg.Connected

	stop := make(chan struct{})
	first := make(chan bool, 1) // result of the very first install attempt

	go func() {
		// The tap source and the run loop that services it must live on the same
		// OS thread, so every install attempt and the run loop run here, pinned.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		attempts := 0
		sentFirst := false
		for {
			ok := C.lp10InstallTap() != 0
			if !sentFirst {
				first <- ok
				sentFirst = true
			}
			if ok {
				if attempts > 0 && cfg.OnActive != nil {
					cfg.OnActive() // re-armed after an earlier denial
				}
				// Fast path for a stop that already landed. The authoritative
				// guard is the C-side stop flag: lp10RunLoop re-checks it
				// before every run slice, so a stop landing between this
				// select and the run-loop entry (where CFRunLoopStop is a
				// no-op on a not-yet-running loop) still exits promptly.
				select {
				case <-stop:
					C.lp10StopTap()
					return
				default:
				}
				C.lp10RunLoop() // blocks until lp10StopTap
				return
			}
			attempts++
			select {
			case <-stop:
				return
			case <-time.After(retryInterval):
			}
		}
	}()

	var once sync.Once
	stopFn := func() {
		once.Do(func() {
			close(stop)     // breaks the retry wait
			C.lp10StopTap() // breaks a running CFRunLoop (a no-op if not installed)
		})
	}

	if <-first {
		return stopFn, nil
	}
	return stopFn, errors.New("grant Accessibility to your terminal in System Settings; it re-arms automatically")
}
