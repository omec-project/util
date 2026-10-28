// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
)

// createIPv6DestinationXML is createDestinationXML with the address in the IPv6 arm.
func createIPv6DestinationXML(did, addr, extra string) string {
	return createDestinationXMLIn("IPv6Address", did, deliveryX2Only, addr, tcpPort("42069"), extra)
}

// destinationsAnswer asks for every destination and returns the marshalled answer.
func destinationsAnswer(t *testing.T, srv *Server) string {
	t.Helper()

	resp, err := srv.Process(request("GetAllDestinationDetailsRequest", ""), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	out, err := marshalResponse(resp)
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}

	return string(out)
}

// TestIPv6DestinationsAreAnsweredInTheSchemasSpelling: the schema's IPv6 type admits only
// eight four-digit lowercase groups, so a destination stored as the peer spelled it would make
// the element's destination answers invalid — "::1" fails the pattern outright. A zone or an
// IPv4-mapped address in the IPv6 arm is refused rather than rendered into the IPv4 arm.
func TestIPv6DestinationsAreAnsweredInTheSchemasSpelling(t *testing.T) {
	requireXmllint(t)

	srv := NewServer(store.New(), "neID")
	mustAck(t, srv, createIPv6DestinationXML(didAgencyA, "2001:DB8::1", ""))

	// Held in the same spelling, so the senders, the reachability supplier and the
	// answers all key one endpoint by one string.
	held := srv.heldDestinations()
	if len(held) != 1 || held[0].Address != "[2001:0db8:0000:0000:0000:0000:0000:0001]:42069" {
		t.Errorf("the destination is held as %+v, want the expanded address", held)
	}

	out := destinationsAnswer(t, srv)
	if !strings.Contains(out, "2001:0db8:0000:0000:0000:0000:0000:0001") {
		t.Errorf("the destination is not answered in the expanded form:\n%s", out)
	}
	if problems := validateAgainstSchema(t, []byte(out)); len(problems) > 0 {
		t.Errorf("the destination answer is invalid against the schema: %v\n%s", problems, out)
	}

	for _, addr := range []string{"fe80::1%eth0", "::ffff:10.0.0.1"} {
		m := serve(t, NewServer(store.New(), "neID"), createIPv6DestinationXML(didAgencyA, addr, ""))
		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
			t.Errorf("IPv6 destination %q: want a 1010 refusal, got %+v", addr, m.ErrorInformation)
		}
	}
}

// TestAConfiguredIPv6DestinationIsAnsweredInTheSchemasSpelling: an operator writes a
// configured destination, so it is not normalised on the way in — the answer must spell
// it as the schema does all the same.
//
// A configured address may carry a zone or be IPv4-mapped, which the schema's IPv6 type
// cannot express. The zone is dropped and a mapped address is answered as the IPv4
// endpoint it names; before, either failed to parse as IPv6 and landed in the IPv4 arm.
func TestAConfiguredIPv6DestinationIsAnsweredInTheSchemasSpelling(t *testing.T) {
	requireXmllint(t)

	for _, c := range []struct{ address, want string }{
		{"[2001:db8::7]:42069", "<c:IPv6Address>2001:0db8:0000:0000:0000:0000:0000:0007</c:IPv6Address>"},
		{"[fe80::1%eth0]:42069", "<c:IPv6Address>fe80:0000:0000:0000:0000:0000:0000:0001</c:IPv6Address>"},
		{"[::ffff:10.0.0.1]:42069", "<c:IPv4Address>10.0.0.1</c:IPv4Address>"},
	} {
		t.Run(c.address, func(t *testing.T) {
			srv := NewServer(store.New(), "neID", WithConfiguredDestinations(ConfiguredDestination{
				DID: didAgencyA, DeliveryType: deliveryX2Only, Address: c.address,
			}))

			out := destinationsAnswer(t, srv)
			if !strings.Contains(out, c.want) {
				t.Errorf("the configured destination is not answered as %s:\n%s", c.want, out)
			}
			if problems := validateAgainstSchema(t, []byte(out)); len(problems) > 0 {
				t.Errorf("the destination answer is invalid against the schema: %v\n%s", problems, out)
			}
		})
	}
}

// TestAnIdentifierAssociationExtensionOnADestinationIsRefused: the extension is a task
// extension. Acknowledged on a destination, it would mean nothing — the outcome the package's
// rule on extensions exists to prevent.
func TestAnIdentifierAssociationExtensionOnADestinationIsRefused(t *testing.T) {
	srv := NewServer(store.New(), "neID")
	m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"),
		`
      <ns1:destinationDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>`+
			`<ext:IdentifierAssociationExtensions xmlns:ext="urn:3GPP:ns:li:3GPPX1Extensions:r18:v6">`+
			`<ext:IdentifierAssociationEventsGenerated>All</ext:IdentifierAssociationEventsGenerated>`+
			`</ext:IdentifierAssociationExtensions></ns1:destinationDetailsExtensions>`))

	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeCreateDestFailed {
		t.Fatalf("want a 6000 refusal, got %+v", m.ErrorInformation)
	}
	if len(srv.heldDestinations()) != 0 {
		t.Error("a refused destination must not be stored")
	}
}

// TestRefusalsCarryTheirSpecificCodes: a missing mandatory element and a value outside a
// closed enumeration are schema errors, 1010; an extension the element does not recognise
// is a destination it cannot create, 6000 -- not the generic 1000, and not 3000 where
// nothing about a task is the problem.
func TestRefusalsCarryTheirSpecificCodes(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want int
	}{
		{
			"CreateDestination with no destinationDetails",
			string(request("CreateDestinationRequest", "")), errCodeSchemaError,
		},
		{
			"CreateDestination with a deliveryType outside the enumeration",
			createDestinationXML(didAgencyA, "X9", "10.0.60.122", tcpPort("42069"), ""), errCodeSchemaError,
		},
		{
			"CreateDestination with an extension of an unknown owner",
			createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"),
				"\n      <ns1:destinationDetailsExtensions><ns1:Owner>SomeVendor</ns1:Owner></ns1:destinationDetailsExtensions>"),
			errCodeCreateDestFailed,
		},
		{
			"ModifyTask with no taskDetails",
			string(request("ModifyTaskRequest", "")), errCodeSchemaError,
		},
		{
			"ActivateTask with no taskDetails",
			string(request("ActivateTaskRequest", "")), errCodeSchemaError,
		},
		{
			"ActivateTask with a deliveryType outside the enumeration",
			activateXMLWith(string(testXID), "X9", "", ""), errCodeSchemaError,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := serve(t, NewServer(store.New(), "neID"), c.body)
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != c.want {
				t.Errorf("want a %d refusal, got %+v", c.want, m.ErrorInformation)
			}
		})
	}
}

// TestAZeroPaddedIPv4DestinationIsAccepted: TS 103 280's IPv4Address pattern admits a
// leading zero in an octet, so "010.000.000.001" is a schema-valid destination address.
// It is accepted and held, and answered, as the one canonical spelling of that endpoint.
func TestAZeroPaddedIPv4DestinationIsAccepted(t *testing.T) {
	srv := NewServer(store.New(), "neID")
	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "010.000.000.001", tcpPort("42069"), ""))

	held := srv.heldDestinations()
	if len(held) != 1 || held[0].Address != "10.0.0.1:42069" {
		t.Errorf("the destination is held as %+v, want the canonical 10.0.0.1:42069", held)
	}
}
