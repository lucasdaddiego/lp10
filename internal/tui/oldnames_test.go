package tui

// Test-only names. newTheme is the dark palette the rendering tests build
// directly; the app picks its palette through ensureTheme.

func newTheme() *theme { return newThemeFor(true) }
