// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// The subject of this file is where a task's product goes, and what happens to the task
// fields that decide it.
//
// The defect it exists for is not one the element could report: a task named its
// delivery endpoints, the endpoints resolved, and then the IRI-POIs delivered to their
// own configured address anyway. With one agency configured and one warrant, that is
// invisible. With two warrants provisioned to two agencies, both agencies' product
// arrives at whichever address configuration happened to name — so the assertions here
// are mostly about *two* of something, because one of anything passes either way.

const (
	// The DIDs are UUIDs because the schema types a DId as one, and because an element
	// whose own test material is malformed cannot tell a conformant ADMF from a broken
	// one.
	didAgencyA = "11111111-1111-4111-8111-111111111111"
	didAgencyB = "22222222-2222-4222-8222-222222222222"
	didBoth    = "33333333-3333-4333-8333-333333333333"

	// endpointA is the address most cases provision, named rather than repeated so
	// that a case using a *different* address is visibly doing so on purpose.
	endpointA = "10.0.60.122:42069"
)

// createDestinationXML builds a CreateDestinationRequest with an IPv4 address. portXML is the
// contents of the TS 103 280 `port` element, so a test can send the shape the schema defines
// or one it does not.
func createDestinationXML(did, deliveryType, addr, portXML, extra string) string {
	return createDestinationXMLIn("IPv4Address", did, deliveryType, addr, portXML, extra)
}

// createDestinationXMLIn builds a CreateDestinationRequest whose address is in the named arm
// of the TS 103 280 IPAddress choice.
func createDestinationXMLIn(arm, did, deliveryType, addr, portXML, extra string) string {
	return string(request("CreateDestinationRequest", `
    <ns1:destinationDetails>
      <ns1:dId>`+did+`</ns1:dId>
      <ns1:deliveryType>`+deliveryType+`</ns1:deliveryType>
      <ns1:deliveryAddress>
        <ns1:ipAddressAndPort>
          <c:address xmlns:c="http://uri.etsi.org/03280/common/2017/07">
            <c:`+arm+`>`+addr+`</c:`+arm+`>
          </c:address>
          <c:port xmlns:c="http://uri.etsi.org/03280/common/2017/07">`+portXML+`</c:port>
        </ns1:ipAddressAndPort>
      </ns1:deliveryAddress>`+extra+`
    </ns1:destinationDetails>`))
}

// tcpPort is the schema's own shape for a port: a child element, not element text.
func tcpPort(p string) string {
	return `<c:TCPPort>` + p + `</c:TCPPort>`
}

// activateXMLWith builds an ActivateTaskRequest whose taskDetails carry extra after the
// mandatory elements, in the xs:sequence order the schema fixes.
func activateXMLWith(xid, deliveryType, listOfDIDs, afterDIDs string) string {
	return string(request("ActivateTaskRequest", `
    <ns1:taskDetails>
      <ns1:xId>`+xid+`</ns1:xId>
      <ns1:targetIdentifiers>
        <ns1:targetIdentifier><ns1:supiimsi>208930100007488</ns1:supiimsi></ns1:targetIdentifier>
      </ns1:targetIdentifiers>
      <ns1:deliveryType>`+deliveryType+`</ns1:deliveryType>
      <ns1:listOfDIDs>`+listOfDIDs+`</ns1:listOfDIDs>`+afterDIDs+`
    </ns1:taskDetails>`))
}

func dIDs(dids ...string) string {
	var b strings.Builder
	for _, d := range dids {
		b.WriteString("\n        <ns1:dId>" + d + "</ns1:dId>")
	}

	return b.String()
}

// serve runs body through a server and requires an acknowledgement.
func serve(t *testing.T, srv *Server, body string) X1ResponseMessage {
	t.Helper()
	resp, err := srv.Process([]byte(body), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(resp.Messages))
	}

	return resp.Messages[0]
}

func mustAck(t *testing.T, srv *Server, body string) {
	t.Helper()
	m := serve(t, srv, body)
	if m.OK == "" {
		t.Fatalf("want an acknowledgement, got %+v (%+v)", m.Type, m.ErrorInformation)
	}
}

// endpointsOf lists a stored task's delivery endpoints of one type.
func endpointsOf(t *testing.T, st *store.Store, xid types.XID, dt types.DeliveryType) []string {
	t.Helper()
	task, ok := st.Get(xid)
	if !ok {
		t.Fatalf("task %s was not stored", xid)
	}
	var out []string
	for _, d := range task.Deliveries {
		if d.Type == dt {
			out = append(out, d.Address)
		}
	}

	return out
}

// provisionAgencyA creates didAgencyA at the element, the way TS 33.128 requires: it
// marks ListOfDIDs mandatory on an ActivateTask and requires the endpoints to have been
// provisioned with CreateDestination beforehand.
//
// The element refuses a task naming a destination identifier it cannot resolve — storing it
// with the subset would mean an agency the warrant names receives nothing while provisioning
// reports success — so a test whose subject is something other than destination resolution
// has to provision the one its fixture names.
func provisionAgencyA(t *testing.T, srv *Server) {
	t.Helper()

	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
}

// TestTaskCarriesTheX2DestinationsItNamed: TS 33.128 marks ListOfDIDs mandatory in every
// ActivateTask table it defines and requires the endpoints to have been provisioned with
// CreateDestination beforehand; this is the element holding up its end.
func TestTaskCarriesTheX2DestinationsItNamed(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))

	got := endpointsOf(t, st, testXID, types.DeliveryX2)
	if len(got) != 1 || got[0] != endpointA {
		t.Errorf("X2 endpoints = %v, want [10.0.60.122:42069]", got)
	}
}

// TestATaskNamingAnUnresolvableDestinationIsRefused: accepting such a task would store it,
// report it as active and produce product for whichever endpoints *did* resolve — so an agency
// named in the warrant would receive nothing while provisioning reported success, which is
// undiscoverable from outside. An ADMF relying on configured destinations is served two ways:
// a task that names no destination at all falls back to the element's configured endpoint, and
// a destination agreed out of band is declared in the element's own configuration, where it
// resolves.
//
// Refused before it is stored, so nothing is left behind — a task in the store is one
// this element reports as active and acts on.
func TestATaskNamingAnUnresolvableDestinationIsRefused(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	provisionAgencyA(t, srv)
	m := serve(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyB), ""))

	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeNoSuchDID {
		t.Fatalf("got %+v, want error %d (dId does not exist on the NE)", m.ErrorInformation, errCodeNoSuchDID)
	}
	if !strings.Contains(m.ErrorInformation.ErrorDescription, didAgencyB) {
		t.Errorf("the refusal does not name the identifier the ADMF has to fix: %q",
			m.ErrorInformation.ErrorDescription)
	}
	if st.Len() != 0 {
		t.Error("a task naming a destination this element cannot resolve was stored: it would be " +
			"reported as active, and the agency the warrant names would receive nothing")
	}
}

// And the mixed case, which is the one that made this a refusal rather than a report: a
// list of which some resolve and some do not is refused whole. Delivering to the subset
// is under-delivery an ADMF cannot see, and delivering the rest to the element's own
// configured endpoint is the element deciding where a warrant's product goes.
func TestAPartiallyResolvableDestinationListIsRefusedWhole(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	provisionAgencyA(t, srv)
	m := serve(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA, didAgencyB), ""))

	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeNoSuchDID {
		t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeNoSuchDID)
	}
	if st.Len() != 0 {
		t.Error("a task whose destination list only partly resolved was stored with the subset")
	}
}

// An X2andX3 destination is one destination serving two interfaces. Collapsed to a single
// endpoint type it would never yield an X2 endpoint, and the IRI would never see it.
func TestX2andX3DestinationServesBothInterfaces(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	mustAck(t, srv, createDestinationXML(didBoth, deliveryX2andX3, "10.0.60.122", tcpPort("42069"), ""))
	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2andX3, dIDs(didBoth), ""))

	for _, dt := range []types.DeliveryType{types.DeliveryX2, types.DeliveryX3} {
		got := endpointsOf(t, st, testXID, dt)
		if len(got) != 1 || got[0] != endpointA {
			t.Errorf("%s endpoints = %v, want [10.0.60.122:42069]", dt, got)
		}
	}
}

// Two warrants, two provisioned endpoints, no crossing: neither task may fall back to the
// configured endpoint the other's product goes to.
func TestTwoTasksKeepTheirOwnDestinations(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
	mustAck(t, srv, createDestinationXML(didAgencyB, deliveryX2Only, "10.0.60.123", tcpPort("42070"), ""))

	const xidB = types.XID("60b93d1e-1b53-4d63-aacb-e4d99811bc0b")
	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))
	mustAck(t, srv, activateXMLWith(string(xidB), deliveryX2Only, dIDs(didAgencyB), ""))

	for _, c := range []struct{ xid, want string }{
		{string(testXID), "10.0.60.122:42069"},
		{string(xidB), "10.0.60.123:42070"},
	} {
		got := endpointsOf(t, st, types.XID(c.xid), types.DeliveryX2)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("task %s X2 endpoints = %v, want [%s]", c.xid, got, c.want)
		}
	}
}

// A destination an operator and an ADMF agreed out of band resolves exactly as a
// provisioned one does. Nothing in either specification says a task's destination
// identifier must have arrived over X1, and refusing such an ADMF would be wrong.
func TestConfiguredDestinationResolvesLikeAProvisionedOne(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID", WithConfiguredDestinations(ConfiguredDestination{
		DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.200:42069",
	}))

	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))

	got := endpointsOf(t, st, testXID, types.DeliveryX2)
	if len(got) != 1 || got[0] != "10.0.60.200:42069" {
		t.Errorf("X2 endpoints = %v, want [10.0.60.200:42069]", got)
	}
}

// Where both sources declare the same DID, the provisioned one wins: an ADMF that has
// gone to the trouble of provisioning an endpoint has said something more recent and more
// specific than configuration.
func TestProvisionedDestinationSupersedesTheConfiguredOne(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID", WithConfiguredDestinations(ConfiguredDestination{
		DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.200:42069",
	}))

	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))

	got := endpointsOf(t, st, testXID, types.DeliveryX2)
	if len(got) != 1 || got[0] != endpointA {
		t.Errorf("X2 endpoints = %v, want the provisioned [10.0.60.122:42069]", got)
	}
}

// TestShadowingAReferencedDIDIsRefused: a DID's meaning must not change under a live
// warrant.
//
// A provisioned destination beats a configured one, but a task's endpoints are resolved
// once at activation and copied into the task. So creating one under a DID configuration
// declares changes what the element *answers* about that DID while every task activated
// before that moment keeps delivering to the configured address — and the provisioning
// function can then read the new destination back from an element still sending a live
// warrant's product to the old one.
func TestShadowingAReferencedDIDIsRefused(t *testing.T) {
	const configuredAddr = "10.0.60.200:42069"

	st := store.New()
	srv := NewServer(st, "neID", WithConfiguredDestinations(
		ConfiguredDestination{DID: didAgencyA, DeliveryType: deliveryX2Only, Address: configuredAddr},
		ConfiguredDestination{DID: didAgencyB, DeliveryType: deliveryX2Only, Address: "10.0.60.201:42069"},
	))

	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))

	m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
	if m.ErrorInformation == nil {
		t.Fatal("a DID an active task depends on was redefined beneath it")
	}
	if got := m.ErrorInformation.ErrorCode; got != errCodeCreateDestFailed {
		t.Errorf("error code = %d, want %d — the ADMF reads the code before the text", got, errCodeCreateDestFailed)
	}
	// And says which identifier, because 6000 is the generic destination-creation
	// failure: an ADMF provisioning several in one pass cannot act on the code alone.
	if d := m.ErrorInformation.ErrorDescription; !strings.Contains(d, didAgencyA) {
		t.Errorf("error description = %q, want it to name %s", d, didAgencyA)
	}

	// The element must not be able to report a destination it is not using: the answer
	// to GetDestinationDetails and what the task delivers to have to be the same address.
	if got := endpointsOf(t, st, testXID, types.DeliveryX2); len(got) != 1 || got[0] != configuredAddr {
		t.Errorf("the active task delivers to %v, want [%s]", got, configuredAddr)
	}
	reported := serve(t, srv, string(request("GetDestinationDetailsRequest",
		"\n    <ns1:dId>"+didAgencyA+"</ns1:dId>")))
	if len(reported.Destinations) != 1 {
		t.Fatalf("GetDestinationDetails reported %d destinations, want 1", len(reported.Destinations))
	}
	if got := reported.Destinations[0].Address; got != configuredAddr {
		t.Errorf("the element reports %q while the task delivers to %q", got, configuredAddr)
	}

	// The legitimate case stays open: a configured DID nothing references may still be
	// replaced, which is how an operator's static declaration gets superseded before use.
	mustAck(t, srv, createDestinationXML(didAgencyB, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
}

// Precedence resolved silently is the risk the three-source design carries. An operator
// whose configured entry is not the one in force has to be able to see that, and the only
// place an element can say it is in what it reports about its destinations.
func TestReportedDestinationsSayWhereTheyCameFrom(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID", WithConfiguredDestinations(
		ConfiguredDestination{DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.200:42069"},
		ConfiguredDestination{DID: didAgencyB, DeliveryType: deliveryX2Only, Address: "10.0.60.201:42069"},
	))
	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))

	held := srv.heldDestinations()
	if len(held) != 2 {
		t.Fatalf("reported %d destinations, want 2 (one provisioned over a configured one, one configured)", len(held))
	}

	byDID := map[string]ReportedDestination{}
	for _, d := range held {
		byDID[d.DID] = d
	}

	shadowing := byDID[didAgencyA]
	if shadowing.Configured || !shadowing.ShadowsConfigured || shadowing.Address != endpointA {
		t.Errorf("provisioned entry = %+v, want the provisioned address, marked as superseding a configured entry", shadowing)
	}
	if note := destinationProvenance(shadowing); !strings.Contains(note, "superseding") {
		t.Errorf("reported friendlyName = %q, want it to say the configured entry was superseded", note)
	}

	configured := byDID[didAgencyB]
	if !configured.Configured || configured.ShadowsConfigured {
		t.Errorf("configured entry = %+v, want it marked as configured and shadowing nothing", configured)
	}
	if note := destinationProvenance(configured); !strings.Contains(note, "configuration") {
		t.Errorf("reported friendlyName = %q, want it to say the entry is configured", note)
	}

	// And the same question asked about one identifier gets the same answer, so an ADMF
	// cannot get two accounts of one destination.
	one, ok := srv.destinationByDID(didAgencyB)
	if !ok || !one.Configured || one.Address != "10.0.60.201:42069" {
		t.Errorf("GetDestinationDetails on a configured DID = %+v, %v; want the configured entry", one, ok)
	}
}

// A configured entry that could not be dialled is dropped rather than stored. It is
// operator configuration and not a peer's message, so there is nobody to refuse — and a
// stored one would resolve a task's destination to an address nothing can reach.
func TestMalformedConfiguredDestinationsAreDropped(t *testing.T) {
	srv := NewServer(store.New(), "neID", WithConfiguredDestinations(
		ConfiguredDestination{DID: "not-a-uuid", DeliveryType: deliveryX2Only, Address: "10.0.60.200:42069"},
		ConfiguredDestination{DID: didAgencyA, DeliveryType: "X4Only", Address: "10.0.60.200:42069"},
		ConfiguredDestination{DID: didAgencyB, DeliveryType: deliveryX2Only, Address: "10.0.60.200"},
	))

	if held := srv.heldDestinations(); len(held) != 0 {
		t.Errorf("held %+v, want none: every entry was malformed", held)
	}
}

// The port defect, on the parsing side. A conformant peer sends the port as a child
// element; parsed as element text it came out as zero, so the element would have stored a
// destination pointing nowhere and delivered nothing to it — and never noticed, because
// the only peer it has ever spoken to is another copy of itself sending the same wrong
// shape.
func TestConformantPortShapeParses(t *testing.T) {
	for _, c := range []struct{ name, portXML string }{
		{"TCPPort", `<c:TCPPort>42069</c:TCPPort>`},
		{"UDPPort", `<c:UDPPort>42069</c:UDPPort>`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := NewServer(store.New(), "neID")
			mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", c.portXML, ""))

			d, ok := srv.destinationByDID(didAgencyA)
			if !ok || d.Address != endpointA {
				t.Errorf("stored destination = %+v, %v; want 10.0.60.122:42069", d, ok)
			}
		})
	}
}

// A port as the element's own text, rather than the TCPPort/UDPPort choice, is not a port.
// Refusing beats storing a destination with no port, which is a task that delivers
// nowhere while the ADMF is told all is well.
func TestPortAsElementTextIsRefused(t *testing.T) {
	srv := NewServer(store.New(), "neID")
	m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", "42069", ""))
	if m.ErrorInformation == nil {
		t.Fatalf("want a refusal, got %+v", m)
	}
	if _, ok := srv.destinationByDID(didAgencyA); ok {
		t.Error("a refused destination must not be stored")
	}
}

// TS 103 221-1 clause 6.3.1.1: "it is an error if the DID is already present at the NE."
// The code matters on its own — an ADMF re-provisioning after a restart tells "already
// there" from "something went wrong" by the code before it reads any text.
func TestDuplicateDestinationIsRefusedWithItsOwnCode(t *testing.T) {
	srv := NewServer(store.New(), "neID")
	body := createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), "")
	mustAck(t, srv, body)

	m := serve(t, srv, body)
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeDIDExists {
		t.Fatalf("re-creating a DID = %+v, want error %d", m.ErrorInformation, errCodeDIDExists)
	}
	// The description is matched on by a peer that predates the code, so it stays.
	if !strings.Contains(m.ErrorInformation.ErrorDescription, "already present") {
		t.Errorf("description = %q, want it to still say the destination is already present",
			m.ErrorInformation.ErrorDescription)
	}
}

// The schema types a DId as a TS 103 280 UUID. Surrounding whitespace is not among the
// refusals: UUID derives from xs:token, so the pattern applies to the collapsed value (see
// TestTokenWhitespaceIsNotPartOfAnIdentifier). Accepting anything else means
// interoperating with a provisioning function no conformant one would produce, and hiding
// the format defect on both sides until it surfaces as a mismatch neither can attribute.
func TestDestinationIdentifierMustBeAUUID(t *testing.T) {
	for _, did := range []string{
		"pre-shared-did",
		"11111111-1111-4111-8111",              // too short
		"11111111-1111-4111-8111-11111111111G", // not hex
		"11111111111141118111111111111111",     // unhyphenated
		"11111111-1111-4111-8111-11111111111A", // uppercase, which the pattern excludes
	} {
		t.Run(did, func(t *testing.T) {
			srv := NewServer(store.New(), "neID")
			m := serve(t, srv, createDestinationXML(did, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
			}
			if len(srv.heldDestinations()) != 0 {
				t.Error("a refused destination must not be stored")
			}
		})
	}
}

// The refusal rule, per field. Each entry is a task that, accepted, would have the field
// thrown away, and the difference between the two halves of this test is the whole
// subject: a field is disregarded only where the specification addresses it to a function
// this element is not, or where disregarding it cannot change what is intercepted or
// where the product goes.
func TestTaskFieldsThatCannotBeHonouredAreRefused(t *testing.T) {
	const ext3GPP = "urn:3GPP:ns:li:3GPPX1Extensions:r18:v6"

	for _, c := range []struct {
		name      string
		afterDIDs string
		wantIn    string
	}{
		{
			name:      "a traffic policy to be applied to the task",
			afterDIDs: "\n      <ns1:listOfTrafficPolicyReferences><ns1:trafficPolicyReference>" + didAgencyB + "</ns1:trafficPolicyReference></ns1:listOfTrafficPolicyReferences>",
			wantIn:    "listOfTrafficPolicyReferences",
		},
		{
			name:      "an extension owned by somebody we do not know",
			afterDIDs: `<ns1:taskDetailsExtensions><ns1:Owner>SomeVendor</ns1:Owner></ns1:taskDetailsExtensions>`,
			wantIn:    "SomeVendor",
		},
		{
			name: "a 3GPP extension whose content we do not model",
			afterDIDs: `<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>` +
				`<ext:HeaderReporting xmlns:ext="` + ext3GPP + `"/></ns1:taskDetailsExtensions>`,
			wantIn: "HeaderReporting",
		},
		{
			name: "a recognised extension carrying a value outside its enumeration",
			afterDIDs: `<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>` +
				`<ext:IdentifierAssociationExtensions xmlns:ext="` + ext3GPP + `">` +
				`<ext:IdentifierAssociationEventsGenerated>Some</ext:IdentifierAssociationEventsGenerated>` +
				`</ext:IdentifierAssociationExtensions></ns1:taskDetailsExtensions>`,
			wantIn: "IdentifierAssociationEventsGenerated",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := NewServer(st, "neID")
			m := serve(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), c.afterDIDs))

			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeActivateFailed {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeActivateFailed)
			}
			if !strings.Contains(m.ErrorInformation.ErrorDescription, c.wantIn) {
				t.Errorf("description %q does not name %q — a refusal an ADMF cannot act on is barely better than silence",
					m.ErrorInformation.ErrorDescription, c.wantIn)
			}
			if st.Len() != 0 {
				t.Error("a refused task must not be applied")
			}
		})
	}
}

// Destinations named only by set. A dSId names a DestinationSetDetails Generic Object,
// and this element implements no Generic Objects, so the identifier can never name
// anything it holds — while carrying failover and duplication semantics an
// acknowledgement would silently promise.
func TestDestinationSetsAreRefused(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	body := activateXMLWith(string(testXID), deliveryX2Only,
		"\n        <ns1:dSId>"+didAgencyB+"</ns1:dSId>", "")
	m := serve(t, srv, body)
	if m.ErrorInformation == nil || !strings.Contains(m.ErrorInformation.ErrorDescription, "dSId") {
		t.Fatalf("got %+v, want a refusal naming dSId", m.ErrorInformation)
	}
	if st.Len() != 0 {
		t.Error("a refused task must not be applied")
	}
}

// A modification refused for the same reason gets the modification's own code. The
// registry has separate entries for the two, and an ADMF reconciling a failure needs to
// know which operation failed.
func TestARefusedModificationCarriesTheModifyCode(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")
	provisionAgencyA(t, srv)
	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))

	body := strings.Replace(
		activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA),
			"\n      <ns1:listOfTrafficPolicyReferences><ns1:trafficPolicyReference>"+didAgencyB+
				"</ns1:trafficPolicyReference></ns1:listOfTrafficPolicyReferences>"),
		"ActivateTaskRequest", "ModifyTaskRequest", 1)
	m := serve(t, srv, body)
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeModifyFailed {
		t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeModifyFailed)
	}
}

// The other half of the rule. Both of these fields read, from their names, like ones to
// refuse; their descriptions say otherwise, and refusing them would refuse a conformant
// task.
func TestDisregardedTaskFieldsAreAcceptedAndApplied(t *testing.T) {
	for _, c := range []struct{ name, afterDIDs string }{
		{
			// "for use by an NE that is performing mediation … This shall be included
			// between the ADMF and the MDF." A POI is not an MDF.
			name: "mediation details, which are addressed to an MDF",
			afterDIDs: "\n      <ns1:listOfMediationDetails><ns1:mediationDetails>" +
				"<ns1:LIID>LIID-1</ns1:LIID><ns1:deliveryType>HI2Only</ns1:deliveryType>" +
				"</ns1:mediationDetails></ns1:listOfMediationDetails>",
		},
		{
			// A permission to self-deactivate on completion. This element never
			// concludes that a task has completed, so nothing is lost either way.
			name:      "permission to deactivate implicitly",
			afterDIDs: "\n      <ns1:implicitDeactivationAllowed>true</ns1:implicitDeactivationAllowed>",
		},
		{
			name:      "permission to deactivate implicitly, withheld",
			afterDIDs: "\n      <ns1:implicitDeactivationAllowed>false</ns1:implicitDeactivationAllowed>",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := NewServer(st, "neID")
			mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), ""))
			mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), c.afterDIDs))

			// Accepted *and applied*: an acknowledgement with no task behind it would
			// pass an assertion about the response and fail the point of the test.
			if got := endpointsOf(t, st, testXID, types.DeliveryX2); len(got) != 1 {
				t.Errorf("X2 endpoints = %v, want the task to have been applied in full", got)
			}
		})
	}
}

// A destination extension is refused for the same reason a task's is: on this interface
// an extension exists to change the meaning of the message that carries it, and on a
// destination that means changing where product goes.
func TestDestinationExtensionsAreRefused(t *testing.T) {
	srv := NewServer(store.New(), "neID")
	m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"),
		"\n      <ns1:destinationDetailsExtensions><ns1:Owner>SomeVendor</ns1:Owner></ns1:destinationDetailsExtensions>"))

	if m.ErrorInformation == nil {
		t.Fatalf("want a refusal, got %+v", m)
	}
	if len(srv.heldDestinations()) != 0 {
		t.Error("a refused destination must not be stored")
	}
}

// The one task extension this element acts on. TS 33.128 table 6.2.2.1-1 makes it
// conditional on the AMF IRI-POI's ActivateTask and gives it force in both directions:
// absent, the identifier-association records "shall not be generated"; present as
// "IdentifierAssociation", *only* those records and AMFLocationUpdate are.
func TestIdentifierAssociationExtensionSetsTheTaskRecordScope(t *testing.T) {
	const ext3GPP = "urn:3GPP:ns:li:3GPPX1Extensions:r18:v6"
	extension := func(events string) string {
		return `<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>` +
			`<ext:IdentifierAssociationExtensions xmlns:ext="` + ext3GPP + `">` +
			`<ext:IdentifierAssociationEventsGenerated>` + events + `</ext:IdentifierAssociationEventsGenerated>` +
			`</ext:IdentifierAssociationExtensions></ns1:taskDetailsExtensions>`
	}

	for _, c := range []struct {
		name              string
		afterDIDs         string
		want              types.RecordScope
		wantAssociation   bool
		wantOtherPreserve bool
	}{
		{
			name: "no extension", afterDIDs: "", want: types.RecordScopeStandard,
			wantAssociation: false, wantOtherPreserve: true,
		},
		{
			name: "IdentifierAssociation", afterDIDs: extension("IdentifierAssociation"),
			want: types.RecordScopeIdentifierAssociation, wantAssociation: true, wantOtherPreserve: false,
		},
		{
			name: "All", afterDIDs: extension("All"),
			want: types.RecordScopeAll, wantAssociation: true, wantOtherPreserve: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := NewServer(st, "neID")
			provisionAgencyA(t, srv)
			mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), c.afterDIDs))

			task, ok := st.Get(testXID)
			if !ok {
				t.Fatal("task was not stored")
			}
			if task.RecordScope != c.want {
				t.Errorf("RecordScope = %q, want %q", task.RecordScope, c.want)
			}
			if got := task.WantsIdentifierAssociation(); got != c.wantAssociation {
				t.Errorf("WantsIdentifierAssociation = %v, want %v", got, c.wantAssociation)
			}
			if got := task.WantsGeneralRecords(); got != c.wantOtherPreserve {
				t.Errorf("WantsGeneralRecords = %v, want %v", got, c.wantOtherPreserve)
			}
		})
	}
}

// A peer must not be able to choose header values that make this element's own answer
// invalid — least of all on the answer refusing it, whose request has been trusted by
// nothing. A conformant ADMF discards a reply that does not validate, so an invalid
// refusal is a refusal that was never sent.
func TestMalformedRequestHeadersAreNotEchoedBack(t *testing.T) {
	srv := NewServer(store.New(), "neID")

	body := strings.NewReplacer(
		"v1.6.1", "not-a-version",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "not-a-uuid",
	).Replace(string(request("PingRequest", "")))

	m := serve(t, srv, body)
	if m.Version != supportedVersion {
		t.Errorf("answer version %q, want this element's %q", m.Version, supportedVersion)
	}
	if !types.ValidUUID(m.X1TransactionID) {
		t.Errorf("echoed x1TransactionId %q is not a UUID", m.X1TransactionID)
	}

	// A conformant peer's transaction identifier is its own, untouched: the substitution
	// costs an ADMF its correlation, and only a peer that sent something no conformant ADMF
	// would send pays it.
	ok := serve(t, srv, string(request("PingRequest", "")))
	if ok.X1TransactionID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Errorf("a conformant peer's x1TransactionId was altered: %q", ok.X1TransactionID)
	}
}

// TestAnAnswerCarriesTheVersionThisElementEncodedItIn: TS 103 221-1 table 6.1-1 defines
// Version as the version of the present document used to encode the message, so an answer
// states this element's version, not the version the request was encoded in.
func TestAnAnswerCarriesTheVersionThisElementEncodedItIn(t *testing.T) {
	srv := NewServer(store.New(), "neID")

	for _, v := range []string{supportedVersion, "v1.21.1", "v1.0.0"} {
		body := strings.Replace(string(request("PingRequest", "")), "<ns1:version>"+supportedVersion+"</ns1:version>",
			"<ns1:version>"+v+"</ns1:version>", 1)
		if !strings.Contains(body, "<ns1:version>"+v+"</ns1:version>") {
			t.Fatalf("the request fixture does not carry a version element to replace")
		}

		if got := serve(t, srv, body).Version; got != supportedVersion {
			t.Errorf("a request in %s was answered in %q, want this element's %q", v, got, supportedVersion)
		}
	}
}

// A reported task carries the DIDs it was given. listOfDIDs is mandatory inside
// taskDetails, so omitting it made every answer that reports a task invalid — and left an
// ADMF auditing this element unable to see which destinations a task it holds names.
func TestAReportedTaskCarriesItsDestinationIdentifiers(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	provisionAgencyA(t, srv)
	// Both provisioned: a task naming a destination the element cannot resolve is
	// refused, so the reporting property is asserted with two resolvable ones.
	mustAck(t, srv, createDestinationXML(didAgencyB, deliveryX2Only, "10.0.60.123", tcpPort("42069"), ""))
	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA, didAgencyB), ""))

	m := serve(t, srv, string(request("GetTaskDetailsRequest", "\n    <ns1:xId>"+string(testXID)+"</ns1:xId>")))
	out, err := marshalResponse(&X1Response{Messages: []X1ResponseMessage{m}})
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}

	// Both, including the one that resolved to nothing: what is reported is what the
	// task names, not what this element happened to be able to dial.
	for _, did := range []string{didAgencyA, didAgencyB} {
		if !strings.Contains(string(out), "<ns1:dId>"+did+"</ns1:dId>") {
			t.Errorf("reported task omits dId %s:\n%s", did, out)
		}
	}
}

// TestTaskIdentifiersMustBeUUIDs covers the sharpest case of the identifier rule, and the
// one a scenario about destinations does not reach.
//
// The schema types a task's `xId` and its `productID` as `etsi103280:UUID`, exactly as it
// types a `dId`. A malformed one is not cosmetic: `types.XID.Bytes()` maps an unparseable
// value to sixteen zero bytes, an MDF discards product it cannot attribute to a warrant,
// and neither side reports anything. So the element accepted the task, ran the
// interception, delivered every record under a label the mediation function throws away,
// and told the ADMF it was done. Refusing at the door is the only place that is visible.
func TestTaskIdentifiersMustBeUUIDs(t *testing.T) {
	const goodXID = "50b93d1e-1b53-4d63-aacb-e4d99811bc0b"

	for _, c := range []struct {
		name      string
		xid       string
		afterDIDs string
	}{
		{name: "xId is not a UUID", xid: "warrant-42"},
		{name: "xId is uppercase, which the pattern excludes", xid: strings.ToUpper(goodXID)},
		{name: "xId is unhyphenated", xid: strings.ReplaceAll(goodXID, "-", "")},
		{
			name: "productID is not a UUID", xid: goodXID,
			afterDIDs: "\n      <ns1:productID>warrant-42</ns1:productID>",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := NewServer(st, "neID")
			m := serve(t, srv, activateXMLWith(c.xid, deliveryX2Only, dIDs(didAgencyA), c.afterDIDs))

			// 1010 names what is wrong — a value outside the format the schema defines —
			// where a generic activate failure would name only where it happened.
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
			}
			if st.Len() != 0 {
				t.Error("a refused task must not be applied: its product would be delivered " +
					"under a label no MDF can attribute")
			}
		})
	}

	// A ModifyTask naming a malformed xId gets the same code, not "no such task". 2020
	// would be true — no task with that identifier exists — but it would point the ADMF at
	// activating a task that would fail for the same reason. The registry says to use the
	// most specific code available, and 1010 names the actual fault.
	t.Run("a modification naming a malformed xId", func(t *testing.T) {
		st := store.New()
		srv := NewServer(st, "neID")
		body := strings.Replace(
			activateXMLWith("warrant-42", deliveryX2Only, dIDs(didAgencyA), ""),
			"ActivateTaskRequest", "ModifyTaskRequest", 1)
		m := serve(t, srv, body)
		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
			t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
		}
		if st.Len() != 0 {
			t.Error("a refused modification must not create a task")
		}
	})

	// An absent xId is still "missing", not "not a UUID": the mandatory-field check says
	// the clearer thing, and the two are different facts to be told.
	t.Run("an absent xId is reported as missing", func(t *testing.T) {
		srv := NewServer(store.New(), "neID")
		m := serve(t, srv, activateXMLWith("", deliveryX2Only, dIDs(didAgencyA), ""))
		if m.ErrorInformation == nil ||
			!strings.Contains(m.ErrorInformation.ErrorDescription, "missing xId") {
			t.Fatalf("got %+v, want a refusal saying the xId is missing", m.ErrorInformation)
		}
	})

	// And the conformant pair is untouched, including the ProductID a triggering function
	// supplies to label product with the warrant rather than with its own trigger task.
	st := store.New()
	srv := NewServer(st, "neID")
	provisionAgencyA(t, srv)
	mustAck(t, srv, activateXMLWith(goodXID, deliveryX2Only, dIDs(didAgencyA),
		"\n      <ns1:productID>cccccccc-cccc-4ccc-8ccc-cccccccccccc</ns1:productID>"))

	task, ok := st.Get(types.XID(goodXID))
	if !ok {
		t.Fatal("a conformant task was not stored")
	}
	if task.DeliveryXID().IsZero() {
		t.Error("the label this task's product carries is all zeros, which an MDF discards")
	}
}

// TestATaskWantingX2NamingOnlyAnX3DestinationIsRefused is the other half of the fallback
// scenario, and the half a "names nothing" test does not reach: a task *can* name a
// destination this element holds and still resolve no endpoint for the product it wants.
//
// A destination provisioned X3Only carries content. Delivering signalling to it would be a
// disclosure to an endpoint the ADMF designated for something else; acknowledging the task
// and delivering to this element's configured MDF2 instead would be the element choosing an
// agency the task did not name. TS 103 221-1 clause 6.2.1.2 has the element reject the
// combination, with 3040. The other mismatches are in TestEveryOrderedProductNeedsANamedDestination.
func TestATaskWantingX2NamingOnlyAnX3DestinationIsRefused(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "neID")

	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX3Only, "10.0.60.122", tcpPort("42069"), ""))
	m := serve(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))

	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeBadDeliveryCombination {
		t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeBadDeliveryCombination)
	}
	if st.Len() != 0 {
		t.Error("a task with no destination for the product it orders was stored")
	}
}

// TestDeactivationRefusesWhatItCannotHonour covers the answer TS 103 221-1 table 6.2.3-2
// requires, whose opposite fails in the most costly direction available.
//
// "OK or Error — … Also, it is an error if the XID is not already present at the NE." It is
// the mirror of the CreateDestination rule answered with 2030, and an unconditional
// acknowledgement here is worse than either: an ADMF withdrawing a warrant with a mistyped
// XID was told the withdrawal completed while the interception went on running, and
// interception outliving its authority is the one direction this plane must never fail in.
func TestDeactivationRefusesWhatItCannotHonour(t *testing.T) {
	deactivate := func(xid string) string {
		return strings.Replace(
			string(request("DeactivateTaskRequest", "\n    <ns1:xId>"+xid+"</ns1:xId>")),
			"ActivateTaskRequest", "DeactivateTaskRequest", 1)
	}

	for _, c := range []struct {
		name string
		xid  string
		want int
	}{
		{
			// Not a UUID, so no conformant ADMF could have provisioned it and nothing can
			// be holding it. The format is the more specific fault, so it is the one named.
			name: "an xId that is not a UUID", xid: "warrant-42", want: errCodeSchemaError,
		},
		{
			// Well formed, and simply not here — the likelier mistake by far, and the one a
			// format check alone would leave answering "completed".
			name: "a well-formed xId the element does not hold",
			xid:  "cccccccc-cccc-4ccc-8ccc-cccccccccccc", want: errCodeNoSuchTask,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := NewServer(store.New(), "neID")
			m := serve(t, srv, deactivate(c.xid))
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != c.want {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, c.want)
			}
			if m.OK != "" {
				t.Error("a refused deactivation must not also acknowledge")
			}
		})
	}

	// And a deactivation the element *can* honour still succeeds, and still runs the
	// teardown a POI needs to undo product it applied for the target.
	t.Run("a task the element holds", func(t *testing.T) {
		st := store.New()
		var torn []types.XID
		srv := NewServer(st, "neID", OnTaskChange(func(prev, next *types.InterceptTask) {
			if next == nil {
				torn = append(torn, prev.XID)
			}
		}))
		provisionAgencyA(t, srv)
		mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2Only, dIDs(didAgencyA), ""))
		mustAck(t, srv, deactivate(string(testXID)))

		if st.Len() != 0 {
			t.Error("the task was not removed")
		}
		if len(torn) != 1 || torn[0] != testXID {
			t.Errorf("the teardown ran for %v, want exactly [%s]", torn, testXID)
		}
	})
}
