<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# The statements the X1 provisioning decisions rest on

This file exists so that a reviewer can check every X1 provisioning decision against a
quoted statement without re-downloading a specification. Where a decision cites "the spec
says", the words it means are below, transcribed from the sources named here.

Three documents govern, and they are not interchangeable:

| Source | Version | What it settles |
|---|---|---|
| ETSI TS 103 221-1 | v1.21.1 (2025-08) | the field descriptions and the M/C/O markers — what a field *means* |
| 3GPP TS 33.128 | v18.16.0 | which of those fields a 5G POI must receive, and with what content |
| `TS_103_221_01.xsd` | 1.19.1 | the element names, order and value formats a peer's validator runs |

Where the prose and the schema could disagree, the prose is normative for behaviour and
the schema still decides what validates. See `README.md` for that tension and for where
each vendored file came from.

Ellipses inside a quotation are the transcription's; everything else is verbatim, including
the specifications' own inconsistent spacing.

## TS 103 221-1 clause 6.2.1.2 — the TaskDetails fields

**`ListOfDIDs`** — M:

> Details of where to send the intercepted traffic.
>
> It is an implementation decision for the NE to determine how to duplicate traffic if
> multiple destinations and/or destination sets are specified, or if multiple destinations
> or destination sets are supported.
>
> *Format:* List of Destination Identifiers (DID) and/or List of Destination Set
> Identifiers (DSID) referencing the desired delivery destination records.

**`ListOfMediationDetails`** — C:

> Set of details for use by an NE that is performing mediation (i.e. a mediation and
> delivery function). This shall be included between the ADMF and the MDF. Multiple
> instances of this parameter may be included (e.g. when multiple LIIDs are associated
> with an XID).

This is why a task carrying it is accepted with the field disregarded: the field is
addressed to a mediation function, and the AMF, SMF and UPF host POIs. Disregarding it is
what the specification asks; refusing it would refuse a legal task.

**`CorrelationID`** — O:

> Correlation identifier to assign to intercepted material for this Task. Intended for use
> in triggering scenarios, and shall be ignored by non-mediation function NEs.

The UPF's triggered CC-POI departs from this knowingly — see the last section.

**`ImplicitDeactivationAllowed`** — O:

> Indication that a Task may implicitly deactivate itself once the NE has determined that
> it has completed. On deactivation of the Task, the NE shall issue a ReportTaskIssue
> message with the appropriate TaskReportType (see clause 6.5.2).

The subject is a task that has *completed*, not one whose element has stopped hearing from
its ADMF. That is why the element disregards it rather than refusing it, and why the
keepalive purge is unrelated to it.

**`ProductID`** — O:

> When provided, shall be used by the receiving entity to populate the X2/X3 XID header as
> per ETSI TS 103 221-2 [19], clause 5.2.7 instead of the XID of the Task. If not provided,
> the XID of the Task shall be used.

**`ListOfServiceTypes`** — C:

> Shall be included when explicitly identifying the CSP-provided service(s) to be reported
> for this task. Details of the use of this field are left to the relevant LI architecture.

**`TaskDetailsExtensions`** — O:

> One or more extension placeholders; each may be populated by a list of elements defined
> by external specifications.

**`ListOfTrafficPolicyReferences`** — O:

> Ordered list  of TrafficPolicyReferences to be applied to the LITaskObject.
>
> *Format:* Given in ETSI TS 103 120 [28], clause 8.2.13 ListOfTrafficPolicyReferences.

A task carrying it is refused: it is an instruction *about the task*, defined in a
specification this element does not implement, and a policy silently unapplied is the same
undetectable divergence an unhonoured `listOfServiceTypes` would be.

And, immediately beneath the same table, two rejection rules:

> If a Task has an invalid combination of DeliveryType and Destinations (e.g. "X2andX3"
> delivery specified, but only an X2 Destination given), or an invalid combination of
> DeliveryType and any Destinations included in the Destination Set identified by the DSID,
> then the NE shall reject the ActivateTaskRequest with an appropriate error.

> If a Task has a ServiceType not supported by the NE, then the NE shall reject the
> ActivateTaskRequest with an appropriate error.

The second is implemented: such a task is refused with 3050. The first is enforced in
part: with `RequireResolvableDIDs`, a task ordering content with no X3 destination is
refused, and a task naming a destination identifier the element cannot resolve is refused
with 2040. Other combinations of DeliveryType and destinations are not checked.

## TS 103 221-1 clause 6.3.1 — CreateDestination

Clause 6.3.1.1, the response:

> OK or Error … The general errors in clause 6.7 apply. Also, it is an error if the DID is
> already present at the NE.

Clause 6.3.1.2, table 6.3.1.2-1 — `DID`, M:

> Destination Identifier which uniquely identifies the destination.
>
> *Format:* UUIDv4 (see clause 5.1).

That format is what the element validates, refusing anything outside it with 1010, and
the schema agrees: `DId` restricts
`etsi103280:UUID`, whose pattern is `[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`.
Note the pattern is lowercase-only and does not itself pin the version nibble.

`FriendlyName`, O:

> A human-readable name associated with the delivery destination.
>
> *Format:* Free-text string.

`DestinationDetailsExtensions`, O: as `TaskDetailsExtensions` above.

From clause 6.7's error table:

> 2030  DID already exists on the NE

## TS 103 221-1 annex E — destination sets, which this element refuses

> The ADMF issues a CreateObject request, containing the DestinationSetDetails object, to
> the NE (see clause 6.8.1).

> The ADMF issues an ActivateTask request containing the DestinationSetDetails Generic
> Object ID(s), also referred to as the DSID(s), to be used within the ListofDIDs field
> (see clause 6.2.1).

So a `dSId` is a Generic Object identifier. This element answers
`GetAllGenericObjectDetails` by omitting the list — the specification's own way of saying
Generic Objects are unsupported — and refuses the object CRUD outright, so a `dSId` can
never name anything it holds. Refusing a task whose destinations are named only that way is
therefore the consistent answer rather than an additional restriction.

What refusing avoids implementing, from table E.2.2-1 and the text beneath it:

> DestinationSetType … Enumerated value - one of "Redundant" or "Duplicate".

> Preference defines the DIDs order of use with the smallest integer indicating the most
> preferred DID(s). Should the most preferred DID(s) become unavailable the next preferred
> and available DID(s) shall be used.

> Where the DestinationSetType included within the DestinationSetDetails is "Duplicate",
> the NE will send copies of intercepted traffic to all DIDs within the Destination Set.

Redundancy with failover, in other words, and product duplication — neither of which this
element implements, and both of which an acknowledged-but-unresolved `dSId` would silently
promise.

## TS 33.128 — why ListOfDIDs is not optional for a 5G POI

Table 6.2.2.1-1, *ActivateTask message for the IRI-POI in the AMF*, the row SD-Core's
AMF is governed by — `ListOfDIDs`, **M**:

> Delivery endpoints for LI_X2 for the IRI-POI in the AMF. These delivery endpoints are
> configured using the CreateDestination message as described in ETSI TS 103 221-1 [7]
> clause 6.3.1 prior to the task activation.

That sentence is not peculiar to the AMF. The phrase "configured using the CreateDestination
message" occurs 48 times in TS 33.128 v18.16.0, and `ListOfDIDs` appears as a field name in
73 rows: 57 of them mark it M — every ActivateTask and ModifyTask table — and the other 16,
the Mediation Details tables for MDF2 and MDF3, mark it C. So for a POI the field is
mandatory: the schema permits an empty `listOfDIDs`, but 33.128 is normative on top of the
schema and does not.

Table 6.2.3-9, *ActivateTask message for triggering the UPF IRI-POI* — the table this
element is checked against:

> DeliveryType — Set to "X2Only". M

> TaskDetailsExtensions/HeaderReporting — Header reporting-specific tag to be carried in
> the TaskDetailsExtensions field of ETSI TS 103 221-1 [7]. See table 6.2.3.9.2-1. M

> ListOfDIDs — Delivery endpoints of LI_X2. These delivery endpoints shall be configured
> by the IRI-TF in the SMF using the CreateDestination message as described in ETSI
> TS 103 221-1 [7] clause 6.3.1 prior to first use. M

Read in context (clause 6.2.3.4) this table governs an IRI-POI *in the UPF*, triggered over
LI_T2, and it exists only to report packet header information — "if approach 1 described in
clause 6.2.3.9.1 is used for packet header information reporting". SD-Core's UPF hosts
no IRI-POI: it hosts a triggered CC-POI, which already takes its X3 destination from the
task. So the table describes a function that is absent rather than one that is wrong.

Table 6.2.2.1-1 also carries the one task extension this element recognises —
`TaskDetailsExtensions/IdentifierAssociationExtensions`, **C**:

> This field shall be included if the IRI POI is required to generate
> AMFIdentifierAssociation and AMFIdentifierDeassociation records (see clause 6.2.2.2.1).
> If the field is absent, AMFIdentifierAssociation and AMFIdentifierDeassociation records
> shall not be generated.

Every element accepts it on a task — an SMF or UPF has no identifier-association records to
produce, and loses nothing by being told which to produce — and refuses it on a destination,
where it would mean nothing.

## A knowing departure from a `shall`

TS 103 221-1 says `CorrelationID` "shall be ignored by non-mediation function NEs" (quoted
above). The triggered CC-POI in the UPF is not a mediation function and does not ignore it:
it stamps the value on every X3 PDU. TS 33.128 table 6.2.3-6 makes `CorrelationID`
mandatory on the LI_T3 trigger and defines it as the value that lets an MDF join content to
the signalling the IRI-POI reported, and where 33.128 specialises 221-1 for 5G, 33.128
governs. Recorded so that a reviewer who finds the contradiction finds the reasoning with
it.
