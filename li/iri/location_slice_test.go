// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"encoding/asn1"
	"encoding/hex"
	"strings"
	"testing"
)

// The members below are the user location, the slices, the 5GMM capability, the
// de-registration type bits and the service accept's two PSI bitmaps. Their tags are held to the
// module by asn1_drift_test.go and emit_tags_test.go, and their encodings by the golden
// fixtures; the tests here state each one's bytes by hand, where a regression in one member
// reads plainer than in a whole-record vector.

// nrUserLocation is a Location carrying only an NR user location: TAI 262-01 / 000001 and the
// given cell, with no network identifiers and no currentLoc.
func nrUserLocation(cell NRCellID) Location {
	plmn := PLMNID{MCC: "262", MNC: "01"}
	return Location{LocationInfo: LocationInfo{UserLocation: UserLocation{NRLocation: NRLocation{
		TAI:  TAI{PLMNID: plmn, TAC: TAC{0x00, 0x00, 0x01}},
		NCGI: NCGI{PLMNID: plmn, NRCellID: cell},
	}}}}
}

// plmnTLV is PLMNID 262-01 as the member [1] it is in both TAI and NCGI.
const plmnTLV = "a109" + "8103323632" + "82023031"

// TestNRCellIDIsWrittenAsThirtySixBits: NRCellID ::= BIT STRING (SIZE(36)). The cell identity is
// held as its value and must go out as a bit string of exactly 36 bits — five octets, the value
// left-aligned, one octet saying four bits are unused, and those four bits zero, as DER requires.
// The expected octets are derived bit by bit here rather than by the emitter's shift, and the
// element is read back with encoding/asn1 to show a decoder recovers the same cell.
func TestNRCellIDIsWrittenAsThirtySixBits(t *testing.T) {
	for _, cell := range []NRCellID{0, 1, 0x123456789, 0x800000001, 1<<36 - 1} {
		want := make([]byte, 5)
		for i := range 36 {
			if cell>>(35-i)&1 == 1 {
				want[i/8] |= 0x80 >> (i % 8)
			}
		}

		der, err := EncodeXIRI(AMFLocationUpdate{SUPI: IMSI("262019876543210"), Location: nrUserLocation(cell)})
		if err != nil {
			t.Fatalf("cell %#x: %v", cell, err)
		}
		// nCGI [2] { pLMNID [1], nRCellID [2] BIT STRING, four unused bits }.
		tlv := "8206" + "04" + hex.EncodeToString(want)
		if !containsTLV(der, "a213"+plmnTLV+tlv, nil) {
			t.Errorf("cell %#x: no nCGI [2] { pLMNID, [2] %s } in % x", cell, tlv, der)

			continue
		}

		raw, err := hex.DecodeString(tlv)
		if err != nil {
			t.Fatal(err)
		}
		var bits asn1.BitString
		if _, err := asn1.UnmarshalWithParams(raw, &bits, "tag:2"); err != nil {
			t.Fatalf("cell %#x: the nRCellID element does not decode as a BIT STRING: %v", cell, err)
		}
		var got NRCellID
		for i := range bits.BitLength {
			got = got<<1 | NRCellID(bits.At(i))
		}
		if bits.BitLength != 36 || got != cell {
			t.Errorf("cell %#x decodes as %d bits holding %#x", cell, bits.BitLength, got)
		}
	}
}

// TestTheNewMembersAreWrittenAtTheirTags states each added member's element by hand: the
// module's tag, its form, and its content.
func TestTheNewMembersAreWrittenAtTheirTags(t *testing.T) {
	guti := FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, AMFPointer: 1, FiveGTMSI: 1}
	sst := func(n SliceServiceType) SNSSAI { return SNSSAI{SliceServiceType: n} }
	// An S-NSSAI as a SEQUENCE OF element: a universal SEQUENCE around sliceServiceType [1].
	snssaiTLV := func(n byte) string { return "3003" + "8101" + hex.EncodeToString([]byte{n}) }
	// location: locationInfo [1] { userLocation [1] { nRLocation [2] { tAI [1], nCGI [2] } } },
	// for cell 1.
	userLocation := "a12b" + "a129" + "a227" + "a110" + plmnTLV + "8203000001" + "a213" + plmnTLV + "8206040000000010"
	location := func(tag string) string { return tag + "2d" + userLocation }

	for _, tc := range []struct {
		name  string
		event any
		want  []string
	}{
		{"AMFRegistration: slice [3], location [9], fiveGMMCapability [24]", AMFRegistration{
			RegistrationType: RegTypeMobility, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: guti,
			Slice: Slice{
				AllowedNSSAI:    NSSAI{sst(1)},
				ConfiguredNSSAI: NSSAI{sst(2)},
				RejectedNSSAI:   RejectedNSSAI{{CauseValue: 1, SNSSAI: sst(3)}},
			},
			Location:          nrUserLocation(1),
			FiveGMMCapability: FiveGMMCapability{0x07, 0x00, 0x01},
		}, []string{
			// slice [3] { allowedNSSAI [1] {…}, configuredNSSAI [2] {…},
			//   rejectedNSSAI [3] { SEQUENCE { causeValue [1] 1, sNSSAI [2] { [1] 3 } } } }
			"a31a" + "a105" + snssaiTLV(1) + "a205" + snssaiTLV(2) + "a30a" + "3008" + "810101" + "a203" + "810103",
			location("a9"),
			"9803070001",
		}},
		{"AMFStartOfInterceptionWithRegisteredUE: slice [3], location [9]", AMFStartOfInterceptionWithRegisteredUE{
			RegistrationResult: RegResult3GPPAccess, SUPI: IMSI("262019876543210"), GUTI: guti,
			Slice:    Slice{AllowedNSSAI: NSSAI{sst(1)}},
			Location: nrUserLocation(1),
		}, []string{"a307" + "a105" + snssaiTLV(1), location("a9")}},
		{"AMFDeregistration: location [9], switchOffIndicator [10], reRegRequiredIndicator [11]", AMFDeregistration{
			DeregistrationDirection: DirUEInitiated, AccessType: AccessThreeGPP,
			Location:               nrUserLocation(1),
			SwitchOffIndicator:     SwitchOffNormalDetach,
			ReRegRequiredIndicator: ReRegistrationRequired,
		}, []string{location("a9"), "8a0101", "8b0101"}},
		{"AMFIdentifierDeassociation: location [6]", AMFIdentifierDeassociation{
			SUPI: IMSI("262019876543210"), GUTI: guti, Location: nrUserLocation(1),
		}, []string{location("a6")}},
		{"AMFUEServiceAccept: uplinkDataStatus [5], pDUSessionStatus [6]", AMFUEServiceAccept{
			UserIdentifiers:        sampleIdentifiers(),
			ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
			UplinkDataStatus:       UplinkDataStatus{0x20, 0x00},
			PDUSessionStatus:       PDUSessionStatus{0x22, 0x00, 0x00},
		}, []string{"85022000", "8603220000"}},
		{"AMFUnsuccessfulProcedure: requestedSlice [3]", AMFUnsuccessfulProcedure{
			FailedProcedureType: FailedRegistration, FailureCause: FiveGMMCause(62),
			RequestedSlice: NSSAI{sst(1), sst(2)},
		}, []string{"a30a" + snssaiTLV(1) + snssaiTLV(2)}},
		{"SMFUnsuccessfulProcedure: requestedSlice [4], location [19]", SMFUnsuccessfulProcedure{
			FailedProcedureType: SMFFailedPDUSessionEstablishment, FailureCause: FiveGSMCause(26),
			Initiator: InitiatorNetwork, RequestedSlice: NSSAI{sst(1)}, Location: nrUserLocation(1),
		}, []string{"a405" + snssaiTLV(1), location("b3")}},
		{"SMFPDUSessionEstablishment: location [11]", SMFPDUSessionEstablishment{
			PDUSessionID: 5, GTPTunnelID: FTEID{TEID: 1}, PDUSessionType: PDUSessionTypeIPv4, DNN: "internet",
			RequestType: SMRequestInitial, GTPTunnelInfo: someTunnelInfo, Location: nrUserLocation(1),
		}, []string{location("ab")}},
		{"SMFPDUSessionModification: location [7]", SMFPDUSessionModification{
			RequestType: SMRequestModification, GTPTunnelInfo: someTunnelInfo, Location: nrUserLocation(1),
		}, []string{location("a7")}},
		{"SMFPDUSessionRelease: location [9]", SMFPDUSessionRelease{
			SUPI: IMSI("262019876543210"), PDUSessionID: 5, Location: nrUserLocation(1),
		}, []string{location("a9")}},
		{"SMFStartOfInterceptionWithEstablishedPDUSession: location [11]", SMFStartOfInterceptionWithEstablishedPDUSession{
			PDUSessionID: 5, GTPTunnelID: FTEID{TEID: 1}, PDUSessionType: PDUSessionTypeIPv4, DNN: "internet",
			RequestType: SMRequestExisting, GTPTunnelInfo: someTunnelInfo, Location: nrUserLocation(1),
		}, []string{location("ab")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			der, err := EncodeXIRI(tc.event)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !containsTLV(der, want, nil) {
					t.Errorf("no element %s in % x", want, der)
				}
			}
		})
	}
}

// TestAMandatoryLocationMayBeAUserLocationAlone: the payload tables ask a location to be encoded
// with locationInfo.userLocation, so a mandatory location carrying a user location and no
// currentLoc is a location, not an empty one; and the empty one is still refused.
func TestAMandatoryLocationMayBeAUserLocationAlone(t *testing.T) {
	guti := FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, AMFPointer: 1, FiveGTMSI: 1}
	for name, build := range map[string]func(Location) any{
		"AMFLocationUpdate": func(l Location) any {
			return AMFLocationUpdate{SUPI: IMSI("262019876543210"), Location: l}
		},
		"AMFIdentifierAssociation": func(l Location) any {
			return AMFIdentifierAssociation{SUPI: IMSI("262019876543210"), GUTI: guti, Location: l}
		},
	} {
		if _, err := EncodeXIRI(build(nrUserLocation(7))); err != nil {
			t.Errorf("%s with a user location only was refused: %v", name, err)
		}
		if _, err := EncodeXIRI(build(Location{})); err == nil {
			t.Errorf("%s with an empty Location was encoded", name)
		}
	}
}

// TestAPresentEmptyNSSAIIsWritten: NSSAI ::= SEQUENCE OF SNSSAI has no SIZE, so an empty list is
// a value. Like TAIList, an OPTIONAL NSSAI is written exactly when it is non-nil: nil is absent,
// and an empty list goes out as an empty one rather than being dropped.
//
// The nil forms are the mandatory-only golden fixtures'.
func TestAPresentEmptyNSSAIIsWritten(t *testing.T) {
	der, err := EncodeXIRI(AMFRegistration{
		RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
		SUPI: IMSI("262019876543210"), GUTI: testGUTI, Slice: Slice{AllowedNSSAI: NSSAI{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// slice [3] { allowedNSSAI [1] {} }.
	if !containsTLV(der, "a302a100", nil) {
		t.Errorf("an empty allowed NSSAI is not written as [1] {} inside slice [3]: % x", der)
	}

	// failedProcedureType [1], failureCause [2] { fiveGMMCause [1] 62 }, requestedSlice [3] {}.
	assertEncodes(t, AMFUnsuccessfulProcedure{
		FailedProcedureType: FailedRegistration, FailureCause: FiveGMMCause(62), RequestedSlice: NSSAI{},
	}, "301581050413120f01a20ca50a810101a20381013ea300")
}

// TestTheNewLeavesAreCheckedAgainstTheirDefinitions reaches each added restricted leaf through a
// record that carries it. TestEveryConstraintBoundIsEnforced holds every table entry to the
// module at its bounds; this shows each entry is reached where the leaf actually sits.
func TestTheNewLeavesAreCheckedAgainstTheirDefinitions(t *testing.T) {
	accept := func(uplink UplinkDataStatus, status PDUSessionStatus) AMFUEServiceAccept {
		return AMFUEServiceAccept{
			UserIdentifiers: sampleIdentifiers(), ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
			UplinkDataStatus: uplink, PDUSessionStatus: status,
		}
	}
	registration := func(capability FiveGMMCapability, slice Slice, loc Location) AMFRegistration {
		return AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: testGUTI,
			FiveGMMCapability: capability, Slice: slice, Location: loc,
		}
	}
	deregistration := func(off SwitchOffIndicator, rereg ReRegRequiredIndicator) AMFDeregistration {
		return AMFDeregistration{
			DeregistrationDirection: DirUEInitiated, AccessType: AccessThreeGPP,
			SwitchOffIndicator: off, ReRegRequiredIndicator: rereg,
		}
	}
	withNCGINID := func(nid NID) Location {
		l := nrUserLocation(1)
		l.LocationInfo.UserLocation.NRLocation.NCGI.NID = nid
		return l
	}

	for _, tc := range []struct {
		name  string
		event any
		want  string
	}{
		{"a one-octet uplink data status", accept(UplinkDataStatus{0x20}, nil), "UplinkDataStatus is defined as SIZE(2..32)"},
		{"a 33-octet uplink data status", accept(make(UplinkDataStatus, 33), nil), "UplinkDataStatus is defined as SIZE(2..32)"},
		{"a present but empty PDU session status", accept(nil, PDUSessionStatus{}), "PDUSessionStatus is defined as SIZE(2..32)"},
		{"a 33-octet PDU session status", accept(nil, make(PDUSessionStatus, 33)), "PDUSessionStatus is defined as SIZE(2..32)"},
		{"a 14-octet 5GMM capability", registration(make(FiveGMMCapability, 14), Slice{}, Location{}), "FiveGMMCapability is defined as SIZE(1..13)"},
		{"a present but empty 5GMM capability", registration(FiveGMMCapability{}, Slice{}, Location{}), "FiveGMMCapability is defined as SIZE(1..13)"},
		{"a rejected S-NSSAI cause above its range", registration(nil, Slice{
			RejectedNSSAI: RejectedNSSAI{{CauseValue: 256, SNSSAI: SNSSAI{SliceServiceType: 1}}},
		}, Location{}), "RejectedSliceCauseValue is defined as INTEGER (0..255)"},
		{"an S-NSSAI in a slice list with a short differentiator", registration(nil, Slice{
			AllowedNSSAI: NSSAI{{SliceServiceType: 1, SliceDifferentiator: SliceDifferentiator{0x01}}},
		}, Location{}), "SliceDifferentiator is defined as SIZE(3)"},
		{"a cell identity one bit too wide", registration(nil, Slice{}, nrUserLocation(1<<36)), "NRCellID is defined as BIT STRING (SIZE(36))"},
		{"a negative cell identity", registration(nil, Slice{}, nrUserLocation(-1)), "NRCellID is defined as BIT STRING (SIZE(36))"},
		{"a cell's network identifier that is not eleven characters", registration(nil, Slice{}, withNCGINID("short")), "NID is defined as UTF8String (SIZE(11))"},
		{"a switch-off indicator outside its enumeration", deregistration(3, 0), "SwitchOffIndicator is an ENUMERATED with values 1..2"},
		{"a re-registration indicator outside its enumeration", deregistration(0, 3), "ReRegRequiredIndicator is an ENUMERATED with values 1..2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil {
				t.Fatal("encoded a value its definition forbids")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal does not name the leaf and its definition.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}

	// The conformant forms, at the bounds, must pass, or the cases above prove nothing.
	for name, event := range map[string]any{
		"status bitmaps at two and 32 octets": accept(UplinkDataStatus{0x20, 0x00}, make(PDUSessionStatus, 32)),
		"a 5GMM capability at one and a cell at the widest": registration(FiveGMMCapability{0x07}, Slice{
			RejectedNSSAI: RejectedNSSAI{{CauseValue: 255, SNSSAI: SNSSAI{SliceServiceType: 1}}},
		}, nrUserLocation(1<<36-1)),
		"both de-registration bits at their last value": deregistration(SwitchOffSwitchOff, ReRegistrationNotRequired),
		"neither de-registration bit, which is absence": deregistration(0, 0),
	} {
		if _, err := EncodeXIRI(event); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}
