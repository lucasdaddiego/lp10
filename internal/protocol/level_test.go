package protocol

import "testing"

// sysWith builds a minimal @@s line (the required prefix) with a softvol field.
func sysRec(softvol string) Record {
	line := "1000 0.1 0.1 0.1 100000 200000 2 fw.1 Linux-5 - - - - - - - - - - - - - - - - - - - - - " + softvol
	return Record{"s": {line}, "v": {"MID-Read:64 Data:75 Length:2"}}
}

func TestLevelDesyncTracker(t *testing.T) {
	st := NewState()
	if d := st.DiagnosticView(st.posAt); d.SoftvolOK || d.LevelDesync {
		t.Fatal("nothing sampled yet")
	}
	// in step: vol 75 -> softvol 74 (±1 tolerated)
	for _, sv := range []string{"74", "75", "73"} {
		ApplyRecord(st, sysRec(sv))
		if d := st.DiagnosticView(st.posAt); !d.SoftvolOK || d.LevelDesync {
			t.Errorf("softvol %s vs vol 75 should be in step: %+v", sv, d.LevelDesync)
		}
	}
	// one bad sample: not yet (could straddle a volume change)
	ApplyRecord(st, sysRec("59"))
	if d := st.DiagnosticView(st.posAt); d.LevelDesync || d.Softvol != 59 {
		t.Errorf("one bad sample must not flag; softvol=%d desync=%v", d.Softvol, d.LevelDesync)
	}
	// second consecutive bad sample flags it
	ApplyRecord(st, sysRec("59"))
	if d := st.DiagnosticView(st.posAt); !d.LevelDesync {
		t.Error("two consecutive bad samples should flag the desync")
	}
	// an unread sample ("-") changes nothing; a good one clears the streak
	ApplyRecord(st, sysRec("-"))
	if d := st.DiagnosticView(st.posAt); !d.LevelDesync || d.Softvol != 59 {
		t.Error("an unread sample must leave the tracker alone")
	}
	ApplyRecord(st, sysRec("74"))
	if d := st.DiagnosticView(st.posAt); d.LevelDesync {
		t.Error("a good sample should clear the desync")
	}
	// vol 0 expects softvol 0 (floored, not −1); garbage is ignored
	st2 := NewState()
	ApplyRecord(st2, Record{"s": {sysRec("0")["s"][0]}, "v": {"MID-Read:64 Data:0 Length:1"}})
	ApplyRecord(st2, sysRec("0"))
	if d := st2.DiagnosticView(st2.posAt); d.LevelDesync {
		t.Error("vol 0 / softvol 0 is in step")
	}
	ApplyRecord(st2, sysRec("abc"))
	ApplyRecord(st2, sysRec("-5"))
	if d := st2.DiagnosticView(st2.posAt); !d.SoftvolOK || d.Softvol != 0 {
		t.Errorf("garbage samples must be ignored: %+v", d.Softvol)
	}
}
