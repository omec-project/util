// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// The subject of this file is what encoding/xml does with a message that carries an element
// twice where the schema allows it once: it keeps one, and the element acted on the decoder's
// choice. Each case below decoded to a task or destination this element held and acknowledged.

// withTaskDetails rewrites the fixture's taskDetails children: old is replaced, once.
func withTaskDetails(old, replacement string) string {
	if !strings.Contains(activateXML, old) {
		panic("fixture does not contain " + old)
	}

	return strings.Replace(activateXML, old, replacement, 1)
}

// ext3GPP is the 3GPP X1 extension namespace, as the extension elements declare it.
const ext3GPP = NamespaceX1Ext3GPP

const fixtureXID = "<ns1:xId>50b93d1e-1b53-4d63-aacb-e4d99811bc0b</ns1:xId>"

// TestARepeatedSingleElementIsRefused: every case is a message the schema refuses and the
// decoder read as one of its readings.
func TestARepeatedSingleElementIsRefused(t *testing.T) {
	const fseid = `<ns2:FSEID><ns2:SEID>42</ns2:SEID></ns2:FSEID>`

	for _, c := range []struct {
		name, body string
	}{
		{"an arm repeated", withTarget(
			`<ns1:supiimsi>208930100007488</ns1:supiimsi><ns1:supiimsi>208930100007489</ns1:supiimsi>`)},
		{"an empty arm beside a populated one", withTarget(
			`<ns1:imsi/><ns1:supiimsi>208930100007488</ns1:supiimsi>`)},
		{"an empty arm on its own", withTarget(`<ns1:supiimsi/>`)},
		{"a criterion carrying two F-SEIDs", withTarget(ext(fseid + `<ns2:FSEID><ns2:SEID>43</ns2:SEID></ns2:FSEID>`))},
		{"a criterion carrying two PDRIDs", withTarget(ext(`<ns2:PDRID>1</ns2:PDRID><ns2:PDRID>2</ns2:PDRID>`))},
		{"an F-SEID carrying two SEIDs", withTarget(ext(`<ns2:FSEID><ns2:SEID>42</ns2:SEID><ns2:SEID>43</ns2:SEID></ns2:FSEID>`))},
		{"an F-TEID carrying two TEIDs", withTarget(ext(`<ns2:FTEID><ns2:TEID>5</ns2:TEID><ns2:TEID>6</ns2:TEID></ns2:FTEID>`))},
		{"an extension carrying two Owners", withTarget(
			strings.Replace(ext(fseid), `<ns1:Owner>3GPP</ns1:Owner>`, `<ns1:Owner>3GPP</ns1:Owner><ns1:Owner>3GPP</ns1:Owner>`, 1))},
		{"an LI_T3 container carrying an element its schema does not define", withTarget(
			strings.Replace(ext(fseid), `</ns2:UPFLIT3TargetIdentifierExtensions>`,
				`<ns2:Stray/></ns2:UPFLIT3TargetIdentifierExtensions>`, 1))},
		{"two xIds", withTaskDetails(fixtureXID, fixtureXID+
			"<ns1:xId>cccccccc-cccc-4ccc-8ccc-cccccccccccc</ns1:xId>")},
		{"two deliveryTypes", withTaskDetails("<ns1:deliveryType>X2andX3</ns1:deliveryType>",
			"<ns1:deliveryType>X2andX3</ns1:deliveryType><ns1:deliveryType>X2Only</ns1:deliveryType>")},
		{"two targetIdentifiers lists", withTaskDetails("<ns1:deliveryType>",
			"<ns1:targetIdentifiers><ns1:targetIdentifier><ns1:imsi>208930100007488</ns1:imsi>"+
				"</ns1:targetIdentifier></ns1:targetIdentifiers><ns1:deliveryType>")},
		{"two listOfDIDs", withTaskDetails("</ns1:taskDetails>",
			"<ns1:listOfDIDs><ns1:dId>"+testDIDInActivateXML+"</ns1:dId></ns1:listOfDIDs></ns1:taskDetails>")},
		{"two correlationIDs", withTaskDetails("</ns1:taskDetails>",
			"<ns1:correlationID>1</ns1:correlationID><ns1:correlationID>2</ns1:correlationID></ns1:taskDetails>")},
		{"two productIDs", withTaskDetails("</ns1:taskDetails>",
			"<ns1:productID>cccccccc-cccc-4ccc-8ccc-cccccccccccc</ns1:productID>"+
				"<ns1:productID>dddddddd-dddd-4ddd-8ddd-dddddddddddd</ns1:productID></ns1:taskDetails>")},
		{"a task extension carrying two Owners", withTaskDetails("</ns1:taskDetails>",
			`<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner><ns1:Owner>3GPP</ns1:Owner>`+
				identifierAssociation("All")+`</ns1:taskDetailsExtensions></ns1:taskDetails>`)},
		{"an identifier-association extension carrying two values", withTaskDetails("</ns1:taskDetails>",
			`<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>`+
				`<ext:IdentifierAssociationExtensions xmlns:ext="`+ext3GPP+`">`+
				`<ext:IdentifierAssociationEventsGenerated>All</ext:IdentifierAssociationEventsGenerated>`+
				`<ext:IdentifierAssociationEventsGenerated>IdentifierAssociation</ext:IdentifierAssociationEventsGenerated>`+
				`</ext:IdentifierAssociationExtensions></ns1:taskDetailsExtensions></ns1:taskDetails>`)},
		{"a message carrying two taskDetails", strings.Replace(activateXML, "</ns1:x1RequestMessage>",
			"<ns1:taskDetails/></ns1:x1RequestMessage>", 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			m := serve(t, testServer(st), c.body)

			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
			}
			if st.Len() != 0 {
				t.Error("a task read one way out of a message the schema refuses was stored")
			}
		})
	}
}

// identifierAssociation is the TS 33.128 identifier-association extension element.
func identifierAssociation(scope string) string {
	return `<ext:IdentifierAssociationExtensions xmlns:ext="` + ext3GPP + `">` +
		`<ext:IdentifierAssociationEventsGenerated>` + scope + `</ext:IdentifierAssociationEventsGenerated>` +
		`</ext:IdentifierAssociationExtensions>`
}

// TestARepeatedDeactivationIdentifierIsRefused: the message-level form, on the request whose
// misreading costs most. Two xIds, and the decoder kept the second, so the element withdrew a
// warrant the ADMF may not have meant -- or kept running the one it did.
func TestARepeatedDeactivationIdentifierIsRefused(t *testing.T) {
	st := store.New()
	srv := testServer(st)
	mustAck(t, srv, activateXML)

	m := serve(t, srv, string(request("DeactivateTaskRequest",
		"\n    <ns1:xId>cccccccc-cccc-4ccc-8ccc-cccccccccccc</ns1:xId>\n    "+fixtureXID)))
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
		t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
	}
	if m.Type != errorResponse || requestMessageType(m.RequestType) != "DeactivateTask" {
		t.Errorf("answered as %q for %q, want an ErrorResponse naming DeactivateTask", m.Type, m.RequestType)
	}
	if st.Len() != 1 {
		t.Error("the task was withdrawn on the decoder's reading of a malformed deactivation")
	}
}

// TestTwoIdentifierAssociationExtensionsAreRefused: each placeholder admits any number of
// extension elements, so this is schema-valid -- and the task holds one scope, so it would run
// under whichever the decode kept. Refused with the operation's code, not as malformed.
func TestTwoIdentifierAssociationExtensionsAreRefused(t *testing.T) {
	for _, c := range []struct{ name, exts string }{
		{"in one placeholder", `<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>` +
			identifierAssociation("All") + identifierAssociation("IdentifierAssociation") +
			`</ns1:taskDetailsExtensions>`},
		{"in two placeholders", `<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>` +
			identifierAssociation("All") + `</ns1:taskDetailsExtensions>` +
			`<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>` +
			identifierAssociation("IdentifierAssociation") + `</ns1:taskDetailsExtensions>`},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			m := serve(t, testServer(st), withTaskDetails("</ns1:taskDetails>", c.exts+"</ns1:taskDetails>"))
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeActivateFailed {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeActivateFailed)
			}
			if st.Len() != 0 {
				t.Error("a task with two record scopes was stored under one of them")
			}
		})
	}
}

// TestAnIdentifierBesideTheLIT3CriteriaIsRefused: the extension placeholder's content is an
// xs:any, and encoding/xml dropped what no field claimed -- so an identifier type 3GPP defines
// and this element does not model, sent beside the LI_T3 criteria, was read as the criteria
// alone, and the task ran under fewer conditions than ordered. Refused as an identifier type
// this element does not support (3010), whether it stands beside the criteria or alone. Two
// LI_T3 containers in one extension are an unsupported combination (3020): the schema admits
// them, and the decode kept the last.
func TestAnIdentifierBesideTheLIT3CriteriaIsRefused(t *testing.T) {
	const fseid = `<ns2:FSEID><ns2:SEID>42</ns2:SEID></ns2:FSEID>`
	const akma = `<ns2:AKMAIdentifier>a-kid@example.net</ns2:AKMAIdentifier>`
	container := `<ns2:UPFLIT3TargetIdentifierExtensions><ns2:UPFLIT3TargetIdentifier>` + fseid +
		`</ns2:UPFLIT3TargetIdentifier></ns2:UPFLIT3TargetIdentifierExtensions>`

	for _, c := range []struct {
		name, xml string
		code      int
	}{
		{"an AKMA identifier beside the criteria", strings.Replace(ext(fseid),
			`<ns1:Owner>3GPP</ns1:Owner>`, `<ns1:Owner>3GPP</ns1:Owner>`+akma, 1), errCodeUnsupportedTarget},
		{"an AKMA identifier alone", `<ns1:targetIdentifierExtension><ns1:Owner>3GPP</ns1:Owner>` +
			akma + `</ns1:targetIdentifierExtension>`, errCodeUnsupportedTarget},
		{"two LI_T3 containers", `<ns1:targetIdentifierExtension><ns1:Owner>3GPP</ns1:Owner>` +
			container + container + `</ns1:targetIdentifierExtension>`, errCodeBadTargetCombination},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			m := serve(t, testServer(st), withTarget(c.xml))
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != c.code {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, c.code)
			}
			if st.Len() != 0 {
				t.Error("a task was stored with part of its target identifier dropped")
			}
		})
	}
}

// TestTaskScalarsOutsideTheirTypesAreMalformed: an empty or non-integer value in a field the
// schema types is the message violating its format (1010), not a task this element declines.
// An empty element decoded to the same "" as an absent one, so an empty productID or
// correlationID was read as not sent, and an empty targetIdentifiers as a task the element
// could not apply (3000).
func TestTaskScalarsOutsideTheirTypesAreMalformed(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"an empty targetIdentifiers", withTaskDetails(
			activateXML[strings.Index(activateXML, "<ns1:targetIdentifiers>"):strings.Index(activateXML, "</ns1:targetIdentifiers>")],
			"<ns1:targetIdentifiers>")},
		{"a correlationID that is not an integer", withTaskDetails("</ns1:taskDetails>",
			"<ns1:correlationID>twelve</ns1:correlationID></ns1:taskDetails>")},
		{"an empty correlationID", withTaskDetails("</ns1:taskDetails>",
			"<ns1:correlationID> </ns1:correlationID></ns1:taskDetails>")},
		{"an empty productID", withTaskDetails("</ns1:taskDetails>", "<ns1:productID/></ns1:taskDetails>")},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			// An element that honours a correlation, so the correlationID cases are refused
			// for their value and not for being sent at all.
			m := serve(t, testServer(st, HonoursCorrelationID()), c.body)
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
			}
			if st.Len() != 0 {
				t.Error("a malformed task was stored")
			}
		})
	}
}

// TestIntegersTakeTheSchemasLexicalForms: xs:integer admits a leading "+" and surrounding
// whitespace, which strconv refuses -- and a refusal there failed the decode of the whole
// container, so a conformant message was answered with a top-level error. Each value here is
// conformant and is stored as the number it spells.
func TestIntegersTakeTheSchemasLexicalForms(t *testing.T) {
	for _, c := range []struct {
		name, xml string
		want      types.TargetIdentifier
	}{
		{"an SEID", ext(`<ns2:FSEID><ns2:SEID>+42</ns2:SEID></ns2:FSEID>`), types.TargetIdentifier{Type: types.TargetFSEID, Value: "42"}},
		{"a TEID", ext(`<ns2:FTEID><ns2:TEID> +007 </ns2:TEID></ns2:FTEID>`), types.TargetIdentifier{Type: types.TargetFTEID, Value: "7"}},
		{"a PDRID", ext(`<ns2:PDRID>+5</ns2:PDRID>`), types.TargetIdentifier{Type: types.TargetPDRID, Value: "5"}},
		{"a QERID", ext(`<ns2:QERID>+6</ns2:QERID>`), types.TargetIdentifier{Type: types.TargetQERID, Value: "6"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			mustAck(t, testServer(st), withTarget(c.xml))
			task, _ := st.Get(testXID)
			if len(task.Targets) != 1 || task.Targets[0] != c.want {
				t.Errorf("targets = %+v, want [%+v]", task.Targets, c.want)
			}
		})
	}

	t.Run("a destination port", func(t *testing.T) {
		st := store.New()
		srv := NewServer(st, "neID")
		mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("+42069"), ""))
		if d, _ := srv.destinationByDID(didAgencyA); d.Address != endpointA {
			t.Errorf("address = %q, want %q", d.Address, endpointA)
		}
	})
}

// TestAnIntegerOutsideItsTypeRefusesOneMessage: a value outside its type is that message's
// schema error, answered 1010 against it. Decoded with strconv it failed the whole container,
// so the other messages in it were answered with a top-level error too and none was applied.
func TestAnIntegerOutsideItsTypeRefusesOneMessage(t *testing.T) {
	for _, c := range []struct{ name, bad string }{
		{"an SEID that is not a number", withTarget(ext(`<ns2:FSEID><ns2:SEID>forty-two</ns2:SEID></ns2:FSEID>`))},
		{"a negative TEID", withTarget(ext(`<ns2:FTEID><ns2:TEID>-5</ns2:TEID></ns2:FTEID>`))},
		{"a PDRID past 32 bits", withTarget(ext(`<ns2:PDRID>4294967296</ns2:PDRID>`))},
		{"an empty QERID", withTarget(ext(`<ns2:QERID/>`))},
		{"a port past 65535", createDestinationXML(didAgencyB, deliveryX2Only, "10.0.60.122", tcpPort("70000"), "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := testServer(st)

			// The malformed message first, then a conformant one, in one container.
			second := createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), "")
			body := strings.Replace(c.bad, "</ns1:X1Request>",
				second[strings.Index(second, "<ns1:x1RequestMessage"):], 1)
			resp, err := srv.Process([]byte(body), admfPeer(t))
			if err != nil {
				t.Fatalf("the whole container was refused for one message's value: %v", err)
			}
			if len(resp.Messages) != 2 {
				t.Fatalf("got %d answers, want 2", len(resp.Messages))
			}
			if e := resp.Messages[0].ErrorInformation; e == nil || e.ErrorCode != errCodeSchemaError {
				t.Errorf("the malformed message got %+v, want error %d", e, errCodeSchemaError)
			}
			if resp.Messages[1].OK == "" {
				t.Errorf("the conformant message beside it was not applied: %+v", resp.Messages[1].ErrorInformation)
			}
		})
	}
}

// TestFSEIDAddressesAreCheckedButNotMatched: the addresses an F-SEID carries are not part of
// the criterion -- within one UPF the SEID is unambiguous -- but each is held to its schema type,
// so a malformed one is refused rather than acknowledged unread.
func TestFSEIDAddressesAreCheckedButNotMatched(t *testing.T) {
	st := store.New()
	m := serve(t, testServer(st), withTarget(ext(
		`<ns2:FSEID><ns2:SEID>42</ns2:SEID><ns2:IPv4Address>10.0.0.256</ns2:IPv4Address></ns2:FSEID>`)))
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
		t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
	}

	// A dual-stack node's pair is valid, and the criterion is the SEID alone.
	st = store.New()
	mustAck(t, testServer(st), withTarget(ext(`<ns2:FSEID><ns2:SEID>42</ns2:SEID>`+
		`<ns2:IPv4Address>10.0.0.1</ns2:IPv4Address><ns2:IPv6Address>2001:db8::1</ns2:IPv6Address></ns2:FSEID>`)))
	task, _ := st.Get(testXID)
	if want := (types.TargetIdentifier{Type: types.TargetFSEID, Value: "42"}); len(task.Targets) != 1 || task.Targets[0] != want {
		t.Errorf("targets = %+v, want [%+v]", task.Targets, want)
	}
}

// TestACriterionNamedTwiceIsHeldOnce: every criterion must match, so a repeated one adds
// nothing -- and held twice, it made the pairwise contradiction check a peer's to size.
func TestACriterionNamedTwiceIsHeldOnce(t *testing.T) {
	st := store.New()
	const imsi = "<ns1:supiimsi>208930100007488</ns1:supiimsi>"
	mustAck(t, testServer(st), withTarget(imsi+"</ns1:targetIdentifier><ns1:targetIdentifier>"+imsi))

	task, _ := st.Get(testXID)
	if len(task.Targets) != 1 || len(task.TargetElements) != 1 {
		t.Errorf("targets = %+v (elements %v), want the criterion once", task.Targets, task.TargetElements)
	}
}

// TestATaskIsBoundedInItsCriteria: the list is unbounded in the schema, so a task past the
// bound is refused with the operation's code rather than as malformed, and the bound is what
// keeps the contradiction check, which runs under the transition lock, from being a peer's to
// size.
func TestATaskIsBoundedInItsCriteria(t *testing.T) {
	list := func(n int) string {
		var b strings.Builder
		for i := range n {
			if i > 0 {
				b.WriteString("</ns1:targetIdentifier><ns1:targetIdentifier>")
			}
			fmt.Fprintf(&b, "<ns1:udpPort>%d</ns1:udpPort>", i)
		}

		return b.String()
	}

	// Ports contradict one another, so a list of them within the bound is refused for that;
	// what matters here is that the bound is decided first and names itself.
	st := store.New()
	m := serve(t, testServer(st), withTarget(list(maxTaskCriteria+1)))
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeActivateFailed ||
		!strings.Contains(m.ErrorInformation.ErrorDescription, "at most") {
		t.Fatalf("got %+v, want error %d naming the bound", m.ErrorInformation, errCodeActivateFailed)
	}

	m = serve(t, testServer(store.New()), withTarget(list(maxTaskCriteria)))
	if m.ErrorInformation != nil && strings.Contains(m.ErrorInformation.ErrorDescription, "at most") {
		t.Errorf("a task at the bound was refused for the bound: %+v", m.ErrorInformation)
	}
}

// TestADestinationReadOneWayIsRefused: the destination counterpart. A second address, port or
// deliveryAddress arm was dropped or overwritten by the decode, and the destination was
// created from whichever survived.
func TestADestinationReadOneWayIsRefused(t *testing.T) {
	base := createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), "")
	const ns = `xmlns:c="http://uri.etsi.org/03280/common/2017/07"`

	for _, c := range []struct{ name, body string }{
		{"two dIds", strings.Replace(base, "<ns1:dId>"+didAgencyA+"</ns1:dId>",
			"<ns1:dId>"+didAgencyA+"</ns1:dId><ns1:dId>"+didAgencyB+"</ns1:dId>", 1)},
		{"a second deliveryAddress arm", strings.Replace(base, "</ns1:ipAddressAndPort>",
			"</ns1:ipAddressAndPort><ns1:uri>https://mdf.example</ns1:uri>", 1)},
		{"two addresses", strings.Replace(base, "<c:IPv4Address>10.0.60.122</c:IPv4Address>",
			"<c:IPv4Address>10.0.60.122</c:IPv4Address><c:IPv4Address>10.0.60.123</c:IPv4Address>", 1)},
		{"an empty address arm beside a populated one", strings.Replace(base, "<c:IPv4Address>10.0.60.122</c:IPv4Address>",
			"<c:IPv6Address/><c:IPv4Address>10.0.60.122</c:IPv4Address>", 1)},
		{"two ports", strings.Replace(base, tcpPort("42069"), tcpPort("42069")+tcpPort("42070"), 1)},
		{"two address elements", strings.Replace(base, `<c:port `+ns+`>`,
			`<c:address `+ns+`><c:IPv4Address>10.0.60.123</c:IPv4Address></c:address><c:port `+ns+`>`, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := NewServer(store.New(), "neID")
			m := serve(t, srv, c.body)
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
			}
			if held := srv.heldDestinations(); len(held) != 0 {
				t.Errorf("a destination read one way was created: %+v", held)
			}
		})
	}
}

// TestADestinationAddressIsATokenAcrossLines: both address arms are patterns over xs:token,
// which collapses surrounding whitespace, so an address sent across lines is the address.
func TestADestinationAddressIsATokenAcrossLines(t *testing.T) {
	srv := NewServer(store.New(), "neID")
	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "\n  10.0.60.122\n  ", tcpPort("42069"), ""))
	if d, _ := srv.destinationByDID(didAgencyA); d.Address != endpointA {
		t.Errorf("address = %q, want %q", d.Address, endpointA)
	}
}

// TestEveryOrderedProductNeedsANamedDestination: TS 103 221-1 clause 6.2.1.2 -- "If a Task
// has an invalid combination of DeliveryType and Destinations (e.g. "X2andX3" delivery
// specified, but only an X2 Destination given) … the NE shall reject the ActivateTaskRequest
// with an appropriate error", and 3040 is that error. Acknowledged, the product with no named
// destination went to this element's configured endpoint for it, which on an element serving
// several agencies is one the task did not choose.
func TestEveryOrderedProductNeedsANamedDestination(t *testing.T) {
	for _, c := range []struct {
		name, deliveryType string
		destinations       map[string]string // DID → its deliveryType
		refused            bool
	}{
		{"X2Only naming only an X3 destination", deliveryX2Only, map[string]string{didAgencyA: deliveryX3Only}, true},
		{"X3Only naming only an X2 destination", deliveryX3Only, map[string]string{didAgencyA: deliveryX2Only}, true},
		{"X2andX3 naming only an X2 destination", deliveryX2andX3, map[string]string{didAgencyA: deliveryX2Only}, true},
		{"X2andX3 naming only an X3 destination", deliveryX2andX3, map[string]string{didAgencyA: deliveryX3Only}, true},
		{
			"X2andX3 naming one of each", deliveryX2andX3,
			map[string]string{didAgencyA: deliveryX2Only, didAgencyB: deliveryX3Only},
			false,
		},
		{"X2andX3 naming one serving both", deliveryX2andX3, map[string]string{didBoth: deliveryX2andX3}, false},
		{"X3Only naming one serving both", deliveryX3Only, map[string]string{didBoth: deliveryX2andX3}, false},
	} {
		for _, op := range []string{"ActivateTaskRequest", "ModifyTaskRequest"} {
			t.Run(c.name+"/"+op, func(t *testing.T) {
				st := store.New()
				srv := NewServer(st, "neID")
				var dids []string
				for did, dt := range c.destinations {
					mustAck(t, srv, createDestinationXML(did, dt, "10.0.60.122", tcpPort("42069"), ""))
					dids = append(dids, did)
				}
				if op == "ModifyTaskRequest" {
					// A task to modify, valid on its own terms.
					mustAck(t, srv, activateXMLWith(string(testXID), c.destinations[dids[0]], dIDs(dids[0]), ""))
				}

				body := strings.Replace(activateXMLWith(string(testXID), c.deliveryType, dIDs(dids...), ""),
					"ActivateTaskRequest", op, 1)
				m := serve(t, srv, body)
				switch {
				case c.refused && (m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeBadDeliveryCombination):
					t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeBadDeliveryCombination)
				case !c.refused && m.ErrorInformation != nil:
					t.Fatalf("refused %+v, want an acknowledgement", m.ErrorInformation)
				}
				task, held := st.Get(testXID)
				switch {
				case c.refused && op == "ActivateTaskRequest" && held:
					t.Error("the refused task was stored")
				case c.refused && op == "ModifyTaskRequest" && deliveryTypeOf(task.Products) == c.deliveryType &&
					c.deliveryType != c.destinations[dids[0]]:
					t.Error("the refused modification was applied")
				}
			})
		}
	}
}

// TestAContentTaskNamingNoDestinationAtAnElementRequiringOne: the same combination with no
// destination named at all, at an element with nowhere configured to send content.
func TestAContentTaskNamingNoDestinationAtAnElementRequiringOne(t *testing.T) {
	st := store.New()
	m := serve(t, NewServer(st, "neID", RequireResolvableDIDs()),
		activateXMLWith(string(testXID), deliveryX3Only, "", ""))
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeBadDeliveryCombination {
		t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeBadDeliveryCombination)
	}

	// Signalling alone is served by the configured endpoint, with or without the option.
	mustAck(t, NewServer(store.New(), "neID", RequireResolvableDIDs()),
		activateXMLWith(string(testXID), deliveryX2Only, "", ""))
}

// TestATaskAnswerCarriesWhatTheTaskRunsUnder: the label its product carries, the value its
// PDUs are stamped with, and which records it produces are what an auditing ADMF needs to read
// back; an answer silent on all three told it the task held none of them.
func TestATaskAnswerCarriesWhatTheTaskRunsUnder(t *testing.T) {
	requireXmllint(t)
	checkVendoredSchemas(t)

	st := store.New()
	srv := testServer(st, HonoursCorrelationID())
	mustAck(t, srv, withTaskDetails("</ns1:taskDetails>",
		"<ns1:correlationID>+2752413510594253201</ns1:correlationID>"+
			"<ns1:productID>cccccccc-cccc-4ccc-8ccc-cccccccccccc</ns1:productID>"+
			`<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>`+identifierAssociation("All")+
			`</ns1:taskDetailsExtensions></ns1:taskDetails>`))

	for _, req := range [][]byte{
		request("GetTaskDetailsRequest", "\n    "+fixtureXID),
		request("GetAllTaskDetailsRequest", ""),
		request("GetAllDetailsRequest", ""),
	} {
		resp, err := srv.Process(req, admfPeer(t))
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		out, err := marshalResponse(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, want := range []string{
			"<ns1:correlationID>2752413510594253201</ns1:correlationID>",
			"<ns1:productID>cccccccc-cccc-4ccc-8ccc-cccccccccccc</ns1:productID>",
			"<ext:IdentifierAssociationEventsGenerated>All</ext:IdentifierAssociationEventsGenerated>",
		} {
			if !strings.Contains(string(out), want) {
				t.Errorf("%s does not carry %s", resp.Messages[0].Type, want)
			}
		}
		if problems := validateAgainstSchema(t, out); len(problems) > 0 {
			t.Errorf("%s does not validate: %v", resp.Messages[0].Type, problems)
		}

		// And it reads back as what was provisioned: a requester parsing it sees the same
		// three fields.
		var back X1Response
		if err := xml.Unmarshal(out, &back); err != nil {
			t.Fatalf("the answer does not parse: %v", err)
		}
		tasks := back.Messages[0].ReportedTasks()
		if len(tasks) != 1 {
			t.Fatalf("%s reads back as %d tasks", resp.Messages[0].Type, len(tasks))
		}
		td := tasks[0].TaskDetails
		if td.CorrelationID != "2752413510594253201" || td.ProductID != "cccccccc-cccc-4ccc-8ccc-cccccccccccc" ||
			recordScope(td) != types.RecordScopeAll {
			t.Errorf("%s reads back as correlation %q, product %q, scope %q",
				resp.Messages[0].Type, td.CorrelationID, td.ProductID, recordScope(td))
		}
	}
}

// TestDestinationPortsRenderInTheirType: the destination answers render the port as the
// TCPPort it is dialled as, from the number rather than the text, at the bounds of that type.
func TestDestinationPortsRenderInTheirType(t *testing.T) {
	requireXmllint(t)
	checkVendoredSchemas(t)

	srv := NewServer(store.New(), "neID", WithConfiguredDestinations(
		ConfiguredDestination{DID: didBoth, DeliveryType: deliveryX2Only, Address: "10.0.60.122:0080"},
	))
	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("2"), ""))
	mustAck(t, srv, createDestinationXML(didAgencyB, deliveryX2Only, "10.0.60.122", `<c:UDPPort>65535</c:UDPPort>`, ""))

	resp, err := srv.Process(request("GetAllDestinationDetailsRequest", ""), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	out, err := marshalResponse(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{"<c:TCPPort>2</c:TCPPort>", "<c:TCPPort>65535</c:TCPPort>", "<c:TCPPort>80</c:TCPPort>"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the answer does not carry %s:\n%s", want, out)
		}
	}
	if problems := validateAgainstSchema(t, out); len(problems) > 0 {
		t.Errorf("the destination answer does not validate: %v", problems)
	}
}

// TestAConfiguredPortThisElementCannotDialIsDropped: 0 and 1 are outside the TCPPort type the
// port is dialled and reported as, so an entry naming either resolves to nothing deliverable.
func TestAConfiguredPortThisElementCannotDialIsDropped(t *testing.T) {
	for _, port := range []string{"0", "1"} {
		d := ConfiguredDestination{DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.122:" + port}
		if d.Valid() == nil {
			t.Errorf("port %s accepted", port)
		}
	}
	if err := (ConfiguredDestination{DID: didAgencyA, DeliveryType: deliveryX2Only, Address: "10.0.60.122:2"}).Valid(); err != nil {
		t.Errorf("port 2 refused: %v", err)
	}
}

// TestAnAddressArmOfWhitespaceCarriesNothing: both arms are xs:token patterns, so surrounding
// whitespace is not part of the value and an arm of nothing else is no arm. Checked on the type
// itself, which a caller may use without the request path's collapse.
func TestAnAddressArmOfWhitespaceCarriesNothing(t *testing.T) {
	if n := (IPAddress{IPv4: "10.0.0.1", IPv6: " \n "}).Arms(); n != 1 {
		t.Errorf("Arms() = %d, want 1: a whitespace-only arm carries no value", n)
	}
	a := IPAddress{IPv4: "\n 10.0.0.1 \t"}
	if err := a.Valid(); err != nil {
		t.Errorf("Valid() = %v, want the address the token spells", err)
	}
	if got := a.Canonical(); got != "10.0.0.1" {
		t.Errorf("Canonical() = %q, want 10.0.0.1", got)
	}
}
