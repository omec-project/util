// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func sampleIdentifiers() UserIdentifiers {
	return Identifiers(IMSI("262019876543210"), IMEISV("3534250000000151"), MSISDN("4915123456789"))
}

// TestUserIdentifiersNesting is the assertion the whole UserIdentifiers modelling
// turns on. TS 33.128 nests three levels — UserIdentifiers -> FiveGSSubscriberIDs
// -> SEQUENCE OF a CHOICE whose arms are themselves CHOICEs — and a context tag on
// a CHOICE is explicit, so each arm must encode as [n] { inner }. Registering the
// leaves flat would emit one level too few, produce bytes that decode against a
// laxer reader, and be rejected by the published module.
func TestUserIdentifiersNesting(t *testing.T) {
	der := assertEncodes(t, AMFUEServiceAccept{
		UserIdentifiers:        sampleIdentifiers(),
		ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
	}, "305081050413120f01a247bf811343a13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839a20382014e")

	// userIdentifiers [1] { fiveGSSubscriberIDs [1] { fiveGSSubscriberID [1] {
	//   sUPI [1] { iMSI [1] }, pEI [3] { iMEISV [2] }, gPSI [4] { mSISDN [1] } } } },
	// in the CHOICE's tag order, so two records for the same subscriber are
	// byte-comparable.
	want := "a13c" + "a13a" + "a138" +
		"a111" + "810f" + hex.EncodeToString([]byte("262019876543210")) +
		"a312" + "8210" + hex.EncodeToString([]byte("3534250000000151")) +
		"a40f" + "810d" + hex.EncodeToString([]byte("4915123456789"))
	if !containsTLV(der, want, nil) {
		t.Errorf("userIdentifiers is not nested three levels deep in tag order: % x", der)
	}
}

// TestIdentifiersSkipsAbsentLeaves: a subscriber with no PEI or GPSI yields a
// one-entry list, not a three-entry list with empty arms.
func TestIdentifiersSkipsAbsentLeaves(t *testing.T) {
	u := Identifiers(IMSI("262019876543210"), nil, nil)
	if n := len(u.FiveGS.IDs); n != 1 {
		t.Fatalf("got %d identifiers, want 1: %#v", n, u.FiveGS.IDs)
	}
	// And no identity at all omits the inner list rather than carrying it empty:
	// fiveGSSubscriberID is SIZE(1..MAX), so present-and-empty is schema-invalid.
	if ids := Identifiers(nil, nil, nil).FiveGS.IDs; ids != nil {
		t.Errorf("empty Identifiers carries %#v, want nil", ids)
	}
}

// TestEmptyUserIdentifiersAreRefused: both members of UserIdentifiers are OPTIONAL, so the empty
// form is schema-valid, and every record carrying it makes it mandatory as the identifiers of the
// target UE. A record with none is attributable to nobody, so it is refused — and one identity is
// enough.
func TestEmptyUserIdentifiersAreRefused(t *testing.T) {
	for _, build := range []func(UserIdentifiers) any{
		func(u UserIdentifiers) any {
			return AMFUEServiceAccept{UserIdentifiers: u, ServiceMessageIdentity: ServiceAcceptIdentity{0x4E}}
		},
		func(u UserIdentifiers) any {
			return AMFRANHandoverCommand{
				UserIdentifiers: u, AMFUENGAPID: 7, RANUENGAPID: 9, HandoverType: HandoverIntra5GS,
				TargetToSourceContainer: RANTargetToSourceContainer{0xDE, 0xAD},
			}
		},
		func(u UserIdentifiers) any {
			return AMFRANHandoverRequest{
				UserIdentifiers: u, AMFUENGAPID: 7, RANUENGAPID: 9, HandoverType: HandoverIntra5GS,
				HandoverCause:                 CauseMisc(3),
				PDUSessionResourceInformation: PDUSessionResourceInformation{PDUSessionID: 5},
				TargetToSourceContainer:       RANTargetToSourceContainer{0xDE, 0xAD},
				SourceToTargetContainer:       RANSourceToTargetContainer{0xBE, 0xEF},
			}
		},
	} {
		empty := build(Identifiers(nil, nil, nil))
		if _, err := EncodeXIRI(empty); err == nil || !strings.Contains(err.Error(), "UserIdentifiers names no identity") {
			t.Errorf("%T with no identity: err = %v, want a refusal naming UserIdentifiers", empty, err)
		}
		if _, err := EncodeXIRI(build(Identifiers(nil, nil, MSISDN("4915123456789")))); err != nil {
			t.Errorf("%T with one identity was refused: %v", empty, err)
		}
	}
}

// TestAnEmptyMandatoryLocationIsRefused: every member of Location is OPTIONAL, so the empty
// form is schema-valid, and tables 6.2.2.2.4-1 and 6.2.2.2.7-1 make it mandatory as the location
// the network determined. An empty one reports no location, so it is refused where it is
// mandatory — and still omitted, not refused, where it is optional.
func TestAnEmptyMandatoryLocationIsRefused(t *testing.T) {
	guti := FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, AMFPointer: 1, FiveGTMSI: 1}
	located := Location{LocationInfo: LocationInfo{CurrentLocation: true}}

	for name, build := range map[string]func(Location) any{
		"AMFLocationUpdate": func(l Location) any {
			return AMFLocationUpdate{SUPI: IMSI("262019876543210"), Location: l}
		},
		"AMFIdentifierAssociation": func(l Location) any {
			return AMFIdentifierAssociation{SUPI: IMSI("262019876543210"), GUTI: guti, Location: l}
		},
	} {
		if _, err := EncodeXIRI(build(Location{})); err == nil || !strings.Contains(err.Error(), name+".Location reports no location") {
			t.Errorf("%s with an empty Location: err = %v, want a refusal naming its Location", name, err)
		}
		if _, err := EncodeXIRI(build(located)); err != nil {
			t.Errorf("%s with a Location was refused: %v", name, err)
		}
	}

	// Optional in AMFUnsuccessfulProcedure: an empty one is absent, not lost.
	if _, err := EncodeXIRI(AMFUnsuccessfulProcedure{
		FailedProcedureType: FailedRegistration, FailureCause: FiveGSMCause(27),
	}); err != nil {
		t.Errorf("AMFUnsuccessfulProcedure with no Location was refused: %v", err)
	}
}

// TestEmptyRANContainersAreDelivered: the module leaves both transparent containers unbounded,
// and the AMF copies what the RAN sent, so an empty container is the RAN's value, not one the
// element lost. A RAN that sends one (gnbsim rel-2.3.0 sends a zero-length Target to Source
// Transparent Container) still gets its handover reported: the record encodes, carrying the
// empty OCTET STRING.
func TestEmptyRANContainersAreDelivered(t *testing.T) {
	request := func() AMFRANHandoverRequest {
		return AMFRANHandoverRequest{
			UserIdentifiers: sampleIdentifiers(), AMFUENGAPID: 7, RANUENGAPID: 9, HandoverType: HandoverIntra5GS,
			HandoverCause:                 CauseMisc(3),
			PDUSessionResourceInformation: PDUSessionResourceInformation{PDUSessionID: 5},
			TargetToSourceContainer:       RANTargetToSourceContainer{0xDE, 0xAD},
			SourceToTargetContainer:       RANSourceToTargetContainer{0xBE, 0xEF},
		}
	}
	noTarget, noSource := request(), request()
	noTarget.TargetToSourceContainer = RANTargetToSourceContainer{}
	noSource.SourceToTargetContainer = nil
	command := AMFRANHandoverCommand{
		UserIdentifiers: sampleIdentifiers(), AMFUENGAPID: 7, RANUENGAPID: 9, HandoverType: HandoverIntra5GS,
	}

	// The empty member's own implicit tag with length zero: [5] in the command, [9] and [11] in
	// the request.
	for name, tc := range map[string]struct {
		event any
		empty []byte
	}{
		"request, empty target-to-source":  {noTarget, []byte{0x89, 0x00}},
		"request, absent source-to-target": {noSource, []byte{0x8b, 0x00}},
		"command, absent target-to-source": {command, []byte{0x85, 0x00}},
	} {
		der, err := EncodeXIRI(tc.event)
		if err != nil {
			t.Errorf("%s: refused (%v); an empty container is what the RAN sent", name, err)

			continue
		}
		// The mandatory member is present, as an empty OCTET STRING, not omitted.
		if !bytes.Contains(der, tc.empty) {
			t.Errorf("%s: no empty member % x in % x", name, tc.empty, der)
		}
	}
}

// TestAServiceMessageIdentityIsOneOctet: table 6.2.2.2.13-1 encodes serviceMessageIdentity per
// TS 24.501 clause 9.7, a one-octet message type, though the module leaves the alternatives
// unbounded. The whole NAS PDU handed over in error is refused, not delivered as an identity.
func TestAServiceMessageIdentityIsOneOctet(t *testing.T) {
	for _, id := range []any{
		ServiceRequestIdentity{0x7E, 0x00, 0x4C},
		ServiceAcceptIdentity{0x7E, 0x00, 0x4E},
		ServiceRequestIdentity{},
		ServiceAcceptIdentity{},
	} {
		_, err := EncodeXIRI(AMFUEServiceAccept{UserIdentifiers: sampleIdentifiers(), ServiceMessageIdentity: id})
		if err == nil || !strings.Contains(err.Error(), "SIZE(1)") {
			t.Errorf("serviceMessageIdentity %T % x: err = %v, want a SIZE(1) refusal", id, id, err)
		}
	}
}

func TestSMFUnsuccessfulProcedureEncoding(t *testing.T) {
	der := assertEncodes(t, SMFUnsuccessfulProcedure{
		FailedProcedureType: SMFFailedPDUSessionEstablishment,
		FailureCause:        FiveGSMCause(0x1a), // insufficient resources
		Initiator:           InitiatorNetwork,
		SUPI:                IMSI("262019876543210"),
		PEI:                 IMEISV("3534250000000151"),
		GPSI:                MSISDN("4915123456789"),
		PDUSessionID:        5,
		DNN:                 DNN("internet"),
		RequestType:         SMRequestInitial,
		AccessType:          AccessThreeGPP,
	}, "305f81050413120f01a256aa5481010182011a830102a511810f323632303139383736353433323130a712821033353334323530303030303030313531a80f810d343931353132333435363738398901058c08696e7465726e65748f0101900101")
	// failedProcedureType [1] 1, failureCause [2] 26, initiator [3] network(2).
	if !containsTLV(der, "810101"+"82011a"+"830102", nil) {
		t.Errorf("mandatory members are not [1] 1, [2] 26, [3] 2 in order: % x", der)
	}
}

// TestSMFUnsuccessfulProcedureMandatoryOnly: the three mandatory members alone
// must encode, since that is all some rejection sites know.
func TestSMFUnsuccessfulProcedureMandatoryOnly(t *testing.T) {
	assertEncodes(t, SMFUnsuccessfulProcedure{
		FailedProcedureType: SMFFailedPDUSessionRelease,
		FailureCause:        FiveGSMCause(0x2b),
		Initiator:           InitiatorNetwork,
	}, "301481050413120f01a20baa0981010382012b830102")
}

func TestAMFUEServiceAcceptEncoding(t *testing.T) {
	der := assertEncodes(t, AMFUEServiceAccept{
		UserIdentifiers:        sampleIdentifiers(),
		ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		ServiceType:            []byte{0x01},
	}, "305381050413120f01a24abf811346a13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839a20382014e830101")
	// serviceMessageIdentity [2] EXPLICIT around serviceAccept [2] 0x4E.
	if !containsTLV(der, "a20382014e", nil) {
		t.Errorf("serviceMessageIdentity is not [2] { [2] 4e }: % x", der)
	}
}

// TestOpaquePayloadsAreCopiedVerbatim: the AMF
// passes these through without parsing, so a byte the AMF saw must be the byte the
// MDF sees. A codec that helpfully normalised one would change evidence.
func TestOpaquePayloadsAreCopiedVerbatim(t *testing.T) {
	// Deliberately awkward: leading and trailing zero bytes, and a 0x00 run that a
	// string-oriented codec might truncate.
	//
	// Sixteen octets, because `UEPolicy ::= OCTET STRING (SIZE(16..65540))` and the encoder
	// checks that: the shortest permitted length is also the sharpest boundary to encode.
	policy := UEPolicy{
		0x00, 0xFF, 0x00, 0x00, 0x7F, 0x80, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0xFE, 0xFF, 0x00, 0x7F,
	}
	nrppa := []byte{0x00, 0x01, 0x02, 0xFF, 0x00}
	lpp := []byte{0xAB, 0x00, 0xCD}
	target := RANTargetToSourceContainer{0x00, 0xDE, 0xAD, 0x00}
	source := RANSourceToTargetContainer{0xBE, 0xEF, 0x00, 0x00}

	// Each payload must appear on the wire as its own member's tag and length followed
	// by exactly the bytes given — nothing normalised, trimmed or re-encoded.
	t.Run("uEPolicy", func(t *testing.T) {
		der := assertEncodes(t, AMFUEPolicyTransfer{SUPI: IMSI("262019876543210"), UEPolicy: policy}, "303281050413120f01a229bf811225a111810f323632303139383736353433323130861000ff00007f80000001020304feff007f")
		if !containsTLV(der, "8610", policy) {
			t.Errorf("uEPolicy [6] is not carried verbatim: % x", der)
		}
	})

	t.Run("positioning payloads", func(t *testing.T) {
		der := assertEncodes(t, AMFPositioningInfoTransfer{
			SUPI:             IMSI("262019876543210"),
			NRPPaMessage:     nrppa,
			LPPMessage:       lpp,
			LCSCorrelationID: LCSCorrelationID("corr-1"),
		}, "303381050413120f01a22abf6f27a111810f3236323031393837363534333231308605000102ff008703ab00cd8806636f72722d31")
		if !containsTLV(der, "8605", nrppa) {
			t.Errorf("nRPPaMessage [6] is not carried verbatim: % x", der)
		}
		if !containsTLV(der, "8703", lpp) {
			t.Errorf("lPPMessage [7] is not carried verbatim: % x", der)
		}
		if !containsTLV(der, "8806", []byte("corr-1")) {
			t.Errorf("lcsCorrelationId [8] is not carried verbatim: % x", der)
		}
	})

	t.Run("handover containers", func(t *testing.T) {
		der := assertEncodes(t, AMFRANHandoverRequest{
			UserIdentifiers:               sampleIdentifiers(),
			AMFUENGAPID:                   1,
			RANUENGAPID:                   2,
			HandoverType:                  HandoverIntra5GS,
			HandoverCause:                 CauseRadioNetwork(17),
			PDUSessionResourceInformation: PDUSessionResourceInformation{PDUSessionID: 5},
			TargetToSourceContainer:       target,
			SourceToTargetContainer:       source,
		}, "306981050413120f01a260bf725da13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839820101830102840101a503810111a603810105890400dead008b04beef0000")
		if !containsTLV(der, "8904", target) {
			t.Errorf("targetToSourceContainer [9] is not carried verbatim: % x", der)
		}
		if !containsTLV(der, "8b04", source) {
			t.Errorf("sourceToTargetContainer [11] is not carried verbatim: % x", der)
		}
		if !containsTLV(der, "a503810111", nil) {
			t.Errorf("handoverCause is not [5] { radioNetwork [1] 17 }: % x", der)
		}
	})
}

func TestAMFRANHandoverCommandEncoding(t *testing.T) {
	der := assertEncodes(t, AMFRANHandoverCommand{
		UserIdentifiers:         sampleIdentifiers(),
		AMFUENGAPID:             1099511627775, // the top of the range
		RANUENGAPID:             4294967295,
		HandoverType:            HandoverIntra5GS,
		TargetToSourceContainer: RANTargetToSourceContainer{0x01, 0x02},
	}, "306081050413120f01a257bf7154a13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839820600ffffffffff830500ffffffff84010185020102")
	// The tops of both ranges need a leading zero octet to stay positive:
	// aMFUENGAPID [2] 00 ff ff ff ff ff, rANUENGAPID [3] 00 ff ff ff ff.
	if !containsTLV(der, "820600ffffffffff"+"830500ffffffff", nil) {
		t.Errorf("NGAP ids at the top of their ranges are not encoded as expected: % x", der)
	}
}

// TestHandoverCauseArms: every cause group must be distinguishable on the wire, since
// the group is half the meaning — "radio network: handover desirable" and "misc:
// hardware failure" describe very different events.
func TestHandoverCauseArms(t *testing.T) {
	for _, tc := range []struct {
		arm  any
		want string // handoverCause [5] EXPLICIT around the arm's own tag
	}{
		{CauseRadioNetwork(1), "a503810101"},
		{CauseTransport(2), "a503820102"},
		{CauseNas(3), "a503830103"},
		{CauseProtocol(4), "a503840104"},
		{CauseMisc(5), "a503850105"},
	} {
		der, err := EncodeXIRI(AMFRANHandoverRequest{
			UserIdentifiers:               sampleIdentifiers(),
			AMFUENGAPID:                   1,
			RANUENGAPID:                   2,
			HandoverType:                  HandoverIntra5GS,
			HandoverCause:                 tc.arm,
			PDUSessionResourceInformation: PDUSessionResourceInformation{PDUSessionID: 5},
			TargetToSourceContainer:       RANTargetToSourceContainer{0x01},
			SourceToTargetContainer:       RANSourceToTargetContainer{0x02},
		})
		if err != nil {
			t.Fatalf("EncodeXIRI(%T): %v", tc.arm, err)
		}
		if !containsTLV(der, tc.want, nil) {
			t.Errorf("cause arm %T is not carried as %s: % x", tc.arm, tc.want, der)
		}
	}
}

// TestARadioNetworkCauseOf28IsRefused: CauseRadioNetwork runs 1..52 and defines no 28, so a
// range alone would admit a value no receiver can interpret. Its neighbours are carried.
func TestARadioNetworkCauseOf28IsRefused(t *testing.T) {
	request := func(cause CauseRadioNetwork) AMFRANHandoverRequest {
		return AMFRANHandoverRequest{
			UserIdentifiers: sampleIdentifiers(), AMFUENGAPID: 1, RANUENGAPID: 2, HandoverType: HandoverIntra5GS,
			HandoverCause:                 cause,
			PDUSessionResourceInformation: PDUSessionResourceInformation{PDUSessionID: 5},
			TargetToSourceContainer:       RANTargetToSourceContainer{0x01},
			SourceToTargetContainer:       RANSourceToTargetContainer{0x02},
		}
	}
	if _, err := EncodeXIRI(request(28)); err == nil || !strings.Contains(err.Error(), "defines no value 28") {
		t.Errorf("CauseRadioNetwork(28): err = %v, want a refusal", err)
	}
	for _, c := range []CauseRadioNetwork{27, 29} {
		if _, err := EncodeXIRI(request(c)); err != nil {
			t.Errorf("CauseRadioNetwork(%d) was refused: %v", c, err)
		}
	}
}

// TestRecordsDiscriminate: each of these records must be carried under its own
// XIRIEvent alternative. Their tags are 10, 111, 113, 114, 146 and 147 — all but the
// first use high-tag-number form, which is where a tag typo hides.
func TestRecordsDiscriminate(t *testing.T) {
	cases := []struct {
		name  string
		event any
		want  string
	}{
		{"unsuccessfulSMProcedure", SMFUnsuccessfulProcedure{
			FailedProcedureType: SMFFailedPDUSessionEstablishment, FailureCause: 1, Initiator: InitiatorNetwork,
		}, "301481050413120f01a20baa09810101820101830102"},
		{"positioningInfoTransfer", AMFPositioningInfoTransfer{
			SUPI: IMSI("262011"), LCSCorrelationID: "c",
		}, "301981050413120f01a210bf6f0da1088106323632303131880163"},
		{"handoverCommand", AMFRANHandoverCommand{
			UserIdentifiers: sampleIdentifiers(), AMFUENGAPID: 1, RANUENGAPID: 2,
			HandoverType: HandoverIntra5GS, TargetToSourceContainer: RANTargetToSourceContainer{0x01},
		}, "305681050413120f01a24dbf714aa13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839820101830102840101850101"},
		{"handoverRequest", AMFRANHandoverRequest{
			UserIdentifiers: sampleIdentifiers(), AMFUENGAPID: 1, RANUENGAPID: 2,
			HandoverType: HandoverIntra5GS, HandoverCause: CauseRadioNetwork(1),
			PDUSessionResourceInformation: PDUSessionResourceInformation{PDUSessionID: 5},
			TargetToSourceContainer:       RANTargetToSourceContainer{0x01},
			SourceToTargetContainer:       RANSourceToTargetContainer{0x02},
		}, "306381050413120f01a25abf7257a13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839820101830102840101a503810101a6038101058901018b0102"},
		{"uePolicyTransfer", AMFUEPolicyTransfer{
			// Sixteen octets: SIZE(16..65540), which the encoder checks.
			SUPI: IMSI("262011"), UEPolicy: make(UEPolicy, 16),
		}, "302981050413120f01a220bf81121ca1088106323632303131861000000000000000000000000000000000"},
		{"ueServiceAccept", AMFUEServiceAccept{
			UserIdentifiers: sampleIdentifiers(), ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		}, "305081050413120f01a247bf811343a13ca13aa138a111810f323632303139383736353433323130a312821033353334323530303030303030313531a40f810d34393135313233343536373839a20382014e"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertEncodes(t, tc.event, tc.want)
		})
	}
}
