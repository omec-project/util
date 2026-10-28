// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"text/template"
	"time"
)

// reportThrottle is the minimum interval between reports of the same issue type,
// so a persistent fault (e.g. an unreachable MDF hit on every event) does not
// flood the ADMF.
const reportThrottle = 30 * time.Second

// The LI-plane conditions an element reports at network-element scope. Each is a condition
// name, not a TS 103 221-1 TypeOfNEIssueMessage value: the name leads the report's free-text
// description, where an ADMF can tell one fault from another, and neIssueEncodings maps it
// onto the closed TypeOfNEIssueMessage enumeration and the issue code the schema provides.
// None names a target or a warrant.
//
// Each condition is carried by exactly one of this element's two mechanisms, and which
// one is decided by the condition's nature rather than by convenience.
//
// A *state* can be re-observed, so a fault probe answers it whenever a provisioning
// function asks for the element's status (see WithFaultProbes). An *event* happened once
// and cannot be observed again, so it is pushed when it happens (Notify) and is never
// accumulated into the status answer: accumulating events needs either an expiry, which
// discards real faults on a timer nobody can justify, or none at all, which makes an
// element permanently faulty. Both end with the answer no longer being read.
//
//	condition                  kind   carried by  meaning
//	mdfUnreachable             state  both        the mediation function is reachable or it is not
//	x3EgressDown               state  probe       the datapath egress socket is up or it is not
//	x3PuntLost                 event  push        a copy the datapath could not hand over
//	x3FramingLost              event  push        a copy this element could not frame in time
//	x3DeliveryLost             event  push        a copy the delivery queue had no room for
//	x2DeliveryLost             event  push        a record the delivery queue had no room for
//	contentUntasked            event  push        a copy that arrived under no warrant
//	x3TagInvalid               event  push        a copy that arrived uncorrelatable
//	duplicationRefused         event  push        a rule the datapath refused or did not confirm
//	recordValueSubstituted     event  push        a record field carrying the nearest permitted value
//	triggerFaulty              event  push        a POI reporting a trigger as not running
//	keepalivesMissing          state  push        the ADMF has not been heard from within TIME_P2
//	taskingPurged              event  push        tasking the keepalive procedure removed, once
//	reconcileFailed            event  push        a restart this element could not reconcile
//	taskingWithdrawalFailed    event  push        a withdrawal a POI did not acknowledge
//	taskingWithdrawalStuck     event  push        a withdrawal outstanding long enough to matter
//	x1AuthFailed               event  push        a provisioning attempt that was refused
//	x1ResponseUnattributable   event  push        an answer that could not be bound to its request
//	taskingAbsent              state  push        observable, deliberately not a probe (below)
//	x1ListenFailed             state  push        observable, but unaskable (below)
//	invalidConfig              state  push        observable, but unaskable (below)
//
// mdfUnreachable is the one condition carried by *both*, and it is not an exception to
// the rule above but the rule followed all the way. A state has an ending as well as a
// beginning, and TS 103 221-1 clause 5.3 requires both to be reported — "The NE shall
// also indicate that a fault has been cleared". So the probe answers it when asked, and
// DestinationWatcher pushes each transition, naming the destination the transition
// concerns (clause 6.5.3). The two carry the same fact to different questions: "how much
// is wrong" and "what just changed".
//
// keepalivesMissing has both edges too, pushed by the keepalive procedure of clause 6.6.2:
// the FaultReport when timer P2 expires and the FaultCleared when the ADMF is heard again.
// It is not a probe, because the only party that can ask is the ADMF whose silence it
// reports, and its next request ends the condition.
//
// The last three are re-observable and still do not belong in a status answer, which is
// the part of this that cannot be recovered from the code:
//
//   - taskingAbsent. Holding no tasking is checkable at any moment and is not a fault.
//     Most elements hold none most of the time, so reporting it would make "faulty" the
//     normal state and the field worthless. It is pushed once at startup, where it means
//     something else entirely — this element has *lost* the tasking it held — which is a
//     transition rather than a state.
//   - x1ListenFailed. If the X1 listener did not come up there is nobody to ask, so a
//     probe for it could never be consulted. It travels the reporting path, which is a
//     different socket, and that is the only mechanism that can carry it.
//   - invalidConfig. The same shape: an element that refuses its configuration never
//     reaches the point of answering questions about itself.
//
// Adding a condition means answering this question first. Getting it wrong in the probe
// direction makes the status answer useless: a probe that reasons about delivery it cannot
// see answers "Faults" on every healthy element.
const (
	NEIssueX1ListenFailed = "x1ListenFailed"
	NEIssueX3EgressDown   = "x3EgressDown"
	NEIssueMDFUnreachable = "mdfUnreachable"
	NEIssueInvalidConfig  = "invalidConfig"
	// NEIssueContentUntasked: the datapath delivered content for a session no
	// interception task covers. A triggered POI cannot label such content with a
	// warrant, and a mediation function discards what it cannot attribute, so the
	// content is dropped — but an authorised interception may be silently producing
	// nothing, which only the ADMF can resolve.
	NEIssueContentUntasked = "contentUntasked"
	// The three places content can be lost on its way to the MDF each get their
	// own type, because the ADMF's response to them differs — datapath capacity,
	// this element's CPU, or the mediation function's ingest rate — and because
	// reports are throttled per type, so sharing one would let whichever fired
	// first hide the others.
	//
	// NEIssueX3PuntLost: the datapath could not hand the copy to this element at
	// all; its egress socket was full. Datapath-side capacity.
	NEIssueX3PuntLost = "x3PuntLost"
	// NEIssueX3FramingLost: this element received the copy but could not frame it
	// fast enough. Local CPU.
	NEIssueX3FramingLost = "x3FramingLost"
	// NEIssueX3DeliveryLost: the copy was framed but could not be delivered; the
	// queue toward the MDF was full. The mediation function is reachable but
	// slower than the offered rate.
	NEIssueX3DeliveryLost = "x3DeliveryLost"
	// NEIssueX2DeliveryLost is the same condition on the signalling interface: a
	// record was built and could not be delivered, because the queue toward the MDF2
	// was full.
	//
	// It is its own type rather than sharing x3DeliveryLost for the reason the three
	// content types are separate — reports are throttled per type, so sharing one
	// lets whichever fires first hide the other — and for a sharper one: an element
	// may deliver X2 and X3 to different mediation functions belonging to the same
	// agency, and "which interface is losing product" is the first thing that
	// distinguishes a slow MDF from a slow element.
	//
	// The condition it names is deliberately not answerable by a reachability probe:
	// a full queue at one instant is a burst the buffer exists to absorb, not a fault
	// (see AsyncSender.Unreachable). So it is reported as the drops occur or it is
	// not reported at all, which is why the pool's drop hook is the only site that
	// can raise it.
	NEIssueX2DeliveryLost = "x2DeliveryLost"
	// NEIssueKeepalivesMissing: no X1 request has arrived from the ADMF within TIME_P2,
	// which TS 103 221-1 clause 6.6.2 reports as a FaultReport with status/fault code 9050
	// "Keepalives not received". The next X1 request clears it.
	NEIssueKeepalivesMissing = "keepalivesMissing"
	// NEIssueTaskingPurged: the keepalive procedure deactivated all tasking because the
	// party responsible for it went quiet. Interception has stopped — which is the
	// safe direction, since tasking must not outlive the authority for it — but the
	// ADMF has to know it happened rather than infer it from an absence of product.
	// Clause 6.6.2 sends it as an Alert with issue code 10000 "Database cleared".
	NEIssueTaskingPurged = "taskingPurged"
	// NEIssueReconcileFailed: this element could not establish what tasking a POI
	// still holds from before its restart, so it may have left interception running
	// that it cannot withdraw. It is an element-level condition rather than a task
	// one — which warrants they were is exactly what was lost — so it is reported
	// here rather than as a task issue, which would have to name an XID.
	NEIssueReconcileFailed = "reconcileFailed"
	// NEIssueTaskingAbsent: this element has come up with lawful interception
	// enabled and no tasking at all. On a first deployment that is simply true and
	// harmless; after a restart it is the only notice the ADMF gets that the
	// warrants it provisioned are gone, since tasking is held in memory and nothing
	// else announces the loss.
	NEIssueTaskingAbsent = "taskingAbsent"
	// NEIssueTriggerFaulty: a POI this element triggers reports that a content
	// trigger this element installed is not running — its provisioning did not
	// complete, or it carries an unresolved fault. The warrant is live and the
	// triggering function believes interception is in place, so nothing else would
	// report the gap: the POI answers to this element, not to the ADMF, and the
	// ADMF cannot ask it directly over the internal triggering interface.
	//
	// A terminating fault would be wrong. The interception has stopped, but this
	// element has not stopped it and may recover it on the next session event, so
	// what the ADMF is told is that something is currently broken rather than that
	// the tasking is over.
	NEIssueTriggerFaulty = "triggerFaulty"
	// NEIssueDuplicationRefused: this element's own datapath refused a rule that
	// carries out an interception it has accepted. The task is live, the element
	// answered the provisioning function that it would intercept, and the traffic is
	// not being duplicated.
	//
	// It is reported rather than only retried because retrying may not help: the
	// datapath refuses a rule for reasons this element cannot resolve — capacity, a
	// rule it cannot express — and an interception that is authorised and producing
	// nothing is a condition only the ADMF can act on. Non-terminating: the next
	// re-derivation attempts it again, so what the ADMF is told is that something is
	// currently broken and not that the tasking is over.
	NEIssueDuplicationRefused = "duplicationRefused"
	// NEIssueX3TagInvalid: the datapath delivered content whose correlation tag is
	// unusable, so the MDF cannot join the content to the session's signalling. The
	// interception is running but its product is not correlatable — a fault the ADMF
	// must know about even though content keeps flowing.
	NEIssueX3TagInvalid = "x3TagInvalid"
	// NEIssueRecordValueSubstituted: the network gave this element a value for a
	// record's field that the record's own definition has no value for, so the field
	// carries the nearest value the definition permits instead.
	//
	// It happens where two specifications enumerate the same concept and one has been
	// extended since the other: an NGAP handover cause the release of TS 33.128 this
	// element targets does not enumerate, for instance. The record is delivered,
	// because the field is mandatory and a procedure that went unreported is a larger
	// gap than one whose reason is imprecise — but the substituted value is itself a
	// legitimate reading, so without this report an agency cannot tell a network that
	// gave no reason from one that gave a reason this element could not carry.
	//
	// Non-terminating and element-scoped: it says something about what this element can
	// express, not about any warrant, and interception continues unaffected.
	NEIssueRecordValueSubstituted = "recordValueSubstituted"
	// NEIssueTaskingWithdrawalFailed: this element ordered a POI it triggers to stop
	// intercepting, and the POI did not acknowledge. The withdrawal is retried and
	// the element still holds the trigger in its own bookkeeping, so nothing is lost
	// — but until the POI answers, interception it has been told to end may be
	// continuing. Distinct from reconcileFailed, which is the same ignorance arrived
	// at from a restart rather than from a refused instruction.
	NEIssueTaskingWithdrawalFailed = "taskingWithdrawalFailed"
	// NEIssueTaskingWithdrawalStuck: a withdrawal has gone unacknowledged long
	// enough that interception is probably still running without authority. It is a
	// different condition from the failure that began it — "the last attempt failed"
	// and "authority was removed some time ago and content is still flowing" call for
	// different responses — and repeating the first would never say so.
	NEIssueTaskingWithdrawalStuck = "taskingWithdrawalStuck"
	// NEIssueX1AuthFailed: a peer holding an LI-CA certificate tried to provision
	// this element under an identity it is not bound to, or one this element does
	// not answer to. Nothing is malfunctioning — the request was refused — but
	// somebody inside the LI trust domain is attempting to task or untask network
	// elements, and this channel is the only place that can be said.
	NEIssueX1AuthFailed = "x1AuthFailed"
	// NEIssueX1ResponseUnattributable: this element sent an X1 request and received
	// an answer it could not bind to it — the wrong response type, an unfamiliar
	// transaction identifier, or an element naming itself as something other than
	// the one addressed.
	//
	// Distinct from a peer's refusal, which is a task-level condition the element
	// can attribute to a warrant: which task an unattributable answer concerned is
	// exactly what has not been established, so this is network-element level.
	//
	// Distinct too from taskingWithdrawalFailed, and the distinction is what makes
	// this worth its own condition. Both can hold at once, and the operator action
	// is opposite: taskingWithdrawalFailed against a POI that never answered means
	// go and look at the POI, whereas a withdrawal failing because *this* element is
	// refusing a well-formed answer is a configuration or routing fault to correct
	// here. Without the distinction, a systematic mismatch presents as a POI at
	// fault while the POI is answering perfectly well.
	NEIssueX1ResponseUnattributable = "x1ResponseUnattributable"
)

// TS 103 221-1 table 6.5.4-1: TypeOfNEIssueMessage is a closed enumeration, not
// free text. Anything else is schema-invalid and a conformant ADMF discards the
// message — which for a fault channel means the fault is never heard.
const (
	neIssueWarning      = "Warning"
	neIssueFaultCleared = "FaultCleared"
	neIssueFaultReport  = "FaultReport"
	neIssueAlert        = "Alert"
)

// Status/fault and issue codes from TS 103 221-1 table 6.7-3. Table 6.5.4-1 makes IssueCode
// conditional: it carries a code from the issue-code section of table 6.7-3 on an "Alert",
// and one from the status/fault section on a "FaultReport", "FaultCleared" or "Warning", and
// its use is mandatory where a clause requires it — as clause 6.6.2 does for 9050 and 10000.
const (
	issueCodeNonTerminatingFault = 9020
	issueCodeTerminatingFault    = 9030
	issueCodeKeepalivesNotRcvd   = 9050
	issueCodeDatabaseCleared     = 10000
)

// neIssueEncoding is how a condition this implementation knows about is expressed
// in the two fields the standard provides for it. The condition itself stays in
// the free-text description, where arbitrary strings are allowed and an ADMF can
// still tell one fault from another.
type neIssueEncoding struct {
	kind string
	code int
}

// neIssueEncodings maps each condition onto conformant fields. Where the registry
// has a code that names the condition exactly, it is used: keepalives that stopped
// arriving (9050), and tasking that is gone — deactivated by the keepalive procedure, or
// lost by an element that has come up holding nothing — is the "Database cleared" (10000)
// clause 6.6.2 sends after the purge.
var neIssueEncodings = map[string]neIssueEncoding{
	NEIssueX1ListenFailed:         {neIssueFaultReport, issueCodeTerminatingFault},
	NEIssueX3EgressDown:           {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueMDFUnreachable:         {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueInvalidConfig:          {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueContentUntasked:        {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueX3PuntLost:             {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueX3FramingLost:          {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueX3DeliveryLost:         {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueX2DeliveryLost:         {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueX3TagInvalid:           {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueRecordValueSubstituted: {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueDuplicationRefused:     {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueReconcileFailed:        {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueTriggerFaulty:          {neIssueFaultReport, issueCodeNonTerminatingFault},
	// A withdrawal that has not landed yet is a fault this element is working on:
	// it is retrying, and the trigger is still in its bookkeeping.
	NEIssueTaskingWithdrawalFailed: {neIssueFaultReport, issueCodeNonTerminatingFault},
	// One that has not landed for long enough is terminating in the sense the
	// registry means: this element cannot end an interception it was told to end,
	// and no further attempt of its own is going to change that.
	NEIssueTaskingWithdrawalStuck: {neIssueFaultReport, issueCodeTerminatingFault},
	// An answer this element cannot attribute is a fault it is working on — the
	// request is retried where the caller retries — but it is not one further
	// attempts will resolve if the cause is a mismatch in configuration, which is
	// why it is reported rather than only retried.
	NEIssueX1ResponseUnattributable: {neIssueFaultReport, issueCodeNonTerminatingFault},
	NEIssueKeepalivesMissing:        {neIssueFaultReport, issueCodeKeepalivesNotRcvd},
	NEIssueTaskingPurged:            {neIssueAlert, issueCodeDatabaseCleared},
	NEIssueTaskingAbsent:            {neIssueAlert, issueCodeDatabaseCleared},
	// A rejected provisioning attempt is not a fault — nothing has broken and
	// interception is unaffected — so it is an Alert rather than a FaultReport, the
	// enumeration value clause 6.5.4 pairs with a current security issue on the NE.
	// It carries no issue code: that field is conditional, required only when the
	// condition appears in the registry's issue-code section, and that section holds
	// nothing security-related to name. Inventing a code, or borrowing a fault code
	// for something that is not a fault, would tell the ADMF something untrue.
	NEIssueX1AuthFailed: {neIssueAlert, 0},
}

// encodeNEIssue returns the wire fields for a condition. An unrecognised one still
// yields a valid message: emitting an invalid enumeration would lose the report
// entirely, which is worse than reporting it less specifically.
func encodeNEIssue(condition string) neIssueEncoding {
	if e, ok := neIssueEncodings[condition]; ok {
		return e
	}

	return neIssueEncoding{neIssueFaultReport, issueCodeNonTerminatingFault}
}

// Reporter sends NE-initiated X1 issue reports to the ADMF (ETSI TS 103 221-1
// ReportNEIssueRequest). It is how a POI surfaces LI-plane faults — X1 bind
// failure, X3 egress socket down, MDF delivery failing, invalid config — to the
// controlling ADMF over the authorized X1 channel, never to general operator
// logs. Reports carry only NE-level status (no target or warrant identifier),
// preserving undetectability.
type Reporter struct {
	admfURL string
	admfID  string
	neID    string
	client  *http.Client
	now     func() time.Time
	// authenticated says whether an answer must prove whose it is. Same rule as on the
	// requester side (expectsAuthentication): a Reporter built without TLS for an http URL
	// speaks plain HTTP and has no certificate to bind.
	authenticated bool

	mu sync.Mutex
	// lastSent throttles a repeat of the same report, keyed by reportKey — condition and
	// scope — rather than by issue type alone, so two destinations failing inside one
	// window are two reports and neither hides the other.
	lastSent map[reportKey]time.Time
	// sending is the set of keys whose report or retraction is in flight right now.
	//
	// It exists because the decision to send and the record of having sent are two
	// steps, and something has to hold the key in between. It is also what
	// bounds the dispatch form in NotifyAsync: a report site driven by packet rate
	// finds its key reserved while the previous attempt is still outstanding, so one
	// condition costs one attempt at a time however fast the condition recurs.
	sending map[reportKey]bool
	// reported is what this element has told the provisioning function is wrong and
	// has not yet told it is right again. It is what makes a clearing report
	// possible: knowing a fault *cleared* requires knowing it was previously set,
	// which no amount of re-observing the present can supply.
	//
	// **It is not, and must not become, the answer to a status request.** That answer
	// is determined from what the element can observe when it is asked, and is
	// deliberately not a history of what was reported — see WithFaultProbes, and the
	// reasoning there for why an accumulating status answer ends up unread. These are
	// the standard's own two mechanisms and they answer different questions: "what
	// changed" and "what holds now". This is the first.
	reported map[reportKey]bool

	// forgotten marks task-scoped keys whose task ended while a report or retraction for
	// them was in flight. Settling that send records nothing: the XID it names has been
	// deactivated, and a record left behind would be inherited by a task re-activated under
	// the same XID, which the watcher would then send an AllClear for a fault it never had.
	forgotten map[reportKey]bool
}

// reportScope is which of the three scopes clause 6.5.1 defines a report carries.
type reportScope int

const (
	scopeElement reportScope = iota
	scopeTask
	scopeDestination
)

// reportKey identifies one condition at one scope, which is the unit a throttle
// and a clearing report both act on.
type reportKey struct {
	scope reportScope
	// id is the XID or DID the report concerns, empty at element scope.
	id string
	// condition is the issue type or the reason, so two different faults about one
	// destination stay distinguishable.
	condition string
}

// reserve reports whether a report of this condition should be sent now, and holds
// the key for the send that follows. It is the throttle, keyed per condition per
// scope rather than per issue type.
//
// Deciding to send and recording that a send happened are separate steps — reserve, then
// commit or release — so that a report which failed to reach the ADMF is neither
// suppressed for a throttle window the ADMF never saw nor left in reported, from where a
// later clearing report would announce the end of something nobody had been told began.
//
// **The reservation is held for the duration of the send, not merely across the
// decision.** That is what bounds NotifyAsync: releasing it at the decision would let
// a per-packet report site admit again on the very next copy, which is the
// goroutine-per-copy shape that form exists to avoid. End it with commit or release.
func (r *Reporter) reserve(k reportKey) bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sending[k] {
		return false
	}
	if last, ok := r.lastSent[k]; ok && r.now().Sub(last) < reportThrottle {
		return false
	}
	r.sending[k] = true

	return true
}

// reserveState is reserve for a fault that is a *state*: one the element reports once and
// retracts when it ends. It also declines while the ADMF already holds that fault — reported
// and not yet retracted — because the throttle window only bounds how often a report may be
// repeated, and a state the ADMF holds is not news when the window lapses. Without it a
// destination that stayed unreachable was reported again on the first sample after each
// window: two NonTerminatingFaults for one outage, the second describing nothing that had
// changed.
//
// Events, and the keepalive procedure's 9050 — which clause 6.6.2 repeats each time TIME_P2
// expires while the ADMF stays silent — use reserve.
func (r *Reporter) reserveState(k reportKey) bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	held := r.reported[k]
	r.mu.Unlock()
	if held {
		return false
	}

	return r.reserve(k)
}

// commit records that a reserved report reached the ADMF. The throttle window starts
// here rather than at the decision, so a window only ever suppresses repeats of
// something that was actually delivered, and the condition becomes retractable.
func (r *Reporter) commit(k reportKey, record bool) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()

	delete(r.sending, k)
	if r.forgotten[k] {
		delete(r.forgotten, k)
		delete(r.reported, k)
		delete(r.lastSent, k)

		return
	}
	r.lastSent[k] = now

	// A record is what a retraction later finds. A task-scoped report is recorded only when
	// it comes from NotifyTaskFault, the task watcher's form, because the watcher is what
	// retracts it and forgets it when the task ends — and the server forgets a removed task's
	// records at once (forgetTask). An event pushed with NotifyTaskAsync has no ending, so
	// recording it would grow the record with every interception that ever had one, with
	// nothing to drop it. An element-scoped *event* (Notify) is never retracted either, but
	// its entry is keyed by condition alone, so there is at most one per condition.
	if record {
		r.reported[k] = true
	}

	r.forgetExpiredLocked(now)
}

// forgetExpiredLocked drops throttle entries whose window has passed. Past its window an
// entry suppresses nothing, so dropping it changes no decision; keeping it would make the
// throttle a record of every key ever reported. r.mu must be held.
func (r *Reporter) forgetExpiredLocked(now time.Time) {
	for k, last := range r.lastSent {
		if now.Sub(last) >= reportThrottle {
			delete(r.lastSent, k)
		}
	}
}

// release gives back a reservation whose send did not reach the ADMF. Nothing is
// recorded: the condition is eligible again at the next observation, no clearing report
// can later refer to a fault that was never delivered, and a retraction that failed leaves
// the fault recorded so it can be sent again.
func (r *Reporter) release(k reportKey) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.sending, k)
	if r.forgotten[k] {
		delete(r.forgotten, k)
		delete(r.reported, k)
		delete(r.lastSent, k)
	}
}

// settle ends a reservation according to what the send established.
//
// Anything that is not a delivered send is treated as not delivered, which is the
// direction the rule chooses deliberately: over-reporting a fault costs the ADMF a
// duplicate, under-reporting costs it the fault.
func (r *Reporter) settle(k reportKey, err error) {
	if err != nil {
		r.release(k)

		return
	}
	r.commit(k, k.scope != scopeTask)
}

// settleRecorded is settle for a report that is always recorded: a task-scoped state the task
// watcher will retract.
func (r *Reporter) settleRecorded(k reportKey, err error) {
	if err != nil {
		r.release(k)

		return
	}
	r.commit(k, true)
}

// clearing reports whether a fault this element has reported at this key is now
// being retracted, and reserves the key for the retraction.
//
// It answers false for a fault that was never reported, which is what stops a
// watcher starting up on a healthy element from announcing recoveries from faults
// nobody was told about.
//
// It deliberately does not consult the throttle. A fault beginning and that same
// fault clearing are two events and not a repetition, and an element that throttled
// the second against the first would report a fault it never retracts — which is
// worse than reporting neither, because the ADMF acts on the first.
//
// The record is kept until commitClear rather than dropped here, so a retraction that
// fails can be sent again. Dropping it in front of the send would leave the ADMF holding a
// fault this element believes it has cleared, and nothing re-observes a fault that has
// gone away.
func (r *Reporter) clearing(k reportKey) bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sending[k] {
		return false
	}
	if !r.reported[k] {
		return false
	}
	r.sending[k] = true

	return true
}

// holds reports whether a fault at this key is reported and not yet retracted, counting
// one whose report or retraction is still in flight. It is what lets a watcher that
// asked for a retraction know whether it arrived, and so whether to keep asking.
func (r *Reporter) holds(k reportKey) bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.reported[k] || r.sending[k]
}

// commitClear forgets a fault whose retraction reached the ADMF.
func (r *Reporter) commitClear(k reportKey) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.sending, k)
	delete(r.reported, k)
	delete(r.forgotten, k)
	// The throttle is forgotten with it, so the same fault recurring immediately is
	// reported rather than suppressed as a repeat of the one just retracted.
	delete(r.lastSent, k)
}

// settleClear ends a retraction's reservation according to what the send established.
// A retraction that did not arrive leaves the fault recorded, so it stays retractable.
//
// A retraction the ADMF refused is settled as delivered. The refusal is the ADMF's answer to
// this retraction, not a failure to reach it, and sending the same retraction again gets the
// same answer — every time the next opportunity comes round, which for the keepalive fault is
// every X1 request the ADMF sends. An ADMF refusing to clear a fault it does not hold is the
// likely case, and a fault it does not hold is already cleared as far as it is concerned.
func (r *Reporter) settleClear(k reportKey, err error) {
	var refused *RequestError
	if err != nil && !errors.As(err, &refused) {
		r.release(k)

		return
	}
	r.commitClear(k)
}

// NewReporter returns a Reporter that POSTs to the ADMF's X1 endpoint admfURL
// over mutual TLS, identifying itself as neID to the ADMF admfID.
func NewReporter(admfURL, admfID, neID string, tlsConfig *tls.Config) *Reporter {
	return &Reporter{
		admfURL:       admfURL,
		admfID:        admfID,
		neID:          neID,
		authenticated: expectsAuthentication(admfURL, tlsConfig),
		client:        newX1Client(tlsConfig, expectsAuthentication(admfURL, tlsConfig), roleADMF, admfID),
		now:           monotonicNow,
		lastSent:      make(map[reportKey]time.Time),
		sending:       make(map[reportKey]bool),
		reported:      make(map[reportKey]bool),
		forgotten:     make(map[reportKey]bool),
	}
}

// reportTemplate emits an X1Request carrying a ReportNEIssueRequest in the
// conventional ns1/xsi wire form (Go's encoding/xml can't produce the xsi:type
// QName cleanly), mirroring the response template.
var reportTemplate = template.Must(template.New("x1report").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:ReportNEIssueRequest">
    <ns1:admfIdentifier>{{esc .AdmfID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .TxID}}</ns1:x1TransactionId>
    <ns1:typeOfNeIssueMessage>{{esc .Kind}}</ns1:typeOfNeIssueMessage>
    <ns1:description>{{esc .Description}}</ns1:description>
{{- if .IssueCode}}
    <ns1:issueCode>{{.IssueCode}}</ns1:issueCode>
{{- end}}
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// Task-scoped conditions: the ones an element answers *about one task* when its status is
// asked for, or reports against one task over ReportTaskIssue.
//
// **Declared here, beside the element-scoped conditions, and with their scope recorded in a
// table rather than decided at each call site.** The scope of a condition is a property of the
// condition — "the mediation functions I deliver to are unreachable" is true of every task this
// element holds; "the datapath is not duplicating what this task requires" is true of one — and
// deciding it per author is how a condition ends up reported at a scope that cannot act on it:
// a task's fault reported at element scope tells a provisioning function that something among
// its warrants has stopped and leaves it unable to say which.
//
// The description a POI sends is prose because that is the only free-form field a task's fault
// list has — errorCode and errorDescription, nothing else — so the condition name leads it, as
// NEFault does for the element's own faults. An ADMF correlating a pushed report against an
// interrogation answer then has a token to match on rather than two sentences.
const (
	// TaskIssueDuplicationNotProgrammed: the datapath is not duplicating traffic this task
	// selects. Covers a rule the datapath refused, a push that was lost, and a rule never
	// programmed at all — the element cannot tell them apart and does not need to, because
	// what it reports is that no copy is being made.
	TaskIssueDuplicationNotProgrammed = "duplicationNotProgrammed"
	// TaskIssueNoTrafficSelected: nothing this element holds carries traffic this task selects.
	// For a triggered POI, whose task was installed against traffic that existed, this is an
	// interception producing nothing.
	TaskIssueNoTrafficSelected = "noTrafficSelected"
	// TaskIssueTriggerNotRunning: a point of interception this element triggered reports the
	// task as not running. Raised by a triggering function, which is the only party that can
	// see it — the POI answers to it and not to the ADMF.
	TaskIssueTriggerNotRunning = "triggerNotRunning"
	// TaskIssueRecordNotEncoded: a record this warrant's interception produced could not be
	// encoded, so it was not delivered. The product an agency receives for this warrant is
	// incomplete by exactly that record.
	//
	// Task-scoped because the element-scoped delivery-loss report may name neither the warrant
	// nor the record type, and on its own says only that *something* did not encode — the same
	// signal an unreachable mediation function produces, which admits the same wrong diagnosis.
	// An agency missing one side of an exchange needs to know which product is absent and for
	// which warrant.
	TaskIssueRecordNotEncoded = "recordNotEncoded"
	// TaskIssueRecordNotProduced: a record the specification mandates for this warrant could
	// not be produced, because the content it must carry was not available to the element when
	// its trigger occurred — for an AMF, a MANAGE UE POLICY COMPLETE whose COMMAND it no longer
	// holds, after a restart or for a UE answering under another procedure transaction. The
	// product is incomplete by that record, as with TaskIssueRecordNotEncoded; the difference is
	// that nothing was built to fail.
	//
	// An event, like TaskIssueRecordNotEncoded: pushed once when it happens (NotifyTaskAsync)
	// and never retracted, since a record that was not produced does not later become produced.
	TaskIssueRecordNotProduced = "recordNotProduced"
	// TaskIssueContentUnderAnotherTask: a session this task selects is also selected by
	// another task, and this element delivers each duplicated packet under exactly one of them
	// — the lowest XID — so this task receives none of that session's content.
	//
	// A fault and not a warning: the warrant is authorised and its product is incomplete, which
	// is what a non-terminating fault says, and nothing in the delivered product would tell the
	// agency otherwise. Reported against the task that loses the content, because that is the
	// warrant whose product is incomplete — the one receiving it has nothing wrong with it — and
	// only against that one: the other task's XID is not named, since which warrants overlap is
	// for the ADMF to correlate from its own provisioning. A state, so the task watcher retracts
	// it when the overlap ends.
	TaskIssueContentUnderAnotherTask = "contentUnderAnotherTask"
)

// taskStates is the task-scoped conditions that are states: re-observable, so they have an
// ending, and the task watcher reports both edges of each (see WatchTaskFaults). The rest
// are events, pushed when they happen and never retracted.
var taskStates = map[string]bool{
	TaskIssueDuplicationNotProgrammed: true,
	TaskIssueNoTrafficSelected:        true,
	TaskIssueTriggerNotRunning:        true,
	TaskIssueContentUnderAnotherTask:  true,
}

// taskScoped is the set of conditions that concern one task rather than the element.
//
// It exists so the choice is made once. TestConditionScopesAreDisjoint asserts that no
// condition is in both this and neIssueEncodings: a condition reported at both scopes is one
// an ADMF sees twice and cannot reconcile, and a condition in neither is one nothing can
// classify.
var taskScoped = map[string]bool{
	TaskIssueDuplicationNotProgrammed: true,
	TaskIssueNoTrafficSelected:        true,
	TaskIssueTriggerNotRunning:        true,
	TaskIssueRecordNotEncoded:         true,
	TaskIssueRecordNotProduced:        true,
	TaskIssueContentUnderAnotherTask:  true,
}

// TS 103 221-1 TaskReportType values (clause 6.5.2 / the published XSD
// enumeration). A Triggering Function uses these to tell the LIPF what became of
// an interception it was asked to arrange.
const (
	// TaskReportAllClear: a fault previously reported at this scope has cleared.
	//
	// The counterpart of FaultCleared, which is the network-element one, at the two
	// scopes whose report type is this enumeration. Clause 5.3 requires both — "The NE
	// shall also indicate that a fault has been cleared (see clauses 6.5.2 and 6.5.3)
	// unless otherwise configured" — and clause 6.5.2.1 says it of a task: "If a
	// non-terminating fault is cleared, the NE shall send another ReportTaskIssue
	// indicating the fault is cleared." This element sends it at destination scope
	// (NotifyDestinationClear) and at task scope (NotifyTaskClear), for the conditions
	// that are states; a task-scoped event has no ending to report.
	TaskReportAllClear = "AllClear"
	// TaskReportWarning: something is wrong but the interception continues.
	TaskReportWarning = "Warning"
	// TaskReportNonTerminatingFault: a fault the interception survives.
	TaskReportNonTerminatingFault = "NonTerminatingFault"
	// TaskReportTerminatingFault: the interception cannot continue. This is what a
	// CC-TF sends when a triggered POI refuses or fails its trigger — the warrant
	// is authorised but no product will be produced, which only the LIPF can act on.
	TaskReportTerminatingFault = "TerminatingFault"
)

// reportTaskTemplate emits an X1Request carrying a ReportTaskIssueRequest. Element
// order follows the ReportTaskIssueRequest xs:sequence: xId, taskReportType, then
// the optional error code and details.
var reportTaskTemplate = template.Must(template.New("x1taskissue").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:ReportTaskIssueRequest">
    <ns1:admfIdentifier>{{esc .AdmfID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .TxID}}</ns1:x1TransactionId>
    <ns1:xId>{{esc .XID}}</ns1:xId>
    <ns1:taskReportType>{{esc .ReportType}}</ns1:taskReportType>
{{- if .Details}}
    <ns1:taskIssueDetails>{{esc .Details}}</ns1:taskIssueDetails>
{{- end}}
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// Notify reports an NE-level issue and discards the outcome. It is the form the
// network functions call: an issue report is best-effort by design — the LI plane
// must not surface faults through anything but this channel, so a failed report has
// nowhere general to go — and expressing that as a void call keeps every call site
// from blank-assigning an error the linter then flags. A nil Reporter is a no-op, so a
// network function with no ADMF configured need not guard every call. A caller holding the
// Reporter behind an interface must still guard: a nil interface value cannot be called at
// all.
//
// **The outcome is not lost, only not returned.** The caller has nowhere to put the error;
// the reporter does, and settles it before this returns. A report that did not arrive
// leaves its key unreserved rather than recorded as told, so the condition is eligible at
// the next observation and no retraction is later sent for a fault the ADMF never had.
//
// It stays silent on failure for the same reason ReportNEIssue does: writing the
// error anywhere general would be the very disclosure this plane exists to avoid.
func (r *Reporter) Notify(issueType, description string) {
	if r == nil {
		return
	}
	//nolint:errcheck // fire-and-forget by design; see the doc comment
	_ = r.ReportNEIssue(issueType, description)
}

// NotifyAsync reports an NE-level issue without blocking the caller. It is the form
// for a path that may not wait — a data-plane loop, a signalling path, or an X1
// request goroutine — and it is the answer to that hazard for every network function,
// so none of them hand-rolls its own.
//
// **The throttle is consulted here, synchronously, and the POST is dispatched only
// when the report is going to be sent.** That ordering is the whole design, and it is
// why this is not `go Notify`. The shipper's report sites fire per dropped copy, so
// spawning first would spawn a goroutine per packet, each of which takes this mutex,
// discovers it is throttled and exits — trading a 10s stall for unbounded goroutine
// churn on the one path that must stay cheap. With the check first, an admitted report
// costs one goroutine per condition per throttle window and a suppressed one costs a
// lock acquisition. The reservation is held until the send settles, so a condition
// recurring at packet rate has at most one attempt outstanding, and the goroutine that
// makes it ends within x1RequestTimeout.
//
// It dispatches reportNEIssueAs and not Notify or ReportNEIssue: those consult the
// throttle themselves, so reserving here and then calling one of them reserves twice
// on one key, and the second call suppresses the message the first just allowed —
// which would silently turn this channel off rather than make it non-blocking.
func (r *Reporter) NotifyAsync(issueType, description string) {
	if r == nil {
		return
	}

	k := reportKey{scope: scopeElement, condition: issueType}
	if !r.reserve(k) {
		return
	}
	encoding := encodeNEIssue(issueType)

	go func() {
		r.settle(k, r.reportNEIssueAs(issueType, encoding.kind, encoding.code, description))
	}()
}

// NotifyTask reports a per-task issue and discards the outcome — the task-scoped
// counterpart to Notify, wrapping ReportTaskIssue for the same reason.
func (r *Reporter) NotifyTask(xid, reportType, details string) {
	if r == nil {
		return
	}
	//nolint:errcheck // fire-and-forget by design; see the doc comment
	_ = r.ReportTaskIssue(xid, reportType, details)
}

// NotifyTaskAsync reports a per-task issue without blocking the caller — the task-scoped
// counterpart to NotifyAsync, and for the same reason.
//
// NotifyTask is fire-and-forget about the *outcome* but not about the *call*: it performs the mTLS
// round trip on the caller's goroutine, bounded only by the client timeout. That is safe on a
// triggering function's own loops, which is where it is used, and unsafe on a subscriber's
// signalling path — a synchronous POST there is a delay the target can observe, which is precisely
// what undetectability forbids.
//
// condition is the token the throttle keys on, separate from details so that two different
// conditions about one task cannot suppress each other. As in NotifyAsync the reservation is taken
// *before* the goroutine is spawned, so a condition recurring quickly costs a lock acquisition
// rather than a goroutine; reports are throttled per (xid, condition), and the goroutine ends
// within x1RequestTimeout.
//
// It is the form for an event, which has no ending, so the report is never recorded as a
// standing fault: nothing will retract it. A condition that is a state and should be retracted
// when it ends belongs to the task watcher (WatchTaskFaults), through the WithTaskFaults
// supplier.
func (r *Reporter) NotifyTaskAsync(xid, reportType, condition, details string) {
	if r == nil || xid == "" {
		return
	}

	k := reportKey{scope: scopeTask, id: xid, condition: condition}
	if !r.reserve(k) {
		return
	}

	go func() {
		r.settle(k, r.ReportTaskIssue(xid, reportType, details))
	}()
}

// ReportTaskIssue POSTs a ReportTaskIssueRequest to the ADMF for a specific
// interception task. TS 33.128 clause 5.2.6 requires this of a Triggering
// Function whose triggered POI answers an error: the warrant exists and is
// authorised, but the product it authorises is not being produced, and the
// triggering function is the only party that knows.
//
// Unlike ReportNEIssue this names an XID, because the issue is with one
// interception rather than with the element, and details must still describe the fault
// without naming the target. This method sends every time it is called; NotifyTaskAsync
// is the throttled form, keyed per (xid, condition). A nil Reporter sends nothing.
func (r *Reporter) ReportTaskIssue(xid, reportType, details string) error {
	if r == nil {
		return nil
	}
	// Held rather than generated inline, because the answer has to be bound to it.
	txID := newUUID()

	var body bytes.Buffer
	if err := reportTaskTemplate.Execute(&body, struct {
		AdmfID, NeID, Timestamp, TxID, XID, ReportType, Details string
	}{
		AdmfID:     r.admfID,
		NeID:       r.neID,
		Timestamp:  x1Timestamp(r.now()),
		TxID:       txID,
		XID:        xid,
		ReportType: reportType,
		Details:    details,
	}); err != nil {
		return fmt.Errorf("x1: rendering ReportTaskIssueRequest: %w", err)
	}

	resp, err := postX1(r.client, r.admfURL, &body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return r.readReportResponse("ReportTaskIssueRequest", txID, resp)
}

// NotifyTaskFault reports a task-scoped condition that is a state, once per (xid, condition)
// until it is retracted, and records it so NotifyTaskClear can later retract it.
//
// It is the task-scoped NotifyDestinationFault: synchronous, for the task watcher, which is
// the one owner of both edges. NotifyTaskAsync remains the form for an event.
func (r *Reporter) NotifyTaskFault(xid, condition, details string) {
	r.notifyTaskFaultWhile(xid, condition, details, nil)
}

// notifyTaskFaultWhile is NotifyTaskFault that sends only if held, asked once the key is
// reserved, still reports true. Asked after the reservation rather than before it, so a task
// removed after the question marks the send forgotten (forgetTask) and one removed before it is
// not reported at all. A nil held always holds.
func (r *Reporter) notifyTaskFaultWhile(xid, condition, details string, held func() bool) {
	if r == nil || xid == "" {
		return
	}
	k := reportKey{scope: scopeTask, id: xid, condition: condition}
	if !r.reserveState(k) {
		return
	}
	if held != nil && !held() {
		r.release(k)

		return
	}
	r.settleRecorded(k, r.ReportTaskIssue(xid, TaskReportNonTerminatingFault, condition+": "+details))
}

// NotifyTaskClear retracts a task-scoped fault previously reported, with an AllClear, and
// does nothing if none was. Like NotifyDestinationClear it is not throttled against the
// report it retracts: the two are a state change and not a repetition.
func (r *Reporter) NotifyTaskClear(xid, condition string) {
	if r == nil || xid == "" {
		return
	}
	k := reportKey{scope: scopeTask, id: xid, condition: condition}
	if !r.clearing(k) {
		return
	}
	r.settleClear(k, r.ReportTaskIssue(xid, TaskReportAllClear, condition+": resolved"))
}

// standingTaskFaults returns the task-scoped faults reported and not yet retracted.
func (r *Reporter) standingTaskFaults() []reportKey {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var out []reportKey
	for k := range r.reported {
		if k.scope == scopeTask {
			out = append(out, k)
		}
	}

	return out
}

// forgetTask drops every task-scoped record of xid without retracting it: the task has ended,
// which ends its faults, and an AllClear would name an XID the ADMF has deactivated. Done when
// the task is removed rather than at the next watcher sample, so a task re-activated under the
// same XID within one interval does not inherit the old task's faults. A key whose report or
// retraction is still in flight -- including a first report, which has no record yet -- is
// marked, and its settlement records nothing.
//
// It walks every in-flight and recorded key, so a purge of n tasks costs n walks. Both sets
// hold faults this element has reported and not retracted, and reports outstanding right now,
// which stay small beside the tasks an element holds; an index by XID would be the remedy if
// they did not.
func (r *Reporter) forgetTask(xid string) {
	if r == nil || xid == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	ofTask := func(k reportKey) bool { return k.scope == scopeTask && k.id == xid }
	for k := range r.sending {
		if ofTask(k) {
			r.forgotten[k] = true
		}
	}
	for k := range r.reported {
		if ofTask(k) && !r.sending[k] {
			delete(r.reported, k)
			delete(r.lastSent, k)
		}
	}
}

// forget drops the record of a fault without retracting it. The task watcher uses it for a
// task no longer held: the task's end ends its faults, and the ADMF has deactivated the XID
// an AllClear would name.
func (r *Reporter) forget(k reportKey) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sending[k] {
		return
	}
	delete(r.reported, k)
	delete(r.lastSent, k)
}

// reportDestinationTemplate emits an X1Request carrying a
// ReportDestinationIssueRequest. Element order follows its xs:sequence: dId,
// destinationReportType, then the optional error code and details.
var reportDestinationTemplate = template.Must(template.New("x1destissue").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:ReportDestinationIssueRequest">
    <ns1:admfIdentifier>{{esc .AdmfID}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .NeID}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .Timestamp}}</ns1:messageTimestamp>
    <ns1:version>{{version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .TxID}}</ns1:x1TransactionId>
    <ns1:dId>{{esc .DID}}</ns1:dId>
    <ns1:destinationReportType>{{esc .ReportType}}</ns1:destinationReportType>
{{- if .Details}}
    <ns1:destinationIssueDetails>{{esc .Details}}</ns1:destinationIssueDetails>
{{- end}}
  </ns1:x1RequestMessage>
</ns1:X1Request>`))

// NotifyDestinationFault reports that a delivery destination is at fault, once until it is
// retracted (see reserveState), and records that it has been reported so the fault can later
// be retracted.
//
// (TS 103 221-1 clause 6.5.3): "The NE shall send a ReportDestinationIssue request
// when it becomes aware of an issue (warning or fault) relating specifically to a
// particular DID."
//
// Clause 6.5.1 scopes an issue three ways — to a task, to a delivery destination,
// or to the whole element — and the scope is what tells a provisioning function
// where to act. An unreachable mediation function reported at element scope is the
// truth at the wrong scope: a function that provisioned several destinations learns
// one of them is unreachable and cannot learn which, so the only action available
// to it concerns all of them.
//
// **Naming the DID is not a widening of what this channel discloses.** It is the
// provisioning function's own identifier for an endpoint it created; it names
// neither a target nor a warrant, and the rule that a report names neither is
// unchanged. details is human-readable text under the same rule.
//
// Fire-and-forget, like Notify: a failed report has nowhere to go, and this is the
// form the network functions call.
func (r *Reporter) NotifyDestinationFault(did, condition, details string) {
	if r == nil {
		return
	}
	k := reportKey{scope: scopeDestination, id: did, condition: condition}
	if !r.reserveState(k) {
		return
	}
	// Fire-and-forget in what it tells the caller, not in what it records: the outcome
	// has nowhere to go but it still decides whether this element may claim to have
	// reported the fault.
	r.settle(k, r.ReportDestinationIssue(did, TaskReportNonTerminatingFault, condition+": "+details))
}

// NotifyElementFault reports a condition at network-element scope, once until it is
// retracted (see reserveState), and records it so it can later be retracted.
//
// The same message Notify sends, and the same conditions — what it adds is the
// record, so a condition the element can re-observe can also be reported as
// cleared. Notify remains the form for an *event*, which happened once and has no
// ending anybody could observe.
func (r *Reporter) NotifyElementFault(condition, description string) {
	if r == nil {
		return
	}
	k := reportKey{scope: scopeElement, condition: condition}
	if !r.reserveState(k) {
		return
	}
	// reportNEIssueAs and not ReportNEIssue: that one consults the throttle itself,
	// and reserving twice on one key means the second call suppresses the message
	// the first just allowed.
	encoding := encodeNEIssue(condition)
	r.settle(k, r.reportNEIssueAs(condition, encoding.kind, encoding.code, description))
}

// NotifyElementClear retracts a network-element-level fault previously reported,
// and does nothing if none was.
//
// Clause 5.3 again, at the third scope: FaultCleared is the TypeOfNeIssueMessage
// for it, as AllClear is the TaskReportType at the other two. It carries the
// status/fault code of the fault it clears, which table 6.5.4-1 requires of a
// FaultCleared whose code is in that section.
func (r *Reporter) NotifyElementClear(condition string) {
	if r == nil {
		return
	}
	k := reportKey{scope: scopeElement, condition: condition}
	if !r.clearing(k) {
		return
	}
	code := 0
	if encoding := encodeNEIssue(condition); encoding.kind == neIssueFaultReport {
		code = encoding.code
	}
	r.settleClear(k, r.reportNEIssueAs(condition, neIssueFaultCleared, code, "resolved"))
}

// raise reports a network-element fault and returns the ADMF's answer, recording the fault
// so that NotifyElementClear can later retract it.
//
// It is for a procedure that acts on the answer — the keepalive procedure starts timer P3
// only on an OK — so it is not throttled: the procedure's own timers space its reports. It
// does not send while a report or retraction of the same condition is still in flight.
//
// **The fault is recorded unless the ADMF cannot hold it, not only when it said it does.**
// The keepalive procedure's FaultCleared is owed for a fault the ADMF may hold, and an answer
// lost after the report arrived — a timeout, a connection reset — leaves it holding one this
// element never heard acknowledged. Recorded only on an OK, that fault would stand at the ADMF
// for as long as the element runs: the next X1 request finds nothing to clear. So the error is
// still returned, and the procedure treats the report as unanswered, but the record is kept
// unless the answer settles that the report did not arrive. Over-reporting costs the ADMF a
// FaultCleared for a fault it may not have; under-reporting costs it a fault it can never see
// end.
func (r *Reporter) raise(condition, description string) error {
	if r == nil {
		return errNoKeepaliveReporter
	}
	k := reportKey{scope: scopeElement, condition: condition}

	r.mu.Lock()
	if r.sending[k] {
		r.mu.Unlock()

		return fmt.Errorf("x1: a %s report is already in flight", condition)
	}
	r.sending[k] = true
	r.mu.Unlock()

	encoding := encodeNEIssue(condition)
	err := r.reportNEIssueAs(condition, encoding.kind, encoding.code, description)
	if mayHaveArrived(err) {
		r.commit(k, true)
	} else {
		r.release(k)
	}

	return err
}

// mayHaveArrived reports whether a report whose send returned err may be held by the ADMF.
//
// Exactly three outcomes settle that it is not. An ADMF that refused the report
// (*RequestError) or could not parse it (a TopLevelError answer) has said so; and a request
// that never obtained a connection (*NotSentError: the dial failed, the TLS handshake failed,
// the ADMF's certificate did not bind its identifier) carried nothing. Anything else leaves
// open that the report arrived and only its answer did not: a timeout or a reset once
// connected, an HTTP status other than 200 — which may come from a proxy or from the ADMF
// itself, and does not say which — and an answer that could not be bound.
func mayHaveArrived(err error) bool {
	if err == nil {
		return true
	}

	var refused *RequestError
	if errors.As(err, &refused) {
		return false
	}
	var unparsed *ResponseError
	if errors.As(err, &unparsed) && unparsed.Got == "TopLevelError" {
		return false
	}
	var unsent *NotSentError

	return !errors.As(err, &unsent)
}

// NotifyDestinationClear retracts a fault previously reported for a destination,
// and does nothing if none was.
//
// Clause 5.3: "The NE shall also indicate that a fault has been cleared." The
// retraction is not throttled against the report that announced it — see clearing —
// because the two are a state change and not a repetition.
func (r *Reporter) NotifyDestinationClear(did, condition string) {
	if r == nil {
		return
	}
	k := reportKey{scope: scopeDestination, id: did, condition: condition}
	if !r.clearing(k) {
		return
	}
	r.settleClear(k, r.ReportDestinationIssue(did, TaskReportAllClear, condition+": resolved"))
}

// ReportDestinationIssue POSTs a ReportDestinationIssueRequest to the ADMF
// (TS 103 221-1 clause 6.5.3). Prefer NotifyDestinationFault and
// NotifyDestinationClear, which carry the throttling and the record of what has
// been reported; this is the message on its own. A nil Reporter sends nothing.
func (r *Reporter) ReportDestinationIssue(did, reportType, details string) error {
	if r == nil {
		return nil
	}
	txID := newUUID()

	var body bytes.Buffer
	if err := reportDestinationTemplate.Execute(&body, struct {
		AdmfID, NeID, Timestamp, TxID, DID, ReportType, Details string
	}{
		AdmfID:     r.admfID,
		NeID:       r.neID,
		Timestamp:  x1Timestamp(r.now()),
		TxID:       txID,
		DID:        did,
		ReportType: reportType,
		Details:    details,
	}); err != nil {
		return fmt.Errorf("x1: rendering ReportDestinationIssueRequest: %w", err)
	}

	resp, err := postX1(r.client, r.admfURL, &body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return r.readReportResponse("ReportDestinationIssueRequest", txID, resp)
}

// ReportNEIssue POSTs a ReportNEIssueRequest to the ADMF. issueType is a
// TS 103 221-1 typeOfNeIssueMessage (see the NEIssue* constants); description is
// human-readable NE-level text that MUST NOT contain a target or warrant
// identifier. Best-effort — the caller may ignore the returned error.
func (r *Reporter) ReportNEIssue(issueType, description string) error {
	// Throttle repeats of the same issue type so a persistent fault does not
	// flood the ADMF; safe to call on every failed event.
	k := reportKey{scope: scopeElement, condition: issueType}
	if !r.reserve(k) {
		return nil
	}

	// The condition leads the description because that is the only field on this
	// message where it may legitimately appear, and an ADMF still needs to tell one
	// fault from another.
	encoding := encodeNEIssue(issueType)

	err := r.reportNEIssueAs(issueType, encoding.kind, encoding.code, description)
	r.settle(k, err)

	return err
}

// reportNEIssueAs renders and sends a ReportNEIssue with the message kind stated
// rather than derived.
//
// It exists because a condition and its retraction are the same condition and
// different messages: the registry maps a condition to the kind that *announces*
// it, and there is no second entry for the kind that ends it. Deriving the kind
// would make FaultCleared unreachable.
//
// It does not consult the throttle. Its callers have already decided that this
// message is a state change rather than a repetition. A nil Reporter sends nothing.
func (r *Reporter) reportNEIssueAs(issueType, kind string, code int, description string) error {
	if r == nil {
		return nil
	}
	encoding := neIssueEncoding{kind: kind, code: code}

	txID := newUUID()

	var body bytes.Buffer
	if err := reportTemplate.Execute(&body, struct {
		AdmfID, NeID, Timestamp, TxID, Kind, Description string
		IssueCode                                        int
	}{
		AdmfID:      r.admfID,
		NeID:        r.neID,
		Timestamp:   x1Timestamp(r.now()),
		TxID:        txID,
		Kind:        encoding.kind,
		IssueCode:   encoding.code,
		Description: issueType + ": " + description,
	}); err != nil {
		return fmt.Errorf("x1: rendering ReportNEIssueRequest: %w", err)
	}

	resp, err := postX1(r.client, r.admfURL, &body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return r.readReportResponse("ReportNEIssueRequest", txID, resp)
}

// readReportResponse establishes whether the ADMF accepted a report.
//
// **HTTP 200 is not the answer.** Clause 7.2.2.2 is explicit that HTTP codes indicate
// HTTP-level errors only and that an X1-level error "shall be … returned as a HTTP 200 OK
// response", so a 200 wrapping an ErrorResponse — or a TopLevelError, which the ADMF
// returns when it could not parse what this element sent — is a report the ADMF discarded.
// Read as delivered, it commits: the element records the fault as reported, stops
// re-reporting it while the condition holds, and later sends an AllClear retracting a fault
// the ADMF never had. So the ADMF's list of faults and the element's disagree, in the one
// direction neither side can detect.
//
// It reuses the requester side's binding rather than adding a second decoder, because both
// senders on this interface have the same obligation and the only difference is which role
// the peer holds: the response type derived from the request's, the transaction identity,
// and the responder's certificate in the ADMF role — so an answer from something other than
// the responsible ADMF cannot commit a report either.
func (r *Reporter) readReportResponse(requestType, txID string, resp *http.Response) error {
	h := header{OurID: r.admfID, NeID: r.neID, TxID: txID, Type: requestType}

	m, err := readBoundResponse(h, resp, r.authenticated, roleADMF, r.admfID)
	if err != nil {
		return err
	}
	if m.ErrorInformation != nil {
		return &RequestError{Code: m.ErrorInformation.ErrorCode, Description: m.ErrorInformation.ErrorDescription}
	}

	// And an acceptance says so. A response of the right type that carries neither an error nor
	// an oK is not one: committing on it would record a fault as told on an answer that told
	// this element nothing.
	//
	// "Acknowledged" without "AndCompleted" is acceptance of a report: the ADMF has it, and
	// there is no further step of this element's to complete. ReportNEIssueResponse is the
	// exception the schema makes — its oK is OKAckAndComplete, which has no second value — so
	// there it is a value outside the type and the answer is malformed.
	switch {
	case m.OK == ackOK:
	case m.OK == ackPending && requestType != "ReportNEIssueRequest":
	case m.OK == "":
		return fmt.Errorf("x1: response %q carried neither acknowledgement nor error", localType(m.Type))
	default:
		return fmt.Errorf("x1: malformed response: oK %q is not a value the schema defines for %s",
			m.OK, responseTypeFor(requestType))
	}

	return nil
}

// NewUUID returns a random RFC 4122 v4 UUID — the form TS 103 221-1 requires of
// an x1TransactionId, an XID and a DID alike. Exported because a Triggering
// Function allocates XIDs and DIDs of its own and should not reimplement the
// generator to do it.
func NewUUID() string {
	return newUUID()
}

// newUUID returns a random RFC 4122 v4 UUID for the x1TransactionId, without an
// external dependency (this package uses the standard library only).
func newUUID() string {
	var b [16]byte
	//nolint:errcheck // crypto/rand.Read never returns an error
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
