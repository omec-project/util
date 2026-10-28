// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"encoding/binary"
	"testing"
)

// withFormat marshals an X2 PDU and then rewrites its minor version and payload format on
// the wire, as a peer on a later revision would send them.
func withFormat(t *testing.T, minor byte, format PayloadFormat) []byte {
	t.Helper()

	b, err := (&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable, Payload: []byte{1}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b[1] = minor
	binary.BigEndian.PutUint16(b[12:14], uint16(format))

	return b
}

// TestAPayloadFormatFromALaterRevisionIsDecoded: clause 5.2.1 makes a minor increment
// backwards-compatible, and a later revision may add a payload format. A PDU carrying one
// must still decode — refusing it would refuse a conformant peer for being newer.
func TestAPayloadFormatFromALaterRevisionIsDecoded(t *testing.T) {
	const unlisted PayloadFormat = 18
	if _, listed := payloadFormatRules[unlisted]; listed {
		t.Fatalf("format %d is in the table now; pick a value it does not list", unlisted)
	}

	p, _, err := Unmarshal(withFormat(t, MinorVersion+1, unlisted))
	if err != nil {
		t.Fatalf("a PDU from a later minor revision with payload format %d was refused: %v", unlisted, err)
	}
	if p.PayloadFormat != unlisted {
		t.Errorf("decoded payload format %d, want %d", p.PayloadFormat, unlisted)
	}
}

// TestAFormatTheTableForbidsIsRefusedOnDecode: tolerance covers values this element does
// not know, not values the table forbids on that PDU type — Ethernet is X3-only.
func TestAFormatTheTableForbidsIsRefusedOnDecode(t *testing.T) {
	if _, _, err := Unmarshal(withFormat(t, MinorVersion, PayloadFormatEthernet)); err == nil {
		t.Error("an X2 PDU carrying the X3-only Ethernet format was accepted")
	}
}

// TestThisElementDoesNotSendAFormatItDoesNotKnow: the decoder's tolerance does not extend to
// what this element sends.
func TestThisElementDoesNotSendAFormatItDoesNotKnow(t *testing.T) {
	if _, err := (&PDU{Type: PDUTypeX2, PayloadFormat: 18, Direction: DirectionNotApplicable}).Marshal(); err == nil {
		t.Error("a PDU with a payload format the table does not list was marshalled")
	}
}
