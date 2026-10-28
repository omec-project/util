// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"encoding/binary"
	"testing"
)

// TestMarshalRefusesWhatTheTablesDoNotDefine: a product PDU carries one of table 5.2.6-1's
// directions — 0 is reserved for the keepalive PDUs, which carry none — and every PDU carries
// one of table 5.2.2-1's types. Either written anyway is a PDU whose header says something no
// mediation function can read correctly.
func TestMarshalRefusesWhatTheTablesDoNotDefine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pdu     *PDU
		wantErr bool
	}{
		{"X2 with direction 0", &PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128}, true},
		{"X3 with direction 0", &PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4}, true},
		{"X3 with direction 6", &PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: 6}, true},
		{"X2 with direction 1", &PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionUnknown}, false},
		{"X2 with direction 5", &PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable}, false},
		{"PDU type 0", &PDU{Type: 0, Direction: DirectionNotApplicable}, true},
		{"PDU type 5", &PDU{Type: 5, Direction: DirectionNotApplicable}, true},
		{"a keepalive, which has no direction", Keepalive(7), false},
		{"an acknowledgement, which has no direction", KeepaliveAck(7), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.pdu.Marshal(); (err != nil) != tc.wantErr {
				t.Errorf("Marshal() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestDecodingStaysLenientAboutDirectionAndType: what Marshal refuses to build is still
// decoded, because a peer on a later minor version may send a type or direction this
// transcription does not list, and clause 5.2.1 requires it to stay decodable. The reader
// decides what a decoded PDU means.
func TestDecodingStaysLenientAboutDirectionAndType(t *testing.T) {
	b, err := (&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionUnknown}).Marshal()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		typ  uint16
		dir  uint16
	}{
		{"direction 0 on X2", uint16(PDUTypeX2), 0},
		{"direction 9 on X2", uint16(PDUTypeX2), 9},
		{"PDU type 7", 7, uint16(DirectionUnknown)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := append([]byte(nil), b...)
			binary.BigEndian.PutUint16(w[2:4], tc.typ)
			binary.BigEndian.PutUint16(w[14:16], tc.dir)

			p, _, err := Unmarshal(w)
			if err != nil {
				t.Fatalf("Unmarshal() error = %v, want the PDU decoded", err)
			}
			if uint16(p.Type) != tc.typ || uint16(p.Direction) != tc.dir {
				t.Errorf("decoded type %d direction %d, want %d and %d", p.Type, p.Direction, tc.typ, tc.dir)
			}
		})
	}
}
