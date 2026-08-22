package protocol

import (
	"strings"
	"testing"
)

func TestValidatePayloadNight(t *testing.T) {
	for _, d := range []string{"0", "1"} {
		if !ValidatePayload(91, d) {
			t.Errorf("91 %q should be valid", d)
		}
	}
	for _, d := range []string{"", "2", "on", "1 ", "-1", "01"} {
		if ValidatePayload(91, d) {
			t.Errorf("91 %q should be rejected", d)
		}
	}
}

func TestReduceCommandsNightLastWins(t *testing.T) {
	got := ReduceCommands([]Command{
		{Mid: 91, Data: "1"}, {Mid: 40, Data: "NEXT"}, {Mid: 91, Data: "0"}, {Mid: 64, Data: "10"},
	})
	if len(got) != 3 || got[0].Mid != 40 || got[1].Mid != 91 || got[1].Data != "0" || got[2].Mid != 64 {
		t.Errorf("reduced = %+v, want [40 NEXT] [91 0] [64 10]", got)
	}
}

func TestParseNight(t *testing.T) {
	cases := []struct {
		in       string
		on, know bool
	}{
		{"numid=68,iface=MIXER,name='AED Multi-band DRC enable'\n  ; type=BOOLEAN,access=rw------,values=1\n  : values=on", true, true},
		{"  : values=off", false, true},
		{"  : values=off\r", false, true}, // printable() strips the CR a device might ship
		{"", false, false},
		{"amixer: Cannot find the given element from control default", false, false},
		{"  ; type=BOOLEAN,access=rw------,values=1", false, false}, // the count line is not a value
		{"  : values=maybe", false, false},
		{"values=on\x1b[31m", false, false}, // escape-stripped, then no longer a clean on
	}
	for _, c := range cases {
		var lines []string
		if c.in != "" {
			lines = strings.Split(c.in, "\n")
		}
		on, know := parseNight(lines)
		if on != c.on || know != c.know {
			t.Errorf("parseNight(%q) = %v,%v want %v,%v", c.in, on, know, c.on, c.know)
		}
	}
}

func TestApplyRecordNightAndBaseline(t *testing.T) {
	st := NewState()
	if _, needed := st.NightRestore(); needed {
		t.Fatal("nothing reported yet: no restore needed")
	}
	rec := Record{"n": {"  : values=off"}}
	ApplyRecord(st, rec)
	s := st.Snap()
	if !s.NightKnown || s.Night {
		t.Fatalf("after @@n off: Night=%v Known=%v", s.Night, s.NightKnown)
	}
	// lp10 switches it on (optimistic), the device echoes on: baseline stays off
	st.SetNightLocal(true)
	if s := st.Snap(); !s.Night {
		t.Error("SetNightLocal(true) should flip the snapshot at once")
	}
	ApplyRecord(st, Record{"n": {"  : values=on"}})
	orig, needed := st.NightRestore()
	if !needed || orig {
		t.Errorf("restore = (orig=%v needed=%v), want off needed", orig, needed)
	}
	// a reconnect re-reads the value lp10 set: still not the baseline
	ApplyRecord(st, Record{"n": {"  : values=on"}})
	if orig, needed := st.NightRestore(); !needed || orig {
		t.Errorf("after reconnect echo: restore = (orig=%v needed=%v), want off needed", orig, needed)
	}
	// back to the baseline: nothing to restore
	ApplyRecord(st, Record{"n": {"  : values=off"}})
	if _, needed := st.NightRestore(); needed {
		t.Error("at the baseline no restore is needed")
	}
	// an @@n-only record carries no player data
	if ApplyRecord(NewState(), rec) {
		t.Error("@@n alone must not count as player data")
	}
}

func TestApplyRecordNightGarbageLeavesState(t *testing.T) {
	st := NewState()
	ApplyRecord(st, Record{"n": {"  : values=on"}})
	ApplyRecord(st, Record{"n": {"garbage"}})
	if s := st.Snap(); !s.NightKnown || !s.Night {
		t.Errorf("an unparseable @@n must not disturb the last good state: %+v", s.Night)
	}
}
