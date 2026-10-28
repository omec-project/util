// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// TestKeepaliveIsClause51 pins the PDU byte for byte against clause 5.1: Version,
// PDU Type and Header Length populated, every other mandatory field zero, no
// payload, one Sequence Number.
//
// Written against the specification's field list rather than against Marshal, so
// that a change to the encoder cannot quietly redefine what a Keepalive is.
func TestKeepaliveIsClause51(t *testing.T) {
	b, err := Keepalive(7).Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	// 40 mandatory + one attribute of 4 header bytes and a 4-octet value.
	if len(b) != 48 {
		t.Errorf("Keepalive is %d bytes, want 48 (40 mandatory + an 8-byte Sequence Number)", len(b))
	}
	if b[0] != MajorVersion || b[1] != MinorVersion {
		t.Errorf("version = %d.%d, want %d.%d", b[0], b[1], MajorVersion, MinorVersion)
	}
	if got := binary.BigEndian.Uint16(b[2:4]); got != uint16(PDUTypeKeepalive) {
		t.Errorf("PDU type = %d, want %d", got, PDUTypeKeepalive)
	}
	if got := binary.BigEndian.Uint32(b[4:8]); got != 48 {
		t.Errorf("header length field = %d, want 48", got)
	}
	if got := binary.BigEndian.Uint32(b[8:12]); got != 0 {
		t.Errorf("payload length field = %d, want 0 — clause 5.1's NOTE: a Keepalive has no payload", got)
	}
	// Payload format, direction, XID and correlation id: offsets 12 through 39.
	for i, v := range b[12:40] {
		if v != 0 {
			t.Errorf("mandatory header byte at offset %d = %#x, want 0 — clause 5.1 zeroes every field but three", i+12, v)
		}
	}
	if len(b) > 48 {
		t.Errorf("bytes past the header: %x", b[48:])
	}
}

// TestKeepaliveRoundTrips is the first thing on this interface that will ever be
// decoded in production, so it is decoded here.
func TestKeepaliveRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		name string
		pdu  *PDU
		typ  PDUType
	}{
		{"keepalive", Keepalive(7), PDUTypeKeepalive},
		{"acknowledgement", KeepaliveAck(7), PDUTypeKeepaliveAck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := tc.pdu.Marshal()
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			got, n, err := Unmarshal(b)
			if err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if n != len(b) {
				t.Errorf("consumed %d bytes of %d", n, len(b))
			}
			if got.Type != tc.typ {
				t.Errorf("type = %d, want %d", got.Type, tc.typ)
			}
			if len(got.Payload) != 0 {
				t.Errorf("payload = %x, want none", got.Payload)
			}
			if seq, ok := KeepaliveSequence(got); !ok || seq != 7 {
				t.Errorf("KeepaliveSequence() = %d, %v; want 7, true", seq, ok)
			}
		})
	}
}

// TestKeepaliveSequenceRejectsMalformed covers what a peer can do to us: the
// acknowledgement is the first thing this element reads from an MDF, and a number
// it cannot use must be reported as absent rather than guessed at.
func TestKeepaliveSequenceRejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		pdu  *PDU
	}{
		{"no attributes at all", &PDU{Type: PDUTypeKeepaliveAck}},
		{"some other attribute", &PDU{Type: PDUTypeKeepaliveAck, Attributes: []TLV{NFID("smf-1")}}},
		{"a sequence number of the wrong width", &PDU{
			Type:       PDUTypeKeepaliveAck,
			Attributes: []TLV{{Type: AttrSequenceNumber, Value: []byte{0, 7}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if seq, ok := KeepaliveSequence(tc.pdu); ok {
				t.Errorf("KeepaliveSequence() = %d, true; want _, false", seq)
			}
		})
	}
}

// TestKeepalivePayloadFormatIsAcceptedOnKeepalivesOnly is table 5.4.1-1's value 0,
// "Reserved for Keepalive mechanism", which is N/A on X2 and X3. CONFORMANCE.md
// records that the reservation is honoured; this is what enforces it.
func TestKeepalivePayloadFormatIsAcceptedOnKeepalivesOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		typ     PDUType
		wantErr bool
	}{
		{"keepalive", PDUTypeKeepalive, false},
		{"acknowledgement", PDUTypeKeepaliveAck, false},
		{"X2", PDUTypeX2, true},
		{"X3", PDUTypeX3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &PDU{Type: tc.typ, PayloadFormat: PayloadFormatReserved}
			if tc.wantErr {
				// A valid direction, so that the format is the only thing refused.
				p.Direction = DirectionNotApplicable
			}
			_, err := p.Marshal()
			if (err != nil) != tc.wantErr {
				t.Errorf("Marshal() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestKeepaliveCounterStartsAtZeroAndWraps is clause 5.3.9's numbering rule applied
// to the counter this mechanism owns.
func TestKeepaliveCounterStartsAtZeroAndWraps(t *testing.T) {
	var c keepaliveCounter
	for want := range uint32(3) {
		if got := c.take(); got != want {
			t.Errorf("take() = %d, want %d", got, want)
		}
	}

	var wrapping keepaliveCounter
	wrapping.next.Store(^uint32(0))
	if got := wrapping.take(); got != ^uint32(0) {
		t.Errorf("take() = %d, want %d", got, ^uint32(0))
	}
	if got := wrapping.take(); got != 0 {
		t.Errorf("after the maximum, take() = %d, want 0 — clause 5.3.9 restarts the sequence", got)
	}
}

// TestKeepaliveCountersAreIndependentPerConnection is the property the counter
// exists for: one per connection, so a replacement connection numbers from zero.
func TestKeepaliveCountersAreIndependentPerConnection(t *testing.T) {
	var first, second keepaliveCounter
	first.take()
	first.take()

	if got := second.take(); got != 0 {
		t.Errorf("a second connection starts at %d, want 0", got)
	}
}

func TestKeepaliveCounterIsConcurrencySafe(t *testing.T) {
	var c keepaliveCounter
	var wg sync.WaitGroup
	const n = 100

	seen := make([]uint32, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen[i] = c.take()
		}()
	}
	wg.Wait()

	got := make(map[uint32]bool, n)
	for _, v := range seen {
		if got[v] {
			t.Fatalf("sequence number %d handed out twice", v)
		}
		got[v] = true
	}
}

// TestKeepaliveDoesNotDisturbSequencer is the mistake clause 5.3.9 forbids, pinned:
// a Keepalive must not advance the numbering a warrant's product is delivered with,
// and must not create a context of its own in it.
func TestKeepaliveDoesNotDisturbSequencer(t *testing.T) {
	s := NewSequencer()
	if got := s.Next(sampleXID, sampleCID); got != 0 {
		t.Fatalf("first product PDU numbered %d, want 0", got)
	}

	var c keepaliveCounter
	for range 5 {
		Keepalive(c.take())
	}

	if got := s.Next(sampleXID, sampleCID); got != 1 {
		t.Errorf("after 5 keepalives the next product PDU is numbered %d, want 1 — "+
			"the keepalive counter and the product counter are not the same counter", got)
	}
	if got := s.Len(); got != 1 {
		t.Errorf("Sequencer holds %d contexts, want 1 — a keepalive numbers no context", got)
	}
}

// TestKeepaliveConfigValidate covers the relationship the specification leaves
// implied, and the defaults it states outright.
func TestKeepaliveConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     KeepaliveConfig
		wantErr bool
	}{
		{"the zero value is the specification's own defaults", KeepaliveConfig{}, false},
		{"the specification's defaults, stated", KeepaliveConfig{TimeP1: DefaultTimeP1, TimeP2: DefaultTimeP2}, false},
		{"a shortened pair keeping the ratio", KeepaliveConfig{TimeP1: time.Second, TimeP2: 3 * time.Second}, false},
		{"only TIME_P1 set, so TIME_P2 defaults above it", KeepaliveConfig{TimeP1: 30 * time.Second}, false},
		{"TIME_P2 equal to TIME_P1", KeepaliveConfig{TimeP1: time.Minute, TimeP2: time.Minute}, true},
		{"TIME_P2 below TIME_P1", KeepaliveConfig{TimeP1: time.Minute, TimeP2: 30 * time.Second}, true},
		{"a negative timer", KeepaliveConfig{TimeP1: -time.Second}, true},
		// Only TIME_P2 set, below the default TIME_P1 of 60s: the pair that reads as
		// harmless in a configuration file and disconnects every connection.
		{"only TIME_P2 set, and below the default TIME_P1", KeepaliveConfig{TimeP2: 30 * time.Second}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestKeepaliveDefaultsAreTheSpecifications: a deployment that configures nothing gets the
// clause 6.2.4 defaults of 60 and 180 seconds, and callers inherit them by passing zeros
// through rather than each writing the numbers down.
func TestKeepaliveDefaultsAreTheSpecifications(t *testing.T) {
	got := KeepaliveConfig{}.withDefaults()

	if got.TimeP1 != 60*time.Second {
		t.Errorf("default TIME_P1 = %s, want 60s (clause 6.2.4)", got.TimeP1)
	}
	if got.TimeP2 != 180*time.Second {
		t.Errorf("default TIME_P2 = %s, want 180s (clause 6.2.4)", got.TimeP2)
	}
	if got.Disabled {
		t.Error("the zero value disables the mechanism; a deployment that says nothing must get the conformant behaviour")
	}
}

// TestKeepaliveSequenceOfNothingIsNoNumber: an exported reader handed nil answers "no usable
// number" rather than faulting the goroutine that called it.
func TestKeepaliveSequenceOfNothingIsNoNumber(t *testing.T) {
	if n, ok := KeepaliveSequence(nil); ok || n != 0 {
		t.Errorf("KeepaliveSequence(nil) = %d, %v; want 0, false", n, ok)
	}
}
