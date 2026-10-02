package tui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lucasdaddiego/lp10/internal/mediakey"
)

func TestKeyToAction(t *testing.T) {
	cases := []struct {
		k    mediakey.Key
		want string
		ok   bool
	}{
		{mediakey.PlayPause, "toggle", true},
		{mediakey.Next, "next", true},
		{mediakey.Prev, "prev", true},
		{mediakey.KeyNone, "", false},
	}
	for _, c := range cases {
		if got, ok := keyToAction(c.k); got != c.want || ok != c.ok {
			t.Errorf("keyToAction(%v) = (%q,%v), want (%q,%v)", c.k, got, ok, c.want, c.ok)
		}
	}
}

// A mediaKeyMsg must run the mapped action through do() on the update loop:
// each transport key reaches the tunnel as its own code.
func TestMediaKeyMsgRunsAction(t *testing.T) {
	m, _, collect := makeModel(t)
	for action, want := range map[string]string{"next": "NXT", "prev": "PRE", "toggle": "POP"} {
		m.Update(mediaKeyMsg{action: action})
		if got := wire(collect()); !slices.Equal(got, []string{want}) {
			t.Errorf("media %s -> %v, want [%s]", action, got, want)
		}
	}
}

// The tap re-arming mid-session (Accessibility granted) is good news: "media
// keys on" prints on the notice line in the plain pen, and the red error line
// stays clear.
func TestMediaKeysOnIsANoticeNotAnError(t *testing.T) {
	m, st, _ := makeModel(t)
	var sent []tea.Msg
	mediaKeyConfig(st, func(msg tea.Msg) { sent = append(sent, msg) }).OnActive()
	for _, msg := range sent {
		m.Update(msg)
	}
	if e := st.Snap().Error; e != "" {
		t.Errorf("the error slot holds %q", e)
	}
	if m.notice != "media keys on" || m.noticeWarn {
		t.Errorf("notice %q (warn %v), want \"media keys on\" in the plain pen", m.notice, m.noticeWarn)
	}
}
