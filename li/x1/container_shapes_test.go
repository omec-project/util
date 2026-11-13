// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"fmt"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// Each test below builds an element whose every interrogation answer lists a known number of
// entries of one kind, just over half the bound, and sends three of them in one container. The
// first two are answered — the bound is checked before each answer, and after one the
// container is under it — and the third is refused, because two answers have reached it. The
// count of entries is the test's own, from what it provisioned, so an answer shape the bound
// fails to count shows up as a third answer, not as agreement between the code and itself.
const overHalf = maxProjectedEntries/2 + 1

// assertTwoAnsweredThenRefused checks the three answers to a container of three interrogations
// whose answers each list perAnswer entries, counted by entries.
func assertTwoAnsweredThenRefused(t *testing.T, resp *X1Response, perAnswer int, entries func(X1ResponseMessage) int) {
	t.Helper()

	if len(resp.Messages) != 3 {
		t.Fatalf("%d answers to 3 messages", len(resp.Messages))
	}
	for i, m := range resp.Messages[:2] {
		if m.ErrorInformation != nil {
			t.Fatalf("answer %d refused: %+v", i, *m.ErrorInformation)
		}
		if n := entries(m); n != perAnswer {
			t.Fatalf("answer %d lists %d entries, want %d", i, n, perAnswer)
		}
	}
	if m := resp.Messages[2]; m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeGeneric {
		t.Errorf("the third interrogation was answered after the container had projected %d entries, "+
			"over the bound of %d: the bound does not count this answer's entries", 2*perAnswer, maxProjectedEntries)
	}
}

// TestTheBoundCountsDestinations: GetAllDestinationDetails projects destinations, not tasks.
func TestTheBoundCountsDestinations(t *testing.T) {
	dests := make([]ConfiguredDestination, overHalf)
	for i := range dests {
		dests[i] = ConfiguredDestination{
			DID:          fmt.Sprintf("d0000000-0000-4000-8000-%012d", i),
			DeliveryType: "X2Only",
			Address:      "192.0.2.1:9000",
		}
	}

	// Whatever the test server holds of its own, counted from an element with nothing configured.
	base, err := testServer(store.New()).Process(containerOf("GetAllDestinationDetailsRequest"), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	own := len(base.Messages[0].Destinations)

	srv := testServer(store.New(), WithConfiguredDestinations(dests...))
	resp, err := srv.Process(containerOf(repeated("GetAllDestinationDetailsRequest", 3)...), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	assertTwoAnsweredThenRefused(t, resp, own+overHalf, func(m X1ResponseMessage) int { return len(m.Destinations) })
}

// TestTheBoundCountsElementFaults: GetNEStatus projects the element's faults.
func TestTheBoundCountsElementFaults(t *testing.T) {
	probes := make([]FaultProbe, overHalf)
	for i := range probes {
		probes[i] = func() *X1Error { return NEFault(NEIssueMDFUnreachable, fmt.Sprintf("fault %d", i)) }
	}

	srv := testServer(store.New(), WithFaultProbes(probes...))
	resp, err := srv.Process(containerOf(repeated("GetNEStatusRequest", 3)...), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	assertTwoAnsweredThenRefused(t, resp, overHalf, func(m X1ResponseMessage) int { return len(m.Faults) })
}

// TestTheBoundCountsTaskFaults: a task's own faults are entries in its answer, as many as the
// element supplies, beside the one task.
func TestTheBoundCountsTaskFaults(t *testing.T) {
	const xid = "50b93d1e-1b53-4d63-aacb-000000000001"
	st := store.New()
	st.Activate(types.InterceptTask{XID: xid, Targets: []types.TargetIdentifier{supiTarget("1")}})

	faults := make([]X1Error, overHalf)
	for i := range faults {
		faults[i] = X1Error{ErrorCode: errCodeGeneric, ErrorDescription: fmt.Sprintf("fault %d", i)}
	}
	srv := testServer(st, WithTaskFaults(func(types.XID) []X1Error { return faults }))

	resp, err := srv.Process(containerWithXID("GetTaskDetailsRequest", xid, 3), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// One task and its faults per answer.
	assertTwoAnsweredThenRefused(t, resp, 1+overHalf, func(m X1ResponseMessage) int {
		return len(m.Tasks) + len(m.TaskFaults[xid])
	})
}

// containerWithXID is containerOf for a request type that names a task: n messages of msgType,
// each naming xid.
func containerWithXID(msgType, xid string, n int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`)
	for i := range n {
		fmt.Fprintf(&b, `<ns1:x1RequestMessage xsi:type="ns1:%s"><ns1:admfIdentifier>admfID</ns1:admfIdentifier>`+
			`<ns1:neIdentifier>neID</ns1:neIdentifier><ns1:messageTimestamp>2026-01-01T00:00:00.000000Z</ns1:messageTimestamp>`+
			`<ns1:version>v1.6.1</ns1:version><ns1:x1TransactionId>aaaaaaaa-aaaa-4aaa-8aaa-%012d</ns1:x1TransactionId>`+
			`<ns1:xId>%s</ns1:xId></ns1:x1RequestMessage>`, msgType, i, xid)
	}
	b.WriteString(`</ns1:X1Request>`)

	return []byte(b.String())
}
