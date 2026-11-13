// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
)

// twoGPSIs is the fixture's task naming a second GPSI beside its first: two values of a
// property a subscriber holds one of, which no subject can match.
func twoGPSIs(body string) string {
	return strings.Replace(body,
		"<ns1:e164Number>2125552368</ns1:e164Number>",
		"<ns1:e164Number>2125552368</ns1:e164Number></ns1:targetIdentifier>"+
			"<ns1:targetIdentifier><ns1:gpsiMsisdn>5551234567</ns1:gpsiMsisdn>", 1)
}

// TestAContradictoryTaskIsRefusedWithoutAnyPOI: every criterion of a task must match, so one
// naming two values of a single-valued property can never match, and TS 103 221-1 table
// 6.2.1.2-1 has the element reject the combination. The refusal is the library's: this server
// has no CanApply, so nothing else could refuse it.
func TestAContradictoryTaskIsRefusedWithoutAnyPOI(t *testing.T) {
	st := store.New()
	srv := testServer(st)

	m := serve(t, srv, twoGPSIs(activateXML))
	// 3020 is the registry's "Unsupported combination of TargetIdentifiers", which is what
	// this is; the generic 3000 would say only that the task failed.
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeBadTargetCombination {
		t.Fatalf("a task naming two GPSIs = %+v, want error %d", m.ErrorInformation, errCodeBadTargetCombination)
	}
	if _, held := st.Get(testXID); held {
		t.Error("a task no subject can match was stored")
	}
	for _, value := range []string{"2125552368", "5551234567"} {
		if strings.Contains(m.ErrorInformation.ErrorDescription, value) {
			t.Errorf("the refusal quotes a subscriber identity: %q", m.ErrorInformation.ErrorDescription)
		}
	}

	// The same rule holds for a modification.
	mustAck(t, srv, activateXML)
	modify := twoGPSIs(strings.Replace(activateXML, "ActivateTaskRequest", "ModifyTaskRequest", 1))
	// The code is the same for a modification: table 6.7-3 lists it for both operations.
	if m := serve(t, srv, modify); m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeBadTargetCombination {
		t.Fatalf("a modification naming two GPSIs = %+v, want error %d", m.ErrorInformation, errCodeBadTargetCombination)
	}
}

// TestAReplayAfterADestinationChangedIsStillTheSameRequest: whether an ActivateTask repeats the
// held task is a question about what the ADMF sent, so the endpoints its DIDs resolve to —
// this element's resolution — do not enter into it. A replay after a destination it names was
// re-provisioned is acknowledged, not refused with 2010.
func TestAReplayAfterADestinationChangedIsStillTheSameRequest(t *testing.T) {
	const did = "c1b2d3e4-1111-4222-8333-944455556666"

	st := store.New()
	srv := testServer(st)
	mustAck(t, srv, createDestinationXML(did, "X2Only", "10.20.30.40", tcpPort("9000"), ""))

	activate := activateXMLWith(string(testXID), "X2Only", dIDs(did), "")
	mustAck(t, srv, activate)

	srv.mu.Lock()
	d := srv.destinations[did]
	d.Address = "10.20.30.41:9000"
	srv.destinations[did] = d
	srv.mu.Unlock()

	if m := serve(t, srv, activate); m.OK != ackOK {
		t.Errorf("a replay after its destination was re-provisioned = %+v, want acknowledged", m.ErrorInformation)
	}
}
