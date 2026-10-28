// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"encoding/hex"
	"net"
	"strings"
	"testing"
)

var testGUTI = FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 1}

// TestAMandatoryMemberCannotBeSentEmpty: the emitter writes a mandatory member whatever it
// holds, so an empty one would go out as a zero-length value its own type forbids. The
// constraint walk refuses it first.
func TestAMandatoryMemberCannotBeSentEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event any
		want  string
	}{
		{"a zero GUTI where the GUTI is mandatory", AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"),
		}, "GUTI.MCC is empty"},
		{"a nil UE policy", AMFUEPolicyTransfer{
			SUPI: IMSI("262019876543210"),
		}, "UEPolicy carries no octets"},
		{"an empty LCS correlation id", AMFPositioningInfoTransfer{
			SUPI: IMSI("262019876543210"),
		}, "LCSCorrelationID is empty"},
		{"a tracking area with no TAC", AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: testGUTI,
			FiveGSTAIList: TAIList{{PLMNID: PLMNID{MCC: "262", MNC: "01"}}},
		}, "TAC carries no octets"},
		{"a present but empty slice differentiator", SMFPDUSessionModification{
			SNSSAI:      SNSSAI{SliceServiceType: 1, SliceDifferentiator: SliceDifferentiator{}},
			RequestType: SMRequestModification,
		}, "SliceDifferentiator carries no octets"},
		{"a present but empty subscriber-identity list", AMFUEServiceAccept{
			UserIdentifiers:        UserIdentifiers{FiveGS: FiveGSSubscriberIDs{IDs: []any{}}},
			ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		}, "SEQUENCE SIZE(1..MAX)"},
		{"an empty endpoint address in a list", SMFPDUSessionEstablishment{
			PDUSessionID: 5, PDUSessionType: PDUSessionTypeIPv4, DNN: "internet",
			RequestType: SMRequestInitial, UEEndpoint: []any{IPv4Address(nil)},
		}, "carries no octets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil {
				t.Fatal("encoded a mandatory member as an empty value its type forbids")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal does not say what is wrong.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}
}

// TestIdentityLeavesAreCheckedAgainstTheirOwnTypes: a record's SUPI, PEI and GPSI come from the
// network function's own context, not from a validated task, so nothing else stops a value
// that violates its type — a fifteen-digit IMEI, check digit included, where TS 33.128
// defines fourteen.
func TestIdentityLeavesAreCheckedAgainstTheirOwnTypes(t *testing.T) {
	release := func(supi, pei, gpsi any) SMFPDUSessionRelease {
		return SMFPDUSessionRelease{SUPI: supi, PEI: pei, GPSI: gpsi, PDUSessionID: 5}
	}

	for _, tc := range []struct {
		name  string
		event any
		want  string
	}{
		{"an empty mandatory SUPI", release(IMSI(""), nil, nil), "IMSI is defined as NumericString (SIZE(6..15))"},
		{"a five-digit IMSI", release(IMSI("26201"), nil, nil), "SIZE(6..15)"},
		{"a sixteen-digit IMSI", release(IMSI("2620198765432101"), nil, nil), "SIZE(6..15)"},
		{"an IMSI with its TS 29.571 prefix", release(IMSI("imsi-262019876543210"), nil, nil), "SIZE(6..15)"},
		{"an IMSI with a letter", release(IMSI("26201987654321a"), nil, nil), "is an identity of digits only"},
		{"an IMEI with its check digit", release(IMSI("262019876543210"), IMEI("356938035643809"), nil), "IMEI is defined as NumericString (SIZE(14))"},
		{"a fifteen-digit IMEISV", release(IMSI("262019876543210"), IMEISV("356938035643809"), nil), "SIZE(16)"},
		{"a sixteen-digit MSISDN", release(IMSI("262019876543210"), nil, MSISDN("4915123456789012")), "SIZE(1..15)"},
		{"an MSISDN in E.164 + form", release(IMSI("262019876543210"), nil, MSISDN("+4915123456789")), "is an identity of digits only"},
		{"a bad identity inside userIdentifiers", AMFUEServiceAccept{
			UserIdentifiers:        Identifiers(IMSI("262019876543210"), IMEI("356938035643809"), nil),
			ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		}, "IMEI is defined as"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil {
				t.Fatal("encoded an identity its own type forbids")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal does not name the leaf and its definition.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}

	// The conformant forms must pass, or the cases above prove nothing.
	if _, err := EncodeXIRI(release(IMSI("262019876543210"), IMEI("35693803564380"), MSISDN("4915123456789"))); err != nil {
		t.Errorf("conformant identities were refused: %v", err)
	}
	if _, err := EncodeXIRI(release(IMSI("262011"), IMEISV("3569380356438091"), MSISDN("1"))); err != nil {
		t.Errorf("conformant identities at the bounds were refused: %v", err)
	}
}

// TestEncodeXIRIRefusesNoEvent: a nil event is an error, not a panic in the constraint walk.
func TestEncodeXIRIRefusesNoEvent(t *testing.T) {
	if _, err := EncodeXIRI(nil); err == nil {
		t.Error("a nil event was encoded")
	}
}

// TestAPointerToARecordIsRefused: the XIRIEvent CHOICE takes record values, and the checks on the
// encode path are keyed on the value types, so a pointer is refused by name rather than
// dereferenced.
func TestAPointerToARecordIsRefused(t *testing.T) {
	_, err := EncodeXIRI(&AMFRegistration{
		RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
		SUPI: IMSI("262019876543210"), GUTI: testGUTI,
	})
	if err == nil || !strings.Contains(err.Error(), "not an alternative of the XIRIEvent CHOICE") {
		t.Errorf("a pointer to a record was not refused as such: %v", err)
	}
}

// TestARefusalNamesTheRecordOnce: a refusal names the record by its bare type name, the way the
// emitter's own refusals do, rather than repeating the package qualifier after the "iri:" prefix.
func TestARefusalNamesTheRecordOnce(t *testing.T) {
	_, err := EncodeXIRI(AMFRegistration{
		RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
		SUPI: IMSI("262019876543210"),
		GUTI: FiveGGUTI{MCC: "2620", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 1},
	})
	if err == nil {
		t.Fatal("a four-digit MCC was encoded")
	}
	if !strings.HasPrefix(err.Error(), "iri: AMFRegistration.GUTI.MCC ") {
		t.Errorf("the refusal does not name the field as AMFRegistration.GUTI.MCC: %v", err)
	}
}

// TestUTF8StringsAreValidAndMandatoryOnesNonEmpty: a UTF8String member must carry valid UTF-8,
// which a strict decoder checks, and a mandatory one must not be empty — the module bounds the
// size of none of DNN, NAI or homeNetworkIdentifier, so an empty one is schema-valid, but a
// session with no data network name or a SUPI with no characters names nothing.
func TestUTF8StringsAreValidAndMandatoryOnesNonEmpty(t *testing.T) {
	establishment := func(dnn DNN) SMFPDUSessionEstablishment {
		return SMFPDUSessionEstablishment{
			SUPI: IMSI("262019876543210"), PDUSessionID: 5, PDUSessionType: PDUSessionTypeIPv4,
			DNN: dnn, RequestType: SMRequestInitial,
		}
	}
	registration := func(supi any) AMFRegistration {
		return AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: supi, GUTI: testGUTI,
		}
	}
	suci := SUCI{
		MCC: "262", MNC: "01", HomeNetworkPublicKeyID: []byte{0x00}, SchemeOutput: []byte{0x01},
		HomeNetworkIdentifier: "\xff",
	}

	for _, tc := range []struct {
		name  string
		event any
		want  string
	}{
		{"a DNN that is not UTF-8", establishment("\xff\xfe"), "DNN is not valid UTF-8"},
		{"an empty mandatory DNN", establishment(""), "DNN is empty, and DNN is mandatory"},
		{"an empty mandatory NAI SUPI", registration(NAI("")), "SUPI is empty, and NAI is mandatory"},
		{"an NAI that is not UTF-8", registration(NAI("user\xff@example.org")), "SUPI is not valid UTF-8, and NAI"},
		{"a home network identifier that is not UTF-8", AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: testGUTI, SUCI: suci,
		}, "HomeNetworkIdentifier is not valid UTF-8"},
		{"a network identifier that is not UTF-8", AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: testGUTI,
			FiveGSTAIList: TAIList{{PLMNID: PLMNID{MCC: "262", MNC: "01"}, TAC: TAC{0x00, 0x01}, NID: "0123456789\xff"}},
		}, "NID is not valid UTF-8"},
		{"an LCS correlation id that is not UTF-8", AMFPositioningInfoTransfer{
			SUPI: IMSI("262019876543210"), LCSCorrelationID: "\xff",
		}, "LCSCorrelationID is not valid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil {
				t.Fatal("encoded a UTF8String a conformant decoder would refuse, or one naming nothing")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal does not say what is wrong.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}

	// The conformant forms pass, an empty optional DNN is absence, and a UTF8String may carry any
	// valid UTF-8, not only ASCII.
	for _, ok := range []any{
		establishment("internet"),
		establishment("dnn-\u00e9"),
		registration(NAI("user@example.org")),
		SMFUnsuccessfulProcedure{
			FailedProcedureType: SMFFailedPDUSessionEstablishment, FailureCause: 26,
			Initiator: InitiatorNetwork,
		},
	} {
		if _, err := EncodeXIRI(ok); err != nil {
			t.Errorf("a conformant record was refused: %v", err)
		}
	}
}

// TestAPLMNIsDigitsOnly: NumericString admits a space, but an MCC or MNC is decimal digits
// throughout, and a space in one is a misconfigured PLMN rather than a value.
func TestAPLMNIsDigitsOnly(t *testing.T) {
	for _, guti := range []FiveGGUTI{
		{MCC: "262", MNC: "0 1", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 1},
		{MCC: "2 2", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 1},
	} {
		_, err := EncodeXIRI(AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: guti,
		})
		if err == nil || !strings.Contains(err.Error(), "decimal digits throughout") {
			t.Errorf("a PLMN with a space (%q/%q) was not refused: %v", guti.MCC, guti.MNC, err)
		}
	}
}

// TestRATTypeIsBoundedByTheModule: RATType is range-checked like every other enumeration.
func TestRATTypeIsBoundedByTheModule(t *testing.T) {
	rec := func(r RATType) AMFRegistration {
		return AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI: IMSI("262019876543210"), GUTI: testGUTI, RATType: r,
		}
	}

	for _, bad := range []RATType{34, 99, -1} {
		if _, err := EncodeXIRI(rec(bad)); err == nil {
			t.Errorf("RATType %d is not defined by the module and was encoded", bad)
		}
	}
	// 21 (wBEUTRANLEO) and 33 (nREREDCAP) are the module's own, though this package names
	// neither; 0 is absence.
	for _, ok := range []RATType{0, 1, 21, 33} {
		if _, err := EncodeXIRI(rec(ok)); err != nil {
			t.Errorf("RATType %d was refused: %v", ok, err)
		}
	}
}

// TestTheModificationEndpointIsOneAddress: SMFPDUSessionModification's uEEndpoint is the CHOICE
// itself. As a []any it could carry two alternatives under one [13], or none, and both encode as
// structures a conformant decoder refuses.
func TestTheModificationEndpointIsOneAddress(t *testing.T) {
	rec := func(ep any) SMFPDUSessionModification {
		return SMFPDUSessionModification{RequestType: SMRequestModification, UEEndpoint: ep}
	}

	der, err := EncodeXIRI(rec(UEEndpointAddress(net.ParseIP("10.45.0.2"))))
	if err != nil {
		t.Fatal(err)
	}
	// [13] EXPLICIT { [1] IMPLICIT OCTET STRING 0a2d0002 }.
	if !strings.Contains(hex.EncodeToString(der), "ad0681040a2d0002") {
		t.Errorf("the endpoint is not one [13]-wrapped alternative: %x", der)
	}

	// Absent: the record is exactly the eight octets of [13] { [1] four octets } shorter.
	without, err := EncodeXIRI(rec(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(der)-len(without) != 8 {
		t.Errorf("an absent endpoint was not omitted: %x against %x", without, der)
	}

	if _, err := EncodeXIRI(rec(UEEndpoint(net.ParseIP("10.45.0.2")))); err == nil {
		t.Error("a list was accepted where the module defines a single CHOICE")
	}
}

// TestAZeroVolumeAndTMSIAreSent: a release that moved no bytes, and a 5G-TMSI of zero, are
// values, not absences, so both are pointers and a present zero is sent.
func TestAZeroVolumeAndTMSIAreSent(t *testing.T) {
	zero := int64(0)

	der, err := EncodeXIRI(SMFPDUSessionRelease{
		SUPI: IMSI("262019876543210"), PDUSessionID: 5, UplinkVolume: &zero, DownlinkVolume: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hex.EncodeToString(der), "870100880100") {
		t.Errorf("zero volumes were not sent: %x", der)
	}

	der, err = EncodeXIRI(SMFPDUSessionRelease{SUPI: IMSI("262019876543210"), PDUSessionID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(hex.EncodeToString(der), "880100") {
		t.Errorf("absent volumes were sent: %x", der)
	}

	tmsi := FiveGTMSI(0)
	der, err = EncodeXIRI(AMFUEServiceAccept{
		UserIdentifiers:        sampleIdentifiers(),
		ServiceMessageIdentity: ServiceAcceptIdentity{0x4E},
		FiveGTMSI:              &tmsi,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hex.EncodeToString(der), "840100") {
		t.Errorf("a zero 5G-TMSI was not sent: %x", der)
	}
}

// TestAnAllZeroAMFIDIsSent: each of an AMF identifier's three leaves admits zero, so 000000 is
// an identifier an AMF can be configured with. The member is a pointer, so a present all-zero
// identifier is sent, and an absent one is not.
func TestAnAllZeroAMFIDIsSent(t *testing.T) {
	zero := &AMFID{}
	for name, build := range map[string]func(*AMFID) any{
		"SMFPDUSessionEstablishment": func(a *AMFID) any {
			return SMFPDUSessionEstablishment{
				PDUSessionID: 5, GTPTunnelID: FTEID{TEID: 1}, PDUSessionType: PDUSessionTypeIPv4,
				DNN: "internet", RequestType: SMRequestInitial, AMFID: a,
			}
		},
		"SMFStartOfInterceptionWithEstablishedPDUSession": func(a *AMFID) any {
			return SMFStartOfInterceptionWithEstablishedPDUSession{
				PDUSessionID: 5, GTPTunnelID: FTEID{TEID: 1}, PDUSessionType: PDUSessionTypeIPv4,
				DNN: "internet", RequestType: SMRequestExisting, AMFID: a,
			}
		},
		"SMFUnsuccessfulProcedure": func(a *AMFID) any {
			return SMFUnsuccessfulProcedure{
				FailedProcedureType: SMFFailedPDUSessionEstablishment, FailureCause: 26,
				Initiator: InitiatorNetwork, AMFID: a,
			}
		},
	} {
		// aMFID [13] { aMFRegionID [1] 0, aMFSetID [2] 0, aMFPointer [3] 0 }.
		const amfID = "ad09" + "810100" + "820100" + "830100"
		present, err := EncodeXIRI(build(zero))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(hex.EncodeToString(present), amfID) {
			t.Errorf("%s: an all-zero AMF identifier was not sent: %x", name, present)
		}
		absent, err := EncodeXIRI(build(nil))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(hex.EncodeToString(absent), "ad09") {
			t.Errorf("%s: an absent AMF identifier was sent: %x", name, absent)
		}
	}
}
