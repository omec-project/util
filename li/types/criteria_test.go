// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"slices"
	"testing"
)

// Every criterion TS 33.128 table 6.2.3-7 requires must be a distinct type that
// survives a round trip through a task, and must be distinguishable from the
// PFCP Session ID that was the only one supported before. A duplicated constant
// value would silently conflate two criteria and intercept the wrong traffic.
func TestTableCriteriaAreDistinct(t *testing.T) {
	criteria := []TargetIdentifierType{
		TargetFTEID, TargetUEIPv4, TargetUEIPv6, TargetTCPPort, TargetUDPPort,
		TargetPDRID, TargetQERID, TargetNetworkInstance, TargetGTPTunnelDirection,
		TargetPDR, TargetFSEID,
	}
	if len(criteria) != 11 {
		t.Fatalf("expected the nine table types plus the two address forms, got %d", len(criteria))
	}

	seen := make(map[TargetIdentifierType]bool, len(criteria))
	for _, c := range criteria {
		if c == "" {
			t.Error("a criterion has an empty value, so it cannot be distinguished on the wire")
		}
		if seen[c] {
			t.Errorf("criterion %q is duplicated: two criteria sharing a value would select each other's traffic", c)
		}
		seen[c] = true
	}
}

// subscriberIdentifiers are the identity types an IRI-POI matches a target by.
var subscriberIdentifiers = []TargetIdentifierType{TargetSUPI, TargetPEI, TargetGPSI}

// TestEverySubscriberIdentifierRenders: every subscriber identity type must render,
// because an xIRI reports the identity it matched as a conditional attribute TS 33.128
// table 5.3.2-2 marks required.
//
// XMLFragments drops what it cannot render rather than substituting something else,
// which is the right rule — but it means a subscriber identity type with no element
// name would cost every xIRI matched on it a mandatory attribute, with nothing
// failing and nothing logged. That is the failure this test exists to make loud, and
// it is here rather than in a POI because here is where the type would be added.
func TestEverySubscriberIdentifierRenders(t *testing.T) {
	// A value of the shape each type's own element takes: a PEI is fourteen or sixteen
	// digits, and one that is neither fits no element and rightly renders as nothing.
	sample := map[TargetIdentifierType]string{TargetSUPI: "262019876543210", TargetPEI: "35784005123456"}
	for _, c := range subscriberIdentifiers {
		v, ok := sample[c]
		if !ok {
			v = "1234567890"
		}
		id := TargetIdentifier{Type: c, Value: v}
		frag, ok := id.XMLFragment()
		if !ok {
			t.Errorf("%q renders no XML fragment, so an xIRI matched on it would carry no matched target identifier", c)

			continue
		}
		if frag == "" {
			t.Errorf("%q renders an empty fragment", c)
		}
	}
}

// TestMatchedByComparesEachCriterionByValue: a task naming a criterion matches an entity
// holding exactly that criterion, so the comparison is by type and value. The network
// instance is the schema's hexBinary — "internet" hex-encoded — not a name.
func TestMatchedByComparesEachCriterionByValue(t *testing.T) {
	for _, tc := range []struct {
		typ        TargetIdentifierType
		val, other string
	}{
		{TargetFTEID, "1234@192.0.2.1", "1234@192.0.2.2"},
		{TargetUEIPv4, "192.0.2.50", "192.0.2.51"},
		{TargetUEIPv6, "2001:0db8:0000:0000:0000:0000:0000:0001", "2001:0db8:0000:0000:0000:0000:0000:0002"},
		{TargetTCPPort, "443", "444"},
		{TargetUDPPort, "2152", "2153"},
		{TargetPDRID, "7", "8"},
		{TargetQERID, "3", "4"},
		{TargetNetworkInstance, "696e7465726e6574", "696d73"},
		{TargetGTPTunnelDirection, "Outbound", "Inbound"},
		{TargetPDR, "0a0b0c", "0a0b0d"},
	} {
		task := InterceptTask{Targets: []TargetIdentifier{{Type: tc.typ, Value: tc.val}}}

		if !task.MatchedBy([]TargetIdentifier{{Type: tc.typ, Value: tc.val}}) {
			t.Errorf("%s %q does not match itself", tc.typ, tc.val)
		}
		if task.MatchedBy([]TargetIdentifier{{Type: tc.typ, Value: tc.other}}) {
			t.Errorf("%s %q matched %q", tc.typ, tc.val, tc.other)
		}
		// The same value under another type is a different criterion.
		if task.MatchedBy([]TargetIdentifier{{Type: TargetSUPI, Value: tc.val}}) {
			t.Errorf("%s %q matched a SUPI with the same value", tc.typ, tc.val)
		}
	}
}

// TestEveryCriterionIsRequired is TS 103 221-1 table 6.2.1.2-1: "Where multiple criteria
// are present, all criteria are required to be matched." A subject presenting one of a
// task's two identifiers is not the task's subject — matching it anyway intercepts
// someone the warrant does not describe.
func TestEveryCriterionIsRequired(t *testing.T) {
	supi := TargetIdentifier{Type: TargetSUPI, Value: "262019876543210"}
	gpsi := TargetIdentifier{Type: TargetGPSI, Value: "4915123456789"}
	pei := TargetIdentifier{Type: TargetPEI, Value: "35784005123456"}
	task := InterceptTask{Targets: []TargetIdentifier{supi, gpsi}}

	for _, tc := range []struct {
		name string
		held []TargetIdentifier
		want bool
	}{
		{"both, and more", []TargetIdentifier{supi, pei, gpsi}, true},
		{"both", []TargetIdentifier{gpsi, supi}, true},
		{"only the SUPI", []TargetIdentifier{supi, pei}, false},
		{"only the GPSI", []TargetIdentifier{gpsi}, false},
		{"nothing", nil, false},
	} {
		if got := task.MatchedBy(tc.held); got != tc.want {
			t.Errorf("%s: MatchedBy = %v, want %v", tc.name, got, tc.want)
		}
	}

	if (InterceptTask{}).MatchedBy([]TargetIdentifier{supi}) {
		t.Error("a task naming nothing matched a subject")
	}
}

// TestNamesOnlyTypesRequiresEveryCriterionToBeEvaluable: because every criterion must
// match, one the element never evaluates makes the task unmatchable at that element, so
// the question is whether all of them are of kinds it handles — only the types count.
func TestNamesOnlyTypesRequiresEveryCriterionToBeEvaluable(t *testing.T) {
	task := InterceptTask{Targets: []TargetIdentifier{
		{Type: TargetSUPI, Value: "262019876543210"},
		{Type: TargetFSEID, Value: "42"},
	}}

	for _, tc := range []struct {
		kinds []TargetIdentifierType
		want  bool
	}{
		{[]TargetIdentifierType{TargetSUPI, TargetFSEID}, true},
		{[]TargetIdentifierType{TargetSUPI, TargetPEI, TargetGPSI}, false},
		{[]TargetIdentifierType{TargetFSEID}, false},
		{nil, false},
	} {
		if got := task.NamesOnlyTypes(tc.kinds...); got != tc.want {
			t.Errorf("NamesOnlyTypes(%v) = %v, want %v", tc.kinds, got, tc.want)
		}
	}

	if (InterceptTask{}).NamesOnlyTypes(TargetSUPI) {
		t.Error("a task naming nothing is evaluable")
	}
}

// TestContradictoryTargetsFindsCombinationsNothingSatisfies: a task whose criteria can
// never all match at once is "an inappropriate combination of identifiers" that table
// 6.2.1.2-1 has the element reject. A combination some subject or packet can satisfy
// is not one.
func TestContradictoryTargetsFindsCombinationsNothingSatisfies(t *testing.T) {
	id := func(typ TargetIdentifierType, v string) TargetIdentifier {
		return TargetIdentifier{Type: typ, Value: v}
	}

	for _, tc := range []struct {
		name    string
		targets []TargetIdentifier
		want    bool
	}{
		{"two SUPIs", []TargetIdentifier{id(TargetSUPI, "262019876543210"), id(TargetSUPI, "262019876543211")}, true},
		{"two GPSIs", []TargetIdentifier{id(TargetGPSI, "1"), id(TargetGPSI, "2")}, true},
		{"two sessions", []TargetIdentifier{id(TargetFSEID, "1"), id(TargetFSEID, "2")}, true},
		{"two directions", []TargetIdentifier{id(TargetGTPTunnelDirection, "Outbound"), id(TargetGTPTunnelDirection, "Inbound")}, true},
		{"two UE addresses", []TargetIdentifier{id(TargetUEIPv4, "192.0.2.1"), id(TargetUEIPv4, "192.0.2.2")}, true},
		// A packet carries a UE address of one family.
		{"a UE IPv4 and a UE IPv6 address", []TargetIdentifier{
			id(TargetUEIPv4, "192.0.2.1"), id(TargetUEIPv6, "2001:0db8:0000:0000:0000:0000:0000:0001"),
		}, true},
		{"a UE IPv4 address and a port", []TargetIdentifier{id(TargetUEIPv4, "192.0.2.1"), id(TargetTCPPort, "443")}, false},
		{"a TCP and a UDP port", []TargetIdentifier{id(TargetTCPPort, "53"), id(TargetUDPPort, "53")}, true},
		{"two TCP ports", []TargetIdentifier{id(TargetTCPPort, "80"), id(TargetTCPPort, "443")}, true},
		{"two IMEIs", []TargetIdentifier{id(TargetPEI, "35784005123456"), id(TargetPEI, "35784005123457")}, true},
		{"an IMEISV and another device's IMEI", []TargetIdentifier{id(TargetPEI, "3578400512345678"), id(TargetPEI, "35784005123457")}, true},
		{"an IMEISV and its own IMEI", []TargetIdentifier{id(TargetPEI, "3578400512345678"), id(TargetPEI, "35784005123456")}, false},
		{"a SUPI and a GPSI", []TargetIdentifier{id(TargetSUPI, "262019876543210"), id(TargetGPSI, "1")}, false},
		{"a session and a direction", []TargetIdentifier{id(TargetFSEID, "1"), id(TargetGTPTunnelDirection, "Outbound")}, false},
		{"a criterion named twice", []TargetIdentifier{id(TargetFSEID, "1"), id(TargetFSEID, "1")}, false},
		{"one criterion", []TargetIdentifier{id(TargetSUPI, "262019876543210")}, false},
	} {
		_, _, got := InterceptTask{Targets: tc.targets}.ContradictoryTargets()
		if got != tc.want {
			t.Errorf("%s: ContradictoryTargets = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSplitTargetsReturnsEveryMatchAndOnlyTheSubjectsOwnRest: every held identity the
// task names is a match, the rest of the subject's identities are "other", and an
// identity appearing twice is reported where it falls each time.
func TestSplitTargetsReturnsEveryMatchAndOnlyTheSubjectsOwnRest(t *testing.T) {
	supi := TargetIdentifier{Type: TargetSUPI, Value: "262019876543210"}
	pei := TargetIdentifier{Type: TargetPEI, Value: "35784005123456"}
	gpsi := TargetIdentifier{Type: TargetGPSI, Value: "4915123456789"}
	task := InterceptTask{Targets: []TargetIdentifier{supi, pei}}

	matched, other := task.SplitTargets([]TargetIdentifier{supi, gpsi, pei})
	if !slices.Equal(matched, []TargetIdentifier{supi, pei}) || !slices.Equal(other, []TargetIdentifier{gpsi}) {
		t.Errorf("SplitTargets = %v, %v; want both named identities matched and the GPSI other", matched, other)
	}

	matched, other = task.SplitTargets([]TargetIdentifier{gpsi})
	if matched != nil || !slices.Equal(other, []TargetIdentifier{gpsi}) {
		t.Errorf("SplitTargets with no match = %v, %v", matched, other)
	}

	if matched, other = task.SplitTargets(nil); matched != nil || other != nil {
		t.Errorf("SplitTargets(nil) = %v, %v; want nothing", matched, other)
	}

	if matched, _ = task.SplitTargets([]TargetIdentifier{supi, supi}); len(matched) != 2 {
		t.Errorf("a repeated held identity was matched %d times, want each occurrence", len(matched))
	}
}

// TestTheTwoShapesOfOneTEIDAreCompatible: an F-TEID criterion is "TEID" or "TEID@address", and
// the one without an address is satisfied wherever the addressed one is, so a task naming both
// shapes of one TEID is not a contradiction. Two TEIDs, or one TEID at two addresses, are.
func TestTheTwoShapesOfOneTEIDAreCompatible(t *testing.T) {
	fteid := func(v string) TargetIdentifier { return TargetIdentifier{Type: TargetFTEID, Value: v} }

	for _, c := range []struct {
		a, b       string
		contradict bool
	}{
		{"5", "5@10.0.0.1", false},
		{"5@10.0.0.1", "5", false},
		{"5@10.0.0.1", "5@10.0.0.2", true},
		{"5", "6@10.0.0.1", true},
		{"5", "6", true},
		// Addresses are compared as addresses: two spellings of one endpoint agree.
		{"5@2001:0db8:0000:0000:0000:0000:0000:0001", "5@2001:db8::1", false},
		{"5@10.0.0.1", "5@::ffff:10.0.0.1", false},
		{"5@2001:db8::1", "5@2001:db8::2", true},
	} {
		task := InterceptTask{Targets: []TargetIdentifier{fteid(c.a), fteid(c.b)}}
		if _, _, got := task.ContradictoryTargets(); got != c.contradict {
			t.Errorf("F-TEID %q with %q: contradictory = %v, want %v", c.a, c.b, got, c.contradict)
		}
	}
}
