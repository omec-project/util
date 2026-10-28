// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"bytes"
	"errors"
	"testing"
)

// Fixed XID and correlation identifier, for deterministic vectors.
var (
	sampleXID = [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	sampleCID = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
)

// TestMarshalGolden pins the exact byte layout against a hand-derived vector so
// any drift from the TS 103 221-2 wire format is caught.
func TestMarshalGolden(t *testing.T) {
	p := &PDU{
		Type:          PDUTypeX2,
		PayloadFormat: PayloadFormat3GPP33128,
		Direction:     DirectionFromTarget,
		XID:           sampleXID,
		CorrelationID: sampleCID,
		Payload:       []byte("hi"),
	}
	got, err := p.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := []byte{
		0x00, 0x05, // version 0.5
		0x00, 0x01, // PDU type = X2
		0x00, 0x00, 0x00, 0x28, // header length = 40
		0x00, 0x00, 0x00, 0x02, // payload length = 2
		0x00, 0x02, // payload format = 3GPP 33.128
		0x00, 0x03, // direction = from target
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, // XID
		1, 2, 3, 4, 5, 6, 7, 8, // correlation ID
		'h', 'i', // payload
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch\n got: % x\nwant: % x", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	orig := &PDU{
		Type:          PDUTypeX3,
		PayloadFormat: PayloadFormatIPv4,
		Direction:     DirectionToTarget,
		XID:           sampleXID,
		CorrelationID: sampleCID,
		Attributes: []TLV{
			{Type: 1, Value: []byte{0xde, 0xad}},
			{Type: 7, Value: []byte("beef")},
		},
		Payload: []byte{0x45, 0x00, 0x00, 0x1c}, // looks like an IPv4 header start
	}
	enc, err := orig.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	dec, n, err := Unmarshal(enc)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if n != len(enc) {
		t.Errorf("consumed %d bytes, want %d", n, len(enc))
	}
	if dec.Type != orig.Type || dec.PayloadFormat != orig.PayloadFormat ||
		dec.Direction != orig.Direction || dec.XID != orig.XID || dec.CorrelationID != orig.CorrelationID {
		t.Errorf("header mismatch: %+v vs %+v", dec, orig)
	}
	if !bytes.Equal(dec.Payload, orig.Payload) {
		t.Errorf("payload mismatch: % x vs % x", dec.Payload, orig.Payload)
	}
	if len(dec.Attributes) != 2 || dec.Attributes[0].Type != 1 ||
		!bytes.Equal(dec.Attributes[1].Value, []byte("beef")) {
		t.Errorf("attributes mismatch: %+v", dec.Attributes)
	}
}

func TestUnmarshalIncomplete(t *testing.T) {
	//nolint:errcheck // test
	full, _ := (&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable, Payload: []byte("payload")}).Marshal()
	for _, n := range []int{0, 5, 11, len(full) - 1} {
		if _, _, err := Unmarshal(full[:n]); !errors.Is(err, ErrIncomplete) {
			t.Errorf("Unmarshal(%d bytes) err = %v, want ErrIncomplete", n, err)
		}
	}
	if _, consumed, err := Unmarshal(full); err != nil || consumed != len(full) {
		t.Errorf("Unmarshal(full) = %d, %v; want %d, nil", consumed, err, len(full))
	}
}

func TestRejectsBadVersionAndFormat(t *testing.T) {
	// Bad version on the wire.
	//nolint:errcheck // test
	b, _ := (&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable}).Marshal()
	b[0] = 9 // corrupt major version
	if _, _, err := Unmarshal(b); err == nil {
		t.Error("Unmarshal accepted a bad major version")
	}

	// GTP-U is not allowed on X2 (it is X3-only) — must be rejected on encode.
	if _, err := (&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormatGTPU, Direction: DirectionNotApplicable}).Marshal(); err == nil {
		t.Error("Marshal accepted GTP-U payload on an X2 PDU")
	}
	// IPv4 is X3-allowed — must succeed.
	if _, err := (&PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionNotApplicable}).Marshal(); err != nil {
		t.Errorf("Marshal rejected a valid X3 IPv4 PDU: %v", err)
	}
}

// TestPayloadFormatTableMatchesSpec asserts the whole of TS 103 221-2 V1.10.1
// table 5.4.1-1, not only the handful of formats this project emits. The table in
// x2x3.go is transcribed by hand from a specification that publishes no
// machine-readable form, so nothing checks it against the source; what this test
// does is make the *next* revision's addition fail here rather than pass
// unnoticed, which is how value 17 went missing for three revisions.
func TestPayloadFormatTableMatchesSpec(t *testing.T) {
	// Value, name, permitted on X2, permitted on X3 — table 5.4.1-1.
	spec := []struct {
		value  PayloadFormat
		name   string
		x2, x3 bool
	}{
		{PayloadFormatReserved, "Reserved for Keepalive mechanism", false, false}, // N/A in the table
		{PayloadFormatETSI102232_1, "ETSI TS 102 232-1 Defined Payload", true, true},
		{PayloadFormat3GPP33128, "3GPP TS 33.128 Defined Payload", true, true},
		{PayloadFormat3GPP33108, "3GPP TS 33.108 Defined Payload", true, true},
		{PayloadFormatProprietary, "Proprietary Payload", true, true},
		{PayloadFormatIPv4, "IPv4 Packet", true, true},
		{PayloadFormatIPv6, "IPv6 Packet", true, true},
		{PayloadFormatEthernet, "Ethernet Frame", false, true},
		{PayloadFormatRTP, "RTP Packet", false, true},
		{PayloadFormatSIP, "SIP Message", true, false},
		{PayloadFormatDHCP, "DHCP Message", true, false},
		{PayloadFormatRADIUS, "RADIUS Packet", true, false},
		{PayloadFormatGTPU, "GTP-U Message", false, true},
		{PayloadFormatMSRP, "MSRP Message", false, true},
		{PayloadFormat33108EPSIRI, "3GPP TS 33.108 EpsIRIContent", true, false},
		{PayloadFormatMIME, "MIME Message", true, true},
		{PayloadFormatUnstructured, "3GPP Unstructured PDU", false, true},
		{PayloadFormatPSPDUPayload, "ETSI TS 102 232-1 PS-PDU.Payload", true, true},
	}

	if len(payloadFormatRules) != len(spec) {
		t.Errorf("payloadFormatRules has %d entries, table 5.4.1-1 defines %d", len(payloadFormatRules), len(spec))
	}
	for _, want := range spec {
		if _, ok := payloadFormatRules[want.value]; !ok {
			t.Errorf("payload format %d (%s) is missing from payloadFormatRules", want.value, want.name)
			continue
		}
		if got := want.value.allowedOn(PDUTypeX2); got != want.x2 {
			t.Errorf("format %d (%s) on X2 = %v, table 5.4.1-1 says %v", want.value, want.name, got, want.x2)
		}
		if got := want.value.allowedOn(PDUTypeX3); got != want.x3 {
			t.Errorf("format %d (%s) on X3 = %v, table 5.4.1-1 says %v", want.value, want.name, got, want.x3)
		}
	}
}

// TestVersionCompatibility covers the rule in clause 5.2.1: the major version
// increments on a backwards-incompatible change and the minor on a
// backwards-compatible addition. A peer one minor ahead — which every MDF built
// against V1.10.1 is, since that revision raised the value to 6 — must therefore
// still decode. Rejecting it, as this once did, refuses a conformant peer.
func TestVersionCompatibility(t *testing.T) {
	encode := func(t *testing.T) []byte {
		t.Helper()
		b, err := (&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable, Payload: []byte("x")}).Marshal()
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return b
	}

	t.Run("a higher minor version decodes", func(t *testing.T) {
		b := encode(t)
		b[1] = MinorVersion + 1 // 0.6 — what V1.10.1 defines
		p, _, err := Unmarshal(b)
		if err != nil {
			t.Fatalf("Unmarshal rejected version 0.%d: %v", MinorVersion+1, err)
		}
		if !bytes.Equal(p.Payload, []byte("x")) {
			t.Errorf("payload = % x, want %q", p.Payload, "x")
		}
	})

	t.Run("a lower minor version decodes", func(t *testing.T) {
		b := encode(t)
		b[1] = MinorVersion - 1
		if _, _, err := Unmarshal(b); err != nil {
			t.Errorf("Unmarshal rejected version 0.%d: %v", MinorVersion-1, err)
		}
	})

	t.Run("a differing major version is rejected", func(t *testing.T) {
		b := encode(t)
		b[0] = MajorVersion + 1 // 1.5 — a layout this implementation cannot assume
		if _, _, err := Unmarshal(b); err == nil {
			t.Error("Unmarshal accepted major version 1, which may have moved fields")
		}
	})
}
