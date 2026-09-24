package protocol

import (
	"testing"
	"time"
)

// A failed reg-5 read at connect leaves the stream's firmware "." or ".23"
// for the whole connection. TakeOTARequest used to hand the worker the empty
// build that makes (an "unrecognised firmware string" verdict) instead of
// falling back to the LSSDP answer it already held.
func TestOTARequestFallsBackWhenRegFiveFailed(t *testing.T) {
	st := NewState()
	st.SetLSSDP(&LSSDPInfo{FW: "AR241CE_8530.23.2"})
	ApplyRecord(st, Record{"s": {"692428.1 0.4 0.3 0.2 135000 215000 2 .23 Linux-5.15.137"}})
	st.RequestOTA()
	build, ok := st.TakeOTARequest()
	if !ok || build != "AR241CE_8530" {
		t.Errorf("TakeOTARequest = (%q, %v), want (\"AR241CE_8530\", true) from LSSDP", build, ok)
	}
}

// With no build-shaped firmware anywhere yet, the request is held rather than
// handed over: LSSDP may still answer, and a held request keeps the view on
// "checking…" instead of a failure nothing retries.
func TestOTARequestHeldUntilABuildHasItsShape(t *testing.T) {
	st := NewState()
	ApplyRecord(st, Record{"s": {"692428.1 0.4 0.3 0.2 135000 215000 2 . Linux-5.15.137"}})
	st.RequestOTA()
	if build, ok := st.TakeOTARequest(); ok {
		t.Errorf("handed over %q with no build known", build)
	}
	if !st.DiagnosticView(time.Now()).OTAPending {
		t.Error("the held request must stay pending")
	}
	st.SetLSSDP(&LSSDPInfo{FW: "AR241CE_8530.23.2"})
	if build, ok := st.TakeOTARequest(); !ok || build != "AR241CE_8530" {
		t.Errorf("once LSSDP answers: (%q, %v)", build, ok)
	}
}

// A request stays pending until the vendor's answer lands, not only until the
// worker takes it: the manifest round trip runs up to its timeout, and the
// view has to keep saying "checking…" through it.
func TestOTARequestPendingWhileInFlight(t *testing.T) {
	st := NewState()
	st.SetLSSDP(&LSSDPInfo{FW: "AR241CE_8530.23.2"})
	st.RequestOTA()
	if _, ok := st.TakeOTARequest(); !ok {
		t.Fatal("the request should be handed over")
	}
	if !st.DiagnosticView(time.Now()).OTAPending {
		t.Error("taken but unanswered: the check is in flight and must read pending")
	}
	st.SetOTA(OTAInfo{At: time.Now(), Asked: "AR241CE_8530", UpToDate: true})
	if st.DiagnosticView(time.Now()).OTAPending {
		t.Error("answered: nothing is pending any more")
	}
	// a second u during a check survives the first answer
	st.RequestOTA()
	if _, ok := st.TakeOTARequest(); !ok {
		t.Fatal("the second request should be handed over")
	}
	st.RequestOTA()
	st.SetOTA(OTAInfo{At: time.Now(), Asked: "AR241CE_8530", UpToDate: true})
	if !st.DiagnosticView(time.Now()).OTAPending {
		t.Error("a request raised mid-check was lost with the answer")
	}
}
