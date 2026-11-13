// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
)

// TestAnUnreachableDestinationIsNotReportedAsWorking: one element must not make two
// statements about one fact that disagree.
//
// An element that has told the ADMF over ReportDestinationIssue that an endpoint is unreachable
// must not answer "working" when the ADMF asks about it. Interrogation is how a provisioning
// function checks a pushed report, so the answer it acts on is the interrogation — and a wrong
// one errs in the unsafe direction: it says product is arriving when it is not.
func TestAnUnreachableDestinationIsNotReportedAsWorking(t *testing.T) {
	const (
		did  = "11111111-1111-1111-1111-111111111111"
		addr = "10.0.60.122:42069"
	)

	down := map[string]bool{}
	srv := NewServer(store.New(), "neID",
		WithConfiguredDestinations(ConfiguredDestination{
			DID: did, DeliveryType: deliveryX2Only, Address: addr,
		}),
		// The delivery layer's own answer, which is the whole point: the same function the
		// element gives DestinationHealthOf, so the pushed report and this answer cannot
		// come from different states.
		WithDestinationReachability(func(a string) bool { return down[a] }),
	)

	get := func(t *testing.T) (X1ResponseMessage, string) {
		t.Helper()
		m := serve(t, srv, string(request("GetDestinationDetailsRequest",
			"\n    <ns1:dId>"+did+"</ns1:dId>")))
		if len(m.Destinations) != 1 {
			t.Fatalf("GetDestinationDetails reported %d destinations, want 1", len(m.Destinations))
		}

		return m, responseBody(m)
	}

	t.Run("reachable", func(t *testing.T) {
		m, body := get(t)
		if m.Destinations[0].Unreachable {
			t.Error("a reachable destination was reported unreachable")
		}
		if !strings.Contains(body, "<ns1:destinationDeliveryStatus>activeAndWorking<") {
			t.Errorf("the answer does not say activeAndWorking:\n%s", body)
		}
	})

	// The delivery layer loses the endpoint. Nothing else changes: no re-provisioning, no
	// task change, no restart — which is exactly why a cached status would still be saying
	// activeAndWorking here.
	down[addr] = true

	t.Run("unreachable", func(t *testing.T) {
		m, body := get(t)
		if !m.Destinations[0].Unreachable {
			t.Error("the element does not carry the delivery fault into what it reports")
		}
		if !strings.Contains(body, "<ns1:destinationDeliveryStatus>deliveryFault<") {
			t.Errorf("the answer still says the destination is working:\n%s", body)
		}
		// A status without a fault to read is a status an ADMF cannot act on beyond
		// retrying blindly, and the schema gives listOfFaults for exactly this.
		if !strings.Contains(body, "unresolvedFault") {
			t.Errorf("deliveryFault was reported with an empty listOfFaults:\n%s", body)
		}
	})

	// And back. A fault that cannot be observed to clear is a fault that sticks on, which
	// is the failure the element's own status probes are built to avoid; this answer is
	// computed per call for the same reason.
	down[addr] = false

	t.Run("recovered", func(t *testing.T) {
		_, body := get(t)
		if !strings.Contains(body, "<ns1:destinationDeliveryStatus>activeAndWorking<") {
			t.Errorf("the destination did not recover in what the element reports:\n%s", body)
		}
	})
}

// TestGetAllDestinationDetailsCarriesTheSameAnswer covers the list form, which is a separate
// render path: the per-DID answer and the list answer are assembled by different functions,
// and an element that told the truth in one and not the other would be as inconsistent as the
// one this fixes.
func TestGetAllDestinationDetailsCarriesTheSameAnswer(t *testing.T) {
	const (
		didUp   = "11111111-1111-1111-1111-111111111111"
		didDown = "22222222-2222-2222-2222-222222222222"
		addrUp  = "10.0.60.122:42069"
		down    = "10.0.60.201:42069"
	)

	srv := NewServer(store.New(), "neID",
		WithConfiguredDestinations(
			ConfiguredDestination{DID: didUp, DeliveryType: deliveryX2Only, Address: addrUp},
			ConfiguredDestination{DID: didDown, DeliveryType: deliveryX2Only, Address: down},
		),
		WithDestinationReachability(func(a string) bool { return a == down }),
	)

	m := serve(t, srv, string(request("GetAllDestinationDetailsRequest", "")))
	if len(m.Destinations) != 2 {
		t.Fatalf("reported %d destinations, want 2", len(m.Destinations))
	}
	for _, d := range m.Destinations {
		want := d.Address == down
		if d.Unreachable != want {
			t.Errorf("destination %s at %s reported unreachable=%v, want %v",
				d.DID, d.Address, d.Unreachable, want)
		}
	}

	body := responseBody(m)
	if n := strings.Count(body, "<ns1:destinationDeliveryStatus>deliveryFault<"); n != 1 {
		t.Errorf("the list reports %d destinations in delivery fault, want 1:\n%s", n, body)
	}
	if n := strings.Count(body, "<ns1:destinationDeliveryStatus>activeAndWorking<"); n != 1 {
		t.Errorf("the list reports %d working destinations, want 1:\n%s", n, body)
	}
}

// TestAnElementThatSuppliesNoReachabilityAnswerStillReportsWorking: activeAndWorking stays the
// answer where it is the honest one. An element with no delivery layer to ask
// — a test, or one built before its pool exists — has nothing to say about reachability, and
// activeAndWorking is then a default rather than a claim contradicted elsewhere.
func TestAnElementThatSuppliesNoReachabilityAnswerStillReportsWorking(t *testing.T) {
	const did = "11111111-1111-1111-1111-111111111111"

	srv := NewServer(store.New(), "neID", WithConfiguredDestinations(ConfiguredDestination{
		DID: did, DeliveryType: deliveryX2Only, Address: "10.0.60.122:42069",
	}))

	m := serve(t, srv, string(request("GetDestinationDetailsRequest",
		"\n    <ns1:dId>"+did+"</ns1:dId>")))
	if len(m.Destinations) != 1 || m.Destinations[0].Unreachable {
		t.Fatalf("an element with no reachability answer reported %+v", m.Destinations)
	}
	if body := responseBody(m); !strings.Contains(body, "activeAndWorking") {
		t.Errorf("the answer is not activeAndWorking:\n%s", body)
	}
}
