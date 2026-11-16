// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/omec-project/util/li/types"
)

// Each X1 response type defines its own body, and they are not variations on one shape: a
// details response carries content and no acknowledgement, an acknowledgement carries the
// reverse, and an error carries the type of the request it is refusing. One uniform template
// cannot express that — it would put an `oK` in responses that define none, render a
// `taskStatus` as a string where the schema defines a complex type, and omit the mandatory
// elements of `GetAllDetails`.
//
// So the shape is decided here, per type, where it can be read against the schema and
// tested. `TestRenderedResponsesValidate` validates every one of these against the
// published XSD.

// The response types whose renderers report a task's faults, named because
// rendersTaskFaults and responseBody must agree on them.
const (
	getAllDetailsResponse     = "GetAllDetailsResponse"
	getTaskDetailsResponse    = "GetTaskDetailsResponse"
	getAllTaskDetailsResponse = "GetAllTaskDetailsResponse"
)

// rendersTaskFaults reports whether a response type's renderer reports a task's faults, so
// that the supplier is consulted only for answers that carry what it says. It must agree with
// responseBody: every case below that calls taskResponseDetails is listed here.
func rendersTaskFaults(responseType string) bool {
	switch responseType {
	case getAllDetailsResponse, getTaskDetailsResponse, getAllTaskDetailsResponse:
		return true
	default:
		return false
	}
}

// responseBody renders the elements a response type defines, indented to sit inside
// x1ResponseMessage.
func responseBody(m X1ResponseMessage) string {
	switch m.Type {
	case errorResponse:
		return errorBody(m)
	case getAllDetailsResponse:
		return allDetailsBody(m)
	case getTaskDetailsResponse:
		return taskDetailsBody(m)
	case getAllTaskDetailsResponse:
		return listBody("listOfTaskResponseDetails", func(b *strings.Builder) {
			for _, t := range m.Tasks {
				b.WriteString(taskResponseDetails(6, t, m.TaskFaults[t.XID]))
			}
		})
	case "GetAllDestinationDetailsResponse":
		return listBody("listOfDestinationResponseDetails", func(b *strings.Builder) {
			for _, d := range m.Destinations {
				b.WriteString(destinationResponseDetails(6, d))
			}
		})
	case "GetDestinationDetailsResponse":
		if len(m.Destinations) == 0 {
			return ""
		}

		return destinationResponseDetails(4, m.Destinations[0])
	case "GetNEStatusResponse":
		return neStatusDetails(4, m.Faults)
	case "ListAllDetailsResponse":
		return listAllDetailsBody(m)
	case "GetAllGenericObjectDetailsResponse":
		// Empty on purpose, and it has to be stated here rather than left to the
		// acknowledgement case below: the schema gives this type one child, the optional
		// listOfGenericObjectResponseDetails, and no `oK`. Omitting the list is the
		// specification's own way of saying Generic Objects are not supported; emitting an
		// acknowledgement instead would not validate.
		return ""
	default:
		// The acknowledgement types: ActivateTask, ModifyTask, DeactivateTask,
		// CreateDestination, Keepalive, Ping. Their schema definition is X1ResponseMessage
		// plus oK, and nothing else.
		if m.OK != "" {
			return el(4, "oK", escapeXML(m.OK))
		}

		return ""
	}
}

// errorBody renders an ErrorResponse. `requestMessageType` is mandatory: without it every
// refusal would be invalid against the schema — including the refusals that tell an ADMF its
// request was not authorised.
func errorBody(m X1ResponseMessage) string {
	var b strings.Builder
	b.WriteString(el(4, "requestMessageType", requestMessageType(m.RequestType)))
	b.WriteString(open(4, "errorInformation"))
	if m.ErrorInformation != nil {
		b.WriteString(el(6, "errorCode", strconv.Itoa(m.ErrorInformation.ErrorCode)))
		b.WriteString(el(6, "errorDescription", escapeXML(m.ErrorInformation.ErrorDescription)))
	}
	b.WriteString(closeTag(4, "errorInformation"))

	return b.String()
}

// allDetailsBody renders a GetAllDetailsResponse: neStatusDetails, then the task list, then
// the destination list, all three mandatory and in the schema's order.
//
// listOfGenericObjectResponseDetails is optional and omitted: this element implements no
// Generic Objects, and the schema permits leaving it out rather than claiming an empty set.
func allDetailsBody(m X1ResponseMessage) string {
	var b strings.Builder
	b.WriteString(neStatusDetails(4, m.Faults))

	b.WriteString(open(4, "listOfTaskResponseDetails"))
	for _, t := range m.Tasks {
		b.WriteString(taskResponseDetails(6, t, m.TaskFaults[t.XID]))
	}
	b.WriteString(closeTag(4, "listOfTaskResponseDetails"))

	b.WriteString(open(4, "listOfDestinationResponseDetails"))
	for _, d := range m.Destinations {
		b.WriteString(destinationResponseDetails(6, d))
	}
	b.WriteString(closeTag(4, "listOfDestinationResponseDetails"))

	return b.String()
}

// listBody wraps a list in its element. The element is mandatory on GetAllDetails and
// optional on the per-list answers, and is emitted either way: an empty list is the schema's
// own answer for "the element holds none", and the specification says so outright — "If there
// are no destinations, an empty list shall be returned - this is not an error". That matters
// most for a restarted element, which holds nothing precisely when an ADMF most needs a usable
// answer.
func listBody(name string, items func(*strings.Builder)) string {
	var b strings.Builder
	b.WriteString(open(4, name))
	items(&b)
	b.WriteString(closeTag(4, name))

	return b.String()
}

// listAllDetailsBody renders a ListAllDetailsResponse: the identifiers only, no details.
//
// The element names here are capitalised — ListOfXIDs, ListOfDIDs — unlike every other
// element in this schema. That is the schema's own inconsistency, not ours, and it is exactly
// the kind of detail a validator catches and a careful reading does not.
//
// ListOfGenericObjectIDs is omitted rather than emitted empty: the schema permits leaving it
// out when Generic Objects are unsupported, and this element supports none. An empty list
// would assert that it implements them and holds nothing.
func listAllDetailsBody(m X1ResponseMessage) string {
	var b strings.Builder

	b.WriteString(open(4, "ListOfXIDs"))
	for _, t := range m.Tasks {
		b.WriteString(el(6, "xId", escapeXML(string(t.XID))))
	}
	b.WriteString(closeTag(4, "ListOfXIDs"))

	b.WriteString(open(4, "ListOfDIDs"))
	for _, d := range m.Destinations {
		b.WriteString(el(6, "dId", escapeXML(d.DID)))
	}
	b.WriteString(closeTag(4, "ListOfDIDs"))

	return b.String()
}

// taskDetailsBody renders a GetTaskDetailsResponse, which is one taskResponseDetails and no
// acknowledgement.
func taskDetailsBody(m X1ResponseMessage) string {
	if len(m.Tasks) == 0 {
		return ""
	}

	return taskResponseDetails(4, m.Tasks[0], m.TaskFaults[m.Tasks[0].XID])
}

// taskResponseDetails renders one task as the element reports it: what was provisioned, plus
// its status.
func taskResponseDetails(ind int, t types.InterceptTask, faults []X1Error) string {
	var b strings.Builder
	b.WriteString(open(ind, "taskResponseDetails"))

	b.WriteString(open(ind+2, "taskDetails"))
	b.WriteString(el(ind+4, "xId", escapeXML(string(t.XID))))
	b.WriteString(open(ind+4, "targetIdentifiers"))
	for i, id := range t.Targets {
		element := ""
		if i < len(t.TargetElements) {
			element = t.TargetElements[i]
		}
		b.WriteString(el(ind+6, "targetIdentifier", targetXMLAs(id, element)))
	}
	b.WriteString(closeTag(ind+4, "targetIdentifiers"))
	b.WriteString(el(ind+4, "deliveryType", deliveryTypeOf(t.Products)))
	// listOfDIDs is mandatory inside taskDetails, and omitting it made every answer
	// that reports a task invalid. The DIDs are the ones the task carries, not the
	// endpoints they resolved to: an ADMF comparing this against what it sent is
	// comparing identifiers, and the addresses behind them are reported separately by
	// the destination answers.
	//
	// The element is emitted even when the task named nothing, because the schema
	// permits an empty ListOfDids and a task that named no destination is a real state
	// this element holds — the one the configured default endpoint serves.
	b.WriteString(open(ind+4, "listOfDIDs"))
	for _, did := range t.DIDs {
		b.WriteString(el(ind+6, "dId", escapeXML(did)))
	}
	b.WriteString(closeTag(ind+4, "listOfDIDs"))
	// The optional fields this element acts on, in the schema's order, so an ADMF auditing a
	// task reads back what it is running under -- the label its product carries, the value its
	// PDUs are stamped with, and which records it produces -- rather than an account silent on
	// all three. A correlationID of 0 is not rendered: the task holds the value, not whether
	// one was sent, and 0 is also what a task without one holds.
	if t.CorrelationID != 0 {
		b.WriteString(el(ind+4, "correlationID", strconv.FormatUint(t.CorrelationID, 10)))
	}
	if t.ProductID != "" {
		b.WriteString(el(ind+4, "productID", escapeXML(string(t.ProductID))))
	}
	if t.RecordScope != types.RecordScopeStandard {
		b.WriteString(open(ind+4, "taskDetailsExtensions"))
		b.WriteString(el(ind+6, "Owner", ExtensionOwner3GPP))
		b.WriteString(openNS(ind+6, "ext", "IdentifierAssociationExtensions"))
		b.WriteString(elNS(ind+8, "ext", "IdentifierAssociationEventsGenerated", escapeXML(string(t.RecordScope))))
		b.WriteString(closeNS(ind+6, "ext", "IdentifierAssociationExtensions"))
		b.WriteString(closeTag(ind+4, "taskDetailsExtensions"))
	}
	b.WriteString(closeTag(ind+2, "taskDetails"))

	b.WriteString(taskStatus(ind+2, faults))
	b.WriteString(closeTag(ind, "taskResponseDetails"))

	return b.String()
}

// taskStatus renders the schema's TaskStatus, a complex type: not the strings "Active" or
// "Inactive", which appear in no version of the schema.
//
// What the schema asks for is `provisioningStatus`: whether the element has *provisioned*
// the task, not whether it is switched on. A task this element holds has been applied, so it
// is `complete`. The optional counters are omitted rather than guessed.
func taskStatus(ind int, faults []X1Error) string {
	// Always `complete`, and the alternatives are unreachable rather than unhandled.
	//
	// A task in the store has been applied: activation refuses anything this element cannot
	// carry out *before* storing it, so `failed` cannot describe something held. And a
	// deactivated task is gone rather than retained, which the specification requires
	// outright — "to stop a Task 'temporarily', ADMFs shall deactivate the Task and then
	// activate a new Task" — so there is no dormant task for `awaitingProvisioning` to
	// describe either.
	var b strings.Builder
	b.WriteString(open(ind, "taskStatus"))
	b.WriteString(el(ind+2, "provisioningStatus", "complete"))
	b.WriteString(listOfFaults(ind+2, faults))
	b.WriteString(closeTag(ind, "taskStatus"))

	return b.String()
}

// destinationResponseDetails renders one destination as the element reports it.
func destinationResponseDetails(ind int, d ReportedDestination) string {
	var b strings.Builder
	b.WriteString(open(ind, "destinationResponseDetails"))

	b.WriteString(open(ind+2, "destinationDetails"))
	b.WriteString(el(ind+4, "dId", escapeXML(d.DID)))
	b.WriteString(el(ind+4, "friendlyName", escapeXML(destinationProvenance(d))))
	b.WriteString(el(ind+4, "deliveryType", escapeXML(d.DeliveryType)))
	b.WriteString(open(ind+4, "deliveryAddress"))
	b.WriteString(deliveryAddress(ind+6, d.Address))
	b.WriteString(closeTag(ind+4, "deliveryAddress"))
	b.WriteString(closeTag(ind+2, "destinationDetails"))

	b.WriteString(open(ind+2, "destinationStatus"))
	// activeAndWorking or deliveryFault are the only enumerated values, and the element's
	// own delivery layer decides which: see WithDestinationReachability for why answering
	// activeAndWorking unconditionally would be worse than answering nothing.
	status, faults := "activeAndWorking", []X1Error(nil)
	if d.Unreachable {
		status = "deliveryFault"
		// The TS 103 221-1 non-terminating-fault code: the one a ReportNEIssue carries for a
		// fault of this kind. The pushed ReportDestinationIssue states a report type rather
		// than a code, so the interrogation answer is where the code appears.
		faults = []X1Error{{
			ErrorCode:        issueCodeNonTerminatingFault,
			ErrorDescription: "delivery destination is unreachable",
		}}
	}
	b.WriteString(el(ind+4, "destinationDeliveryStatus", status))
	b.WriteString(listOfFaults(ind+4, faults))
	b.WriteString(closeTag(ind+2, "destinationStatus"))

	b.WriteString(closeTag(ind, "destinationResponseDetails"))

	return b.String()
}

// destinationProvenance is what this element says about where a destination came from,
// carried in the destination's friendlyName.
//
// friendlyName is the only free-text field a reported destination has — "a human-readable
// name associated with the delivery destination" — and this element has something to say
// there that no other field can carry: that an entry resolves from configuration rather
// than from CreateDestination, and, where both declare the same DID, that the provisioned
// one has superseded the configured one. Precedence resolved silently is the risk the
// three-source design carries; this is where it stops being silent.
//
// The ADMF's own name, where it gave one, leads — it is the field's actual purpose, and
// this element only appends to it.
func destinationProvenance(d ReportedDestination) string {
	var note string
	switch {
	case d.Configured:
		note = "declared in this element's configuration, not provisioned over X1"
	case d.ShadowsConfigured:
		note = "provisioned over X1, superseding a configured entry for this DID"
	default:
		note = "provisioned over X1"
	}
	if d.FriendlyName == "" {
		return note
	}

	return d.FriendlyName + " (" + note + ")"
}

// deliveryAddress renders a TS 103 280 IPAddressPort from a "host:port".
//
// Both `address` and `port` are element-only choices, not text: `port` takes a `TCPPort` or
// a `UDPPort` child. Rendering the number as the element's text produces XML no peer can
// validate, and means a conformant peer's port parses as zero.
func deliveryAddress(ind int, address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// Not host:port. Rendering half an address would assert something false about
		// where product goes, so nothing is rendered and the response fails validation
		// visibly rather than misreporting the destination.
		return ""
	}

	var b strings.Builder
	b.WriteString(open(ind, "ipAddressAndPort"))
	b.WriteString(openNS(ind+2, "c", "address"))
	// Rendered in the schema's spelling of each family whatever form the address was
	// stored in — a configured destination is written by an operator, not normalised on
	// the way in. A zone has no representation in the schema's IPv6 type and is dropped
	// rather than letting the address fail to parse and land in the IPv4 arm, and an
	// IPv4-mapped address is the IPv4 endpoint it names.
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.WithZone("").Unmap().String()
	}
	name, value := addressArm(host)
	b.WriteString(elNS(ind+4, "c", name, escapeXML(value)))
	b.WriteString(closeNS(ind+2, "c", "address"))
	b.WriteString(openNS(ind+2, "c", "port"))
	// X2 and X3 are carried over TCP (TS 103 221-2), so the port is a TCPPort. Rendered from
	// the number rather than the text: a configured address is the operator's spelling, and
	// "0080" is the same port as 80.
	if n, err := strconv.ParseUint(port, 10, 16); err == nil {
		port = strconv.FormatUint(n, 10)
	}
	b.WriteString(elNS(ind+4, "c", "TCPPort", escapeXML(port)))
	b.WriteString(closeNS(ind+2, "c", "port"))
	b.WriteString(closeTag(ind, "ipAddressAndPort"))

	return b.String()
}

// neStatusDetails renders the element's own status: OK, or Faults with the unresolved ones.
//
// The fault set is supplied by the caller, which computes it from the conditions that hold
// at the moment the question is asked — see unresolvedFaults. This renders whatever it is
// given rather than deciding anything itself, so what an element reports about its own
// health is settled in exactly one place.
func neStatusDetails(ind int, faults []X1Error) string {
	status := "OK"
	if len(faults) > 0 {
		status = "Faults"
	}

	var b strings.Builder
	b.WriteString(open(ind, "neStatusDetails"))
	b.WriteString(el(ind+2, "neStatus", status))
	b.WriteString(listOfFaults(ind+2, faults))
	b.WriteString(closeTag(ind, "neStatusDetails"))

	return b.String()
}

// listOfFaults renders the schema's ListOfFaults, which is mandatory wherever it appears and
// may be empty.
func listOfFaults(ind int, faults []X1Error) string {
	if len(faults) == 0 {
		return fmt.Sprintf("%s<ns1:listOfFaults/>\n", indent(ind))
	}

	var b strings.Builder
	b.WriteString(open(ind, "listOfFaults"))
	for _, f := range faults {
		b.WriteString(open(ind+2, "unresolvedFault"))
		b.WriteString(el(ind+4, "errorCode", strconv.Itoa(f.ErrorCode)))
		b.WriteString(el(ind+4, "errorDescription", escapeXML(f.ErrorDescription)))
		b.WriteString(closeTag(ind+2, "unresolvedFault"))
	}
	b.WriteString(closeTag(ind, "listOfFaults"))

	return b.String()
}

// requestMessageTypes is the schema's RequestMessageType enumeration. A value outside it does
// not validate, so an unrecognised request cannot simply have its own name echoed back.
var requestMessageTypes = map[string]bool{
	"ActivateTask": true, "ModifyTask": true, "DeactivateTask": true,
	"DeactivateAllTasks": true, "GetTaskDetails": true, "CreateDestination": true,
	"ModifyDestination": true, "RemoveDestination": true, "RemoveAllDestinations": true,
	"GetDestinationDetails": true, "GetNEStatus": true, "GetAllDetails": true,
	"GetAllTaskDetails": true, "GetAllDestinationDetails": true,
	"GetAllGenericObjectDetails": true, "ListAllDetails": true,
	"ReportTaskIssue": true, "ReportDestinationIssue": true, "ReportNEIssue": true,
	"Ping": true, "Keepalive": true, "CreateObject": true, "ModifyObject": true,
	"GetObject": true, "DeleteObject": true, "ListObjectsOfType": true,
	"DeleteAllObjects": true, "ExtendedRequestMessageType": true,
}

// requestMessageType maps the request being refused to the enumerated value naming it.
//
// A request type outside the enumeration — a peer sending something this schema version does
// not define — has no name we may use, so it is reported as ExtendedRequestMessageType, the
// enumeration's own escape for types defined elsewhere. The alternative is emitting a value
// that invalidates the error response, which would make a refusal unreportable.
func requestMessageType(requestType string) string {
	name := strings.TrimSuffix(requestType, "Request")
	if requestMessageTypes[name] {
		return name
	}

	return "ExtendedRequestMessageType"
}

// deliveryTypeOf names the DeliveryType enumeration value for a task's product types.
func deliveryTypeOf(p []types.ProductType) string {
	iri := slices.Contains(p, types.ProductIRI)
	cc := slices.Contains(p, types.ProductCC)
	switch {
	case iri && cc:
		return deliveryX2andX3
	case cc:
		return deliveryX3Only
	default:
		return deliveryX2Only
	}
}

// ── XML emission helpers ────────────────────────────────────────────
// Small and explicit, so the difference between a valid and an invalid response is visible
// where the response is built. ind is the indentation in spaces; the body renderers nest
// their elements two spaces per level below x1ResponseMessage, which sits at four.
//
// **Values are passed already escaped.** el and elNS write value verbatim, so every caller
// escapes what it did not produce itself (escapeXML) -- a peer-sourced identifier, an
// address, a description. Element names are always this package's own constants.

func indent(n int) string { return strings.Repeat(" ", n) }

func open(ind int, name string) string     { return fmt.Sprintf("%s<ns1:%s>\n", indent(ind), name) }
func closeTag(ind int, name string) string { return fmt.Sprintf("%s</ns1:%s>\n", indent(ind), name) }

func el(ind int, name, value string) string {
	return fmt.Sprintf("%s<ns1:%s>%s</ns1:%s>\n", indent(ind), name, value, name)
}

func openNS(ind int, ns, name string) string {
	return fmt.Sprintf("%s<%s:%s>\n", indent(ind), ns, name)
}

func closeNS(ind int, ns, name string) string {
	return fmt.Sprintf("%s</%s:%s>\n", indent(ind), ns, name)
}

func elNS(ind int, ns, name, value string) string {
	return fmt.Sprintf("%s<%s:%s>%s</%s:%s>\n", indent(ind), ns, name, value, ns, name)
}

// heldDestinations lists every destination this element can resolve a DID to — those an
// ADMF provisioned and those its configuration declares — ordered by DID so that two
// answers to the same question agree. A map's order does not.
//
// Configured entries are reported because they are destinations from the ADMF's point of
// view: a task naming one is delivered to it exactly as if it had been provisioned, so
// an answer that omitted them would tell an auditing ADMF that a DID it referenced is
// unknown here when product is reaching it. Each is marked, so the answer stays literal
// about what was provisioned as well as complete about what resolves.
func (s *Server) heldDestinations() []ReportedDestination {
	out := s.heldDestinationsUnannotated()
	s.annotateReachability(out)

	return out
}

// heldDestinationsUnannotated is the same list without the reachability answer. Split out because
// that answer comes from the element's delivery layer, which holds locks of its own — calling
// into it under s.mu would order this package's lock ahead of theirs, and the fault probes are
// kept outside the lock for exactly that reason.
func (s *Server) heldDestinationsUnannotated() []ReportedDestination {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]ReportedDestination, 0, len(s.destinations)+len(s.configured))
	for did, dest := range s.destinations {
		out = append(out, s.reportedLocked(did, dest))
	}
	for did, dest := range s.configured {
		// One entry per DID, not two: what is reported is what is in force. The
		// provisioned entry carries the fact that it superseded this one.
		if _, shadowed := s.destinations[did]; shadowed {
			continue
		}
		out = append(out, s.reportedLocked(did, dest))
	}
	slices.SortFunc(out, func(a, b ReportedDestination) int {
		return strings.Compare(a.DID, b.DID)
	})

	return out
}

// reportedLocked turns a held destination into the form the answers render. Caller holds
// s.mu.
func (s *Server) reportedLocked(did string, d heldDestination) ReportedDestination {
	_, alsoConfigured := s.configured[did]

	return ReportedDestination{
		DID:               did,
		DeliveryType:      d.DeliveryType,
		Address:           d.Address,
		FriendlyName:      d.FriendlyName,
		Configured:        d.Configured,
		ShadowsConfigured: !d.Configured && alsoConfigured,
	}
}

// unresolvedFaults returns the fault conditions that hold at this moment.
//
// Computed per call, never cached. That is the whole design: a status answer assembled from
// what is observable now cannot report a fault that has cleared, and cannot need an expiry or
// an explicit clear — the two mechanisms that would otherwise decide between discarding real
// faults on a timer and reporting an element as permanently broken.
//
// The cost is that a fault which is an *event* rather than a state — a copy dropped at the
// egress, an authentication attempt refused — is reported when it happens and is not
// re-observable afterwards, so it does not appear here. That is why the push reporting is not
// redundant.
//
// It consults only what a POI registers, and holds no condition of its own. "Tasking is held
// that no destination resolves for" looks like one, but it is not a fault for these POIs:
// the AMF sends X2 to the MDF2 in its configuration, the SMF provisions its configured MDF3 at
// the CC-POI, and the UPF, which does read a task's destinations, refuses a content task
// without one before storing it. The condition would hold for every ordinary task provisioned
// without DIDs, and an element that always answers "Faults" is ignored exactly as fast as one
// that always answers "OK".
func (s *Server) unresolvedFaults() []X1Error {
	var faults []X1Error

	s.mu.Lock()
	probes := slices.Clone(s.faultProbes)
	s.mu.Unlock()

	for _, probe := range probes {
		if f := probe(); f != nil {
			faults = append(faults, *f)
		}
	}

	return faults
}

// destinationByDID returns the one destination a DID resolves to, provisioned or
// configured. It answers the same question heldDestinations does, for one identifier, so
// the two cannot disagree about whether this element can deliver to a DID.
func (s *Server) destinationByDID(did string) (ReportedDestination, bool) {
	reported, ok := s.destinationByDIDUnannotated(did)
	if !ok {
		return ReportedDestination{}, false
	}

	// Outside the lock, as heldDestinationsUnannotated documents.
	one := []ReportedDestination{reported}
	s.annotateReachability(one)

	return one[0], true
}

// destinationByDIDUnannotated resolves one DID without the reachability answer.
func (s *Server) destinationByDIDUnannotated(did string) (ReportedDestination, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dest, ok := s.resolveLocked(did)
	if !ok {
		return ReportedDestination{}, false
	}

	return s.reportedLocked(did, dest), true
}

// annotateReachability fills in each entry's delivery status from the element's own delivery
// layer. Called with s.mu released.
//
// An element that supplies no answer leaves every entry as it is, which reports
// activeAndWorking: a consequence of the element having nothing to say, not a claim made on
// its behalf.
func (s *Server) annotateReachability(dests []ReportedDestination) {
	s.mu.Lock()
	unreachable := s.destinationReachable
	s.mu.Unlock()

	if unreachable == nil {
		return
	}

	for i := range dests {
		dests[i].Unreachable = unreachable(dests[i].Address)
	}
}
