// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// TestAnElementOutsideTheX1NamespaceIsNotRead: a struct tag without a namespace matches its
// local name in any namespace, so <x:xId xmlns:x="urn:evil"> was read as the X1 xId. The tags
// are pinned to the X1 namespace, and an element in another one is not the element they name.
func TestAnElementOutsideTheX1NamespaceIsNotRead(t *testing.T) {
	const foreign = `xmlns:x="urn:evil"`

	t.Run("a DeactivateTask naming its task in another namespace", func(t *testing.T) {
		st := store.New()
		st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{{Type: types.TargetGPSI, Value: "2125552368"}}})

		m := serve(t, testServer(st), string(request("DeactivateTaskRequest", `
    <x:xId `+foreign+`>`+string(testXID)+`</x:xId>`)))

		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
			t.Errorf("want a %d refusal, got %+v", errCodeSchemaError, m.ErrorInformation)
		}
		if _, ok := st.Get(testXID); !ok {
			t.Error("the task was deactivated")
		}
	})

	for name, body := range map[string]string{
		"its xId": strings.Replace(activateXML,
			"<ns1:xId>"+string(testXID)+"</ns1:xId>",
			"<x:xId "+foreign+">"+string(testXID)+"</x:xId>", 1),
		"its target": strings.Replace(activateXML,
			"<ns1:e164Number>2125552368</ns1:e164Number>",
			"<x:e164Number "+foreign+">2125552368</x:e164Number>", 1),
		"the wrapper of its destinations": strings.Replace(strings.Replace(activateXML,
			"<ns1:listOfDIDs>", "<x:listOfDIDs "+foreign+">", 1),
			"</ns1:listOfDIDs>", "</x:listOfDIDs>", 1),
		"a destination inside its X1 wrapper": strings.Replace(activateXML,
			"<ns1:dId>"+testDIDInActivateXML+"</ns1:dId>",
			"<x:dId "+foreign+">"+testDIDInActivateXML+"</x:dId>", 1),
	} {
		t.Run("an ActivateTask carrying "+name+" in another namespace", func(t *testing.T) {
			st := store.New()

			m := serve(t, testServer(st), body)

			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Errorf("want a %d refusal, got %+v", errCodeSchemaError, m.ErrorInformation)
			}
			if _, ok := st.Get(testXID); ok {
				t.Error("the task was activated")
			}
		})
	}

	// The container: dropped, a message in another namespace would go unanswered, beside an X1
	// one as much as alone.
	foreignMessage := strings.Replace(strings.Replace(activateXML,
		"<ns1:x1RequestMessage", "<x:x1RequestMessage "+foreign, 1),
		"</ns1:x1RequestMessage>", "</x:x1RequestMessage>", 1)
	beside := string(request("DeactivateTaskRequest", `
    <ns1:xId>`+string(testXID)+`</ns1:xId>`))
	beside = strings.Replace(beside, "</ns1:X1Request>", foreignMessage[strings.Index(foreignMessage, "<x:x1RequestMessage"):strings.Index(foreignMessage, "</ns1:X1Request>")]+"</ns1:X1Request>", 1)

	signed := strings.Replace(activateXML, "</ns1:X1Request>",
		`<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#"/></ns1:X1Request>`, 1)

	for name, body := range map[string]string{
		"a foreign request message alone":            foreignMessage,
		"a foreign request message beside an X1 one": beside,
		"an XML signature beside its message":        signed,
	} {
		t.Run("a container carrying "+name, func(t *testing.T) {
			st := store.New()

			if _, err := testServer(st).Process([]byte(body), admfPeer(t)); err == nil {
				t.Error("processed")
			}
			if _, ok := st.Get(testXID); ok {
				t.Error("the task was activated")
			}
		})
	}
}

// TestANamespaceSpelledAsAPrefixIsNotReadAsThatPrefixes: a decoder reading tokens resolves each
// name against the declarations it has seen, so a name already resolved to a namespace spelled
// like a declared prefix -- xmlns:x="ns1" beside xmlns:ns1 for the X1 namespace -- was resolved
// again by the next decoder in the chain, and <x:xId> was read as the X1 xId although a
// namespace-aware reader puts it in "ns1".
func TestANamespaceSpelledAsAPrefixIsNotReadAsThatPrefixes(t *testing.T) {
	const decls = `xmlns:x="ns1" xmlns:p="urn:3GPP:ns:li:3GPPX1Extensions:r18:v6" xmlns:y="p" xmlns:xsi=`
	declare := func(body string) string { return strings.Replace(body, "xmlns:xsi=", decls, 1) }

	t.Run("a DeactivateTask's xId", func(t *testing.T) {
		st := store.New()
		st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{{Type: types.TargetGPSI, Value: "2125552368"}}})

		m := serve(t, testServer(st), declare(string(request("DeactivateTaskRequest", `
    <x:xId>`+string(testXID)+`</x:xId>`))))

		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
			t.Errorf("want a %d refusal, got %+v", errCodeSchemaError, m.ErrorInformation)
		}
		if _, ok := st.Get(testXID); !ok {
			t.Error("the task was deactivated")
		}
	})

	// The same respelling by other routes: a default namespace, a declaration on the element
	// itself, and an attribute's prefix.
	for name, body := range map[string]string{
		"a default namespace spelled as a prefix": string(request("DeactivateTaskRequest", `
    <xId xmlns="ns1">`+string(testXID)+`</xId>`)),
		"an alias declared on the element": string(request("DeactivateTaskRequest", `
    <x:xId xmlns:x="ns1">`+string(testXID)+`</x:xId>`)),
		"an alias for the xsi namespace": strings.Replace(strings.Replace(string(request("DeactivateTaskRequest", `
    <ns1:xId>`+string(testXID)+`</ns1:xId>`)),
			`xmlns:xsi=`, `xmlns:q="xsi" xmlns:xsi=`, 1), `xsi:type=`, `q:type=`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			st := store.New()
			st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{{Type: types.TargetGPSI, Value: "2125552368"}}})

			m := serve(t, testServer(st), body)

			if m.OK != "" {
				t.Errorf("acknowledged: %+v", m)
			}
			if _, ok := st.Get(testXID); !ok {
				t.Error("the task was deactivated")
			}
		})
	}

	t.Run("an ActivateTask's details", func(t *testing.T) {
		body := declare(activateXML)
		body = strings.ReplaceAll(body, "<ns1:taskDetails>", "<x:taskDetails>")
		body = strings.ReplaceAll(body, "</ns1:taskDetails>", "</x:taskDetails>")
		st := store.New()

		m := serve(t, testServer(st), body)

		if m.OK != "" {
			t.Errorf("acknowledged: %+v", m)
		}
		if _, ok := st.Get(testXID); ok {
			t.Error("the task was activated")
		}
	})

	t.Run("an extension's 3GPP element", func(t *testing.T) {
		// Inside an extension placeholder the namespace check stops, so what is under test is
		// that the pinned tag does not bind the element: it is unknown extension content.
		var ext MessageExtension
		body := `<taskDetailsExtensions xmlns="http://uri.etsi.org/03221/X1/2017/10" ` + decls + `"http://www.w3.org/2001/XMLSchema-instance">` +
			`<Owner>3GPP</Owner><y:IdentifierAssociationExtensions><y:IdentifierAssociationEventsGenerated>true</y:IdentifierAssociationEventsGenerated></y:IdentifierAssociationExtensions>` +
			`</taskDetailsExtensions>`

		if err := decodeDocument([]byte(body), &ext); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ext.IdentifierAssociation != nil {
			t.Error("an element in namespace p was read as the 3GPP IdentifierAssociationExtensions")
		}
	})

	t.Run("the root element", func(t *testing.T) {
		body := declare(activateXML)
		body = strings.Replace(body, "<ns1:X1Request", "<x:X1Request", 1)
		body = strings.Replace(body, "</ns1:X1Request>", "</x:X1Request>", 1)
		st := store.New()

		if _, err := testServer(st).Process([]byte(body), admfPeer(t)); err == nil {
			t.Error("processed: the root is not the X1 X1Request")
		}
		if _, ok := st.Get(testXID); ok {
			t.Error("the task was activated")
		}
	})
}

// TestContentAnotherSchemaDefinesIsNotChecked: the check stops below the X1 elements whose
// content is another namespace's, so an LI_T3 criterion in the 3GPP extension still activates.
func TestContentAnotherSchemaDefinesIsNotChecked(t *testing.T) {
	var sub subtree
	sub.x1Depth = 8
	for _, el := range []xml.StartElement{
		{Name: xml.Name{Space: Namespace, Local: "taskDetails"}},
		{Name: xml.Name{Space: Namespace, Local: "targetIdentifierExtension"}},
		{Name: xml.Name{Space: NamespaceX1Ext3GPP, Local: "UPFLIT3TargetIdentifierExtensions"}},
	} {
		sub.depth++
		sub.x1Namespaced(el)
	}

	if sub.foreign != "" {
		t.Errorf("extension content recorded as foreign: %s", sub.foreign)
	}
}

// TestElementsAnotherSchemaTypesAreNotCheckedBelow: a target arm typed by a TS 103 280 complex
// type, and a Generic Object of a subtype another schema defines, carry elements outside the X1
// namespace, and are answered for what they are rather than refused as foreign.
func TestElementsAnotherSchemaTypesAreNotCheckedBelow(t *testing.T) {
	t.Run("an ipv4Cidr target", func(t *testing.T) {
		body := strings.Replace(activateXML,
			"<ns1:e164Number>2125552368</ns1:e164Number>",
			`<ns1:ipv4Cidr><c:IPv4CIDR xmlns:c="http://uri.etsi.org/03280/common/2017/07">10.0.0.0/24</c:IPv4CIDR></ns1:ipv4Cidr>`, 1)

		m := serve(t, testServer(store.New()), body)

		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeUnsupportedTarget {
			t.Errorf("want a %d refusal, got %+v", errCodeUnsupportedTarget, m.ErrorInformation)
		}
	})

	t.Run("a Generic Object of a subtype", func(t *testing.T) {
		body := string(request("CreateObjectRequest", `
    <ns1:createObject xmlns:ds="http://uri.etsi.org/03221/X1/2017/10/DestinationSet" xsi:type="ds:DestinationSetDetails">
      <ns1:objectId>bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb</ns1:objectId>
      <ds:DestinationSetType>x</ds:DestinationSetType>
    </ns1:createObject>`))

		m := serve(t, testServer(store.New()), body)

		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeUnsupportedRequest {
			t.Errorf("want a %d refusal, got %+v", errCodeUnsupportedRequest, m.ErrorInformation)
		}
	})
}

// TestOtherSchemaContentFollowsTheSchema derives from the vendored X1 schema the elements whose
// content another schema defines -- those typed by an Extension, by the abstract GenericObject,
// or by a complex type of TS 103 280 or the hashed-identifier schema -- and holds
// otherSchemaContent to exactly that set.
func TestOtherSchemaContentFollowsTheSchema(t *testing.T) {
	complexTypes := func(file string) map[string]bool {
		names := make(map[string]bool)
		walkSchema(t, file, func(el xml.StartElement, _ string) {
			if el.Name.Local == "complexType" {
				names[attr(el, "name")] = true
			}
		})

		return names
	}
	common := complexTypes("TS_103_280.xsd")
	hashed := complexTypes("TS_103_221_01_HashedID.xsd")

	extensions := make(map[string]bool)
	walkSchema(t, "TS_103_221_01.xsd", func(el xml.StartElement, complexType string) {
		if el.Name.Local == "extension" && attr(el, "base") == "Extension" {
			extensions[complexType] = true
		}
	})

	want := make(map[string]bool)
	walkSchema(t, "TS_103_221_01.xsd", func(el xml.StartElement, _ string) {
		if el.Name.Local != "element" {
			return
		}
		ty := attr(el, "type")
		prefix, local, qualified := strings.Cut(ty, ":")
		switch {
		case !qualified && (extensions[ty] || ty == "GenericObject"),
			qualified && prefix == "etsi103280" && common[local],
			qualified && prefix == "hashedID" && hashed[local]:
			want[attr(el, "name")] = true
		}
	})

	for name := range want {
		if !otherSchemaContent[name] {
			t.Errorf("%s is missing from otherSchemaContent", name)
		}
	}
	for name := range otherSchemaContent {
		if !want[name] {
			t.Errorf("otherSchemaContent lists %s, whose content the X1 schema defines", name)
		}
	}
}

// walkSchema calls f with every element of a vendored schema and the name of the complexType
// enclosing it, if any.
func walkSchema(t *testing.T, file string, f func(el xml.StartElement, complexType string)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "schemas", file))
	if err != nil {
		t.Fatal(err)
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	var enclosing []string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			current := ""
			if len(enclosing) > 0 {
				current = enclosing[len(enclosing)-1]
			}
			if el.Name.Local == "complexType" && attr(el, "name") != "" {
				current = attr(el, "name")
			}
			f(el, current)
			enclosing = append(enclosing, current)
		case xml.EndElement:
			enclosing = enclosing[:len(enclosing)-1]
		}
	}
}

// attr returns an unqualified attribute's value.
func attr(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}

	return ""
}
