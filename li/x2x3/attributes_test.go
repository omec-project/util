// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// TestAttributeTypesMatchSpec asserts the six type numbers against ETSI
// TS 103 221-2 table 5.3.1-2 (V1.10.1). Transcribed constants with no check are how
// payload format 17 stayed missing for three revisions.
func TestAttributeTypesMatchSpec(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uint16
		want uint16
	}{
		{"Network Function ID", AttrNFID, 6},
		{"Interception Point ID", AttrIPID, 7},
		{"Sequence Number", AttrSequenceNumber, 8},
		{"Timestamp", AttrTimestamp, 9},
		{"Matched Target Identifier", AttrMatchedTargetIdentifier, 17},
		{"Other Target Identifier", AttrOtherTargetIdentifier, 18},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, table 5.3.1-2 says %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestTimestampEncoding pins clause 5.3.10's timespec: two 32-bit unsigned
// integers, seconds then nanoseconds. The two later vectors are the point — the
// clause's NOTE claims instants after 2038 cannot be encoded, which is true only of
// a signed field, and this one is unsigned.
func TestTimestampEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		when time.Time
		want []byte
	}{
		{
			name: "one second and two nanoseconds after the epoch",
			when: time.Unix(1, 2),
			want: []byte{0, 0, 0, 1, 0, 0, 0, 2},
		},
		{
			name: "the instant a signed 32-bit seconds field overflows",
			when: time.Unix(1<<31, 0), // 2038-01-19T03:14:08Z
			want: []byte{0x80, 0x00, 0x00, 0x00, 0, 0, 0, 0},
		},
		{
			name: "the last instant the encoding can carry",
			when: time.Unix(1<<32-1, 999_999_999), // 2106-02-07T06:28:15Z
			want: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x3B, 0x9A, 0xC9, 0xFF},
		},
	} {
		got := Timestamp(tc.when)
		if got.Type != AttrTimestamp {
			t.Errorf("%s: type = %d, want %d", tc.name, got.Type, AttrTimestamp)
		}
		if !bytes.Equal(got.Value, tc.want) {
			t.Errorf("%s: value = % x, want % x", tc.name, got.Value, tc.want)
		}
	}
}

// TestTimestampKeepsClockResolution: the nanosecond field carries what the clock
// offered. Rounding to microseconds here would import a rule that belongs to X1's
// textual timestamps — and on Linux, where Go reports nanoseconds, it would discard
// three digits of every timestamp we send.
func TestTimestampKeepsClockResolution(t *testing.T) {
	const nanos = 123_456_789
	got := Timestamp(time.Unix(1_767_225_600, nanos))
	if n := binary.BigEndian.Uint32(got.Value[4:8]); n != nanos {
		t.Errorf("nanoseconds = %d, want %d — the value was rounded", n, nanos)
	}
}

// TestATimestampOutsideTheFieldIsClamped: the field carries 1970 to 2106. An instant outside
// it is clamped to the nearest end rather than wrapped, so a zero time.Time does not encode as
// an unrelated instant somewhere in the window.
func TestATimestampOutsideTheFieldIsClamped(t *testing.T) {
	for _, tc := range []struct {
		name        string
		at          time.Time
		secs, nanos uint32
	}{
		{"zero time", time.Time{}, 0, 0},
		{"before 1970", time.Unix(-1, 500), 0, 0},
		{"after 2106", time.Unix(math.MaxUint32+10, 7), math.MaxUint32, 999_999_999},
	} {
		got := Timestamp(tc.at)
		if s := binary.BigEndian.Uint32(got.Value[0:4]); s != tc.secs {
			t.Errorf("%s: seconds = %d, want %d", tc.name, s, tc.secs)
		}
		if n := binary.BigEndian.Uint32(got.Value[4:8]); n != tc.nanos {
			t.Errorf("%s: nanoseconds = %d, want %d", tc.name, n, tc.nanos)
		}
	}
}

func TestSequenceNumberEncoding(t *testing.T) {
	for _, n := range []uint32{0, 1, 0xDEADBEEF, 0xFFFFFFFF} {
		got := SequenceNumber(n)
		if got.Type != AttrSequenceNumber {
			t.Errorf("type = %d, want %d", got.Type, AttrSequenceNumber)
		}
		if len(got.Value) != 4 {
			t.Fatalf("value is %d octets, clause 5.3.9 says four", len(got.Value))
		}
		if v := binary.BigEndian.Uint32(got.Value); v != n {
			t.Errorf("value = %d, want %d", v, n)
		}
	}
}

// TestIdentityAttributesCarryTheirValue covers the four attributes whose value is
// text: the two element identities and the two target identifiers. The target
// identifier form is clause 5.3.18's, whose example this mirrors.
func TestIdentityAttributesCarryTheirValue(t *testing.T) {
	for _, tc := range []struct {
		got  TLV
		typ  uint16
		want string
	}{
		{NFID("smf-1"), AttrNFID, "smf-1"},
		{IPID("SMF-IRI-POI"), AttrIPID, "SMF-IRI-POI"},
		{MatchedTargetIdentifier("<imsi>204081234567890</imsi>"), AttrMatchedTargetIdentifier, "<imsi>204081234567890</imsi>"},
		{OtherTargetIdentifier("<gpsiMsisdn>9000000002</gpsiMsisdn>"), AttrOtherTargetIdentifier, "<gpsiMsisdn>9000000002</gpsiMsisdn>"},
	} {
		if tc.got.Type != tc.typ {
			t.Errorf("type = %d, want %d", tc.got.Type, tc.typ)
		}
		if string(tc.got.Value) != tc.want {
			t.Errorf("value = %q, want %q", tc.got.Value, tc.want)
		}
	}
}

// TestMarshalCarriesAllSixAttributes is the xIRI case end to end: the six
// attributes TS 33.128 table 5.3.2-2 requires, through Marshal and back.
func TestMarshalCarriesAllSixAttributes(t *testing.T) {
	when := time.Unix(1_767_225_600, 123_456_789)
	attrs := []TLV{
		NFID("amf-1"),
		IPID("AMF-IRI-POI"),
		SequenceNumber(7),
		Timestamp(when),
		MatchedTargetIdentifier("<supiimsi>208930100007488</supiimsi>"),
		OtherTargetIdentifier("<gpsiMsisdn>9000000002</gpsiMsisdn>"),
	}
	orig := &PDU{
		Type:          PDUTypeX2,
		PayloadFormat: PayloadFormat3GPP33128,
		Direction:     DirectionNotApplicable,
		XID:           sampleXID,
		CorrelationID: sampleCID,
		Attributes:    attrs,
		Payload:       []byte("xIRI"),
	}

	enc, err := orig.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	wantHeader := MandatoryHeaderLength
	for _, a := range attrs {
		wantHeader += 4 + len(a.Value)
	}
	if got := int(binary.BigEndian.Uint32(enc[4:8])); got != wantHeader {
		t.Errorf("header length = %d, want %d (40 + the TLV region)", got, wantHeader)
	}

	dec, n, err := Unmarshal(enc)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if n != len(enc) {
		t.Errorf("consumed %d bytes, want %d", n, len(enc))
	}
	if !bytes.Equal(dec.Payload, orig.Payload) {
		t.Errorf("payload = % x, want % x — the payload offset moved", dec.Payload, orig.Payload)
	}
	if len(dec.Attributes) != len(attrs) {
		t.Fatalf("decoded %d attributes, want %d", len(dec.Attributes), len(attrs))
	}
	for i, want := range attrs {
		if dec.Attributes[i].Type != want.Type || !bytes.Equal(dec.Attributes[i].Value, want.Value) {
			t.Errorf("attribute %d = %d/% x, want %d/% x", i,
				dec.Attributes[i].Type, dec.Attributes[i].Value, want.Type, want.Value)
		}
	}
}

// marshalSink keeps the compiler from optimising away the work being measured.
var marshalSink []byte

// TestMarshalAllocatesOnceWithOrWithoutAttributes is the hot-path guard. The X3
// shipper marshals once per duplicated packet, so an attribute region that costs an
// extra allocation costs it per packet. Building the TLVs into their own slice
// before the header — which is what this package did — is exactly that cost.
func TestMarshalAllocatesOnceWithOrWithoutAttributes(t *testing.T) {
	bare := &PDU{
		Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionFromTarget,
		XID: sampleXID, CorrelationID: sampleCID, Payload: make([]byte, 1400),
	}
	withAttrs := *bare
	withAttrs.Attributes = []TLV{
		NFID("upf-1"), IPID("UPF-CC-POI"),
		SequenceNumber(42), Timestamp(time.Unix(1_767_225_600, 0)),
	}

	measure := func(p *PDU) float64 {
		return testing.AllocsPerRun(200, func() {
			marshalSink, _ = p.Marshal() //nolint:errcheck // measuring allocation, not behaviour
		})
	}

	bareAllocs, attrAllocs := measure(bare), measure(&withAttrs)
	if bareAllocs != 1 {
		t.Errorf("marshalling without attributes allocates %v times, want 1 (the output buffer)", bareAllocs)
	}
	if attrAllocs > bareAllocs {
		t.Errorf("marshalling four attributes allocates %v times against %v without: the attribute region is being built separately again", attrAllocs, bareAllocs)
	}
}
