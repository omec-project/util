<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# TS 103 221-2 conformance disposition

**Made against ETSI TS 103 221-2 V1.10.1 (2026-03).** Every table and clause number below
refers to that revision. When a newer revision is published, this document is what tells you
whether the code disagrees with the specification or merely predates it — that distinction
is the reason the version is stated at the top rather than left implied.

## What this is, and what it is not

This is a **reading**, not a check. ETSI publishes no machine-readable definition of the
X2/X3 framing: the sole attachment to part 2 is `TS_103_221_02_Configuration.xsd`, which
describes how an X2/X3 interface is *configured* over **X0** — annex C.1 opens "this annex
is only applicable when the X0 interface (see ETSI TS 104 000) is used", and the schema
extends `etsi104000:ConfigurationDetails` in namespace `urn:etsi:li:104000:xsdns:v2` — and
says nothing about the PDU layout. TS 103 221-1 defines no message carrying these fields
either; this element implements no X0 interface, and the timers are deployment
configuration. The X1 side verifies itself against a published XSD on every build; nothing
equivalent is possible here, so this document is a per-field disposition maintained by hand
and is only as current as the revision it names.

Two things do provide external validation, and it is worth being precise about what each
covers:

- **Interoperability testing against an independent reference implementation of the
  mediation side.** This catches anything that breaks decode — a wrong offset, a bad length, a mis-ordered field.
  It cannot catch a field or PDU type that *neither* implementation emits, because nothing
  ever exercises it. Every gap recorded below is of that second kind.
- **A table-driven test over the payload format table** (`x2x3_test.go`), which asserts the
  whole of table 5.4.1-1 including the X2/X3 permissions. That is not a check against the
  published source, but it does make the next addition fail a test rather than pass
  unnoticed.

Do not close a question about this package by citing the package comment, or this document,
as evidence of conformance. Both are claims. The specification is the evidence.

## Version field

The Version field (clause 5.2.1) is a claim about which revision of the specification a PDU
was created against. Its value is a 16-bit integer split into an 8-bit major and an 8-bit
minor: the major increments on a backwards-incompatible change to the PDU structure, the
minor on a backwards-compatible addition (a new PDU type, payload direction, conditional
attribute or payload format).

| Specification revision | Version field value |
|---|---|
| V1.6.1 (2022-03) | 5 (major 0, minor 5) |
| V1.7.1 (2024-12) | 5 |
| V1.8.1 (2025-05) | 5 |
| V1.9.1 (2025-08) | 5 |
| V1.10.1 (2026-03) | **6** (major 0, minor 6) |

The table is compiled from clause 5.2.1 of each revision rather than from the change-history
annex, which flattens ambiguously. The value changes once, in V1.10.1.

**This package emits 5**, the value for every revision through V1.9.1.

It is not 6 because of the peer. Keepalive, the mechanism V1.10.1 conformance depends on, is
implemented — see the PDU type table below — but the reference implementation this package
interoperates with **refuses a 0.6 PDU**, closing the connection and storing nothing. Its own
source agrees: `li-lib-x1x2x3` 1.0.3 holds `MINOR_VERSION = 5` and throws on anything else.
Clause 5.2.1 makes a minor increment backwards-compatible, so that is a defect in the peer —
and one that would cost all delivery to it, not part of it, if this element raised the field.

So this is blocked on a mediation function that accepts 0.6, not on work here. It is the
one gap in this document whose remedy is not in this repository.

**On decode**, a differing minor version is accepted and a differing major version is
rejected, which follows the compatibility rule the clause defines. A payload format table
5.4.1-1 does not list is accepted with it, since a later minor revision may add one; a format
the table forbids on that PDU type is refused. On encode only the listed, permitted formats
are sent. The keepalive reader reads
an X2/X3 socket, so that leniency is load-bearing. The read path is bounded — a peer's
declared length beyond a few kilobytes is refused and the connection dropped — because
`Unmarshal` waits for as many bytes as a header claims and imposes no maximum of its own.

## Mandatory header fields — table 5.1-1

| Field | Octets | Clause | Disposition |
|---|---|---|---|
| Version | 2 | 5.2.1 | **Emitted and parsed.** See above. |
| PDU Type | 2 | 5.2.2 | **Emitted and parsed.** `Marshal` refuses a type the table does not define; decoding accepts any, and the reader decides what to do with it. See the PDU type table below. |
| Header Length | 4 | 5.2.3 | **Emitted and parsed.** Derived on encode from 40 + the conditional attribute bytes, validated on decode against the mandatory minimum of 40. Not stored on the struct, so it cannot disagree with the bytes. |
| Payload Length | 4 | 5.2.4 | **Emitted and parsed.** Derived on encode, used on decode to frame the stream. Not stored, as above. |
| Payload Format | 2 | 5.2.5 | **Emitted and parsed**, and validated against the PDU type — see table 5.4.1-1 below. |
| Payload Direction | 2 | 5.2.6 | **Emitted and parsed.** All six values are modelled, and `Marshal` refuses a product PDU whose direction is not one of 1 to 5; see the direction table below. |
| XID | 16 | 5.2.7 | **Emitted and parsed.** Correctly implements the ProductID override: the clause requires that when the X1 task carries an optional ProductID, the XID field is populated with it instead of the task's own XID. `types.InterceptTask.DeliveryXID` encodes that rule and all three POIs label product through it. |
| Correlation ID | 8 | 5.2.8 | **Emitted and parsed.** Supplied by the triggering function for triggered tasks and never derived locally; zero when the POI does not correlate the PDU, as the clause requires. |
| Conditional Attribute Fields | variable | 5.3 | **Emitted and parsed.** An IRI-POI populates the six TS 33.128 requires on an xIRI, and a CC-POI the four table 5.3.3-2 requires on an xCC; the remaining sixteen are absent with a reason. See table 5.3.1-2 below. |
| Payload | variable | 5.4 | **Emitted and parsed.** |

## PDU types — table 5.2.2-1

| Value | Meaning | Disposition |
|---|---|---|
| 1 | X2 PDU | **Emitted** by the AMF and SMF IRI-POIs. |
| 2 | X3 PDU | **Emitted** by the UPF CC-POI. |
| 3 | Keepalive | **Emitted**, on every X2/X3 connection at least every TIME_P1 (default 60 s), unconditionally rather than when idle — product PDUs are not acknowledged, so traffic says nothing about the mediation function's liveness. Also **answered**: a Keepalive arriving at this element is acknowledged with its own Sequence Number, clause 6.2.4 making both types supported by POIs *and* MDFs. |
| 4 | Keepalive Acknowledgement | **Emitted and read.** Read to apply the TIME_P2 rule (default 180 s), measured from the last *valid* acknowledgement seen rather than per Keepalive; on expiry the connection is disconnected, redialled and reported to the ADMF as an unreachable destination; a destination found unreachable this way, or by any other failed exchange, is redialled once per second until it answers, so its fault is retracted while it is idle rather than by the next delivery. **What establishes liveness is the Sequence Number**: clause 6.2.4 numbers the acknowledgement from the Keepalive it answers, so the number is the only evidence available that the peer is answering *this element* rather than emitting traffic. An acknowledgement carrying no usable number, one beyond what this connection has issued, or one already answered is counted as a mismatch and does **not** postpone TIME_P2 — a peer stuck in a loop, a middlebox replaying, and an endpoint a misroute put in the path all satisfy "something is answering", and each of them takes no product. It is not treated as a protocol error either: the connection is not torn down over a numbering defect in a peer that is carrying product, so the fail-safe decides. A dial that succeeds does not mark a destination reachable: a mediation function that refuses this element's certificate under TLS 1.3 completes the dial and refuses only in an alert. Such a refusal is reported once, deliveries are refused rather than written until a Keepalive on a later connection is acknowledged, and the destination is redialled once per second for as long as it refuses or cannot be dialled — so a mediation function that comes back fixed is found with nothing to deliver, and while a refusal stands a valid acknowledgement is the only thing that marks it reachable. With none standing, a delivery whose write succeeds marks it reachable too. A refusal the reader reads after a writer has already dropped its connection, as against a peer that resets refused connections, is recorded all the same. |

## Payload directions — table 5.2.6-1

All six values are modelled and match the table. Value 0 is reserved for the keepalive
mechanism, and is emitted on exactly the PDUs that reservation is for: clause 5.1 has a
Keepalive zero every mandatory field but three, so its direction is 0 and never anything
else. No product PDU carries it: `Marshal` refuses an X2 or X3 PDU whose direction is 0 or
outside the table. Decoding accepts any value, since a peer on a later minor version may use
one added after this transcription.

| Value | Meaning | Modelled as |
|---|---|---|
| 0 | Reserved for the keepalive mechanism | `DirectionReservedKeepalive` |
| 1 | Direction not known to the POI | `DirectionUnknown` |
| 2 | Sent to (received by) the target | `DirectionToTarget` |
| 3 | Sent from the target | `DirectionFromTarget` |
| 4 | Result of data or events in more than one direction | `DirectionMultiple` |
| 5 | Direction not applicable | `DirectionNotApplicable` |

## Conditional attributes — table 5.3.1-2

**Six of the 22 defined attribute types are emitted; the other sixteen are not.** `Marshal`
writes the type/length/value structure of table 5.3.1-1 and `Unmarshal` parses it back.

**The Timestamp attribute is the only event time this element sends.** `li/iri` reads no
clock, and every `Timestamp` field in `TS33128Payloads.asn` is OPTIONAL and left unset, so
without the header attribute an agency receiving this product could tell only when the PDU
arrived, not when the intercepted event happened.

Clause 5.3 makes these attributes something the POI "may provide … as directed by the
relevant LI architecture", and for this project the relevant architecture is 3GPP TS 33.128 —
which directs a great deal. **Six of the 22 are required, and all six are emitted.**

TS 33.128 table 5.3.1-2 applies to both LI_X2 and LI_X3 and says the **NFID** "shall be set
to indicate the NF that contains the POI" and the **IPID** "shall be set to indicate the POI
(within the NF) that generated the xIRI". Table 5.3.2-2 adds, for every xIRI on LI_X2,
**Timestamp** ("shall be present and set to the time at which the event occurred"),
**Sequence Number** ("shall be present"), **Matched Target Identifier** ("shall be set to
indicate what target identity was matched") and **Other Target Identifier** ("shall be set
with all other target identities present at the NF"). Table 5.3.3-2 requires Timestamp and
Sequence Number on LI_X3 too, and makes AXRI optional.

So the AMF and SMF IRI-POIs each owe six attributes on every xIRI, and the UPF CC-POI owes
four on every xCC. All are emitted.

**What interoperability verifies, and what it does not.** The reference implementation stores
PDUs whose headers are longer than the mandatory 40 bytes — a 40-byte control beside headers
of 49, 80 and 163 bytes — and parses the attribute region of each, including the unprefixed
target-identifier fragment clause 5.3.18 illustrates. It does not validate the fragment as
XML, and no mediation function that *requires* these attributes has been observed at all —
the same limit every interoperability result here carries.

| Type | Name | Clause | Disposition |
|---|---|---|---|
| 1 | ETSI TS 102 232-1 defined attribute | 5.3.2 | Absent. Carries an attribute defined by a specification this project does not implement. |
| 2 | 3GPP TS 33.128 defined attribute | 5.3.3 | Absent. **The one most likely to be required**, since TS 33.128 is this project's governing architecture. |
| 3 | 3GPP TS 33.108 defined attribute | 5.3.4 | Absent. TS 33.108 is the EPS-generation interface; not implemented here. |
| 4 | Proprietary attribute | 5.3.5 | Absent by choice. A standardized attribute is preferable and the clause frames this as temporary support. |
| 5 | Domain ID (DID) | 5.3.6 | Absent. TS 33.128 does not require it; the destinations are resolved from the X1 task, and where a task names more than one of a delivery type every one of them receives that task's product. |
| 6 | Network Function ID (NFID) | 5.3.7 | **Emitted** on X2 and X3, set to the identifier the element asserts on X1 (its `neId`), so the identity an MDF receives is the one the ADMF tasks. |
| 7 | Interception Point ID (IPID) | 5.3.8 | **Emitted** on X2 and X3: `AMF-IRI-POI`, `SMF-IRI-POI`, `UPF-CC-POI`. Each network function contains one point of interception, so the value is a property of the code rather than of the deployment. |
| 8 | Sequence Number | 5.3.9 | **Emitted** on X2 and X3, numbered per the clause's `(XID, DID, NFID, IPID, Correlation ID)` context and not per connection — `Sequencer` holds one counter per live context and drops it when the tasking does — on an ordinary withdrawal, a bulk deactivation and a fail-safe purge alike, since the numbering belongs to the tasking and not to the circumstances of its removal, and *not* on a modification, which keeps the XID and so keeps every context the task's own records have already numbered. **The DID is deliberately not part of the key.** The other four components are constant for a task at one POI, so the varying part is `(XID, Correlation ID)`; keying on the DID as well would number one record separately per destination, and a record delivered to two of a task's destinations must carry one number or each mediation function holds a numbering no other does, with gaps in it indistinguishable from loss. The keepalive mechanism has its own counter, one per connection, for the reason the clause implies: a Keepalive PDU zeroes the XID and Correlation ID, so its context is not any task's, and numbering keepalives from a task's sequence would advance the numbers an MDF reads to detect that task's lost product. |
| 9 | Timestamp | 5.3.10 | **Emitted** on X2 and X3 as the clause's POSIX timespec, two *unsigned* 32-bit integers. On X2 it is the time the event occurred, captured at the report hook; on X3 the time the xCC was generated, taken at framing. **It is the only time information this element sends** — see the note below. |
| 10 | Source IPv4 address | 5.3.11 | Absent. For X3 the addresses are inside the intercepted packet. |
| 11 | Destination IPv4 address | 5.3.12 | Absent, as above. |
| 12 | Source IPv6 address | 5.3.13 | Absent, as above. |
| 13 | Destination IPv6 address | 5.3.14 | Absent, as above. |
| 14 | Source port | 5.3.15 | Absent, as above. |
| 15 | Destination port | 5.3.16 | Absent, as above. |
| 16 | IP protocol | 5.3.17 | Absent, as above. |
| 17 | Matched target identifier | 5.3.18 | **Emitted** on X2, one occurrence per identity of the task that the subject presents, as the clause's bare UTF-8 fragment (`<supiimsi>…</supiimsi>`). Not emitted on X3, which table 5.3.3-2 does not require and a CC-POI tasked by packet criteria could not populate without inventing it. |
| 18 | Other target identifier | 5.3.19 | **Emitted** on X2, one occurrence per remaining identity **of that subject**. Not every identity the network function holds: the literal reading of table 5.3.2-2 would disclose unrelated subscribers to an agency holding a warrant for one. |
| 19 | MIME content type | 5.3.20 | Absent. Applies to the MIME payload format, which this project does not emit. |
| 20 | MIME content transfer encoding | 5.3.21 | Absent, as above. |
| 21 | Additional XID related information (AXRI) | 5.3.22 | Absent. TS 33.128 clause 5.3.3 makes it a "may" for efficient xCC delivery, not a requirement. |
| 22 | SDP session description | 5.3.23 | Absent. Applies to session descriptions this project does not intercept. |

## Payload formats — table 5.4.1-1

Values 1 to 16 were compared field by field against the table and the X2/X3 permissions
match it exactly. Value 0 is `N/A` in the specification, because a keepalive PDU carries no
payload, and is refused here on both interfaces, which is consistent with that. Value 17 was
introduced by CR019r2 in V1.8.1; the transcribed table carries the revision it was read from
so that the next addition is noticed.

Of these, this project emits exactly three: format 2 on X2, and formats 5 and 12 on X3.

| Value | Payload format | X2 | X3 | Disposition |
|---|---|---|---|---|
| 0 | Reserved for keepalive | N/A | N/A | Refused on both, consistent with the reservation. |
| 1 | ETSI TS 102 232-1 defined payload | Yes | Yes | Permitted, never emitted. |
| 2 | 3GPP TS 33.128 defined payload | Yes | Yes | **Emitted** — every xIRI record. |
| 3 | 3GPP TS 33.108 defined payload | Yes | Yes | Permitted, never emitted. |
| 4 | Proprietary payload | Yes | Yes | Permitted, never emitted. |
| 5 | IPv4 packet | Yes | Yes | **Emitted** on X3 — decapsulated inner IPv4. |
| 6 | IPv6 packet | Yes | Yes | Permitted, not currently emitted. |
| 7 | Ethernet frame | No | Yes | Permitted on X3, never emitted. |
| 8 | RTP packet | No | Yes | Permitted on X3, never emitted. |
| 9 | SIP message | Yes | No | Permitted on X2, never emitted. |
| 10 | DHCP message | Yes | No | Permitted on X2, never emitted. |
| 11 | RADIUS packet | Yes | No | Permitted on X2, never emitted. |
| 12 | GTP-U message | No | Yes | **Emitted** on X3 — the encapsulated form. Confirmed against the reference implementation; see below. |
| 13 | MSRP message | No | Yes | Permitted on X3, never emitted. |
| 14 | 3GPP TS 33.108 EpsIRIContent | Yes | No | Permitted on X2, never emitted. |
| 15 | MIME message | Yes | Yes | Permitted, never emitted. |
| 16 | 3GPP unstructured PDU | No | Yes | Permitted on X3, never emitted. |
| 17 | ETSI TS 102 232-1 PS-PDU.Payload | Yes | Yes | Permitted, never emitted. Added in V1.8.1. |

### Value 12, settled against the reference implementation

Which reading of value 12 an MDF implements is not something the specification text settles
on its own: "GTP-U message" could mean the encapsulated packet as it appears on N3, or an
inner packet the POI has labelled by the tunnel it arrived on. SD-Core's UPF CC-POI emits the
first — it copies downlink traffic after encapsulation, so what is delivered is the GTP-U
datagram.

**The reference implementation resolves it the same way.** Receiving from a UPF, its own
inbound handler logs the PDU it has parsed:

```
X2X3InboundHandlerAdapter : Received message: PduObject[majorVersion=0, minorVersion=5,
  pduType=X3_PDU, payloadFormat=GTP_U, payloadDirection=SENT_TO_TARGET, xid=…, correlationID=…,
  conditionalAttributeFields=[…], payload=…]
```

So the peer decodes value 12 into its own `GTP_U` payload format, accepts the PDU, and
retains it — with the payload this element sent, which is the encapsulated form. That is
agreement on the reading, established by interoperation rather than by argument.

**What it does not establish**, stated because the distinction is the whole point of this
document: the simulator accepting a payload is not the same as a mediation function
*validating* it. Nothing here shows that a strict MDF would reconstruct the session from the
encapsulated form, only that the reference implementation reads the label as this element
means it. The uplink copy is delivered as format 5 (decapsulated IPv4) and raises no such
question.

## What this disposition does not cover

An audit that finds nothing is not proof there is nothing, and this one is a reading. Stated
plainly, so nobody mistakes its scope for a clean bill of health:

- **The framing was read, not checked.** No test compares this package against TS 103 221-2,
  because ETSI publishes nothing machine-readable to compare it against. The reading is as
  good as the person who did it.
- **The byte-level layout is covered by interoperability, not by this document.** Offsets,
  lengths and field order are exercised every time this element delivers to the reference
  implementation. What that cannot see — and what this disposition is aimed at —
  is anything neither implementation emits.
- **Nothing here was verified against a mediation function that requires the missing
  attributes.** The gaps below are established from the specification text; how a strict MDF
  reacts to them has not been observed.
- **Only the profile this project uses was read.** TS 103 221-2 clause 6 defines a default
  profile that is mandatory to support and permits others; alternatives were not considered.
- **The keepalive mechanism is verified only against this project's own endpoint.** The
  reference implementation declares the Keepalive and Keepalive Acknowledgement PDU types and
  implements neither, so no independent implementation exercises the acknowledgement path.
  That is a limit of the evidence, not of the implementation.

## Known gaps, in order of consequence

1. **A fragmented IPv4 datagram whose first fragment has not been seen is discarded.** This
   describes SD-Core's UPF CC-POI, which builds X3 product with this package; it is a CC-POI
   limit rather than a wire-format one, and it is declared here because the loss
   it leaves is otherwise invisible: a copy dropped before framing leaves no gap in the X3
   sequence, so a mediation function cannot detect it.

   Only a transport-port criterion is affected, because it is the only one that has to read
   the packet. A fragmented datagram carries its transport header in the first fragment only,
   so the classification is taken from that fragment and applied to the datagram's later ones
   through a bounded, direction- and identity-scoped memo held for the life of the tasking
   generation. Every fragment of an authorised datagram is therefore delivered *provided the
   fragments arrive in order*, which is the normal case over a GTP-U path.

   What is not done is **retaining a fragment that arrives before the one carrying the
   transport header**. Holding copies would be a second mechanism on the one path in this
   element whose cost is per packet, in front of a datagram queue that holds ten by default,
   and it is reachable by a peer who can choose fragment order — a denial-of-service surface
   introduced to recover the rarest arrival order. So such a fragment is discarded, and
   reported through the same X3 content-loss condition a full delivery queue raises. The same
   applies to a classification that has expired or that the memo's ceiling refused to record.

   If measurement ever shows out-of-order arrival is material, holding is its own change with
   its own bounds. IPv6 fragmentation is out of scope for the reason the criteria disposition
   gives: SD-Core has no IPv6 PDU sessions.

2. **The emitted Version field is 5, and V1.10.1 defines 6.** Not a matter of this element's
   own conformance: what holds the bump back is that the only interoperability peer available
   refuses 0.6. See the version section above. **Blocked externally**, not deferred.
