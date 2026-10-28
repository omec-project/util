// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"encoding/binary"
	"math"
	"time"
)

// Conditional attribute types, ETSI TS 103 221-2 table 5.3.1-2.
//
// TRANSCRIBED FROM V1.10.1 (2026-03). Restate the revision whenever this list is
// extended, for the reason payloadFormatRules carries the same stamp: a stale
// transcription and a current one look identical without one.
//
// Only the six this project emits are named. The table defines 22, and
// CONFORMANCE.md in this directory carries the disposition of every one of them —
// for the sixteen that are absent, the reason is the part worth reading.
//
// Which interface owes which is 3GPP TS 33.128's to say, not this specification's:
// clause 5.3 makes every attribute conditional "as directed by the relevant LI
// architecture". Table 5.3.1-2 of TS 33.128 requires NFID and IPID on both X2 and
// X3; table 5.3.2-2 adds Timestamp, Sequence Number and the two target identifiers
// for an xIRI; table 5.3.3-2 requires Timestamp and Sequence Number for an xCC and
// requires neither target identifier.
const (
	AttrNFID                    uint16 = 6
	AttrIPID                    uint16 = 7
	AttrSequenceNumber          uint16 = 8
	AttrTimestamp               uint16 = 9
	AttrMatchedTargetIdentifier uint16 = 17
	AttrOtherTargetIdentifier   uint16 = 18
)

// NFID identifies the network function containing the point of interception
// (clause 5.3.7). TS 33.128 leaves the format to the carrier — "a unique identifier
// assigned to the NF by the network (e.g. FQDN)" — and this project uses the
// identifier the element already asserts on X1, so that the identity a mediation
// function receives is the one the provisioning function tasks.
//
// Constant for the life of a process: build it once and reuse the TLV rather than
// per PDU, which on X3 means per packet.
func NFID(id string) TLV {
	return TLV{Type: AttrNFID, Value: []byte(id)}
}

// IPID identifies the point of interception within that network function
// (clause 5.3.8). Each network function here contains exactly one, so this too is
// constant for the life of a process.
func IPID(id string) TLV {
	return TLV{Type: AttrIPID, Value: []byte(id)}
}

// SequenceNumber is the PDU's number within its context (clause 5.3.9), as a
// four-octet unsigned integer.
//
// The context is not the connection. The clause numbers PDUs "with the same XID,
// DID, NFID, IPID and Correlation ID context", keeps X2 and X3 separate within one
// context, and restarts at zero on wrap — which is what Sequencer implements. A
// counter held per connection would number unrelated contexts from one sequence.
func SequenceNumber(n uint32) TLV {
	var v [4]byte
	binary.BigEndian.PutUint32(v[:], n)

	return TLV{Type: AttrSequenceNumber, Value: v[:]}
}

// Timestamp carries an instant as the POSIX.1-2017 timespec clause 5.3.10 defines:
// two successive 32-bit unsigned integers, seconds then nanoseconds.
//
// Unsigned is what the clause says, so these eight octets carry every instant from
// 1970 to 2106. The clause's own NOTE — that timestamps after 2038 cannot be
// encoded — reads the field as signed, which it is not, so there is nothing here to
// design around. An instant outside that window (a zero time.Time, say) is clamped to
// the nearest end of it rather than wrapped into an unrelated instant; the caller
// passes a clock reading, so that is not a value it should meet.
//
// This function timestamps nothing itself, deliberately. What belongs here is the
// time the *event* occurred on X2 (TS 33.128 table 5.3.2-2) and the time the xCC was
// generated on X3 (table 5.3.3-2). Those differ whenever a record is built after the
// fact, and a mediation function cannot tell which one it was given — so sampling
// the clock in here would quietly convert the first into the second.
//
// The nanosecond field carries whatever resolution the clock offered, unrounded. The
// six-digit rule that governs X1's textual timestamps is a rendering rule for a
// different interface; applying it here would discard precision for nothing.
func Timestamp(t time.Time) TLV {
	secs, nanos := t.Unix(), uint32(t.Nanosecond()) // Nanosecond is in [0, 1e9)
	switch {
	case secs < 0:
		secs, nanos = 0, 0
	case secs > math.MaxUint32:
		secs, nanos = math.MaxUint32, 999_999_999
	}

	var v [8]byte
	binary.BigEndian.PutUint32(v[0:4], uint32(secs)) // clamped above
	binary.BigEndian.PutUint32(v[4:8], nanos)

	return TLV{Type: AttrTimestamp, Value: v[:]}
}

// MatchedTargetIdentifier reports the target identity whose match produced this
// xIRI (clause 5.3.18). The value is a TS 103 221-1 TargetIdentifier's contents
// *without* the enclosing tag, UTF-8 — `<imsi>204081234567890</imsi>` is the
// clause's own example — which li/types renders.
//
// Multiple occurrences are permitted, and where a task named several identities the
// subject presents, all of them matched: emitting one and calling it the match would
// be an arbitrary claim.
func MatchedTargetIdentifier(fragment string) TLV {
	return TLV{Type: AttrMatchedTargetIdentifier, Value: []byte(fragment)}
}

// OtherTargetIdentifier reports one further identity of the same subject held at
// this network function (clause 5.3.19), in the same encoding as the matched one.
//
// One TLV per identity, since multiple occurrences are permitted. "Other target
// identities present at the NF" means the subject's — not every identity the network
// function happens to hold, which would disclose unrelated subscribers to an agency
// holding a warrant for one.
func OtherTargetIdentifier(fragment string) TLV {
	return TLV{Type: AttrOtherTargetIdentifier, Value: []byte(fragment)}
}

// Identity is the part of a PDU's conditional attributes that belongs to the emitting
// element rather than to the task: the two identities of clauses 5.3.7 and 5.3.8, and
// the numbering of clause 5.3.9.
//
// One per point of interception. The two identities are built once, since they are
// constant for the life of a process, and the numbering is per correlation context —
// so a POI holds one of these and asks it for each PDU's attributes.
type Identity struct {
	nfid TLV
	ipid TLV
	seq  *Sequencer
}

// NewIdentity builds the attribute context for one point of interception: nfID is the
// identifier the network function asserts on X1 (which is what makes the identity a
// mediation function receives the same one the provisioning function tasks), and
// pointID names the POI within it.
func NewIdentity(nfID, pointID string) *Identity {
	return &Identity{
		nfid: NFID(nfID),
		ipid: IPID(pointID),
		seq:  NewSequencer(),
	}
}

// Attributes returns the conditional attributes for one PDU: the element's two
// identities, this PDU's sequence number within its (XID, Correlation ID) context,
// the given instant, and one target identifier attribute per matched and per other
// identity.
//
// The instant is the caller's to choose, because the two interfaces define it
// differently — the event's time on X2 (TS 33.128 table 5.3.2-2) and the xCC's
// generation time on X3 (table 5.3.3-2). Nothing here reads a clock, so a POI cannot
// report the second where the first was required.
//
// A CC-POI passes no identities: table 5.3.3-2 requires neither target identifier of
// an xCC, and a POI tasked by packet-detection criteria holds no subscriber identity
// to report anyway.
//
// Call it where the PDU is built, not where it is written: the number must be taken
// before the delivery queue that may drop the PDU, so that the loss shows up as a gap
// rather than being closed over.
func (i *Identity) Attributes(xid [xidLength]byte, corr [CorrelationIDLength]byte, at time.Time, matched, other []string) []TLV {
	attrs := make([]TLV, 0, 4+len(matched)+len(other))
	attrs = append(attrs,
		i.nfid,
		i.ipid,
		SequenceNumber(i.seq.Next(xid, corr)),
		Timestamp(at),
	)
	for _, frag := range matched {
		attrs = append(attrs, MatchedTargetIdentifier(frag))
	}
	for _, frag := range other {
		attrs = append(attrs, OtherTargetIdentifier(frag))
	}

	return attrs
}

// Forget drops the numbering state for every context under an XID, so it does not
// outlive the tasking it belongs to. Wire it to the X1 deactivation hook where one task
// is one warrant; see Sequencer.Forget.
func (i *Identity) Forget(xid [xidLength]byte) {
	i.seq.Forget(xid)
}

// ForgetContext drops the numbering state for one correlation context under an XID,
// leaving the XID's other contexts numbering where they were.
//
// This is the one a triggered CC-POI needs: its tasks are allocated per (warrant,
// session, UPF) and share the warrant's delivery XID, so releasing by XID when one
// session ends restarts the numbering of every other session that warrant is still
// intercepting there. See Sequencer.ForgetContext for why that is worse than a leak.
func (i *Identity) ForgetContext(xid [xidLength]byte, corr [CorrelationIDLength]byte) {
	i.seq.ForgetContext(xid, corr)
}

// Contexts reports how many correlation contexts are being numbered, so that "this
// state is bounded by live tasking" is assertable from a POI's own tests.
func (i *Identity) Contexts() int {
	return i.seq.Len()
}
