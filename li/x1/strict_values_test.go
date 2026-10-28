// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
)

// The cases here sit on the line between a message outside its schema, 1010, and a conformant
// message this element cannot act on, which is the operation's own refusal. Each one was on the
// wrong side of it.

// TestAnEmptySUPINAIIsTheOperationsRefusal: an NAI is an unrestricted xs:string, so a supinai
// of nothing, or of nothing but whitespace, conforms. It still names no subscriber, so the task
// is refused -- with 3000 or 3001, because 1010 would tell the ADMF its message was malformed.
func TestAnEmptySUPINAIIsTheOperationsRefusal(t *testing.T) {
	for _, c := range []struct{ name, arm string }{
		{"empty", "<ns1:supinai/>"},
		{"whitespace", "<ns1:supinai> \n </ns1:supinai>"},
	} {
		t.Run(c.name+" on activation", func(t *testing.T) {
			st := store.New()
			m := serve(t, testServer(st), withTarget(c.arm))
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeActivateFailed {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeActivateFailed)
			}
			if st.Len() != 0 {
				t.Error("a task naming no subscriber was stored")
			}
		})
		t.Run(c.name+" on modification", func(t *testing.T) {
			st := store.New()
			srv := testServer(st)
			mustAck(t, srv, activateXML)
			m := serve(t, srv, strings.Replace(withTarget(c.arm), "ActivateTaskRequest", "ModifyTaskRequest", 1))
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeModifyFailed {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeModifyFailed)
			}
		})
	}
}

// TestACorrelationIDTakesTheSchemasLexicalForms: xs:nonNegativeInteger admits surrounding
// whitespace, a leading "+", leading zeros, and a negative zero. Each is the number it spells.
func TestACorrelationIDTakesTheSchemasLexicalForms(t *testing.T) {
	for _, c := range []struct {
		lexical string
		want    uint64
	}{
		{"-0", 0}, {"-000", 0}, {"+0", 0}, {"007", 7}, {" 5 ", 5}, {"+12", 12},
	} {
		t.Run(c.lexical, func(t *testing.T) {
			st := store.New()
			mustAck(t, testServer(st, HonoursCorrelationID()), withTaskDetails("</ns1:taskDetails>",
				"<ns1:correlationID>"+c.lexical+"</ns1:correlationID></ns1:taskDetails>"))
			if task, _ := st.Get(testXID); task.CorrelationID != c.want {
				t.Errorf("correlation = %d, want %d", task.CorrelationID, c.want)
			}
		})
	}

	// A negative value other than zero is outside the type.
	st := store.New()
	m := serve(t, testServer(st, HonoursCorrelationID()), withTaskDetails("</ns1:taskDetails>",
		"<ns1:correlationID>-1</ns1:correlationID></ns1:taskDetails>"))
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
		t.Errorf("-1: got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
	}
}

// TestABooleanTakesTheSchemasLexicalForms: xs:boolean is "true", "false", "1" or "0", whitespace
// collapsed. strconv.ParseBool also takes "TRUE" and "t", which the schema refuses, and its
// failure failed the decode of the whole container.
func TestABooleanTakesTheSchemasLexicalForms(t *testing.T) {
	implicit := func(v string) string {
		return withTaskDetails("</ns1:taskDetails>",
			"<ns1:implicitDeactivationAllowed>"+v+"</ns1:implicitDeactivationAllowed></ns1:taskDetails>")
	}
	for _, v := range []string{"true", "false", "1", "0", " true\n"} {
		t.Run(v, func(t *testing.T) {
			mustAck(t, testServer(store.New()), implicit(v))
		})
	}

	for _, v := range []string{"TRUE", "t", "maybe", ""} {
		t.Run("refused "+v, func(t *testing.T) {
			st := store.New()
			srv := testServer(st)
			// The malformed message first, then a conformant one, in one container.
			second := createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), "")
			body := strings.Replace(implicit(v), "</ns1:X1Request>",
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
			if st.Len() != 0 {
				t.Error("the malformed task was stored")
			}
		})
	}
}

// TestAMandatoryPartMissingIsMalformed: each of these decoded to the zero value of a field and
// was refused, if at all, as a criterion or destination this element could not use. The decode
// now sees what was absent, so the answer names the schema violation.
func TestAMandatoryPartMissingIsMalformed(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"an F-SEID with no SEID", withTarget(ext(`<ns2:FSEID/>`))},
		{"an F-TEID with no TEID", withTarget(ext(`<ns2:FTEID><ns2:IPv4Address>10.0.0.1</ns2:IPv4Address></ns2:FTEID>`))},
		{"a targetIdentifier with no arm", withTarget(``)},
		{"an LI_T3 criterion with no arm", withTarget(ext(``))},
		{"an empty F-SEID address", withTarget(ext(`<ns2:FSEID><ns2:SEID>42</ns2:SEID><ns2:IPv4Address/></ns2:FSEID>`))},
		{"a blank F-SEID address", withTarget(ext(`<ns2:FSEID><ns2:SEID>42</ns2:SEID><ns2:IPv6Address> </ns2:IPv6Address></ns2:FSEID>`))},
		{"an empty F-TEID address", withTarget(ext(`<ns2:FTEID><ns2:TEID>5</ns2:TEID><ns2:IPv4Address/></ns2:FTEID>`))},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			m := serve(t, testServer(st), c.body)
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
			}
			if st.Len() != 0 {
				t.Error("a malformed task was stored")
			}
		})
	}

	base := createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", tcpPort("42069"), "")
	addr := base[strings.Index(base, "<ns1:deliveryAddress>") : strings.Index(base, "</ns1:deliveryAddress>")+len("</ns1:deliveryAddress>")]
	for _, c := range []struct{ name, body string }{
		{"an empty deliveryAddress", strings.Replace(base, addr, "<ns1:deliveryAddress/>", 1)},
		{"no deliveryAddress", strings.Replace(base, addr, "", 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := NewServer(store.New(), "neID")
			m := serve(t, srv, c.body)
			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, errCodeSchemaError)
			}
			if held := srv.heldDestinations(); len(held) != 0 {
				t.Errorf("a malformed destination was created: %+v", held)
			}
		})
	}
}

// TestAnAttributeSentTwiceIsNotXML: encoding/xml keeps the last of two attributes with one name,
// so a request message carrying two xsi:type attributes was answered as whichever came second --
// an activation stored, or answered as a ping, by the order of two attributes. XML's "Unique Att
// Spec" makes such a document not well-formed, so the container is unparseable.
func TestAnAttributeSentTwiceIsNotXML(t *testing.T) {
	const activate = `xsi:type="ns1:ActivateTaskRequest"`
	if !strings.Contains(activateXML, activate) {
		t.Fatal("fixture does not carry the expected xsi:type")
	}
	for _, c := range []struct{ name, attrs string }{
		{"activate then ping", activate + ` xsi:type="ns1:PingRequest"`},
		{"ping then activate", `xsi:type="ns1:PingRequest" ` + activate},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			body := strings.Replace(activateXML, activate, c.attrs, 1)
			if _, err := testServer(st).Process([]byte(body), admfPeer(t)); err == nil {
				t.Error("a request message carrying two xsi:type attributes was processed")
			}
			if st.Len() != 0 {
				t.Error("a task was stored from a document that is not XML")
			}
		})
	}
}
