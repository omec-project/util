<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# Conformance dispositions

These packages implement a **subset** of the Lawful Interception standards they name. Three
dispositions record which subset, one per wire format, each stamped with the specification
revision it was made against. This page is the index and the list of open gaps; it does not
restate what the dispositions say, so the counts live in each disposition and are not
repeated here.

| Interface | Disposition | Made against | What it covers |
|---|---|---|---|
| **X1** — task provisioning | [`x1/CONFORMANCE.md`](x1/CONFORMANCE.md) | ETSI TS 103 221-1 **V1.21.1**, schema **v1.19.1** | Every clause 6 message: implemented, refused with which code, or diverging by decision. Peer authentication (clause 8). The client direction, where an SMF triggers a CC-POI over LI_T3 |
| **X2/X3** — xIRI and xCC delivery | [`x2x3/CONFORMANCE.md`](x2x3/CONFORMANCE.md) | ETSI TS 103 221-2 **V1.10.1** | Header fields, PDU types, payload directions and formats, and every conditional attribute of table 5.3.1-2 |
| **IRI records** — record content | [`iri/CONFORMANCE.md`](iri/CONFORMANCE.md) | 3GPP TS 33.128 **V18.16.0** and its `TS33128Payloads.asn` | Every mandatory field of every record these packages encode, and a verdict for every conditional field |

## Open gaps, across all three

Ordered by consequence, not by interface. Each is stated in full where it belongs; here it is
named with enough detail that a reader can tell whether it matters to them.

### IRI records

1. **A 13-octet `MANAGE UE POLICY COMMAND` is not delivered.** One PLMN, one instruction
   deleting one policy section, and neither optional IE: an ordinary PCF operation, and
   shorter than the `SIZE(16..65540)` TS 33.128 gives `UEPolicy`. The record is refused rather
   than padded, and the network function reports a task-scoped `recordNotEncoded` naming the
   warrant and the record type. Declared against TS 33.128, whose bound would have to be 13 —
   `iri/CONFORMANCE.md`, *Open*.
2. **No conditional field is known to be held and left unreported.** The conditional fields
   whose source has not been traced are counted in `iri/CONFORMANCE.md` as `UNTRACED`, which
   that disposition labels "an admission, not a conclusion".
3. **Two smaller ones**, each in `iri/CONFORMANCE.md` under *Open*: the AMF's records do not
   agree on whether they carry `location`; and `currentLoc` cannot be reported as `false`,
   which is latent while no record reports a location that is not current.

### X2/X3

4. **A fragmented IPv4 datagram whose first fragment has not been seen is discarded** by the
   UPF CC-POI, where a transport-port criterion applies. The loss leaves no gap in the X3
   sequence, so a mediation function cannot detect it; the element reports it to the ADMF as
   X3 content loss. `x2x3/CONFORMANCE.md`, *Known gaps*.
5. **The emitted Version field is 5, where V1.10.1 defines 6.** Blocked externally: the
   mechanism the bump depends on is implemented, and the only interoperability peer available
   refuses 0.6.
6. **The keepalive acknowledgement path has not been verified against an independent
   implementation.** The mechanism is implemented on both interfaces; the reference
   implementation declares the PDU types and uses them nowhere, so only these packages' own
   endpoints have exercised it. A limit of the evidence rather than of the implementation.

### X1

7. **The content POI cannot observe, at element scope, a datapath that disagrees with its own
   record.** Where this element created the uncertainty it answers at task scope
   (`duplicationNotProgrammed`); the general case needs a read path into the datapath that the
   BESS module does not offer. Declared, open — `x1/CONFORMANCE.md`, *Interrogation*.
8. **No strict third-party ADMF has exercised the implementation.** The interoperability
   evidence is the reference simulator and these packages' own endpoints, so a fault only a
   strict validator would reject has not been exercised. A limit of the evidence rather than
   of the implementation.
9. **Four smaller ones**, each in `x1/CONFORMANCE.md` under *Known gaps*: a destination cannot
   be corrected or removed over X1 while a task references one; an element in the X1 namespace
   that the schema does not define is refused only where a choice or an extension is read; a
   provisioned `correlationID` of 0 is not reported back; the fault channel is best-effort.

## Deliberate deviations

The places where these packages depart from the letter of TS 103 221-1, each for a stated
reason and recorded in full in `x1/CONFORMANCE.md`:

- **`CorrelationID` is acted on by the triggered CC-POI**, which clause 6.2.1.2 says a
  non-mediation NE shall ignore; TS 33.128 table 6.2.3-6 makes it mandatory on the LI_T3
  trigger.
- **The emitted X1 Version is `v1.6.1`** while the behaviour is built against V1.21.1.
- **An `ActivateTask` repeating a held task exactly is acknowledged**, where table 6.2.1.1-2
  makes a held XID an error: a retried activation stays idempotent. A different task under a
  held XID is refused with 2010.
- **Warnings and alerts are not logged at the element**, which clause 5.3 requires "for audit
  reasons": they are reported to the ADMF over X1 and nowhere else, because a local log would
  make interception detectable to whoever operates the network function.

The X2/X3 Version field above is a gap, not a deviation: it is held at 5 by the
interoperability peer, not by choice.

## Capabilities refused by design, which are not gaps

These capabilities are refused by design rather than pending. Each was read against the
question "what here is mandatory", and none is. They are recorded so that a refusal is
distinguishable from unfinished work: a capability the element does not implement is
declared, not faked. `README.md`'s feature table lists every refusal, including smaller ones
not repeated here.

- **Generic Objects** (TS 103 221-1 clause 6.8), and therefore **destination sets** (`dSId`,
  annex E), which are Generic Objects.
- **Traffic policies** (`listOfTrafficPolicyReferences`, annex F). Marked **O**, which
  functions receive them is left to LEA–CSP agreement and out of scope by annex F.1, and
  they are a content-plane instrument with no meaning at an IRI-POI.
- **Content interception for more than one warrant covering the same session.** Where several
  tasks' criteria cover one PFCP session, the session's content is shipped under the first of
  them — the lowest XID, chosen stably — and under that task's own criteria: a copy that task's
  criteria exclude (a tunnel-direction filter, say) is dropped, not offered to the next task, so
  where the overlap is partial part of the session's content reaches no warrant at all. That
  first-covering-wins rule is a declared boundary. Each task left without that session's
  content is reported in fault (`contentUnderAnotherTask`, non-terminating, against its own
  XID) until the overlap ends, saying its content is delivered under a lower-XID task, or not at
  all where that task's own criteria exclude it. The fault does not satisfy the warrants
  receiving nothing, and is not described as though it does: it tells the ADMF which of its
  tasks is losing content, so the loss is visible rather than silent. Interception-related information is unaffected; several tasks matching one
  subject each produce their own xIRI.
- **Service-type scoping of a task** (`listOfServiceTypes`), which is refused rather than
  acknowledged and ignored.
- **A provisioned `correlationID` at an information point of interception** (TS 103 221-1
  clause 6.2.1.2). Refused at the AMF and the SMF, honoured at the UPF: the one disposition in
  this list that depends on which element is asked rather than on the field.

  A content POI stamps the provisioned value on every unit it delivers, and an LI_T3 trigger
  carries one mandatorily, so the UPF acts on it. An IRI-POI in a 5G core cannot: the
  correlation that joins its records to a session's content is derived from the session — the
  SMF sends its F-SEID — and one task covers many sessions, so a single provisioned value
  applied to all of them would join at the mediation function what the network keeps separate.
  That is not what an ADMF asking for a correlation wants, and the element cannot ask which it
  meant.

  Accepting the field and ignoring it would acknowledge an interception different from the one
  the ADMF authorised, with no channel through which the divergence could be reported: a task
  field that cannot be honoured is refused, not ignored. An AMF or SMF task provisioning
  `correlationID` is refused with error 3000 or 3001, naming the reason. Refusing it at every
  element was considered and rejected: it would refuse tasking an ADMF may legitimately send to
  several elements at once, which is the reasoning the recognised-extension case follows.

## Two record-shape questions settled by reading, not inference

Recorded here because the answer to the first decides the shape of a record rather than only
its content.

**An `AMFDeregistration` names both accesses in one record, not one record per access.**
TS 33.128 V18.16.0 table 6.2.2.2.3-1 makes `accessType` mandatory with cardinality 1, and
`TS33128Payloads.asn` defines

```
AccessType ::= ENUMERATED { threeGPPAccess(1), nonThreeGPPAccess(2), threeGPPandNonThreeGPPAccess(3) }
```

so "both" is a value of the single field. Clause 6.2.2.2.3's own trigger text agrees: the
xIRI is generated when the IRI-POI detects that a UE "has deregistered from the 5GS over
at least one access type" — one record about a deregistration, however many accesses it
covered. The AMF therefore reports the scope it acted on (the access the UE requested,
which NAS can express as both) rather than the access the request happened to arrive over.

**The identifier binding is one binding across accesses**, so `AMFIdentifierDeassociation`
is emitted when the UE is left registered on none — not once per deregistration. A
dual-registered UE deregistering one access keeps its SUPI↔5G-GUTI binding, and the
element goes on producing records under it.

## How to read a disposition

Each follows the same shape: what it is and what it is not, the per-clause or per-table
dispositions, an explicit *what this does not cover*, and *known gaps in order of
consequence*. The middle section is the substance; the third is what stops the document being
mistaken for a clean bill of health.

Two of the three are readings rather than checks, because ETSI publishes nothing
machine-readable for X2/X3 framing, and 3GPP's ASN.1 marks conditional and optional fields
alike as `OPTIONAL` while the M/C/O markers live in the payload tables. X1 is the exception
and has both a schema validator and a drift check — neither of which can notice that a
"shall" in the prose has no code behind it, which is what reading the prose is for.
