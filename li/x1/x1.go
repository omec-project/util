// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"bytes"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// maxRequestBytes bounds an X1 request body. A request carries provisioning messages,
// each a few kilobytes; a megabyte holds hundreds of them, which is far more than an ADMF
// sends in one container, while keeping one request's memory bounded.
const maxRequestBytes = 1 << 20

// The TS 103 221-1 deliveryType values, which appear both as something this
// element parses from a request and as something it emits in a details answer.
const (
	deliveryX2Only  = "X2Only"
	deliveryX3Only  = "X3Only"
	deliveryX2andX3 = "X2andX3"
)

// The two X1 response shapes: an acknowledgement, or an error carrying a code.
const (
	ackOK         = "AcknowledgedAndCompleted"
	errorResponse = "ErrorResponse"
)

// responseTemplate emits an X1Response in the conventional TS 103 221-1 wire
// form (xsi/ns1 prefixes, xsi:type QName), which Go's encoding/xml can't
// produce cleanly. Input is still parsed structurally with encoding/xml.
var responseTemplate = template.Must(template.New("x1resp").Funcs(x1TemplateFuncs).Funcs(template.FuncMap{
	"responseBody": responseBody,
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Response xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:ext="urn:3GPP:ns:li:3GPPX1Extensions:r18:v6" xmlns:c="http://uri.etsi.org/03280/common/2017/07">{{range .Messages}}
  <ns1:x1ResponseMessage xsi:type="ns1:{{esc .Type}}">
    <ns1:admfIdentifier>{{esc .AdmfIdentifier}}</ns1:admfIdentifier>
    <ns1:neIdentifier>{{esc .NeIdentifier}}</ns1:neIdentifier>
    <ns1:messageTimestamp>{{esc .MessageTimestamp}}</ns1:messageTimestamp>
    <ns1:version>{{esc .Version}}</ns1:version>
    <ns1:x1TransactionId>{{esc .X1TransactionID}}</ns1:x1TransactionId>
{{responseBody . -}}
  </ns1:x1ResponseMessage>{{end}}
</ns1:X1Response>`))

// x1TimestampLayout renders the ETSI TS 103 280 QualifiedMicrosecondDateTime that every
// X1 message carries. Its pattern demands *exactly* six fractional digits:
//
//	[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}(Z|[+-][0-9]{2}:[0-9]{2})
//
// The zeros in this layout are what keep trailing zeros; Go's nines, as in time.RFC3339Nano,
// strip them, and roughly one timestamp in ten would then carry five digits or fewer and
// fail a peer's validation -- in responses, in the triggers a CC-TF sends, and in fault
// reports alike. One shared layout serves all of them, and a test pins it with a clock
// whose fraction ends in zeros.
const x1TimestampLayout = "2006-01-02T15:04:05.000000Z07:00"

// x1Timestamp renders t for an X1 message, in UTC.
//
// UTC is applied here, where the instant is formatted, and not by the clocks that produce
// it. time.Time.UTC strips the monotonic reading, so a clock returning time.Now().UTC()
// makes every elapsed-time comparison made with it a wall-clock difference — and the
// keepalive watchdog and the report throttles are elapsed-time comparisons. See
// monotonicNow.
func x1Timestamp(t time.Time) string { return t.UTC().Format(x1TimestampLayout) }

// monotonicNow is the clock this package's servers, reporters and requesters read by
// default. It is time.Now, unconverted, so it keeps the monotonic reading that makes
// Sub and comparisons immune to a step in the wall clock. With the reading stripped, a
// forward step larger than the keepalive window — a VM resumed, an NTP step — purges
// every warrant at the next tick while the ADMF is alive, and a backward step keeps a
// dead ADMF's tasking past the window the fail-safe exists to enforce.
func monotonicNow() time.Time { return time.Now() }

// marshalResponse renders an X1Response to its wire form.
func marshalResponse(resp *X1Response) ([]byte, error) {
	var b bytes.Buffer
	if err := responseTemplate.Execute(&b, resp); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Server is an http.Handler implementing the X1 network-element endpoint
// (TS 103 221-1). It applies received tasking to Store. Mount it at the X1 path
// the ADMF is configured to use (e.g. "/X1/NE"). It logs nothing about task
// content, to keep interception undetectable.
type Server struct {
	store  *store.Store
	neID   string
	admfID string // responsible ADMF; empty disables the "expected ADMF" check
	now    func() time.Time
	// onTaskChange is this element's one lifecycle callback. See OnTaskChange for
	// why one event carrying both sides beats two events under one XID.
	onTaskChange func(prev, next *types.InterceptTask)
	// onPurge names why tasking went, after it has gone. See OnPurge.
	onPurge func(types.InterceptTask, PurgeReason)
	// onAuthFailure is told the X1 error code when a peer fails clause 8.2.4
	// authentication. Nil leaves such failures unreported: refused and invisible.
	onAuthFailure func(code int)
	// canApply asks the POI whether it can actually carry out a task before it is
	// acknowledged. Nil accepts everything, which is right for a POI whose only
	// question about a task is answered by this package.
	canApply func(types.InterceptTask) error
	// faultProbes answer whether a fault condition holds *now*. They are consulted when the
	// element is asked for its status, never cached, so no answer can go stale — which is the
	// failure mode every retaining design shares. See WithFaultProbes.
	faultProbes []FaultProbe
	// taskFaults answers what is wrong with one task, now. Consulted when a task's status is
	// rendered; nil means this element supplies none, and every task then answers with an
	// empty fault list. See WithTaskFaults.
	taskFaults func(types.XID) []X1Error
	// destinationReachable answers whether this element can currently deliver to one
	// address. Consulted when a destination's details are assembled, never cached, for the
	// same reason the probes are not.
	//
	// nil means the element supplied no answer, and every destination then reports
	// activeAndWorking, as a stated default rather than a claim made on the element's
	// behalf. See WithDestinationReachability.
	destinationReachable func(addr string) bool
	// deactivateAllDisabled and removeAllDestinationsDisabled carry what a deployment has
	// agreed about the two bulk operations. The specification enables both when there has been
	// no agreement in advance, and so does this element.
	//
	// DeactivateAllTasks: "By default (if there has been no agreement in advance) then
	// DeactivateAllTasks is enabled." It fails safe — interception stops, which is the
	// direction this whole capability fails in anyway.
	//
	// RemoveAllDestinations: clause 6.3.4 enables it in the same terms, and guards it: an NE
	// refuses it while any destination is referenced by a task, so it cannot leave a tasked
	// element with nowhere to deliver. It is also the only X1 path by which a destination
	// provisioned wrongly can be removed, since ModifyDestination and RemoveDestination are not
	// implemented.
	deactivateAllDisabled         bool
	removeAllDestinationsDisabled bool

	// transitionMu makes a task transition and the notification that carries it one
	// indivisible step, against every other task operation including the fail-safe
	// purge.
	//
	// **The interleaving that matters is not two activations.** It is an activation
	// racing the keepalive-lapse purge, which snapshots the held tasks and only then
	// empties the store: a task stored in between is removed from the store with no
	// onTaskChange(task, nil) at all. The element then answers the ADMF "activated",
	// holds nothing, and leaves the POI applying tasking that no longer exists
	// anywhere — at a triggering function, content interception surviving a purge
	// whose whole purpose was to stop everything, kept alive by the keepalives the
	// forgotten trigger goes on earning. That is the worst of the available
	// directions, and an unsynchronised read-modify-notify reaches it first.
	//
	// The POI callback runs *under* this lock, which serialises X1 provisioning
	// against itself for the duration of the callback — at the UPF, a session walk.
	// That is the correct reading of the interface rather than a concession:
	// purgeAllTasking and notifyRemoved already document ordering guarantees ("the
	// store is cleared first, so a POI re-deriving during its own teardown sees an
	// empty one"; "what is being reported is that interception stopped, so it must
	// have stopped") which mean nothing unless no other transition can interleave.
	// The lock is what makes those guarantees hold.
	//
	// It is not taken by the fault probes or by recordActivity. Probes are documented
	// as safe to call from the X1 request goroutine and perform no I/O, so a
	// GetNEStatus must not queue behind a DeactivateTask's session walk; recordActivity
	// keeps mu. Where both are held, this one is taken first.
	transitionMu sync.Mutex

	mu       sync.Mutex
	lastSeen time.Time // time of the last X1 message from the ADMF (keepalive watchdog)
	// heard counts X1 requests from the ADMF. It tells a purge whether a request arrived while
	// it ran, which lastSeen cannot when two requests share an instant.
	heard uint64
	// keepalive is the TS 103 221-1 clause 6.6.2 procedure's state, guarded by mu. See
	// WatchKeepalive.
	keepalive keepaliveState
	// keepaliveReporter carries the procedure's reports to the ADMF. See KeepaliveReporter.
	keepaliveReporter *Reporter
	// taskFaultReporter is the reporter the task fault watcher reports through, guarded by
	// mu. A removed task's records are forgotten in it at once (see notifyRemoved).
	taskFaultReporter *Reporter
	// requireDIDs refuses a CC task whose destinations are unknown, rather than
	// accepting it and delivering nothing.
	requireDIDs bool
	// honoursCorrelation records that this element acts on a provisioned correlation
	// value. See HonoursCorrelationID.
	honoursCorrelation bool
	// destinations maps a DID provisioned with CreateDestination to the destination it
	// names. A task carries DIDs rather than addresses, so this is how an NE learns
	// where to deliver product.
	destinations map[string]heldDestination
	// configured holds DID→destination entries this element's own configuration
	// declares. They resolve exactly as provisioned ones do, and a provisioned entry
	// for the same DID takes precedence — see resolveLocked. Neither specification
	// requires that a task's destination identifier arrived over X1 rather than having
	// been agreed out of band, so this is a supported arrangement and not a fallback.
	configured map[string]heldDestination

	// afterPurgeSnapshot, afterDestinationGuard and afterLapseDecision are set only by
	// this package's tests, to hold a goroutine inside a window a few instructions wide;
	// nil otherwise. afterPurgeSnapshot runs between reading the tasks a purge tears down
	// and emptying the store; afterDestinationGuard inside createDestination, after its
	// refusals are decided and before the destination is stored; afterLapseDecision
	// inside a keepalive purge, after it concludes the ADMF has gone quiet and before it acts.
	afterPurgeSnapshot    func()
	afterDestinationGuard func()
	afterLapseDecision    func()
}

// heldDestination is a delivery destination this element can resolve a DID to.
//
// The X1 delivery type is kept as the ADMF stated it rather than reduced to a single
// product type. Reducing it is what stopped an "X2andX3" destination from ever yielding
// an X2 endpoint: one destination serving both interfaces is one record here and two
// endpoints at the point of delivery.
type heldDestination struct {
	Address      string // "host:port", the form the X2/X3 senders dial
	DeliveryType string // X2Only | X3Only | X2andX3
	FriendlyName string // the ADMF's own name for it, where one was given
	// Configured marks an entry declared in this element's configuration rather than
	// provisioned over X1.
	Configured bool
}

// endpoints expands a destination into the delivery endpoints it serves.
//
// did is the identifier the destination was resolved by, and it is carried onto
// every endpoint, so a point of interception reporting a delivery fault can say *which*
// destination it concerns: the endpoint is the domain model every POI works in, and an
// identifier dropped at this boundary is absent from all of them.
func (d heldDestination) endpoints(did string) []types.DeliveryEndpoint {
	switch d.DeliveryType {
	case deliveryX2Only:
		return []types.DeliveryEndpoint{{Type: types.DeliveryX2, Address: d.Address, DID: did}}
	case deliveryX3Only:
		return []types.DeliveryEndpoint{{Type: types.DeliveryX3, Address: d.Address, DID: did}}
	case deliveryX2andX3:
		return []types.DeliveryEndpoint{
			{Type: types.DeliveryX2, Address: d.Address, DID: did},
			{Type: types.DeliveryX3, Address: d.Address, DID: did},
		}
	default:
		// Unreachable: a destination is only stored once deliveryProducts has accepted
		// its type. Returning nothing rather than guessing keeps an unknown type from
		// becoming a delivery to the wrong interface.
		return nil
	}
}

// Option customises a Server.
type Option func(*Server)

// PurgeReason says why tasking was removed. It exists because the removals differ
// in what they mean to an operator, not in what they do to the element.
type PurgeReason int

const (
	// PurgeWithdrawal: an explicit DeactivateTask. The expected end of an
	// interception.
	PurgeWithdrawal PurgeReason = iota
	// PurgeBulkDeactivate: DeactivateAllTasks. Expected too — a provisioning
	// function clearing this element deliberately.
	PurgeBulkDeactivate
	// PurgeKeepaliveLapse: the fail-safe, because the controlling function stopped
	// answering. Nothing asked for this, and it is the only one of the three an
	// operator must investigate.
	PurgeKeepaliveLapse
)

// OnTaskChange registers the lifecycle callback: one event per transition of this
// element's tasking, carrying the task as it was and as it becomes.
//
//	(nil, task)  activation
//	(prev, next) modification, or an activation replacing an XID already held
//	(task, nil)  removal — deactivation, bulk deactivation, or fail-safe purge
//
// One event carries both tasks because a modification keeps the XID: as two events,
// "remove the old task" and "install the new one" share a key, and a POI that removed
// after installing would reclaim what it had just created.
//
// The POI decides what changed. This package does not guess from the target
// identifiers alone -- a task's products can change with its targets untouched, and
// every guess of that kind is another field somebody forgot.
//
// An exact replay is not a transition and fires nothing: re-provisioning is how a
// provisioning function restores tasking after a restart, and it must not re-emit
// records that report the beginning of an interception that never stopped.
//
// The callback runs synchronously on the X1 request goroutine, under the lock that
// serialises every task transition, so it must not block and must not call back into the
// Server -- that would deadlock. Both pointers are to values owned by the caller for the
// duration of the call; a POI keeping either must copy it.
func OnTaskChange(fn func(prev, next *types.InterceptTask)) Option {
	return func(s *Server) { s.onTaskChange = fn }
}

// OnPurge registers a callback run after tasking has been removed, naming why.
//
// It carries no instruction — the removal itself travels on OnTaskChange, and has
// already happened by the time this runs. It exists so that an element reporting
// "my tasking was purged" can say whether anybody asked for it. An element that
// reports every withdrawal as a fail-safe purge teaches its operator to ignore the
// channel, and the report that matters then arrives into that habit.
//
// It runs under the same transition lock as OnTaskChange: it must not block -- a
// synchronous report over the network belongs on a goroutine -- and must not call back
// into the Server.
func OnPurge(fn func(types.InterceptTask, PurgeReason)) Option {
	return func(s *Server) { s.onPurge = fn }
}

// CanApply registers a check run before a task is stored or acknowledged. An
// error refuses the activation or modification, with the error's text as the
// description the requesting function receives.
//
// It exists for the things only the POI can answer. A triggered CC-POI is given
// detection criteria it may be unable to evaluate — an identifier naming state its
// datapath does not hold — and accepting one would leave the triggering function
// believing an interception is running that can never produce anything. That is
// undiscoverable from the outside, which is why the answer has to come before the
// acknowledgement rather than from a callback after it.
//
// The check runs synchronously on the X1 request goroutine, under the transition lock, so
// it must not block and must not call back into the Server. It must not apply anything
// either: a task it approves may still be refused afterwards for a reason this package
// owns.
func CanApply(fn func(types.InterceptTask) error) Option {
	return func(s *Server) { s.canApply = fn }
}

// FaultProbe reports whether one fault condition currently holds. It returns nil when the
// condition does not hold, and the fault to report when it does.
//
// A probe is asked at the moment a provisioning function asks for the element's status, so it
// describes the present rather than the past. Whoever holds the knowledge owns the probe: only
// the content shipper knows whether its mediation function is reachable, and only the datapath
// knows whether it is currently losing copies.
type FaultProbe func() *X1Error

// NEFault is the fault a probe reports for one of the conditions in report.go.
//
// The condition leads the description exactly as it does in a pushed report, so an ADMF
// reads the same token whichever mechanism carried it and a probe cannot invent a
// vocabulary of its own. detail says how much is wrong: it must name no target, warrant or
// destination, since the element's own status carries none of those and a probe written by
// whoever holds the knowledge is where that rule is easiest to break.
//
// The error code is the registry's generic one. TS 103 221-1's codes name failures of a
// *request*, and none of them names a condition of the element, so what an ADMF can act on
// here is the condition rather than a number picked to look specific.
//
// The condition must be one report.go declares at element scope. One that is not is replaced
// by a description saying so, as TaskFault does at task scope: a condition outside the shared
// vocabulary is one an ADMF cannot correlate with any report.
func NEFault(condition, detail string) *X1Error {
	if _, declared := neIssueEncodings[condition]; !declared {
		return &X1Error{
			ErrorCode: errCodeGeneric,
			ErrorDescription: "this element reported a condition it does not declare at " +
				"element scope, which is a defect in the element",
		}
	}

	return &X1Error{
		ErrorCode:        errCodeGeneric,
		ErrorDescription: condition + ": " + detail,
	}
}

// TaskFault is one condition an element reports against a task it holds, for a WithTaskFaults
// supplier to return.
//
// It exists so the code is chosen once rather than by each element: the TS 103 221-1
// non-terminating-fault code, the one a ReportNEIssue carries for a fault of this kind. The
// pushed ReportTaskIssue states a report type rather than a code, so the interrogation answer is
// where the code appears; the destination answers make the same choice.
//
// Non-terminating, because every condition an element can say this about leaves the task
// installed and the element running: what has stopped is the interception's product, not the
// element's willingness to carry it out. A terminating code would tell an ADMF to re-provision
// an element that has nothing wrong with its provisioning.
//
// The description is free text and is the only free-form field in the answer. It must not name
// the subject: the task is already identified by the answer the fault appears in, so a target
// identity here would be an identity on the X1 interface that the answer did not need. The
// supplier signature is the structural half of that — it is handed an XID and nothing else.
// The condition must be one report.go declares as task-scoped. A condition that is not is
// refused rather than sent: an element-scoped condition answered against a task is the defect
// this scoping exists to prevent, in the direction that is harder to notice — the answer looks
// like an answer.
func TaskFault(condition, detail string) X1Error {
	if !taskScoped[condition] {
		return X1Error{
			ErrorCode: issueCodeNonTerminatingFault,
			ErrorDescription: "this element reported a condition it does not declare as " +
				"task-scoped, which is a defect in the element rather than in the task",
		}
	}

	return X1Error{
		ErrorCode:        issueCodeNonTerminatingFault,
		ErrorDescription: condition + ": " + detail,
	}
}

// MDFUnreachableProbe returns the probe every POI registers: whether the mediation functions
// this element delivers to can be reached right now.
//
// count answers how many destinations are currently unreachable and how many are in use at all —
// x2x3.Pool.UnreachableAmong over the destinations in use answers it, and a POI keeping its own
// clients supplies the equivalent. It is called on the X1 request goroutine, so it must not
// perform I/O (see FaultProbe).
//
// It takes counts rather than the destinations themselves so that the answer *cannot* name
// one. An element's own status says how much is wrong, never whose product is affected, and
// making that structural is cheaper than remembering it at three call sites.
func MDFUnreachableProbe(count func() (unreachable, inUse int)) FaultProbe {
	return func() *X1Error {
		unreachable, inUse := count()
		if unreachable == 0 {
			return nil
		}

		return NEFault(NEIssueMDFUnreachable, fmt.Sprintf(
			"%d of %d delivery destination(s) unreachable", unreachable, inUse))
	}
}

// WithFaultProbes registers conditions the element can observe about itself, for the status a
// provisioning function can ask for.
//
// Registering none is a legitimate configuration and not a silent one: the element then answers
// that no observable condition holds, which is true. What it does *not* mean is that nothing has
// ever gone wrong — faults are pushed as they happen over ReportNEIssue, and the ones that are
// events rather than states cannot be re-observed later. The two mechanisms answer different
// questions, "what just went wrong" and "what is wrong now", and neither replaces the other.
func WithFaultProbes(probes ...FaultProbe) Option {
	return func(s *Server) { s.faultProbes = append(s.faultProbes, probes...) }
}

// faultsForTasks asks the element what is wrong with each task it is about to report.
//
// Returns nil when no supplier is registered: an element that supplies nothing answers with
// empty fault lists, not with an invented condition.
//
// The supplier is called outside the server's lock, once per task in the answer. A supplier
// that is slow makes an interrogation slow, which is the same trade WithFaultProbes takes and
// is stated there: the alternative is caching, and a cached fault is the stale answer this
// design exists to avoid.
func (s *Server) faultsForTasks(tasks []types.InterceptTask) map[types.XID][]X1Error {
	if s.taskFaults == nil || len(tasks) == 0 {
		return nil
	}

	out := make(map[types.XID][]X1Error, len(tasks))
	for _, t := range tasks {
		if f := s.taskFaults(t.XID); len(f) > 0 {
			out[t.XID] = f
		}
	}
	if len(out) == 0 {
		return nil
	}

	return out
}

// WithTaskFaults supplies the answer to "is there anything wrong with *that task*", for the
// task status a provisioning function can ask for.
//
// Without it, every task this element holds answers `provisioningStatus: complete` with an
// empty fault list — which is true of provisioning and silent about operation. Activation is
// what establishes that a task can be carried out, and nothing after that point revises the
// answer, so a task whose duplication the datapath has since refused, or whose session has
// gone, answers exactly as one producing product.
//
// **The party that cannot see anything else is a triggering function.** It tasks a point of
// interception over an interface the provisioning function cannot reach, so the POI's own
// answer is the only account of that interception available to it. Conditions of this kind
// *are* reported — at element scope, by the POI's issue reports — so what is missing is not
// the information but the attribution: `triggerFaulty` exists to raise one of these against
// one warrant, and cannot be raised by a function that cannot tell which warrant a fault
// concerned.
//
// **Asked, not stored**, which is the same choice WithFaultProbes and WithDestinationReachability
// make, and for the same reason: a recorded fault goes stale, needs an expiry or an explicit
// clear, and gets reported after the condition has ended. Every condition here is re-observable
// from state the element already holds, so the honest answer is computed when the question is
// asked.
//
// The supplier is keyed by XID and returns codes and descriptions only. It cannot be handed a
// target identity because it is never given one: a task-scoped answer already names the
// warrant, and adding the subject would put a target identifier into an answer that does not
// need one.
//
// It is called without the server's lock and may be called concurrently. Returning nil is the
// answer "nothing is wrong with this task", which is what an element that supplies no faults
// says for every task.
func WithTaskFaults(faults func(types.XID) []X1Error) Option {
	return func(s *Server) { s.taskFaults = faults }
}

// WithDestinationReachability supplies the answer to "can this element deliver to that
// address right now", for the destination status a provisioning function can ask for.
//
// Without it, GetDestinationDetails and GetAllDestinationDetails answer activeAndWorking for
// every destination, while the same element may be reporting the same endpoint as
// unreachable over ReportDestinationIssue. Two answers about one fact, from one element,
// would then disagree: the pushed report says the product is not arriving and the
// interrogation says it is. An ADMF reconciling them would trust the interrogation, because
// asking is how it checks.
//
// The function is the delivery layer's own — the same `unreachable` a network function
// already supplies to DestinationHealthOf — so the two answers come from one source and
// cannot diverge. Passed in rather than derived here, because this package holds no
// connections: only the element that delivers knows whether delivery is working.
//
// It is called without the server's lock and may be called concurrently.
func WithDestinationReachability(unreachable func(addr string) bool) Option {
	return func(s *Server) { s.destinationReachable = unreachable }
}

// WithoutDeactivateAllTasks refuses bulk deactivation, which TS 103 221-1 otherwise requires
// an element to perform by default.
//
// Named for the non-default so that the default is visible by its absence: an element with no
// option set will stop every interception on one authenticated message, because the
// specification says it must.
func WithoutDeactivateAllTasks() Option {
	return func(s *Server) { s.deactivateAllDisabled = true }
}

// WithoutRemoveAllDestinations refuses bulk destination removal, which TS 103 221-1 clause
// 6.3.4 otherwise requires an element to perform by default. Named for the non-default, as
// WithoutDeactivateAllTasks is.
func WithoutRemoveAllDestinations() Option {
	return func(s *Server) { s.removeAllDestinationsDisabled = true }
}

// BulkOptions turns a deployment's expressed policy on the two bulk operations into the
// options that carry it, so a network function reads its configuration and passes the
// result rather than deciding for itself what an unset value means.
//
// A nil value is "no agreement in advance" — the specification's own phrase for the state
// its defaults are stated against — and yields no option, leaving the default this package
// holds. A non-nil value that already matches the default likewise yields no option, since
// the options express deviations.
//
// It exists because three network functions would otherwise each write the same pair of
// conditions. A single inverted condition in one of them changes what one element does about
// a destructive operation, which is the least likely difference to be noticed and the worst
// to have.
func BulkOptions(deactivateAllTasks, removeAllDestinations *bool) []Option {
	var opts []Option
	if deactivateAllTasks != nil && !*deactivateAllTasks {
		opts = append(opts, WithoutDeactivateAllTasks())
	}
	if removeAllDestinations != nil && !*removeAllDestinations {
		opts = append(opts, WithoutRemoveAllDestinations())
	}
	return opts
}

// HonoursCorrelationID declares that this element acts on a provisioned correlation
// value, so a task carrying one is accepted rather than refused.
//
// **Whether a field can be honoured is a property of the element, not only of the
// field**, and this is the field that forced that distinction. A content POI stamps the
// provisioned value on every unit it delivers and an LI_T3 trigger carries one
// mandatorily, so the UPF's CC-POI sets this. An information POI in a 5G core cannot:
// the correlation that joins its records to a session's content is the session's, one
// task covers many sessions, and a single provisioned value applied to all of them
// would join at the mediation function what the network keeps separate. So the AMF and
// SMF do not set it, and refuse the field instead.
//
// Refusing everywhere is not available either — it would refuse tasking an ADMF may
// legitimately send to several elements at once, which is the reasoning the recognised
// extension already follows. Neither uniform answer works, which is why the element's
// role is part of the test.
func HonoursCorrelationID() Option {
	return func(s *Server) { s.honoursCorrelation = true }
}

// RequireResolvableDIDs makes the server refuse, with 3040, a task that orders content
// delivery and names no destination.
//
// A task that names destinations is held to them at every element: a DID this element
// cannot resolve is refused with 2040, and an ordered product none of the named
// destinations serves with 3040. What remains is the task naming none, which by default
// is served by the element's configured endpoints — an IRI-POI's MDF2 address agreed out
// of band, the arrangement real ADMFs use. That default is wrong for a *triggered*
// CC-POI, which has no configured X3 endpoint: its triggering function has no other way
// to discover that the destination it provisioned has been lost (a restart, say), so an
// acknowledgement it cannot honour leaves content being dropped while the triggering
// function believes interception is running.
func RequireResolvableDIDs() Option {
	return func(s *Server) { s.requireDIDs = true }
}

// OnAuthFailure registers a callback run when a peer fails TS 103 221-1 clause
// 8.2.4 authentication — presenting a certificate the LI CA issued, but asserting
// an identity it is not bound to, or one this element does not answer to. It is
// given the X1 error code the peer was refused with.
//
// A POI wires this to its ADMF reporter. Without it an attack on the provisioning
// interface is refused correctly and then recorded nowhere, since this plane
// deliberately keeps out of operator logs. The callback runs synchronously on the
// X1 request goroutine, so it must not block.
func OnAuthFailure(fn func(code int)) Option {
	return func(s *Server) { s.onAuthFailure = fn }
}

// WithADMF names the ADMF responsible for this network element. When set, a
// request asserting a different ADMF identifier is refused even if that
// identifier is properly bound into the peer's certificate — certification by
// the LI CA is not by itself authority to task this NE (TS 103 221-1 error 1040).
//
// **Unset, or set to "", it accepts any peer the LI CA certified as the ADMF.** The
// clause 8.2.4 binding still holds — the identifier a peer asserts must be bound into
// its own certificate — but any identifier will do. That includes another network
// element's: a UPF's or an SMF's certificate binds its own identifier by Subject UID
// exactly as an ADMF's does, and the binding carries no role, so without this option
// any element holding an LI certificate can create destinations on this one, task it,
// or deactivate all of its tasking. Set it wherever the responsible ADMF is known.
func WithADMF(admfID string) Option {
	return func(s *Server) { s.admfID = admfID }
}

// ConfiguredDestination is a DID→endpoint mapping declared in a network function's own
// configuration, for destinations an ADMF and an operator agreed out of band.
//
// DID must be the UUID the schema defines, DeliveryType one of X2Only, X3Only and
// X2andX3, and Address a "host:port" — the same three things CreateDestination carries,
// because the entry has to resolve identically to a provisioned one.
type ConfiguredDestination struct {
	DID          string
	DeliveryType string
	Address      string
}

// Valid reports whether this entry can be resolved, or why it cannot.
//
// Exported because a POI has somewhere to report a rejected entry and this package does
// not: an unusable mapping means a task naming that DID cannot be delivered, and the
// operator who declared it would not otherwise learn why. The option below drops such an
// entry; the network function that supplied it tells the ADMF over X1.
func (d ConfiguredDestination) Valid() error {
	if err := validIdentifier("did", d.DID); err != nil {
		return err
	}
	if _, err := deliveryProducts(d.DeliveryType); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(d.Address)
	if err != nil {
		return fmt.Errorf("address is not host:port")
	}
	// **SplitHostPort accepts a host that is not an address**, so the host and port are
	// checked as values: an entry that resolves to a destination nothing can dial is
	// dropped here, rather than refusing, later, a task for an identifier the operator
	// believes they supplied.
	//
	// Looser than the provisioned path on purpose. Operator configuration is accepted in
	// any form the dialler accepts -- a zone, an IPv4-mapped address, a port with leading
	// zeros -- and is dialled as written; only the details answers render it in the
	// schema's spelling (see deliveryAddress). A provisioned address is a peer's message
	// against a schema type, and is refused outside it.
	if _, err := netip.ParseAddr(host); err != nil {
		return fmt.Errorf("address host %q is not an IP address", host)
	}
	// 0 and 1 are outside the TCPPort type the destination answers render the port as, and
	// X2 and X3 are dialled over TCP, so neither names an endpoint this element can deliver to.
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n <= 1 {
		return fmt.Errorf("address port %q is not a port number this element can deliver to", port)
	}

	return nil
}

// WithConfiguredDestinations declares destinations this element can resolve without
// their having been provisioned over X1.
//
// TS 33.128 requires that a task *name* its delivery endpoints and that the element
// deliver to what it named; neither it nor TS 103 221-1 requires that the element
// learned the mapping over X1 rather than by agreement. So an ADMF referencing
// pre-shared destinations is conformant, and refusing it would be wrong.
//
// A destination provisioned over X1 under the same DID takes precedence, and the
// element says so in what it reports about its destinations — a configured entry
// silently superseded is the one outcome this must not have.
//
// A malformed entry is dropped rather than stored: it is operator configuration, not a
// peer's message, so there is nobody to refuse, and storing it would resolve a task's
// destination to an address nothing can dial. Dropping it is not the same as ignoring it —
// see ConfiguredDestination.Valid, which the caller uses to tell the ADMF.
func WithConfiguredDestinations(dests ...ConfiguredDestination) Option {
	return func(s *Server) {
		for _, d := range dests {
			if d.Valid() != nil {
				continue
			}
			s.configured[d.DID] = heldDestination{
				Address:      d.Address,
				DeliveryType: d.DeliveryType,
				Configured:   true,
			}
		}
	}
}

// NewServer returns an X1 Server backed by s, identifying itself as neID.
func NewServer(s *store.Store, neID string, opts ...Option) *Server {
	srv := &Server{
		store:        s,
		neID:         neID,
		now:          monotonicNow,
		destinations: make(map[string]heldDestination),
		configured:   make(map[string]heldDestination),
	}
	for _, opt := range opts {
		opt(srv)
	}
	return srv
}

// ServeHTTP serves the X1 interface over HTTP, as TS 103 221-1 clause 7.2 binds it: one
// X1Request per POST, answered with an X1Response, or with an X1TopLevelErrorResponse when
// the request cannot be parsed. HTTP status codes report HTTP-level errors only (clause
// 7.2.2.2): a method other than POST, a body larger than maxRequestBytes, or a body that
// cannot be read.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "X1 requires POST", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		// A body over the limit is refused whole rather than truncated: parsing a prefix
		// would answer a request the peer did not send.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "X1 request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	// The peer certificate authenticates the request beyond the handshake
	// (TS 103 221-1 clause 8.2.4). It counts only once the handshake has verified it:
	// NewListener requires that, but this handler can be mounted on any server, and under
	// RequestClientCert or RequireAnyClientCert a certificate is presented and never
	// verified, so binding to it would bind to whatever the peer claimed. An unverified one
	// is treated as none, and every message is refused as unauthenticated.
	var peer *x509.Certificate
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 && len(r.TLS.VerifiedChains) > 0 {
		peer = r.TLS.PeerCertificates[0]
	}
	resp, err := s.Process(body, peer)
	if err != nil {
		// TS 103 221-1 clause 6.1: "If the X1 Request could not be parsed, then the
		// response shall be constructed with an ADMF and NE Identifier …,
		// MessageTimestamp and Version, and a 'TopLevelError' flag but no other
		// information." The schema defines the element for it, so a conformant ADMF
		// has a structured answer to expect.
		//
		// Clause 7.2.2.2 settles the status code: "HTTP error codes shall only be used to
		// indicate HTTP-level errors, and shall not be used to indicate errors with the X1
		// responses themselves." A request that arrived intact and could not be parsed is
		// an X1-level error, so it is a 200 carrying the defined response, and the
		// decoder's own message never reaches the wire.
		w.Header().Set("Content-Type", "application/xml")
		//nolint:errcheck // a peer that hung up mid-response is not actionable, and must not be logged
		_, _ = w.Write(s.topLevelError(peer))

		return
	}
	out, err := marshalResponse(resp)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	//nolint:errcheck // a peer that hung up mid-response is not actionable, and must not be logged
	_, _ = w.Write(out)
}

// topLevelErrorTemplate is the clause 6.1 answer to a request that could not be
// parsed. It is a different root element from X1Response — the schema declares
// X1TopLevelErrorResponse separately — and carries exactly four fields. Notably it
// has no x1TransactionId: table 6.1-1 makes that field conditional and says it
// "shall be omitted for 'TopLevelError' situations", which is consistent, since
// the identifier would have had to come from the request nobody could read.
var topLevelErrorTemplate = template.Must(template.New("x1tle").Funcs(x1TemplateFuncs).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1TopLevelErrorResponse xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10">
  <ns1:admfIdentifier>{{esc .AdmfID}}</ns1:admfIdentifier>
  <ns1:neIdentifier>{{esc .NeID}}</ns1:neIdentifier>
  <ns1:messageTimestamp>{{esc .Timestamp}}</ns1:messageTimestamp>
  <ns1:version>{{esc .Version}}</ns1:version>
</ns1:X1TopLevelErrorResponse>`))

// topLevelError renders the clause 6.1 response.
//
// The ADMF identifier cannot come from the request, so it comes from
// configuration — or, where this element has no configured ADMF, from the peer's
// certificate, which is what clause 6.1's "extracting the identifier of the
// Requester from the X.509 certificate if necessary" provides for. An empty value
// is schema-valid but names nobody, and an ADMF matching answers to itself by this
// identifier could not attribute the one message that says its request was unreadable.
func (s *Server) topLevelError(peer *x509.Certificate) []byte {
	admf := s.admfID
	if admf == "" {
		// Both binding forms, not just the Subject UID. certBinds accepts either that
		// or an annex G binding URI when authenticating, so a peer identified solely by
		// the second is one this element has accepted and must also be able to name.
		admf = certIdentifier(peer, roleADMF)
	}

	var body bytes.Buffer
	if err := topLevelErrorTemplate.Execute(&body, struct {
		AdmfID, NeID, Timestamp, Version string
	}{
		AdmfID:    admf,
		NeID:      s.neID,
		Timestamp: x1Timestamp(s.now()),
		Version:   supportedVersion,
	}); err != nil {
		// Executing a template over four strings cannot fail; returning an empty body
		// is still better than panicking on the provisioning path.
		return nil
	}

	return body.Bytes()
}

// Process parses an X1 request body, applies each message to the store, and
// returns the response envelope. peer is the certificate the requester presented
// (nil if none), against which each message is authenticated. Exposed for testing
// without HTTP.
//
// A request carrying no message is refused as unparseable. The schema requires at least
// one, and so does an answer: an X1Response with no message would not validate, so the
// clause 6.1 top-level error is the only well-formed reply.
//
// So is a request that is not well-formed XML — an element carrying one attribute twice
// included. A typed field outside its type is not that: the integers and booleans are decoded
// as text and refused with 1010 against the one message that carried them (see strict.go).
func (s *Server) Process(body []byte, peer *x509.Certificate) (*X1Response, error) {
	var req X1Request
	if err := xml.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("x1: malformed request: %w", err)
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("x1: request carries no message")
	}
	resp := &X1Response{}
	projected := 0
	for _, m := range req.Messages {
		m.collapseTokens()
		rm := s.applyAuthenticated(m, peer, projected)
		projected += projectedEntries(rm)
		resp.Messages = append(resp.Messages, rm)
	}

	return resp, nil
}

// maxProjectedEntries bounds what the answers in one container may project of this element's
// state: the tasks, destinations and faults they list, counted across every message in it.
//
// maxRequestBytes bounds a request, and that is not a bound on its answer. An interrogation is
// a few hundred bytes and its answer is the whole of what the element holds, so one container
// of a few thousand GetAllDetails against an element holding a few thousand tasks asks for
// gigabytes — built in memory, every task's faults computed once per message, before a byte
// is written. Any authenticated peer can send one.
//
// So interrogations are answered until the container's answers have projected this many
// entries, and each one after that is refused with 1000, naming the reason. The first is
// always answered whatever the element holds — an ADMF must be able to audit an element of any
// size — so the bound is this plus one projection of the state. Refused per message rather
// than by a top-level error, because the request was parsed and clause 6.1 reserves that for
// one that was not; and an ADMF that asked for the same state twice in one container loses
// nothing it can use by having the repeats refused.
const maxProjectedEntries = 4096

// projectedEntries counts the entries an answer projects of this element's state.
func projectedEntries(rm X1ResponseMessage) int {
	n := len(rm.Tasks) + len(rm.Destinations) + len(rm.Faults)
	for _, faults := range rm.TaskFaults {
		n += len(faults)
	}

	return n
}

// isInterrogation reports whether a request type asks what this element holds: the Get and
// List requests of clauses 6.4 and 6.8, whose answers maxProjectedEntries bounds.
func isInterrogation(msgType string) bool {
	t := localType(msgType)

	return strings.HasPrefix(t, "Get") || strings.HasPrefix(t, "List")
}

// applyAuthenticated authenticates a request message and, only if it passes,
// applies it. projected is how many entries the answers before it in the same container
// have projected — see maxProjectedEntries.
func (s *Server) applyAuthenticated(m X1RequestMessage, peer *x509.Certificate, projected int) X1ResponseMessage {
	if code, desc := s.authenticate(m, peer); code != 0 {
		// Someone reached this interface with a certificate from the LI CA and tried to
		// task or untask this element as somebody they are not. Refusing it is not
		// enough on its own: undetectability means we deliberately log nothing, so
		// without this the most security-relevant event this interface can witness
		// would leave no trace anywhere at all. Clause 6.5.4 anticipates that, listing
		// a current security issue on the NE among the reasons to report.
		//
		// Only the error code reaches the ADMF, never the identifier the peer asserted:
		// that is attacker-chosen text, and an LI management channel is the last place
		// to start echoing it. The Reporter throttles per condition, so a peer
		// hammering the interface produces one report per interval, not a flood — the
		// report is a signal to go and look, not an audit trail.
		if s.onAuthFailure != nil {
			s.onAuthFailure(code)
		}

		return X1ResponseMessage{
			Type:             errorResponse,
			AdmfIdentifier:   m.AdmfIdentifier,
			NeIdentifier:     s.neID,
			MessageTimestamp: x1Timestamp(s.now()),
			Version:          supportedVersion,
			X1TransactionID:  echoTransactionID(m.X1TransactionID),
			ErrorInformation: &X1Error{ErrorCode: code, ErrorDescription: desc},
			// The schema makes requestMessageType mandatory on an ErrorResponse. A refusal
			// that does not validate is a refusal a peer discards, so the type travels even
			// on the authentication path — where the request is refused before anything but
			// its type has been trusted.
			RequestType: localType(m.Type),
		}
	}
	// **Recorded before the message is applied, not after the batch.**
	//
	// An authenticated X1 message means the responsible ADMF is alive — this feeds the
	// keepalive watchdog (TS 103 221-1: the ADMF sends KeepaliveRequest at least every
	// TIME_P1; if they lapse the NE purges tasking). Unauthenticated traffic must not
	// reset it, or anyone able to reach the X1 port could hold the fail-safe open
	// indefinitely while the real ADMF is gone — which is why this is here rather than
	// on every message.
	//
	// Recorded *before* applying because of what a lapse purge does with it. The purge
	// re-reads lastSeen under the transition lock precisely so a recovery activation
	// acknowledged during the window is not purged by a tick already in flight; recorded
	// after the batch, the activation would release that lock with lastSeen still stale
	// and the tick would purge the tasking the ADMF has just been told is active.
	//
	// It is also the truer statement: the ADMF is alive because its message arrived, not
	// because this element finished acting on it.
	s.recordActivity()

	// Clause 4.5 keeps minor and patch increments backwards compatible, so any v1.x.y is
	// processed; another major version, or a value outside the schema's pattern, is a
	// message this element cannot read reliably. Table 6.7-3 has the code for it, and its
	// suggested content is the version the element supports.
	if !versionPattern.MatchString(m.Version) {
		return X1ResponseMessage{
			Type:             errorResponse,
			AdmfIdentifier:   m.AdmfIdentifier,
			NeIdentifier:     s.neID,
			MessageTimestamp: x1Timestamp(s.now()),
			Version:          supportedVersion,
			X1TransactionID:  echoTransactionID(m.X1TransactionID),
			ErrorInformation: &X1Error{
				ErrorCode:        errCodeUnsupportedVersion,
				ErrorDescription: "unsupported version; this element supports " + supportedVersion,
			},
			RequestType: localType(m.Type),
		}
	}

	if projected >= maxProjectedEntries && isInterrogation(m.Type) {
		// Refused before it is applied, so the state is not projected only to be dropped.
		return X1ResponseMessage{
			Type:             errorResponse,
			AdmfIdentifier:   m.AdmfIdentifier,
			NeIdentifier:     s.neID,
			MessageTimestamp: x1Timestamp(s.now()),
			Version:          supportedVersion,
			X1TransactionID:  echoTransactionID(m.X1TransactionID),
			ErrorInformation: &X1Error{
				ErrorCode: errCodeGeneric,
				ErrorDescription: "this container's answers have reached the size this element " +
					"returns at once; send this request in a container of its own",
			},
			RequestType: localType(m.Type),
		}
	}

	return s.apply(m)
}

// purgeAllTasking removes every task and runs the per-task teardown over what was there.
//
// The order matters, and is why this is one function rather than two callers each doing it: the
// store is cleared *first*, so a POI re-deriving against the task set during its own teardown
// sees an empty one. Clearing the store alone is not a purge — a CC-POI's duplication would keep
// running with its product going nowhere attributable — and running the hooks first would have
// each of them re-derive against tasks still present.
//
// Both the keepalive fail-safe and DeactivateAllTasks arrive here, which is what stops a bulk
// deactivation becoming a second, subtly different implementation of the same thing.
func (s *Server) purgeAllTasking(reason PurgeReason) {
	// Held across snapshot, clear and teardown. Without it a task activated between
	// the snapshot and the clear is removed with no teardown of its own — see
	// transitionMu.
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	s.purgeAllTaskingLocked(reason)
}

// purgeAllTaskingLocked is purgeAllTasking's body, for the caller that already holds the
// transition lock because its own decision had to be taken under it — the lapse purge,
// whose test of whether the ADMF has gone quiet must not be separated from acting on it.
//
// Caller holds s.transitionMu.
func (s *Server) purgeAllTaskingLocked(reason PurgeReason) {
	tasks := s.store.Snapshot()
	// nil in production. The window between the snapshot and the clear is a few
	// instructions wide, so a test holds it open here rather than racing two goroutines at
	// it -- see TestAnActivationRacingTheFailSafePurgeLeavesNoOrphan.
	if s.afterPurgeSnapshot != nil {
		s.afterPurgeSnapshot()
	}
	s.store.DeactivateAll()
	for _, t := range tasks {
		s.notifyRemoved(t, reason)
	}
}

// deactivateAll performs the bulk deactivation TS 103 221-1 requires every implementation to
// support: "If enabled, the DeactivateAllTasks command shall perform a 'DeactivateTask' command
// for all Tasks on the NE."
func (s *Server) deactivateAll() { s.purgeAllTasking(PurgeBulkDeactivate) }

// notifyChanged reports a successful activation or modification as the one
// transition it is.
//
// An exact replay fires nothing. An ADMF re-sending tasking it already sent — its
// restart-recovery path — must not make this element re-emit records that report
// the beginning of an interception, because none began, and must not tear down and
// rebuild state that was already correct.
//
// What changed is the POI's to work out. Deciding here, from the target identifiers
// alone, would make a change of products invisible: adding CC would never begin content
// interception for a target's existing sessions, and removing it would leave that
// interception running after the authority for it was gone.
func (s *Server) notifyChanged(prevTask types.InterceptTask, hadPrev bool, task types.InterceptTask) {
	if s.onTaskChange == nil || (hadPrev && sameTask(prevTask, task)) {
		return
	}

	var prev *types.InterceptTask
	if hadPrev {
		prev = &prevTask
	}
	s.onTaskChange(prev, &task)
}

// sameTask reports whether two tasks are the same in every field a POI could act
// on. Used to recognise a replay, where anything else is a transition and is the
// POI's to interpret, and by the task watcher to tell the task a fault was observed
// on from what a ModifyTask has made of it since. A task deactivated and activated
// again identically compares equal; the watcher asks the reporter about that.
func sameTask(a, b types.InterceptTask) bool {
	return reflect.DeepEqual(a, b)
}

// sameRequest reports whether two tasks were requested identically: every field the
// ActivateTask carried, which excludes the delivery endpoints its DIDs resolve to. Those are
// this element's resolution, not the request, so a replay is still the same request if a
// destination it names has been re-provisioned in between.
func sameRequest(a, b types.InterceptTask) bool {
	a.Deliveries, b.Deliveries = nil, nil

	return reflect.DeepEqual(a, b)
}

// notifyRemoved runs a removal's teardown and then names why it happened. The
// order is the contract: what is being reported is that interception stopped, so
// it must have stopped.
func (s *Server) notifyRemoved(task types.InterceptTask, reason PurgeReason) {
	if s.onTaskChange != nil {
		s.onTaskChange(&task, nil)
	}
	if s.onPurge != nil {
		s.onPurge(task, reason)
	}

	s.mu.Lock()
	reporter := s.taskFaultReporter
	s.mu.Unlock()
	reporter.forgetTask(string(task.XID))
}

// destinationsInUse counts the destinations some task still references.
//
// The specification refuses a bulk removal while any is referenced, and the reason is worth
// holding on to: a task whose destination has gone is a task producing product with nowhere to
// send it — the failure RequireResolvableDIDs exists to prevent, reached by removing the
// destination rather than by never providing it.
func (s *Server) destinationsInUse() int {
	referenced := make(map[string]struct{})
	for _, t := range s.store.Snapshot() {
		for _, did := range t.DIDs {
			referenced[did] = struct{}{}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for did := range s.destinations {
		if _, ok := referenced[did]; ok {
			n++
		}
	}

	return n
}

// removeAllDestinations empties the provisioned destination store. Only reachable when
// the operation is not disabled and nothing references a destination.
//
// Configured entries are untouched. They are the operator's, not the ADMF's: the
// specification's bulk removal is over "all Destinations on the NE" in the sense of
// those an ADMF created, and an X1 message that could delete an element's own
// configuration would be a different and much larger power.
//
// The in-use count and the removal are taken together, under the transition lock, for
// the same reason createDestination takes it: the guard is decided from task state, and a
// task activated between the count and the clear would be left referencing a destination
// this element has just deleted — producing product with nowhere to send it, which is the
// failure RequireResolvableDIDs exists to prevent, reached by removing the destination
// rather than by never providing it.
func (s *Server) removeAllDestinations() (int, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	if n := s.destinationsInUse(); n > 0 {
		// "Since a RemoveDestination request can only be issued against destinations that
		// are not in use, an NE shall respond with an error if the ADMF sends a
		// RemoveAllDestinations request while any of the Destinations are referenced by
		// Tasks."
		return errCodeDestinationsInUse, fmt.Errorf("%d destination(s) are referenced by tasks", n)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.destinations = make(map[string]heldDestination)

	return 0, nil
}

// applyTaskTransition performs an activation or a modification as one indivisible
// step: read the task this element currently holds, replace it in the store, and tell
// the point of interception — with no other task operation, bulk deactivation or
// fail-safe purge able to interleave.
//
// net/http gives each request a goroutine, so without one shared lock the three steps
// interleave with other requests. See transitionMu for what that would let through, and
// why the notification is issued from inside the operation rather than after it.
func (s *Server) applyTaskTransition(m X1RequestMessage, isModify bool) (int, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	var (
		err  error
		code int // TS 103 221-1 error code; 0 means "pick the generic one"
	)

	// The task already held under this XID, if any, decides two cases below: an
	// activation naming a held XID is a replay or a conflict, and a modification of a
	// task that is not held is refused. A modification carries what it replaces to the
	// POI, which has no other way to take down the state it applied for the old task.
	var prevTask types.InterceptTask
	var hadPrev bool
	if m.TaskDetails != nil {
		prevTask, hadPrev = s.store.Get(types.XID(m.TaskDetails.XID))
	}
	// Two questions answered before the switch, because each one picks a case, and
	// answered here rather than inside taskFromDetails because the code for a task
	// refusal differs between an activation and a modification.
	//
	// They are separate questions and stay separate: a value that violates the format
	// its schema defines is malformed, which is a general message error, while a field
	// this element cannot honour is a well-formed instruction it must refuse. The
	// registry orders them the same way, and so does the switch.
	var malformed, unhonourable error
	if m.TaskDetails != nil {
		malformed = malformedTaskIdentifiers(*m.TaskDetails)
		unhonourable = unhonourableTaskFields(*m.TaskDetails, s.honoursCorrelation)
	}
	switch {
	case isModify && m.TaskDetails == nil:
		// taskDetails is mandatory in a ModifyTaskRequest, so this is the message
		// violating its own schema: 1010, not the generic 1000.
		err = fmt.Errorf("missing taskDetails")
		code = errCodeSchemaError
	case malformed != nil:
		// Before the "no such task" check below, deliberately. Answered there, a ModifyTask
		// naming a malformed or absent xId would get 2020, "XID does not exist on NE" --
		// true, but an accident of ordering, and it points the ADMF at activating a task
		// that would fail for the same reason. 1010 names the actual fault, and
		// "implementers shall use the most specific error code available".
		err = malformed
		code = errorCode(err)
	case unhonourable != nil:
		err = unhonourable
		code = errorCode(err)
		if code == 0 {
			code = taskFailureCode(isModify)
		}
	case isModify && !hadPrev:
		// Applying this would silently create the task, leaving the ADMF believing
		// it had adjusted an interception that never existed here — the same class
		// of undetected divergence as tasking lost to a restart. Answering
		// "no such task" is what lets it activate the warrant instead.
		err = fmt.Errorf("no such task")
		code = errCodeNoSuchTask
	case !isModify && hadPrev:
		code, err = s.activateHeld(*m.TaskDetails, prevTask)
	default:
		var task types.InterceptTask
		task, err = s.activate(m)
		// A refusal from the activate path may name its own code — a malformed
		// identifier is a schema error, not a generic failure — so it travels
		// rather than being flattened below.
		code = errorCode(err)
		if err != nil && code == 0 {
			// What is left is a refusal whose reason is in the description: a POI's
			// CanApply saying it cannot carry out this task, or a field of the task
			// details this element will not accept. 3000/3001 are the registry's own
			// "details of why the Task cannot be activated/modified", which is what
			// that is; 1000 says only that something went wrong, and an ADMF reads
			// the code before the text.
			code = taskFailureCode(isModify)
		}
		if err == nil {
			s.notifyChanged(prevTask, hadPrev, task)
		}
	}

	return code, err
}

func (s *Server) apply(m X1RequestMessage) X1ResponseMessage {
	rm := X1ResponseMessage{
		AdmfIdentifier:   m.AdmfIdentifier,
		NeIdentifier:     s.neID,
		MessageTimestamp: x1Timestamp(s.now()),
		Version:          supportedVersion,
		X1TransactionID:  echoTransactionID(m.X1TransactionID),
	}

	if serr := m.shape.err(localType(m.Type)); serr != nil {
		// A header or body element the message carries twice: the decode kept one of them,
		// and acting on it would be acting on the decoder's choice. See strict.go.
		rm.Type = errorResponse
		rm.RequestType = localType(m.Type)
		rm.ErrorInformation = &X1Error{ErrorCode: errCodeSchemaError, ErrorDescription: serr.Error()}

		return rm
	}

	var err error
	var code int // TS 103 221-1 error code; 0 means "pick the generic one"
	switch localType(m.Type) {
	case "ActivateTaskRequest", "ModifyTaskRequest":
		code, err = s.applyTaskTransition(m, localType(m.Type) == "ModifyTaskRequest")
		rm.Type = strings.Replace(localType(m.Type), "Request", "Response", 1)
	case "DeactivateTaskRequest":
		code, err = s.deactivate(m.XID)
		if err != nil && code == 0 {
			code = errorCode(err)
		}
		rm.Type = "DeactivateTaskResponse"
	case "DeactivateAllTasksRequest":
		if s.deactivateAllDisabled {
			// The specification's own text, verbatim, because an ADMF may match on it.
			err = fmt.Errorf("DeactivateAllTasks message is not enabled")
			code = errCodeDeactAllOff
		} else {
			s.deactivateAll()
		}
		rm.Type = "DeactivateAllTasksResponse"
	case "RemoveAllDestinationsRequest":
		if s.removeAllDestinationsDisabled {
			// Verbatim again — and note the specification says "request" here where it says
			// "message" above. That asymmetry is its own, and is preserved rather than
			// tidied, because tidying it would break a peer matching the published string.
			err = fmt.Errorf("RemoveAllDestinations request is not enabled")
			code = errCodeRemoveAllOff
		} else {
			// The in-use guard is inside removeAllDestinations, so it is evaluated
			// atomically with the removal rather than a few instructions before it.
			code, err = s.removeAllDestinations()
		}
		rm.Type = "RemoveAllDestinationsResponse"
	case "GetAllTaskDetailsRequest":
		// One of the interrogation set TS 103 221-1 clause 6.4.1 requires every
		// implementation to support. It projects the same task state GetAllDetails does,
		// through the same renderer, so the two answers cannot disagree about what this
		// element holds — an ADMF that got different answers could not tell which to trust.
		rm.Tasks = s.store.Snapshot()
		rm.Type = "GetAllTaskDetailsResponse"
	case "GetAllDestinationDetailsRequest":
		rm.Destinations = s.heldDestinations()
		rm.Type = "GetAllDestinationDetailsResponse"
	case "ListAllDetailsRequest":
		// Identifiers only. This is what an ADMF reaches for after being told an element has
		// lost its tasking: it needs the list before it can reconcile, and answering with an
		// empty one is a usable answer where an error is not.
		rm.Tasks = s.store.Snapshot()
		rm.Destinations = s.heldDestinations()
		rm.Type = "ListAllDetailsResponse"
	case "GetNEStatusRequest":
		rm.Faults = s.unresolvedFaults()
		rm.Type = "GetNEStatusResponse"
	case "GetDestinationDetailsRequest":
		if m.DID == "" {
			err = refuse(errCodeSchemaError, "missing dId")
			code = errCodeSchemaError
		} else if d, found := s.destinationByDID(m.DID); found {
			rm.Destinations = []ReportedDestination{d}
		} else {
			// A destination the element does not hold is an error, not an empty answer: an
			// empty success would tell the ADMF the destination exists and is blank.
			err = fmt.Errorf("no such destination")
			code = errCodeNoSuchDID
		}
		rm.Type = "GetDestinationDetailsResponse"
	case "GetTaskDetailsRequest", "GetAllDetailsRequest":
		// A provisioning function has no other way to discover that this element has
		// lost the tasking it was given — a restart discards it, and nothing pushes
		// that fact anywhere. Answering these is what lets an ADMF audit and
		// re-provision instead of believing an interception is running that is not
		// — which TS 103 221-1 also requires.
		if localType(m.Type) == "GetAllDetailsRequest" {
			rm.Tasks = s.store.Snapshot()
			rm.Destinations = s.heldDestinations()
			rm.Faults = s.unresolvedFaults()
		} else if t, found := s.store.Get(types.XID(m.XID)); found {
			rm.Tasks = []types.InterceptTask{t}
		} else if m.XID == "" {
			// xId is mandatory on this request in the schema.
			err = refuse(errCodeSchemaError, "missing xId")
			code = errCodeSchemaError
		} else {
			// Reporting "no such task" is the whole point: it is the answer that
			// tells an ADMF its warrant is not in place here.
			err = fmt.Errorf("no such task")
			code = errCodeNoSuchTask
		}
		rm.Type = strings.Replace(localType(m.Type), "Request", "Response", 1)
	case "GetAllGenericObjectDetailsRequest":
		// Answered rather than refused, because clause 6.4.1 lists it among the requests that
		// "shall be supported" with no qualifier — unlike DeleteAllObjects below, which is
		// required only "if the implementation supports Generic Objects".
		//
		// The answer omits listOfGenericObjectResponseDetails, which is what the specification
		// defines for an element in this position: "May be omitted if Generic Objects are not
		// supported by the NE". The same sentence governs the object lists inside GetAllDetails
		// and ListAllDetails, which this element also omits, so all three answers say the
		// same thing about Generic Objects. An *empty* list would be a different and false
		// claim — that they are implemented and none is held.
		rm.Type = "GetAllGenericObjectDetailsResponse"
	case "CreateObjectRequest", "ModifyObjectRequest", "GetObjectRequest",
		"DeleteObjectRequest", "ListObjectsOfTypeRequest", "DeleteAllObjectsRequest":
		// The clause 6.8 object CRUD, which is conditional where the query above is not:
		// "The DeleteAllObjects request shall be supported if the implementation supports
		// Generic Objects", and an element that cannot store an object "shall reject the
		// CreateObjectRequest with an appropriate error response".
		//
		// Refused explicitly rather than by falling through to the unknown-type case, so that
		// the difference between this and the query above is stated where it is decided. An
		// acknowledgement would tell a provisioning function its object had been stored.
		err = fmt.Errorf("this NE does not support Generic Objects")
		code = errCodeUnsupportedRequest
	case "CreateDestinationRequest":
		code, err = s.createDestination(m.DestinationDetails)
		if err != nil && code == 0 {
			code = errorCode(err)
		}
		if err != nil && code == 0 {
			// What remains is a destination this element declined to create for a reason
			// the description gives. 6000 is the registry's "details of why the
			// Destination cannot be created" — the destination equivalent of 3000 — where
			// 1000 says only that something went wrong.
			code = errCodeCreateDestFailed
		}
		rm.Type = "CreateDestinationResponse"
	case "KeepaliveRequest":
		// Liveness from the ADMF (TS 103 221-1). Process already recorded the
		// activity that resets the watchdog; just acknowledge.
		rm.Type = "KeepaliveResponse"
	case "PingRequest":
		rm.Type = "PingResponse"
	default:
		err = fmt.Errorf("unsupported request type %q", localType(m.Type))
		code = errCodeUnsupportedRequest
	}

	// Every answer that reports a task reports its faults, filled here rather than at the four
	// sites that set rm.Tasks — the same argument GetAllTaskDetails makes about projecting one
	// task state through one renderer. An ADMF that asked two ways and got two answers could
	// not tell which to trust, and a site added later inherits this instead of remembering it.
	//
	// Only for the answers that render them, though. ListAllDetails names its tasks by XID
	// and nothing more, and the supplier may walk every session this element holds, once per
	// task — work done outside any lock and then thrown away.
	if rendersTaskFaults(rm.Type) {
		rm.TaskFaults = s.faultsForTasks(rm.Tasks)
	}

	if err != nil {
		rm.Type = errorResponse
		rm.RequestType = localType(m.Type)
		if code == 0 {
			code = errCodeGeneric
		}
		rm.ErrorInformation = &X1Error{ErrorCode: code, ErrorDescription: err.Error()}
	} else {
		rm.OK = ackOK
	}
	return rm
}

// deactivate removes one task, or reports why it cannot.
//
// TS 103 221-1 table 6.2.3-2 is explicit about the last case: "it is an error if the XID is
// not already present at the NE" — the mirror of the CreateDestination rule this element
// already answers with 2030, and the reason 2020 exists.
//
// A deactivation is never acknowledged unconditionally. An ADMF withdrawing a warrant with
// a mistyped XID must not be told the withdrawal completed while the interception goes on
// running: interception outliving its authority is the one direction this plane must never
// fail in. So a malformed identifier is refused, and an XID this element does not hold is
// answered 2020 -- including a re-sent deactivation after a keepalive purge or a restart,
// which is how the ADMF learns the element was not holding the warrant it thought it was
// withdrawing.
func (s *Server) deactivate(xid string) (int, error) {
	if xid == "" {
		return 0, refuse(errCodeSchemaError, "missing xId")
	}
	if err := validIdentifier("xId", xid); err != nil {
		return 0, err
	}

	// The validation above needs no lock and takes none; from here the operation
	// touches this element's tasking, and read-remove-notify is one step.
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	// Read before removal so a POI can undo state it applied for the target (e.g. clear
	// mid-session CC on the SMF), and so "was it held" is answered from the same read.
	task, existed := s.store.Get(types.XID(xid))
	if !existed {
		return errCodeNoSuchTask, fmt.Errorf("no such task")
	}
	s.store.Deactivate(types.XID(xid))
	s.notifyRemoved(task, PurgeWithdrawal)

	return 0, nil
}

// createDestination records a delivery destination against its DID. Re-creating
// an existing DID is an error per TS 103 221-1 clause 6.3.1.1 ("it is an error if the
// DID is already present at the NE"), so a misconfiguration cannot silently redirect an
// agency's product elsewhere.
//
// It returns the TS 103 221-1 error code alongside the reason, since the registry names
// two of these refusals exactly and "implementers shall use the most specific error code
// available". A zero code leaves the caller to pick the generic one.
func (s *Server) createDestination(d *DestinationDetails) (int, error) {
	// destinationDetails and its dId are mandatory in the schema, so a message without
	// them violates its own format: 1010, the code the package gives every schema error,
	// rather than the generic 1000.
	if d == nil {
		return errCodeSchemaError, fmt.Errorf("missing destinationDetails")
	}
	if err := d.malformedShape(); err != nil {
		return 0, err
	}
	if d.DID == "" {
		return errCodeSchemaError, fmt.Errorf("missing dId")
	}
	if err := validIdentifier("dId", d.DID); err != nil {
		// The schema types a DId as a TS 103 280 UUID, so a value outside that format
		// is a schema error rather than a destination-creation failure — 1010 names
		// what is actually wrong where 6000 would name only where it happened.
		//
		// Refusing it matters beyond tidiness: an element that stores a malformed
		// identifier interoperates with a provisioning function no conformant one would
		// produce, and its own test material stops being a guide to what a real ADMF
		// sends.
		return 0, err
	}
	if _, err := deliveryProducts(d.DeliveryType); err != nil {
		return 0, err
	}
	if err := unhonourableExtensions(d.Extensions, "destination"); err != nil {
		// No destination extension is recognised, so every one reaches this. The rule
		// is the task's: an extension exists to change the meaning of the message that
		// carries it, and on a destination that means changing where product goes.
		//
		// Uncoded, so the caller answers 6000: an extension the element does not
		// recognise is a well-formed message it cannot honour, not a schema error.
		return 0, err
	}
	dest, err := destinationFrom(*d)
	if err != nil {
		return 0, err
	}

	// **The guards below and the mutation are one step against the transitions they
	// race.** Both refusals are decided from state a *task* operation changes: whether
	// any task references the DID, which an activation or a deactivation moves. Asked
	// outside the transition lock, an activation naming this DID could land between the
	// question and the answer — so the element would create a destination under a
	// configured identifier a live task depends on, and every task activated before that
	// moment would keep delivering to the configured address while the element answered
	// with the new one. A provisioning function could read a destination back from an
	// element still sending a live warrant's product somewhere else.
	//
	// Lock order is the file's existing one: transition lock, then store, then s.mu. No
	// network I/O runs under it, and destinations are provisioned once per endpoint
	// before first use, so the latency this adds to X1 is bounded by a map write.
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	// Whether a task references the DID is answered from the task store, which has its
	// own lock, so it is asked before s.mu is taken.
	referenced := s.didReferenced(d.DID)

	// nil in production. The window between the reference check and the mutation is a
	// few instructions wide, so a test holds it open here rather than racing two
	// goroutines at it -- see TestACreateRacingAnActivationResolvesOneWayOrTheOther.
	if s.afterDestinationGuard != nil {
		s.afterDestinationGuard()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.destinations[d.DID]; exists {
		// The code, 2030, is what tells a peer re-provisioning after a restart that the
		// destination is already there rather than that something went wrong.
		return errCodeDIDExists, fmt.Errorf("destination already present")
	}
	if _, declared := s.configured[d.DID]; declared && referenced {
		// A provisioned destination wins over a configured one (see resolveLocked), but
		// a task's endpoints are resolved once at activation and copied into the task.
		// Creating this would therefore change what the element *answers* about the DID
		// while every task activated before this moment kept delivering to the
		// configured address — so a provisioning function could read the new destination
		// back from an element still sending a live warrant's product to the old one.
		//
		// Refused only where both hold. Creating under a configured DID nothing
		// references is how an operator's static declaration gets replaced before use,
		// and that stays available.
		return errCodeCreateDestFailed, fmt.Errorf(
			"dId %s is declared in this element's configuration and referenced by an active task", d.DID)
	}
	s.destinations[d.DID] = dest
	return 0, nil
}

// didReferenced reports whether any task this element holds names the DID.
func (s *Server) didReferenced(did string) bool {
	for _, t := range s.store.Snapshot() {
		if slices.Contains(t.DIDs, did) {
			return true
		}
	}

	return false
}

// destinationFrom maps provisioned destination details onto the destination this element
// holds: where the X2/X3 senders dial, and what it may be dialled for.
func destinationFrom(d DestinationDetails) (heldDestination, error) {
	if d.Address.IPAddressAndPort == nil {
		// A URI, E.164 number or email address. 6020 is the registry's own entry for
		// it, and says more than the generic code would: why it could not be created.
		return heldDestination{}, codedError{errCodeBadAddressType, fmt.Errorf("unsupported deliveryAddress")}
	}
	ap := d.Address.IPAddressAndPort

	// **The value, not only the field that carried it.** An address that is not an
	// address would be created, acknowledged and dialled forever, and a message populating
	// both port arms would leave this element choosing which one a warrant's product goes
	// to. Both are schema errors (1010): the types restrict them, and a value violating its
	// own type is malformed rather than something this element declines to honour.
	if err := ap.Address.Valid(); err != nil {
		return heldDestination{}, codedError{errCodeSchemaError, err}
	}
	if err := ap.Port.Valid(); err != nil {
		if errors.Is(err, errPortNamesNoEndpoint) {
			// Inside the type, so not malformed: a destination this element cannot create.
			return heldDestination{}, codedError{errCodeCreateDestFailed, err}
		}

		return heldDestination{}, codedError{errCodeSchemaError, err}
	}

	// Stored in the schema's own spelling, so the destination answers render what the
	// schema accepts, and so one endpoint has one address string wherever it is keyed.
	host := ap.Address.Canonical()
	if ap.Address.IPv4 == "" {
		// An IPv6 literal needs brackets before it can be joined to a port.
		host = "[" + host + "]"
	}
	port := ap.Port.Value()

	return heldDestination{
		Address:      host + ":" + strconv.FormatUint(uint64(port), 10),
		DeliveryType: d.DeliveryType,
		FriendlyName: d.FriendlyName,
	}, nil
}

// resolveLocked returns the destination a DID names, preferring one provisioned over X1
// to one declared in configuration. Caller holds s.mu.
func (s *Server) resolveLocked(did string) (heldDestination, bool) {
	if d, ok := s.destinations[did]; ok {
		return d, true
	}
	d, ok := s.configured[did]

	return d, ok
}

// resolveDIDs turns the DIDs a task references into delivery endpoints, and reports
// separately the identifiers it could not resolve.
//
// **The unresolved identifiers are what matter.** The endpoints alone cannot distinguish
// the two situations that produce a short list, and they need opposite answers: a DID that
// resolved to an X3-only destination contributes no X2 endpoint, which is correct and
// complete, while a DID this element cannot place at all is an instruction it cannot
// carry out. Without them, a task naming three agencies of which one is unknown would be
// stored with two, and provisioning would report success while an agency named in the
// warrant received nothing.
//
// The caller refuses such a task. An ADMF is still entitled to task an IRI-POI whose MDF2
// address comes from configuration: a task that names *no* DID falls back to the element's
// configured endpoint, and destinations agreed out of band are declared in the element's
// own configuration (WithConfiguredDestinations), where they resolve. What is refused is
// the case neither of those covers — a named identifier the element genuinely cannot
// place — because substituting an endpoint of its own is the element deciding where a
// warrant's product goes.
func (s *Server) resolveDIDs(dids []string) (endpoints []types.DeliveryEndpoint, unresolved []string) {
	if len(dids) == 0 {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, did := range dids {
		dest, ok := s.resolveLocked(did)
		if !ok {
			unresolved = append(unresolved, did)

			continue
		}
		endpoints = append(endpoints, dest.endpoints(did)...)
	}

	return endpoints, unresolved
}

// activateHeld answers an ActivateTask naming an XID this element already holds.
//
// Table 6.2.1.1-2 makes it an error: "it is an error if the XID is already present at
// the NE", and a different task under a held XID is refused with 2010 — a change to a
// held task is what ModifyTask is for, and replacing it silently would let an ADMF
// believe it had activated a warrant while it had overwritten another. A replay of the
// identical task is acknowledged instead, and changes nothing: an ADMF that resends an
// activation whose answer it lost is asking for what this element already does, and
// refusing it would turn a lost response into a provisioning failure. The comparison is
// of the request — every field the ActivateTask carried, its DIDs included but not the
// endpoints they resolve to — so a replay is identical whatever the x1TransactionId.
func (s *Server) activateHeld(td TaskDetails, held types.InterceptTask) (int, error) {
	task, err := s.taskFromDetails(td)
	if err != nil {
		code := errorCode(err)
		if code == 0 {
			code = taskFailureCode(false)
		}

		return code, err
	}
	// The store stamps the state; the held task carries it, so the replay must too.
	task.State = types.TaskActive
	if sameRequest(held, task) {
		return 0, nil
	}

	return errCodeXIDExists, fmt.Errorf("XID already exists on NE; use ModifyTask to change a held task")
}

func (s *Server) activate(m X1RequestMessage) (types.InterceptTask, error) {
	if m.TaskDetails == nil {
		// Mandatory in the schema, as in ModifyTask: a schema error, not a task this
		// element cannot apply.
		return types.InterceptTask{}, refuse(errCodeSchemaError, "missing taskDetails")
	}
	task, err := s.taskFromDetails(*m.TaskDetails)
	if err != nil {
		return types.InterceptTask{}, err
	}
	// Asked before the task is stored, so a refusal leaves nothing behind: a task in
	// the store is a task this element reports as active and acts on.
	if s.canApply != nil {
		if err := s.canApply(task); err != nil {
			return types.InterceptTask{}, err
		}
	}
	if !s.store.Activate(task) {
		return types.InterceptTask{}, fmt.Errorf("invalid task")
	}
	// Answer with the task as this element now holds it, not as it was built. The
	// store stamps the task state, so a caller comparing this against what it held
	// before — which is how a replay is told from a change — must be comparing two
	// values of the same provenance.
	//
	// The found bool is honoured rather than discarded. Under the transition lock a
	// concurrent removal cannot land between the store and this read, so this guards an
	// interleaving that should be impossible -- but answering the absence as though it
	// were the task just activated would be silent: the POI is handed a zero InterceptTask, with
	// no XID and no targets, as a successful activation, and the ADMF is told the
	// warrant is running.
	stored, found := s.store.Get(task.XID)
	if !found {
		return types.InterceptTask{}, fmt.Errorf("task was removed before the activation completed")
	}

	return stored, nil
}

// taskFromDetails maps X1 TaskDetails onto an interception task, resolving the
// DIDs it references against the destinations provisioned with CreateDestination.
func (s *Server) taskFromDetails(td TaskDetails) (types.InterceptTask, error) {
	if td.XID == "" {
		return types.InterceptTask{}, fmt.Errorf("missing xId")
	}
	if len(td.TargetIdentifiers) == 0 {
		return types.InterceptTask{}, fmt.Errorf("no target identifier")
	}
	// Every identifier is mapped, and one unmappable identifier refuses the whole
	// task. Taking the ones we understand and dropping the rest would narrow the
	// interception below what was ordered while answering that it had been applied.
	targets := make([]types.TargetIdentifier, 0, len(td.TargetIdentifiers))
	elements := make([]string, 0, len(td.TargetIdentifiers))
	seen := make(map[types.TargetIdentifier]bool, len(td.TargetIdentifiers))
	for _, ti := range td.TargetIdentifiers {
		mapped, err := mapTarget(ti)
		if err != nil {
			return types.InterceptTask{}, err
		}
		// The element the identifier was requested as, so the answers can use it. The
		// extension arm yields several criteria and names no plain element.
		element := ""
		if arm, ok := plainArm(ti); ok {
			element = arm.element
		}
		for _, target := range mapped {
			// A criterion named twice is one criterion: every criterion must match, and a
			// value matches twice exactly when it matches once. Kept once, with the element it
			// was first requested as, so the task holds -- and the contradiction check below
			// compares -- each distinct criterion once, however long the list a peer sent.
			if seen[target] {
				continue
			}
			seen[target] = true
			targets = append(targets, target)
			elements = append(elements, element)
		}
	}
	if len(targets) > maxTaskCriteria {
		// The schema leaves the list unbounded, so this is not malformed: it is a task this
		// element will not hold, and the operation's own code says so. The bound is what
		// keeps the pairwise contradiction check, which runs under the transition lock that
		// serialises every X1 transition, from being a peer's to size.
		return types.InterceptTask{}, fmt.Errorf(
			"the task names %d distinct criteria; this element holds at most %d per task",
			len(targets), maxTaskCriteria)
	}
	products, err := deliveryProducts(td.DeliveryType)
	if err != nil {
		return types.InterceptTask{}, err
	}

	// A provisioned ProductID replaces the XID in delivered PDU headers, and a
	// provisioned CorrelationID is the value to stamp on them (TS 103 221-1
	// clause 6.2.1.2). Both are optional in TS 103 221-1 and mandatory for an
	// LI_T3 trigger; a POI that needs them enforces that itself, since an
	// IRI-POI tasked by a real ADMF legitimately receives neither.
	var correlation uint64
	if td.CorrelationID != "" {
		// malformedTaskIdentifiers has refused anything but digits, so this fails only on a
		// value past 64 bits: inside the schema's unbounded type, and beyond what a PDU's
		// correlation field carries.
		correlation, err = strconv.ParseUint(td.CorrelationID, 10, 64)
		if err != nil {
			return types.InterceptTask{}, fmt.Errorf("correlationID exceeds the 64 bits this element can stamp")
		}
	}
	deliveries, unresolved := s.resolveDIDs(td.ListOfDIDs)
	if len(unresolved) > 0 {
		// **Before it is stored, before any callback and before any trigger.** A task
		// held here is one this element reports as active and acts on, so a partially
		// applied destination list has to be refused rather than stored with the subset:
		// otherwise an agency the warrant names receives nothing while the provisioning
		// function is told the interception is running, which is undiscoverable from
		// outside. On an element serving several agencies the alternative is worse than
		// silence -- the product would go to whichever endpoint the element's own
		// configuration names, crossing from one agency to another, and no destination the
		// element could not resolve may be an exception to that isolation.
		//
		// 2040 is the registry's own "DID does not exist on the NE", which tells the ADMF
		// what to do; a generic failure would tell it only that something went wrong. The
		// identifier is named here, on the X1 answer to the party that assigned it, and
		// nowhere else (see refuse).
		return types.InterceptTask{}, codedError{errCodeNoSuchDID, fmt.Errorf(
			"listOfDids names %d destination identifier(s) this element cannot resolve: %s",
			len(unresolved), strings.Join(unresolved, ", "))}
	}
	if len(td.ListOfDIDs) > 0 {
		// Every product the deliveryType orders needs a destination among the ones the task
		// names: "If a Task has an invalid combination of DeliveryType and Destinations (e.g.
		// "X2andX3" delivery specified, but only an X2 Destination given) … then the NE shall
		// reject the ActivateTaskRequest with an appropriate error" (TS 103 221-1 clause
		// 6.2.1.2), and 3040 is that error.
		//
		// Acknowledged instead, the product with no named destination would go to whatever
		// this element's own configuration names for it -- an endpoint the task did not
		// choose, on an element that may serve several agencies -- or nowhere. A task naming
		// no destination at all is not this case: it is served by the configured endpoints,
		// which is the arrangement WithConfiguredDestinations exists for.
		for _, p := range products {
			want, iface := types.DeliveryX2, "X2"
			if p == types.ProductCC {
				want, iface = types.DeliveryX3, "X3"
			}
			if !hasDelivery(deliveries, want) {
				return types.InterceptTask{}, codedError{errCodeBadDeliveryCombination, fmt.Errorf(
					"deliveryType %s orders %s delivery, and no destination the task names is an %s destination",
					td.DeliveryType, iface, iface)}
			}
		}
	} else if s.requireDIDs && slices.Contains(products, types.ProductCC) {
		// Accepting this would mean duplicating a subject's traffic and discarding
		// every copy, while the party that asked for it is told all is well. Content
		// ordered with no destination named is the same invalid combination as above
		// at an element with nowhere configured to send it.
		return types.InterceptTask{}, codedError{
			errCodeBadDeliveryCombination,
			fmt.Errorf("deliveryType %s orders X3 delivery, and the task names no destination", td.DeliveryType),
		}
	}

	task := types.InterceptTask{
		XID:            types.XID(td.XID),
		Targets:        targets,
		TargetElements: elements,
		DIDs:           td.ListOfDIDs,
		Products:       products,
		ProductID:      types.XID(td.ProductID),
		CorrelationID:  correlation,
		Deliveries:     deliveries,
		RecordScope:    recordScope(td),
	}
	// Every criterion must match (TS 103 221-1 table 6.2.1.2-1), so a task naming two values
	// of a property that holds one at a time can never match, and the table has the element
	// reject "an inappropriate combination of identifiers" rather than acknowledge it. Decided
	// here, once, for every element; what an element can evaluate at all is its CanApply's.
	// The kinds are named and the values are not: they may be a subscriber's identities.
	//
	// 3020, "Unsupported combination of TargetIdentifiers", is the registry's entry for it.
	if a, b, ok := task.ContradictoryTargets(); ok {
		return types.InterceptTask{}, codedError{errCodeBadTargetCombination, fmt.Errorf(
			"a %s criterion and a %s criterion cannot both match: every criterion of a task must match",
			a.Type, b.Type)}
	}

	return task, nil
}

// targetXML renders one target identifier as the contents of a
// <targetIdentifier> element, in the form the schemas define for it.
//
// It exists because an identifier cannot be reported as "element name plus value": the LI_T3
// packet-detection criteria are not plain identifier elements but arms of a 3GPP extension, nested
// inside targetIdentifierExtension with an Owner. Rendered as a plain element they would be XML no
// ADMF could validate, and a criterion with no mapping of its own would tell an auditing ADMF the
// element was tasked by a subscriber identity when it was tasked by a tunnel, a port or a
// direction. GetTaskDetails and GetAllDetails exist so an ADMF can discover what an element
// actually holds; answering them wrongly defeats the only mechanism it has.
//
// The output round-trips: what this emits, mapTarget parses back to the same
// identifier.
func targetXML(t types.TargetIdentifier) string {
	// The element names come from types, which is where they have to live now that
	// the X2 conditional attribute of TS 103 221-2 clause 5.3.18 renders the same
	// identifiers: one mapping, so the two interfaces cannot come to disagree about
	// what an identifier is called. The prefix is this document's, not the mapping's.
	if el, ok := t.Element(); ok {
		return "<ns1:" + el + ">" + escapeXML(t.Value) + "</ns1:" + el + ">"
	}

	arm, ok := extensionTargetArm(t)
	if !ok {
		// An identifier this package cannot render is reported as nothing rather than
		// as something else. An empty targetIdentifier is visibly wrong to an ADMF;
		// a plausible but incorrect one is not.
		return ""
	}

	return "<ns1:targetIdentifierExtension>" +
		"<ns1:Owner>" + ExtensionOwner3GPP + "</ns1:Owner>" +
		"<ext:UPFLIT3TargetIdentifierExtensions>" +
		"<ext:UPFLIT3TargetIdentifier>" + arm + "</ext:UPFLIT3TargetIdentifier>" +
		"</ext:UPFLIT3TargetIdentifierExtensions>" +
		"</ns1:targetIdentifierExtension>"
}

// targetXMLAs renders an identifier as the element it was requested as, when that is
// known, and otherwise as targetXML would.
//
// The identifier types fold several elements into one — supinai into SUPI, peiImeisv
// into PEI, e164Number into GPSI — so rendering a stored identifier from its type alone
// would answer a request in a different vocabulary from the one it used, and for an
// IMEISV in one the schema forbids. The value was checked against the requested element's own
// type when the task was accepted, so rendering it back there is valid.
func targetXMLAs(t types.TargetIdentifier, element string) string {
	if element == "" {
		return targetXML(t)
	}

	value := t.Value
	if element == elPEIIMEICD && digitsBetween(value, imeiDigits, imeiDigits) {
		// Stored as the fourteen-digit IMEI; the element the request used carries the
		// check digit too, verified on the way in, so this reproduces what was sent.
		value += string(imeiCheckDigit(value))
	}

	return "<ns1:" + element + ">" + escapeXML(value) + "</ns1:" + element + ">"
}

// extensionTargetArm renders the 3GPP LI_T3 arm for a packet-detection criterion.
func extensionTargetArm(t types.TargetIdentifier) (string, bool) {
	el := func(name, v string) string { return "<ext:" + name + ">" + escapeXML(v) + "</ext:" + name + ">" }

	switch t.Type {
	case types.TargetFSEID:
		return "<ext:FSEID>" + el("SEID", t.Value) + "</ext:FSEID>", true
	case types.TargetFTEID:
		// The value is "TEID" or "TEID@address"; the address, when present, is the
		// node that terminates the tunnel.
		teid, addr, hasAddr := strings.Cut(t.Value, "@")
		arm := "<ext:FTEID>" + el("TEID", teid)
		if hasAddr {
			// The same arm selection and the same rendering rule the triggering
			// path uses. This answer echoes a value a peer supplied, so a
			// conformant peer's value round-trips untouched; normalising it here
			// is what keeps a *non*-conformant one from making this element's own
			// details answer invalid, which is the answer an ADMF audits with.
			name, value := addressArm(addr)
			arm += el(name, value)
		}

		return arm + "</ext:FTEID>", true
	case types.TargetPDRID:
		return el("PDRID", t.Value), true
	case types.TargetQERID:
		return el("QERID", t.Value), true
	case types.TargetNetworkInstance:
		return el("NetworkInstance", t.Value), true
	case types.TargetGTPTunnelDirection:
		return el("GTPTunnelDirection", t.Value), true
	case types.TargetPDR:
		return el("PDR", t.Value), true
	default:
		return "", false
	}
}

// hasDelivery reports whether the endpoints include one of the given type.
func hasDelivery(endpoints []types.DeliveryEndpoint, t types.DeliveryType) bool {
	for _, e := range endpoints {
		if e.Type == t && e.Address != "" {
			return true
		}
	}

	return false
}

// mapTarget maps one X1 target identifier onto the criteria it names. It returns
// a slice because one arm — the LI_T3 extension — is itself a list, and every
// member of that list is a criterion the task ordered.
func mapTarget(t TargetIdentifier) ([]types.TargetIdentifier, error) {
	// A plain arm is stored in its canonical spelling, so the task matches the identifier
	// a consumer builds however the peer spelled it. The switch below keeps the order of
	// precedence plainArm shares with it.
	if arm, ok := plainArm(t); ok {
		value, err := canonicalArmValue(arm)
		if err != nil {
			return nil, err
		}
		if arm.element == elGTPUTunnelID {
			// canonicalArmValue has already made value a decimal within the schema's
			// range, so the parse cannot fail; checked anyway rather than assumed.
			if n, perr := strconv.ParseUint(value, 10, 64); perr != nil || n > math.MaxUint32 {
				// Schema-valid, one past what a TEID holds: no tunnel carries it, so the
				// criterion could never match and the task would be acknowledged to
				// produce nothing.
				return nil, fmt.Errorf("gtpuTunnelId %s is not a TEID", value)
			}
		}

		if arm.element == elPEIIMEICD {
			// Schema-valid -- the type is fifteen digits, not a valid IMEI -- so not 1010: the
			// operation's own refusal, as for a TEID above, and a message that sends the
			// operator to the digit rather than to the XML. canonicalArmValue has already made
			// the value fifteen digits, so the indexing is safe.
			if tok := xsToken(arm.value); imeiCheckDigit(tok[:imeiDigits]) != tok[imeiDigits] {
				return nil, fmt.Errorf("%s: the check digit does not match the IMEI", arm.element)
			}
		}

		return one(armTypes[arm.element], value), nil
	}

	// plainArm's order does not decide between arms: one targetIdentifier is an
	// xs:choice, so two populated arms cannot occur, and malformedTaskIdentifiers refuses
	// the message before this is reached rather than letting an order decide. Several
	// identifiers are several entries of the task's `targetIdentifiers` list, and each
	// becomes a criterion, all of which are required to match.
	if t.Extension != nil {
		return mapExtensionTarget(t.Extension)
	}
	// An empty supinai, which its unrestricted type admits and the decode cannot tell from
	// one not sent.
	if t.shape.first == elSUPINAI && t.shape.children == 1 {
		return nil, errors.New("supinai carries no NAI, so no subscriber could match it")
	}

	// An arm TS 103 221-1 defines and this element does not model -- or none at all, which
	// decodes the same way (see malformedTaskIdentifiers). 3010 is the registry's own entry,
	// and the element names are the schema's vocabulary, not a value, so they are quoted.
	return nil, refuse(errCodeUnsupportedTarget, "unsupported target identifier type%s", elementNames(t.Unmodelled))
}

// elementNames renders the names of unmodelled elements for a refusal, or nothing when there
// are none.
func elementNames(els []UnmodelledElement) string {
	if len(els) == 0 {
		return ""
	}
	names := make([]string, 0, len(els))
	for _, e := range els {
		names = append(names, e.XMLName.Local)
	}

	return " " + nameList(names)
}

// maxNamedElements bounds how many element names one refusal quotes. The names are what the
// peer needs to see its mistake, and a few say that; every one of a request's worth would make
// the answer as large as the request.
const maxNamedElements = 8

// nameList joins element names for a refusal, the first maxNamedElements of them and a count of
// the rest.
func nameList(names []string) string {
	if len(names) <= maxNamedElements {
		return strings.Join(names, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxNamedElements], ", "), len(names)-maxNamedElements)
}

// one is the single-criterion result every plain arm produces.
func one(kind types.TargetIdentifierType, value string) []types.TargetIdentifier {
	return []types.TargetIdentifier{{Type: kind, Value: value}}
}

// mapExtensionTarget maps the 3GPP LI_T3 packet-detection criteria of TS 33.128
// table 6.2.3-7 onto target identifiers. Clause 6.2.3 requires a CC-POI to support
// "at least the identifier types given in table 6.2.3-7", so all of them are
// accepted here: the three plain TS 103 221-1 arms are handled in mapTarget, and
// the seven extension arms in mapUPFLIT3Identifier.
//
// **Every member of the list becomes a criterion.** UPFLIT3TargetIdentifier is a
// SEQUENCE OF CHOICE, and its members join the task's other criteria: all of them are
// required to match (TS 103 221-1 table 6.2.1.2-1), so a list narrows the traffic
// selected — a session's F-SEID with a tunnel direction is that session's traffic in
// one direction. Mapping only the first member would run an interception other than
// the one ordered while acknowledging it.
//
// A member that cannot be mapped refuses the **whole task**, the rule taskFromDetails
// applies to the outer list, applied one level deeper.
//
// Accepting a criterion at this layer means it can be *provisioned*. Whether the
// CC-POI can then evaluate it against its own session state is a separate question
// answered further in — and a criterion it cannot evaluate is still refused there
// rather than accepted and ignored.
func mapExtensionTarget(ext *TargetIdentifierExtension) ([]types.TargetIdentifier, error) {
	// **The Owner is what says whose content this is.** TS 103 221-1 annex B makes the
	// extension a placeholder — an Owner naming the specification that defines the
	// content, then that content — and TS 33.128 table 6.2.3-7 names 3GPP as the owner of
	// these criteria. Reading them as LI_T3 without checking it means the element applies
	// 3GPP detection criteria to a message that claimed to carry someone else's, or
	// claimed nothing: the nested element names and namespace matching is not the same
	// statement as the extension being 3GPP's, and another owner is entitled to define
	// elements of any name in a namespace this element happens to recognise.
	//
	// The same rule the message-level extensions take (unhonourableExtensions), applied to
	// the structure whose content decides which traffic is intercepted.
	//
	// 3010 for each of the refusals below that is about *what* was sent, and 3020 for the one
	// about how it was combined: the registry's own entries, which tell an ADMF more than the
	// operation's generic failure.
	if ext.Owner != ExtensionOwner3GPP {
		return nil, refuse(errCodeUnsupportedTarget, "target identifier extension owned by %q is not supported", ext.Owner)
	}
	if len(ext.Unmodelled) > 0 {
		// An identifier type beside the LI_T3 criteria, or instead of them, that this element
		// does not model. Applying the criteria alone would intercept under fewer conditions
		// than were ordered -- which, since every condition must hold, is a wider interception.
		return nil, refuse(errCodeUnsupportedTarget,
			"target identifier extension carries unsupported identifier type%s", elementNames(ext.Unmodelled))
	}
	if ext.lit3Containers > 1 {
		// The extension's content is an xs:any of unbounded occurrence, so two containers are
		// schema-valid. Every criterion of both would join the task, but one targetIdentifier
		// is one identifier, and which container's list was meant as it is not this element's
		// to settle.
		return nil, refuse(errCodeBadTargetCombination,
			"target identifier extension carries %d UPFLIT3TargetIdentifierExtensions; this element supports one",
			ext.lit3Containers)
	}
	if ext.UPFT3 == nil || len(ext.UPFT3.Identifiers) == 0 {
		return nil, refuse(errCodeUnsupportedTarget, "unsupported target identifier extension")
	}

	out := make([]types.TargetIdentifier, 0, len(ext.UPFT3.Identifiers))
	for i, id := range ext.UPFT3.Identifiers {
		target, err := mapUPFLIT3Identifier(id)
		if err != nil {
			return nil, fmt.Errorf("LI_T3 detection criterion %d of %d: %w", i+1, len(ext.UPFT3.Identifiers), err)
		}
		out = append(out, target)
	}

	return out, nil
}

// mapUPFLIT3Identifier maps one arm of the UPFLIT3TargetIdentifier choice. Each arm
// is refused rather than defaulted when its value is unusable: a criterion that
// resolves to nothing would arm an interception that produces no product, and one
// that resolves to everything would collect beyond the warrant.
func mapUPFLIT3Identifier(id UPFLIT3Identifier) (types.TargetIdentifier, error) {
	switch {
	case id.FSEID != nil:
		if id.FSEID.SEID == 0 {
			return types.TargetIdentifier{}, fmt.Errorf("FSEID criterion carries no SEID")
		}
		// The addresses are not matched -- within one UPF the SEID is unambiguous -- but each
		// is checked against its schema type, so a malformed one is refused rather than
		// acknowledged unread. Both may be present: the sequence allows a dual-stack node.
		for _, a := range []IPAddress{{IPv4: id.FSEID.IPv4Address}, {IPv6: id.FSEID.IPv6Address}} {
			if a.Arms() == 0 {
				continue
			}
			if err := a.Valid(); err != nil {
				// The address is not quoted: see refuse.
				return types.TargetIdentifier{}, refuse(errCodeSchemaError, "FSEID address: %v", err)
			}
		}

		return types.TargetIdentifier{
			Type:  types.TargetFSEID,
			Value: strconv.FormatUint(id.FSEID.SEID, 10),
		}, nil

	case id.FTEID != nil:
		return mapFTEID(*id.FTEID)

	case id.PDRID != nil:
		return types.TargetIdentifier{
			Type:  types.TargetPDRID,
			Value: strconv.FormatUint(uint64(*id.PDRID), 10),
		}, nil

	case id.QERID != nil:
		return types.TargetIdentifier{
			Type:  types.TargetQERID,
			Value: strconv.FormatUint(uint64(*id.QERID), 10),
		}, nil

	case id.NetworkInstance != "":
		// xs:hexBinary in the schema, so it is carried and compared as the octets it
		// encodes rather than as a name.
		v, err := hexBinaryCriterion("NetworkInstance", id.NetworkInstance)
		if err != nil {
			return types.TargetIdentifier{}, err
		}

		return types.TargetIdentifier{Type: types.TargetNetworkInstance, Value: v}, nil

	case id.GTPTunnelDirection != "":
		// A closed enumeration. Anything else is refused: an unrecognised direction
		// would either match nothing or both directions, and both are wrong in a way
		// the triggering function could not detect.
		switch id.GTPTunnelDirection {
		case GTPDirectionOutbound, GTPDirectionInbound:
			return types.TargetIdentifier{
				Type:  types.TargetGTPTunnelDirection,
				Value: id.GTPTunnelDirection,
			}, nil
		default:
			return types.TargetIdentifier{}, fmt.Errorf(
				"GTPTunnelDirection %q is not one of the enumerated values", id.GTPTunnelDirection)
		}

	case id.PDR != "":
		v, err := hexBinaryCriterion("PDR", id.PDR)
		if err != nil {
			return types.TargetIdentifier{}, err
		}

		return types.TargetIdentifier{Type: types.TargetPDR, Value: v}, nil
	}

	return types.TargetIdentifier{}, refuse(errCodeUnsupportedTarget,
		"unsupported detection criterion%s", elementNames(id.Unmodelled))
}

// mapFTEID maps an F-TEID criterion to "TEID" or "TEID@address", the address in the
// canonical spelling the plain address arms use, so one tunnel endpoint has one value
// however a peer wrote it.
//
// The address is optional in the schema. Where it is absent the criterion is the TEID
// alone, which cannot separate two tunnels sharing one; that is the triggering
// function's choice to make, not ours to refuse. Three things are refused:
//   - A TEID of 0. The schema makes the element mandatory, and an absent one decodes as 0;
//     either way 0 names no user-plane tunnel (TS 29.281 reserves it for path management),
//     so the criterion could never match. The operation's own refusal, as for an FSEID
//     with no SEID.
//   - An address that is not one of its schema type: 1010, as for the plain address arms.
//   - Both an IPv4 and an IPv6 address. The schema admits both, for a dual-stack endpoint,
//     but a criterion holds one address and a packet arrives on one of them, so keeping
//     either would narrow the interception to one family by this element's choice, and
//     dropping both would widen it to every endpoint with that TEID. Refused as a
//     criterion this element cannot apply rather than read in one of those ways.
func mapFTEID(f FTEID) (types.TargetIdentifier, error) {
	if f.TEID == 0 {
		return types.TargetIdentifier{}, errors.New("FTEID criterion carries no TEID")
	}
	v := strconv.FormatUint(uint64(f.TEID), 10)

	addr := IPAddress{IPv4: xsToken(f.IPv4Address), IPv6: xsToken(f.IPv6Address)}
	switch addr.Arms() {
	case 0:
		return types.TargetIdentifier{Type: types.TargetFTEID, Value: v}, nil
	case 1:
		if err := addr.Valid(); err != nil {
			// The address is not quoted: see refuse.
			return types.TargetIdentifier{}, refuse(errCodeSchemaError, "FTEID address: %v", err)
		}

		return types.TargetIdentifier{Type: types.TargetFTEID, Value: v + "@" + addr.Canonical()}, nil
	default:
		return types.TargetIdentifier{}, errors.New(
			"FTEID criterion names both an IPv4 and an IPv6 address; a criterion matches a tunnel endpoint by one")
	}
}

// hexBinaryCriterion checks a criterion of the schema's xs:hexBinary type and returns it
// in one spelling, lower case. A value that is not an even number of hexadecimal digits is
// outside the type, so 1010; an empty one is inside it but names nothing a packet could
// carry, so the operation's own refusal.
func hexBinaryCriterion(element, value string) (string, error) {
	tok := xsToken(value)
	octets, err := hex.DecodeString(tok)
	if err != nil {
		return "", refuse(errCodeSchemaError, "%s is not hexBinary as the schema requires", element)
	}
	if len(octets) == 0 {
		return "", fmt.Errorf("%s criterion names no value", element)
	}

	return strings.ToLower(tok), nil
}

// malformedTaskIdentifiers returns a refusal when a task's own identifiers do not conform
// to the format the schema defines for them, or nil when they do.
//
// The task's `xId` and the `productID` that replaces it in delivered PDU headers are both
// `etsi103280:UUID`. A malformed one is not cosmetic: `types.XID.Bytes()` maps an
// unparseable value to sixteen zero bytes, an MDF discards product it cannot attribute to
// a warrant, and nothing reports either — so the interception runs and every record is
// delivered under a label the mediation function throws away.
//
// An *absent* xId is left to taskFromDetails, which calls it missing. "Missing" and "not a
// UUID" are different things to be told, and the mandatory-field check already said the
// first one clearly.
func malformedTaskIdentifiers(td TaskDetails) error {
	// An element the schema allows once, sent twice: the decode kept one, and no reading of
	// the message is authoritative. See strict.go.
	if err := td.shape.err("taskDetails"); err != nil {
		return err
	}
	// xId is mandatory in TaskDetails, so a task without one is the message violating its
	// own schema. Checked here, ahead of the "no such task" test, so a ModifyTask without
	// one is not answered 2020 as though it named a task this element lacks.
	if td.XID == "" {
		return refuse(errCodeSchemaError, "missing xId")
	}
	if err := validIdentifier("xId", td.XID); err != nil {
		return err
	}
	// Present but empty is not absent: the element was sent, and "" is outside its type.
	if td.ProductID != "" || td.sentProductID {
		if err := validIdentifier("productID", td.ProductID); err != nil {
			return err
		}
	}
	// xs:nonNegativeInteger. collapseTokens has already removed the whitespace, the leading
	// "+" and the sign of a negative zero its lexical space admits, so what remains must be digits; a value past 64
	// bits is inside the type and refused later as one this element cannot carry.
	if td.CorrelationID != "" || td.sentCorrelationID {
		if !digitsBetween(td.CorrelationID, 1, len(td.CorrelationID)) {
			return refuse(errCodeSchemaError, "correlationID is not a non-negative integer as the schema requires")
		}
	}
	// targetIdentifiers is mandatory and holds at least one targetIdentifier.
	if len(td.TargetIdentifiers) == 0 {
		return refuse(errCodeSchemaError, "targetIdentifiers carries no targetIdentifier; the schema requires at least one")
	}
	for _, ext := range td.TaskDetailsExtensions {
		if err := ext.malformedShape("taskDetailsExtensions"); err != nil {
			return err
		}
	}
	// The same check the two identifiers above get, for the same reason. A dId the
	// schema cannot type is one no destination can be found under, so the task is
	// accepted and then delivers to nothing — the failure RequireResolvableDIDs
	// exists to prevent, arriving by the one route that skips it.
	for _, did := range td.ListOfDIDs {
		if err := validIdentifier("dId", did); err != nil {
			return err
		}
	}
	for _, ti := range td.TargetIdentifiers {
		// **The value, not only which arm carried it.** These arms are typed by the
		// schema — an IPv4Address is a TS 103 280 IPv4 address, a tcpPort a Port. A task
		// carrying a port outside its type's range, or an address that is not an address,
		// would otherwise be stored, acted on as a detection criterion, and echoed back by
		// GetTaskDetails as this element's own account of what it holds. Checked here
		// because this is the decode path every task crosses: a check beside one consumer
		// is not inherited by the next one.
		if err := malformedTargetValue(ti); err != nil {
			return err
		}
		// **A choice with no child at all is a schema error; one carrying only an arm this
		// element does not model is not.** The decode counts every child, the unmodelled
		// ones included (TargetIdentifier.Unmodelled), so the two are told apart: an arm TS
		// 103 221-1 defines and this element does not model is a conformant message it cannot
		// act on, and falls through to mapTarget for 3010 -- answering 1010 would send an
		// operator to correct something that is correct. TestRefuseCriteriaOutsideTheTable
		// pins that half.
		if ti.shape.noArm() {
			return refuse(errCodeSchemaError, "targetIdentifier populates no arm of its choice; exactly one is required")
		}
		if n := max(populatedArms(ti), ti.shape.children); n > 1 {
			// The schema defines TargetIdentifier as an xs:choice, so a message
			// populating two arms is invalid against it and no reading of it is
			// authoritative. Selecting one would mean the *element* deciding the scope
			// of an interception that the provisioning function ordered.
			//
			// Refused here rather than in unhonourableTaskFields, and the distinction is
			// the one the ordering at the call site already draws: this is a message that
			// violates its own format, not a well-formed task carrying something this
			// element cannot honour. Asking whether we could honour its contents presumes
			// something that has not been established.
			return refuse(errCodeSchemaError,
				"targetIdentifier populates %d arms of a choice; exactly one is valid", n)
		}
		// One arm this element models, sent empty. Every plain arm's type but supinai's
		// excludes the empty string, and decoded it is indistinguishable from an arm not sent
		// at all -- which would otherwise be refused as an identifier type this element does
		// not support. supinai is an unrestricted xs:string, so an empty one is schema-valid;
		// mapTarget refuses it as an identity no subscriber has.
		if _, populated := plainArm(ti); !populated && ti.shape.first != elSUPINAI {
			if _, modelled := armTypes[ti.shape.first]; modelled {
				return refuse(errCodeSchemaError, "%s carries no value; its schema type requires one", ti.shape.first)
			}
		}
		// The same rule one level deeper. UPFLIT3TargetIdentifier is a *sequence of*
		// choices, so several members are valid and several arms of one member are
		// not — and the reason is unchanged: a member with two arms has no
		// authoritative reading, so selecting one narrows or widens an interception by
		// this element's decision rather than the provisioning function's. Checking
		// the outer choice and not this one would have left the same defect at the one
		// level where a CC-POI actually reads its detection criteria.
		if ti.Extension == nil {
			continue
		}
		if err := ti.Extension.shape.err("targetIdentifierExtension"); err != nil {
			return err
		}
		if ti.Extension.UPFT3 == nil {
			continue
		}
		if len(ti.Extension.UPFT3.Unmodelled) > 0 {
			// The container's schema holds UPFLIT3TargetIdentifier and nothing else.
			return refuse(errCodeSchemaError,
				"UPFLIT3TargetIdentifierExtensions carries%s, which its schema does not define",
				elementNames(ti.Extension.UPFT3.Unmodelled))
		}
		for i, id := range ti.Extension.UPFT3.Identifiers {
			where := fmt.Sprintf("LI_T3 detection criterion %d of %d", i+1, len(ti.Extension.UPFT3.Identifiers))
			if n := max(populatedLIT3Arms(id), id.shape.children); n > 1 {
				return refuse(errCodeSchemaError,
					"%s populates %d arms of a choice; exactly one is valid", where, n)
			}
			if id.shape.noArm() {
				return refuse(errCodeSchemaError, "%s populates no arm of its choice; exactly one is required", where)
			}
			if err := id.shape.err(where); err != nil {
				return err
			}
			// A closed enumeration over xs:string, so a value outside it -- an empty one
			// included, which decodes as the arm not sent -- is outside the schema.
			if id.shape.first == "GTPTunnelDirection" &&
				id.GTPTunnelDirection != GTPDirectionOutbound && id.GTPTunnelDirection != GTPDirectionInbound {
				return refuse(errCodeSchemaError, "%s: GTPTunnelDirection is not one of the schema's enumerated values", where)
			}
			if id.FSEID != nil {
				if err := id.FSEID.shape.err(where + " FSEID"); err != nil {
					return err
				}
			}
			if id.FTEID != nil {
				if err := id.FTEID.shape.err(where + " FTEID"); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// malformedTargetValue checks the populated plain arm's value against its schema type.
//
// Every typed arm is checked, the subscriber identifiers included: each is a TS 103 280
// pattern type (an IMSI is six to fifteen digits, an IMEI fourteen, an IMEISV sixteen, an
// MSISDN or E.164 number one to fifteen), and a value outside it would be acknowledged,
// stored as a criterion no session could ever match, and echoed back as this element's
// account of what it holds. Only supinai is unrestricted, because the schema types an NAI as a
// plain string. The extension arm's contents are checked by mapUPFLIT3Identifier, which
// refuses each criterion it cannot use.
func malformedTargetValue(t TargetIdentifier) error {
	arm, ok := plainArm(t)
	if !ok {
		return nil
	}
	// canonicalArmValue also refuses a schema-valid value no subscriber could hold, without a
	// code; mapTarget answers that one with the operation's own.
	if _, err := canonicalArmValue(arm); errorCode(err) == errCodeSchemaError {
		return err
	}

	return nil
}

// The plain TargetIdentifier elements this element models, by their schema names.
const (
	elSUPIIMSI     = "supiimsi"
	elIMSI         = "imsi"
	elSUPINAI      = "supinai"
	elPEIIMEI      = "peiImei"
	elPEIIMEISV    = "peiImeisv"
	elIMEI         = "imei"
	elPEIIMEICD    = "peiImeiCheckDigit"
	elGPSIMSISDN   = "gpsiMsisdn"
	elE164Number   = "e164Number"
	elGTPUTunnelID = "gtpuTunnelId"
	elIPv4Address  = "ipv4Address"
	elIPv6Address  = "ipv6Address"
	elTCPPort      = "tcpPort"
	elUDPPort      = "udpPort"
)

// targetArm is one plain TargetIdentifier arm: the element that carried it and its text.
type targetArm struct {
	element, value string
}

// plainArm returns the first populated plain arm of t, in mapTarget's order of
// precedence, or false when t carries none — only the extension, or nothing modelled.
func plainArm(t TargetIdentifier) (targetArm, bool) {
	for _, a := range []targetArm{
		{elSUPIIMSI, t.SUPIIMSI},
		{elIMSI, t.IMSI},
		{elSUPINAI, t.SUPINAI},
		{elPEIIMEI, t.PEIIMEI},
		{elPEIIMEISV, t.PEIIMEISV},
		{elIMEI, t.IMEI},
		{elPEIIMEICD, t.PEIIMEICheckDigit},
		{elGPSIMSISDN, t.GPSIMSISDN},
		{elE164Number, t.E164Number},
		{elGTPUTunnelID, t.GTPUTunnelID},
		{elIPv4Address, t.IPv4Address},
		{elIPv6Address, t.IPv6Address},
		{elTCPPort, t.TCPPort},
		{elUDPPort, t.UDPPort},
	} {
		if a.value != "" {
			return a, true
		}
	}

	return targetArm{}, false
}

// armTypes is the identifier type each plain arm is matched as.
var armTypes = map[string]types.TargetIdentifierType{
	elSUPIIMSI: types.TargetSUPI, elIMSI: types.TargetSUPI, elSUPINAI: types.TargetSUPI,
	elPEIIMEI: types.TargetPEI, elPEIIMEISV: types.TargetPEI,
	elIMEI: types.TargetPEI, elPEIIMEICD: types.TargetPEI,
	elGPSIMSISDN: types.TargetGPSI, elE164Number: types.TargetGPSI,
	elGTPUTunnelID: types.TargetFTEID,
	elIPv4Address:  types.TargetUEIPv4, elIPv6Address: types.TargetUEIPv6,
	elTCPPort: types.TargetTCPPort, elUDPPort: types.TargetUDPPort,
}

// canonicalArmValue checks an arm's value against its schema type and returns it in one
// canonical spelling, or a 1010 refusal.
//
// **Canonical, because matching compares strings.** The pattern types are xs:token, whose
// surrounding XML whitespace the schema collapses, and the integer types admit a sign and
// leading zeros ("+80", "0080"), so one value has several valid spellings -- and a task
// stored in one of them would never equal the identifier a consumer built in another. The
// IPv6 type admits only the expanded lowercase form, so the parseable compressed forms a
// peer may send are stored expanded, which is also the form the answers must use. Matching
// compares the stored text (types.InterceptTask.MatchedBy), so a consumer presenting a UE
// IPv6 address must present it expanded too; SD-Core's CC-POI refuses a UE IPv6 criterion
// outright, so no deployed consumer compares one today.
//
// The value is not quoted in the refusal: for the subscriber identifiers it is a
// subscriber's identity (see refuse).
func canonicalArmValue(a targetArm) (string, error) {
	bad := func() (string, error) {
		return "", refuse(errCodeSchemaError, "%s does not conform to its schema type", a.element)
	}
	tok := xsToken(a.value)

	switch a.element {
	case elSUPIIMSI, elIMSI:
		if !digitsBetween(tok, imsiMinDigits, imsiMaxDigits) {
			return bad()
		}

		return tok, nil
	case elPEIIMEI, elIMEI:
		if !digitsBetween(tok, imeiDigits, imeiDigits) {
			return bad()
		}

		return tok, nil
	case elPEIIMEICD:
		// The schema type is fifteen digits. Stored as the fourteen-digit IMEI the network
		// functions present, which drops the check digit -- so the check digit is verified
		// rather than discarded unread: a wrong one means the ADMF's value names no device,
		// and the answer re-derives it, so only a correct one round-trips exactly.
		// The check digit is verified in mapTarget, not here: a wrong one is schema-valid.
		if !digitsBetween(tok, imeiDigits+1, imeiDigits+1) {
			return bad()
		}

		return tok[:imeiDigits], nil
	case elPEIIMEISV:
		if !digitsBetween(tok, imeisvDigits, imeisvDigits) {
			return bad()
		}

		return tok, nil
	case elGPSIMSISDN, elE164Number:
		if !digitsBetween(tok, msisdnMinDigits, msisdnMaxDigits) {
			return bad()
		}

		return tok, nil
	case elSUPINAI:
		// xs:string, so neither restricted nor whitespace-collapsed, and a value of nothing but
		// whitespace is inside it. It is still no NAI (RFC 7542), and a criterion on it could
		// never match, so it is refused -- with the operation's own code rather than 1010,
		// because the message conforms. The value is otherwise kept exactly as sent.
		if xsToken(a.value) == "" {
			return "", errors.New("supinai carries no NAI, so no subscriber could match it")
		}

		return a.value, nil
	case elGTPUTunnelID:
		// GtpTunnelId is an xs:integer from 0 to 4294967296. The upper bound is one past
		// what a TEID holds; mapTarget refuses it as a criterion this element cannot use.
		return schemaInteger(tok, 0, gtpTunnelIDMax, bad)
	case elTCPPort:
		// TS 103 280 TCPPort is minExclusive 1, so 1 is outside it.
		return schemaInteger(tok, 2, math.MaxUint16, bad)
	case elUDPPort:
		return schemaInteger(tok, 0, math.MaxUint16, bad)
	case elIPv4Address:
		ip, ok := parseSchemaIPv4(tok)
		if !ok {
			return bad()
		}

		return ip.String(), nil
	case elIPv6Address:
		// A zone and an IPv4-mapped address both parse, and neither is an address of the
		// schema's IPv6 type: refused rather than stored in a form the answers could only
		// render into the wrong arm.
		ip, err := netip.ParseAddr(tok)
		if err != nil || ip.Is4() || ip.Is4In6() || ip.Zone() != "" {
			return bad()
		}

		return expandIPv6(ip.AsSlice()), nil
	}

	return bad()
}

// schemaInteger checks an xs:integer lexical value against a range and returns it in
// canonical decimal.
func schemaInteger(tok string, lo, hi int64, bad func() (string, error)) (string, error) {
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil || n < lo || n > hi {
		return bad()
	}

	return strconv.FormatInt(n, 10), nil
}

// maxTaskCriteria bounds the distinct criteria one task may hold. A task names its subject by
// one identifier, or a triggered CC-POI's session by a handful; this is far past either, and
// small enough that the pairwise contradiction check stays negligible under the transition lock.
const maxTaskCriteria = 256

// The TS 103 280 lengths of the identifier types that are strings of digits.
const (
	imsiMinDigits   = 6
	imsiMaxDigits   = 15
	imeiDigits      = 14 // the 15-digit IMEICheckDigit adds the TS 23.003 check digit
	imeisvDigits    = 16
	msisdnMinDigits = 1
	msisdnMaxDigits = 15
)

// gtpTunnelIDMax is the upper bound of the schema's GtpTunnelId, one past what a TEID can
// hold: mapTarget refuses that value as a criterion no tunnel carries.
const gtpTunnelIDMax = 1 << 32

// xsToken trims the whitespace an xs:token value's surroundings may carry: space, tab,
// carriage return and line feed, which the schema collapses, and nothing else. Wider
// trimming -- strings.TrimSpace's Unicode spaces -- would accept values the schema refuses.
func xsToken(s string) string {
	return strings.Trim(s, " \t\r\n")
}

// schemaIPv4 is TS 103 280's IPv4Address pattern, anchored. It admits a leading zero in
// an octet ("010.000.000.001"), which netip.ParseAddr refuses as ambiguous.
var schemaIPv4 = regexp.MustCompile(`^((25[0-5]|2[0-4][0-9]|[01]?[0-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9]?[0-9])$`)

// parseSchemaIPv4 parses an address of the schema's IPv4Address type, reading each
// octet as decimal whatever its leading zeros, so every spelling the schema admits is
// accepted and stored in one canonical form.
func parseSchemaIPv4(s string) (netip.Addr, bool) {
	if !schemaIPv4.MatchString(s) {
		return netip.Addr{}, false
	}
	var octets [4]byte
	for i, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return netip.Addr{}, false
		}
		octets[i] = byte(n)
	}

	return netip.AddrFrom4(octets), true
}

// imeiCheckDigit is the check digit TS 23.003 Annex B defines for a fourteen-digit IMEI:
// the Luhn digit, doubling every second digit from the right of the fourteen.
func imeiCheckDigit(imei string) byte {
	sum := 0
	for i := len(imei) - 1; i >= 0; i-- {
		d := int(imei[i] - '0')
		if (len(imei)-1-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}

	return byte('0' + (10-sum%10)%10)
}

// digitsBetween reports whether s is all decimal digits with a length in [lo, hi].
func digitsBetween(s string, lo, hi int) bool {
	if len(s) < lo || len(s) > hi {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

// populatedArms counts how many arms of the TargetIdentifier choice carry a value.
//
// It is deliberately a count over every arm rather than a check of the two that
// mapTarget happens to reach first: a guard sharing the switch's order would let that
// order decide the answer. A new arm added to the struct and not added here would be
// silently exempt — hence one place, listing all of them, beside the declaration they
// mirror.
func populatedArms(t TargetIdentifier) int {
	n := 0
	for _, v := range []string{
		t.IMSI, t.SUPIIMSI, t.SUPINAI,
		t.PEIIMEI, t.PEIIMEISV, t.IMEI, t.PEIIMEICheckDigit,
		t.GPSIMSISDN, t.E164Number,
		t.GTPUTunnelID, t.IPv4Address, t.IPv6Address,
		t.TCPPort, t.UDPPort,
	} {
		if v != "" {
			n++
		}
	}
	if t.Extension != nil {
		n++
	}
	// An arm this element does not model is still an arm: counting it is what refuses a
	// choice carrying one modelled and one unmodelled arm, rather than narrowing it to the
	// one understood. A choice carrying only unmodelled arms counts one here and falls
	// through to mapTarget's refusal with the operation's code.
	n += len(t.Unmodelled)

	return n
}

// populatedLIT3Arms is populatedArms for the inner choice: how many arms of one
// LI_T3 detection criterion carry a value. Listed in full, for the same reason —
// a criterion added to the struct and not added here would be silently exempt.
func populatedLIT3Arms(id UPFLIT3Identifier) int {
	n := 0
	for _, set := range []bool{
		id.FSEID != nil,
		id.PDRID != nil,
		id.QERID != nil,
		id.FTEID != nil,
		id.NetworkInstance != "",
		id.GTPTunnelDirection != "",
		id.PDR != "",
	} {
		if set {
			n++
		}
	}

	return n + len(id.Unmodelled)
}

// unhonourableTaskFields returns a refusal for the first field of td this element can
// neither act on nor safely disregard, or nil when there is none.
//
// The rule it applies, and the reason there is a rule rather than a list: a field the
// element discards silently produces an interception that differs from the one
// authorised, in a way nobody outside the element can discover. The provisioning
// function is acknowledged and has no channel through which the divergence could be
// reported. So the default is refusal, and a field is disregarded only where the
// specification addresses it to a function this element is not, or where disregarding
// it cannot change what is intercepted or where the product goes.
//
// What is deliberately *not* here: listOfMediationDetails and
// implicitDeactivationAllowed, both of which pass. See their declarations for the
// sentences that put them on the other side of the rule.
//
// honoursCorrelation says whether this element acts on a provisioned correlation value.
// It is a parameter rather than a property of td because the answer differs by element
// -- see HonoursCorrelationID: a field one point of interception honours and another
// cannot has to follow the element.
func unhonourableTaskFields(td TaskDetails, honoursCorrelation bool) error {
	if td.CorrelationID != "" && !honoursCorrelation {
		// Accepting it would mean ignoring it: neither IRI-POI uses a provisioned value —
		// the AMF sends a zero correlation and the SMF sends the session's F-SEID. The
		// value at stake is how the mediation function joins this element's records to a
		// warrant's content, so an ADMF that provisioned it per clause 6.2.1.2 would
		// receive an acknowledgement and a different interception from the one it
		// authorised, with no channel through which the divergence could be reported.
		// That is precisely what this function exists to prevent.
		//
		// Honouring it here is not the alternative: a 5G correlation is per session by
		// construction, and one provisioned value across the many sessions a task covers
		// would collapse them into one stream at the mediation function. Refusing names
		// the divergence at the moment it can still be corrected.
		//
		// No code is attached, so the caller supplies 3000 or 3001 by whether it is
		// serving an activation or a modification — the registry's own "details of why
		// the Task cannot be activated/modified", which is what this is.
		return fmt.Errorf("a provisioned correlationID is not supported by this element: " +
			"its correlation is derived per session, so one value cannot serve a task covering many")
	}

	if len(td.ListOfServiceTypes) > 0 {
		// This element applies no service-type scoping, so honouring the task as sent
		// would intercept every service for the target when a narrower set was
		// authorised -- more product than the warrant allows, and silently. Refusing it is
		// this element's design decision rather than a rule TS 33.128 states for the AMF or
		// SMF: the specification gives that remedy -- "shall reject the task with an
		// appropriate error" -- for the SMSF, EES, 5GMS AF and NWDAF POIs, and the same
		// reasoning applies here. TS 103 221-1 table 6.7-3 has a code for exactly this,
		// 3050.
		return refuse(errCodeBadServiceType, "service-type scoping is not supported")
	}
	if len(td.ListOfTrafficPolicyReferences) > 0 {
		// "Ordered list  of TrafficPolicyReferences to be applied to the LITaskObject",
		// defined in TS 103 120 clause 8.2.13. It is an instruction about the task, and
		// this project implements no TS 103 120 traffic policies — so an accepted task
		// would run without the policy that was meant to shape it.
		return fmt.Errorf("listOfTrafficPolicyReferences is not supported")
	}
	if len(td.DSIDs) > 0 {
		// A dSId names a destination *set*, which annex E defines as a Generic Object:
		// the ADMF creates a DestinationSetDetails object and then references its
		// object id here. This element implements no Generic Objects — it refuses the
		// object CRUD outright — so a dSId can never name anything it holds.
		//
		// Refused whenever one is present, not only when the task names nothing else.
		// A task naming a dId *and* a dSId has still expressed an intent this element
		// would discard, and sets carry real semantics to discard: "Redundant" with a
		// preference order and failover, or "Duplicate", where "the NE will send copies
		// of intercepted traffic to all DIDs within the Destination Set".
		return fmt.Errorf("destination sets (dSId) are not supported")
	}
	if err := unhonourableExtensions(td.TaskDetailsExtensions, "task"); err != nil {
		return err
	}

	return nil
}

// unhonourableExtensions applies the same rule to an extension placeholder, on a task or
// on a destination.
//
// The instinct with an unknown extension is to ignore it. On this interface that is
// backwards: an extension exists in order to change the meaning of the message carrying
// it — the LI_T3 detection criteria arrive through exactly such a placeholder — so the
// test is on the owner and the content, not on presence.
func unhonourableExtensions(exts []MessageExtension, on string) error {
	associations := 0
	for _, ext := range exts {
		associations += max(ext.associations, boolInt(ext.IdentifierAssociation != nil))
	}
	if associations > 1 {
		// Each placeholder may carry any number of extension elements, so this is
		// schema-valid -- and the decode keeps one value, so a task carrying two with
		// different scopes would run under whichever it kept.
		return fmt.Errorf("%s carries %d IdentifierAssociationExtensions; this element acts on one", on, associations)
	}
	for _, ext := range exts {
		if ext.Owner != ExtensionOwner3GPP {
			return fmt.Errorf("%s extension owned by %q is not supported", on, ext.Owner)
		}
		// The identifier-association extension is a *task* extension: TS 33.128 defines it
		// in TaskDetailsExtensions, where it selects records. Recognised on a destination it
		// would be acknowledged and then mean nothing, which is the outcome this function
		// exists to prevent -- so on anything but a task it is as unrecognised as any other.
		if ext.IdentifierAssociation == nil || len(ext.Content) > 0 || on != "task" {
			return fmt.Errorf("3GPP %s extension %s is not supported", on, extensionContentNames(ext))
		}
		// Only the AMF IRI-POI is given this extension, and only 33.128 table 6.2.2.1-1
		// defines it, but it is accepted at every element this package serves: an SMF
		// or UPF that refused it would refuse a task an ADMF may legitimately send to
		// several elements at once, and an element that produces no
		// identifier-association records loses nothing by being told which to produce.
		switch ext.IdentifierAssociation.EventsGenerated {
		case string(types.RecordScopeIdentifierAssociation), string(types.RecordScopeAll):
		default:
			// A closed enumeration. Defaulting would either withhold records the
			// warrant authorises or produce records it does not, and the element could
			// not tell which it had done.
			return fmt.Errorf("IdentifierAssociationEventsGenerated %q is not one of the enumerated values",
				ext.IdentifierAssociation.EventsGenerated)
		}
	}

	return nil
}

// malformedShape refuses an extension placeholder carrying its Owner twice, or an
// identifier-association extension carrying its value twice. where names the placeholder.
func (ext MessageExtension) malformedShape(where string) error {
	if err := ext.shape.err(where); err != nil {
		return err
	}
	if ext.IdentifierAssociation != nil {
		return ext.IdentifierAssociation.shape.err("IdentifierAssociationExtensions")
	}

	return nil
}

// malformedShape refuses destination details carrying an element twice that the schema allows
// once, at any level a decode would read as one. See strict.go.
func (d DestinationDetails) malformedShape() error {
	if err := d.shape.err("destinationDetails"); err != nil {
		return err
	}
	for _, ext := range d.Extensions {
		if err := ext.malformedShape("destinationDetailsExtensions"); err != nil {
			return err
		}
	}
	// deliveryAddress is a choice, of which only ipAddressAndPort is modelled: a second arm
	// beside it would otherwise be dropped and the destination created from the first.
	if err := d.Address.shape.choiceErr("deliveryAddress"); err != nil {
		return err
	}
	if d.Address.shape.noArm() {
		return refuse(errCodeSchemaError, "deliveryAddress populates no arm of its choice; exactly one is required")
	}
	if ap := d.Address.IPAddressAndPort; ap != nil {
		// The address and port choices are checked by their own Valid.
		return ap.shape.err("ipAddressAndPort")
	}

	return nil
}

// extensionContentNames lists the element names an extension carried, so a refusal says
// what was sent. Empty content is named as such rather than as nothing, since an
// extension with an Owner and no content is its own kind of malformed.
func extensionContentNames(ext MessageExtension) string {
	if ext.IdentifierAssociation != nil {
		// A recognised element alongside unmodelled ones: name them all, since it is
		// the combination that is not supported.
		names := []string{"IdentifierAssociationExtensions"}
		for _, item := range ext.Content {
			names = append(names, item.XMLName.Local)
		}

		return nameList(names)
	}
	if len(ext.Content) == 0 {
		return "(no content)"
	}

	names := make([]string, 0, len(ext.Content))
	for _, item := range ext.Content {
		names = append(names, item.XMLName.Local)
	}

	return nameList(names)
}

func boolInt(b bool) int {
	if b {
		return 1
	}

	return 0
}

// recordScope reads the per-task record scoping out of a task's extensions. A task
// carrying none is RecordScopeStandard, which is what TS 33.128 clause 6.2.2.2.1 means
// by the identifier-association records not being generated.
//
// It runs after unhonourableTaskFields has passed, so an extension present here is one
// with a 3GPP owner, recognised content and an enumerated value.
func recordScope(td TaskDetails) types.RecordScope {
	for _, ext := range td.TaskDetailsExtensions {
		if ext.IdentifierAssociation != nil {
			return types.RecordScope(ext.IdentifierAssociation.EventsGenerated)
		}
	}

	return types.RecordScopeStandard
}

// taskFailureCode is the registry's "generic ActivateTask/ModifyTask failure", whose
// suggested content is the reason the task cannot be applied. More specific than 1000,
// which says only that something went wrong.
func taskFailureCode(isModify bool) int {
	if isModify {
		return errCodeModifyFailed
	}

	return errCodeActivateFailed
}

// codedError pairs a refusal with the TS 103 221-1 error code that names it, so a code
// chosen where the reason is known reaches the response instead of being flattened to
// the generic one on the way out. Table 6.7-3 ends "Implementers shall use the most
// specific error code available", and an ADMF acts on the code before it reads the text.
type codedError struct {
	code int
	err  error
}

// Error returns the refusal's description, which becomes the response's
// ErrorDescription; the code travels separately.
func (e codedError) Error() string { return e.err.Error() }

// errorCode returns the TS 103 221-1 code an error carries, or 0 when it carries none.
func errorCode(err error) int {
	var c codedError
	if errors.As(err, &c) {
		return c.code
	}

	return 0
}

// refuse builds a refusal carrying a specific code.
//
// **What a refusal's description may quote.** Every description travels back to the peer,
// so it names what was refused -- the element, an enumeration token or extension owner, or
// a destination identifier the provisioning function itself assigned -- which the peer
// needs in order to see its own mistake. It never quotes a subscriber identity or an
// address, which an LI channel must not reflect, nor a malformed xId, dId or productID,
// which could be anything; the ADMF knows what it sent. Quoted text is escaped like every
// other rendered string.
func refuse(code int, format string, args ...any) error {
	return codedError{code: code, err: fmt.Errorf(format, args...)}
}

// supportedVersion is the X1 interface version this element speaks, and the value it
// substitutes when a peer's is not one the schema admits.
const supportedVersion = "v1.6.1"

// x1TemplateFuncs is what every X1 template is given: XML escaping, and the interface
// version this element speaks.
//
// version is a function rather than a literal in each template because the answers this
// element receives are checked against the constant (see validateBound): with a literal per
// template, bumping the constant would make every request state one version and every
// answer be checked against another, in a way no single template reads as wrong.
var x1TemplateFuncs = template.FuncMap{
	"esc":     escapeXML,
	"version": func() string { return supportedVersion },
}

// versionPattern is the schema's Version restriction, `v1\.\d+\.\d+`.
var versionPattern = regexp.MustCompile(`^v1\.\d+\.\d+$`)

// echoTransactionID keeps a peer from choosing a header value that makes *our own* answer
// invalid.
//
// Every response echoes the request's x1TransactionId, and the schema restricts it to a
// UUID. A malformed one would otherwise produce a malformed reply, and the reply easiest to
// spoil is the one whose request has been trusted by nothing: the refusal telling a peer it
// may not task this element. A conformant ADMF validating replies discards an invalid one.
//
// The version needs no such guard: a response states the version this element encoded it
// in (TS 103 221-1 table 6.1-1, "Version of the present document used for encoding the
// message"), which is supportedVersion whatever the request said.
//
// Substituting the transaction identifier does cost something — an ADMF matches a response
// to its request by this value — so it is worth being explicit about who pays. Only a peer
// that sent a non-conformant identifier is affected, and it has no conformant correlation to
// lose; a conformant ADMF's value is echoed untouched. Set against that, echoing the
// malformed value invalidates the whole response, which loses the correlation *and*
// everything else the message says. A fresh value is chosen rather than a fixed one so that
// two refusals to the same peer stay distinguishable.
func echoTransactionID(id string) string {
	if types.ValidUUID(id) {
		return id
	}

	return newUUID()
}

// validIdentifier reports whether an identifier conforms to the UUID format the schema
// defines for it. field names the element, so an ADMF can tell which of a message's
// identifiers it got wrong.
//
// Three of them are `etsi103280:UUID`: a destination's `dId`, a task's `xId`, and the
// `productID` that replaces the XID in delivered PDU headers. The `xId` is the sharpest
// case: `types.XID.Bytes()` maps a non-canonical XID to sixteen zero bytes, an MDF discards
// product it cannot attribute to a warrant, and nothing reports either -- so a task with a
// malformed `xId` would be accepted, run, and deliver every record under a label the
// mediation function throws away. Refusing at the door is the only place that failure is
// visible.
func validIdentifier(field, value string) error {
	if !types.ValidUUID(value) {
		// A value, so not quoted: see refuse.
		return refuse(errCodeSchemaError,
			"%s is not a UUID as the schema requires", field)
	}

	return nil
}

// deliveryProducts maps a deliveryType onto the products it orders.
//
// DeliveryType is a closed enumeration in the schema, so a value outside it is the
// message violating its own type: 1010, on a task and a destination alike, rather than
// "cannot be applied here" (3000) or "something went wrong" (1000).
func deliveryProducts(dt string) ([]types.ProductType, error) {
	switch dt {
	case deliveryX2Only:
		return []types.ProductType{types.ProductIRI}, nil
	case deliveryX3Only:
		return []types.ProductType{types.ProductCC}, nil
	case deliveryX2andX3:
		return []types.ProductType{types.ProductIRI, types.ProductCC}, nil
	}
	return nil, refuse(errCodeSchemaError, "deliveryType %q is not one of the enumerated values", dt)
}

// localType strips any namespace prefix from an xsi:type value
// ("ns1:ActivateTaskRequest" -> "ActivateTaskRequest").
func localType(t string) string {
	if i := strings.LastIndex(t, ":"); i >= 0 {
		return t[i+1:]
	}
	return t
}
