// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

// Package iri builds and encodes 3GPP TS 33.128 xIRI records — the Intercept
// Related Information payload carried in an X2 PDU (payload format 3GPP-33.128).
//
// The records are defined in the TS33128Payloads ASN.1 module
// (DEFINITIONS IMPLICIT TAGS), encoded with DER. We hand-write only the subset of
// records the AMF/SMF POIs actually emit, as typed structs whose `asn1:"..."` tags
// record the module's field tags, and encode them with explicit emitters over the
// standard library's encoding/asn1 (emit.go). The emitters write each field's tag
// themselves; the struct tags state the module's tag and optionality, and are read at run
// time only for `optional`, which the constraint walk uses to tell an absent member from an
// empty mandatory one (isOptional in constraints.go). asn1_drift_test.go audits the tags
// against the published module, and emit_tags_test.go holds the emitters to them.
//
// It encodes the seventeen XIRIEvent records listed in emit.go's xiriEvent: eleven
// produced by the AMF, five by the SMF, and AMFPositioningInfoTransfer, which is
// modelled but produced by no point of interception (see li/README.md). Each record
// type is a slice of the module's record: the members a point of interception here can
// supply, with the rest recorded, with a reason, in asn1_drift_test.go.
package iri

import "net"

// xIRIPayloadOID is the fixed RELATIVE-OID identifying an xIRI payload for
// TS 33.128 r18 v15: {threeGPP(4) ts33128(19) r18(18) version15(15) xIRI(1)}.
// All arcs are < 128, so the RELATIVE-OID content is one byte per arc. It is
// carried in the [1] IMPLICIT field of XIRIPayload, where the implicit context
// tag replaces the universal RELATIVE-OID tag, so the bytes on the wire are
// exactly this content.
var xIRIPayloadOID = []byte{0x04, 0x13, 0x12, 0x0F, 0x01}

// Target-identifier leaf types. They are distinct Go types (not bare strings)
// so the CHOICE emitters can tell the alternatives apart by type, and so
// constraints.go can check each against its own definition.
//
// IMEI is the fourteen-digit form, TAC and serial number, without the check digit
// TS 29.571's "imei-" PEI carries as a fifteenth: a network function holding that
// form drops the last digit before building one.
type (
	IMSI   string // NumericString(6..15)
	NAI    string // UTF8String
	IMEI   string // NumericString(14)
	IMEISV string // NumericString(16)
	MSISDN string // NumericString(1..15)
)

// AMFRegistrationType ::= ENUMERATED, see clause 6.2.2.2.2.
type AMFRegistrationType int

// The AMFRegistrationType values, numbered as the module numbers them.
const (
	RegTypeInitial          AMFRegistrationType = 1
	RegTypeMobility         AMFRegistrationType = 2
	RegTypePeriodic         AMFRegistrationType = 3
	RegTypeEmergency        AMFRegistrationType = 4
	RegTypeSNPNOnboarding   AMFRegistrationType = 5
	RegTypeDisasterMobility AMFRegistrationType = 6
	RegTypeDisasterInitial  AMFRegistrationType = 7
)

// AMFRegistrationResult ::= ENUMERATED.
type AMFRegistrationResult int

// The AMFRegistrationResult values: the access types the UE is registered over.
const (
	RegResult3GPPAccess     AMFRegistrationResult = 1
	RegResultNon3GPPAccess  AMFRegistrationResult = 2
	RegResult3GPPAndNon3GPP AMFRegistrationResult = 3
)

// AMFDirection ::= ENUMERATED — who initiated the procedure.
type AMFDirection int

// The AMFDirection values.
const (
	DirNetworkInitiated AMFDirection = 1
	DirUEInitiated      AMFDirection = 2
)

// AccessType ::= ENUMERATED.
type AccessType int

// The AccessType values.
const (
	AccessThreeGPP    AccessType = 1
	AccessNonThreeGPP AccessType = 2
	AccessBoth        AccessType = 3
)

// --- SMF PDU-session types ---

// PDUSessionID ::= INTEGER (0..255).
//
// Zero is TS 24.007 table 11.2.3.1c.1's "No PDU session identity assigned", and it is written
// where it is mandatory rather than refused: the module's range admits it, so a receiver
// accepts it, and refusing it would drop a record about a session — the AMF's handover
// record takes the identity from what the RAN sent — that a conformant mediation function
// would have accepted. Where the member is OPTIONAL, zero is how absence is spelled, which
// is what that value means.
type PDUSessionID int

// PDUSessionType ::= ENUMERATED.
type PDUSessionType int

// The PDUSessionType values, numbered as TS 24.501 numbers the PDU session type.
const (
	PDUSessionTypeIPv4         PDUSessionType = 1
	PDUSessionTypeIPv6         PDUSessionType = 2
	PDUSessionTypeIPv4v6       PDUSessionType = 3
	PDUSessionTypeUnstructured PDUSessionType = 4
	PDUSessionTypeEthernet     PDUSessionType = 5
)

// DNN ::= UTF8String — Data Network Name.
type DNN string

// FiveGSMRequestType ::= ENUMERATED.
type FiveGSMRequestType int

// The FiveGSMRequestType values.
const (
	SMRequestInitial           FiveGSMRequestType = 1
	SMRequestExisting          FiveGSMRequestType = 2
	SMRequestInitialEmergency  FiveGSMRequestType = 3
	SMRequestExistingEmergency FiveGSMRequestType = 4
	SMRequestModification      FiveGSMRequestType = 5
	SMRequestReserved          FiveGSMRequestType = 6
	SMRequestMAPDU             FiveGSMRequestType = 7
)

// The SNSSAI and FTEID leaves. Named rather than declared inline, so the constraint table
// keyed on type reaches them; see constraints.go for why keying on a field name would not
// survive the next record that spells the field differently.
//
// `SliceServiceType` and `TEID` are inline INTEGERs in the module, which gives them no name of
// their own — these are this package's. `IPv4Address` and `IPv6Address` are the module's own
// types, and FTEID's members below are declared with them because that is what the module says
// they are.
type (
	SliceDifferentiator []byte // SIZE(3)
	SliceServiceType    int    // INTEGER (0..255)
	TEID                int64  // INTEGER (0..4294967295)
)

// SNSSAI ::= SEQUENCE — the slice identifier (SST + optional SD).
//
// A mapped HPLMN SST of zero is omitted, and so is an S-NSSAI whose SST is zero with no SD
// (the optional member is then the zero structure). Left as values rather than pointers
// because the network functions do not hold an SST of zero for a session: table 5.15.2.2-1 of
// TS 23.501 standardises no value 0, and the SMF reads the SST from a configured slice. The same
// argument covers an optional PDU session ID of zero (see PDUSessionID). It does not cover
// AMFID, which is a pointer for that reason.
type SNSSAI struct {
	SliceServiceType    SliceServiceType    `asn1:"tag:1"`
	SliceDifferentiator SliceDifferentiator `asn1:"tag:2,optional"`
	MappedHPLMNSST      SliceServiceType    `asn1:"tag:3,optional"`
	// The mapped HPLMN SD is the same leaf as the serving SD — `OCTET STRING (SIZE(3))` in
	// the module both times — so it carries the same type and inherits the same check.
	MappedHPLMNSD SliceDifferentiator `asn1:"tag:4,optional"`
}

// FTEID ::= SEQUENCE — a GTP-U F-TEID (tunnel id + endpoint IP).
type FTEID struct {
	TEID        TEID        `asn1:"tag:1"`
	IPv4Address IPv4Address `asn1:"tag:2,optional"`
	IPv6Address IPv6Address `asn1:"tag:3,optional"`
}

// FiveGSGTPTunnels ::= SEQUENCE — the 5GS user plane tunnels of a PDU session
// (TS 33.128 table 6.2.3-1C). Only uLNGUUPTunnelInformation is modelled: it is
// the F-TEID of the UPF endpoint of the NG-U transport bearer, which is the
// value a POI here can supply. additionalULNGUUPTunnelInformation (multiple NG-U
// bearers) and dLRANTunnelInformation (downlink RAN tunnel and QoS flows) are
// not modelled — the SMF holds neither for the single default path it manages.
type FiveGSGTPTunnels struct {
	ULNGUUPTunnelInformation FTEID `asn1:"tag:1,optional"`
}

// GTPTunnelInfo ::= SEQUENCE — the user plane GTP tunnels of a session
// (TS 33.128 table 6.2.3-1B). The payload tables mark this field **M** in
// SMFPDUSessionEstablishment, SMFPDUSessionModification and
// SMFStartOfInterceptionWithEstablishedPDUSession, while the published ASN.1
// module marks it OPTIONAL — so a record omitting it would decode cleanly and still
// be non-conformant. It is modelled, and the SMF populates it; see CONFORMANCE.md.
//
// ePSGTPTunnels [2] is not modelled: it reports PDN connection events at an
// SGW/PGW, which this project does not implement.
type GTPTunnelInfo struct {
	FiveGSGTPTunnels FiveGSGTPTunnels `asn1:"tag:1,optional"`
}

// Location ::= SEQUENCE — minimal model. All six members are OPTIONAL; only
// locationInfo [1] is modelled (and within it only currentLocation), so a Location
// may be empty. The deeper detail (userLocation → EUTRA/NR/N3GA/..., positioningInfo,
// the 4G variants, iMSLocation) is not modelled; CONFORMANCE.md records it.
type Location struct {
	LocationInfo LocationInfo `asn1:"tag:1,optional"`
}

// LocationInfo ::= SEQUENCE — minimal: currentLocation only; the rest (userLocation,
// geoInfo, rATType, timeZone, additionalCellIDs) is not modelled.
type LocationInfo struct {
	CurrentLocation bool `asn1:"tag:2,optional"`
}

// AMFFailedProcedureType ::= ENUMERATED.
type AMFFailedProcedureType int

// The AMFFailedProcedureType values.
const (
	FailedRegistration            AMFFailedProcedureType = 1
	FailedSMS                     AMFFailedProcedureType = 2
	FailedPDUSessionEstablishment AMFFailedProcedureType = 3
)

// FiveGMMCause / FiveGSMCause ::= INTEGER (0..255) — the two AMFFailureCause
// CHOICE arms. Distinct Go types so the CHOICE emitter can tell them apart.
type (
	FiveGMMCause int
	FiveGSMCause int
)

// SMFFailedProcedureType ::= ENUMERATED — which SM procedure failed, not why.
// The cause travels separately in FiveGSMCause.
type SMFFailedProcedureType int

// The SMFFailedProcedureType values.
const (
	SMFFailedPDUSessionEstablishment SMFFailedProcedureType = 1
	SMFFailedPDUSessionModification  SMFFailedProcedureType = 2
	SMFFailedPDUSessionRelease       SMFFailedProcedureType = 3
)

// Initiator ::= ENUMERATED — who is initiating the rejection or indicating the
// failure, per the field's description in TS 33.128 table 6.2.3-5.
type Initiator int

// The Initiator values.
const (
	InitiatorUE      Initiator = 1
	InitiatorNetwork Initiator = 2
	InitiatorUnknown Initiator = 3
)

// --- UserIdentifiers, the identity list the newer AMF records carry ---
//
// TS 33.128:
//
//	UserIdentifiers     ::= SEQUENCE { fiveGSSubscriberIDs [1] FiveGSSubscriberIDs OPTIONAL,
//	                                   ePSSubscriberIDs    [2] EPSSubscriberIDs OPTIONAL }
//	FiveGSSubscriberIDs ::= SEQUENCE { fiveGSSubscriberID [1] SEQUENCE SIZE(1..MAX) OF FiveGSSubscriberID }
//	FiveGSSubscriberID  ::= CHOICE { sUPI [1] SUPI, sUCI [2] SUCI, pEI [3] PEI, gPSI [4] GPSI }
//
// The inner arms are themselves CHOICEs, and a context tag on a CHOICE is
// automatically explicit — so each arm is a one-field Go struct, and the emitter
// (fiveGSSubscriberID in emit.go) writes it as [n] { [m] leaf }, the EXPLICIT
// wrapper the module asks for. Writing the leaves flat would emit [1] IMSI where the
// module wants [1] { [1] IMSI }.
//
// ePSSubscriberIDs is not modelled: it carries IMSI/IMEI/MSISDN in their EPS
// forms for a UE with an EPS presence, which SD-Core's AMF never has.
type (
	// SubscriberSUPI is the sUPI [1] arm; Value holds an IMSI or NAI.
	SubscriberSUPI struct {
		Value any `asn1:"choice:supi"`
	}
	// SubscriberPEI is the pEI [3] arm; Value holds an IMEI or IMEISV.
	SubscriberPEI struct {
		Value any `asn1:"choice:pei"`
	}
	// SubscriberGPSI is the gPSI [4] arm; Value holds an MSISDN or NAI.
	SubscriberGPSI struct {
		Value any `asn1:"choice:gpsi"`
	}
)

// FiveGSSubscriberIDs wraps the SEQUENCE SIZE(1..MAX) OF the CHOICE above. The
// list is declared []any, one CHOICE alternative per element; a present list must
// hold at least one, which constraints.go checks.
type FiveGSSubscriberIDs struct {
	IDs []any `asn1:"tag:1,choice:fiveGSSubscriberID"`
}

// UserIdentifiers is mandatory in the records that carry it, and both its members are
// OPTIONAL, so the empty form is schema-valid — and refused here. Every record this package
// emits that carries it (AMFRANHandoverCommand, AMFRANHandoverRequest, AMFUEServiceAccept)
// describes it as the identifiers associated with the target UE, two of them "including the
// target identifier" (tables 6.2.2.2.9.2-1 and 6.2.2.2.13-1), so an empty list is a record
// attributable to nobody. Build it with Identifiers.
type UserIdentifiers struct {
	FiveGS FiveGSSubscriberIDs `asn1:"tag:1,optional"`
}

// Identifiers builds a UserIdentifiers from whichever of the three identity
// leaves are known, skipping absent ones. Order follows the CHOICE's tag order so
// two records for the same subscriber compare equal.
//
// The list is SIZE(1..MAX), so a UserIdentifiers with no identity at all omits
// fiveGSSubscriberIDs entirely rather than carrying an empty list — present and
// empty would be schema-invalid. EncodeXIRI then refuses the record; see UserIdentifiers.
func Identifiers(supi, pei, gpsi any) UserIdentifiers {
	var ids []any
	if supi != nil {
		ids = append(ids, SubscriberSUPI{Value: supi})
	}
	if pei != nil {
		ids = append(ids, SubscriberPEI{Value: pei})
	}
	if gpsi != nil {
		ids = append(ids, SubscriberGPSI{Value: gpsi})
	}
	if len(ids) == 0 {
		return UserIdentifiers{}
	}
	return UserIdentifiers{FiveGS: FiveGSSubscriberIDs{IDs: ids}}
}

// --- NGAP-facing leaf types, for the handover records ---

// AMFUENGAPID ::= INTEGER (0..1099511627775), RANUENGAPID ::= INTEGER (0..4294967295).
type (
	AMFUENGAPID int64
	RANUENGAPID int64
)

// HandoverType ::= ENUMERATED, mirroring TS 38.413's HandoverType.
type HandoverType int

// The HandoverType values, numbered from one as the module numbers them; NGAP numbers
// the same members from zero, so a value read from NGAP is mapped, never cast.
const (
	HandoverIntra5GS     HandoverType = 1
	HandoverFiveGSToEPS  HandoverType = 2
	HandoverEPSTo5GS     HandoverType = 3
	HandoverFiveGSToUTRA HandoverType = 4
)

// The five HandoverCause arms. Each is an ENUMERATED whose values follow the
// corresponding NGAP Cause group in TS 38.413 clause 9.3.1.2, numbered from 1 in
// the order the module lists them. Distinct Go types so the CHOICE emitter can tell
// the groups apart; the NGAP-to-LI value mapping is done where the record is
// built, not here. causevalues.go names the values.
type (
	CauseRadioNetwork int
	CauseTransport    int
	CauseNas          int
	CauseProtocol     int
	CauseMisc         int
)

// RANTargetToSourceContainer / RANSourceToTargetContainer ::= OCTET STRING — the
// opaque RRC containers. Copied, never parsed: the AMF passes them through, and a
// byte it saw must be the byte the MDF sees.
//
// The module bounds neither, so an empty one is schema-valid, and it is delivered as such.
// Both are mandatory here, and the NGAP transparent containers they copy are mandatory
// (presence M) in the messages that carry them, but TS 38.413 sets no minimum length, and
// the AMF copies what the RAN sent: an empty container is the RAN's value, not one the
// element lost. gnbsim rel-2.3.0 sends a zero-length Target to Source Transparent Container.
type (
	RANTargetToSourceContainer []byte
	RANSourceToTargetContainer []byte
)

// PDUSessionResourceInformation ::= SEQUENCE — which session moved with the target.
type PDUSessionResourceInformation struct {
	PDUSessionID PDUSessionID `asn1:"tag:1"`
}

// --- Service-accept and transfer leaf types ---

// ServiceMessageIdentity ::= CHOICE { serviceRequest [1] OCTET STRING,
// serviceAccept [2] OCTET STRING } — the message-type octet, per TS 24.501
// clause 9.7, not the whole PDU.
//
// The module leaves both alternatives unconstrained; table 6.2.2.2.13-1 says "Encoding per
// TS 24.501 clause 9.7", whose message type is one octet. constraints.go holds both to
// SIZE(1) on that authority, so a whole NAS PDU handed over in error is refused rather than
// delivered as an identity no receiver can read.
type (
	ServiceRequestIdentity []byte
	ServiceAcceptIdentity  []byte
)

// UEPolicy ::= OCTET STRING (SIZE(16..65540)) and lcsCorrelationId ::= UTF8String
// (SIZE(1..255)). The policy is an opaque payload: copied, never parsed.
//
// The policy is the content of a MANAGE UE POLICY COMMAND (TS 24.501 table D.5.1.1.1), not of
// whichever UE policy message happened to be relayed; see AMFUEPolicyTransfer.
type (
	UEPolicy         []byte
	LCSCorrelationID string
)

// The FiveGGUTI leaves. Every one is a named type in the module with a restriction of its own,
// and every one arrives from this element's own configuration and context — so nothing validates
// them before they reach a record, which is why they are named here and checked in
// constraints.go.
type (
	MCC         string // NumericString (SIZE(3))
	MNC         string // NumericString (SIZE(2..3))
	AMFRegionID int    // INTEGER (0..255)
	AMFSetID    int    // INTEGER (0..1023)
	AMFPointer  int    // INTEGER (0..63)
	FiveGTMSI   int64  // INTEGER (0..4294967295)
)

// FiveGGUTI ::= SEQUENCE. All members are IMPLICIT context-tagged.
type FiveGGUTI struct {
	MCC         MCC         `asn1:"tag:1"`
	MNC         MNC         `asn1:"tag:2"`
	AMFRegionID AMFRegionID `asn1:"tag:3"`
	AMFSetID    AMFSetID    `asn1:"tag:4"`
	AMFPointer  AMFPointer  `asn1:"tag:5"`
	FiveGTMSI   FiveGTMSI   `asn1:"tag:6"`
}

// The SUCI leaves. TS 33.128 SUCI ::= SEQUENCE carries the subscription concealed
// identifier the UE presented, and every member is a named type in the module with a
// restriction of its own. Like the FiveGGUTI leaves, these arrive from a NAS message,
// so nothing has validated them by the time they reach a record — which is the
// argument for naming them here.
type (
	RoutingIndicator       int    // INTEGER (0..9999)
	ProtectionSchemeID     int    // INTEGER (0..15)
	HomeNetworkPublicKeyID []byte // OCTET STRING, unconstrained
	SchemeOutput           []byte // OCTET STRING, unconstrained
	SUPIType               int    // INTEGER (0..7)
	RoutingIndicatorLength int    // INTEGER (1..4)
	HomeNetworkIdentifier  string // UTF8String, unconstrained
)

// SUCI ::= SEQUENCE. All members are IMPLICIT context-tagged.
//
// routingIndicatorLength is the member most easily lost: the module says it "shall be
// included if different from the number of meaningful digits given in routingIndicator",
// so a routing indicator of "0123" must carry a length of 4 that the INTEGER 123 cannot
// express. Nothing on the wire reveals its absence, which puts it in the same class as
// the conditional fields this package's CONFORMANCE.md exists to track.
type SUCI struct {
	MCC                    MCC                     `asn1:"tag:1"`
	MNC                    MNC                     `asn1:"tag:2"`
	RoutingIndicator       RoutingIndicator        `asn1:"tag:3"`
	ProtectionSchemeID     ProtectionSchemeID      `asn1:"tag:4"`
	HomeNetworkPublicKeyID HomeNetworkPublicKeyID  `asn1:"tag:5"`
	SchemeOutput           SchemeOutput            `asn1:"tag:6"`
	RoutingIndicatorLength *RoutingIndicatorLength `asn1:"tag:7,optional"`
	SUPIType               *SUPIType               `asn1:"tag:8,optional"`
	HomeNetworkIdentifier  HomeNetworkIdentifier   `asn1:"tag:9,optional"`
}

// The TAI leaves. TAC is the tracking area code and NID the network identifier of a
// stand-alone non-public network.
type (
	TAC []byte // OCTET STRING (SIZE(2..3))
	NID string // UTF8String (SIZE(11))
)

// PLMNID ::= SEQUENCE, the MCC/MNC pair the module uses wherever a PLMN appears in its
// own right rather than inline.
type PLMNID struct {
	MCC MCC `asn1:"tag:1"`
	MNC MNC `asn1:"tag:2"`
}

// TAI ::= SEQUENCE and TAIList ::= SEQUENCE OF TAI.
//
// fiveGSTAIList is the *registration area* — TS 33.128 asks for "tracking areas
// associated with the registration area within which the UE is current registered",
// per access type, not the single serving TAI the AMF also holds.
type TAI struct {
	PLMNID PLMNID `asn1:"tag:1"`
	TAC    TAC    `asn1:"tag:2"`
	NID    NID    `asn1:"tag:3,optional"`
}

// TAIList ::= SEQUENCE OF TAI.
type TAIList []TAI

// SMFServingNetwork ::= SEQUENCE, the serving PLMN of a PDU session.
type SMFServingNetwork struct {
	PLMNID PLMNID `asn1:"tag:1"`
	NID    NID    `asn1:"tag:2,optional"`
}

// AMFID ::= SEQUENCE, the AMF Identifier of TS 23.003 clause 2.10.1 — the AMF half of a
// GUAMI. Its three leaves are the same named types FiveGGUTI already uses, so they are
// already range-checked by constraints.go.
//
// Every record carrying it declares it as a pointer, present exactly when non-nil: each of the
// three leaves admits zero, so AMF identifier 000000 is one an AMF can be configured with, and
// as a value member it would encode as absent.
type AMFID struct {
	AMFRegionID AMFRegionID `asn1:"tag:1"`
	AMFSetID    AMFSetID    `asn1:"tag:2"`
	AMFPointer  AMFPointer  `asn1:"tag:3"`
}

// RATType ::= ENUMERATED. Only the values a 5G core can report are named; the module
// also defines EPS and wireline arms this project never produces.
//
// It is OPTIONAL in every record that carries it, so zero means absent and it is
// deliberately not in constraints.go's mandatoryEnums.
type RATType int

// The RATType values a 5G core reports.
const (
	RATNR          RATType = 1
	RATEUTRA       RATType = 2
	RATWLAN        RATType = 3
	RATVirtual     RATType = 4
	RATNBIOT       RATType = 5
	RATLTEM        RATType = 9
	RATNRU         RATType = 10
	RATEUTRAU      RATType = 11
	RATTrustedN3GA RATType = 12
	RATTrustedWLAN RATType = 13
	// The NR satellite arms. An AMF serving non-terrestrial access reports a session
	// over one of these four, and leaving them unnamed would report no RAT type at all
	// for it.
	RATNRLEO      RATType = 16
	RATNRMEO      RATType = 17
	RATNRGEO      RATType = 18
	RATNROtherSat RATType = 19
	RATNRRedCap   RATType = 20
)

// SUPIUnauthenticatedIndication ::= BOOLEAN.
//
// Declared as a pointer wherever it appears, because false is its meaningful ordinary
// value — the SUPI *was* authenticated — and an OPTIONAL field whose value equals its
// type's zero is otherwise indistinguishable from an absent one; the emitters write a
// pointer member exactly when it is non-nil. Absent means the record carries no SUPI, so the condition
// governing the field does not hold; it does not mean "authenticated".
type SUPIUnauthenticatedIndication bool

// AMFRegistration is a slice of TS 33.128 AMFRegistration: the four mandatory
// members (registrationType, registrationResult, sUPI, gUTI) and the optionals sUCI,
// pEI, gPSI, fiveGSTAIList and rATType. The CHOICE-typed members (sUPI/pEI/gPSI) are
// EXPLICIT-tagged because a context tag on a CHOICE is automatically explicit.
type AMFRegistration struct {
	RegistrationType   AMFRegistrationType   `asn1:"tag:1"`
	RegistrationResult AMFRegistrationResult `asn1:"tag:2"`
	SUPI               any                   `asn1:"tag:4,explicit,choice:supi"`
	SUCI               SUCI                  `asn1:"tag:5,optional"`
	PEI                any                   `asn1:"tag:6,explicit,choice:pei,optional"`
	GPSI               any                   `asn1:"tag:7,explicit,choice:gpsi,optional"`
	GUTI               FiveGGUTI             `asn1:"tag:8"`
	FiveGSTAIList      TAIList               `asn1:"tag:11,optional"`
	RATType            RATType               `asn1:"tag:18,optional"`
}

// AMFDeregistration is a slice of TS 33.128 AMFDeregistration: the mandatory
// deregistrationDirection + accessType, plus the target-identifier optionals.
// (sUPI/sUCI/pEI/gPSI/gUTI are all OPTIONAL in this record.)
type AMFDeregistration struct {
	DeregistrationDirection AMFDirection `asn1:"tag:1"`
	AccessType              AccessType   `asn1:"tag:2"`
	SUPI                    any          `asn1:"tag:3,explicit,choice:supi,optional"`
	SUCI                    SUCI         `asn1:"tag:4,optional"`
	PEI                     any          `asn1:"tag:5,explicit,choice:pei,optional"`
	GPSI                    any          `asn1:"tag:6,explicit,choice:gpsi,optional"`
	GUTI                    FiveGGUTI    `asn1:"tag:7,optional"` // value+optional: an all-zero GUTI reads as absent
}

// AMFStartOfInterceptionWithRegisteredUE is a slice of the same-named record,
// generated when interception is activated for an already-registered UE.
// Mandatory: registrationResult, sUPI, gUTI. registrationType is OPTIONAL here.
type AMFStartOfInterceptionWithRegisteredUE struct {
	RegistrationResult AMFRegistrationResult `asn1:"tag:1"`
	RegistrationType   AMFRegistrationType   `asn1:"tag:2,optional"`
	SUPI               any                   `asn1:"tag:4,explicit,choice:supi"`
	SUCI               SUCI                  `asn1:"tag:5,optional"`
	PEI                any                   `asn1:"tag:6,explicit,choice:pei,optional"`
	GPSI               any                   `asn1:"tag:7,explicit,choice:gpsi,optional"`
	GUTI               FiveGGUTI             `asn1:"tag:8"`
	FiveGSTAIList      TAIList               `asn1:"tag:12,optional"`
}

// SMFPDUSessionEstablishment is a slice of the same-named TS 33.128 record.
// Mandatory: pDUSessionID, gTPTunnelID, pDUSessionType, dNN, requestType.
// uEEndpoint [9] is OPTIONAL here and carries the address assigned to the UE —
// distinct from gTPTunnelID, which is the serving UPF's tunnel endpoint. Table 6.2.3-1
// makes it conditional, "if available": an empty list is omitted.
// Deferred optionals (deeper subtrees): location [11] and the long tail.
type SMFPDUSessionEstablishment struct {
	SUPI                any                            `asn1:"tag:1,explicit,choice:supi,optional"`
	SUPIUnauthenticated *SUPIUnauthenticatedIndication `asn1:"tag:2,optional"`
	PEI                 any                            `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI                any                            `asn1:"tag:4,explicit,choice:gpsi,optional"`
	PDUSessionID        PDUSessionID                   `asn1:"tag:5"`
	GTPTunnelID         FTEID                          `asn1:"tag:6"`
	PDUSessionType      PDUSessionType                 `asn1:"tag:7"`
	SNSSAI              SNSSAI                         `asn1:"tag:8,optional"`
	UEEndpoint          []any                          `asn1:"tag:9,choice:ueEndpointAddress,optional"`
	DNN                 DNN                            `asn1:"tag:12"`
	AMFID               *AMFID                         `asn1:"tag:13,optional"`
	RequestType         FiveGSMRequestType             `asn1:"tag:15"`
	AccessType          AccessType                     `asn1:"tag:16,optional"`
	RATType             RATType                        `asn1:"tag:17,optional"`
	ServingNetwork      SMFServingNetwork              `asn1:"tag:22,optional"`
	GTPTunnelInfo       GTPTunnelInfo                  `asn1:"tag:25,optional"`
}

// SMFPDUSessionModification is a slice of the same-named record. Only
// requestType is mandatory.
type SMFPDUSessionModification struct {
	SUPI                any                            `asn1:"tag:1,explicit,choice:supi,optional"`
	SUPIUnauthenticated *SUPIUnauthenticatedIndication `asn1:"tag:2,optional"`
	PEI                 any                            `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI                any                            `asn1:"tag:4,explicit,choice:gpsi,optional"`
	SNSSAI              SNSSAI                         `asn1:"tag:5,optional"`
	RequestType         FiveGSMRequestType             `asn1:"tag:8"`
	AccessType          AccessType                     `asn1:"tag:9,optional"`
	RATType             RATType                        `asn1:"tag:10,optional"`
	PDUSessionID        PDUSessionID                   `asn1:"tag:11,optional"` // note: value 0 indistinguishable from absent
	// The CHOICE itself, not a SEQUENCE OF as in the other records: build it with UEEndpointAddress.
	UEEndpoint     any               `asn1:"tag:13,explicit,choice:ueEndpointAddress,optional"`
	ServingNetwork SMFServingNetwork `asn1:"tag:14,optional"`
	GTPTunnelInfo  GTPTunnelInfo     `asn1:"tag:16,optional"`
}

// SMFPDUSessionRelease is a slice of the same-named record. Mandatory: sUPI,
// pDUSessionID. Deferred optionals: timeOfFirst/LastPacket [5][6] (GeneralizedTime),
// location [9], and the cause tail.
type SMFPDUSessionRelease struct {
	SUPI         any          `asn1:"tag:1,explicit,choice:supi"`
	PEI          any          `asn1:"tag:2,explicit,choice:pei,optional"`
	GPSI         any          `asn1:"tag:3,explicit,choice:gpsi,optional"`
	PDUSessionID PDUSessionID `asn1:"tag:4"`
	// The volumes are pointers, so a session that moved no bytes reports 0. The module
	// declares them as unconstrained INTEGERs, so a negative value is schema-valid and
	// is encoded as given; a producer converting from an unsigned byte counter must not
	// let it wrap.
	UplinkVolume   *int64 `asn1:"tag:7,optional"`
	DownlinkVolume *int64 `asn1:"tag:8,optional"`
}

// AMFLocationUpdate is a slice of the same-named record. Mandatory: sUPI,
// location (a minimal Location for now — see Location).
type AMFLocationUpdate struct {
	SUPI     any       `asn1:"tag:1,explicit,choice:supi"`
	SUCI     SUCI      `asn1:"tag:2,optional"`
	PEI      any       `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI     any       `asn1:"tag:4,explicit,choice:gpsi,optional"`
	GUTI     FiveGGUTI `asn1:"tag:5,optional"`
	Location Location  `asn1:"tag:6"`
}

// AMFUnsuccessfulProcedure is a slice of the same-named record. Mandatory:
// failedProcedureType and failureCause (a CHOICE of 5GMM/5GSM cause).
type AMFUnsuccessfulProcedure struct {
	FailedProcedureType AMFFailedProcedureType `asn1:"tag:1"`
	FailureCause        any                    `asn1:"tag:2,explicit,choice:amfFailureCause"`
	SUPI                any                    `asn1:"tag:4,explicit,choice:supi,optional"`
	SUCI                SUCI                   `asn1:"tag:5,optional"`
	PEI                 any                    `asn1:"tag:6,explicit,choice:pei,optional"`
	GPSI                any                    `asn1:"tag:7,explicit,choice:gpsi,optional"`
	GUTI                FiveGGUTI              `asn1:"tag:8,optional"`
	Location            Location               `asn1:"tag:9,optional"`
}

// AMFIdentifierAssociation is a slice of the same-named TS 33.128 record,
// generated when the AMF binds a target's SUPI to a (newly assigned) 5G-GUTI.
// Mandatory: sUPI [1], gUTI [5]; the pEI/gPSI target-identifier optionals are
// also carried. Deferred optionals (deeper types): sUCI [2], oldGUTI [6]
// (EPS5GGUTI), additionalUserIdentifiers [7], relatedAMFUENGAPID [8].
type AMFIdentifierAssociation struct {
	SUPI any       `asn1:"tag:1,explicit,choice:supi"`
	SUCI SUCI      `asn1:"tag:2,optional"`
	PEI  any       `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI any       `asn1:"tag:4,explicit,choice:gpsi,optional"`
	GUTI FiveGGUTI `asn1:"tag:5"`
	// location [6] is MANDATORY in this record, unlike the other records that carry a
	// Location: a record without it fails schema validation. The minimal Location the
	// other records use satisfies the requirement.
	Location      Location `asn1:"tag:6"`
	FiveGSTAIList TAIList  `asn1:"tag:7,optional"`
}

// AMFIdentifierDeassociation is a slice of the same-named TS 33.128 record,
// generated when the AMF releases a target's SUPI↔5G-GUTI binding. Mandatory:
// sUPI [1] and gUTI [5]. location [6] is optional here — unlike in
// AMFIdentifierAssociation — and is not modelled.
//
// The GUTI is [5]; [2] is sUCI, and a receiver reads a 5G-GUTI written there as a SUCI.
type AMFIdentifierDeassociation struct {
	SUPI any       `asn1:"tag:1,explicit,choice:supi"`
	SUCI SUCI      `asn1:"tag:2,optional"`
	PEI  any       `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI any       `asn1:"tag:4,explicit,choice:gpsi,optional"`
	GUTI FiveGGUTI `asn1:"tag:5"`
}

// UEEndpointAddress alternatives. TS 33.128:
//
//	UEEndpointAddress ::= CHOICE {
//	    iPv4Address     [1] IPv4Address,      -- OCTET STRING (SIZE(4))
//	    iPv6Address     [2] IPv6Address,      -- OCTET STRING (SIZE(16))
//	    ethernetAddress [3] MACAddress        -- OCTET STRING (SIZE(6))
//	}
//
// Distinct Go types, like the IMSI/NAI target-identifier leaves, so the CHOICE
// emitter can tell the alternatives apart by type.
//
// Most ueEndpoint fields are a SEQUENCE OF this CHOICE, so they are declared as []any
// and tagged with the choice, which applies to each element. SMFPDUSessionModification's
// is the exception: `uEEndpoint [13] UEEndpointAddress OPTIONAL` is the CHOICE itself, so
// it is a single any.
type (
	IPv4Address []byte // SIZE(4)
	IPv6Address []byte // SIZE(16)
	MACAddress  []byte // SIZE(6)
)

// ServiceType is the one-octet service type of a SERVICE REQUEST or ACCEPT. Inline in the
// module — `OCTET STRING (SIZE(1))` — so the name is this package's.
type ServiceType []byte // SIZE(1)

// UEEndpoint builds the ueEndpoint list for a single UE address, discriminating
// v4 from v6 the way the rest of the project does — To4 first, because a
// 4-in-6-mapped address answers To16 as well and would otherwise be reported as
// IPv6. Returns nil for an absent or unusable address — an Unstructured session,
// or an Ethernet session whose MAC is not yet known — which every record treats as
// "not available", as the TS 33.128 tables make uEEndpoint conditional.
func UEEndpoint(ip net.IP) []any {
	if a := UEEndpointAddress(ip); a != nil {
		return []any{a}
	}
	return nil
}

// UEEndpointAddress is the single UEEndpointAddress alternative for ip, or nil for an
// absent or unusable address — the form SMFPDUSessionModification's uEEndpoint takes, and
// each element of the others'.
//
// The unspecified address (0.0.0.0 or ::) is unusable: it is what a session context holds
// before an address is allocated, and reported, it would name an endpoint no UE has.
func UEEndpointAddress(ip net.IP) any {
	if ip == nil || ip.IsUnspecified() {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return IPv4Address(append([]byte(nil), v4...))
	}
	if v16 := ip.To16(); v16 != nil {
		return IPv6Address(append([]byte(nil), v16...))
	}
	return nil
}

// SMFStartOfInterceptionWithEstablishedPDUSession is a slice of the same-named
// TS 33.128 record (XIRIEvent [9]), generated when interception is activated for
// a UE that already has an active PDU session. Mandatory: pDUSessionID,
// gTPTunnelID, pDUSessionType, dNN, requestType. servingNetwork and gTPTunnelInfo
// are modelled; location and the EPS tail are not, as for SMFPDUSessionEstablishment.
//
// uEEndpoint is not OPTIONAL in the module, but table 6.2.3-4 makes it conditional
// (0..N, "if available"). The field is therefore always written, and a session with no
// endpoint address — Unstructured, or Ethernet before a MAC is learned — is reported with
// an empty list, the module's only way to say "not available" here. Build it with
// UEEndpoint.
type SMFStartOfInterceptionWithEstablishedPDUSession struct {
	SUPI                any                            `asn1:"tag:1,explicit,choice:supi,optional"`
	SUPIUnauthenticated *SUPIUnauthenticatedIndication `asn1:"tag:2,optional"`
	PEI                 any                            `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI                any                            `asn1:"tag:4,explicit,choice:gpsi,optional"`
	PDUSessionID        PDUSessionID                   `asn1:"tag:5"`
	GTPTunnelID         FTEID                          `asn1:"tag:6"`
	PDUSessionType      PDUSessionType                 `asn1:"tag:7"`
	SNSSAI              SNSSAI                         `asn1:"tag:8,optional"`
	UEEndpoint          []any                          `asn1:"tag:9,choice:ueEndpointAddress"`
	DNN                 DNN                            `asn1:"tag:12"`
	AMFID               *AMFID                         `asn1:"tag:13,optional"`
	RequestType         FiveGSMRequestType             `asn1:"tag:15"`
	AccessType          AccessType                     `asn1:"tag:16,optional"`
	RATType             RATType                        `asn1:"tag:17,optional"`
	ServingNetwork      SMFServingNetwork              `asn1:"tag:22,optional"`
	GTPTunnelInfo       GTPTunnelInfo                  `asn1:"tag:23,optional"`
}

// SMFUnsuccessfulProcedure is a slice of the same-named record (XIRIEvent [10]),
// generated wherever the SMF refuses or fails a session procedure for a target.
// Mandatory: failedProcedureType, failureCause, initiator — all three knowable at
// every rejection site. The target-identifier optionals and pDUSessionID are
// carried because a record an agency cannot attribute to a subscriber and a
// session is of little use. Deferred optionals: the EPS tail, requestedSlice,
// location, and the long list beyond.
type SMFUnsuccessfulProcedure struct {
	FailedProcedureType SMFFailedProcedureType         `asn1:"tag:1"`
	FailureCause        FiveGSMCause                   `asn1:"tag:2"`
	Initiator           Initiator                      `asn1:"tag:3"`
	SUPI                any                            `asn1:"tag:5,explicit,choice:supi,optional"`
	SUPIUnauthenticated *SUPIUnauthenticatedIndication `asn1:"tag:6,optional"`
	PEI                 any                            `asn1:"tag:7,explicit,choice:pei,optional"`
	GPSI                any                            `asn1:"tag:8,explicit,choice:gpsi,optional"`
	PDUSessionID        PDUSessionID                   `asn1:"tag:9,optional"` // value 0 indistinguishable from absent
	UEEndpoint          []any                          `asn1:"tag:10,choice:ueEndpointAddress,optional"`
	DNN                 DNN                            `asn1:"tag:12,optional"`
	AMFID               *AMFID                         `asn1:"tag:13,optional"`
	RequestType         FiveGSMRequestType             `asn1:"tag:15,optional"`
	AccessType          AccessType                     `asn1:"tag:16,optional"`
	RATType             RATType                        `asn1:"tag:17,optional"`
}

// AMFUEServiceAccept is a slice of the same-named record (XIRIEvent [147]),
// generated when the AMF sends a SERVICE ACCEPT to the target. Mandatory:
// userIdentifiers, serviceMessageIdentity.
type AMFUEServiceAccept struct {
	UserIdentifiers        UserIdentifiers `asn1:"tag:1"`
	ServiceMessageIdentity any             `asn1:"tag:2,explicit,choice:serviceMessageIdentity"`
	ServiceType            ServiceType     `asn1:"tag:3,optional"`
	FiveGTMSI              *FiveGTMSI      `asn1:"tag:4,optional"` // a pointer: 0 is a valid 5G-TMSI
}

// AMFUEPolicyTransfer is a slice of the same-named record (XIRIEvent [146]). Mandatory:
// sUPI, uEPolicy. The policy is copied opaquely, like the RAN containers.
//
// Clause 6.2.2.2.12 generates it on one event only: the AMF sending a
// Namf_Communication_N1MessageNotify request that carries the UE's MANAGE UE POLICY COMPLETE —
// the UE confirming it accepted the policies. Its uEPolicy is not that message but the one it
// confirms: "Content of the N1 NAS message MANAGE UE POLICY COMMAND, as defined in TS 24.501
// table D.5.1.1.1". So the producer reports on the uplink COMPLETE and carries the downlink
// COMMAND it answers. A COMMAND relayed and not yet confirmed is not a reportable event, and
// the COMPLETE itself (two octets) is never the content.
//
// The lower bound, 16, is the shortest COMMAND carrying the optional UE policy network
// classmark: two octets of header, an 11-octet UE policy section management list (LV-E) and a
// 3-octet classmark (TLV). The upper bound, 65540, adds the per-IE maxima, which table
// D.5.1.1.1's NOTE forbids together: a COMMAND's content cannot exceed 65535 octets. So only
// the lower bound is exact. A COMMAND with neither optional IE is shorter than 16 only in one
// shape — one PLMN, one instruction deleting a policy section — which is 13 octets, an ordinary
// PCF operation; the constraint refuses it, and CONFORMANCE.md records that disagreement.
type AMFUEPolicyTransfer struct {
	SUPI     any       `asn1:"tag:1,explicit,choice:supi"`
	SUCI     SUCI      `asn1:"tag:2,optional"`
	PEI      any       `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI     any       `asn1:"tag:4,explicit,choice:gpsi,optional"`
	GUTI     FiveGGUTI `asn1:"tag:5,optional"`
	UEPolicy UEPolicy  `asn1:"tag:6"`
}

// AMFPositioningInfoTransfer is a slice of the same-named record
// (XIRIEvent [111]), generated when the AMF relays an NRPPa or LPP message for a
// target. Mandatory: sUPI, lcsCorrelationId. Both payloads are copied opaquely.
type AMFPositioningInfoTransfer struct {
	SUPI any `asn1:"tag:1,explicit,choice:supi"`
	// Modelled so the record is complete against the module, and populated by nobody:
	// no POI emits this record. All four of clause 6.2.2.2.8's trigger events are
	// exchanges with an LMF and this AMF has none, so the condition cannot arise. In
	// scope the day an LMF is deployed — see li/README.md.
	SUCI             SUCI             `asn1:"tag:2,optional"`
	PEI              any              `asn1:"tag:3,explicit,choice:pei,optional"`
	GPSI             any              `asn1:"tag:4,explicit,choice:gpsi,optional"`
	GUTI             FiveGGUTI        `asn1:"tag:5,optional"`
	NRPPaMessage     []byte           `asn1:"tag:6,optional"`
	LPPMessage       []byte           `asn1:"tag:7,optional"`
	LCSCorrelationID LCSCorrelationID `asn1:"tag:8"`
}

// AMFRANHandoverCommand is a slice of the same-named record (XIRIEvent [113]),
// generated when the AMF sends a HANDOVER COMMAND to the source RAN node. All
// five members are mandatory, and all five are available where the command is
// sent.
type AMFRANHandoverCommand struct {
	UserIdentifiers         UserIdentifiers            `asn1:"tag:1"`
	AMFUENGAPID             AMFUENGAPID                `asn1:"tag:2"`
	RANUENGAPID             RANUENGAPID                `asn1:"tag:3"`
	HandoverType            HandoverType               `asn1:"tag:4"`
	TargetToSourceContainer RANTargetToSourceContainer `asn1:"tag:5"`
}

// AMFRANHandoverRequest is a slice of the same-named record (XIRIEvent [114]).
// Despite the name it is generated on the HANDOVER REQUEST **ACKNOWLEDGE** from
// the target RAN node, per TS 33.128 clause 6.2.2.2.9.3. Eight
// mandatory members; handoverCause and sourceToTargetContainer originate in the
// earlier HANDOVER REQUIRED and have to be carried forward.
type AMFRANHandoverRequest struct {
	UserIdentifiers               UserIdentifiers               `asn1:"tag:1"`
	AMFUENGAPID                   AMFUENGAPID                   `asn1:"tag:2"`
	RANUENGAPID                   RANUENGAPID                   `asn1:"tag:3"`
	HandoverType                  HandoverType                  `asn1:"tag:4"`
	HandoverCause                 any                           `asn1:"tag:5,explicit,choice:handoverCause"`
	PDUSessionResourceInformation PDUSessionResourceInformation `asn1:"tag:6"`
	TargetToSourceContainer       RANTargetToSourceContainer    `asn1:"tag:9"`
	SourceToTargetContainer       RANSourceToTargetContainer    `asn1:"tag:11"`
}

// XIRIPayload ::= SEQUENCE { xIRIPayloadOID [1] RELATIVE-OID, event [2] XIRIEvent }.
// event is a CHOICE, hence EXPLICIT-tagged.
type XIRIPayload struct {
	OID   []byte `asn1:"tag:1"`
	Event any    `asn1:"tag:2,explicit,choice:xiriEvent"`
}

// EncodeXIRI wraps an xIRI event (e.g. AMFRegistration) in an XIRIPayload and
// returns its DER encoding, suitable as the payload of an X2 PDU with payload
// format 3GPP-33.128.
func EncodeXIRI(event any) ([]byte, error) {
	// Every constrained leaf, on the one path every record crosses: the restrictions the
	// type definitions carry. A record violating its own definition would otherwise go out:
	// this element would believe it delivered, a conformant mediation function would discard
	// what it cannot validate, and no fault would be raised on either side. See
	// constraints.go.
	if err := validateConstraints(event); err != nil {
		return nil, err
	}

	return encodePayload(event)
}
