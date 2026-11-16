<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# li — Lawful Interception for SD-Core

The packages under `github.com/omec-project/util/li` provide the in-network
**Points of Interception (POIs)** and the X1/X2/X3 interfaces that let SD-Core meet
a Communication Service Provider's Lawful Interception (LI) obligations. They are
imported by the AMF, SMF, and UPF.

The Administration (ADMF/LIPF) and Mediation & Delivery (MDF2/MDF3) functions
are **external, third-party systems** (for testing, an ETSI TS 103 221 X1/X2/X3
simulator) — SD-Core implements only the POIs and the interfaces toward them.

LI is **opt-in and undetectable**: with no LI configuration a network function
behaves and looks exactly as before, and even when active it emits nothing to
general logs, metrics, or signalling that would reveal that a subscriber — or
the network element itself — is being intercepted.

> **Scope of this document.** These packages are a library. What this document says
> about network-function configuration keys, Helm and OnRamp values, the BESS datapath and
> deployment behaviour describes the SD-Core consumers — the AMF, SMF and UPF — which are
> delivered in their own repositories.

---

## How it works

Three network functions act as POIs:

| NF  | Role | Produces | Interface |
|-----|------|----------|-----------|
| **AMF** | IRI-POI | Intercept Related Information (registration, deregistration, location update, identifier (de)association, unsuccessful procedure, start-of-interception) as 3GPP TS 33.128 **xIRI** | X2 → MDF2 |
| **SMF** | IRI-POI + CC Triggering Function | PDU-session xIRI (establishment/modification/release/start-of-interception); instructs the UPF to duplicate a tasked target's user plane, and tasks its interception point with the warrant | X2 → MDF2; PFCP `DUPL` FAR + X1 trigger → UPF |
| **UPF** | CC-POI | Content of Communication — the duplicated user-plane packets — as ETSI TS 103 221-2 **xCC** | X3 → MDF3 |

```
                 ┌─────────────┐
   task warrants │ ADMF / LIPF │  (external)
   over X1 ─────▶│             │
   (mTLS)        └─────────────┘
                   │ X1        │ X1
                   ▼           ▼
              ┌────────┐   ┌────────┐  PFCP   ┌──────┐
              │  AMF   │   │  SMF   │──DUPL──▶│ UPF  │
              │ IRI-POI│   │IRI-POI │  FAR    │CC-POI│
              │        │   │ CC-TF  │───X1───▶│      │
              └────┬───┘   └───┬────┘ trigger └──┬───┘
             xIRI  │   xIRI    │             xCC │
             X2    ▼   X2      ▼             X3  ▼
              ┌──────────────────┐         ┌──────────┐
              │       MDF2       │         │   MDF3   │   (external)
              └──────────────────┘         └──────────┘
```

- The **ADMF** provisions interception tasks (warrants) over **X1** (ETSI TS 103 221-1,
  XML over mutual TLS). All three network functions expose an X1 listener, but the
  UPF's authorised peer is the **SMF**, not the ADMF: the UPF is a *triggered* point
  of interception, and the SMF's triggering function tasks it in the ADMF's role.
- Each tasked network function matches events/traffic against the target
  (by SUPI, PEI, or GPSI) using a local task store — no external lookup at
  interception time.
- **xIRI** is delivered over **X2** to the configured **MDF2**; **xCC** over
  **X3** to the configured **MDF3** (both ETSI TS 103 221-2, mutual TLS).
- **CC takes two interfaces, and needs both.** PFCP tells the UPF *to* copy a
  session's packets; the X1 trigger tells it *whose warrant* the copies serve,
  which correlation identifier to stamp on them, and where to send them. A copy
  the UPF cannot attribute to a warrant is discarded and reported, never delivered,
  because a mediation function attributes product by warrant alone and silently
  drops what it cannot place.
- **CC** works via PFCP: the SMF sets the `DUPL` apply-action + Duplicating
  Parameters on the target session's FAR; the UPF's BESS datapath tees a copy of
  the packets to a userspace socket, and the UPF's X3 shipper frames and
  delivers them.
- **X1 keepalive (TS 103 221-1 clause 6.6.2, enabled by default):** the element runs
  timer P2 (default 1 h) over the X1 requests it receives. When P2 expires with nothing
  received, it reports fault 9050 "Keepalives not received" to the ADMF and starts P1
  (default 1 min). An OK answer starts P3 (default 2 h); when P3 expires, or P1 expires with
  no OK answer, it deactivates all tasks and reports Alert 10000 "Database cleared" — so
  warrants do not outlive an operational controller — unless it is configured to retain
  tasking. Any X1 request ends the procedure, and a delivered fault 9050 is cleared with
  `FaultCleared`, sent again with the next request until one arrives. **At the UPF**, which is
  tasked over LI_T3 by the SMF's triggering function, the ADMF's answer does not count: TS
  33.128 clause 5.2.6 makes the triggering function the UPF's "ADMF", and it cannot answer,
  so the UPF deactivates all tasks when P1 expires and has no P3.
- **X2/X3 keepalive (on by default):** a different mechanism, in the other
  direction. ETSI TS 103 221-2 clause 6.2.4 has each POI send a Keepalive PDU on
  every delivery connection at least every TIME_P1 (60 s), and disconnect and
  reconnect if nothing acknowledges within TIME_P2 (180 s). This element does that
  at every expiry and reports `mdfUnreachable` once per outage: only a valid
  acknowledgement ends one, so a silence after it is reported again
  (`x2x3/CONFORMANCE.md`, PDU type 4). It runs unconditionally, not only when a
  connection is idle: product PDUs are never acknowledged, so traffic proves the
  socket is open and nothing about whether the mediation function behind it is
  still running.

  Both timers are deployment configuration (`x2x3KeepaliveTimeP1`,
  `x2x3KeepaliveTimeP2`; TIME_P2 must exceed TIME_P1), and `x2x3KeepaliveEnabled:
  false` turns it off. **That switch is for one situation:** a mediation function
  that does not implement the MDF half of the clause and therefore never
  acknowledges. This element would disconnect such a peer every TIME_P2 and lose
  whatever was in flight, and a refusal at the TLS layer would stand against it
  for good, since only an acknowledgement lifts one, so every delivery to it would
  be refused even once it accepts this element's certificate. A test simulator
  that answers no Keepalive is such a peer, and a deployment pointed at one sets
  the switch false.
- **Security:** X1/X2/X3 all use mutual TLS with credentials from a dedicated LI
  PKI, kept separate from the SBI certificates. Verification is never skipped.

### Package layout

Each package is imported as `github.com/omec-project/util/li/<package>`.

| Package | Purpose |
|---------|---------|
| `types`  | Target identifiers, tasks, product/delivery types (transport-agnostic) |
| `store`  | Concurrency-safe active-task store, indexed by target identifier |
| `x1`     | ETSI TS 103 221-1 X1 provisioning listener + NE-issue reporter (ADMF direction) |
| `iri`    | 3GPP TS 33.128 xIRI record builders + DER encoder over `encoding/asn1` (no decoder: see `docs/asn1-decoding.md`) |
| `x2x3`   | ETSI TS 103 221-2 X2/X3 PDU framing + delivery client (see `x2x3/CONFORMANCE.md`) |
| `mtls`   | Loads the LI PKI credentials and builds the X1/X2/X3 TLS configs |

---

## Feature support

What these packages and the network functions using them implement, and what they do not. The
gaps are listed as plainly as the capabilities: an operator planning an
integration needs both, and a missing feature discovered during a deployment is
more expensive than one read here.

### Supported

| Area | Feature | Notes |
|---|---|---|
| **X1 provisioning** | `ActivateTask`, `ModifyTask`, `DeactivateTask` | ETSI TS 103 221-1, mutual TLS |
| | `Keepalive`, and the clause 6.6.2 procedure when it stops | Enabled by default: fault 9050 when timer P2 expires; all tasks deactivated with Alert 10000 when P3 expires after an OK answer, or P1 expires without one, unless configured to retain tasking; `FaultCleared` when the ADMF returns |
| | The whole interrogation set clause 6.4.1 makes mandatory: `GetTaskDetails`, `GetAllDetails`, `GetAllTaskDetails`, `GetDestinationDetails`, `GetAllDestinationDetails`, `GetNEStatus`, `ListAllDetails`, `GetAllGenericObjectDetails` | How an ADMF audits what an element actually holds — and how it reconciles after one restarts. Holding nothing is a successful empty answer, not an error. In the tests, every response is validated against the published XSD |
| | `DeactivateAllTasks`, **enabled by default** | The specification's own default. One authenticated message stops every interception on the element — see *[Bulk deactivation is enabled by default](#bulk-deactivation-is-enabled-by-default)* |
| | `RemoveAllDestinations`, **enabled by default** | The specification's own default (TS 103 221-1 clause 6.3.4), and its guard: refused while any destination is referenced by a task (8010). Answered with the specification's error when disabled (8020) — see *[Bulk deactivation is enabled by default](#bulk-deactivation-is-enabled-by-default)* |
| | `CreateDestination` | DID → endpoint; the `dId` is validated as the UUID the schema defines, and re-creating one is refused with 2030 (clause 6.3.1.1) |
| | Identifiers validated where they enter | `dId`, `xId` and `productID` are all `etsi103280:UUID` in the schema; a value outside that format is refused with 1010 rather than stored and acted on. The format is all that is checked, as the schema's pattern does: the version and variant digits are not |
| | **A target identifier populating more than one arm is refused** | `targetIdentifier` is an `xs:choice`, so two populated arms cannot occur and no reading of such a message is authoritative — 1010, as the schema error it is. Selecting one would mean this element deciding the scope of an interception your ADMF ordered. It does **not** restrict a task to one identifier: a `targetIdentifiers` *list* naming several is normal, and all of them must match (TS 103 221-1 table 6.2.1.2-1) |
| | Every criterion of a nested LI_T3 list is applied | `UPFLIT3TargetIdentifier` is a sequence of choices, so a list of several is what it is for; one that cannot be mapped refuses the whole task, naming which member of how many |
| | An unparseable request is answered with `TopLevelError` | Clause 6.1's four-field response, at HTTP 200 — clause 7.2.2.2 reserves HTTP error codes for HTTP-level errors |
| | **Every X1 response this element receives is bound to the request that produced it** | Response type, `x1TransactionId`, both element identities and the version. A well-formed acknowledgement from an endpoint naming a different NE is refused, and reported as `x1ResponseUnattributable` — distinct from the POI refusing, because the operator action is the opposite one |
| | **An `OK` of `Acknowledged` is not taken as done** | Clause 5.2.2: the answer when the action "cannot be completed within TIME1", with completion reported later. `x1.Requester` returns it as a distinct pending error, so a triggering function does not treat a trigger as installed on it; an `OK` value outside the schema's enumeration is refused |
| | `DeactivateTask` refuses what it cannot honour | Table 6.2.3-2: "it is an error if the XID is not already present at the NE" — 2020. See *[A deactivation is not always a success](#a-deactivation-is-not-always-a-success)* |
| **Delivery destinations** | **A task's product goes to the destinations the task named** | `listOfDIDs`, for X2 as for X3. TS 33.128 marks it mandatory in every ActivateTask table it defines — see *[Where a task's product goes](#where-a-tasks-product-goes)* for the three sources and their precedence |
| | Destinations **provisioned over X1** with `CreateDestination` | Source 1, and the one that wins |
| | Destinations **declared in configuration** as a DID→endpoint mapping | Source 2. For endpoints an operator and an ADMF agreed out of band; resolves exactly as a provisioned one does |
| | A **configured default endpoint** (`mdf2`) | Source 3, for a task that names **no destination**. A task that names one this element cannot resolve is refused, not served from configuration |
| | An `X2andX3` destination serves both interfaces | One destination, two endpoints |
| | Clause 8.2.4 peer authentication | Identity binding checked per message: 1030 / 1040 / 1060 |
| | NE-initiated fault reporting, at the scope the fault has | `ReportNEIssue`, `ReportTaskIssue` and `ReportDestinationIssue`, with schema-valid message types and issue codes. An issue relating to one delivery destination names that destination's DID (clause 6.5.3), rather than being reported as a fault of the whole element — an ADMF that provisioned several otherwise cannot tell which one failed. Where one endpoint serves several DIDs, each is reported |
| | **A fault that ends is reported as having ended** | Clause 5.3: `AllClear` at task and destination scope, `FaultCleared` at element scope. An element that reports every beginning and no ending leaves an ADMF holding a list that only grows. Rate limiting is per condition and never applies across a state change, so a destination that fails and recovers inside one window still produces both reports |
| | The element answers for the conditions it can currently observe | `GetNEStatus` reports `mdfUnreachable` while delivery is failing and `x3EgressDown` while the datapath egress is down, and stops reporting each when it stops holding — see *Asking an element how it is* below |
| **Targets** | SUPI/IMSI, PEI/IMEI, GPSI/MSISDN | |
| | Six of the nine LI_T3 detection criteria of TS 33.128 table 6.2.3-7, plus UE IP Address for IPv4 | Session ID, tunnel ID, TCP/UDP port, network instance, tunnel direction, PDR; UE IP Address for IPv4. **PDR ID and QER ID are refused**, because they name a rule allocated per PFCP session and reused across sessions — see *LI_T3 detection criteria* below |
| | Several criteria on one task, all of which must match | TS 103 221-1 table 6.2.1.2-1: "Where multiple criteria are present, all criteria are required to be matched." A combination this element cannot evaluate together is refused before the task is acknowledged |
| | Criteria replaced by `ModifyTask`, mid-interception | Table 6.2.3-8. The task is not torn down: superseded traffic stops, newly selected traffic starts, attribution is unchanged |
| | A `ModifyTask` that changes the **products** a task requires | Adding CC begins content interception for the target's existing sessions; removing it withdraws the trigger and clears the duplication. Derived from the task as a whole rather than from its target identifiers, so a change that leaves the target alone is still a change |
| | A criterion this element cannot evaluate is refused **before** the task is acknowledged | Accepting one would leave the requesting function believing an interception is running that can never produce anything — undiscoverable from outside |
| **IRI (X2)** | Every xIRI record TS 33.128 defines for the AMF and SMF is accounted for — produced, or out of scope with the reason | 16 of the 25 produced; see *Which xIRI records this produces* below. A count is not a coverage claim, so the records are listed |
| | TS 33.128 ASN.1 (BER) encoding | Each record's tags, optionality and value bounds are audited against the text of the published module, and every encoding is pinned as fixed hex |
| | Activation mid-session, on already-established sessions | |
| | Delivery asynchronous, off the signalling path | A slow MDF cannot delay a target's signalling |
| **CC (X3)** | UPF CC-POI duplication via the PFCP `DUPL` apply-action | Both directions |
| | The CC-POI enables duplication itself, for criteria the SMF has not marked | So a task keyed by an address or a tunnel intercepts, rather than being acknowledged and producing nothing |
| | Duplication survives the SMF's own session modifications | Re-derived from the tasking wherever rules change; the SMF's `DUPL` bit is never overwritten |
| | Criteria apply to sessions established after the task | No re-tasking when a subscriber attaches later |
| | Copies the duplication over-collected are dropped before delivery | See *the coverage model* below |
| | Every X3 destination a task names receives that task's content | Each copy carries the same sequence number at every destination, and one unreachable mediation function does not deny the others |
| | Tasking that does not require Content of Communication is **refused** | A UPF produces xCC and nothing else, so a trigger whose `deliveryType` is `X2Only` is refused rather than acknowledged — with 3040 when the destinations it names are X3-only (TS 103 221-1 clause 6.2.1.2), otherwise by the UPF's `CanApply` with 3000/3001. Accepting it would tell the triggering function an interception is running that will deliver nothing, and would have the datapath duplicate a subject's traffic so every copy could be discarded |
| | LI_T3 triggering interface, with the SMF as triggering function | TS 33.128 clause 6.2.3.3 |
| | Correlation joining X2 and X3 | The session's real F-SEID |
| | `ProductID` → X2/X3 XID labelling | TS 103 221-1 clause 6.2.1.2, as a general rule |
| | Untasked or unattributable content dropped **and reported** | Never delivered with a label an MDF would discard |
| **Security** | Mutual TLS on X1, X2 and X3, from an X0-provisioned LI PKI | |
| | Undetectability: nothing in any general operator log | No target, warrant, ADMF/MDF address or LI configuration |
| | Unauthorised peers refused silently, reported only to the ADMF | |
| | Per-warrant delivery isolation | Unconditional: no agency's product reaches another's endpoint, for any product type and any delivery path |
| | Concurrent warrants from several agencies, **for IRI** | Each matching task produces its own xIRI, separately numbered, to its own destinations. For content the answer is different — see the gaps table |
| | Subscriber traffic and usage accounting unchanged | Measured exactly once despite duplication |
| **Operational** | UPF restart: interception continues, with no operator action | The destination is re-provisioned and the POI re-triggered |
| | SMF/AMF restart: tasking is lost, the ADMF is told, and it can interrogate the element to reconcile | The whole sequence in one place: *[What happens after a restart](#what-happens-after-a-restart-and-what-the-admf-does-about-it)* |
| | Tasking left behind by a previous process is withdrawn at startup | Retried per UPF until each answers, and the UPF is kept alive only once it has |
| | A withdrawal the UPF does not acknowledge is retried, not forgotten | With the failure and the still-unwithdrawn condition reported separately — see *When a withdrawal cannot be delivered* below |
| | UPF address change (its Service recreated): triggering recovers | No SMF restart needed |

### Not supported

| Area | Feature | Why, or status |
|---|---|---|
| **Delivery destinations** | Destination **sets** (`dSId`) | **Refused**. A set is a `DestinationSetDetails` Generic Object (annex E), and this element implements none — so a `dSId` can never name anything it holds, while carrying failover and duplication semantics an acknowledgement would silently promise. Neither half is stated as mandatory: TS 103 221-1 provides for an NE that does not support Generic Objects ("may be omitted if Generic Objects are not supported"), `ListOfDIDs` accepts "DID **and/or** DSID", and `DestinationSetType: Redundant` turns on a downstream availability check that annex E.1 places "outside the scope of the present document" |
| | `ModifyDestination`, `RemoveDestination` | Not implemented. TS 103 221-1 does not state either as mandatory, unlike the interrogation set above |
| | Delivery to a URI, an E.164 number or an email address | **Refused** with 6020. `deliveryAddress` must be an IP address and port |
| **Generic Objects** | The whole capability, so `CreateObject`, `ModifyObject`, `GetObject`, `DeleteObject`, `ListObjectsOfType`, `DeleteAllObjects` | **Refused**, not acknowledged. These are conditional — "`DeleteAllObjects` shall be supported *if* the implementation supports Generic Objects" — and an acknowledgement would tell an ADMF its object had been stored. The mandatory *query*, `GetAllGenericObjectDetails`, is answered with its object list **omitted**, which is how the standard says a Generic-Object-less element answers; an empty list would claim they are implemented and none is held |
| **Targets** | The IPv6 form of one LI_T3 detection criterion, **UE IP Address** | *Refused*, not ignored — and an interception-scope question rather than an LI one: SD-Core has no IPv6 PDU sessions to intercept. See the breakdown below |
| | Service-type scoping of a task (`listOfServiceTypes`) | Not applied, so a task carrying it is **refused** (3050) rather than acknowledged — accepting it would deliver every service for the target when a narrower set was authorised |
| **Task fields** | Traffic policies (`listOfTrafficPolicyReferences`) | **Refused** with 3000/3001, the most specific code table 6.7-3 offers. Optional (marked O), and annex F.1 leaves which functions receive traffic policy information "for agreement between the LEA and CSP … out of scope of the present document". They are also a content-plane instrument — every TS 103 120 criterion matches packets and every action shapes packet delivery — so none applies at an IRI-POI, and accepting one unapplied could deliver full content where headers or nothing was authorised |
| | Any `taskDetailsExtensions` or `destinationDetailsExtensions` other than the one below | **Refused**. An extension exists in order to change the meaning of the message carrying it, so an unknown one cannot be ignored. This includes `TaskDetailsExtensions/HeaderReporting`, which TS 33.128 tables 6.2.3-0A and 6.2.3-9 mark C and M respectively — packet header information reporting is not implemented, so a task asking for it is refused rather than acknowledged and ignored |
| | Mediation details (`listOfMediationDetails`) | **Accepted and disregarded**, deliberately — see *[Fields accepted and disregarded](#fields-accepted-and-disregarded)* |
| | `implicitDeactivationAllowed` | **Accepted and disregarded**, likewise |
| | A provisioned `correlationID` at the AMF or SMF (TS 103 221-1 clause 6.2.1.2) | **Refused** with 3000/3001 rather than accepted and disregarded. The UPF's CC-POI honours it — it stamps the value on every X3 PDU — but an IRI-POI's correlation is derived per session, one task covers many sessions, and a single provisioned value across them would join at the mediation function what the network keeps separate. See `CONFORMANCE.md` |
| **CC (X3)** | Content for **more than one warrant covering the same session** | **Declared, not deferred.** Where several tasks' detection criteria cover one PFCP session, the CC-POI ships that session's content under the first of them — the lowest XID, chosen stably so the session's packets are never divided among the covering warrants — and under that task's own criteria; the others receive none of it. **First covering wins, and that is a declared boundary**: a copy the first task's criteria exclude (a tunnel-direction filter, say) is dropped, not offered to the next covering task, so where the overlap is partial part of the session's content reaches no warrant at all. Each task left without the session's content is reported in fault (`contentUnderAnotherTask`, a ReportTaskIssue against its own XID, non-terminating, cleared with an AllClear when the overlap ends) whose detail reads "N session(s) this task selects are also selected by a lower-XID task; their content is delivered under that task, or not at all where that task's own criteria exclude it", and **that fault does not satisfy the unserved warrants**: it tells the ADMF which of its tasks is losing content, so the loss is visible, not that the content reached it. Implementing it would need one logically identical X3 PDU per covering task, each with its own XID, correlation context, sequence numbering and destinations — per-packet fan-out proportional to the overlapping warrants, at the one point of interception whose cost is per packet rather than per event. Worth revisiting for a deployment that runs concurrent warrants from different agencies against one subscriber. IRI is unaffected: several tasks matching one subject each produce their own xIRI |
| **Provisioning** | More than one ADMF per network element | One responsible ADMF; a second identity is refused |
| **State** | Warrants persisted across a restart | Deliberate. Interception must not outlive contact with the function that authorised it, so the failure direction is "stopped" — which means a planned upgrade needs re-provisioning in the runbook. What the ADMF is told, and what it asks next, is in *[What happens after a restart](#what-happens-after-a-restart-and-what-the-admf-does-about-it)* |
| **Scope** | HI1/HI2/HI3 and delivery to the LEMF | The mediation function's role, not a POI's |
| | 4G/LTE interception (MME, S-GW) | 5G only |
| | IMS/voice and SMS content | |
| | Location detail beyond the mandatory minimum | Records carry the minimal `Location` the schema requires; richer detail is deferred |

#### Where a task's product goes

A task names its delivery endpoints in `listOfDIDs`, and the product of that task goes to
those endpoints. 3GPP TS 33.128 marks the field **M** in every ActivateTask table it
defines — the phrase "configured using the CreateDestination message" occurs 48 times in
v18.16.0 — so this is not optional for a 5G POI, and an element that substituted its own
configuration would be diverging from the warrant.

**Ignoring the task's destinations would be a disclosure.** An element sending every xIRI to
the `mdf2` address in its own configuration is indistinguishable from a correct one while one
agency is configured; with two warrants provisioned to two agencies' MDF2s, both agencies'
product would arrive at whichever address configuration names — a disclosure to an agency
holding no warrant for it. So the AMF and SMF IRI-POIs deliver each task's product to the
destinations it names.

An element resolves a DID from three sources, in this order:

| | Source | When it applies |
|---|---|---|
| 1 | **Provisioned over X1** with `CreateDestination` | Always wins where it resolves. What TS 33.128 mandates |
| 2 | **Declared in configuration**, as a DID→endpoint mapping | For destinations agreed out of band. Resolves exactly as a provisioned one does, so a task naming such a DID is accepted |
| 3 | **The configured default endpoint** (`mdf2`) | Only for a task that names **no destination at all**. Naming none the element can resolve is a different fact, and is refused — see below |

**A destination identifier this element cannot resolve is refused, not replaced.** Source 3
serves a task that named nothing — a gap the provisioning function left, which the
configured endpoint fills, and the case of a provisioning function that does not use
destinations. A task that names identifiers and resolves none of them, or only some of
them, is a different fact: it is an assertion the element cannot honour as stated, and
substituting an endpoint of its own is the element deciding where a warrant's product goes.
On an element serving several agencies the product then goes to whichever endpoint local
configuration names, which is the disclosure this whole section exists to prevent, reached
from the other direction. So such a task is refused with 2040 ("dId does not exist on the
NE") before it is stored, before any callback and before any trigger, and the refusal names
the identifier on the X1 answer.

**The destinations must between them serve the task's `deliveryType`.** Clause 6.2.1.2: a task
whose `deliveryType` and destinations do not agree — "X2andX3" delivery with only an X2
destination given, say — is rejected, and table 6.7-3 has a code for it, **3040**. The
element does not make up the missing half from its configured default: that endpoint serves a
task that names no destination, not one that names the wrong kind.

A deployment whose ADMF names DIDs it never provisioned at this element, expecting `mdf2` to
serve them, receives refusals. The remedy is to declare those DIDs in the element's
configuration — source 2, which is a supported arrangement and resolves.

Configuration is a supported way of supplying a destination, not a degraded one. Nothing
in either specification requires that a DID arrived over X1 rather than having been
agreed; what TS 33.128 requires is that the task names its destinations and the element
delivers to what it named. Source 3 exists so that a provisioning function naming no
destinations is still served.

What is deliberately **not** offered is a switch making configuration override the task's
destinations. That would make ignoring the task's destinations a supported option, and an
operator who set it would be non-conformant with no signal that they were.

Where sources 1 and 2 declare the same DID, the provisioned one is used — and the element
says so. `GetDestinationDetails`, `GetAllDestinationDetails` and `GetAllDetails` report
configured entries alongside provisioned ones, each marked in its `friendlyName`:

```
provisioned over X1
declared in this element's configuration, not provisioned over X1
provisioned over X1, superseding a configured entry for this DID
```

An ADMF's own `friendlyName`, where it gave one, leads and the note is appended in
parentheses. It is the only free-text field a reported destination has, and precedence
resolved invisibly is the one thing a three-source design must not do.

**One case is refused rather than resolved: creating a destination under a DID that
configuration declares *and* an active task already references.** A task's endpoints are
resolved once, at activation, and copied into the task — so provisioning over such a DID
would change what the element answers about it while every task activated beforehand kept
delivering to the configured address, leaving an ADMF able to read the new destination back
from an element still sending a live warrant's product to the old one. The refusal carries
6000 and names the DID. Creating under a configured DID nothing references still succeeds,
which is how an operator's static declaration gets superseded before use.

##### The content path

The three sources above govern X3 as they govern X2, but the element that resolves them
is not the element that delivers. A UPF is a *triggered* point of interception and holds
no MDF3 of its own: the SMF's CC Triggering Function resolves the task's X3 destinations,
provisions each one at that UPF with `CreateDestination`, and names them on the LI_T3
trigger. Source 3 for X3 is therefore the SMF's `mdf3`, not anything the UPF holds.

Ignoring a task's X3 destinations here would cost more than on the IRI path it mirrors:
two agencies' warrants would send both agencies' **content** — the subscriber's own
traffic, not metadata — to whichever address configuration names. To serve a task's X3
product from a fixed endpoint, declare its DID in `destinations` pointing at that endpoint,
or have the task name no X3 destination so the SMF's `mdf3` serves it.

**The X1 destination address carries its port as the `TCPPort`/`UDPPort` child element the
TS 103 280 schema defines.** A peer sending the port as the element's text instead is read
as port `0`, and its destination is refused. Each arm is held to its own type: a `TCPPort`
must be at least 2 (the type is `minExclusive 1`), and a `UDPPort` of 0 — schema-valid, but no
endpoint — is refused with **6000**. That fails safe and loudly: the CC Triggering
Function's `CreateDestination` is refused, the trigger is never installed, and the LIPF
receives a `ReportTaskIssue` with a terminating fault rather than an interception quietly
producing nothing.

#### A deactivation is not always a success

`DeactivateTask` for an XID this element does not hold is refused with **2020**, and one
carrying an `xId` that is not a UUID with **1010**. Identifiers are read in the schema's value
space: `xs:token` collapses surrounding whitespace, so a padded `xId` names the same task. TS 103 221-1 table 6.2.3-2 requires the
first outright — "it is an error if the XID is not already present at the NE" — the mirror
of the `CreateDestination` rule answered with 2030.

An ADMF that re-sends a deactivation for tasking this element no longer holds — after a
keepalive purge, or after a restart, since tasking is deliberately not persisted —
receives 2020.

That is the point rather than a side effect. Acknowledging every deactivation would tell the
ADMF a warrant had been withdrawn whether or not anything was, so a mistyped XID would leave
the interception running and report success; interception outliving its authority is the
one direction this plane must never fail in. 2020 is also the only way the ADMF can learn
the element was not holding what it thought it was withdrawing, which is exactly what it
needs to know after a restart.

An ADMF that treats "already gone" as success can map 2020 onto that itself. It could not
recover the information an unconditional acknowledgement would discard.

#### When a withdrawal cannot be delivered

The SMF's CC Triggering Function withdraws a UPF's content trigger by sending it a
`DeactivateTask`. That message can fail — the UPF may be restarting, its X1 endpoint may be
unreachable, the name in `x1Url` may not resolve — and what happens next is the difference
between an interception that ends and one that does not.

**The trigger stays in the SMF's own bookkeeping until the UPF acknowledges.** It is not
released at the moment of the attempt, so an unanswered withdrawal is retried: after 5
seconds, then 10, 20, 40, and every 60 seconds after that, for as long as the process lives.
Retrying does not stop while the SMF still believes the trigger is installed, because a retry
that gives up is the same failure arriving later.

This matters most in the case it was built for. When a session is *released*, the trigger's
detection criterion is that session's identity and can no longer match a packet, so a UPF
that keeps the trigger produces nothing from it. When a **warrant is withdrawn while its
sessions are still live**, the criterion still matches every packet the subject sends: a UPF
that keeps that trigger keeps duplicating the subject's traffic to the mediation function,
correctly labelled, under a warrant that no longer authorises it. Nothing downstream reveals
that — the product is well-formed and attributable, and only the element that failed to
withdraw the trigger is in a position to say so.

**What an operator sees.** Two conditions arrive on the X1 fault channel, and they are
deliberately different:

| Condition | When | What it means |
|---|---|---|
| `taskingWithdrawalFailed` | The first failed attempt | A UPF did not acknowledge a withdrawal. The SMF is retrying and still holds the trigger; nothing is lost yet |
| `taskingWithdrawalStuck` | Once an attempt has been outstanding for 5 minutes | Authority was removed five minutes ago and content is probably still being intercepted. This is the one to act on |

Both name the element and nothing else — no warrant, no target. Which interception it was is
not something this channel carries; the LIPF knows what it withdrew.

**What the fail-safe does and does not reclaim.** A triggered UPF purges all of its tasking
if its triggering function stops sending keepalives, which is what stops interception
outliving an SMF that has died. So the SMF keeps a UPF alive only while it believes that UPF
holds tasking it installed — never merely because the UPF is configured. Three consequences
follow, and the third is a limit rather than a feature:

- An SMF holding no content tasking at all sends no keepalives, and each UPF purges after its
  own window. That is correct: there is nothing to keep, and it means a UPF's interception
  state converges to empty whenever the SMF's does.
- A restarted SMF sends a UPF nothing until it has established what that UPF holds. Until
  then it could not name that tasking and so could never withdraw it, and staying silent lets
  the UPF's fail-safe reclaim it. If the UPF's X1 stays unreachable, interception lapses —
  which is the direction this has to fail in.
- **The fail-safe cannot reclaim one orphaned trigger from a UPF that also holds valid
  tasking.** It operates on the whole relationship between a triggering function and a UPF,
  so it purges everything or nothing, and the valid tasking's keepalives preserve the orphan
  alongside it. Durable withdrawal — the retry above — is the remedy for a single failed
  withdrawal; the keepalive behaviour only ensures the backstop is reachable at all.

**A purge is reported only when nobody asked for it.** An element raises `taskingPurged`
(Alert 10000, clause 6.6.2) when its controlling function — the ADMF, or for a UPF its
triggering function — has gone quiet and the keepalive procedure has deactivated its tasking. An ordinary `DeactivateTask`,
a `ModifyTask` and a bulk deactivation all tear the interception down exactly as thoroughly
and raise nothing: the `DeactivateTaskResponse` is the acknowledgement that they happened.
An element that reported every withdrawal as a fail-safe purge would teach its operator to
ignore the one channel that says a controlling function has stopped answering.

#### Fields accepted and disregarded

Two `TaskDetails` fields are accepted and then ignored. That is a different claim from
"we ignore this field", and the difference is why it is written down: the specification
addresses each of them to something this element is not.

**`listOfMediationDetails`** — TS 103 221-1 defines it as

> Set of details for use by an NE that is performing mediation (i.e. a mediation and
> delivery function). This shall be included between the ADMF and the MDF.

The AMF, SMF and UPF host POIs, not an MDF, so the details are not addressed to them.
Disregarding them is conformant; refusing them would refuse a legal task. For an MDF2
implementation, every field inside the structure — including the `StartTime` and
`EndTime` of the authorisation — becomes mandatory work.

**`implicitDeactivationAllowed`** —

> Indication that a Task may implicitly deactivate itself once the NE has determined that
> it has completed.

These elements never conclude that a task has completed, so they never self-deactivate and
the permission is unused either way. An ADMF that sets it will not receive the
`ReportTaskIssue` the field implies: a missing feature, not a divergence in what is
intercepted. It has nothing to do with the keepalive purge, which stops interception
because contact with the authorising function was lost.

**The one extension that is acted on** is `TaskDetailsExtensions/IdentifierAssociationExtensions`
with owner `3GPP`, which TS 33.128 table 6.2.2.1-1 makes conditional on the AMF IRI-POI's
task. It decides which records that task produces (clause 6.2.2.2.1):

| `IdentifierAssociationEventsGenerated` | Records produced |
|---|---|
| *(extension absent)* | Everything **except** `AMFIdentifierAssociation` and `AMFIdentifierDeassociation`, which "shall not be generated" |
| `IdentifierAssociation` | **Only** `AMFIdentifierAssociation`, `AMFIdentifierDeassociation` and `AMFLocationUpdate` |
| `All` | Every AMF record type |

An ADMF that wants the identifier-association pair asks for it with this extension.

#### LI_T3 detection criteria

TS 33.128 clause 6.2.3 requires a CC-POI to support *at least* the identifier
types in table 6.2.3-7. What this implementation does with each:

| Identifier type | ETSI TS 103 221-1 element | Supported | Resolved against |
|---|---|---|---|
| GTP Tunnel ID | `gtpuTunnelId`, or the extension's `FTEID` | **Yes** | The uplink PDR's `tunnelTEID`, and its `tunnelIP4Dst` where the criterion names an address |
| UE IP Address (IPv4) | `ipv4Address` | **Yes** | The PDR's `ueAddress` |
| UE IP Address (IPv6) | `ipv6Address` | No | Nothing to resolve against: SD-Core has no IPv6 PDU sessions |
| UE TCP/UDP Port | `tcpPort` / `udpPort` | **Yes** | The PDR's SDF filter, or the packet where the filter does not constrain the port |
| PFCP Session ID | `TargetIdentifierExtension/FSEID` | **Yes** | The session's own SEID |
| PDR ID | `TargetIdentifierExtension/PDRID` | **No, refused** | A PDR ID is allocated per PFCP session and reused across sessions from a low number, so it names no subject's traffic. See below |
| QER ID | `TargetIdentifierExtension/QERID` | **No, refused** | Likewise per session and reused. See below |
| Network Instance | `TargetIdentifierExtension/NetworkInstance` | **Yes** | The PDI's Network Instance, so every session on that DNN |
| GTP Tunnel Direction | `TargetIdentifierExtension/GTPTunnelDirection` | **Yes** | The PDR's source interface, and the datapath's tag on each copy |
| PDR | `TargetIdentifierExtension/PDR` | **Yes**, except where the rule leaves a field for this element to assign | The encoded Create PDR IE, parsed with the agent's own PFCP parser and compared against a session's rules in that form. A rule asking the UPF to allocate the UE address or choose the tunnel endpoint (`CH`) is refused: it names a value this element assigns, so it can never compare equal |

A task's criteria are a list, and they are **conjunctive**: traffic is intercepted only
where every one of them matches (TS 103 221-1 table 6.2.1.2-1), so a list narrows — a
session's F-SEID with a tunnel direction is that session's traffic in one direction. A
combination no packet satisfies (two sessions, two directions, two ports, a UE IPv4 with a UE
IPv6 address) is refused at
activation, as the same table requires, by these packages for every element. Clause 5.1.1
says "A Task relates to a single target identifier"; the element follows table 6.2.1.2-1,
which defines the field as a list of criteria that must all match.

What is left is the two rule identifiers and the IPv6 form of UE IP Address.

**PDR ID and QER ID are refused, and this is a declared gap against table 6.2.3-7
rather than a silent one.** Both identifiers are allocated *per PFCP session* and reused
across sessions from a low number, and a CC-POI matches a criterion against every
session it holds. So a task naming PDR 2 would duplicate, attribute and deliver the
traffic of every subscriber whose session happens to hold a PDR of that number — under
that warrant, indistinguishable downstream from the traffic it did name. That is
interception of subjects the warrant does not name, which is the one failure this plane
may never have.

Qualification does not make them worth supporting. Beside a PFCP Session ID, with all
criteria required to match, a rule ID only narrows a selection the Session ID already makes
to one session; on its own, it names every session holding a rule of that number, and
nothing in a duplicated copy distinguishes the intended session's rule from any other's.
So the element refuses the criterion at activation, with a reason, rather than accepting a
form whose only new reach is over-collection.

A triggering function needing to intercept one session's traffic has the PFCP Session ID,
which is exactly that and is supported.

The IPv6 gap is **not an interception limitation**: SD-Core has no IPv6 PDU
sessions to intercept. The SMF cannot allocate an IPv6 UE address — its allocator
computes the pool size over 32 bits, and the NAS session-accept it would build
leaves the PDU address zero-length — the WebConsole provisions subscribers with
`IPv4` as the only allowed session type, and the UPF rejects a PFCP session whose UE
address is not four octets. An IPv6 criterion could therefore never select traffic,
whatever the CC-POI did with it. It becomes supportable when the core does, not
before.

It is **refused**, not ignored: a criterion this element
cannot evaluate would intercept either nothing — mandated interception silently
producing no product — or everything, which is collection beyond the
authorisation. Both are worse than a refusal the triggering function can report to
the LIPF. The refusal arrives *before* the task is acknowledged, so nothing is left
in place appearing to intercept.

Two readings are worth stating because the schema does not.

**A `PDR` criterion is a Create PDR IE**, and it is compared in the parsed form this
agent holds its own rules in, not octet-for-octet. PFCP puts no ordering on the IEs
inside a grouped IE, so two encoders can describe one rule in different bytes and an
octet comparison would miss the match; parsing both sides with the same parser makes
that parser the canonical form. Two consequences follow: fields the agent does not
retain do not take part, so rules differing only in an IE it ignores compare equal;
and the fields a *session* assigns — which session the rule belongs to, the address
that sent it, the counter this UPF chose — are excluded, since they are not
properties of the rule the triggering function described. An Update PDR is refused
even though it parses: one agreed form, so a triggering function cannot say the same
thing two ways and get different answers.

**`GTPTunnelDirection` is
read relative to the UPF.** `Inbound` is the tunnel it receives on, so uplink;
`Outbound` is the tunnel it sends on, so downlink. The enumeration carries no
definition of the vantage point, and taking it the other way round would intercept
the opposite direction to the one authorised.

#### The coverage model

Duplication is an apply-action on a **FAR**, and PDRs reference a FAR, so
duplication covers *the traffic of every PDR pointing at that FAR* — not a criterion,
and not necessarily a whole session. Two consequences an operator should be able to
predict:

- **Coverage is enabled per FAR.** Where a criterion selects some but not all of the
  PDRs sharing a FAR, enabling duplication copies more traffic than the criterion
  identifies. FARs are never split or cloned to avoid this: that would mutate the
  subscriber's own forwarding, which is a surface where two target-visible defects
  have already occurred.
- **The excess is dropped before delivery.** A copy is delivered only if it matches
  a criterion, decided from the datapath's tag and the session's rules where
  possible — a criterion selecting one direction is settled by the tag alone — and
  from the packet only for a transport port the rules do not constrain. A copy that
  did not match is not lost content and is not reported as a delivery fault.

**Duplication is re-derived, not remembered.** Every point that could change the
answer — a task activated, modified or withdrawn, a session established, a session
modified — recomputes what the live tasking implies for the session's current rules,
rather than replaying a set of decisions taken earlier. A remembered set is one more
thing to drift out of step with the datapath, and its drifting would be silent.

One consequence is visible in the logs: **a re-derivation may run twice around a
session establishment or modification.** A session becomes visible to a re-derivation
only once the PFCP handler has stored it, which is after the datapath has been
programmed. So a re-derivation already running when a session is programmed cannot
have accounted for it, and the element asks for one more once the session is stored.
That second pass is not churn and not a retry of a failed one; it is the element
confirming that what it programmed for a session it has just installed still matches
tasking that may have changed while it was installing it. It is asked for only where
the session is being duplicated or has just stopped being, so an element holding no
tasking never performs it.

So exactness depends on the rule structure the SMF installed, and precision is
restored by filtering rather than by finer duplication. The residual limitation:
where a FAR is shared by several PDRs *of the same direction* and the criterion
selects only some — distinguishable only by their SDF filters — the copies cannot be
separated by tag or direction, and a criterion without a transport-port test will
over-collect within that FAR. Duplication granularity is bounded by the PDR/FAR
structure that exists; this does not provide per-flow duplication.

#### Bulk deactivation is enabled by default

`DeactivateAllTasks` stops **every** interception on a network element, and it is enabled
unless you disable it. That is the standard's default, stated outright: "By default (if
there has been no agreement in advance) then DeactivateAllTasks is enabled." So on an
element with no LI-specific option set, one authenticated X1 message removes every warrant
it holds and tears down what each warrant was doing — a CC-POI's duplication included, not
merely the task list.

Two things follow for anyone who can reach an X1 endpoint:

- **The blast radius is the whole element**, not one warrant. Interception stops; it does
  not restart by itself, and the ADMF is not told separately, because the deactivation
  *is* its own request.
- **Reaching the endpoint is the control.** It is gated by clause 8.2.4 peer
  authentication — an LI-CA certificate whose bound identity is this element's responsible
  ADMF — and by whatever network controls stand in front of the port. See
  *[Restricting who can reach X1](#restricting-who-can-reach-x1)*, which matters more
  because this message is answered.

**`RemoveAllDestinations` is enabled unless disabled, too.** TS 103 221-1 clause 6.3.4 makes
it enabled when there has been no agreement in advance, as it does `DeactivateAllTasks`, and
guards it: an NE refuses the request while any destination is still referenced by a task
(8010), so it cannot leave a tasked element with nowhere to deliver. It is the only X1 path by
which a destination provisioned wrongly can be removed, since `ModifyDestination` and
`RemoveDestination` are not implemented. Disabled, it is answered with the specification's
own error (8020).

##### Setting them

Both are **deployment configuration**, because TS 103 221-1 makes them a matter of prior
agreement — "It should be agreed in advance as to whether the DeactivateAllTasks request is
enabled or disabled" (clause 6.2.4) and "It shall be agreed in advance as to whether the
RemoveAllDestinations request is enabled or disabled" (clause 6.3.4) — and an agreement
differs per deployment. Honouring one is not
supposed to need a rebuilt image.

| Element | Where | Keys |
|---|---|---|
| AMF, SMF | `li:` in `amfcfg.yaml` / `smfcfg.yaml` (chart: `config.<nf>.li`) | `deactivateAllTasks`, `removeAllDestinations` |
| UPF | `li` in `upf.jsonc` | `deactivate_all_tasks`, `remove_all_destinations` |

Each is a **tri-state**, and leaving it out is a meaningful answer rather than an omission:

| Value | Meaning |
|---|---|
| unset | "no agreement in advance", the specification's phrase. Both operations are performed, the specification's default |
| `false` | the operation is refused, with the error the specification defines for it (5010 / 8020) |
| `true` | the operation is performed |

A value that will not parse as a boolean is a configuration error and stops the element
from starting; it is never read as unset, because unset is the permissive answer for both
bulk operations and a typo must not widen what an element will do.

Setting `deactivateAllTasks: false` is **visible to your ADMF**: it starts receiving error
5010 rather than an acknowledgement. That is the point of the switch, but it changes what a
peer sees and is worth agreeing before it is deployed.

`RequireResolvableDIDs` is deliberately *not* configuration, though it looks like a switch
of the same kind. It follows from the role an element performs rather than from an
agreement: a triggered CC-POI must refuse a content task whose destination it cannot
resolve, because that refusal is the only way its triggering function learns the
destination was lost — and an IRI-POI must not, because an ADMF legitimately names DIDs it
never provisioned. A deployment able to set it could break the LI_T3 contract from a values
file, so it is not offered.

##### What to set

**On the UPF, disabling both is recommended.** The UPF's X1 peer is not an agency's ADMF
but the deployment's own SMF, acting as CC Triggering Function — and the triggering side
of this library (`x1.Requester`) implements `CreateDestination`, `ActivateTask`,
`ModifyTask`, `DeactivateTask`, `TaskXIDs`, `ReportedTasks` and `Keepalive`, and **no bulk message at all**.
Nothing in the AMF, SMF or UPF sends one. So on a UPF both operations are reachable only by
a peer presenting a certificate bound to the SMF's identity, they are used by nothing, and
turning them off cannot break content interception:

```jsonc
"li": {
  // …
  "deactivate_all_tasks": false,
  "remove_all_destinations": false
}
```

**On the AMF and SMF neither direction is recommended.** Their X1 peer is the agency's ADMF,
and the setting follows the agreement between you and that agency. Both defaults are the
specification's.

The defaults are not changed per element to match the UPF recommendation: an element would
then behave differently from its peers with no configuration present.

#### What happens after a restart, and what the ADMF does about it

Tasking lives in memory. A restarted AMF or SMF therefore holds **no** warrants — see
*Warrants persisted across a restart* above for why that is deliberate — and the whole
recovery path is built out of the messages in this section. In order:

1. **The element says so, unprompted.** On startup with LI enabled and no tasking, it
   sends `ReportNEIssue` with `taskingAbsent`. Nothing else would tell the ADMF: from
   outside, an element holding no warrants is indistinguishable from one holding warrants
   that match nobody.
2. **It withdraws what its previous life left elsewhere.** A restarted SMF removes tasking
   it had triggered at a CC-POI, which no other party could ever remove — the UPF would
   otherwise keep duplicating for a warrant nothing holds. A UPF that is not up yet is
   retried rather than abandoned, and until it answers the SMF sends it no keepalives, so
   tasking it still holds lapses under its own fail-safe instead of being preserved by an
   SMF that cannot name it.
3. **The ADMF interrogates to find out where it stands.** `GetNEStatus` for the element's
   own state, `ListAllDetails` for the identifiers it holds, `GetAllTaskDetails` or
   `GetAllDetails` for the detail. An element holding nothing answers all of them
   *successfully*, with empty lists — the specification is explicit that empty is not an
   error, and that is precisely the state a restarted element is in.
4. **The ADMF re-provisions.** A restarted element holds nothing, so every `ActivateTask`
   is a fresh activation. An `ActivateTask` repeating a task the element already holds is
   acknowledged and changes nothing, so a re-provisioning that overlaps with surviving
   tasking is safe; one naming a held XID with a different task is refused with **2010** —
   changes go through `ModifyTask`.

What the status answer will *not* say is that this element restarted. `GetNEStatus`
reports the fault conditions that hold at the moment it is asked, computed from what the
element can observe rather than from a history — so nothing needs clearing and no answer
can go stale. Lost tasking is an event, and events travel by the push in step 1. The two
mechanisms answer different questions, "what just went wrong" and "what is wrong now", and
an operator should expect a restarted, re-provisioned element to answer `OK` — see *Asking an
element how it is* below for what it answers when something actually is wrong.

The UPF is the exception to all of this: its triggering function is the SMF, which is still
running, so it is re-provisioned and re-triggered with no operator action. A UPF restart is
invisible to the ADMF.

#### Asking an element how it is

`GetNEStatus` answers `OK` or `Faults`, and `Faults` lists the conditions that hold at the
moment the question is asked. A healthy element answers:

```xml
<ns1:neStatusDetails>
  <ns1:neStatus>OK</ns1:neStatus>
  <ns1:listOfFaults/>
</ns1:neStatusDetails>
```

and one that cannot currently deliver answers:

```xml
<ns1:neStatusDetails>
  <ns1:neStatus>Faults</ns1:neStatus>
  <ns1:listOfFaults>
    <ns1:unresolvedFault>
      <ns1:errorCode>1000</ns1:errorCode>
      <ns1:errorDescription>mdfUnreachable: 1 of 2 delivery destination(s) unreachable</ns1:errorDescription>
    </ns1:unresolvedFault>
  </ns1:listOfFaults>
</ns1:neStatusDetails>
```

Two conditions can appear, each answered by the part of the element that can see it:

| Condition | Means | Observed by |
|---|---|---|
| `mdfUnreachable` | one or more of this element's mediation functions failed on the last *exchange* and has not since succeeded — a delivery that failed, a keepalive left unacknowledged for TIME_P2, or a refusal at the TLS layer. A delivery that succeeds afterwards clears it, except after a refusal, which only a valid keepalive acknowledgement clears. An acknowledgement counts only where it carries a sequence number this connection issued: that number is the only evidence it answers *this element* rather than being traffic, so one without it is counted as a mismatch and does not postpone the deadline, while still not being read as a protocol error | the X2/X3 delivery clients — every POI has them |
| `x3EgressDown` | the datapath's content egress socket is not connected, so duplicated packets are discarded before this element ever sees them | the UPF's content shipper, the only party that can see its own egress |

The description says how much is wrong — "1 of 2" — and never which destination, nor any
target or warrant: an element's own status is NE-level, and TS 103 221-1 keeps it separate
from the faults reported per XID and per DID. The error code is the registry's generic 1000,
because its codes name failures of a *request* and none of them names a condition of the
element; what an ADMF acts on here is the condition.

Nothing clears a fault. The answer is computed per request from what the element can observe,
so a condition that stops holding stops being reported and one that starts holding appears in
the next answer, with no message having been exchanged in between. There is one consequence
worth knowing before reading an `OK`: reachability is what the last delivery attempt
established, never a dial made to answer the question, so an element that has had nothing to
send answers `OK` because it has not looked. An element that is producing product answers
from real attempts.

**Why a pushed report can name something the status answer does not.** The two mechanisms
carry different kinds of condition:

- a **state** can be re-observed, so it belongs in the status answer — the mediation function
  is reachable or it is not.
- an **event** happened once and cannot be observed again — a copy dropped at the egress,
  a provisioning attempt refused, tasking purged by the fail-safe — so it is reported when it
  happens over `ReportNEIssue`, and never accumulated into the status answer.

So an ADMF that receives `x3PuntLost` and then asks the element for its status is told `OK`,
and both answers are true: copies were lost a moment ago, and nothing is wrong now.
Accumulating events into the answer would need either an expiry, which discards real faults on
a timer nobody can justify, or none at all, which leaves an element faulty forever after one
bad second — and either way the field stops being read. The full classification, every
condition and which mechanism carries it, is in `x1/report.go` beside the constants, which is
where somebody adding one will be looking.

## Which xIRI records this produces

Every record type TS 33.128 defines for an AMF or SMF IRI-POI is listed here — produced,
or out of scope with the reason. The set is the `AMF*` and `SMF*` alternatives of
`XIRIEvent` in `TS33128Payloads.asn`: **14 for the AMF, 11 for the SMF**. Sixteen are
produced.

This is a list rather than a count because a count reads as coverage: "11 record types"
can be accurate of an implementation that emits none of the records the specification
mandates for procedures these functions perform.

### AMF (11 of 14)

| Record | Status |
|---|---|
| `AMFRegistration` | Produced |
| `AMFDeregistration` | Produced |
| `AMFLocationUpdate` | Produced — on a mobility registration update. The `Location` subtree is the minimal `currentLocation` form; cell and TAI detail is deferred |
| `AMFStartOfInterceptionWithRegisteredUE` | Produced |
| `AMFUnsuccessfulProcedure` | Produced — on a registration reject |
| `AMFIdentifierAssociation` | Produced |
| `AMFIdentifierDeassociation` | Produced |
| `AMFUEServiceAccept` | Produced. TS 33.128 also names a SERVICE ACCEPT answering a CONTROL PLANE SERVICE REQUEST; this AMF does not implement that message, so only the plain event occurs |
| `AMFUEPolicyTransfer` | Produced — the policy is copied, never parsed — **except for the shortest `MANAGE UE POLICY COMMAND`s.** TS 33.128 clause 6.2.2.2.12 triggers the record on the UE's `MANAGE UE POLICY COMPLETE` and makes its `uEPolicy` the COMMAND that COMPLETE confirms. `UEPolicy ::= OCTET STRING (SIZE(16..65540))` fits every COMMAND carrying the optional UE policy network classmark (only the lower bound is exact: TS 24.501 caps a COMMAND at 65535 octets). Without the classmark or a VPS URSP configuration, exactly one shape falls below 16 octets — one PLMN and one instruction deleting one policy section, 13 octets, an ordinary PCF operation — which the schema forbids while the prose mandates the record. Such a record is refused by the encoder and reported as a task-scoped `recordNotEncoded` against the warrant, rather than delivered to a mediation function that would discard it. See `iri/CONFORMANCE.md` |
| `AMFRANHandoverCommand` | Produced — **not exercised end to end**, see *What has not been observed on a live network* below |
| `AMFRANHandoverRequest` | Produced — **not exercised end to end**, as above. Despite the name, clause 6.2.2.2.9.3 triggers it on the AMF *receiving* the HANDOVER REQUEST ACKNOWLEDGE |
| `AMFRANTraceReport` | **Out of scope — the AMF is never a trace collection NE.** It does handle CELL TRAFFIC TRACE, one of the record's four trigger events, so this is not "trace is ignored". What cannot be populated is the mandatory `aMFTraceData`: the TS 32.423 XML an AMF sends to a trace collection entity when it *is* that entity. This AMF originates no trace session and delivers no trace data, so there is no value for the field, and a record with an empty one would encode cleanly and assert something false. In scope the day trace activation is wired up |
| `AMFUEConfigurationUpdate` | **Out of scope — the specification contradicts itself.** `gUTI [2]` is typed `GUTI`, which in the module is the EPS shape (`mMEGroupID`, `mMECode`, `mTMSI`), while the field's own prose calls it the "Current 5G-GUTI". The two differ in member count and member type, so no record satisfies both readings. Checked upstream rather than assumed: unchanged in Forge `main` for r18, and still `gUTI [2] GUTI` in **r19 version7**, whose changelog runs to V19.7.0 with no entry touching `GUTI` or `FiveGGUTI` — while r19's own `AMFRegistration` continues to use `FiveGGUTI`. Populating the EPS shape would mean deriving an MME identity this AMF does not have; emitting a `FiveGGUTI` would fail conformance against the published module. Revisit if a later release changes the type |
| `AMFPositioningInfoTransfer` | **Out of scope — this AMF has no LMF.** All four trigger events in clause 6.2.2.2.8 are exchanges with an LMF (`N1N2MessageTransfer` from it, `N2InfoNotify` and `N1MessageNotify` to it), under the overarching condition that a message is "exchanged between the LMF and NG-RAN via the AMF". This AMF decodes an uplink NRPPa PDU, stores the routing id and drops it — there is no LMF integration, so no exchange. The mandatory `lcsCorrelationId` confirms it independently: it is the TS 29.572 correlation id from those same LMF messages, not the NGAP routing id. In scope the day an LMF is deployed. `li/iri` defines the record already |

### SMF (5 of 11)

| Record | Status |
|---|---|
| `SMFPDUSessionEstablishment` | Produced |
| `SMFPDUSessionModification` | Produced |
| `SMFPDUSessionRelease` | Produced |
| `SMFStartOfInterceptionWithEstablishedPDUSession` | Produced |
| `SMFUnsuccessfulProcedure` | Produced — on all nineteen paths that refuse a procedure: sixteen establishment rejects and three release rejects. Modification reject, modification-command reject and 5GSM STATUS are the specification's other triggers and do not occur in this SMF |
| `SMFMAPDUSessionEstablishment` | **Out of scope — no multi-access sessions.** The SMF implements no ATSSS, so the procedure cannot occur |
| `SMFMAPDUSessionModification` | Out of scope, as above |
| `SMFMAPDUSessionRelease` | Out of scope, as above |
| `SMFStartOfInterceptionWithEstablishedMAPDUSession` | Out of scope, as above |
| `SMFMAUnsuccessfulProcedure` | Out of scope, as above |
| `SMFPDUtoMAPDUSessionModification` | Out of scope, as above |

### Conditional identity fields these records carry

Every record above carries the conditional identities its emitter holds. The table names
them because their absence would be invisible from outside: **a record that omits a
conditional field is perfectly well formed**, so no decoder, no interoperability test and no
mediation function could detect the omission.

| Field | Where it appears |
|---|---|
| `sUCI` | the 8 AMF records the AMF emits that define it. Present only when the UE registered *by* SUCI; a registration by 5G-GUTI reports it absent, which is correct and is the common case |
| `pEI`, `gPSI` | `AMFIdentifierDeassociation`, so it names as many identifiers for a subject as every other record about that subject |
| `fiveGSTAIList` | `AMFRegistration`, `AMFIdentifierAssociation`, `AMFStartOfInterceptionWithRegisteredUE`. The **registration area**, per the tables, not the serving TAI |
| `rATType` | `AMFRegistration` and the 4 SMF session records. The NR satellite arms are mapped, for deployments serving non-terrestrial access |
| `servingNetwork` | `SMFPDUSessionEstablishment`, `SMFPDUSessionModification`, `SMFStartOfInterceptionWithEstablishedPDUSession` |
| `aMFID` | `SMFPDUSessionEstablishment`, `SMFStartOfInterceptionWithEstablishedPDUSession`, `SMFUnsuccessfulProcedure`. Decomposed from the GUAMI the AMF sends on N11 — **not** from `ServingNfId`, which is an NF instance UUID and carries no AMF identifier |
| `uEEndpoint` | `SMFPDUSessionModification`, as in the other session records |
| `sUPIUnauthenticated` | the 4 SMF records that define it. `false` where the SUPI was authenticated, `true` where it was not, absent where the record carries no SUPI |

The last of those is a pointer in `li/iri`, present exactly when it is non-nil: an optional
field omitted whenever it equals its type's zero could encode `true` and never `false`, and
`false` is the ordinary value here.

`AMFPositioningInfoTransfer` models `sUCI` and populates it from nowhere, because no point of
interception emits that record at all. See the out-of-scope note above.

### What has not been observed on a live network

**The two handover records have never been produced by a real handover.**

Everything else in the tables above has been exercised end to end in an SD-Core deployment,
outside this repository: a record is generated, delivered over X2, and decoded at the
receiving end against the published ASN.1 module. `AMFRANHandoverCommand` and
`AMFRANHandoverRequest` have not, because an N2 handover needs two RAN nodes and that
deployment had one.

What *is* established for them:

- both encode with every mandatory member populated, and pass the same tag, optionality
  and bounds audits against `TS33128Payloads.asn` as the other records;
- unit tests cover each cause group, the carried source-to-target container, an
  incomplete handover emitting nothing rather than a partial record, and silence for an
  untasked subscriber;
- the carried state is released on every handover outcome — success, failure, cancel, and
  the preparation-failure exits — so it cannot outlive its handover or be reused by a
  later one;
- the end-to-end tests pass against an AMF carrying these hooks, so they do not disturb
  the paths a single-RAN deployment can exercise.

What is not: nobody has watched a handover complete with the hook present. These are also
the only two hooks this POI has in NGAP handling, which runs for every UE on every
handover, so they carry more risk than the rest.

**If you deploy with two or more RAN nodes, exercise a handover for a tasked target and
confirm both records arrive before relying on them.** Report anything that does not
match the tables above.

### Functions SD-Core does not host

TS 33.128 clause 7 defines IRI-POIs for the UDM, SMSF, MDF and others. No equivalent
audit has been done for those, because SD-Core hosts none of them. Anyone adding one of
those network functions should start by listing its clause-7 records the way this section
lists the AMF's and SMF's — the omission above is deliberate, not surveyed and empty.

## Enabling LI

LI is turned on per network function by adding an `li` configuration block. With
no block present, LI is inactive. Before enabling, you need:

1. **LI PKI credentials**, pre-provisioned out of band (the X0 step): this
   network element's certificate + private key, and the LI CA trust anchor.
   Place them at file paths the NF can read — in Kubernetes, a dedicated,
   access-restricted `Secret` mounted at those paths. Use a PKI **separate**
   from the SBI certificates. **The certificates must carry their owner's X1
   identifier** — see [Certificate requirements](#certificate-requirements)
   below; X1 requests from a certificate that does not are refused. The CA file
   must hold at least one CA certificate (`basicConstraints` CA:TRUE) within its
   validity period — other entries are not trusted — and the element's own
   certificate must be within its validity period too, or LI does not start. The
   files are read once, at startup: rotating them takes effect on restart.
2. An external **ADMF** (to task the NFs over X1) and **MDF2**/**MDF3** (to
   receive xIRI/xCC). Note their addresses.
3. The **LI-enabled NF images** deployed.

The `mdf2` and `mdf3` addresses configured here are **defaults**, for a task that
names no destination the element can resolve. Where a task names its own — which
TS 33.128 requires and which is what an ADMF that calls `CreateDestination` does —
that is where its product goes, on X3 as on X2. See *[Where a task's product
goes](#where-a-tasks-product-goes)* for the three sources and their precedence.

The difference between the two interfaces is only *who* provisions the destination
at the element that delivers. The AMF and SMF resolve their own X2 destinations. The
UPF holds no MDF3 of its own: it is a triggered point of interception,
so the SMF's triggering function resolves the task's X3 destinations, provisions each
one at that UPF with `CreateDestination`, and names them on the trigger — falling
back to its configured `mdf3` only for a task that named nothing resolvable.

### AMF and SMF

Add an `li` block under `configuration` in `amfcfg.yaml` / `smfcfg.yaml`:

```yaml
configuration:
  # ... existing AMF/SMF configuration ...
  li:
    x1Listen: ":8443"                     # address the NF's X1 listener binds (ADMF connects here)
    mdf2: "mdf2.li.example:9000"          # X2 delivery destination (MDF2 host:port)
    neId: "amf-1"                         # this network element's identifier (use "smf-1" on the SMF).
                                          # Required: it is both the identity the ADMF tasks on
                                          # X1 and the Network Function ID every delivered
                                          # record carries, so interception refuses to start
                                          # without it — one setting, so the two cannot disagree.
    cert: "/etc/li/certs/tls.crt"         # X0-pre-provisioned LI certificate
    key: "/etc/li/certs/tls.key"          #   its private key
    caCert: "/etc/li/certs/ca.crt"        #   the LI CA trust anchor
    # --- SMF only: content of communication ---
    # The SMF is the CC triggering function. It tasks the interception point in
    # each UPF over that UPF's X1 endpoint, passing the warrant, the session's
    # correlation identifier and the task's X3 destinations, which it provisions at
    # that UPF first; the UPF holds none of them in its own configuration. A UPF
    # missing from this list will duplicate traffic that nobody can attribute,
    # which the SMF reports to the ADMF as a fault.
    mdf3: "mdf3.li.example:9001"          # X3 delivery DEFAULT (MDF3 host:port), for a task
                                          # that names no X3 destination this element can
                                          # resolve. A task that names one goes there instead.
    upfTriggers:
      - nodeId: "upf"                     # the UPF's N4 node address — IP or DNS name (resolved as on the PFCP path)
        x1Url: "https://upf-1:8443/X1/NE" # its X1 endpoint; the host must be a name its certificate covers
        neId: "upf-1"                     # the identifier its certificate is bound to
    # --- optional: NE→ADMF fault reporting + the clause 6.6.2 keepalive procedure ---
    admfUrl: "https://admf.li.example/X1/NE"  # ADMF X1 endpoint for NE-initiated issue reports
    admfId: "admf-1"                          # responsible ADMF identifier
    keepaliveTimeout: "1h"                    # TIME_P2: no X1 request for this long starts the procedure
    # keepaliveTimeP1: "1m"                   # wait for the ADMF's answer to fault 9050
    # keepaliveTimeP3: "2h"                   # grace after an OK answer before deactivating all tasks
    # keepaliveRetainTasking: false           # true: restart TIME_P2 instead of deactivating
    # keepaliveDisabled: false                # true: no keepalive procedure at all
```

### UPF

Add a top-level `li` block in `upf.jsonc`:

```jsonc
{
  // ... existing UPF configuration ...
  "li": {
    "x3_sockaddr": "/tmp/li_x3",         // must match the datapath's LI_X3_SOCKET_PATH (see below)
    "cert": "/etc/li/certs/tls.crt",     // X0-pre-provisioned LI certificate
    "key": "/etc/li/certs/tls.key",      //   its private key
    "ca_cert": "/etc/li/certs/ca.crt",   //   the LI CA trust anchor
    "ne_id": "upf-1",                    // this NE's identifier
    "x1_listen": ":8443",                // the triggering interface the SMF tasks it over
    "tf_id": "smf-1",                    // the only element permitted to task it
    "admf_url": "https://admf.li.example/X1/NE",  // optional: NE→ADMF fault reporting
    "admf_id": "admf-1"                  // optional
  }
}
```

The UPF has **no MDF3 address of its own**. It is a *triggered* point of
interception: the SMF's triggering function tells it which sessions to intercept,
under which warrant, and where to deliver the content — so `x1_listen` and `tf_id`
are as essential as the credentials. Without them the UPF can duplicate a
subscriber's traffic but cannot learn whose warrant the copies serve, and content
that cannot be attributed to a warrant is discarded by any mediation function that
receives it. The UPF refuses to start if either is missing, rather than running in
that state.

`tf_id` names the *only* element permitted to task it. The identity is checked
against the certificate the peer presents, so a certificate the LI CA issued to
some other element cannot be used to task this UPF.

The UPF also requires the **content-egress datapath** to be active:

- Run BESS with a pipeline that contains the LI tee (`Replicate` → `GenericEncap`
  → the `liX3` `UnixSocketPort`). Both `conf/up4.bess` — the pipeline deployments
  run by default — and `conf/closed_loop.bess` carry it. A pipeline **without** the
  tee drops a tasked subscriber's traffic outright, because the PFCP agent marks it
  with a forwarding action that such a pipeline has no gate for.
- Set the BESS container's `LI_X3_SOCKET_PATH` environment variable to the **same
  path** as `li.x3_sockaddr` (both default to `/tmp/li_x3`).
- The BESS and pfcpiface containers must **share** that socket path (e.g. an
  `emptyDir` volume mounted at its directory in both containers), so the datapath
  can tee copies to the socket the pfcpiface agent reads.

> **Upgrade the BESS pipeline before, or with, the PFCP agent — never after.**
> This applies to **every** UPF, including ones with no `li` block at all, and it
> is the one ordering constraint LI adds to an existing deployment.
>
> The agent writes an extra `fwd_action` value on every FAR it installs, which the
> pipeline's `farLookup::ExactMatch` must declare. BESS rejects a rule whose value
> count does not match the table, so an agent from an LI-capable image running
> against a pipeline from an older one fails **every** FAR insertion — no
> forwarding rules at all, for any subscriber, tasked or not. The two ship as
> separate images (`upf-pfcpiface` and `upf-bess`), so nothing prevents them being
> rolled independently. Pin both to the same release, or roll `upf-bess` first.
>
> The failure is loud (no user-plane forwarding) rather than silent, but it is a
> full data-plane outage and it looks nothing like a lawful-interception problem.

### Deploying with Helm or OnRamp

The blocks above are what LI reads on disk; deployment tooling can set them for you. The
chart and blueprint support described here is not upstream yet: it is proposed alongside
the network-function changes that consume these packages, and until those land the blocks
are written by hand.

- **SD-Core Helm charts** expose one `config.<nf>.li` intent block per NF, gated by
  `config.<nf>.li.enabled`. The chart merges it into that NF's `configuration.li`
  (the UPF's `li`) and derives the pieces that must stay in step — the X1 bind
  address, the externally-reachable X1 `Service` (a NodePort for the ADMF to reach),
  and the LI PKI `Secret` mount — from the same values, so they cannot drift apart.
  With `enabled: false` (the default) the render is byte-identical to a non-LI deploy.
- **Aether OnRamp** can wrap that in an LI blueprint: a single `lawful_intercept:`
  block (ADMF/MDF endpoints and the two X1 NodePorts) sets the chart's
  `config.<nf>.li` values. Its prerequisites are the out-of-band LI PKI `Secret` and
  firewalling the X1 NodePorts to the ADMF (see
  [Restricting who can reach X1](#restricting-who-can-reach-x1)).

### Configuration reference

| AMF/SMF (`configuration.li`) | UPF (`li`) | Meaning | Required |
|---|---|---|---|
| `x1Listen` | — | X1 listener bind address (ADMF → NF) | AMF/SMF: yes |
| `mdf2` | — | xIRI (X2) delivery **default**, `host:port`. Used only for a task that names no destination at all — see *[Where a task's product goes](#where-a-tasks-product-goes)* | AMF/SMF: yes |
| `destinations` | — | Pre-shared DID→endpoint mappings, a list of `{did, deliveryType, address}`. `did` must be a UUID, `deliveryType` one of `X2Only`/`X3Only`/`X2andX3`, `address` a `host:port`. An entry that is not all three is dropped rather than half-applied | AMF/SMF: optional |
| `mdf3` | — | xCC (X3) delivery **default**, `host:port`. The CC triggering function provisions a task's own X3 destinations at each UPF it triggers and names them on the trigger; this serves only a task that names no destination at all — see *[Where a task's product goes](#where-a-tasks-product-goes)* | SMF: with `upfTriggers` |
| `upfTriggers` | — | Per-UPF triggering endpoints (`nodeId`, `x1Url`, `neId`) | SMF: for CC |
| — | `x1_listen` | Bind address of the triggering interface (SMF → UPF) | UPF: yes |
| — | `tf_id` | Identifier of the element permitted to task this UPF | UPF: yes |
| `neId` | `ne_id` | This network element's identifier | yes |
| `cert` / `key` / `caCert` | `cert` / `key` / `ca_cert` | LI PKI credential file paths | yes |
| `admfUrl` | `admf_url` | ADMF X1 endpoint for NE-initiated fault reports | optional |
| `admfId` | `admf_id` | Responsible ADMF identifier — on the AMF/SMF it also restricts who may task the NF (recommended). **Unset, any peer the LI CA certified may act as the ADMF**, including another network element's certificate, since the identity binding carries no role — see *[Restricting who can reach X1](#restricting-who-can-reach-x1)* | optional |
| `keepaliveTimeout` | `keepalive_timeout` | TIME_P2 of the TS 103 221-1 clause 6.6.2 keepalive procedure: how long without an X1 request before fault 9050 is reported (Go duration; default `1h`). Set it longer than the ADMF's keepalive cadence, since the procedure ends in deactivating all tasks. The UPF also accepts the deprecated `trigger_keepalive`; setting both to different values is refused | optional |
| `keepaliveTimeP1` | `keepalive_time_p1` | TIME_P1: how long to wait for the ADMF's answer to fault 9050 before deactivating all tasks (Go duration; default `1m`) | optional |
| `keepaliveTimeP3` | `keepalive_time_p3` | TIME_P3: how long after an OK answer before all tasks are deactivated (Go duration; default `2h`). AMF and SMF only: the UPF refuses it, since over LI_T3 no answer starts TIME_P3 | optional |
| `keepaliveRetainTasking` | `keepalive_retain_tasking` | `true` keeps all tasking when the procedure would deactivate it, restarting TIME_P2 instead; the 9050 fault is then reported again each time TIME_P2 expires while the ADMF stays silent. Boolean; default `false` | optional |
| `keepaliveDisabled` | `keepalive_disabled` | `true` runs no keepalive procedure at all, so tasking outlives a silent ADMF or triggering function. Boolean; default `false`, the specification's default of enabled | optional |
| `deactivateAllTasks` | `deactivate_all_tasks` | Whether this element performs a bulk deactivation of all its tasking. Boolean; **unset means "no agreement in advance"**, which is the specification's default of *enabled*. Recommended `false` on the UPF — see *[Bulk deactivation is enabled by default](#bulk-deactivation-is-enabled-by-default)* | optional |
| `removeAllDestinations` | `remove_all_destinations` | Whether this element performs a bulk removal of all its destinations. Boolean; **unset means "no agreement in advance"**, which is the specification's default of *enabled* (TS 103 221-1 clause 6.3.4) — see *[Bulk deactivation is enabled by default](#bulk-deactivation-is-enabled-by-default)* | optional |
| `x2x3KeepaliveEnabled` | `x2x3_keepalive_enabled` | Whether the TS 103 221-2 clause 6.2.4 keepalive runs on X2/X3 delivery connections. Boolean; unset means enabled. `false` is for a mediation function that never acknowledges a keepalive | optional |
| `x2x3KeepaliveTimeP1` / `x2x3KeepaliveTimeP2` | `x2x3_keepalive_time_p1` / `x2x3_keepalive_time_p2` | The keepalive's TIME_P1 and TIME_P2 (Go durations; defaults `60s` and `180s`). Each must be at least `1s`, the minimum TS 103 221-2 table C.4-1 allows, and TIME_P2 must exceed TIME_P1 | optional |
| — | `x3_sockaddr` | Datapath X3 tee socket (match `LI_X3_SOCKET_PATH`) | UPF: yes |

### What only the deployment can guarantee

Two properties this software depends on, and can neither enforce nor detect. Each is
invisible to every element, and each silently breaks a join at the mediation function —
so both are listed here rather than left to be discovered from records that do not line
up.

**One warrant's tasks must carry the same `productID` across elements.** A warrant's AMF
task and its SMF task join at the mediation function only because the ADMF gave them a
matching product identifier: that value is what each element stamps on the product it
delivers, and it is the only thing tying one element's signalling to another's. Each
element sees only its own task, so neither can notice a mismatch — the records stay
well-formed, separately deliverable, and unjoinable. If the ADMF allocates a distinct XID
per element (which is normal), it must set one shared `productID` on all of them.

**Node clocks must be synchronised.** Record ordering *across* elements rests entirely on
their clocks agreeing: the timestamp on each record is the time the event occurred by the
producing element's clock, and nothing in the streams re-establishes an order. On a
deployment whose nodes drift, an agency reconstructing a session can see a release before
its establishment, with nothing to indicate it. The specifications say as much, but where an
implementer reads and not where an operator does: TS 103 221-1 clause 4.1.2 has the ADMF
and NE "implement time synchronization where possible", and TS 103 221-2 clause 4.2.3 warns
that it "does not assume that clocks on different POIs are synchronized" and that timestamps
"will not in general permit re-ordering". Run NTP or PTP on every node that hosts one of
these functions.

### Certificate requirements

A certificate signed by the LI CA is **not on its own** accepted for X1. ETSI
TS 103 221-1 clause 8.2.4 requires that the identifier a peer asserts in an X1
message also be bound into the certificate it presented, so that possession of
any LI CA certificate cannot be used to act as some other party. Issue each
certificate with **either** of these (either alone is sufficient):

- a **UID** attribute in the Subject (OID `0.9.2342.19200300.100.1.1`) whose
  value is the owner's X1 identifier; or
- a **`subjectAltName` URI** holding the annex G certificate binding URN:

  ```
  urn:etsi:li:103221-1:cert-binding:{role}:{identifier}
  ```

  where `{role}` is `ADMF` or `NE`, and `{identifier}` is that party's X1
  identifier — the ADMF identifier, or the NF's `neId`.

Prefer the binding URN where you have the choice: it pins the party's **role**
as well as its identifier, so a certificate issued to a network element cannot be
used to act as the ADMF. A bare UID carries no role.

With OpenSSL, either form can be added through the extension config:

```ini
# ADMF certificate, UID form           # ...or the binding-URN form:
subjectAltName = URI:urn:etsi:li:103221-1:cert-binding:ADMF:admf-1
# Subject DN with a UID: -subj "/UID=admf-1/CN=admf"
```

Set `admfId` on the AMF/SMF to the ADMF's identifier as well. The binding proves
the peer is who it says it is; `admfId` is what says *that* party may task this
network element. Without it, any correctly-bound certificate from the LI CA —
including one issued for an MDF or another network element — is accepted.

An X1 request that fails these checks is refused with the standard error code
(`1030` identifier/certificate mismatch, `1040` unexpected ADMF, `1060`
unexpected NE), no tasking is applied, and nothing is written to the operator
log. If X1 tasking is silently refused, this is the first thing to check.

> **Do not terminate X1 TLS at a proxy or ingress.** The network function has to
> see the ADMF's client certificate to perform these checks; an intermediary that
> terminates TLS makes them unenforceable and X1 will refuse every request.

The MDF's own server certificate is verified against the LI CA *and* its name is
checked against the configured MDF address. If you address an MDF by IP
rather than hostname, its certificate needs a matching **IP** SAN.

### Restricting who can reach X1

The AMF's and SMF's X1 listeners are exposed for an ADMF outside the cluster to
reach, which in a NodePort deployment means every host that can reach the node
can open a connection to them. Authentication holds — a peer without a bound LI
certificate gets nowhere — but nothing should be relying on that alone, and a
peer that can connect can consume the listener's capacity before authenticating.

**Authentication is not authorisation unless `admfId` is set.** A peer passes
TS 103 221-1 clause 8.2.4 when the identifier it asserts is bound into its own LI
certificate. With no `admfId` configured, any such identifier is accepted as the
ADMF — and every network element in the LI domain holds a certificate binding its
own identifier, so a UPF's or an SMF's certificate is enough to task an AMF, point
its product at an address of its choosing, or deactivate all of its tasking.
Configure `admfId` on every element whose responsible ADMF is known; on the UPF,
`tf_id` plays the same role for the triggering function.

**Do not do this with a NetworkPolicy.** A policy that selects a network
function's pod isolates it for every listed `policyType`, so an Ingress policy
admitting only X1 also denies SBI from the other network functions and NGAP from
the RAN, taking the core down — on the UPF it would sever N4.

Restrict the node port at the node instead, where the rule concerns one port and
cannot silently cut anything else. Note that kube-proxy DNATs NodePort traffic in
`nat/PREROUTING`, so by the time a packet could reach `filter/INPUT` its
destination port is the pod's, not the node port — the rule has to run before
that, in `raw/PREROUTING`:

```sh
ADMF=192.0.2.1            # the LI system's address
NODE=192.0.2.10           # this node's address
X1_PORTS=30843,30844        # the amf-x1 and smf-x1 node ports

sudo iptables -t raw -N LI-X1 2>/dev/null || sudo iptables -t raw -F LI-X1
sudo iptables -t raw -A LI-X1 -s "$ADMF"/32 -j RETURN
sudo iptables -t raw -A LI-X1 -j DROP
sudo iptables -t raw -C PREROUTING -d "$NODE"/32 -p tcp -m multiport --dports "$X1_PORTS" -j LI-X1 2>/dev/null ||
  sudo iptables -t raw -I PREROUTING -d "$NODE"/32 -p tcp -m multiport --dports "$X1_PORTS" -j LI-X1
```

`DROP` rather than `REJECT` deliberately: a refusal confirms something is
listening, and these ports are best left looking closed to everyone who is not
the ADMF.

Two things this does not cover. Traffic originating **on the node itself** goes
through `OUTPUT`, not `PREROUTING`, so a process already running there is not
stopped by this — it is inside the trust boundary and must be treated as such.
And `iptables` rules do not survive a reboot on their own; persist them the way
the rest of that host's firewall is persisted.

---

## What to expect once enabled

- The ADMF connects to each AMF/SMF X1 listener and provisions warrants; each
  network function acknowledges over X1 and begins matching that target.
- For a tasked target, the AMF/SMF deliver xIRI to the MDF2, and — for a CC
  warrant — the SMF triggers duplication and the UPF delivers xCC to the MDF3.
- **Nothing** about a target appears in the network functions' general logs,
  metrics, or alarms. LI-plane faults (unreachable MDF, X1 bind failure, etc.)
  are reported to the ADMF over X1 when `admfUrl`/`admf_url` is configured, not
  to general logs.
- Removing the `li` block (or deactivating a warrant) stops all product for it;
  and the clause 6.6.2 keepalive procedure, enabled by default, deactivates all
  tasking if the ADMF falls silent, unless the element is configured otherwise.
  An element with no `admfUrl`/`admf_url` runs the procedure too: its fault
  report cannot be answered, so it deactivates when TIME_P1 expires, and the
  Alert that follows has nowhere to go.
- **One edge worth knowing when restarting the SMF under load.** A PDU session
  established while an SMF instance is *shutting down* takes its duplication rule
  from a process that is about to exit. The replacement SMF never knew that
  session, so it never withdraws the rule, and the UPF's CC-POI can be left
  duplicating traffic for a warrant no live SMF holds. Content in that state is
  dropped rather than delivered — the CC-POI refuses to ship what it cannot
  attribute — so nothing unauthorised reaches an MDF, but the duplication persists
  for the life of that session unless the UPF's keepalive procedure, enabled by
  default, deactivates it when the triggering function stops calling — and it only
  stops calling while the replacement SMF holds nothing else at that UPF, since the
  procedure deactivates a whole connection's tasking or none of it (see *When a
  withdrawal cannot be delivered*). Leaving the procedure enabled on the UPF is the
  mitigation; draining sessions before replacing the SMF avoids it entirely.

## Testing

Because the ADMF and MDF are external, LI is exercised end to end against a
third-party stack — for example the **sipgate `li-simulator-x1x2x3`** (an
independent ETSI TS 103 221 implementation, useful as an interop reference).
Point the NFs' `mdf2`/`mdf3`/`admfUrl` at the simulator's endpoints (`mdf3` on the SMF,
which passes it to each UPF) and provision a
task over its X1 client.

One gap in that coverage is worth knowing before you rely on it: the two RAN handover
records are not exercised, because a handover needs two RAN nodes. See
*[What has not been observed on a live network](#what-has-not-been-observed-on-a-live-network)*.

## Standards

- ETSI TS 103 221-1 — X1 (task provisioning)
- ETSI TS 103 221-2 — X2/X3 (xIRI/xCC delivery framing), read against **V1.10.1**
- 3GPP TS 33.127 — LI architecture; 3GPP TS 33.128 — stage-3 procedures / xIRI records
- ETSI TS 104 000 — X0 (credential pre-provisioning)

**Three conformance dispositions record what is and is not implemented on each wire format,
and they are the honest answer rather than this table. `CONFORMANCE.md` indexes them and
lists the open gaps across all three in one place — start there.**

- `x1/CONFORMANCE.md` — TS 103 221-1 message by message, against V1.21.1: what is
  implemented, what is refused and with which code, and where a deliberate decision diverges
  from the prose. Also the two dispositions that belong to this element as an X1 *client*, on
  the LI_T3 triggering path.
- `x2x3/CONFORMANCE.md` — every TS 103 221-2 header field, PDU type, conditional attribute
  and payload format, against V1.10.1. Two gaps are recorded there. The UPF CC-POI discards
  a fragmented IPv4 datagram whose first fragment it has not seen, where a transport-port
  criterion applies, and reports the loss to the ADMF. The emitted Version field is 5 where
  V1.10.1 defines 6, which is blocked externally: the only available interoperability peer
  refuses 0.6. The clause 6.2.4 keepalive mechanism
  **is** implemented, on X2 and X3, though its acknowledgement path has never been exercised
  against an independent implementation — the reference declares the PDU types and
  references them nowhere.

  **The six conditional attributes TS 33.128 requires are emitted**, and an integrator can
  expect them without reading the code: the Network Function ID (the element's own `neId`) and
  the Interception Point ID (`AMF-IRI-POI`, `SMF-IRI-POI`, `UPF-CC-POI`) on both interfaces; a
  Timestamp and a Sequence Number on both; and, on X2 only, one Matched Target Identifier per
  identity of the task the subject presents plus one Other Target Identifier per remaining
  identity of that subject. The header is therefore longer than the fixed 40 bytes — which is
  what the header length field is for, and what an MDF has to read it for. The timestamp is the
  event's time on X2 and the xCC's generation time on X3, as TS 33.128 tables 5.3.2-2 and
  5.3.3-2 respectively define them. The sequence number is numbered per
  `(XID, Correlation ID)` context and not per connection, so one record delivered to two of a
  task's destinations carries one number, and product dropped under sustained MDF slowness
  leaves a visible gap rather than being renumbered over.
- `iri/CONFORMANCE.md` — the TS 33.128 records, bounded to those these packages encode, with
  the M/C/O verdict for every field the published ASN.1 module defines and they do not
  populate.

The X2/X3 and IRI dispositions are readings rather than checks, and each says which of its
statements are: ETSI publishes nothing machine-readable for X2/X3 framing, and the M/C/O
markers of the IRI records live in prose tables. X1 also has a schema validator. Two mechanical checks sit beside them: `iri/asn1_drift_test.go` fails when the
module defines a field no record models and nothing declares it absent, and
`x1/schema_drift_test.go` fails when the X1 schema defines an element no struct declares.
Neither can notice that a "shall" in the prose has no code behind it, which is what the
dispositions are for.

The X1 schemas are vendored under `x1/testdata/schemas/` — the ETSI schemas as published on
ETSI Forge (CR branch `cr/103120/088`), the 3GPP extension from the TS 33.128 attachments —
pinned by digest, with their source URLs and versions beside them. Every X1 message this element sends is
validated against them in test, and `x1/testdata/schemas/SOURCES.md` records the exact
statements the provisioning decisions rest on, so a reviewer can check one against the
other without re-downloading anything.

### X1 timestamps are rendered to a fixed six digits, and that is not a formatting choice

Every X1 message carries a `messageTimestamp` typed as TS 103 280's
`QualifiedMicrosecondDateTime`, whose pattern demands **exactly** six fractional digits:

```
[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}(Z|[+-][0-9]{2}:[0-9]{2})
```

Not "up to six". So this package renders them through one helper with the layout
`2006-01-02T15:04:05.000000Z07:00` — zeros, which pad and keep trailing zeros — rather than
`time.RFC3339Nano`, whose nines emit whatever the clock offers and strip trailing zeros.

That is worth stating plainly because it reads as a style choice and is not one. Linux
gives Go nanosecond resolution, so `RFC3339Nano` renders nine digits about 90% of the
time, eight about 9%, seven about 0.9% — and six only when the nanosecond value lands on
an exact microsecond, one time in a thousand. Every X1 message rendered that way on a
Linux deployment would be malformed, and a validating peer is entitled to discard all of
them. On a microsecond-resolution clock — a developer workstation — the same format fails
only the ~10% of instants whose value ends in a zero, which makes it easy to miss in
testing.

The failure would be the worst available shape: silent, because a peer discarding a
malformed message says nothing; in the **fault reports**, so the messages that say something
is wrong would be the ones thrown away; and in the **triggers**, so a strict UPF would refuse
interception with nothing to point at. The helper covers every site: the responses this
element answers with, the LI_T3 triggers a CC-TF sends, and the fault reports by which an
element tells a provisioning function something is wrong.

`x1/schema_validation_test.go` pins it with a clock whose fractional part ends in zeros, and
asserts the boundary values directly, so a return to a stripping format fails every case
rather than one run in ten.

### Deliberate deviations

The places where these packages depart from the letter of TS 103 221-1, each for a stated
reason; `x1/CONFORMANCE.md` records them with the rest of its dispositions.

- **The emitted X1 Version is `v1.6.1`** while the behaviour is built against V1.21.1 — see
  `x1/CONFORMANCE.md`, *Message envelope*. Every message this element encodes carries it,
  responses included: table 6.1-1 defines Version as the version used to encode the message,
  so a response states this element's version rather than echoing the request's. A message
  whose version is not a `v1.x.y` is refused with **1020**.
- **`CorrelationID` is acted on by the triggered CC-POI**, below.
- **An `ActivateTask` repeating a held task exactly is acknowledged**, where table 6.2.1.1-2
  makes it "an error if the XID is already present at the NE". A retried activation — an
  answer lost in transit, or an ADMF re-provisioning after a restart it cannot see — then
  changes nothing and re-emits nothing, instead of failing with 2010 for a task the element
  is already running as asked. A *different* task under a held XID is still refused with
  **2010**; changes go through `ModifyTask`.
- **Warnings and alerts are not logged at the element.** Clause 5.3 says "the NE shall log
  any warnings and alerts for audit reasons". These packages report them to the ADMF over X1
  and keep no local record, because a local log is readable by whoever operates the network
  function, and LI must not be detectable there — see *Undetectability* in the feature table.
  The audit record is the ADMF's.

#### `CorrelationID` on X3

TS 103 221-1 says of `CorrelationID`:

> Intended for use in triggering scenarios, and **shall be ignored by non-mediation
> function NEs**.

The triggered CC-POI in the UPF is not a mediation function, and it does not ignore it: it
stamps the value on every X3 PDU it delivers. That is deliberate. TS 33.128 table 6.2.3-6
makes `CorrelationID` mandatory on the LI_T3 trigger and defines it as the value that lets
an MDF join content to the signalling the IRI-POI reported for the same session, and where
33.128 specialises 221-1 for 5G we have taken 33.128 to govern.

It is recorded as a **question rather than a settled position**: two specifications, one
`shall`, and a reading that could be wrong. If the right answer is that a POI must ignore
the field and obtain correlation another way, we would rather hear it than keep a silent
contradiction. See `x1/testdata/schemas/SOURCES.md` for both quotations in full.
