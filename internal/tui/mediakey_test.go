package tui

import (
	"slices"
	"testing"

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
