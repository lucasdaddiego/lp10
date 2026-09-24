package tui

// Test-only names. The overlay-era aliases (openOverlay, ovServices, ovLogs)
// are what the older tests grew up with; the app itself calls setView and the
// view constants. newTheme is the dark palette the rendering tests build
// directly; the app picks its palette through ensureTheme.

const (
	ovServices = viewServices
	ovLogs     = viewLogs
)

func (m *model) openOverlay(which view) { m.setView(which) }

func newTheme() *theme { return newThemeFor(true) }
