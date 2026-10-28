// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"bytes"
	"encoding/hex"
	"net"
	"strings"
	"testing"
)

// TestUEEndpointDiscriminatesAddressFamily covers the To4-before-To16 ordering.
// net.IP holds an IPv4 address in 16-byte 4-in-6 form, so To16 answers for both
// families; asking To4 first is what keeps a v4 address from being reported as
// IPv6 with a ::ffff: prefix an agency would have to unpick.
func TestUEEndpointDiscriminatesAddressFamily(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want any
	}{
		{"dotted quad", "10.45.0.2", IPv4Address{10, 45, 0, 2}},
		{"v4 in v6 form", "::ffff:10.45.0.2", IPv4Address{10, 45, 0, 2}},
		{"v6", "2001:db8::1", IPv6Address(net.ParseIP("2001:db8::1").To16())},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UEEndpoint(net.ParseIP(tc.ip))
			if len(got) != 1 {
				t.Fatalf("got %d entries, want 1: %#v", len(got), got)
			}
			switch want := tc.want.(type) {
			case IPv4Address:
				v4, ok := got[0].(IPv4Address)
				if !ok {
					t.Fatalf("got %T, want IPv4Address", got[0])
				}
				if !bytes.Equal(v4, want) {
					t.Errorf("got % x, want % x", v4, want)
				}
				if len(v4) != 4 {
					t.Errorf("IPv4Address is %d bytes, want the SIZE(4) form", len(v4))
				}
			case IPv6Address:
				v6, ok := got[0].(IPv6Address)
				if !ok {
					t.Fatalf("got %T, want IPv6Address", got[0])
				}
				if !bytes.Equal(v6, want) {
					t.Errorf("got % x, want % x", v6, want)
				}
				if len(v6) != 16 {
					t.Errorf("IPv6Address is %d bytes, want the SIZE(16) form", len(v6))
				}
			}
		})
	}
}

// TestUEEndpointAbsentAddress: no address means no list, not an empty one — and so does the
// unspecified address, which is what a session context holds before one is allocated. Reported,
// 0.0.0.0 or :: would name an endpoint no UE has.
func TestUEEndpointAbsentAddress(t *testing.T) {
	for _, ip := range []net.IP{nil, net.IPv4zero, net.IPv6unspecified, net.ParseIP("::ffff:0.0.0.0")} {
		if got := UEEndpoint(ip); got != nil {
			t.Errorf("UEEndpoint(%v) = %#v, want nil", ip, got)
		}
		if got := UEEndpointAddress(ip); got != nil {
			t.Errorf("UEEndpointAddress(%v) = %#v, want nil", ip, got)
		}
	}
}

// TestUEEndpointCarriesEveryAlternative checks each arm of the CHOICE is carried
// inside a real record under its own tag: uEEndpoint [9] { arm [n] address }.
func TestUEEndpointCarriesEveryAlternative(t *testing.T) {
	v6 := IPv6Address(net.ParseIP("2001:db8::1").To16())
	tests := []struct {
		name string
		addr any
		want string // uEEndpoint [9], then the arm's identifier and length
		body []byte
		fix  string
	}{
		{"ipv4", IPv4Address{10, 45, 0, 2}, "a9068104", []byte{10, 45, 0, 2}, "304481050413120f01a23ba939a111810f323632303139383736353433323130850105a60981010182040a141e28870101a90681040a2d00028c08696e7465726e65748f0102"},
		{"ipv6", v6, "a9128210", v6, "305081050413120f01a247a945a111810f323632303139383736353433323130850105a60981010182040a141e28870101a912821020010db80000000000000000000000018c08696e7465726e65748f0102"},
		{"mac", MACAddress{0x02, 0x42, 0xac, 0x11, 0x00, 0x02}, "a9088306", []byte{0x02, 0x42, 0xac, 0x11, 0x00, 0x02}, "304681050413120f01a23da93ba111810f323632303139383736353433323130850105a60981010182040a141e28870101a90883060242ac1100028c08696e7465726e65748f0102"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			der := assertEncodes(t, SMFStartOfInterceptionWithEstablishedPDUSession{
				SUPI:           IMSI("262019876543210"),
				PDUSessionID:   5,
				GTPTunnelID:    FTEID{TEID: 1, IPv4Address: []byte{10, 20, 30, 40}},
				PDUSessionType: PDUSessionTypeIPv4,
				UEEndpoint:     []any{tc.addr},
				DNN:            DNN("internet"),
				RequestType:    SMRequestExisting,
			}, tc.fix)
			if !containsTLV(der, tc.want, tc.body) {
				t.Errorf("uEEndpoint does not carry %s as %s: % x", tc.name, tc.want, der)
			}
		})
	}
}

// TestUEEndpointCarriesMultipleAddresses: the field is a SEQUENCE OF, so a
// dual-stack session can report both families. The SMF only tracks one address
// today, but the record shape is the specification's, not the SMF's.
func TestUEEndpointCarriesMultipleAddresses(t *testing.T) {
	v6 := IPv6Address(net.ParseIP("2001:db8::1").To16())
	der := assertEncodes(t, SMFStartOfInterceptionWithEstablishedPDUSession{
		SUPI:           IMSI("262019876543210"),
		PDUSessionID:   5,
		PDUSessionType: PDUSessionTypeIPv4v6,
		UEEndpoint:     []any{IPv4Address{10, 45, 0, 2}, v6},
		DNN:            DNN("internet"),
		RequestType:    SMRequestExisting,
	}, "305081050413120f01a247a945a111810f323632303139383736353433323130850105a603810100870103a91881040a2d0002821020010db80000000000000000000000018c08696e7465726e65748f0102")
	// [9] of 24 octets: the IPv4 arm (6), then the IPv6 arm (18), in the order given.
	if !containsTLV(der, "a918"+"81040a2d0002"+"8210", v6) {
		t.Errorf("uEEndpoint does not carry both families in order: % x", der)
	}
}

// TestStartOfInterceptionReportsAnEmptyEndpointList: table 6.2.3-4 makes uEEndpoint
// conditional, "if available", while the module declares it without OPTIONAL. A session with no
// endpoint address — Unstructured, or Ethernet before a MAC is learned — is therefore reported,
// with the field written as an empty SEQUENCE, rather than refused: interception of such a
// session must still start.
func TestStartOfInterceptionReportsAnEmptyEndpointList(t *testing.T) {
	for name, ep := range map[string][]any{"nil": nil, "empty": {}} {
		der, err := EncodeXIRI(SMFStartOfInterceptionWithEstablishedPDUSession{
			SUPI:           IMSI("262019876543210"),
			PDUSessionID:   5,
			PDUSessionType: PDUSessionTypeUnstructured,
			UEEndpoint:     ep,
			DNN:            DNN("internet"),
			RequestType:    SMRequestExisting,
		})
		if err != nil {
			t.Fatalf("%s: a session with no endpoint address was refused: %v", name, err)
		}
		// [9] constructed, zero length: the field is present and the list is empty.
		if !strings.Contains(hex.EncodeToString(der), "a900") {
			t.Errorf("%s: uEEndpoint is not written as an empty [9] SEQUENCE: % x", name, der)
		}
	}
}

// TestEstablishmentOmitsAbsentEndpoint: uEEndpoint is OPTIONAL in the
// establishment record, so an absent address must omit the field rather than emit
// it empty — and the record must still encode, unlike the mandatory case above.
func TestEstablishmentOmitsAbsentEndpoint(t *testing.T) {
	base := SMFPDUSessionEstablishment{
		SUPI:           IMSI("262019876543210"),
		PDUSessionID:   5,
		GTPTunnelID:    FTEID{TEID: 1, IPv4Address: []byte{10, 20, 30, 40}},
		PDUSessionType: PDUSessionTypeIPv4,
		DNN:            DNN("internet"),
		RequestType:    SMRequestInitial,
	}
	without := assertEncodes(t, base, "303c81050413120f01a233a631a111810f323632303139383736353433323130850105a60981010182040a141e288701018c08696e7465726e65748f0101")

	withAddr := base
	withAddr.UEEndpoint = UEEndpoint(net.ParseIP("10.45.0.2"))
	with := assertEncodes(t, withAddr, "304481050413120f01a23ba639a111810f323632303139383736353433323130850105a60981010182040a141e28870101a90681040a2d00028c08696e7465726e65748f0101")

	if len(without) >= len(with) {
		t.Errorf("absent endpoint encoding (%d bytes) is not smaller than present (%d) — "+
			"the optional field is being emitted empty rather than omitted", len(without), len(with))
	}
	if !containsTLV(with, "a9068104", []byte{10, 45, 0, 2}) {
		t.Errorf("uEEndpoint is not [9] { iPv4Address [1] 10.45.0.2 }: % x", with)
	}
}

// TestEndpointIsNotTheTunnelEndpoint guards the distinction the whole change is
// about: gTPTunnelID carries the serving UPF's address and uEEndpoint carries the
// subject's. Reporting one as the other would answer a question nobody asked.
func TestEndpointIsNotTheTunnelEndpoint(t *testing.T) {
	der := assertEncodes(t, SMFPDUSessionEstablishment{
		SUPI:           IMSI("262019876543210"),
		PDUSessionID:   5,
		GTPTunnelID:    FTEID{TEID: 1, IPv4Address: []byte{192, 168, 252, 3}}, // UPF N3
		PDUSessionType: PDUSessionTypeIPv4,
		UEEndpoint:     UEEndpoint(net.ParseIP("10.45.0.2")), // the subject
		DNN:            DNN("internet"),
		RequestType:    SMRequestInitial,
	}, "304481050413120f01a23ba639a111810f323632303139383736353433323130850105a6098101018204c0a8fc03870101a90681040a2d00028c08696e7465726e65748f0101")
	// gTPTunnelID [6] { tEID [1] 1, iPv4Address [2] c0 a8 fc 03 }.
	if !containsTLV(der, "a609810101"+"8204", []byte{192, 168, 252, 3}) {
		t.Errorf("gTPTunnelID does not carry the UPF's address: % x", der)
	}
	// uEEndpoint [9] { iPv4Address [1] 0a 2d 00 02 }.
	if !containsTLV(der, "a9068104", []byte{10, 45, 0, 2}) {
		t.Errorf("uEEndpoint does not carry the subject's address: % x", der)
	}
}

// TestAnEmptyOptionalEndpointListIsOmitted: in the establishment and unsuccessful-procedure
// records uEEndpoint is OPTIONAL and conditional, so a list with nothing in it — nil or
// non-nil — omits the field: the address is not available, and that is what absence says.
func TestAnEmptyOptionalEndpointListIsOmitted(t *testing.T) {
	est := SMFPDUSessionEstablishment{
		SUPI:           IMSI("262019876543210"),
		PDUSessionID:   5,
		GTPTunnelID:    FTEID{TEID: 1, IPv4Address: []byte{10, 20, 30, 40}},
		PDUSessionType: PDUSessionTypeIPv4,
		DNN:            DNN("internet"),
		RequestType:    SMRequestInitial,
	}
	absent, err := EncodeXIRI(est)
	if err != nil {
		t.Fatal(err)
	}
	est.UEEndpoint = []any{}
	empty, err := EncodeXIRI(est)
	if err != nil {
		t.Fatalf("an empty establishment endpoint list was refused: %v", err)
	}
	if !bytes.Equal(absent, empty) {
		t.Errorf("an empty establishment endpoint list was not omitted:\nabsent % x\nempty  % x", absent, empty)
	}

	unsuccessful := SMFUnsuccessfulProcedure{
		FailedProcedureType: SMFFailedPDUSessionEstablishment, FailureCause: 1,
		Initiator: InitiatorNetwork,
	}
	absent, err = EncodeXIRI(unsuccessful)
	if err != nil {
		t.Fatal(err)
	}
	unsuccessful.UEEndpoint = []any{}
	empty, err = EncodeXIRI(unsuccessful)
	if err != nil {
		t.Fatalf("an empty unsuccessful-procedure endpoint list was refused: %v", err)
	}
	if !bytes.Equal(absent, empty) {
		t.Errorf("an empty unsuccessful-procedure endpoint list was not omitted:\nabsent % x\nempty  % x", absent, empty)
	}
}
