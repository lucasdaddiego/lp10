// Program lifecycle: Run wires State, the worker goroutines, the media-key tap,
// and the Bubble Tea program together, and tears them down on exit. The model
// itself lives in model.go; see display.go for the package doc.

package tui

import (
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/mediakey"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// startupNote composes the single startup note from the config-load warning
// and the media-key error. State keeps ONE note slot, so the two must share it
// — noted separately, the media-keys note would overwrite an earlier config
// warning before the first paint (exactly on a first run, where Accessibility
// isn't granted yet and a config typo matters most). The config warning leads
// so it survives clipping on a narrow terminal.
func startupNote(warn string, keyErr error) string {
	if keyErr != nil {
		if warn != "" {
			warn += " · "
		}
		warn += "media keys off — " + keyErr.Error()
	}
	return warn
}

// Run wires up State, the worker goroutines, and the Bubble Tea program, then
// tears everything down on exit. Returns the process exit code: 0 clean quit,
// 130 Ctrl-C, 143 SIGTERM/SIGHUP.
func Run(cfg config.Config) (int, error) {
	st := protocol.NewState()
	background := workers.StartRuntime(st, cfg)

	m := newModel(st, cfg, background.Commands, background.EQCommands)
	// The alt screen and window title ride tea.View under bubbletea v2 (see
	// model.View), so the only program-level option left is signal handling,
	// which Run owns below.
	p := tea.NewProgram(m, tea.WithoutSignalHandler())

	// Media transport keys (macOS): drive the device from the keyboard's
	// play/pause, next, and prev even when lp10 isn't focused. The tap only
	// consumes the keys while connected, so they pass through to other apps when
	// the device is away. No-op on non-macOS; best-effort if the tap can't be
	// installed (Accessibility not granted) — note it and carry on.
	stopKeys, keyErr := mediakey.Start(mediakey.Config{
		Connected: func() bool { return st.Snap().Connected },
		OnKey: func(k mediakey.Key) {
			if action, ok := keyToAction(k); ok {
				p.Send(mediaKeyMsg{action: action})
			}
		},
		// Fires only when the tap re-arms after an earlier denial (Accessibility
		// granted mid-session), confirming the keys are now live.
		OnActive: func() { st.Note("media keys on") },
	})
	// Surface the startup problems (broken config, no media-key tap) as ONE
	// note: State keeps a single message slot, so noting them separately would
	// let the later clobber the earlier before the first paint ever showed it.
	if n := startupNote(cfg.Warn, keyErr); n != "" {
		st.Note(n)
	}
	defer stopKeys()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer signal.Stop(sigCh)
	sigCode := &atomic.Int32{}
	sigCode.Store(-1)
	stopSig := make(chan struct{})
	sigDone := make(chan struct{})
	go func() {
		defer close(sigDone)
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGINT {
				sigCode.Store(130)
			} else {
				sigCode.Store(143)
			}
			p.Quit()
		case <-stopSig:
		}
	}()

	finalModel, runErr := p.Run()
	close(stopSig)
	<-sigDone
	// Restore default signal dispositions BEFORE teardown: with the handler
	// goroutine retired, a SIGINT during a slow background.Close (a wedged ssh
	// child riding out the ~4s kill ladder) would otherwise land in the unread
	// channel and be swallowed — leaving Ctrl-C unable to abort the teardown.
	// The terminal is already restored (p.Run returned), and an orphaned child
	// self-heals via fd-close → remote-loop EOF.
	signal.Stop(sigCh)
	stopKeys()

	background.Close(workers.DrainTimeout)
	fmt.Fprint(os.Stdout, "\x1b]0;\x07") // reset the terminal title

	switch {
	case sigCode.Load() >= 0:
		return int(sigCode.Load()), nil
	case runErr != nil:
		return 1, runErr
	default:
		if fm, ok := finalModel.(*model); ok && fm.interrupted {
			return 130, nil
		}
		return 0, nil
	}
}
