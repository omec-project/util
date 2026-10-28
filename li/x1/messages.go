// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

// Package x1 implements the network-element (POI) side of the ETSI TS 103 221-1
// X1 interface: it receives task-provisioning requests from an ADMF/LIPF over
// HTTP (XML bodies) and applies them to the interception-task store. The NF is
// the X1 server; the ADMF is the client.
//
// It also holds the element's X1 client side -- the Requester a triggering function tasks
// a content POI with, and the Reporter that raises issues to the ADMF -- and the
// clause 8 peer binding both directions share. Transport security comes from the
// caller's TLS configuration (see NewListener and package mtls); credential
// pre-provisioning (X0) is outside this package.
package x1

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/omec-project/util/li/types"
)

// Namespace is the ETSI TS 103 221-1 X1 XML namespace. The message-type
// discriminator rides on the xsi:type attribute (XML-Schema-instance namespace).
const Namespace = "http://uri.etsi.org/03221/X1/2017/10"

// X1Request is the top-level X1 request envelope. It may carry one or more
// messages (TS 103 221-1 allows a batch); the common case is a single message.
type X1Request struct {
	XMLName  xml.Name           `xml:"http://uri.etsi.org/03221/X1/2017/10 X1Request"`
	Messages []X1RequestMessage `xml:"x1RequestMessage"`
}

// X1RequestMessage is one request. The xsi:type attribute selects the action
// (ActivateTaskRequest, ModifyTaskRequest, DeactivateTaskRequest, …); the
// action-specific payload is carried in TaskDetails (activate/modify) or XID
// (deactivate). The header fields are common to every message type.
type X1RequestMessage struct {
	Type             string       `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	AdmfIdentifier   string       `xml:"admfIdentifier"`
	NeIdentifier     string       `xml:"neIdentifier"`
	MessageTimestamp string       `xml:"messageTimestamp"`
	Version          string       `xml:"version"`
	X1TransactionID  string       `xml:"x1TransactionId"`
	TaskDetails      *TaskDetails `xml:"taskDetails,omitempty"` // ActivateTask / ModifyTask
	XID              string       `xml:"xId,omitempty"`         // DeactivateTask
	// DestinationDetails carries a delivery destination being provisioned
	// (CreateDestination). A task references destinations by DID, so they must be
	// installed before the first task that names them.
	DestinationDetails *DestinationDetails `xml:"destinationDetails,omitempty"`
	// DID is a bare destination identifier, as GetDestinationDetails carries it --
	// distinct from the DID inside DestinationDetails, which is part of a destination
	// being provisioned. (RemoveDestination carries one too; this element refuses that
	// message, see CONFORMANCE.md.)
	DID string `xml:"dId,omitempty"`

	shape wireShape // what decoding saw; see strict.go
}

// collapseTokens puts every xs:token-derived field of the message into the value the schema
// gives it, before anything reads one.
//
// The schema types the header identifiers and the task, product and destination identifiers
// as xs:token or types derived from it, whose whiteSpace facet is
// "collapse": surrounding whitespace is not part of the value and an internal run is one
// space. A conformant peer may therefore send `<xId> 50b9… </xId>` across lines, and that is
// the same identifier as the unpadded one -- comparing the raw text would refuse it as
// malformed, or key a task under a spelling no later message repeats. correlationID is an
// xs:nonNegativeInteger, whose lexical space also admits a leading "+" and a negative zero. The version and the
// deliveryType are xs:string, whose whitespace is part of the value, and are left as sent.
func (m *X1RequestMessage) collapseTokens() {
	for _, f := range []*string{&m.AdmfIdentifier, &m.NeIdentifier, &m.X1TransactionID, &m.XID, &m.DID} {
		*f = collapseToken(*f)
	}
	if d := m.DestinationDetails; d != nil {
		d.DID = collapseToken(d.DID)
	}
	if td := m.TaskDetails; td != nil {
		td.XID = collapseToken(td.XID)
		td.ProductID = collapseToken(td.ProductID)
		for i := range td.ListOfDIDs {
			td.ListOfDIDs[i] = collapseToken(td.ListOfDIDs[i])
		}
		for i := range td.DSIDs {
			td.DSIDs[i] = collapseToken(td.DSIDs[i])
		}
		td.CorrelationID = nonNegativeIntegerToken(collapseToken(td.CorrelationID))
	}
}

// nonNegativeIntegerToken removes the signs xs:nonNegativeInteger's lexical space admits: a
// leading "+", and the "-" of a negative zero, which is zero. What remains is checked as digits
// where the message is validated, so anything else is left for that check to refuse.
func nonNegativeIntegerToken(s string) string {
	if rest, ok := strings.CutPrefix(s, "-"); ok && rest != "" && strings.Trim(rest, "0") == "" {
		return rest
	}

	return strings.TrimPrefix(s, "+")
}

// collapseToken applies XML Schema's "collapse" whitespace facet: tab, carriage return and
// line feed become spaces, runs of spaces become one, and leading and trailing spaces go.
// Only those four characters are XML whitespace; any other is part of the value.
func collapseToken(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}), " ")
}

// DestinationDetails is a delivery destination (TS 103 221-1 clause 6.3.1.2).
// Field order follows its xs:sequence.
type DestinationDetails struct {
	DID          string          `xml:"dId"`
	FriendlyName string          `xml:"friendlyName,omitempty"`
	DeliveryType string          `xml:"deliveryType"`
	Address      DeliveryAddress `xml:"deliveryAddress"`
	// Extensions is the destination's own extension placeholder. Bound for the same
	// reason ListOfServiceTypes is: an extension exists to change the meaning of the
	// message carrying it, and encoding/xml discards what a struct does not declare.
	Extensions []MessageExtension `xml:"destinationDetailsExtensions,omitempty"`

	shape wireShape
}

// MessageExtension is the TS 103 221-1 Extension placeholder as it appears on a task
// or a destination: an Owner naming the specification that defines the content, then
// that content as elements from another namespace.
//
// Content is captured rather than modelled. Nothing here acts on an extension; the
// point of binding it is to be able to *refuse* one, and a refusal needs the owner and
// the names of what was sent, not their meaning.
type MessageExtension struct {
	Owner string `xml:"Owner"`
	// IdentifierAssociation is the one task extension this element acts on. TS 33.128
	// table 6.2.2.1-1 makes it conditional on the AMF IRI-POI's ActivateTask and gives
	// it real force: absent, the identifier-association records "shall not be
	// generated". Refusing every extension would therefore refuse a conformant 3GPP
	// ADMF, which is why the test is on the owner and content rather than on presence.
	IdentifierAssociation *IdentifierAssociationExtension `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IdentifierAssociationExtensions,omitempty"`
	// Content is every other element the extension carries, kept for its name alone:
	// nothing acts on it, and a refusal needs to say what was sent, not what it meant.
	Content []ExtensionItem `xml:",any"`

	shape wireShape
	// associations counts the IdentifierAssociationExtensions elements decoded: the field keeps
	// the last, so a second one is otherwise invisible.
	associations int
}

// IdentifierAssociationExtension carries the record scoping of TS 33.128
// clause 6.2.2.2.1. EventsGenerated is a closed enumeration — "IdentifierAssociation"
// or "All" — and a value outside it is refused rather than defaulted: guessing would
// either withhold records a warrant authorises or produce records it does not.
type IdentifierAssociationExtension struct {
	EventsGenerated string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IdentifierAssociationEventsGenerated"`

	shape wireShape
}

// ExtensionItem is one element inside an extension, kept for its name alone.
type ExtensionItem struct {
	XMLName xml.Name
}

// DeliveryAddress is a CHOICE of address forms. Only ipAddressAndPort is
// modelled — the X2/X3 senders deliver to a host and port — so a destination
// given as a URI, E.164 number or email address is rejected rather than
// silently accepted and never delivered to.
type DeliveryAddress struct {
	IPAddressAndPort *IPAddressPort `xml:"ipAddressAndPort,omitempty"`

	shape wireShape
}

// IPAddressPort is the TS 103 280 address-and-port structure. Its namespace is
// elementFormDefault="qualified", hence the qualified child names.
type IPAddressPort struct {
	Address IPAddress `xml:"http://uri.etsi.org/03280/common/2017/07 address"`
	Port    Port      `xml:"http://uri.etsi.org/03280/common/2017/07 port"`

	shape wireShape
}

// Port is the TS 103 280 Port CHOICE: a TCPPort or a UDPPort *child element*, never a
// number in the element's own text. Bound as the choice rather than as a bare number,
// because a number in the element's text is what a conformant peer never sends: it would
// parse a conformant port as zero and store a destination pointing nowhere.
type Port struct {
	TCPPort *uint16 `xml:"http://uri.etsi.org/03280/common/2017/07 TCPPort,omitempty"`
	UDPPort *uint16 `xml:"http://uri.etsi.org/03280/common/2017/07 UDPPort,omitempty"`

	shape wireShape
}

// Value returns the port number whichever arm carries it, or 0 when neither does.
// The transport is not distinguished: X2 and X3 are carried over TCP (TS 103 221-2),
// so a UDPPort names the same endpoint this element would dial anyway.
//
// **It does not settle a multi-arm choice, and must not be asked to.** Populating both
// arms is invalid against the schema, so no reading of it is authoritative — and this
// switch's order would make the element choose which port a warrant's product is
// delivered to. Cardinality is decided on the decode path (Arms), before any consumer
// reaches this.
func (p Port) Value() uint16 {
	switch {
	case p.TCPPort != nil:
		return *p.TCPPort
	case p.UDPPort != nil:
		return *p.UDPPort
	default:
		return 0
	}
}

// Arms counts how many arms of the Port choice carry a value.
//
// The same rule the element already applies to targetIdentifier arms, at the level a
// destination's port lives: zero populated arms is a destination with no port, several is
// a message that violates its own format, and exactly one is the only valid shape. Kept
// beside the declaration it mirrors so an arm added to the struct and not added here is
// visible as an omission. A decoded port counts every child element it carried, so a
// repeated arm, or one this structure does not model, is an arm too.
func (p Port) Arms() int {
	n := 0
	for _, v := range []*uint16{p.TCPPort, p.UDPPort} {
		if v != nil {
			n++
		}
	}

	return max(n, p.shape.children)
}

// IPAddress is a CHOICE of IPv4 and IPv6 literal.
type IPAddress struct {
	IPv4 string `xml:"http://uri.etsi.org/03280/common/2017/07 IPv4Address,omitempty"`
	IPv6 string `xml:"http://uri.etsi.org/03280/common/2017/07 IPv6Address,omitempty"`

	shape wireShape
}

// Arms counts how many arms of the IPAddress choice carry a value. See Port.Arms.
//
// An arm of nothing but XML whitespace carries no value: both arms are xs:token patterns, which
// collapse it to nothing.
func (a IPAddress) Arms() int {
	n := 0
	for _, v := range []string{a.IPv4, a.IPv6} {
		if xsToken(v) != "" {
			n++
		}
	}

	return max(n, a.shape.children)
}

// Valid reports whether this address is one arm carrying a value of the family that arm
// names.
//
// **The family matters as much as the cardinality.** TS 103 280 types these as an IPv4
// and an IPv6 address. An address that is not one would be created, acknowledged and then
// dialled forever, with the failure reported as an unreachable mediation function rather
// than as the provisioning error it is. Checking the family and not just the syntax also
// refuses an IPv6 literal placed in the IPv4 arm, which would otherwise be joined to a port
// without brackets and produce an address nothing can dial.
//
// The IPv4 arm is read by the schema's own pattern, which admits a leading zero in an
// octet ("010.000.000.001") that netip.ParseAddr refuses as ambiguous. Both arms are patterns
// over xs:token, so the value is read without the whitespace around it: an address sent across
// lines is the address.
func (a IPAddress) Valid() error {
	switch n := a.Arms(); {
	case n == 0:
		return fmt.Errorf("address populates no arm of a choice; exactly one is valid")
	case n > 1:
		return fmt.Errorf("address populates %d arms of a choice; exactly one is valid", n)
	}

	if xsToken(a.IPv4) != "" {
		if _, ok := parseSchemaIPv4(xsToken(a.IPv4)); !ok {
			return errors.New("IPv4Address is not an IPv4 address")
		}

		return nil
	}
	ip, err := netip.ParseAddr(xsToken(a.IPv6))
	if err != nil || ip.Is4() || ip.Is4In6() || ip.Zone() != "" {
		// A zone or an IPv4-mapped address parses, but neither is an address of the
		// schema's IPv6 type: a zone has no representation in it, and a mapped address
		// names an IPv4 endpoint. Accepting either would store a value this element
		// could not render back into the arm it came in.
		return errors.New("IPv6Address is not an IPv6 address")
	}

	return nil
}

// Canonical returns the address in one spelling: dotted-quad without leading zeros for
// IPv4, and eight four-digit lowercase groups for IPv6. Call it only on an address Valid
// accepted.
//
// The IPv6 pattern admits only the expanded form, so a destination stored as the peer sent
// it — "::1", or upper case — would make this element's own destination answers invalid
// against the schema the request was validated with. The IPv4 form is canonical so that one
// endpoint has one key, however a peer spelled it.
func (a IPAddress) Canonical() string {
	if xsToken(a.IPv4) != "" {
		if ip, ok := parseSchemaIPv4(xsToken(a.IPv4)); ok {
			return ip.String()
		}

		return a.IPv4
	}
	if ip, err := netip.ParseAddr(xsToken(a.IPv6)); err == nil {
		return expandIPv6(ip.AsSlice())
	}

	return a.IPv6
}

// errPortNamesNoEndpoint is Valid's answer for a UDPPort of 0 or 1: inside the schema's type,
// so not a schema error, but not a port this element could deliver to.
var errPortNamesNoEndpoint = errors.New("UDPPort 0 or 1 names no endpoint this element can deliver to")

// Valid reports whether this port is one arm carrying a value a peer can be dialled on.
//
// Each arm is held to its own TS 103 280 type: TCPPort is minExclusive 1, so 0 and 1 are
// outside it and refused as malformed, as is a number past 65535 in either arm. UDPPort admits 0
// and 1, which are schema-valid but name no endpoint this element can deliver to: X2 and X3 are
// carried over TCP, so the number is dialled as a TCP port, and the destination answers render
// it as the TCPPort it is used as -- where 0 and 1 are outside the type. That case returns
// errPortNamesNoEndpoint for the caller to refuse as a destination that cannot be created rather
// than as a malformed one.
func (p Port) Valid() error {
	switch n := p.Arms(); {
	case n == 0:
		return fmt.Errorf("port populates no arm of a choice; exactly one is valid")
	case n > 1:
		return fmt.Errorf("port populates %d arms of a choice; exactly one is valid", n)
	}
	if err := p.shape.err("port"); err != nil {
		return err
	}
	switch {
	case p.TCPPort != nil && *p.TCPPort <= 1:
		return fmt.Errorf("TCPPort %d is outside its schema type, which starts at 2", *p.TCPPort)
	case p.UDPPort != nil && *p.UDPPort <= 1:
		return errPortNamesNoEndpoint
	}

	return nil
}

// TaskDetails describes the interception task being (de)provisioned. It is only ever
// decoded — answers are rendered by templates — so its field order does not bind the wire
// form; the element names do. Note the schema spells correlationID and productID with a
// capitalised "ID", not the "xId"/"dId" pattern its other identifiers use.
type TaskDetails struct {
	XID               string             `xml:"xId"`
	TargetIdentifiers []TargetIdentifier `xml:"targetIdentifiers>targetIdentifier"`
	DeliveryType      string             `xml:"deliveryType"` // X2Only | X3Only | X2andX3
	ListOfDIDs        []string           `xml:"listOfDIDs>dId"`
	// DSIDs are destination *set* identifiers, the other arm of listOfDIDs. A set is a
	// Generic Object (annex E: the ADMF creates a DestinationSetDetails object, then
	// names its object id here), and this element implements no Generic Objects — so a
	// dSId can never name anything it holds. Bound in order to refuse a task whose
	// destinations are named only this way; see the refusal in taskFromDetails.
	DSIDs         []string `xml:"listOfDIDs>dSId,omitempty"`
	CorrelationID string   `xml:"correlationID,omitempty"` // xs:nonNegativeInteger
	ProductID     string   `xml:"productID,omitempty"`     // UUIDv4; overrides XID in X2/X3 headers
	// ListOfServiceTypes narrows a task to particular CSP service types. Bound so
	// that it can be *refused*: encoding/xml drops elements a struct does not
	// declare, so without it an instruction to intercept less would be silently
	// discarded and everything delivered instead. Element names per
	// TS_103_221_01.xsd (listOfServiceTypes containing serviceType).
	ListOfServiceTypes []string `xml:"listOfServiceTypes>serviceType,omitempty"`
	// ListOfMediationDetails is accepted and disregarded, which is what TS 103 221-1
	// asks of a POI. The field is "for use by an NE that is performing mediation (i.e.
	// a mediation and delivery function). This shall be included between the ADMF and
	// the MDF." The AMF, SMF and UPF host POIs, not an MDF, so the details are not
	// addressed to them — and refusing them would refuse a conformant task.
	//
	// Bound all the same, so the disregard is a decision recorded here rather than an
	// element encoding/xml happened to drop.
	ListOfMediationDetails []MediationDetails `xml:"listOfMediationDetails>mediationDetails,omitempty"`
	// ImplicitDeactivationAllowed is likewise accepted and disregarded: "Indication
	// that a Task may implicitly deactivate itself once the NE has determined that it
	// has completed." This element never concludes that a task has completed, so it
	// never self-deactivates and the permission is unused either way. An ADMF setting
	// it will not receive the ReportTaskIssue the field implies — a missing feature,
	// not a divergence in what is intercepted.
	//
	// A pointer so that "absent" and "present and false" stay distinguishable, which
	// the drift audit needs even though the behaviour does not.
	ImplicitDeactivationAllowed *bool `xml:"implicitDeactivationAllowed,omitempty"`
	// TaskDetailsExtensions is refused unless recognised. An extension exists in order
	// to change the meaning of the message that carries it — the LI_T3 detection
	// criteria arrive through exactly such a placeholder — so ignoring an unknown one
	// is the opposite of safe.
	TaskDetailsExtensions []MessageExtension `xml:"taskDetailsExtensions,omitempty"`
	// ListOfTrafficPolicyReferences is refused. It is an "Ordered list  of
	// TrafficPolicyReferences to be applied to the LITaskObject", defined in
	// TS 103 120 clause 8.2.13, which these packages do not implement. A policy that
	// is meant to shape what a task collects, silently unapplied, would be the same
	// failure as an unapplied listOfServiceTypes.
	ListOfTrafficPolicyReferences []string `xml:"listOfTrafficPolicyReferences>trafficPolicyReference,omitempty"`

	shape wireShape
	// sentCorrelationID and sentProductID record that the element was present, however
	// empty: an empty one is outside its type, and decodes to the same "" as an absent one.
	sentCorrelationID, sentProductID bool
}

// MediationDetails is one entry of listOfMediationDetails. Only enough of its
// xs:sequence is bound to show that the element was seen and disregarded on purpose:
// nothing here reads these values, because the structure is addressed to a mediation
// function and this element is not one.
type MediationDetails struct {
	LIID         string `xml:"LIID"`
	DeliveryType string `xml:"deliveryType"`
}

// TargetIdentifier is a CHOICE of target-identifier kinds. Only the subset the
// 5G POIs match on is modelled; the rest of the TS 103 221-1 set is unmodelled
// (an unknown identifier yields an empty match and is rejected).
type TargetIdentifier struct {
	IMSI      string `xml:"imsi,omitempty"`
	SUPIIMSI  string `xml:"supiimsi,omitempty"`
	SUPINAI   string `xml:"supinai,omitempty"`
	PEIIMEI   string `xml:"peiImei,omitempty"`
	PEIIMEISV string `xml:"peiImeisv,omitempty"`
	// IMEI and PEIIMEICheckDigit name the same device as PEIIMEI: the 14-digit IMEI, and
	// the 15-digit form that appends its TS 23.003 check digit. Both are matched as the
	// 14-digit PEI the network functions present.
	IMEI              string `xml:"imei,omitempty"`
	PEIIMEICheckDigit string `xml:"peiImeiCheckDigit,omitempty"`
	GPSIMSISDN        string `xml:"gpsiMsisdn,omitempty"`
	E164Number        string `xml:"e164Number,omitempty"`
	// The plain TS 103 221-1 arms that TS 33.128 table 6.2.3-7 lists as LI_T3
	// packet-detection criteria. Element names are the schema's, which are
	// lowercase-initial — the table names the *types* (IPv4Address, TCPPort), not
	// these elements, so they are not interchangeable.
	//
	// GTPUTunnelID is an integer in the schema (GtpTunnelId is an xs:integer, not a
	// structure), so it carries a TEID with no address alongside it.
	GTPUTunnelID string `xml:"gtpuTunnelId,omitempty"`
	IPv4Address  string `xml:"ipv4Address,omitempty"`
	IPv6Address  string `xml:"ipv6Address,omitempty"`
	TCPPort      string `xml:"tcpPort,omitempty"`
	UDPPort      string `xml:"udpPort,omitempty"`
	// Extension carries identifier types defined outside TS 103 221-1. The only
	// one modelled is the 3GPP LI_T3 packet-detection criteria a CC Triggering
	// Function sends a triggered CC-POI.
	Extension *TargetIdentifierExtension `xml:"targetIdentifierExtension,omitempty"`
	// Unmodelled collects every child element no field above claims — an arm TS 103
	// 221-1 defines that this element does not model, such as macAddress. encoding/xml
	// otherwise drops such an element silently, and a choice carrying one modelled and
	// one unmodelled arm would look like a single modelled arm: the element would narrow
	// the interception to the arm it understood and acknowledge it.
	Unmodelled []UnmodelledElement `xml:",any"`

	shape wireShape
}

// UnmodelledElement is one child element a decoded structure has no field for. Only its
// name is kept: it is evidence that the element was present, not something to act on.
type UnmodelledElement struct {
	XMLName xml.Name
}

// ExtensionOwner3GPP is the Extension/Owner value for identifier types 3GPP
// defines (TS 103 221-1 annex B; TS 33.128 table 6.2.3-7 "Owner" column).
const ExtensionOwner3GPP = "3GPP"

// NamespaceX1Ext3GPP is the namespace of the 3GPP X1 extension schema
// (urn_3GPP_ns_li_3GPPX1Extensions.xsd, shipped with TS 33.128). It is
// elementFormDefault="qualified", so its child elements are namespace-qualified
// on the wire — unlike the ETSI X1 body around them.
const NamespaceX1Ext3GPP = "urn:3GPP:ns:li:3GPPX1Extensions:r18:v6"

// TargetIdentifierExtension is the TS 103 221-1 Extension placeholder: an Owner
// naming the specification that defines the content, then that content.
type TargetIdentifierExtension struct {
	Owner string             `xml:"Owner"`
	UPFT3 *UPFLIT3Extensions `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 UPFLIT3TargetIdentifierExtensions,omitempty"`
	// Unmodelled collects every other element the extension carries -- an identifier 3GPP
	// defines that this element does not model, such as an AKMA one. Without it encoding/xml
	// drops the element, and an extension carrying it beside the LI_T3 criteria would be read
	// as the criteria alone: a target the task ordered, silently not applied.
	Unmodelled []UnmodelledElement `xml:",any"`

	shape wireShape
	// lit3Containers counts the UPFLIT3TargetIdentifierExtensions elements decoded. The field
	// keeps the last, so a second container's criteria would otherwise be dropped unseen.
	lit3Containers int
}

// UPFLIT3Extensions holds the LI_T3 packet-detection criteria for a triggered
// CC-POI in a UPF. The schema allows several and every one of them is a criterion:
// this is a SEQUENCE OF CHOICE, so a list is what the structure is for, and every
// member joins the task's criteria, all of which are required to match (TS 103 221-1
// table 6.2.1.2-1). This element sends one, because a session is identified by its
// F-SEID; it accepts as many as a provisioning function sends.
type UPFLIT3Extensions struct {
	Identifiers []UPFLIT3Identifier `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 UPFLIT3TargetIdentifier"`
	// Unmodelled collects any other child. The schema allows none, so one is a malformed
	// message rather than a criterion to drop.
	Unmodelled []UnmodelledElement `xml:",any"`
}

// UPFLIT3Identifier is a CHOICE over the extension criteria of TS 33.128 table
// 6.2.3-7. All seven arms the schema defines are modelled. Note that NetworkInstance
// and PDR are xs:hexBinary rather than strings, and that FTEID here may carry an
// address beside the TEID — unlike the plain gtpuTunnelId identifier, which the
// schema types as a bare integer.
type UPFLIT3Identifier struct {
	FSEID              *FSEID  `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 FSEID,omitempty"`
	PDRID              *uint32 `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 PDRID,omitempty"`
	QERID              *uint32 `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 QERID,omitempty"`
	NetworkInstance    string  `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 NetworkInstance,omitempty"`
	GTPTunnelDirection string  `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 GTPTunnelDirection,omitempty"`
	FTEID              *FTEID  `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 FTEID,omitempty"`
	PDR                string  `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 PDR,omitempty"`
	// Unmodelled collects every child element no field above claims, for the reason
	// TargetIdentifier.Unmodelled does: a criterion carrying one modelled and one unknown
	// arm would otherwise decode as the modelled arm alone, and the element would apply
	// fewer criteria than were ordered -- which, since every criterion must match, widens
	// the interception.
	Unmodelled []UnmodelledElement `xml:",any"`

	shape wireShape
}

// FTEID is a GTP tunnel endpoint: the TEID, optionally with the address of the node
// that terminates it. The addresses are optional in the schema, so a criterion may
// name a TEID alone — which cannot distinguish two tunnels that share one.
type FTEID struct {
	TEID        uint32 `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 TEID"`
	IPv4Address string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv4Address,omitempty"`
	IPv6Address string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv6Address,omitempty"`

	shape wireShape
}

// GTPDirectionOutbound and GTPDirectionInbound are the schema's GTP tunnel directions, a
// closed enumeration -- "Outbound" or "Inbound", not the uplink/downlink vocabulary used
// elsewhere in 3GPP. A value outside it is refused rather than passed through: an
// unrecognised direction that matched nothing would silently intercept nothing, and one
// that matched everything would collect both directions when one was authorised.
const (
	GTPDirectionOutbound = "Outbound"
	GTPDirectionInbound  = "Inbound"
)

// FSEID is a PFCP session identifier: the SEID plus the address of the node that
// allocated it. Within one UPF the SEID alone is unambiguous, so the addresses
// are not matched: the criterion is the SEID. They are still checked against their
// schema types, so a malformed one is refused rather than acknowledged unread.
type FSEID struct {
	SEID        uint64 `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 SEID"`
	IPv4Address string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv4Address,omitempty"`
	IPv6Address string `xml:"urn:3GPP:ns:li:3GPPX1Extensions:r18:v6 IPv6Address,omitempty"`

	shape wireShape
}

// X1Response is the top-level X1 response envelope.
type X1Response struct {
	XMLName  xml.Name            `xml:"http://uri.etsi.org/03221/X1/2017/10 X1Response"`
	Messages []X1ResponseMessage `xml:"x1ResponseMessage"`
}

// X1ResponseMessage acknowledges one request message.
type X1ResponseMessage struct {
	Type             string   `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	AdmfIdentifier   string   `xml:"admfIdentifier"`
	NeIdentifier     string   `xml:"neIdentifier"`
	MessageTimestamp string   `xml:"messageTimestamp"`
	Version          string   `xml:"version"`
	X1TransactionID  string   `xml:"x1TransactionId"`
	OK               string   `xml:"oK,omitempty"`
	ErrorInformation *X1Error `xml:"errorInformation,omitempty"`
	// Tasks carries the answer to a details query on the way *out*: the server
	// renders one taskResponseDetails per task, so a requester can see what this
	// element actually holds rather than what it believes it provisioned.
	Tasks []types.InterceptTask `xml:"-"`
	// TaskResponses is the same information on the way *in*, parsed by a requester
	// reading a reply. The two directions are separate fields because the outgoing
	// form is built from the domain model and the incoming form is whatever the peer
	// sent, which may include tasks this requester knows nothing about — the point
	// of asking.
	TaskResponses []TaskResponseDetails `xml:"taskResponseDetails"`
	// AllTaskResponses is the same, nested, as GetAllDetailsResponse defines it. The two
	// answers put a task in different places — GetTaskDetails directly, GetAllDetails inside
	// listOfTaskResponseDetails — and encoding/xml matches one tag per field, so a requester
	// that bound only the direct form read *zero* tasks from a GetAllDetails reply. Use
	// ReportedTasks rather than either field.
	//
	// A pointer, so an answer that omits the list — which GetAllDetailsResponse makes
	// mandatory — can be told from one that lists no task.
	AllTaskResponses *TaskResponseList `xml:"listOfTaskResponseDetails"`
	// RequestType is the type of the request being answered. It is only rendered on an
	// ErrorResponse, where the schema makes requestMessageType mandatory — omitting it made
	// every refusal this element sent invalid.
	RequestType string `xml:"-"`
	// Destinations are the destinations this element holds, for the answers that report
	// them. Carried separately from Tasks for the same reason: the outgoing form is built
	// from what the element holds, not parsed from a peer.
	Destinations []ReportedDestination `xml:"-"`
	// Faults are the element's own unresolved faults, for neStatusDetails: the conditions a
	// registered probe says hold at the moment the status was asked for, never a history.
	Faults []X1Error `xml:"-"`
	// TaskFaults are the faults the element reports against individual tasks, keyed by XID —
	// the answer to "what is wrong with that warrant's interception", as distinct from Faults,
	// which answers "what is wrong with this element". A task with no entry has nothing wrong
	// with it.
	TaskFaults map[types.XID][]X1Error `xml:"-"`
}

// ReportedTasks returns the tasks a peer reported, whichever answer carried them.
func (m X1ResponseMessage) ReportedTasks() []TaskResponseDetails {
	if m.AllTaskResponses != nil && len(m.AllTaskResponses.Tasks) > 0 {
		return m.AllTaskResponses.Tasks
	}

	return m.TaskResponses
}

// TaskResponseList is GetAllDetailsResponse's listOfTaskResponseDetails.
type TaskResponseList struct {
	Tasks []TaskResponseDetails `xml:"taskResponseDetails"`
}

// ReportedDestination is one destination as an element reports it: the identifier it is
// held under, where it points, and how this element came to know it.
type ReportedDestination struct {
	DID string
	// DeliveryType is the X1 value the destination was declared with — X2Only, X3Only
	// or X2andX3. It is kept rather than derived from an endpoint type, because one
	// destination serving both interfaces is one record and two endpoints.
	DeliveryType string
	Address      string // "host:port"
	FriendlyName string // the ADMF's own name for it, where one was given
	// Configured marks an entry this element's configuration declares rather than one
	// provisioned with CreateDestination.
	Configured bool
	// ShadowsConfigured marks a provisioned entry that supersedes a configured one for
	// the same DID. Provisioned wins, which is the right precedence and the wrong kind
	// of silence: an operator whose configured entry is not the one in force has to be
	// able to see that from what the element reports.
	ShadowsConfigured bool
	// Unreachable is whether this element can currently deliver to Address, as the
	// delivery layer itself answers it. It is what destinationDeliveryStatus reports:
	// deliveryFault when set, activeAndWorking when not.
	//
	// It is filled in when an answer is assembled and is not stored, for the reason
	// unresolvedFaults gives about the element's own faults: a reachability answer that
	// is cached is an answer that can be stale, and this one is re-observable, so
	// caching it would buy nothing and cost the ADMF a fault that has cleared or a
	// destination reported as working after it stopped working.
	Unreachable bool
}

// TaskResponseDetails is one task as reported by an element (TS 103 221-1
// clause 6.2.5).
type TaskResponseDetails struct {
	TaskDetails TaskDetails `xml:"taskDetails"`
	TaskStatus  TaskStatus  `xml:"taskStatus"`
}

// TaskStatus is how a peer reports the state of a task it holds. It is a complex
// type in the schema, parsed as one: read as a plain string, a peer's entire account of
// a task would be concatenated character data a requester could act on none of.
//
// Only the two fields a requester can act on are modelled. The rest are declared
// disregarded, with reasons, in the response-side drift audit:
//
//   - timeOfLastIntercept, amountOfX2Data, amountOfX3Data, timeOfLastModification,
//     numberOfModifications are statistics. A triggering function does not
//     reconcile against them, and reporting them onward would state as fact
//     figures it has not verified.
//   - taskStatusExtensions is the schema's extension point; there is nothing
//     defined for this element to act on.
type TaskStatus struct {
	// ProvisioningStatus is whether the peer has provisioned the task. Anything
	// other than "complete" means the interception a triggering function believes
	// it installed is not running.
	ProvisioningStatus string `xml:"provisioningStatus"`
	// Faults are the peer's unresolved faults for this task. Mandatory in the
	// schema and may be empty, so an empty list is the healthy answer.
	Faults []X1Error `xml:"listOfFaults>unresolvedFault"`
}

// Healthy reports whether a peer's account of a task says the interception is
// actually running: provisioned, and with no unresolved fault.
//
// It treats an empty provisioning status as healthy. A peer that reports no
// status is not reporting a *problem*, and the alternative — calling silence a
// fault — would make every older or sparser implementation look broken.
func (t TaskStatus) Healthy() bool {
	return len(t.Faults) == 0 && (t.ProvisioningStatus == "" || t.ProvisioningStatus == "complete")
}

// Describe renders the unhealthy part of a status for a fault report. It never
// includes a target identifier or anything else that could identify a subject —
// callers pass it to the ADMF, which is told how much is wrong, never whose.
func (t TaskStatus) Describe() string {
	var b strings.Builder
	if t.ProvisioningStatus != "" && t.ProvisioningStatus != "complete" {
		fmt.Fprintf(&b, "provisioning status %q", t.ProvisioningStatus)
	}
	for _, f := range t.Faults {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "unresolved fault %d", f.ErrorCode)
	}
	if b.Len() == 0 {
		return "no fault reported"
	}

	return b.String()
}

// X1Error carries an error response (subset).
type X1Error struct {
	ErrorCode        int    `xml:"errorCode"`
	ErrorDescription string `xml:"errorDescription"`
}
