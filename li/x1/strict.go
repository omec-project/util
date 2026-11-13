// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

// encoding/xml is lenient in two ways that matter on this interface, and this file closes both
// for the structures a provisioning request carries.
//
// **It reads a repeated element as one.** A second occurrence of an element bound to a single
// field overwrites the first, and a second list wrapper is merged into the first list. So a
// targetIdentifier carrying `<imsi>` twice, a criterion carrying two `<FSEID>`s, or a task carrying
// two `<correlationID>`s decoded as though it carried one, and the element acted on whichever the
// decoder kept. The schema allows each of these once, so the message is malformed and no reading
// of it is authoritative: it is refused with 1010, as a choice populating two arms already is.
//
// **It parses a typed integer or boolean with strconv.** That refuses the leading "+" xs:integer's
// lexical space admits, accepts the "TRUE" xs:boolean's does not, and fails the decode of the
// *whole container* on one bad value -- so a conformant "+80" was answered with a top-level error,
// and so was a port past 65535, which is one message's schema error. Those fields are decoded as
// text here and parsed by the schema's rules, and a value outside its type is recorded and refused
// with 1010 against the message that carried it.
//
// Each decoded structure records what it saw in a wireShape. The refusals are decided where the
// message is validated, not here: an UnmarshalXML that returned an error would fail the whole
// container, which is the second leniency's defect in another form.

// decodeDocument decodes body into v, which must be the whole of body: one root element, with
// nothing around it but whitespace, comments and processing instructions.
//
// xml.Unmarshal stops at the end of the first element and skips anything before it, so a body
// carrying a second X1Request after the first, or text either side of it, was read as though it
// carried only the first. Such a body is not a well-formed XML document, so there is no
// request in it to answer.
//
// A UTF-8 byte order mark is not part of the document, and may precede it. A document type
// declaration is refused. X1 defines none, and its internal subset can declare default
// attributes -- a namespace declaration among them -- which a validating reader applies and
// encoding/xml does not, so the two would read the same body as different elements.
func decodeDocument(body []byte, v any) error {
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if start, ok := tok.(xml.StartElement); ok {
			if err := d.DecodeElement(v, &start); err != nil {
				return err
			}

			break
		}
		if !outsideRoot(tok) {
			return errors.New("content before the root element")
		}
	}
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !outsideRoot(tok) {
			return errors.New("content after the root element")
		}
	}
}

// outsideRoot reports whether a token is one decodeDocument accepts outside the root element:
// whitespace, a comment or a processing instruction (XML 1.0 productions 22 and 27, less the
// document type declaration).
func outsideRoot(tok xml.Token) bool {
	switch t := tok.(type) {
	case xml.CharData:
		return len(bytes.Trim(t, " \t\r\n")) == 0
	case xml.Comment, xml.ProcInst:
		return true
	}

	return false
}

// wireShape is what decoding saw of an element beyond what its fields hold. It is the zero value
// for a structure built in Go rather than decoded, so it constrains only what arrived on the wire.
type wireShape struct {
	// repeated names the first child element that occurred more than once where the schema
	// allows it once.
	repeated string
	// children counts the child elements, for a choice, where more than one is invalid whatever
	// they are -- an empty `<imsi/>` beside a populated arm included.
	children int
	// first names the first child element, so a choice carrying one empty arm can say which.
	first string
	// malformed names the first child whose text is outside its schema type.
	malformed string
	// missing names the first child the schema makes mandatory that did not occur.
	missing string
	// decoded is set for a structure that arrived on the wire, so a choice with no child can
	// be told from one built in Go, whose shape is the zero value.
	decoded bool
	// foreign names the first element of a request message that is outside the namespace the
	// schema puts it in; see x1Namespaced.
	foreign string
}

// err returns the 1010 refusal this shape calls for, or nil. where names the structure, so the
// refusal says which part of the message is wrong. No value is quoted: see refuse.
func (w wireShape) err(where string) error {
	switch {
	case w.foreign != "":
		return refuse(errCodeSchemaError, "%s carries %s outside the X1 namespace", where, w.foreign)
	case w.repeated != "":
		return refuse(errCodeSchemaError, "%s carries more than one %s; the schema allows one", where, w.repeated)
	case w.malformed != "":
		return refuse(errCodeSchemaError, "%s: %s does not conform to its schema type", where, w.malformed)
	case w.missing != "":
		return refuse(errCodeSchemaError, "%s lacks %s; the schema makes it mandatory", where, w.missing)
	}

	return nil
}

// noArm reports a decoded choice that carried no child at all -- not even one this element
// does not model, which the catch-all fields count.
func (w wireShape) noArm() bool { return w.decoded && w.children == 0 }

// choiceErr is err for a structure the schema defines as an xs:choice, where a second child of
// any name is a second arm.
func (w wireShape) choiceErr(where string) error {
	if w.children > 1 {
		return refuse(errCodeSchemaError, "%s populates %d arms of a choice; exactly one is valid", where, w.children)
	}

	return w.err(where)
}

// decodeChildren decodes the element start opens into v exactly as encoding/xml would, and
// returns how many times each direct child element occurred, by local name, and the names in
// the order they first appeared.
//
// The element's tokens are passed through to a second decoder as they are read, and counted on
// the way. The names they carry are already resolved to their namespaces, and are passed without
// the namespace declarations (resolved), so the second decoder leaves them as they are and the
// result is the decode the first decoder would have produced.
//
// Passed through rather than collected first: this runs before the request is authenticated,
// once per element whose decode calls it, nested as deep as those elements are. Collecting each
// subtree before decoding it held every token of the request in memory once per level, about
// 190 times the size of a body of empty elements, so a few concurrent requests of under 1 MiB
// could exhaust the element's memory.
//
// An element anywhere inside that carries one attribute twice fails the decode. encoding/xml
// keeps the last, so two xsi:type attributes on a request message made it whichever operation
// came second -- but XML's own "Unique Att Spec" makes such a document not well-formed, so it is
// not a message with a schema error but no message at all, and the container is unparseable.
func decodeChildren(d *xml.Decoder, start xml.StartElement, v any) (map[string]int, []string, error) {
	sub, err := decodeSubtree(d, start, v, 0)
	if err != nil {
		return nil, nil, err
	}

	return sub.counts, sub.order, nil
}

// decodeSubtree is decodeChildren, also checking the namespace of the elements down to x1Depth
// levels below start (see x1Namespaced). The first one outside it is the subtree's foreign.
func decodeSubtree(d *xml.Decoder, start xml.StartElement, v any, x1Depth int) (*subtree, error) {
	if err := uniqueAttributes(start); err != nil {
		return nil, err
	}
	sub := &subtree{d: d, start: start.Copy(), counts: make(map[string]int), x1Depth: x1Depth}
	if err := xml.NewTokenDecoder(sub).Decode(v); err != nil {
		return nil, err
	}
	// Decode returns at the element's end, but a decode that returned early must not leave the
	// rest of the element for the caller's decoder to read as its own.
	for sub.depth > 0 {
		if _, err := sub.Token(); err != nil {
			return nil, err
		}
	}

	return sub, nil
}

// x1Namespaced is the check decodeSubtree makes.
//
// The struct tags name the X1 namespace, so an element outside it is not read as the X1 element
// of the same local name: without the namespace, encoding/xml matches the local name in any
// namespace, and <x:xId xmlns:x="urn:evil"> was the task's xId. But an element no field matches
// is dropped silently, and a dropped element can change what a message asks for -- a
// listOfDIDs outside the namespace leaves the task naming no destination, which is valid and
// routes its products by this element's configuration. So the element is also recorded, and
// its message refused with 1010 as an element in a position the schema does not allow.
//
// The X1 schema is elementFormDefault="qualified", so every element of a request message is in
// the X1 namespace except the content of those whose type another schema defines: the
// extension placeholders, whose content is an xs:any of other namespaces, the elements typed by
// a complex type of ETSI TS 103 280 or the hashed-identifier schema, and the Generic Object
// elements, whose subtype another schema defines. Below those the check stops; their own
// decoders bind the namespaces they expect. TestOtherSchemaContentFollowsTheSchema derives the
// list from the vendored schema.
func (s *subtree) x1Namespaced(el xml.StartElement) {
	// depth counts start as 1, so el is depth-1 levels below it.
	if s.x1Depth == 0 || s.skipFrom != 0 || s.depth-1 > s.x1Depth {
		return
	}
	switch {
	case el.Name.Space != Namespace:
		if s.foreign == "" {
			s.foreign = el.Name.Local
		}
		s.skipFrom = s.depth
	case otherSchemaContent[el.Name.Local]:
		s.skipFrom = s.depth
	}
}

// otherSchemaContent names the X1 elements whose content another schema defines.
var otherSchemaContent = map[string]bool{
	"taskDetailsExtensions":        true,
	"targetIdentifierExtension":    true,
	"mediationDetailsExtensions":   true,
	"destinationDetailsExtensions": true,
	"taskStatusExtensions":         true,
	"taskIssueExtensions":          true,
	"nEIssueExtensions":            true,
	"ipAddressAndPort":             true,
	"tcpPortRange":                 true,
	"udpPortRange":                 true,
	"ipv4Cidr":                     true,
	"hashedIdentifier":             true,
	// Typed GenericObject, which is abstract: an instance is a subtype named by xsi:type, and
	// its elements are in that subtype's namespace (annex E's DestinationSetDetails, for one).
	// object is in a GetObject answer, which this element sends rather than reads; it is
	// listed so that the list is the schema's.
	"createObject": true,
	"modifyObject": true,
	"object":       true,
}

// subtree hands a second decoder the tokens of one element from the decoder reading it, ending
// at the element's end, and counts the element's direct children as they pass.
type subtree struct {
	d       *xml.Decoder
	start   xml.StartElement
	started bool
	depth   int
	counts  map[string]int
	order   []string

	// x1Depth is how many levels below start x1Namespaced checks; 0 checks none.
	x1Depth int
	// skipFrom is the depth of the element below which the check has stopped, or 0.
	skipFrom int
	// foreign is the first element the check found outside the X1 namespace.
	foreign string
}

func (s *subtree) Token() (xml.Token, error) {
	if !s.started {
		s.started = true
		s.depth = 1

		return resolved(s.start), nil
	}
	if s.depth == 0 {
		return nil, io.EOF
	}

	tok, err := s.d.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case xml.StartElement:
		if err := uniqueAttributes(t); err != nil {
			return nil, err
		}
		if s.depth == 1 {
			if s.counts[t.Name.Local] == 0 {
				s.order = append(s.order, t.Name.Local)
			}
			s.counts[t.Name.Local]++
		}
		s.depth++
		s.x1Namespaced(t)
	case xml.EndElement:
		if s.depth == s.skipFrom {
			s.skipFrom = 0
		}
		s.depth--
	}

	// Copied because the decoder reading it may reuse the bytes behind it on its next call.
	tok = xml.CopyToken(tok)
	if el, ok := tok.(xml.StartElement); ok {
		return resolved(el), nil
	}

	return tok, nil
}

// resolved returns a start element whose names this decoder has already resolved, without the
// namespace declarations it carried, for the next decoder in the chain.
//
// A decoder reading tokens resolves each name against the declarations it has seen, and a name
// already resolved is resolved again: its namespace is looked up as though it were a prefix. So
// a document declaring xmlns:x="ns1" beside xmlns:ns1 for the X1 namespace had <x:xId> resolved
// to "ns1" by the first decoder and to the X1 namespace by the next, which read it as the X1
// xId although a namespace-aware reader puts it in "ns1". Without the declarations the next
// decoder has nothing to resolve against, and leaves the names as they are.
func resolved(el xml.StartElement) xml.StartElement {
	attrs := el.Attr[:0:0]
	for _, a := range el.Attr {
		if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
			continue
		}
		attrs = append(attrs, a)
	}
	el.Attr = attrs

	return el
}

// uniqueAttributes refuses an element carrying the same attribute twice. A set rather than a
// pairwise comparison, since a start tag may carry as many attributes as the request has room
// for and this runs before the request is authenticated.
func uniqueAttributes(el xml.StartElement) error {
	if len(el.Attr) < 2 {
		return nil
	}
	seen := make(map[xml.Name]struct{}, len(el.Attr))
	for _, a := range el.Attr {
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("element %s carries attribute %s twice, which XML does not allow", el.Name.Local, a.Name.Local)
		}
		seen[a.Name] = struct{}{}
	}

	return nil
}

// shapeOf builds the shape of a decoded element whose children the schema allows once each,
// except those named unbounded.
func shapeOf(counts map[string]int, order []string, unbounded ...string) wireShape {
	return shapeWhere(counts, order, func(name string) bool { return slices.Contains(unbounded, name) })
}

// shapeWhere is shapeOf with the unbounded children given by a predicate. The predicate is asked
// once per distinct child name, so the cost is linear in the element however many children it
// carries -- which matters for the extension placeholders, whose xs:any content a sender chooses
// freely and which are decoded before the request is authenticated.
func shapeWhere(counts map[string]int, order []string, unbounded func(string) bool) wireShape {
	w := wireShape{decoded: true}
	for _, n := range counts {
		w.children += n
	}
	if len(order) > 0 {
		w.first = order[0]
	}
	for _, name := range order {
		if counts[name] > 1 && !unbounded(name) {
			w.repeated = name

			break
		}
	}

	return w
}

// exceptOwner is the unbounded-children predicate of the extension placeholders, whose content
// is an xs:any of unbounded occurrence beside a single Owner.
func exceptOwner(name string) bool { return name != "Owner" }

// schemaUnsigned parses an unsigned xs:integer-derived lexical value -- xs:unsignedShort,
// unsignedInt, unsignedLong, or a port -- into bits bits. The lexical space admits surrounding
// whitespace, which the schema collapses, a leading "+", leading zeros, and "-0".
func schemaUnsigned(s string, bits int) (uint64, bool) {
	tok := xsToken(s)
	if rest, ok := strings.CutPrefix(tok, "-"); ok {
		// Negative zero is zero; anything else negative is outside every unsigned type.
		if rest == "" || strings.Trim(rest, "0") != "" {
			return 0, false
		}

		return 0, true
	}
	tok = strings.TrimPrefix(tok, "+")
	if tok == "" || tok[0] < '0' || tok[0] > '9' {
		return 0, false
	}
	n, err := strconv.ParseUint(tok, 10, bits)

	return n, err == nil
}

// schemaBoolean parses an xs:boolean lexical value: "true", "false", "1" or "0", whitespace
// collapsed, and nothing else.
func schemaBoolean(s string) (bool, bool) {
	switch xsToken(s) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}

	return false, false
}

// addressField returns an optional address child as sent. One that was sent with nothing in it
// is recorded as malformed: both address types are patterns that exclude the empty string, and
// decoded into a string it would be indistinguishable from an address not sent at all.
func addressField(raw *string, name string, shape *wireShape) string {
	if raw == nil {
		return ""
	}
	if xsToken(*raw) == "" && shape.malformed == "" {
		shape.malformed = name
	}

	return *raw
}

// unsignedField parses one integer child that was present on the wire. A value outside the
// type is recorded in shape under name, and the field is left at zero.
func unsignedField(raw *string, bits int, name string, shape *wireShape) uint64 {
	if raw == nil {
		return 0
	}
	n, ok := schemaUnsigned(*raw, bits)
	if !ok && shape.malformed == "" {
		shape.malformed = name
	}

	return n
}

// UnmarshalXML decodes a request message, recording a repeated child.
func (m *X1RequestMessage) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields X1RequestMessage
	var f fields
	sub, err := decodeSubtree(d, start, &f, math.MaxInt)
	if err != nil {
		return err
	}
	*m = X1RequestMessage(f)
	// Every child of every request this element receives is single; the two placeholders
	// named are on the issue reports an element sends, never on what it is sent.
	m.shape = shapeOf(sub.counts, sub.order, "taskIssueExtensions", "nEIssueExtensions")
	m.shape.foreign = sub.foreign

	return nil
}

// UnmarshalXML decodes a request container, refusing one that carries an element other than an
// X1 request message. The pinned tag does not read such an element as a message, and dropped
// it would go unanswered; there is no message to attribute it to, so the container is
// unparseable.
func (r *X1Request) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields X1Request
	var f fields
	sub, err := decodeSubtree(d, start, &f, 1)
	if err != nil {
		return err
	}
	if sub.foreign != "" {
		return fmt.Errorf("the request carries %s outside the X1 namespace", sub.foreign)
	}
	*r = X1Request(f)

	return nil
}

// UnmarshalXML decodes a task's details, recording a repeated child and which of the optional
// scalars were present, however empty.
func (td *TaskDetails) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields TaskDetails
	// The boolean is decoded as text for the reason the integers are: strconv.ParseBool both
	// refuses nothing the schema admits and admits what it refuses ("TRUE", "t"), and its
	// failure fails the whole container. The shallower field hides the embedded one.
	var f struct {
		fields
		ImplicitDeactivationAllowed *string `xml:"http://uri.etsi.org/03221/X1/2017/10 implicitDeactivationAllowed"`
	}
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*td = TaskDetails(f.fields)
	td.shape = shapeOf(counts, order, "taskDetailsExtensions")
	if raw := f.ImplicitDeactivationAllowed; raw != nil {
		v, ok := schemaBoolean(*raw)
		if !ok && td.shape.malformed == "" {
			td.shape.malformed = "implicitDeactivationAllowed"
		}
		td.ImplicitDeactivationAllowed = &v
	}
	td.sentCorrelationID = counts["correlationID"] > 0
	td.sentProductID = counts["productID"] > 0

	return nil
}

// UnmarshalXML decodes one targetIdentifier, recording every child element it carried.
func (t *TargetIdentifier) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields TargetIdentifier
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*t = TargetIdentifier(f)
	t.shape = shapeOf(counts, order)

	return nil
}

// UnmarshalXML decodes a target identifier extension, recording a repeated Owner and how many
// LI_T3 containers it carried.
func (e *TargetIdentifierExtension) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields TargetIdentifierExtension
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*e = TargetIdentifierExtension(f)
	// The content is an xs:any of unbounded occurrence, so only the Owner is single.
	e.shape = shapeWhere(counts, order, exceptOwner)
	e.lit3Containers = counts["UPFLIT3TargetIdentifierExtensions"]

	return nil
}

// UnmarshalXML decodes one LI_T3 detection criterion, parsing its integer arms by the schema's
// rules and recording every child element it carried.
func (id *UPFLIT3Identifier) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var wire struct {
		FSEID              *FSEID              `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 FSEID"`
		PDRID              *string             `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 PDRID"`
		QERID              *string             `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 QERID"`
		NetworkInstance    string              `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 NetworkInstance"`
		GTPTunnelDirection string              `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 GTPTunnelDirection"`
		FTEID              *FTEID              `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 FTEID"`
		PDR                string              `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 PDR"`
		Unmodelled         []UnmodelledElement `xml:",any"`
	}
	counts, order, err := decodeChildren(d, start, &wire)
	if err != nil {
		return err
	}
	*id = UPFLIT3Identifier{
		FSEID:              wire.FSEID,
		NetworkInstance:    wire.NetworkInstance,
		GTPTunnelDirection: wire.GTPTunnelDirection,
		FTEID:              wire.FTEID,
		PDR:                wire.PDR,
		Unmodelled:         wire.Unmodelled,
		shape:              shapeOf(counts, order),
	}
	if wire.PDRID != nil {
		v := uint32(unsignedField(wire.PDRID, 32, "PDRID", &id.shape))
		id.PDRID = &v
	}
	if wire.QERID != nil {
		v := uint32(unsignedField(wire.QERID, 32, "QERID", &id.shape))
		id.QERID = &v
	}

	return nil
}

// UnmarshalXML decodes an F-SEID, parsing the SEID by the schema's rules.
func (f *FSEID) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var wire struct {
		SEID        *string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 SEID"`
		IPv4Address *string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv4Address"`
		IPv6Address *string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv6Address"`
	}
	counts, order, err := decodeChildren(d, start, &wire)
	if err != nil {
		return err
	}
	*f = FSEID{shape: shapeOf(counts, order)}
	f.IPv4Address = addressField(wire.IPv4Address, "IPv4Address", &f.shape)
	f.IPv6Address = addressField(wire.IPv6Address, "IPv6Address", &f.shape)
	if wire.SEID == nil {
		f.shape.missing = "SEID"
	}
	f.SEID = unsignedField(wire.SEID, 64, "SEID", &f.shape)

	return nil
}

// UnmarshalXML decodes an F-TEID, parsing the TEID by the schema's rules.
func (f *FTEID) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var wire struct {
		TEID        *string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 TEID"`
		IPv4Address *string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv4Address"`
		IPv6Address *string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv6Address"`
	}
	counts, order, err := decodeChildren(d, start, &wire)
	if err != nil {
		return err
	}
	*f = FTEID{shape: shapeOf(counts, order)}
	f.IPv4Address = addressField(wire.IPv4Address, "IPv4Address", &f.shape)
	f.IPv6Address = addressField(wire.IPv6Address, "IPv6Address", &f.shape)
	if wire.TEID == nil {
		f.shape.missing = "TEID"
	}
	f.TEID = uint32(unsignedField(wire.TEID, 32, "TEID", &f.shape))

	return nil
}

// UnmarshalXML decodes a task or destination extension placeholder, recording a repeated Owner
// and how many identifier-association elements it carried.
func (e *MessageExtension) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields MessageExtension
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*e = MessageExtension(f)
	e.shape = shapeWhere(counts, order, exceptOwner)
	e.associations = counts["IdentifierAssociationExtensions"]

	return nil
}

// UnmarshalXML decodes the identifier-association extension, recording a repeated value.
func (e *IdentifierAssociationExtension) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields IdentifierAssociationExtension
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*e = IdentifierAssociationExtension(f)
	e.shape = shapeOf(counts, order)

	return nil
}

// UnmarshalXML decodes a destination's details, recording a repeated child.
func (dd *DestinationDetails) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields DestinationDetails
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*dd = DestinationDetails(f)
	dd.shape = shapeOf(counts, order, "destinationDetailsExtensions")
	if counts["deliveryAddress"] == 0 {
		dd.shape.missing = "deliveryAddress"
	}

	return nil
}

// UnmarshalXML decodes a delivery address, recording every arm it carried: only one is modelled,
// so a second would otherwise be dropped unseen.
func (a *DeliveryAddress) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields DeliveryAddress
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*a = DeliveryAddress(f)
	a.shape = shapeOf(counts, order)

	return nil
}

// UnmarshalXML decodes an address and port, recording a repeated child.
func (ap *IPAddressPort) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields IPAddressPort
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*ap = IPAddressPort(f)
	ap.shape = shapeOf(counts, order)

	return nil
}

// UnmarshalXML decodes the IPAddress choice, recording every arm it carried.
func (a *IPAddress) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields IPAddress
	var f fields
	counts, order, err := decodeChildren(d, start, &f)
	if err != nil {
		return err
	}
	*a = IPAddress(f)
	a.shape = shapeOf(counts, order)

	return nil
}

// UnmarshalXML decodes the Port choice, parsing the number by the schema's rules and recording
// every arm it carried.
func (p *Port) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var wire struct {
		TCPPort *string `xml:"http://uri.etsi.org/03280/common/2017/07 TCPPort"`
		UDPPort *string `xml:"http://uri.etsi.org/03280/common/2017/07 UDPPort"`
	}
	counts, order, err := decodeChildren(d, start, &wire)
	if err != nil {
		return err
	}
	*p = Port{shape: shapeOf(counts, order)}
	if wire.TCPPort != nil {
		v := uint16(unsignedField(wire.TCPPort, 16, "TCPPort", &p.shape))
		p.TCPPort = &v
	}
	if wire.UDPPort != nil {
		v := uint16(unsignedField(wire.UDPPort, 16, "UDPPort", &p.shape))
		p.UDPPort = &v
	}

	return nil
}
