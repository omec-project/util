// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"slices"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// TestADestinationAddressIsValidatedAsAnAddress is the value half of a rule this element
// already applied to the field: it refused a destination whose *address form* it could not
// use, and then took the IPv4 arm's text as an address without parsing it.
//
// So a destination whose address is not an address was created, acknowledged, and dialled
// forever — with the failure surfacing later as an unreachable mediation function rather
// than as the provisioning error it is, which points an operator at the network instead of
// at the message. Refused as the schema error, because the type restricts it.
func TestADestinationAddressIsValidatedAsAnAddress(t *testing.T) {
	for _, tc := range []struct {
		name, addr, port string
	}{
		{"a host name where an address is typed", "mdf2.example.net", tcpPort("42069")},
		{"not an address at all", "not-an-address", tcpPort("42069")},
		{"an IPv6 literal in the IPv4 arm", "2001:db8::1", tcpPort("42069")},
		{"a truncated address", "10.0.60", tcpPort("42069")},
		{"a port of zero", "10.0.60.122", tcpPort("0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New()
			srv := NewServer(st, "neID")

			m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, tc.addr, tc.port, ""))
			if m.ErrorInformation == nil {
				t.Fatal("the destination was created: this element will dial it for the life of " +
					"the process and report the failure as an unreachable mediation function")
			}
			if m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Errorf("error code = %d, want %d (schema error): the value violates the type its "+
					"schema defines, which is malformed rather than something this element declines",
					m.ErrorInformation.ErrorCode, errCodeSchemaError)
			}

			// And nothing is stored: a destination this element holds is one it answers
			// interrogations with and one a task may name.
			if got := serve(t, srv, string(request("GetAllDetailsRequest", ""))); len(got.Destinations) != 0 {
				t.Errorf("the element holds %d destination(s) after a refusal", len(got.Destinations))
			}
		})
	}
}

// TestBothArmsOfADestinationChoiceAreRefused is the cardinality half. Populating two arms
// is invalid against the schema, so no reading of it is authoritative — and the value
// helpers resolved it by precedence, which made the element decide which address a
// warrant's product goes to and which port it is delivered on.
func TestBothArmsOfADestinationChoiceAreRefused(t *testing.T) {
	const bothPorts = `<c:TCPPort>42069</c:TCPPort><c:UDPPort>42070</c:UDPPort>`

	t.Run("both address arms", func(t *testing.T) {
		st := store.New()
		srv := NewServer(st, "neID")

		body := strings.Replace(
			createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""),
			"<c:IPv4Address>10.0.60.122</c:IPv4Address>",
			"<c:IPv4Address>10.0.60.122</c:IPv4Address><c:IPv6Address>2001:db8::1</c:IPv6Address>", 1)

		m := serve(t, srv, body)
		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
			t.Fatalf("got %+v, want error %d: two populated arms of a choice has no authoritative "+
				"reading, and precedence would have this element choose the destination",
				m.ErrorInformation, errCodeSchemaError)
		}
	})

	t.Run("both port arms", func(t *testing.T) {
		st := store.New()
		srv := NewServer(st, "neID")

		m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", bothPorts, ""))
		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
			t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
		}
	})

	t.Run("no port arm", func(t *testing.T) {
		st := store.New()
		srv := NewServer(st, "neID")

		m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", "", ""))
		if m.ErrorInformation == nil {
			t.Fatal("a destination with no port was created")
		}
	})
}

// TestATaskCarryingAnOutOfRangeValueIsRefused is the same obligation on the task path,
// where mapTarget copied every arm's text into the store with no check at all.
//
// The consequence is not only that the criterion matches nothing. GetTaskDetails and
// GetAllDetails exist so an ADMF can discover what an element actually holds, and the
// element echoed the malformed value back as its own account of what it is intercepting.
func TestATaskCarryingAnOutOfRangeValueIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, target string }{
		{"a port above the range", `<ns1:targetIdentifier><ns1:tcpPort>99999</ns1:tcpPort></ns1:targetIdentifier>`},
		{"a port that is not a number", `<ns1:targetIdentifier><ns1:udpPort>http</ns1:udpPort></ns1:targetIdentifier>`},
		{"an address that is not one", `<ns1:targetIdentifier><ns1:ipv4Address>10.45.0</ns1:ipv4Address></ns1:targetIdentifier>`},
		{"an IPv6 literal in the IPv4 arm", `<ns1:targetIdentifier><ns1:ipv4Address>2001:db8::1</ns1:ipv4Address></ns1:targetIdentifier>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New()
			srv := NewServer(st, "neID")
			provisionAgencyA(t, srv)

			body := strings.Replace(
				activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""),
				`<ns1:targetIdentifiers>`,
				`<ns1:targetIdentifiers>`+tc.target, 1)

			m := serve(t, srv, body)
			if m.ErrorInformation == nil {
				t.Fatal("the task was accepted, and this element now reports the value back as " +
					"its own account of what it is intercepting")
			}
			if m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Errorf("error code = %d, want %d (schema error)",
					m.ErrorInformation.ErrorCode, errCodeSchemaError)
			}
			if st.Len() != 0 {
				t.Error("a task carrying a value outside its type was stored")
			}
		})
	}
}

// TestAForeignOwnerExtensionAppliesNoCriteria is the ownership half.
//
// TS 103 221-1 annex B makes the extension a placeholder: an Owner naming the
// specification that defines the content, then that content. Reading the content as 3GPP
// LI_T3 criteria without checking the Owner means the element applies 3GPP detection
// criteria to a message that claimed to carry someone else's — and matching nested element
// names and a namespace is not the same statement as the extension being 3GPP's.
func TestAForeignOwnerExtensionAppliesNoCriteria(t *testing.T) {
	const criterion = `<ns1:targetIdentifier>
          <ns1:targetIdentifierExtension>
            <ns1:Owner>%OWNER%</ns1:Owner>
            <e:UPFLIT3TargetIdentifierExtensions xmlns:e="urn:3GPP:ns:li:3GPPX1Extensions:r18:v6">
              <e:UPFLIT3TargetIdentifier>
                <e:FSEID><e:SEID>14426627323429955319</e:SEID></e:FSEID>
              </e:UPFLIT3TargetIdentifier>
            </e:UPFLIT3TargetIdentifierExtensions>
          </ns1:targetIdentifierExtension>
        </ns1:targetIdentifier>`

	for _, tc := range []struct {
		name, owner string
		accept      bool
	}{
		{"3GPP", "3GPP", true},
		{"another standards body", "ETSI", false},
		{"absent", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New()
			srv := NewServer(st, "neID")
			provisionAgencyA(t, srv)

			body := strings.Replace(
				activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""),
				`<ns1:targetIdentifiers>`,
				`<ns1:targetIdentifiers>`+strings.Replace(criterion, "%OWNER%", tc.owner, 1), 1)

			m := serve(t, srv, body)
			task, held := st.Get(testXID)

			if !tc.accept {
				if m.ErrorInformation == nil {
					t.Fatal("an extension this element cannot attribute to 3GPP was read as 3GPP " +
						"detection criteria, and the traffic it selects was intercepted")
				}
				if held {
					t.Error("a task carrying a foreign-owner extension was stored")
				}

				return
			}
			if m.ErrorInformation != nil {
				t.Fatalf("a 3GPP-owned extension was refused: %+v", m.ErrorInformation)
			}
			if !held {
				t.Fatal("a 3GPP-owned extension's task was not stored")
			}
			if !slices.ContainsFunc(task.Targets, func(id types.TargetIdentifier) bool {
				return id.Type == types.TargetFSEID
			}) {
				t.Errorf("targets = %+v, want the extension's F-SEID criterion applied", task.Targets)
			}
		})
	}
}

// TestAConfiguredDestinationsValueIsValidated closes the third destination source. An
// operator's static declaration is the one source with no X1 answer to carry a refusal, so
// an unusable entry is dropped — and a task naming its DID is then refused for an
// identifier the operator believes they supplied, which is a confusing way to learn that
// the address was wrong. SplitHostPort alone accepts a host that is not an address.
func TestAConfiguredDestinationsValueIsValidated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dest  ConfiguredDestination
		valid bool
	}{
		{"an address and port", ConfiguredDestination{
			DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.122:42069",
		}, true},
		{"a host name", ConfiguredDestination{
			DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "mdf2.example.net:42069",
		}, false},
		{"no port", ConfiguredDestination{
			DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.122",
		}, false},
		{"a port of zero", ConfiguredDestination{
			DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.122:0",
		}, false},
		{"a port above the range", ConfiguredDestination{
			DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.122:99999",
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.dest.Valid()
			if tc.valid && err != nil {
				t.Errorf("a usable entry was rejected: %v", err)
			}
			if !tc.valid && err == nil {
				t.Error("an entry this element cannot dial was accepted, so a task naming its DID " +
					"resolves to an address nothing can reach")
			}
		})
	}
}
