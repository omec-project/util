// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// statusOf asks the element for its own status the way a provisioning function does, and
// returns the rendered answer.
func statusOf(t *testing.T, srv *Server) string {
	t.Helper()
	resp, err := srv.Process(request("GetNEStatusRequest", ""), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	out, err := marshalResponse(resp)
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}

	return string(out)
}

// TestNEStatusReportsConditionsThatHoldNow is the property the whole design rests on: the
// answer is computed when the question is asked.
//
// Every alternative — retaining pushed faults, expiring them, clearing them explicitly — can
// report a fault that has gone, or fail to report one that arrived. This cannot, and the test
// changes the condition *between* two requests to prove it.
func TestNEStatusReportsConditionsThatHoldNow(t *testing.T) {
	broken := false
	srv := NewServer(store.New(), "neID", WithFaultProbes(func() *X1Error {
		if !broken {
			return nil
		}

		return &X1Error{ErrorCode: 1000, ErrorDescription: "the mediation function is unreachable"}
	}))
	srv.now = func() time.Time { return zeroTailInstant }

	if got := statusOf(t, srv); !strings.Contains(got, "<ns1:neStatus>OK</ns1:neStatus>") {
		t.Errorf("with nothing wrong, want OK\ngot:\n%s", got)
	}

	broken = true
	got := statusOf(t, srv)
	if !strings.Contains(got, "<ns1:neStatus>Faults</ns1:neStatus>") {
		t.Errorf("with a condition holding, want Faults\ngot:\n%s", got)
	}
	if !strings.Contains(got, "the mediation function is unreachable") {
		t.Errorf("the condition is not listed\ngot:\n%s", got)
	}

	// Nothing clears it. The condition simply stops holding, and the next answer reflects
	// that — which is the point of computing rather than retaining.
	broken = false
	if got := statusOf(t, srv); !strings.Contains(got, "<ns1:neStatus>OK</ns1:neStatus>") {
		t.Errorf("once the condition cleared, want OK with no explicit clear\ngot:\n%s", got)
	}
}

// TestTaskWithNoResolvedDestinationIsNotAnElementFault guards a probe that was shipped and
// withdrawn, because re-adding it is the obvious thing to do while reading this package.
//
// "The element holds tasking that no destination resolves for" looks like a condition this
// package can observe alone, and it is — but it is not a *fault*, in any POI this library
// serves. The AMF delivers X2 to the MDF2 in its own configuration; the SMF provisions its
// configured MDF3 at the CC-POI; and the UPF, the one element that does deliver to a task's
// resolved destinations, refuses a content task without one before it is stored. So every
// ordinary task an ADMF provisions without DIDs satisfied the condition, and a deployed AMF
// answered "Faults" while delivering every record correctly.
//
// It was caught by running against a deployment and not by any test here, which is the
// interesting part: the condition is about how the *network functions* deliver, and this
// package cannot see that.
func TestTaskWithNoResolvedDestinationIsNotAnElementFault(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")
	srv.now = func() time.Time { return zeroTailInstant }

	// Exactly what an ADMF provisions when it names no destinations, which is the documented
	// and supported case for an IRI-POI.
	st.Activate(types.InterceptTask{
		XID:      testXID,
		Targets:  []types.TargetIdentifier{{Type: types.TargetSUPI, Value: "262019876543210"}},
		Products: []types.ProductType{types.ProductIRI},
		// No Deliveries: the POI has an MDF address of its own.
	})

	if got := statusOf(t, srv); !strings.Contains(got, "<ns1:neStatus>OK</ns1:neStatus>") {
		t.Errorf("an ordinary task with no resolved destination made the element report itself "+
			"faulty; delivery does not come from the task in any POI this library serves\ngot:\n%s", got)
	}
}

// TestTaskFaultsStayOutOfTheElementStatus keeps the two levels apart, which TS 103 221-1
// separates explicitly: NE status is "OK" or "Faults i.e. NE losing traffic", and those are
// "separate from delivery faults which are reported per XID".
//
// A single warrant's delivery failing must not make the element report itself broken, or an
// ADMF cannot tell "this element is losing traffic" from "one of my warrants is not arriving" —
// and those need different responses.
func TestTaskFaultsStayOutOfTheElementStatus(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")
	srv.now = func() time.Time { return zeroTailInstant }

	// A perfectly deliverable task, and no task-fault supplier registered — which is the
	// configuration this assertion is about. Nothing task-level belongs in the element's own
	// status, and an element that says nothing about a task must still say it in the form the
	// schema defines.
	st.Activate(types.InterceptTask{
		XID:      testXID,
		Targets:  []types.TargetIdentifier{{Type: types.TargetSUPI, Value: "262019876543210"}},
		Products: []types.ProductType{types.ProductIRI},
		Deliveries: []types.DeliveryEndpoint{
			{Type: types.DeliveryX2, Address: "10.0.60.122:42069"},
		},
	})

	if got := statusOf(t, srv); !strings.Contains(got, "<ns1:neStatus>OK</ns1:neStatus>") {
		t.Errorf("a task-level concern must not make the element faulty\ngot:\n%s", got)
	}

	// And the task's own fault list is empty, because nothing supplied one. Task faults reach
	// this list per XID when an element registers WithTaskFaults; what is asserted here is the
	// other half — that the element's *own* status is not where they arrive.
	resp, err := srv.Process(request("GetTaskDetailsRequest",
		"\n    <ns1:xId>"+string(testXID)+"</ns1:xId>"), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	out, err := marshalResponse(resp)
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}
	if !strings.Contains(string(out), "<ns1:listOfFaults/>") {
		t.Errorf("taskStatus should carry an empty fault list\ngot:\n%s", out)
	}
}

// TestMDFUnreachableProbeAnswersBothEdges covers the probe every POI registers, at the level
// this package is responsible for: what the element *says* when its shipper reports a
// destination it cannot reach.
//
// All three assertions matter and two of them are about the probe staying quiet. A probe
// stuck on makes every element report itself faulty — noticed at once, and it discredits the
// field — and a probe stuck off leaves an element that has been losing product for hours
// answering that nothing is wrong, which is invisible and the reason this answer exists.
func TestMDFUnreachableProbeAnswersBothEdges(t *testing.T) {
	unreachable := 0
	srv := NewServer(store.New(), "neID", WithFaultProbes(MDFUnreachableProbe(
		func() (int, int) { return unreachable, 3 })))
	srv.now = func() time.Time { return zeroTailInstant }

	if got := statusOf(t, srv); !strings.Contains(got, "<ns1:neStatus>OK</ns1:neStatus>") {
		t.Errorf("with every destination reachable, want OK\ngot:\n%s", got)
	}

	unreachable = 2
	got := statusOf(t, srv)
	if !strings.Contains(got, "<ns1:neStatus>Faults</ns1:neStatus>") {
		t.Errorf("with delivery failing, want Faults\ngot:\n%s", got)
	}
	// The condition leads the description because it is the only field of this answer where
	// it may appear, and an ADMF has to be able to tell one fault from another.
	if !strings.Contains(got, NEIssueMDFUnreachable) {
		t.Errorf("the answer does not name the condition\ngot:\n%s", got)
	}
	// How much is wrong, so an ADMF can tell one failing destination from all of them.
	if !strings.Contains(got, "2 of 3") {
		t.Errorf("the answer does not say how much is wrong\ngot:\n%s", got)
	}

	// Nothing clears it: the shipper stops reporting the condition and the next answer
	// reflects that.
	unreachable = 0
	if got := statusOf(t, srv); !strings.Contains(got, "<ns1:neStatus>OK</ns1:neStatus>") {
		t.Errorf("once delivery recovered, want OK with nothing having cleared the fault\ngot:\n%s", got)
	}
}

// TestNEFaultCarriesNoIdentity keeps the NE-level answer at NE level.
//
// The probe is handed counts and never the destinations themselves, so an address cannot
// reach the description by accident — this asserts the property rather than trusting the
// signature, because it is the one an author adding a probe is most likely to break while
// trying to be helpful.
func TestNEFaultCarriesNoIdentity(t *testing.T) {
	fault := MDFUnreachableProbe(func() (int, int) { return 1, 2 })()
	if fault == nil {
		t.Fatal("a destination that cannot be reached reported no fault")
	}
	for _, identity := range []string{"10.0.60.122", "42069", string(testXID), "262019876543210"} {
		if strings.Contains(fault.ErrorDescription, identity) {
			t.Errorf("the fault names %q; an element's own status says how much is wrong, never whose",
				identity)
		}
	}
	if fault.ErrorCode == 0 {
		t.Error("the fault carries no error code, which the schema makes mandatory in an unresolvedFault")
	}
}

// TestAnElementHoldingNoTaskingIsNotFaulty guards the next plausible probe, which is
// "taskingAbsent" -- a condition this package *can* observe at any moment, and which still
// must not appear here.
//
// Most elements hold no tasking most of the time. Reporting it would make "Faults" the normal
// answer and the field worthless, and an ADMF could no longer tell an element that is losing
// traffic from one that is simply not tasked. What holding nothing means is meant for the
// push at startup, where it says something else entirely: this element has *lost* the tasking
// it held, which is a transition rather than a state.
func TestAnElementHoldingNoTaskingIsNotFaulty(t *testing.T) {
	srv := NewServer(store.New(), "neID")
	srv.now = func() time.Time { return zeroTailInstant }

	got := statusOf(t, srv)
	if !strings.Contains(got, "<ns1:neStatus>OK</ns1:neStatus>") {
		t.Errorf("an element holding no tasking reported itself faulty; that is the normal "+
			"state of most elements, and reporting it makes the answer worthless\ngot:\n%s", got)
	}
	if strings.Contains(got, NEIssueTaskingAbsent) {
		t.Errorf("the status answer carries %s; it is pushed once at startup, where it means "+
			"tasking was lost, and is not a condition of the element now\ngot:\n%s",
			NEIssueTaskingAbsent, got)
	}
}
