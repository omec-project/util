// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

// The record definitions in this package carry their ASN.1 restrictions in their type
// comments — `OCTET STRING (SIZE(4))`, `INTEGER (0..255)`, `NumericString (SIZE(6..15))` — and
// this file is where those restrictions are enforced. EncodeXIRI refuses a record whose values
// violate their own definitions instead of encoding it: a conformant mediation function would
// discard such a record, and because the delivery itself would succeed, no fault would be raised
// on either side.
//
// The checks sit on the *path* every record crosses rather than beside each builder, so a record
// added later inherits them. They are keyed by **type** rather than by field name for the same
// reason: a named type is inherited and a field name is not, so a record that reuses `UEPolicy`
// or `PDUSessionID` gets the check without anybody remembering to add it, and a leaf that grows
// a new named type is visibly missing from the tables below rather than silently unchecked.
//
// Every restricted leaf of an emitted record has a named type. A restriction on a leaf declared
// as a bare `[]byte`, `int` or `string` would not be enforced, which is why
// TestEveryRestrictedLeafIsNamed refuses such a field.

// octetSize is an OCTET STRING's permitted length range, inclusive. A single permitted length
// is expressed as min == max.
type octetSize struct {
	min, max int
}

// intRange is an INTEGER's permitted value range, inclusive.
type intRange struct {
	min, max int64
}

// utf8Size is a UTF8String's permitted length range in characters, inclusive.
type utf8Size struct {
	min, max int
}

// octetConstraints, intConstraints and utf8Constraints are the restrictions the type
// definitions in iri.go record in their comments, in the one place that checks them.
//
// Every entry names the clause it comes from by naming the type: the comment beside each type
// declaration in iri.go is the source, and the two are meant to be read together.
var (
	octetConstraints = map[reflect.Type]octetSize{
		// The UEEndpointAddress CHOICE arms, which every record describing a session's
		// endpoint carries.
		reflect.TypeOf(IPv4Address(nil)): {4, 4},
		reflect.TypeOf(IPv6Address(nil)): {16, 16},
		reflect.TypeOf(MACAddress(nil)):  {6, 6},
		reflect.TypeOf(UEPolicy(nil)):    {16, 65540},

		// The SNSSAI and service-accept leaves the module declares inline, so this package
		// names them. Both are single permitted lengths, and a single length is the case a
		// check catches most often: a builder handed three octets of slice differentiator
		// where it meant to send the SST has nothing else to trip over.
		reflect.TypeOf(SliceDifferentiator(nil)): {3, 3},
		reflect.TypeOf(ServiceType(nil)):         {1, 1},

		// The two ServiceMessageIdentity alternatives, which the module leaves unconstrained.
		// The bound is table 6.2.2.2.13-1's instead — "Encoding per TS 24.501 clause 9.7",
		// whose message type is one octet — and these two are the only entries here the module
		// does not supply, which TestEveryConstraintBoundIsEnforced names as such.
		reflect.TypeOf(ServiceRequestIdentity(nil)): {1, 1},
		reflect.TypeOf(ServiceAcceptIdentity(nil)):  {1, 1},

		// The tracking-area code of a TAI. homeNetworkPublicKeyID and schemeOutput are
		// OCTET STRINGs the module leaves unconstrained, so they are deliberately absent.
		reflect.TypeOf(TAC(nil)): {2, 3},
	}

	intConstraints = map[reflect.Type]intRange{
		reflect.TypeOf(PDUSessionID(0)): {0, 255},
		reflect.TypeOf(FiveGMMCause(0)): {0, 255},
		reflect.TypeOf(FiveGSMCause(0)): {0, 255},
		reflect.TypeOf(AMFUENGAPID(0)):  {0, 1099511627775},
		reflect.TypeOf(RANUENGAPID(0)):  {0, 4294967295},

		reflect.TypeOf(SliceServiceType(0)): {0, 255},
		reflect.TypeOf(TEID(0)):             {0, 4294967295},

		// The FiveGGUTI members. Each is a named type in the module with a range of its own,
		// and each comes from this element's configuration and context — so nothing stands
		// between a misconfigured PLMN or a truncated AMF identifier and a record.
		reflect.TypeOf(AMFRegionID(0)): {0, 255},
		reflect.TypeOf(AMFSetID(0)):    {0, 1023},
		reflect.TypeOf(AMFPointer(0)):  {0, 63},
		reflect.TypeOf(FiveGTMSI(0)):   {0, 4294967295},

		// The SUCI members with a range. These arrive from a NAS message the UE sent, so
		// nothing has looked at them before they reach a record — a malformed SUCI is a
		// wrong target identity, which is the failure this table exists to make loud.
		reflect.TypeOf(RoutingIndicator(0)):       {0, 9999},
		reflect.TypeOf(ProtectionSchemeID(0)):     {0, 15},
		reflect.TypeOf(SUPIType(0)):               {0, 7},
		reflect.TypeOf(RoutingIndicatorLength(0)): {1, 4},
	}

	utf8Constraints = map[reflect.Type]utf8Size{
		reflect.TypeOf(LCSCorrelationID("")): {1, 255},

		// The network identifier of a stand-alone non-public network, carried by TAI and
		// SMFServingNetwork. homeNetworkIdentifier is a UTF8String the module leaves
		// unconstrained in size, so it is in utf8Strings and not here.
		reflect.TypeOf(NID("")): {11, 11},
	}

	// utf8Strings are every UTF8String leaf of an emitted record. Their content must be valid
	// UTF-8: a strict decoder refuses a UTF8String that is not, and a DNN or NAI reaches a record
	// from the UE or the session context without anything else having looked at it.
	//
	// A mandatory one must not be empty either. The module bounds the size of only NID and
	// lcsCorrelationId (utf8Constraints), so an empty DNN or NAI is schema-valid — but it names
	// nothing: a PDU session always has a data network, and a SUPI or GPSI with no characters
	// identifies no subscriber.
	utf8Strings = map[reflect.Type]bool{
		reflect.TypeOf(NAI("")):                   true,
		reflect.TypeOf(DNN("")):                   true,
		reflect.TypeOf(LCSCorrelationID("")):      true,
		reflect.TypeOf(HomeNetworkIdentifier("")): true,
		reflect.TypeOf(NID("")):                   true,
	}

	// numericConstraints are the NumericString leaves, whose restriction is a length *and* an
	// alphabet.
	//
	// The alphabet is the half worth having: a length check on an MCC would pass "abc", and a
	// PLMN read from configuration is exactly where a stray character arrives. NumericString
	// also admits a space, but an MCC or MNC is decimal digits throughout (TS 23.003), so only
	// digits are accepted.
	numericConstraints = map[reflect.Type]utf8Size{
		reflect.TypeOf(MCC("")): {3, 3},
		reflect.TypeOf(MNC("")): {2, 3},
	}

	// identityConstraints are the target-identity leaves: the SUPI, PEI and GPSI alternatives
	// every record carries, at the length the module gives each. The module defines them as
	// NumericString, which also admits a space; an identity contains none, so only digits are
	// accepted.
	//
	// They are checked here because a record's SUPI, PEI and GPSI come from the network
	// function's own UE or session context rather than from an X1 task, and that context holds
	// them in TS 29.571 form ("imei-" followed by fifteen digits, check digit included). A value
	// that violates its own type names the wrong subject, or none, and a conformant mediation
	// function discards the record.
	//
	// For a mandatory member, empty is never absence here. These types appear only as an
	// alternative of a CHOICE, where absence is a nil member, so an empty alternative in a
	// mandatory member is a present identity with no digits. An OPTIONAL member holding an
	// empty alternative is omitted by the emitter (see present in emit.go), and the walk skips
	// it the same way.
	identityConstraints = map[reflect.Type]utf8Size{
		reflect.TypeOf(IMSI("")):   {6, 15},
		reflect.TypeOf(IMEI("")):   {14, 14},
		reflect.TypeOf(IMEISV("")): {16, 16},
		reflect.TypeOf(MSISDN("")): {1, 15},
	}

	// nonEmpty are the types the module lets a mandatory member hold empty, and for which an
	// empty value is refused anyway, each with what an empty one would mean. The module's
	// SIZE is not the only statement of what a field carries: the payload tables describe
	// it too, and an empty value where they describe something carried is a value the element lost.
	//
	// Checked only where the member is mandatory: an absent optional one is omitted.
	nonEmpty = map[reflect.Type]string{
		// Both its members are OPTIONAL, so the empty SEQUENCE is schema-valid, and the three
		// records carrying it make it mandatory as the identifiers associated with the target
		// UE — "including the target identifier" in tables 6.2.2.2.9.2-1 and 6.2.2.2.13-1.
		// Empty, the record is attributable to nobody.
		reflect.TypeOf(UserIdentifiers{}): "names no identity, and the payload tables make it the " +
			"identifiers associated with the target UE",
		// Every member of Location is OPTIONAL, so the empty SEQUENCE is schema-valid, and the
		// two emitted records carrying it make it mandatory as the location the network
		// determined — tables 6.2.2.2.4-1 and 6.2.2.2.7-1. Empty, it reports no location at
		// all, which is not what either table describes.
		reflect.TypeOf(Location{}): "reports no location, and the payload tables make it the " +
			"location information the network holds for the target UE",
		// Not the RAN transparent containers. They copy what the RAN sent, and TS 38.413 sets
		// no minimum length on them, so an empty one is the RAN's value rather than one the
		// element lost — gnbsim rel-2.3.0 sends a zero-length Target to Source Transparent
		// Container. Refusing it would drop a handover record the module accepts.
	}

	// listMinimums are the SEQUENCE OF members with a SIZE lower bound, keyed by record and
	// field. A present list shorter than this is schema-invalid however well-formed its
	// elements are.
	listMinimums = map[string]int{
		// fiveGSSubscriberID [1] SEQUENCE SIZE(1..MAX) OF FiveGSSubscriberID. Identifiers()
		// never builds an empty list, but a hand-built FiveGSSubscriberIDs can.
		"FiveGSSubscriberIDs.IDs": 1,
	}

	// mandatoryEnums are the enumerated types for which zero is *wrong* rather than absent.
	//
	// The check below exempts zero everywhere else, and correctly: an unset optional member
	// reads as zero in Go and the emitter omits it, so refusing zero would refuse every record
	// that legitimately leaves one out.
	//
	// For a mandatory member that exemption would hide the one value that matters. Every
	// enumeration here is numbered from one, while a neighbouring protocol's enumeration is
	// often numbered from zero — NGAP numbers handoverType and the cause groups from zero — so a
	// value cast straight across instead of mapped arrives as zero exactly when its first member
	// was meant. A mandatory member carrying zero is a record a conformant decoder refuses
	// outright, not one with a wrong value.
	//
	// A type belongs here when it is mandatory in every record this package emits that carries
	// it, so zero can only mean the element wrote a value its own definition does not have.
	// Being wrong in this direction costs a record the receiver would have refused anyway; being
	// wrong in the other costs every record that omits an optional member, which is why the
	// default stays the exemption. TestEveryMandatoryEnumeratedFieldRefusesZero derives the
	// required entries from the module and holds this map, and mandatoryEnumFields below, to
	// them.
	mandatoryEnums = map[reflect.Type]bool{
		reflect.TypeOf(HandoverType(0)): true,

		// The HandoverCause arms. A cause is mandatory in both records that carry one, and the
		// mapping that produces it cannot yield zero — so a zero here is the mapping having
		// been bypassed, which is precisely the defect this guard exists to notice.
		reflect.TypeOf(CauseRadioNetwork(0)): true,
		reflect.TypeOf(CauseTransport(0)):    true,
		reflect.TypeOf(CauseNas(0)):          true,
		reflect.TypeOf(CauseProtocol(0)):     true,
		reflect.TypeOf(CauseMisc(0)):         true,

		// The six that are mandatory in every record this package emits that carries them, so
		// the type is exactly the right key: a record added later that reuses one inherits the
		// guard without anybody remembering it. Each names the record and the
		// module's lowest defined value.
		//
		// AMFDirection ::= ENUMERATED { networkInitiated(1), uEInitiated(2) } —
		// AMFDeregistration/deregistrationDirection.
		reflect.TypeOf(AMFDirection(0)): true,
		// AMFRegistrationResult, lowest threeGPPAccess(1) — AMFRegistration and
		// AMFStartOfInterceptionWithRegisteredUE, mandatory in both. The module also gives it
		// to AMFUEConfigurationUpdate as OPTIONAL, which this package does not emit; modelling
		// that record moves this entry to mandatoryEnumFields, and the sweep says so.
		reflect.TypeOf(AMFRegistrationResult(0)): true,
		// AMFFailedProcedureType, lowest registration(1) —
		// AMFUnsuccessfulProcedure/failedProcedureType.
		reflect.TypeOf(AMFFailedProcedureType(0)): true,
		// SMFFailedProcedureType, lowest pDUSessionEstablishment(1) —
		// SMFUnsuccessfulProcedure/failedProcedureType.
		reflect.TypeOf(SMFFailedProcedureType(0)): true,
		// PDUSessionType, lowest iPv4(1) — SMFPDUSessionEstablishment and
		// SMFStartOfInterceptionWithEstablishedPDUSession. An SMF builds it by casting the NAS
		// value, which TS 24.501 numbers identically; the SMF asserts that correspondence where
		// it builds the record.
		reflect.TypeOf(PDUSessionType(0)): true,
		// Initiator, lowest uE(1) — SMFUnsuccessfulProcedure/initiator.
		reflect.TypeOf(Initiator(0)): true,
	}

	// mandatoryEnumFields are the enumerated fields for which zero is wrong that mandatoryEnums
	// cannot express, keyed by the record and the field rather than by the type.
	//
	// **The type-keyed map is not a weaker version of this one — it is the stronger one where it
	// applies**, for the reason the file comment gives: a named type is inherited and a field
	// name is not, so a record added later that reuses `PDUSessionType` gets the guard without
	// anybody remembering it. What it cannot express is a type the module makes mandatory in one
	// record and OPTIONAL in another.
	//
	// That distinction matters because validateConstraints runs *before* the emitter. It walks
	// the whole record and sees an unset optional member as zero, before the emitter would have
	// omitted it — so a type in mandatoryEnums refuses zero in every record carrying that type,
	// including one where zero is how absence is spelled. `AccessType` is optional in four SMF
	// records and mandatory in one; guarding it by type would refuse those four their absence.
	//
	// Derived from the module and asserted against it by
	// TestEveryMandatoryEnumeratedFieldRefusesZero, in both directions: an entry naming a field
	// the module marks OPTIONAL fails, and so does a mandatory field named by neither map.
	mandatoryEnumFields = map[string]bool{
		// AccessType, lowest threeGPPAccess(1). Mandatory in AMFDeregistration (table
		// 6.2.2.2.3-1, cardinality 1) and OPTIONAL in the four SMF records that carry it.
		//
		// This is the one of the three where zero is a *reachable* value rather than a
		// hypothetical: an AMF that maps a NAS access type it does not recognise to zero, and
		// relies on its caller to substitute the access the message arrived on, emits a
		// mandatory zero from any caller that skips that step. The guard makes that a refusal
		// here rather than a discard at the far end.
		"AMFDeregistration.AccessType": true,

		// FiveGSMRequestType, lowest initialRequest(1). Mandatory in the three session
		// records and OPTIONAL in SMFUnsuccessfulProcedure, where the SMF reports it when it
		// knows the request type and omits it otherwise.
		//
		// SMFPDUSessionModification is a module-versus-payload-table discrepancy, recorded in
		// CONFORMANCE.md: `TS33128Payloads.asn` gives requestType no OPTIONAL, and table
		// 6.2.3-2 marks only gTPTunnelInfo M. The module is what a receiver validates
		// against, so the module is what this follows.
		"SMFPDUSessionEstablishment.RequestType":                      true,
		"SMFPDUSessionModification.RequestType":                       true,
		"SMFStartOfInterceptionWithEstablishedPDUSession.RequestType": true,

		// AMFRegistrationType, lowest initial(1). Mandatory in AMFRegistration and OPTIONAL in
		// AMFStartOfInterceptionWithRegisteredUE — where the AMF may be activating interception
		// for a UE whose registration it never saw, so it has no registration type to report.
		"AMFRegistration.RegistrationType": true,
	}

	// enumConstraints are the ENUMERATED types whose permitted values this module declares.
	//
	// Separate from intConstraints because the refusal has to say something different: an
	// integer outside its range is a value too large, and an enumeration outside its set is a
	// value that *means nothing* — no conformant receiver can interpret it, and there is no
	// nearest legal value it could have intended.
	//
	// The bounds are transcribed by hand from `testdata/asn1/TS33128Payloads.asn` and checked
	// against it, with the values a range admits and the module leaves out, by
	// TestEnumBoundsMatchTheModule. Each bound is the module's, so a value the module defines is
	// never refused for being one iri.go does not name.
	//
	// Reachable from peer input, which is why it matters. An AMF builds a handover record from
	// the cause and handover type in the NGAP message the gNB sent, so a RAN that is
	// non-conformant — or is not the RAN it claims to be — decides what this element encodes.
	// Unchecked, the record goes out structurally well-formed, a conformant mediation function
	// discards what it cannot validate, and the element believes it delivered.
	enumConstraints = map[reflect.Type]intRange{
		reflect.TypeOf(AMFRegistrationType(0)):    {1, 7},
		reflect.TypeOf(AMFRegistrationResult(0)):  {1, 3},
		reflect.TypeOf(AMFDirection(0)):           {1, 2},
		reflect.TypeOf(AccessType(0)):             {1, 3},
		reflect.TypeOf(PDUSessionType(0)):         {1, 5},
		reflect.TypeOf(FiveGSMRequestType(0)):     {1, 7},
		reflect.TypeOf(AMFFailedProcedureType(0)): {1, 3},
		reflect.TypeOf(SMFFailedProcedureType(0)): {1, 3},
		reflect.TypeOf(Initiator(0)):              {1, 3},
		reflect.TypeOf(HandoverType(0)):           {1, 4},
		// RATType, nR(1) .. nREREDCAP(33). iri.go names only the arms a 5G core reports.
		reflect.TypeOf(RATType(0)): {1, 33},

		// The five HandoverCause arms.
		//
		// Each mirrors an NGAP Cause group, and the two number the same concepts differently —
		// so the values here are TS 33.128's. An element that reads a cause from NGAP is
		// responsible for mapping it into this vocabulary before building the record; see
		// causevalues.go, which names the values it maps to.
		//
		// `CauseRadioNetwork` runs 1..52 with 28 absent; enumHoles refuses the 28.
		reflect.TypeOf(CauseRadioNetwork(0)): {1, 52},
		reflect.TypeOf(CauseTransport(0)):    {1, 2},
		reflect.TypeOf(CauseNas(0)):          {1, 4},
		reflect.TypeOf(CauseProtocol(0)):     {1, 7},
		reflect.TypeOf(CauseMisc(0)):         {1, 6},
	}

	// enumHoles are the values inside an enumConstraints range that the module's ENUMERATED
	// does not define. A range cannot express a gap, and a value in one means nothing to a
	// receiver exactly as a value past the end does. TestEnumBoundsMatchTheModule derives the
	// holes from the module and holds this table to them.
	enumHoles = map[reflect.Type]map[int64]bool{
		// CauseRadioNetwork defines no 28: the position NGAP's unknown-qos-flow-id would take,
		// which TS 33.128 does not list.
		reflect.TypeOf(CauseRadioNetwork(0)): {28: true},
	}
)

// validateConstraints walks a record and refuses any value that violates the restriction its
// own type carries.
//
// A reflective walk rather than a check per record, for the reason the file comment gives:
// what must not be possible is adding a record that skips the check. It runs once per record
// on the X2 path, which is per signalling event and not per packet.
func validateConstraints(event any) error {
	if event == nil {
		return missing("the XIRIEvent")
	}

	return walkConstrained(reflect.ValueOf(event), typeName(reflect.TypeOf(event)), false, true)
}

// typeName is how a refusal names a record: its bare name, as the emitter's own refusals do,
// with a leading `*` for a pointer so a refused pointer says what it was.
func typeName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer && t.Elem().Name() != "" {
		return "*" + t.Elem().Name()
	}
	if t.Name() != "" {
		return t.Name()
	}

	return t.String()
}

// sizeText renders a SIZE constraint as the module writes it: SIZE(14) for a single permitted
// length, SIZE(6..15) for a range.
func sizeText(lo, hi int) string {
	if lo == hi {
		return fmt.Sprintf("SIZE(%d)", lo)
	}

	return fmt.Sprintf("SIZE(%d..%d)", lo, hi)
}

// walkConstrained descends v, applying whatever constraint its type carries and then
// recursing into its members. path names where a failure was found, so a refusal says which
// field of which record rather than only that something was wrong.
//
// zeroForbidden is what the parent struct decided about *this* field: mandatoryEnumFields
// keyed by the enclosing type's name and the field's, for the enumerations whose meaning
// depends on which record carries them. It is a parameter rather than a lookup inside
// constraintOf because a value does not know which field it came from — which is the same
// reason the type-keyed tables cannot express these three.
//
// mandatory is whether this value must be present: its field carries no `optional` in its
// asn1 tag, or it is an element of a list, or it is the record itself. **It is what decides
// whether an empty value is absence or a defect.** The emitter omits an optional member that
// is nil, empty or zero, so for those an empty value really is "not present"; it writes a
// mandatory member unconditionally, so an empty one would go out as a zero-length value its
// type forbids, and is refused here instead.
func walkConstrained(v reflect.Value, path string, zeroForbidden, mandatory bool) error {
	if !v.IsValid() {
		return nil
	}

	if err := constraintOf(v, path, zeroForbidden, mandatory); err != nil {
		return err
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		// An optional CHOICE holding its alternative's zero is omitted by the emitter (see
		// present in emit.go), so it is absent and has nothing to check.
		if v.Kind() == reflect.Interface && !mandatory && v.Elem().IsZero() {
			return nil
		}

		// The flags carry through: a pointer expresses presence, so what the field said
		// about its value still applies to the value behind it.
		return walkConstrained(v.Elem(), path, zeroForbidden, mandatory)

	case reflect.Struct:
		// An optional structure at its zero value is omitted (isZero in emit.go), so its
		// members are not present and their own mandatoriness does not apply.
		if !mandatory && v.IsZero() {
			return nil
		}
		t := v.Type()
		for i := range v.NumField() {
			sf := t.Field(i)
			if !sf.IsExported() {
				continue
			}
			key := t.Name() + "." + sf.Name
			field := v.Field(i)
			if least, ok := listMinimums[key]; ok && field.Kind() == reflect.Slice &&
				!field.IsNil() && field.Len() < least {
				return fmt.Errorf("iri: %s.%s has %d entries, and is defined as SEQUENCE SIZE(%d..MAX)",
					path, sf.Name, field.Len(), least)
			}
			if err := walkConstrained(field, path+"."+sf.Name,
				mandatoryEnumFields[key], !isOptional(sf)); err != nil {
				return err
			}
		}

	case reflect.Slice, reflect.Array:
		// A byte slice is a leaf: its constraint is its length, already applied above, and
		// descending into it would check each octet as an integer.
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		// An element of a list is present by being in it.
		for i := range v.Len() {
			if err := walkConstrained(v.Index(i), fmt.Sprintf("%s[%d]", path, i),
				zeroForbidden, true); err != nil {
				return err
			}
		}
	}

	return nil
}

// isOptional reports whether a record field is OPTIONAL, read from its asn1 struct tag —
// the same tag the drift tests hold against the module.
func isOptional(sf reflect.StructField) bool {
	for _, opt := range strings.Split(sf.Tag.Get("asn1"), ",") {
		if opt == "optional" {
			return true
		}
	}

	return false
}

// constraintOf applies the restriction v's own type carries, if it has one. zeroForbidden is
// the enclosing field's verdict on zero, and mandatory whether the value must be present; see
// walkConstrained.
func constraintOf(v reflect.Value, path string, zeroForbidden, mandatory bool) error {
	t := v.Type()

	if why, ok := nonEmpty[t]; ok && mandatory {
		if v.IsZero() || (v.Kind() == reflect.Slice && v.Len() == 0) {
			return fmt.Errorf("iri: %s %s", path, why)
		}
	}

	if c, ok := octetConstraints[t]; ok {
		n := v.Len()
		if n == 0 {
			// An absent OPTIONAL octet string is a nil slice, which the emitter omits. A
			// non-nil empty one it writes, as a present value too short for its type; and a
			// mandatory one it writes whatever it holds.
			if !mandatory && v.IsNil() {
				return nil
			}
			return fmt.Errorf("iri: %s carries no octets, and %s is defined as %s",
				path, t.Name(), sizeText(c.min, c.max))
		}
		if n < c.min || n > c.max {
			return fmt.Errorf("iri: %s is %d octets, and %s is defined as %s",
				path, n, t.Name(), sizeText(c.min, c.max))
		}
	}

	if c, ok := intConstraints[t]; ok {
		n := v.Int()
		if n < c.min || n > c.max {
			return fmt.Errorf("iri: %s is %d, and %s is defined as INTEGER (%d..%d)",
				path, n, t.Name(), c.min, c.max)
		}
	}

	if c, ok := enumConstraints[t]; ok {
		n := v.Int()
		// Zero is "not present", not "present and meaningless" — the same rule the octet
		// strings above take, for the same reason. Every enumeration here is numbered from
		// one, so an unset field of an optional member reads as zero and the codec omits it;
		// refusing that would refuse every record that legitimately leaves the member out.
		//
		// **Except where the member is mandatory**, in which case zero cannot be an omission:
		// it is a peer's enumeration numbered from zero, cast across with its *first* member,
		// and the paragraph beside mandatoryEnums says why that is exactly the value to refuse.
		//
		// Two maps rather than one because the exception has two shapes: a type mandatory
		// wherever it appears, and a field mandatory in this record and optional in the next.
		// See mandatoryEnumFields for why the second cannot be folded into the first.
		//
		// The walk's mandatory flag says the same thing for every member, and is deliberately
		// not consulted here. The flag is read from the struct tags; the two maps are held
		// against the module itself by TestEveryMandatoryEnumeratedFieldRefusesZero. Deciding
		// from the maps keeps the guard on an enumerated member correct even if a struct tag
		// were wrongly marked optional, which is the one mistake that would otherwise reopen
		// it silently.
		if n == 0 && !mandatoryEnums[t] && !zeroForbidden {
			return nil
		}
		if n < c.min || n > c.max {
			return fmt.Errorf("iri: %s is %d, and %s is an ENUMERATED with values %d..%d",
				path, n, t.Name(), c.min, c.max)
		}
		if enumHoles[t][n] {
			return fmt.Errorf("iri: %s is %d, and %s defines no value %d", path, n, t.Name(), n)
		}
	}

	if c, ok := numericConstraints[t]; ok {
		s := v.String()
		if s == "" && mandatory {
			return fmt.Errorf("iri: %s is empty, and %s is defined as NumericString (%s)",
				path, t.Name(), sizeText(c.min, c.max))
		}
		// Empty and optional is absence: the emitter omits it.
		if s != "" {
			if n := len([]rune(s)); n < c.min || n > c.max {
				return fmt.Errorf("iri: %s is %d characters, and %s is defined as NumericString (%s)",
					path, n, t.Name(), sizeText(c.min, c.max))
			}
			for _, r := range s {
				if r < '0' || r > '9' {
					return fmt.Errorf("iri: %s contains %q, and %s is decimal digits throughout",
						path, r, t.Name())
				}
			}
		}
	}

	if c, ok := identityConstraints[t]; ok {
		s := v.String()
		if n := len([]rune(s)); n < c.min || n > c.max {
			return fmt.Errorf("iri: %s is %d characters, and %s is defined as NumericString (%s)",
				path, n, t.Name(), sizeText(c.min, c.max))
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return fmt.Errorf("iri: %s contains %q, and %s is an identity of digits only", path, r, t.Name())
			}
		}
	}

	if utf8Strings[t] {
		s := v.String()
		if s == "" && mandatory {
			if c, sized := utf8Constraints[t]; sized {
				return fmt.Errorf("iri: %s is empty, and %s is defined as UTF8String (%s)",
					path, t.Name(), sizeText(c.min, c.max))
			}
			return fmt.Errorf("iri: %s is empty, and %s is mandatory here: an empty UTF8String "+
				"names nothing", path, t.Name())
		}
		if !utf8.ValidString(s) {
			return fmt.Errorf("iri: %s is not valid UTF-8, and %s is defined as UTF8String",
				path, t.Name())
		}
	}

	if c, ok := utf8Constraints[t]; ok {
		// Empty: refused above when mandatory; absence when optional, which the emitter omits.
		if s := v.String(); s != "" {
			if n := len([]rune(s)); n < c.min || n > c.max {
				return fmt.Errorf("iri: %s is %d characters, and %s is defined as UTF8String (%s)",
					path, n, t.Name(), sizeText(c.min, c.max))
			}
		}
	}

	return nil
}
