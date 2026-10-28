// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"bytes"
	"encoding/hex"
	"net"
	"testing"
)

func sampleRegistration() AMFRegistration {
	return AMFRegistration{
		RegistrationType:   RegTypeInitial,
		RegistrationResult: RegResult3GPPAccess,
		SUPI:               IMSI("262019876543210"),
		PEI:                IMEI("35342500000001"),
		GPSI:               MSISDN("4915123456789"),
		GUTI: FiveGGUTI{
			MCC:         "262",
			MNC:         "01",
			AMFRegionID: 200,
			AMFSetID:    1,
			AMFPointer:  0,
			FiveGTMSI:   3735928559, // 0xDEADBEEF, exercises the int64 range
		},
	}
}

// assertEncodes pins a record's complete encoding against fixed hex, compared byte for
// byte. The fixtures are never regenerated from this encoder, so a change to an encoding
// fails here and has to be justified. A decode round trip cannot stand in for this — it
// still passes when encode and decode change together, which is exactly the failure that
// corrupts a receiver while looking healthy from in here.
func assertEncodes(t *testing.T, event any, want string) []byte {
	t.Helper()
	der, err := EncodeXIRI(event)
	if err != nil {
		t.Fatalf("EncodeXIRI(%T): %v", event, err)
	}
	if got := hex.EncodeToString(der); got != want {
		t.Errorf("%T encoding changed\n got  %s\n want %s", event, got, want)
	}
	return der
}

// containsTLV reports whether der carries the element whose identifier and length
// octets are header, followed by content. Used where the assertion is about one member
// and a hand-derived element states it more plainly than a whole-record fixture.
func containsTLV(der []byte, header string, content []byte) bool {
	h, err := hex.DecodeString(header)
	if err != nil {
		panic(err)
	}
	return bytes.Contains(der, append(h, content...))
}

func TestEncodeXIRI(t *testing.T) {
	assertEncodes(t, sampleRegistration(), "306381050413120f01a25aa158810101820101a411810f323632303139383736353433323130a610810e3335333432353030303030303031a70f810d34393135313233343536373839a81a810332363282023031830200c8840101850100860500deadbeef")
}

// TestAbsentOptionalChoice verifies that absent (nil) optional CHOICE fields are
// omitted, not encoded.
func TestAbsentOptionalChoice(t *testing.T) {
	reg := sampleRegistration()
	reg.PEI = nil  // optional, absent
	reg.GPSI = nil // optional, absent

	der := assertEncodes(t, reg, "304081050413120f01a237a135810101820101a411810f323632303139383736353433323130a81a810332363282023031830200c8840101850100860500deadbeef")
	// Absent optionals must shrink the encoding versus the all-present sample.
	full := assertEncodes(t, sampleRegistration(), "306381050413120f01a25aa158810101820101a411810f323632303139383736353433323130a610810e3335333432353030303030303031a70f810d34393135313233343536373839a81a810332363282023031830200c8840101850100860500deadbeef")
	if len(der) >= len(full) {
		t.Errorf("absent-optional encoding (%d) not smaller than full (%d)", len(der), len(full))
	}
}

func TestDeregistrationEncoding(t *testing.T) {
	assertEncodes(t, AMFDeregistration{
		DeregistrationDirection: DirUEInitiated,
		AccessType:              AccessThreeGPP,
		SUPI:                    IMSI("262019876543210"),
		GUTI:                    FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 200, AMFSetID: 1, FiveGTMSI: 42},
	}, "303c81050413120f01a233a231810102820101a311810f323632303139383736353433323130a716810332363282023031830200c884010185010086012a")
}

func TestStartOfInterceptionEncoding(t *testing.T) {
	assertEncodes(t, AMFStartOfInterceptionWithRegisteredUE{
		RegistrationResult: RegResult3GPPAccess,
		RegistrationType:   RegTypeInitial,
		SUPI:               IMSI("262019876543210"),
		GUTI:               FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 7},
	}, "303b81050413120f01a232a430810101820101a411810f323632303139383736353433323130a815810332363282023031830101840101850100860107")
}

// TestEventDiscrimination confirms each event is carried under its own XIRIEvent
// alternative (no cross-talk between record kinds): the identifier octets after the
// event [2] wrapper are the record's tag.
func TestEventDiscrimination(t *testing.T) {
	cases := []struct {
		name  string
		event any
		want  string
	}{
		{"registration", sampleRegistration(), "306381050413120f01a25aa158810101820101a411810f323632303139383736353433323130a610810e3335333432353030303030303031a70f810d34393135313233343536373839a81a810332363282023031830200c8840101850100860500deadbeef"},
		{"deregistration", AMFDeregistration{DeregistrationDirection: DirNetworkInitiated, AccessType: AccessBoth}, "301181050413120f01a208a206810101820103"},
		{"startOfInterception", AMFStartOfInterceptionWithRegisteredUE{RegistrationResult: RegResult3GPPAccess, SUPI: IMSI("262011"), GUTI: FiveGGUTI{MCC: "262", MNC: "01"}}, "302f81050413120f01a226a424810101a4088106323632303131a815810332363282023031830100840100850100860100"},
		{"smfStartOfInterception", SMFStartOfInterceptionWithEstablishedPDUSession{SUPI: IMSI("262011"), PDUSessionID: 5, PDUSessionType: PDUSessionTypeIPv4, UEEndpoint: UEEndpoint(net.ParseIP("10.45.0.2")), DNN: "internet", RequestType: SMRequestExisting}, "303581050413120f01a22ca92aa1088106323632303131850105a603810100870101a90681040a2d00028c08696e7465726e65748f0102"},
		{"identifierAssociation", AMFIdentifierAssociation{SUPI: IMSI("262011"), GUTI: FiveGGUTI{MCC: "262", MNC: "01"}, Location: Location{LocationInfo: LocationInfo{CurrentLocation: true}}}, "303481050413120f01a22bbf3e28a1088106323632303131a515810332363282023031830100840100850100860100a605a1038201ff"},
		{"identifierDeassociation", AMFIdentifierDeassociation{SUPI: IMSI("262011"), GUTI: FiveGGUTI{MCC: "262", MNC: "01"}}, "302e81050413120f01a225bf813a21a1088106323632303131a515810332363282023031830100840100850100860100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertEncodes(t, tc.event, tc.want)
		})
	}
}

// TestIdentifierAssociationEncoding covers both identifier-association records. Their
// XIRIEvent tags (62 and 186) exceed 30, so this is also the high-tag-number
// (long-form) coverage: the identifier octet is context+constructed+0x1f = 0xBF,
// followed by the base-128 tag continuation.
func TestIdentifierAssociationEncoding(t *testing.T) {
	guti := FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 42}

	der := assertEncodes(t, AMFIdentifierAssociation{
		SUPI:     IMSI("262019876543210"),
		PEI:      IMEI("35342500000001"),
		GPSI:     MSISDN("4915123456789"),
		GUTI:     guti,
		Location: Location{LocationInfo: LocationInfo{CurrentLocation: true}},
	}, "306081050413120f01a257bf3e54a111810f323632303139383736353433323130a310810e3335333432353030303030303031a40f810d34393135313233343536373839a51581033236328202303183010184010185010086012aa605a1038201ff")
	// The [62] alternative must appear on the wire in high-tag-number long form:
	// 0xBF (context|constructed|0x1f) followed by 0x3E (=62 in one octet).
	if !bytes.Contains(der, []byte{0xBF, 0x3E}) {
		t.Errorf("association DER missing high-tag-number form for [62]: % x", der)
	}

	// Deassociation: tag 186 = 1×128 + 58, so two continuation octets 0x81 0x3A
	// after the 0xBF introducer.
	der = assertEncodes(t, AMFIdentifierDeassociation{SUPI: IMSI("262019876543210"), GUTI: guti}, "303781050413120f01a22ebf813a2aa111810f323632303139383736353433323130a51581033236328202303183010184010185010086012a")
	if !bytes.Contains(der, []byte{0xBF, 0x81, 0x3A}) {
		t.Errorf("deassociation DER missing high-tag-number form for [186]: % x", der)
	}
}

func sampleEstablishment() SMFPDUSessionEstablishment {
	return SMFPDUSessionEstablishment{
		SUPI:           IMSI("262019876543210"),
		PDUSessionID:   5,
		GTPTunnelID:    FTEID{TEID: 3735928559, IPv4Address: []byte{10, 0, 0, 1}},
		PDUSessionType: PDUSessionTypeIPv4,
		SNSSAI:         SNSSAI{SliceServiceType: 1, SliceDifferentiator: []byte{0x00, 0x00, 0x7b}},
		DNN:            "internet",
		RequestType:    SMRequestInitial,
		AccessType:     AccessThreeGPP,
	}
}

// recordContents is a record's own encoding — its members, without the XIRIPayload
// and event [2] wrappers around it.
func recordContents(t *testing.T, emit func(*builder)) []byte {
	t.Helper()
	var b builder
	emit(&b)
	if b.err != nil {
		t.Fatalf("emit: %v", b.err)
	}
	return b.buf
}

// TestIdentifierRecordMandatoryTags pins the field tags of the two identifier
// records against TS 33.128, where a wrong tag costs the most and a round trip
// through one encoder and decoder cannot see it, because the two agree with each other:
//
//   - AMFIdentifierAssociation carries mandatory location [6]; without it every
//     record fails schema validation at a conformant receiver.
//   - AMFIdentifierDeassociation carries gUTI under [5]. Under [2], which is sUCI in
//     that record, the 5G-GUTI would not merely be missing but misread as a
//     different identifier.
//
// Asserting on the encoded tags is what catches this class; a decode assertion
// cannot.
func TestIdentifierRecordMandatoryTags(t *testing.T) {
	guti := FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 42}

	// The records' own contents rather than the wrapped payload: XIRIPayload carries
	// its own event [2], so a tag check over the wrapped bytes cannot tell that apart
	// from a sUCI [2] inside the record.
	assoc := recordContents(t, AMFIdentifierAssociation{
		SUPI:     IMSI("262019876543210"),
		GUTI:     guti,
		Location: Location{LocationInfo: LocationInfo{CurrentLocation: true}},
	}.emit)
	// location [6] constructed: context|constructed|6 = 0xA6.
	if !bytes.Contains(assoc, []byte{0xA6}) {
		t.Errorf("association is missing mandatory location [6]: % x", assoc)
	}

	deassoc := recordContents(t, AMFIdentifierDeassociation{
		SUPI: IMSI("262019876543210"),
		GUTI: guti,
	}.emit)
	// gUTI [5] constructed = 0xA5. [2] is sUCI in this record, so the GUTI must
	// not be emitted there.
	if !bytes.Contains(deassoc, []byte{0xA5}) {
		t.Errorf("deassociation is missing mandatory gUTI [5]: % x", deassoc)
	}
	if bytes.Contains(deassoc, []byte{0xA2}) {
		t.Errorf("deassociation emits [2] (sUCI); the GUTI belongs at [5]: % x", deassoc)
	}
}

func TestSMFEstablishmentEncoding(t *testing.T) {
	assertEncodes(t, sampleEstablishment(), "304d81050413120f01a244a642a111810f323632303139383736353433323130850105a60d810500deadbeef82040a000001870101a808810101820300007b8c08696e7465726e65748f0101900101")
}

func TestSMFModificationAndReleaseEncoding(t *testing.T) {
	assertEncodes(t, SMFPDUSessionModification{
		SUPI: IMSI("262019876543210"), RequestType: SMRequestModification, PDUSessionID: 5,
	}, "302481050413120f01a21ba719a111810f3236323031393837363534333231308801058b0105")
	assertEncodes(t, SMFPDUSessionRelease{
		SUPI: IMSI("262019876543210"), PDUSessionID: 5, UplinkVolume: new(int64(1024)), DownlinkVolume: new(int64(8192)),
	}, "302981050413120f01a220a81ea111810f3236323031393837363534333231308401058702040088022000")
}

func TestLocationUpdateEncoding(t *testing.T) {
	assertEncodes(t, AMFLocationUpdate{
		SUPI:     IMSI("262019876543210"),
		GUTI:     FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 9},
		Location: Location{LocationInfo: LocationInfo{CurrentLocation: true}},
	}, "303c81050413120f01a233a331a111810f323632303139383736353433323130a515810332363282023031830101840101850100860109a605a1038201ff")
}

func TestUnsuccessfulProcedureEncoding(t *testing.T) {
	der := assertEncodes(t, AMFUnsuccessfulProcedure{
		FailedProcedureType: FailedRegistration,
		FailureCause:        FiveGMMCause(7),
		SUPI:                IMSI("262019876543210"),
	}, "302681050413120f01a21da51b810101a203810107a411810f323632303139383736353433323130")
	// failureCause [2] EXPLICIT around fiveGMMCause [1] 7.
	if !containsTLV(der, "a203810107", nil) {
		t.Errorf("failureCause is not [2] { [1] 7 }: % x", der)
	}
}

// TestMissingMandatoryErrors verifies that a nil MANDATORY field is a loud error,
// not a silently truncated record.
func TestMissingMandatoryErrors(t *testing.T) {
	reg := sampleRegistration()
	reg.SUPI = nil // mandatory — must not be silently dropped

	if _, err := EncodeXIRI(reg); err == nil {
		t.Fatal("expected an error encoding a record with a nil mandatory SUPI, got nil")
	}
}
