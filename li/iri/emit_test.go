// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func emitted(t *testing.T, build func(*builder)) string {
	t.Helper()
	var b builder
	build(&b)
	if b.err != nil {
		t.Fatalf("emit: %v", b.err)
	}
	return hex.EncodeToString(b.buf)
}

// TestTLVLayer pins the builder against bytes worked out by hand from X.690, not
// against any encoder's output.
func TestTLVLayer(t *testing.T) {
	long := bytes.Repeat([]byte{0xAB}, 200)
	for _, tc := range []struct {
		name  string
		build func(*builder)
		want  string
	}{
		// [3] IMPLICIT INTEGER 200: context|primitive|3 = 0x83; 200 needs a leading zero
		// octet to stay positive.
		{"tagged integer", func(b *builder) { b.integer(3, 200) }, "830200c8"},
		{"tagged zero", func(b *builder) { b.integer(1, 0) }, "810100"},
		{"tagged negative", func(b *builder) { b.integer(1, -129) }, "8102ff7f"},
		// DER BOOLEAN: true is 0xFF, false is 0x00.
		{"tagged true", func(b *builder) { b.boolean(2, true) }, "8201ff"},
		{"tagged false", func(b *builder) { b.boolean(2, false) }, "820100"},
		{"tagged octets", func(b *builder) { b.octets(5, []byte{0x1B}) }, "85011b"},
		{"tagged empty octets", func(b *builder) { b.octets(5, nil) }, "8500"},
		{"tagged text", func(b *builder) { b.text(1, "262") }, "8103323632"},
		// [6] constructed around [1] INTEGER 1: context|constructed|6 = 0xA6.
		{"constructed element", func(b *builder) {
			b.constructed(6, func(c *builder) { c.integer(1, 1) })
		}, "a603810101"},
		{"empty universal SEQUENCE", func(b *builder) { b.sequence(func(*builder) {}) }, "3000"},
		{"empty tagged SEQUENCE", func(b *builder) { b.constructed(1, func(*builder) {}) }, "a100"},
		// [186]: 0xBF introduces the high-tag-number form; 186 = 1×128 + 58, so the
		// base-128 continuation is 0x81 0x3A.
		{"high tag 186", func(b *builder) { b.constructed(186, func(*builder) {}) }, "bf813a00"},
		{"high tag 62", func(b *builder) { b.constructed(62, func(*builder) {}) }, "bf3e00"},
		{"high primitive tag", func(b *builder) { b.integer(31, 1) }, "9f1f0101"},
		// A length of 200 exceeds the short form: 0x81 then one length octet.
		{"long-form length", func(b *builder) { b.octets(6, long) }, "8681c8" + strings.Repeat("ab", 200)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := emitted(t, tc.build); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestTLVLayerReturnsErrors: the first failure is kept and returned, nothing written
// after it lands in the output, and it surfaces through every enclosing element rather
// than being lost at the first level.
func TestTLVLayerReturnsErrors(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	var b builder
	b.integer(1, 1)
	b.sequence(func(s *builder) {
		s.constructed(2, func(c *builder) {
			c.fail(first)
			c.fail(second)
			c.integer(3, 3)
		})
		s.integer(4, 4)
	})
	b.integer(5, 5)
	if !errors.Is(b.err, first) {
		t.Errorf("err = %v, want the first failure", b.err)
	}
	if got := hex.EncodeToString(b.buf); got != "810101" {
		t.Errorf("buf = %s, want only what was written before the failure", got)
	}
}

// TestChoiceAlternatives asserts every registered CHOICE alternative against fixed hex,
// never regenerated from this emitter.
func TestChoiceAlternatives(t *testing.T) {
	v6 := IPv6Address(net.ParseIP("2001:db8::1").To16())
	for _, tc := range []struct {
		alternative func(*builder, string, any)
		v           any
		want        string
	}{
		{supi, IMSI("262019876543210"), "810f323632303139383736353433323130"},
		{supi, NAI("user@example.org"), "821075736572406578616d706c652e6f7267"},
		{pei, IMEI("35342500000001"), "810e3335333432353030303030303031"},
		{pei, IMEISV("3534250000000151"), "821033353334323530303030303030313531"},
		{gpsi, MSISDN("4915123456789"), "810d34393135313233343536373839"},
		{gpsi, NAI("user@example.org"), "821075736572406578616d706c652e6f7267"},
		{ueEndpointAddress, IPv4Address{10, 45, 0, 2}, "81040a2d0002"},
		{ueEndpointAddress, v6, "821020010db8000000000000000000000001"},
		{ueEndpointAddress, MACAddress{0x02, 0x42, 0xac, 0x11, 0x00, 0x02}, "83060242ac110002"},
		{amfFailureCause, FiveGMMCause(11), "81010b"},
		{amfFailureCause, FiveGSMCause(26), "82011a"},
		{fiveGSSubscriberID, SubscriberSUPI{Value: IMSI("262019876543210")}, "a111810f323632303139383736353433323130"},
		{fiveGSSubscriberID, SubscriberPEI{Value: IMEISV("3534250000000151")}, "a312821033353334323530303030303030313531"},
		{fiveGSSubscriberID, SubscriberGPSI{Value: MSISDN("4915123456789")}, "a40f810d34393135313233343536373839"},
		{serviceMessageIdentity, ServiceRequestIdentity{0x4C}, "81014c"},
		{serviceMessageIdentity, ServiceAcceptIdentity{0x4E}, "82014e"},
		{handoverCause, CauseRadioNetwork(17), "810111"},
		{handoverCause, CauseTransport(1), "820101"},
		{handoverCause, CauseNas(2), "830102"},
		{handoverCause, CauseProtocol(3), "840103"},
		{handoverCause, CauseMisc(200), "850200c8"},
	} {
		t.Run(fmt.Sprintf("%T", tc.v), func(t *testing.T) {
			if got := emitted(t, func(b *builder) { tc.alternative(b, "field", tc.v) }); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}

	// The seventeen xiriEvent alternatives: the identifier octets each record is
	// wrapped in. Their contents are pinned, in two forms each, by the golden fixtures.
	eventTags := map[string]string{
		"AMFRegistration": "a1", "AMFDeregistration": "a2", "AMFLocationUpdate": "a3",
		"AMFStartOfInterceptionWithRegisteredUE": "a4", "AMFUnsuccessfulProcedure": "a5",
		"SMFPDUSessionEstablishment": "a6", "SMFPDUSessionModification": "a7",
		"SMFPDUSessionRelease": "a8", "SMFStartOfInterceptionWithEstablishedPDUSession": "a9",
		"SMFUnsuccessfulProcedure": "aa", "AMFIdentifierAssociation": "bf3e",
		"AMFPositioningInfoTransfer": "bf6f", "AMFRANHandoverCommand": "bf71",
		"AMFRANHandoverRequest": "bf72", "AMFUEPolicyTransfer": "bf8112",
		"AMFUEServiceAccept": "bf8113", "AMFIdentifierDeassociation": "bf813a",
	}
	if len(eventTags) != registeredRecordCount {
		t.Fatalf("%d event tags listed, %d records registered", len(eventTags), registeredRecordCount)
	}
	for name, sample := range goldenBareSamples() {
		got := emitted(t, func(b *builder) { xiriEvent(b, "event", sample) })
		if want := eventTags[name]; !strings.HasPrefix(got, want) {
			t.Errorf("%s is wrapped as %s…, want identifier %s", name, got[:6], want)
		}
	}
}

// TestAlternativesRefuseWhatTheyDoNotHold: a value no alternative carries, or no value
// where one is mandatory, is an error naming the field — never an omitted member.
func TestAlternativesRefuseWhatTheyDoNotHold(t *testing.T) {
	for _, alternative := range []func(*builder, string, any){
		supi, pei, gpsi, ueEndpointAddress, amfFailureCause, fiveGSSubscriberID,
		serviceMessageIdentity, handoverCause, xiriEvent,
	} {
		for _, v := range []any{nil, 42, "plain string"} {
			var b builder
			alternative(&b, "Rec.Field", v)
			if b.err == nil || !strings.Contains(b.err.Error(), "Rec.Field") {
				t.Errorf("%T: err = %v, want a refusal naming Rec.Field", v, b.err)
			}
			if len(b.buf) != 0 {
				t.Errorf("%T: wrote %x alongside the refusal", v, b.buf)
			}
		}
	}
}

// TestPointerMembersAreThreeState is the reason the pointer-typed members exist:
// present-and-false, present-and-true and absent must be three different encodings.
// Every site carrying one is checked, since each states its presence at its own call.
func TestPointerMembersAreThreeState(t *testing.T) {
	f, tr := SUPIUnauthenticatedIndication(false), SUPIUnauthenticatedIndication(true)
	type site struct {
		name  string
		build func(*SUPIUnauthenticatedIndication) func(*builder)
		tag   string // the identifier octet the member is written under
	}
	for _, s := range []site{
		{"SMFPDUSessionEstablishment", func(p *SUPIUnauthenticatedIndication) func(*builder) {
			return SMFPDUSessionEstablishment{SUPIUnauthenticated: p}.emit
		}, "82"},
		{"SMFPDUSessionModification", func(p *SUPIUnauthenticatedIndication) func(*builder) {
			return SMFPDUSessionModification{SUPIUnauthenticated: p}.emit
		}, "82"},
		{"SMFStartOfInterceptionWithEstablishedPDUSession", func(p *SUPIUnauthenticatedIndication) func(*builder) {
			return SMFStartOfInterceptionWithEstablishedPDUSession{SUPIUnauthenticated: p}.emit
		}, "82"},
		{"SMFUnsuccessfulProcedure", func(p *SUPIUnauthenticatedIndication) func(*builder) {
			return SMFUnsuccessfulProcedure{SUPIUnauthenticated: p}.emit
		}, "86"},
	} {
		t.Run(s.name, func(t *testing.T) {
			absent := emitted(t, s.build(nil))
			withFalse := emitted(t, s.build(&f))
			withTrue := emitted(t, s.build(&tr))
			if absent == withFalse || absent == withTrue || withFalse == withTrue {
				t.Fatalf("not three encodings:\n absent %s\n false  %s\n true   %s", absent, withFalse, withTrue)
			}
			if !strings.Contains(withFalse, s.tag+"0100") || !strings.Contains(withTrue, s.tag+"01ff") {
				t.Errorf("sUPIUnauthenticated not written as %s: false %s, true %s", s.tag, withFalse, withTrue)
			}
		})
	}

	// The SUCI's two pointer members, whose zero is likewise a value: a routing
	// indicator length of 0 is out of range but still distinct from absent, and a SUPI
	// type of 0 is the IMSI format.
	zeroLen, zeroType := RoutingIndicatorLength(0), SUPIType(0)
	absent := emitted(t, SUCI{}.emit)
	present := emitted(t, SUCI{RoutingIndicatorLength: &zeroLen, SUPIType: &zeroType}.emit)
	if got, want := strings.TrimPrefix(present, absent), "870100880100"; got != want {
		t.Errorf("SUCI with both pointer members present and zero adds %s, want %s", got, want)
	}
}

// TestSequenceOfChoice pins the two SEQUENCE OF CHOICE fields against fixed hex; a
// SEQUENCE OF a CHOICE is the shape an encoder most easily gets wrong.
func TestSequenceOfChoice(t *testing.T) {
	v6 := IPv6Address(net.ParseIP("2001:db8::1").To16())
	for _, tc := range []struct {
		name  string
		build func(*builder)
		want  string
	}{
		{"uEEndpoint IPv4", func(b *builder) {
			b.sequenceOf(9, "f", UEEndpoint(net.ParseIP("10.45.0.2")), ueEndpointAddress)
		}, "a90681040a2d0002"},
		{"uEEndpoint IPv6", func(b *builder) {
			b.sequenceOf(9, "f", UEEndpoint(net.ParseIP("2001:db8::1")), ueEndpointAddress)
		}, "a912821020010db8000000000000000000000001"},
		{"uEEndpoint every arm", func(b *builder) {
			b.sequenceOf(9, "f", []any{IPv4Address{10, 45, 0, 2}, v6, MACAddress{2, 0x42, 0xac, 0x11, 0, 2}}, ueEndpointAddress)
		}, "a92081040a2d0002821020010db800000000000000000000000183060242ac110002"},
		{"identifiers, three arms", func(b *builder) {
			b.constructed(1, Identifiers(IMSI("262019876543210"), IMEISV("3534250000000151"), MSISDN("4915123456789")).emit)
		}, "a13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839"},
		{"identifiers, the other leaves", func(b *builder) {
			b.constructed(1, Identifiers(NAI("user@example.org"), IMEI("35342500000001"), NAI("gpsi@example.org")).emit)
		}, "a13ea13ca13aa112821075736572406578616d706c652e6f7267a310810e3335333432353030303030303031a412821067707369406578616d706c652e6f7267"},
		{"identifiers, one arm", func(b *builder) {
			b.constructed(1, Identifiers(nil, nil, MSISDN("4915123456789")).emit)
		}, "a115a113a111a40f810d34393135313233343536373839"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := emitted(t, tc.build); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
