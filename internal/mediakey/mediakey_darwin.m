#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#import <pthread.h>
#import "mediakey_darwin.h"
#import "_cgo_export.h"

// System-defined event type + aux-button subtype (from IOKit/hidsystem), the
// channel the keyboard's media transport keys arrive on.
#define NX_SYSDEFINED 14
#define NX_SUBTYPE_AUX_CONTROL_BUTTONS 8

// A single process-wide tap. gMu orders the run-loop thread's writes (install)
// against lp10StopTap reading them from another thread; gStop makes stop
// requests durable — CFRunLoopStop on a loop that is not yet running is a
// no-op, so lp10RunLoop re-checks the flag before every run slice instead of
// trusting the wakeup alone. tapCallback reads gTap without the mutex: it runs
// on the install thread itself, after install completed.
static pthread_mutex_t    gMu   = PTHREAD_MUTEX_INITIALIZER;
static int                gStop = 0;
static CFMachPortRef      gTap  = NULL;
static CFRunLoopSourceRef gSrc  = NULL;
static CFRunLoopRef       gLoop = NULL;

static CGEventRef tapCallback(CGEventTapProxy proxy, CGEventType type,
                              CGEventRef event, void *refcon) {
    // The system disables a tap that times out or is disabled by user input;
    // re-enable and let the event through.
    if (type == kCGEventTapDisabledByTimeout ||
        type == kCGEventTapDisabledByUserInput) {
        if (gTap) CGEventTapEnable(gTap, true);
        return event;
    }
    if (type != NX_SYSDEFINED) {
        return event;
    }
    // The tap thread runs a bare CFRunLoop with no enclosing autorelease pool,
    // so the NSEvent (and the CGEvent it retains) would otherwise accumulate
    // until thread exit — one leak per NX_SYSDEFINED event system-wide.
    BOOL consume = NO;
    @autoreleasepool {
        NSEvent *e = [NSEvent eventWithCGEvent:event];
        if (e != nil && [e subtype] == NX_SUBTYPE_AUX_CONTROL_BUTTONS) {
            long data1   = [e data1];
            int  keyCode = (int)((data1 & 0xFFFF0000) >> 16);
            int  keyState = (int)((data1 & 0x0000FF00) >> 8);
            // goMediaKey applies the shared classify/decide logic (Go) and
            // returns 1 to consume the event, 0 to pass it through.
            consume = goMediaKey(keyCode, keyState) != 0;
        }
    }
    return consume ? NULL : event;
}

int lp10InstallTap(void) {
    CGEventMask mask = CGEventMaskBit(NX_SYSDEFINED);
    CFMachPortRef tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                                         kCGEventTapOptionDefault, mask, tapCallback, NULL);
    if (tap == NULL) {
        return 0; // almost always: Accessibility permission not granted
    }
    CFRunLoopSourceRef src  = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0);
    CFRunLoopRef       loop = CFRunLoopGetCurrent();
    CFRunLoopAddSource(loop, src, kCFRunLoopCommonModes);
    CGEventTapEnable(tap, true);
    pthread_mutex_lock(&gMu);
    gTap  = tap;
    gSrc  = src;
    gLoop = loop;
    pthread_mutex_unlock(&gMu);
    return 1;
}

void lp10RunLoop(void) {
    // Bounded slices, re-checking the stop flag before each: a stop that landed
    // between the caller's pre-check and entry here was a no-op CFRunLoopStop
    // on a not-yet-running loop, and CFRunLoopRun would then block this locked
    // thread forever. A stop mid-slice wakes the loop early (RunInMode returns
    // stopped) and the next flag check exits.
    for (;;) {
        pthread_mutex_lock(&gMu);
        int stopped = gStop;
        pthread_mutex_unlock(&gMu);
        if (stopped) {
            return;
        }
        CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.5, false);
    }
}

void lp10StopTap(void) {
    pthread_mutex_lock(&gMu);
    gStop = 1;
    CFMachPortRef tap  = gTap;
    CFRunLoopRef  loop = gLoop;
    pthread_mutex_unlock(&gMu);
    if (tap)  CGEventTapEnable(tap, false);
    if (loop) CFRunLoopStop(loop);
}
