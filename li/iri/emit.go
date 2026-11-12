// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"encoding/asn1"
	"fmt"
	"reflect"
)

// This file is the xIRI encoder: a small TLV layer over the standard library's
// encoding/asn1, and one emit method per structure, each told its fields' tags and
// presence rather than inferring them by reflection.
//
// The record structs in iri.go keep their `asn1:"..."` tags. Nothing here reads them;
// they are the declarative record of the module, audited against the published ASN.1
// by asn1_drift_test.go, and TestASN1EmittedTagsMatchDeclarations holds every emit method
// below to them. A tag changed in one place and not the other fails there.
//
// **Presence is stated at each call site.** A mandatory member is always written. An
// OPTIONAL one is written under the condition beside it, which for most fields is that
// it differs from its type's zero. The pointer-typed members are the exception — present
// exactly when non-nil, so a present false or zero is expressible.

// builder accumulates the encoding of a constructed value's contents. The first error
// sticks, and every later write is a no-op, so an emit method reads as the field list
// it encodes rather than as error handling.
type builder struct {
	buf []byte
	err error
}

func (b *builder) fail(err error) {
	if b.err == nil {
		b.err = err
	}
}

func (b *builder) add(der []byte, err error) {
	if b.err != nil {
		return
	}
	if err != nil {
		b.err = err
		return
	}
	b.buf = append(b.buf, der...)
}

// integer writes [tag] IMPLICIT INTEGER. ENUMERATED members go through here too: under
// an implicit context tag only the contents octets remain, and those are the same.
func (b *builder) integer(tag int, v int64) {
	b.add(asn1.MarshalWithParams(v, fmt.Sprintf("tag:%d", tag)))
}

// boolean writes [tag] IMPLICIT BOOLEAN.
func (b *builder) boolean(tag int, v bool) {
	b.add(asn1.MarshalWithParams(v, fmt.Sprintf("tag:%d", tag)))
}

// octets writes [tag] IMPLICIT OCTET STRING, or any other string type: under an
// implicit tag the contents are the bytes as given.
func (b *builder) octets(tag int, v []byte) {
	b.add(asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: tag, Bytes: v}))
}

// text writes a string-typed member as its bytes. Validation is not this function's: the
// module's string types — their sizes, their alphabets and, for a UTF8String, that the
// content is valid UTF-8 — are checked in constraints.go, which runs before any emitter.
func (b *builder) text(tag int, v string) {
	b.octets(tag, []byte(v))
}

// constructed writes [tag] around whatever build writes. It is both an IMPLICIT-tagged
// SEQUENCE, whose contents are its members, and an EXPLICIT tag, whose contents are one
// complete element — the two have the same form on the wire.
func (b *builder) constructed(tag int, build func(*builder)) {
	b.wrap(asn1.ClassContextSpecific, tag, build)
}

// sequence writes a universal SEQUENCE around whatever build writes.
func (b *builder) sequence(build func(*builder)) {
	b.wrap(asn1.ClassUniversal, asn1.TagSequence, build)
}

func (b *builder) wrap(class, tag int, build func(*builder)) {
	if b.err != nil {
		return
	}
	var inner builder
	build(&inner)
	if inner.err != nil {
		b.err = inner.err
		return
	}
	b.add(asn1.Marshal(asn1.RawValue{Class: class, Tag: tag, IsCompound: true, Bytes: inner.buf}))
}

// isZero reports whether an OPTIONAL structure is at its zero value, which is when it
// is omitted.
func isZero[T any](v T) bool {
	return reflect.ValueOf(&v).Elem().IsZero()
}

// present reports whether an OPTIONAL CHOICE member is written: it holds an
// alternative, and that alternative is not its type's zero.
func present(v any) bool {
	return v != nil && !reflect.ValueOf(v).IsZero()
}

func missing(field string) error {
	return fmt.Errorf("iri: %s is mandatory and carries no value", field)
}

func notAnAlternative(choice, field string, v any) error {
	return fmt.Errorf("iri: %s holds %T, which is not an alternative of the %s CHOICE", field, v, choice)
}

// --- CHOICE alternatives ---
//
// A context tag on a CHOICE is always explicit, so each member that carries one is
// written as [tag] { alternative } — see explicit. The alternatives themselves are
// IMPLICIT-tagged.

// explicit writes [tag] { alternative }, refusing an absent mandatory member by name.
func (b *builder) explicit(tag int, field string, v any, alternative func(*builder, string, any)) {
	if v == nil {
		b.fail(missing(field))
		return
	}
	b.constructed(tag, func(c *builder) { alternative(c, field, v) })
}

// supi: SUPI ::= CHOICE { iMSI [1] IMSI, nAI [2] NAI }.
func supi(b *builder, field string, v any) {
	switch v := v.(type) {
	case IMSI:
		b.text(1, string(v))
	case NAI:
		b.text(2, string(v))
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("SUPI", field, v))
	}
}

// pei: PEI ::= CHOICE { iMEI [1] IMEI, iMEISV [2] IMEISV }.
func pei(b *builder, field string, v any) {
	switch v := v.(type) {
	case IMEI:
		b.text(1, string(v))
	case IMEISV:
		b.text(2, string(v))
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("PEI", field, v))
	}
}

// gpsi: GPSI ::= CHOICE { mSISDN [1] MSISDN, nAI [2] NAI }.
func gpsi(b *builder, field string, v any) {
	switch v := v.(type) {
	case MSISDN:
		b.text(1, string(v))
	case NAI:
		b.text(2, string(v))
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("GPSI", field, v))
	}
}

// ueEndpointAddress: UEEndpointAddress ::= CHOICE { iPv4Address [1], iPv6Address [2],
// ethernetAddress [3] }.
func ueEndpointAddress(b *builder, field string, v any) {
	switch v := v.(type) {
	case IPv4Address:
		b.octets(1, v)
	case IPv6Address:
		b.octets(2, v)
	case MACAddress:
		b.octets(3, v)
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("UEEndpointAddress", field, v))
	}
}

// amfFailureCause: AMFFailureCause ::= CHOICE { fiveGMMCause [1], fiveGSMCause [2] }.
func amfFailureCause(b *builder, field string, v any) {
	switch v := v.(type) {
	case FiveGMMCause:
		b.integer(1, int64(v))
	case FiveGSMCause:
		b.integer(2, int64(v))
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("AMFFailureCause", field, v))
	}
}

// fiveGSSubscriberID: FiveGSSubscriberID ::= CHOICE { sUPI [1] SUPI, sUCI [2] SUCI,
// pEI [3] PEI, gPSI [4] GPSI }. Each modelled arm is itself a CHOICE, so its tag is
// explicit: [n] { [m] leaf }.
func fiveGSSubscriberID(b *builder, field string, v any) {
	switch v := v.(type) {
	case SubscriberSUPI:
		b.constructed(1, func(c *builder) { supi(c, field+".Value", v.Value) })
	case SubscriberPEI:
		b.constructed(3, func(c *builder) { pei(c, field+".Value", v.Value) })
	case SubscriberGPSI:
		b.constructed(4, func(c *builder) { gpsi(c, field+".Value", v.Value) })
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("FiveGSSubscriberID", field, v))
	}
}

// serviceMessageIdentity: ServiceMessageIdentity ::= CHOICE { serviceRequest [1],
// serviceAccept [2] }.
func serviceMessageIdentity(b *builder, field string, v any) {
	switch v := v.(type) {
	case ServiceRequestIdentity:
		b.octets(1, v)
	case ServiceAcceptIdentity:
		b.octets(2, v)
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("ServiceMessageIdentity", field, v))
	}
}

// handoverCause: HandoverCause ::= CHOICE { radioNetwork [1], transport [2], nas [3],
// protocol [4], misc [5] }.
func handoverCause(b *builder, field string, v any) {
	switch v := v.(type) {
	case CauseRadioNetwork:
		b.integer(1, int64(v))
	case CauseTransport:
		b.integer(2, int64(v))
	case CauseNas:
		b.integer(3, int64(v))
	case CauseProtocol:
		b.integer(4, int64(v))
	case CauseMisc:
		b.integer(5, int64(v))
	case nil:
		b.fail(missing(field))
	default:
		b.fail(notAnAlternative("HandoverCause", field, v))
	}
}

// xiriEvent: XIRIEvent ::= CHOICE — the 17 records this package encodes. Tags above 30
// take the high-tag-number form, which encoding/asn1 writes.
func xiriEvent(b *builder, field string, v any) {
	switch v := v.(type) {
	case AMFRegistration:
		b.constructed(1, v.emit)
	case AMFDeregistration:
		b.constructed(2, v.emit)
	case AMFLocationUpdate:
		b.constructed(3, v.emit)
	case AMFStartOfInterceptionWithRegisteredUE:
		b.constructed(4, v.emit)
	case AMFUnsuccessfulProcedure:
		b.constructed(5, v.emit)
	case SMFPDUSessionEstablishment:
		b.constructed(6, v.emit)
	case SMFPDUSessionModification:
		b.constructed(7, v.emit)
	case SMFPDUSessionRelease:
		b.constructed(8, v.emit)
	case SMFStartOfInterceptionWithEstablishedPDUSession:
		b.constructed(9, v.emit)
	case SMFUnsuccessfulProcedure:
		b.constructed(10, v.emit)
	case AMFIdentifierAssociation:
		b.constructed(62, v.emit)
	case AMFPositioningInfoTransfer:
		b.constructed(111, v.emit)
	case AMFRANHandoverCommand:
		b.constructed(113, v.emit)
	case AMFRANHandoverRequest:
		b.constructed(114, v.emit)
	case AMFUEPolicyTransfer:
		b.constructed(146, v.emit)
	case AMFUEServiceAccept:
		b.constructed(147, v.emit)
	case AMFIdentifierDeassociation:
		b.constructed(186, v.emit)
	case nil:
		b.fail(missing(field))
	default:
		// A pointer to a record lands here too, and is refused rather than dereferenced:
		// the constraint walk and this switch are keyed on the value types.
		b.fail(notAnAlternative("XIRIEvent", field, v))
	}
}

// --- SEQUENCE OF ---

// sequenceOf writes [tag] { each element as alternative writes it }. An element cannot
// be absent: optionality belongs to the list.
func (b *builder) sequenceOf(tag int, field string, list []any, alternative func(*builder, string, any)) {
	b.constructed(tag, func(c *builder) {
		for i, v := range list {
			alternative(c, fmt.Sprintf("%s[%d]", field, i), v)
		}
	})
}

func (b *builder) taiList(tag int, list TAIList) {
	b.constructed(tag, func(c *builder) {
		for _, tai := range list {
			c.sequence(tai.emit)
		}
	})
}

// --- Nested structures ---

func (s SNSSAI) emit(b *builder) {
	b.integer(1, int64(s.SliceServiceType))
	if s.SliceDifferentiator != nil {
		b.octets(2, s.SliceDifferentiator)
	}
	if s.MappedHPLMNSST != 0 {
		b.integer(3, int64(s.MappedHPLMNSST))
	}
	if s.MappedHPLMNSD != nil {
		b.octets(4, s.MappedHPLMNSD)
	}
}

func (f FTEID) emit(b *builder) {
	b.integer(1, int64(f.TEID))
	if f.IPv4Address != nil {
		b.octets(2, f.IPv4Address)
	}
	if f.IPv6Address != nil {
		b.octets(3, f.IPv6Address)
	}
}

func (t FiveGSGTPTunnels) emit(b *builder) {
	if !isZero(t.ULNGUUPTunnelInformation) {
		b.constructed(1, t.ULNGUUPTunnelInformation.emit)
	}
}

func (g GTPTunnelInfo) emit(b *builder) {
	if !isZero(g.FiveGSGTPTunnels) {
		b.constructed(1, g.FiveGSGTPTunnels.emit)
	}
}

func (l Location) emit(b *builder) {
	if !isZero(l.LocationInfo) {
		b.constructed(1, l.LocationInfo.emit)
	}
}

func (l LocationInfo) emit(b *builder) {
	if l.CurrentLocation {
		b.boolean(2, l.CurrentLocation)
	}
}

func (f FiveGSSubscriberIDs) emit(b *builder) {
	b.sequenceOf(1, "FiveGSSubscriberIDs.IDs", f.IDs, fiveGSSubscriberID)
}

func (u UserIdentifiers) emit(b *builder) {
	if !isZero(u.FiveGS) {
		b.constructed(1, u.FiveGS.emit)
	}
}

func (p PDUSessionResourceInformation) emit(b *builder) {
	b.integer(1, int64(p.PDUSessionID))
}

func (g FiveGGUTI) emit(b *builder) {
	b.text(1, string(g.MCC))
	b.text(2, string(g.MNC))
	b.integer(3, int64(g.AMFRegionID))
	b.integer(4, int64(g.AMFSetID))
	b.integer(5, int64(g.AMFPointer))
	b.integer(6, int64(g.FiveGTMSI))
}

func (s SUCI) emit(b *builder) {
	b.text(1, string(s.MCC))
	b.text(2, string(s.MNC))
	b.integer(3, int64(s.RoutingIndicator))
	b.integer(4, int64(s.ProtectionSchemeID))
	b.octets(5, s.HomeNetworkPublicKeyID)
	b.octets(6, s.SchemeOutput)
	if s.RoutingIndicatorLength != nil {
		b.integer(7, int64(*s.RoutingIndicatorLength))
	}
	if s.SUPIType != nil {
		b.integer(8, int64(*s.SUPIType))
	}
	if s.HomeNetworkIdentifier != "" {
		b.text(9, string(s.HomeNetworkIdentifier))
	}
}

func (p PLMNID) emit(b *builder) {
	b.text(1, string(p.MCC))
	b.text(2, string(p.MNC))
}

func (t TAI) emit(b *builder) {
	b.constructed(1, t.PLMNID.emit)
	b.octets(2, t.TAC)
	if t.NID != "" {
		b.text(3, string(t.NID))
	}
}

func (s SMFServingNetwork) emit(b *builder) {
	b.constructed(1, s.PLMNID.emit)
	if s.NID != "" {
		b.text(2, string(s.NID))
	}
}

func (a AMFID) emit(b *builder) {
	b.integer(1, int64(a.AMFRegionID))
	b.integer(2, int64(a.AMFSetID))
	b.integer(3, int64(a.AMFPointer))
}

// --- Records ---

func (r AMFRegistration) emit(b *builder) {
	b.integer(1, int64(r.RegistrationType))
	b.integer(2, int64(r.RegistrationResult))
	b.explicit(4, "AMFRegistration.SUPI", r.SUPI, supi)
	if !isZero(r.SUCI) {
		b.constructed(5, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(6, "AMFRegistration.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(7, "AMFRegistration.GPSI", r.GPSI, gpsi)
	}
	b.constructed(8, r.GUTI.emit)
	if r.FiveGSTAIList != nil {
		b.taiList(11, r.FiveGSTAIList)
	}
	if r.RATType != 0 {
		b.integer(18, int64(r.RATType))
	}
}

func (r AMFDeregistration) emit(b *builder) {
	b.integer(1, int64(r.DeregistrationDirection))
	b.integer(2, int64(r.AccessType))
	if present(r.SUPI) {
		b.explicit(3, "AMFDeregistration.SUPI", r.SUPI, supi)
	}
	if !isZero(r.SUCI) {
		b.constructed(4, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(5, "AMFDeregistration.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(6, "AMFDeregistration.GPSI", r.GPSI, gpsi)
	}
	if !isZero(r.GUTI) {
		b.constructed(7, r.GUTI.emit)
	}
}

func (r AMFStartOfInterceptionWithRegisteredUE) emit(b *builder) {
	b.integer(1, int64(r.RegistrationResult))
	if r.RegistrationType != 0 {
		b.integer(2, int64(r.RegistrationType))
	}
	b.explicit(4, "AMFStartOfInterceptionWithRegisteredUE.SUPI", r.SUPI, supi)
	if !isZero(r.SUCI) {
		b.constructed(5, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(6, "AMFStartOfInterceptionWithRegisteredUE.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(7, "AMFStartOfInterceptionWithRegisteredUE.GPSI", r.GPSI, gpsi)
	}
	b.constructed(8, r.GUTI.emit)
	if r.FiveGSTAIList != nil {
		b.taiList(12, r.FiveGSTAIList)
	}
}

func (r SMFPDUSessionEstablishment) emit(b *builder) {
	if present(r.SUPI) {
		b.explicit(1, "SMFPDUSessionEstablishment.SUPI", r.SUPI, supi)
	}
	if r.SUPIUnauthenticated != nil {
		b.boolean(2, bool(*r.SUPIUnauthenticated))
	}
	if present(r.PEI) {
		b.explicit(3, "SMFPDUSessionEstablishment.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "SMFPDUSessionEstablishment.GPSI", r.GPSI, gpsi)
	}
	b.integer(5, int64(r.PDUSessionID))
	b.constructed(6, r.GTPTunnelID.emit)
	b.integer(7, int64(r.PDUSessionType))
	if !isZero(r.SNSSAI) {
		b.constructed(8, r.SNSSAI.emit)
	}
	if len(r.UEEndpoint) > 0 {
		b.sequenceOf(9, "SMFPDUSessionEstablishment.UEEndpoint", r.UEEndpoint, ueEndpointAddress)
	}
	b.text(12, string(r.DNN))
	if r.AMFID != nil {
		b.constructed(13, r.AMFID.emit)
	}
	b.integer(15, int64(r.RequestType))
	if r.AccessType != 0 {
		b.integer(16, int64(r.AccessType))
	}
	if r.RATType != 0 {
		b.integer(17, int64(r.RATType))
	}
	if !isZero(r.ServingNetwork) {
		b.constructed(22, r.ServingNetwork.emit)
	}
	if !isZero(r.GTPTunnelInfo) {
		b.constructed(25, r.GTPTunnelInfo.emit)
	}
}

func (r SMFPDUSessionModification) emit(b *builder) {
	if present(r.SUPI) {
		b.explicit(1, "SMFPDUSessionModification.SUPI", r.SUPI, supi)
	}
	if r.SUPIUnauthenticated != nil {
		b.boolean(2, bool(*r.SUPIUnauthenticated))
	}
	if present(r.PEI) {
		b.explicit(3, "SMFPDUSessionModification.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "SMFPDUSessionModification.GPSI", r.GPSI, gpsi)
	}
	if !isZero(r.SNSSAI) {
		b.constructed(5, r.SNSSAI.emit)
	}
	b.integer(8, int64(r.RequestType))
	if r.AccessType != 0 {
		b.integer(9, int64(r.AccessType))
	}
	if r.RATType != 0 {
		b.integer(10, int64(r.RATType))
	}
	if r.PDUSessionID != 0 {
		b.integer(11, int64(r.PDUSessionID))
	}
	if present(r.UEEndpoint) {
		b.explicit(13, "SMFPDUSessionModification.UEEndpoint", r.UEEndpoint, ueEndpointAddress)
	}
	if !isZero(r.ServingNetwork) {
		b.constructed(14, r.ServingNetwork.emit)
	}
	if !isZero(r.GTPTunnelInfo) {
		b.constructed(16, r.GTPTunnelInfo.emit)
	}
}

func (r SMFPDUSessionRelease) emit(b *builder) {
	b.explicit(1, "SMFPDUSessionRelease.SUPI", r.SUPI, supi)
	if present(r.PEI) {
		b.explicit(2, "SMFPDUSessionRelease.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(3, "SMFPDUSessionRelease.GPSI", r.GPSI, gpsi)
	}
	b.integer(4, int64(r.PDUSessionID))
	if r.UplinkVolume != nil {
		b.integer(7, *r.UplinkVolume)
	}
	if r.DownlinkVolume != nil {
		b.integer(8, *r.DownlinkVolume)
	}
}

func (r AMFLocationUpdate) emit(b *builder) {
	b.explicit(1, "AMFLocationUpdate.SUPI", r.SUPI, supi)
	if !isZero(r.SUCI) {
		b.constructed(2, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(3, "AMFLocationUpdate.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "AMFLocationUpdate.GPSI", r.GPSI, gpsi)
	}
	if !isZero(r.GUTI) {
		b.constructed(5, r.GUTI.emit)
	}
	b.constructed(6, r.Location.emit)
}

func (r AMFUnsuccessfulProcedure) emit(b *builder) {
	b.integer(1, int64(r.FailedProcedureType))
	b.explicit(2, "AMFUnsuccessfulProcedure.FailureCause", r.FailureCause, amfFailureCause)
	if present(r.SUPI) {
		b.explicit(4, "AMFUnsuccessfulProcedure.SUPI", r.SUPI, supi)
	}
	if !isZero(r.SUCI) {
		b.constructed(5, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(6, "AMFUnsuccessfulProcedure.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(7, "AMFUnsuccessfulProcedure.GPSI", r.GPSI, gpsi)
	}
	if !isZero(r.GUTI) {
		b.constructed(8, r.GUTI.emit)
	}
	if !isZero(r.Location) {
		b.constructed(9, r.Location.emit)
	}
}

func (r AMFIdentifierAssociation) emit(b *builder) {
	b.explicit(1, "AMFIdentifierAssociation.SUPI", r.SUPI, supi)
	if !isZero(r.SUCI) {
		b.constructed(2, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(3, "AMFIdentifierAssociation.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "AMFIdentifierAssociation.GPSI", r.GPSI, gpsi)
	}
	b.constructed(5, r.GUTI.emit)
	b.constructed(6, r.Location.emit)
	if r.FiveGSTAIList != nil {
		b.taiList(7, r.FiveGSTAIList)
	}
}

func (r AMFIdentifierDeassociation) emit(b *builder) {
	b.explicit(1, "AMFIdentifierDeassociation.SUPI", r.SUPI, supi)
	if !isZero(r.SUCI) {
		b.constructed(2, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(3, "AMFIdentifierDeassociation.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "AMFIdentifierDeassociation.GPSI", r.GPSI, gpsi)
	}
	b.constructed(5, r.GUTI.emit)
}

func (r SMFStartOfInterceptionWithEstablishedPDUSession) emit(b *builder) {
	if present(r.SUPI) {
		b.explicit(1, "SMFStartOfInterceptionWithEstablishedPDUSession.SUPI", r.SUPI, supi)
	}
	if r.SUPIUnauthenticated != nil {
		b.boolean(2, bool(*r.SUPIUnauthenticated))
	}
	if present(r.PEI) {
		b.explicit(3, "SMFStartOfInterceptionWithEstablishedPDUSession.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "SMFStartOfInterceptionWithEstablishedPDUSession.GPSI", r.GPSI, gpsi)
	}
	b.integer(5, int64(r.PDUSessionID))
	b.constructed(6, r.GTPTunnelID.emit)
	b.integer(7, int64(r.PDUSessionType))
	if !isZero(r.SNSSAI) {
		b.constructed(8, r.SNSSAI.emit)
	}
	b.sequenceOf(9, "SMFStartOfInterceptionWithEstablishedPDUSession.UEEndpoint", r.UEEndpoint, ueEndpointAddress)
	b.text(12, string(r.DNN))
	if r.AMFID != nil {
		b.constructed(13, r.AMFID.emit)
	}
	b.integer(15, int64(r.RequestType))
	if r.AccessType != 0 {
		b.integer(16, int64(r.AccessType))
	}
	if r.RATType != 0 {
		b.integer(17, int64(r.RATType))
	}
	if !isZero(r.ServingNetwork) {
		b.constructed(22, r.ServingNetwork.emit)
	}
	if !isZero(r.GTPTunnelInfo) {
		b.constructed(23, r.GTPTunnelInfo.emit)
	}
}

func (r SMFUnsuccessfulProcedure) emit(b *builder) {
	b.integer(1, int64(r.FailedProcedureType))
	b.integer(2, int64(r.FailureCause))
	b.integer(3, int64(r.Initiator))
	if present(r.SUPI) {
		b.explicit(5, "SMFUnsuccessfulProcedure.SUPI", r.SUPI, supi)
	}
	if r.SUPIUnauthenticated != nil {
		b.boolean(6, bool(*r.SUPIUnauthenticated))
	}
	if present(r.PEI) {
		b.explicit(7, "SMFUnsuccessfulProcedure.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(8, "SMFUnsuccessfulProcedure.GPSI", r.GPSI, gpsi)
	}
	if r.PDUSessionID != 0 {
		b.integer(9, int64(r.PDUSessionID))
	}
	if len(r.UEEndpoint) > 0 {
		b.sequenceOf(10, "SMFUnsuccessfulProcedure.UEEndpoint", r.UEEndpoint, ueEndpointAddress)
	}
	if r.DNN != "" {
		b.text(12, string(r.DNN))
	}
	if r.AMFID != nil {
		b.constructed(13, r.AMFID.emit)
	}
	if r.RequestType != 0 {
		b.integer(15, int64(r.RequestType))
	}
	if r.AccessType != 0 {
		b.integer(16, int64(r.AccessType))
	}
	if r.RATType != 0 {
		b.integer(17, int64(r.RATType))
	}
}

func (r AMFUEServiceAccept) emit(b *builder) {
	b.constructed(1, r.UserIdentifiers.emit)
	b.explicit(2, "AMFUEServiceAccept.ServiceMessageIdentity", r.ServiceMessageIdentity, serviceMessageIdentity)
	if r.ServiceType != nil {
		b.octets(3, r.ServiceType)
	}
	if r.FiveGTMSI != nil {
		b.integer(4, int64(*r.FiveGTMSI))
	}
}

func (r AMFUEPolicyTransfer) emit(b *builder) {
	b.explicit(1, "AMFUEPolicyTransfer.SUPI", r.SUPI, supi)
	if !isZero(r.SUCI) {
		b.constructed(2, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(3, "AMFUEPolicyTransfer.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "AMFUEPolicyTransfer.GPSI", r.GPSI, gpsi)
	}
	if !isZero(r.GUTI) {
		b.constructed(5, r.GUTI.emit)
	}
	b.octets(6, r.UEPolicy)
}

func (r AMFPositioningInfoTransfer) emit(b *builder) {
	b.explicit(1, "AMFPositioningInfoTransfer.SUPI", r.SUPI, supi)
	if !isZero(r.SUCI) {
		b.constructed(2, r.SUCI.emit)
	}
	if present(r.PEI) {
		b.explicit(3, "AMFPositioningInfoTransfer.PEI", r.PEI, pei)
	}
	if present(r.GPSI) {
		b.explicit(4, "AMFPositioningInfoTransfer.GPSI", r.GPSI, gpsi)
	}
	if !isZero(r.GUTI) {
		b.constructed(5, r.GUTI.emit)
	}
	if r.NRPPaMessage != nil {
		b.octets(6, r.NRPPaMessage)
	}
	if r.LPPMessage != nil {
		b.octets(7, r.LPPMessage)
	}
	b.text(8, string(r.LCSCorrelationID))
}

func (r AMFRANHandoverCommand) emit(b *builder) {
	b.constructed(1, r.UserIdentifiers.emit)
	b.integer(2, int64(r.AMFUENGAPID))
	b.integer(3, int64(r.RANUENGAPID))
	b.integer(4, int64(r.HandoverType))
	b.octets(5, r.TargetToSourceContainer)
}

func (r AMFRANHandoverRequest) emit(b *builder) {
	b.constructed(1, r.UserIdentifiers.emit)
	b.integer(2, int64(r.AMFUENGAPID))
	b.integer(3, int64(r.RANUENGAPID))
	b.integer(4, int64(r.HandoverType))
	b.explicit(5, "AMFRANHandoverRequest.HandoverCause", r.HandoverCause, handoverCause)
	b.constructed(6, r.PDUSessionResourceInformation.emit)
	b.octets(9, r.TargetToSourceContainer)
	b.octets(11, r.SourceToTargetContainer)
}

// encodePayload writes XIRIPayload ::= SEQUENCE { xIRIPayloadOID [1] RELATIVE-OID,
// event [2] XIRIEvent }. event is a CHOICE, hence EXPLICIT.
func encodePayload(event any) ([]byte, error) {
	var b builder
	b.sequence(func(p *builder) {
		p.octets(1, xIRIPayloadOID)
		p.explicit(2, "XIRIPayload.Event", event, xiriEvent)
	})
	return b.buf, b.err
}
