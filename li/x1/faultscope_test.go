// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// collectingADMF records every report an element sends it.
type collectingADMF struct {
	srv *httptest.Server

	mu     sync.Mutex
	bodies []string
}

func newCollectingADMF(t *testing.T) *collectingADMF {
	t.Helper()

	a := &collectingADMF{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		a.mu.Lock()
		a.bodies = append(a.bodies, string(body))
		a.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	}))
	t.Cleanup(a.srv.Close)

	return a
}

func (a *collectingADMF) reports() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.bodies...)
}

// counting returns how many recorded reports contain each of the given substrings.
func (a *collectingADMF) counting(substrings ...string) map[string]int {
	out := make(map[string]int, len(substrings))
	for _, body := range a.reports() {
		for _, s := range substrings {
			if strings.Contains(body, s) {
				out[s]++
			}
		}
	}

	return out
}

// reachability is a delivery destination whose reachability a test controls, answered
// from state it holds — the shape every supplier the watcher is used with answers from.
type reachability struct {
	down atomic.Bool
}

// TestAnElementReportNamesNoDestination: a report at network-element scope says that
// something is wrong with the element and never which destination, so an element with
// several provisioned destinations that needs to say which uses the destination scope
// (TestAFaultIsReportedAtDestinationScopeAndThenCleared).
func TestAnElementReportNamesNoDestination(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	if err := r.ReportNEIssue(NEIssueMDFUnreachable, "MDF3 X3 delivery failed"); err != nil {
		t.Fatalf("ReportNEIssue: %v", err)
	}

	reports := admf.reports()
	if len(reports) != 1 {
		t.Fatalf("sent %d reports, want 1", len(reports))
	}
	if !strings.Contains(reports[0], `xsi:type="ns1:ReportNEIssueRequest"`) {
		t.Errorf("not an element-scoped report:\n%s", reports[0])
	}
	if strings.Contains(reports[0], "dId") {
		t.Errorf("an element-scoped report names a destination:\n%s", reports[0])
	}
}

// TestAnElementReportIsThrottledPerCondition: a condition that recurs inside the throttle
// window is reported once — a persistent fault hit on every delivery must not flood the
// ADMF.
func TestAnElementReportIsThrottledPerCondition(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	for range 2 {
		if err := r.ReportNEIssue(NEIssueMDFUnreachable, "MDF3 X3 delivery failed"); err != nil {
			t.Fatalf("ReportNEIssue: %v", err)
		}
	}

	if n := admf.counting("mdfUnreachable")["mdfUnreachable"]; n != 1 {
		t.Errorf("one condition reported twice inside its window produced %d messages, want 1", n)
	}
}

// TestCarryingTheIdentifierDidNotChangeDelivery: carrying the destination identifier for
// reporting must not change delivery.
//
// Endpoints are deduplicated by address: two destination identifiers naming one
// address deliver one copy. Putting an identifier on the endpoint makes two
// entries distinguishable that must stay indistinguishable for delivery, and a
// second copy of a subject's traffic is exactly the failure that deduplication
// exists to prevent. So the identifier is carried for reporting and delivery keeps
// keying on address.
func TestCarryingTheIdentifierDidNotChangeDelivery(t *testing.T) {
	// One endpoint, provisioned twice under two identifiers — which is what an ADMF
	// does when two warrants go to one agency.
	task := types.InterceptTask{
		XID: "11111111-1111-4111-8111-111111111111",
		Deliveries: []types.DeliveryEndpoint{
			{Type: types.DeliveryX3, Address: "10.0.2.5:9000", DID: "33333333-3333-4333-8333-333333333333"},
			{Type: types.DeliveryX3, Address: "10.0.2.5:9000", DID: "44444444-4444-4444-8444-444444444444"},
		},
	}

	if addrs := task.DeliveryAddresses(types.DeliveryX3); len(addrs) != 1 {
		t.Errorf("one endpoint under two identifiers yields %d delivery addresses, want 1 — a subject's traffic would be copied twice", len(addrs))
	}

	// And both identifiers are still reachable, because a fault about that endpoint
	// concerns both destinations the provisioning function created.
	dids := task.DeliveryDIDs(types.DeliveryX3, "10.0.2.5:9000")
	if len(dids) != 2 {
		t.Fatalf("the endpoint resolves to %d destination identifiers, want 2", len(dids))
	}
}

// TestAProvisionedDestinationCarriesItsIdentifier: the identifier survives the
// boundary between provisioning and delivery.
func TestAProvisionedDestinationCarriesItsIdentifier(t *testing.T) {
	srv := NewServer(store.New(), "neID", WithADMF("admfID"))

	mustAck(t, srv, createDestinationXML(didAgencyA, deliveryX2andX3, "10.0.60.122", tcpPort("42069"), ""))
	mustAck(t, srv, activateXMLWith(string(testXID), deliveryX2andX3, dIDs(didAgencyA), ""))

	task, ok := srv.store.Get(testXID)
	if !ok {
		t.Fatal("the task was not stored")
	}
	if len(task.Deliveries) != 2 {
		t.Fatalf("an X2andX3 destination expanded to %d endpoints, want 2", len(task.Deliveries))
	}
	for _, d := range task.Deliveries {
		if d.DID != didAgencyA {
			t.Errorf("%s endpoint carries DID %q, want %q — the identifier is dropped at the boundary between provisioning and delivery",
				d.Type, d.DID, didAgencyA)
		}
	}

	// One address serving both interfaces is still one address per interface, so
	// nothing is delivered twice.
	if addrs := task.DeliveryAddresses(types.DeliveryX3); len(addrs) != 1 {
		t.Errorf("X3 resolves to %d addresses, want 1", len(addrs))
	}
}

// TestAFaultIsReportedAtDestinationScopeAndThenCleared, end to end at the reporter: a
// destination fails and the ADMF is told which one; it recovers and the ADMF is told
// that too.
func TestAFaultIsReportedAtDestinationScopeAndThenCleared(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	dests := &reachability{}
	health := func() []DestinationHealth {
		return []DestinationHealth{{DID: didAgencyA, Address: endpointA, Unreachable: dests.down.Load()}}
	}
	w := NewDestinationWatcher(health, r, time.Millisecond)
	clock := holdClockOn(w)

	// Healthy: nothing to say, and in particular no recovery from a fault nobody was
	// told about.
	w.sample()
	if n := len(admf.reports()); n != 0 {
		t.Fatalf("a healthy element sent %d reports, want 0", n)
	}

	dests.down.Store(true)
	w.sample()

	reports := admf.reports()
	if len(reports) != 1 {
		t.Fatalf("an unreachable destination produced %d reports, want 1", len(reports))
	}
	for _, want := range []string{
		`xsi:type="ns1:ReportDestinationIssueRequest"`,
		"<ns1:dId>" + didAgencyA + "</ns1:dId>",
		"<ns1:destinationReportType>" + TaskReportNonTerminatingFault + "</ns1:destinationReportType>",
	} {
		if !strings.Contains(reports[0], want) {
			t.Errorf("the fault report is missing %q\n%s", want, reports[0])
		}
	}

	dests.down.Store(false)
	clock.recover(t, w, admf)

	reports = admf.reports()
	if len(reports) != 2 {
		t.Fatalf("recovery produced %d reports in all, want 2 — clause 5.3 requires a fault that clears to be reported as cleared", len(reports))
	}
	if !strings.Contains(reports[1], "<ns1:destinationReportType>"+TaskReportAllClear+"</ns1:destinationReportType>") {
		t.Errorf("the clearing report does not carry AllClear\n%s", reports[1])
	}
	if !strings.Contains(reports[1], "<ns1:dId>"+didAgencyA+"</ns1:dId>") {
		t.Errorf("the clearing report does not name the destination it clears\n%s", reports[1])
	}
}

// TestRecoveryIsNotThrottledAgainstTheFailure. A fault beginning and that same
// fault clearing are two events, not a repetition. An element that throttled the
// second against the first would report a fault it never retracts — worse than
// reporting neither, because the ADMF acts on the first.
func TestRecoveryIsNotThrottledAgainstTheFailure(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	dests := &reachability{}
	health := func() []DestinationHealth {
		return []DestinationHealth{{DID: didAgencyA, Address: endpointA, Unreachable: dests.down.Load()}}
	}
	w := NewDestinationWatcher(health, r, time.Millisecond)
	clock := holdClockOn(w)

	// Fails and recovers well inside one throttle window: the recovery hold is the
	// watcher's interval, here far shorter than the reporter's throttle.
	dests.down.Store(true)
	w.sample()
	dests.down.Store(false)
	clock.recover(t, w, admf)

	if got := admf.counting(TaskReportAllClear)[TaskReportAllClear]; got != 1 {
		t.Errorf("the clearing was reported %d times, want 1 — the throttle suppressed a state change", got)
	}

	// And the same fault recurring immediately is reported again rather than
	// suppressed as a repeat of the one just retracted.
	dests.down.Store(true)
	w.sample()
	if got := admf.counting(TaskReportNonTerminatingFault)[TaskReportNonTerminatingFault]; got != 2 {
		t.Errorf("a fault that recurred after being cleared was reported %d times, want 2", got)
	}
}

// TestAPersistingFaultIsReportedOncePerWindow: the throttle suppresses repeats of
// a condition that simply continues, which is what it is for.
func TestAPersistingFaultIsReportedOncePerWindow(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := func() []DestinationHealth {
		return []DestinationHealth{{DID: didAgencyA, Address: endpointA, Unreachable: true}}
	}
	w := NewDestinationWatcher(health, r, time.Millisecond)

	for range 20 {
		w.sample()
	}

	if n := len(admf.reports()); n != 1 {
		t.Errorf("twenty samples of one persisting fault produced %d reports, want 1", n)
	}
}

// TestTwoDestinationsAreReportedSeparately: the throttle is keyed per destination, so two
// destinations failing inside one window are two reports and neither hides the other.
func TestTwoDestinationsAreReportedSeparately(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := func() []DestinationHealth {
		return []DestinationHealth{
			{DID: didAgencyA, Address: endpointA, Unreachable: true},
			{DID: didAgencyB, Address: "10.0.60.123:42069", Unreachable: true},
		}
	}
	NewDestinationWatcher(health, r, time.Millisecond).sample()

	got := admf.counting(didAgencyA, didAgencyB)
	if got[didAgencyA] != 1 || got[didAgencyB] != 1 {
		t.Errorf("two destinations failing produced %d and %d reports, want one each", got[didAgencyA], got[didAgencyB])
	}
}

// TestOneEndpointUnderTwoIdentifiersReportsBoth. The provisioning function's unit
// of action is the destination it created; reporting per endpoint would require it
// to know how this element resolves them.
func TestOneEndpointUnderTwoIdentifiersReportsBoth(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	task := types.InterceptTask{
		XID: testXID,
		Deliveries: []types.DeliveryEndpoint{
			{Type: types.DeliveryX3, Address: endpointA, DID: didAgencyA},
			{Type: types.DeliveryX3, Address: endpointA, DID: didAgencyB},
		},
	}
	health := func() []DestinationHealth {
		return DestinationHealthOf([]types.InterceptTask{task}, types.DeliveryX3,
			func(t types.InterceptTask) []string { return t.DeliveryAddresses(types.DeliveryX3) },
			func(string) bool { return true })
	}
	NewDestinationWatcher(health, r, time.Millisecond).sample()

	got := admf.counting(didAgencyA, didAgencyB)
	if got[didAgencyA] != 1 || got[didAgencyB] != 1 {
		t.Errorf("one unreachable endpoint under two identifiers produced %d and %d reports, want one each",
			got[didAgencyA], got[didAgencyB])
	}
}

// TestAConfiguredEndpointIsNotReportedAtDestinationScope: an endpoint this element
// resolved from its own configuration has no identifier the ADMF assigned, so
// there is nothing destination-scoped to say about it.
func TestAConfiguredEndpointIsNotReportedAtDestinationScope(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	// Through the join, for the reason given in TestAnEndpointWithNoIdentifierIsStillReported.
	health := func() []DestinationHealth {
		return DestinationHealthOf([]types.InterceptTask{{XID: testXID}}, types.DeliveryX2,
			func(types.InterceptTask) []string { return []string{endpointA} },
			func(string) bool { return true })
	}
	NewDestinationWatcher(health, r, time.Millisecond).sample()

	if n := admf.counting("ReportDestinationIssueRequest")["ReportDestinationIssueRequest"]; n != 0 {
		t.Errorf("an endpoint with no provisioned identifier produced %d destination-scoped reports, want 0", n)
	}
	// It is reported, at the scope that can be named — see
	// TestAnEndpointWithNoIdentifierIsStillReported.
	if n := len(admf.reports()); n != 1 {
		t.Errorf("it produced %d reports in all, want 1 at element scope", n)
	}
}

// TestTheJoinKeepsAnAddressProvisioningNeverNamed: the join enumerates identifiers, and an
// address the element delivers to that has none must still come back, or every layer
// above it is watching an empty list.
func TestTheJoinKeepsAnAddressProvisioningNeverNamed(t *testing.T) {
	// No Deliveries: this is what a task naming no DID looks like once resolveDIDs
	// has found nothing to resolve.
	got := DestinationHealthOf([]types.InterceptTask{{XID: testXID}}, types.DeliveryX2,
		func(types.InterceptTask) []string { return []string{endpointA} },
		func(string) bool { return true })

	if len(got) != 1 {
		t.Fatalf("an address with no provisioned identifier yielded %d entries, want 1: %+v", len(got), got)
	}
	if got[0].DID != "" {
		t.Errorf("it was given the identifier %q; there is none to give it", got[0].DID)
	}
	if got[0].Address != endpointA || !got[0].Unreachable {
		t.Errorf("got %+v, want the configured endpoint reported unreachable", got[0])
	}
}

// TestADestinationNamedByTwoTasksIsOneDestination. Reporting it once per task would
// tell the provisioning function about its own tasking rather than about its
// endpoint.
func TestADestinationNamedByTwoTasksIsOneDestination(t *testing.T) {
	shared := types.DeliveryEndpoint{Type: types.DeliveryX3, Address: endpointA, DID: didAgencyA}
	got := DestinationHealthOf([]types.InterceptTask{
		{XID: "11111111-1111-4111-8111-111111111111", Deliveries: []types.DeliveryEndpoint{shared}},
		{XID: "22222222-2222-4222-8222-222222222222", Deliveries: []types.DeliveryEndpoint{shared}},
	}, types.DeliveryX3,
		func(t types.InterceptTask) []string { return t.DeliveryAddresses(types.DeliveryX3) },
		func(string) bool { return true })

	if len(got) != 1 {
		t.Errorf("one destination named by two tasks yielded %d entries, want 1: %+v", len(got), got)
	}
}

// TestTheWatcherStopsWithTheElementItBelongsTo. A background goroutine that
// outlives its element is the shape of a process that leaks one per restart.
func TestTheWatcherStopsWithTheElementItBelongsTo(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})

	w := NewDestinationWatcher(func() []DestinationHealth { return nil }, nil, time.Millisecond)
	go func() {
		defer close(done)
		w.Watch(stop)
	}()

	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop when its element did")
	}
}

// TestTheStatusAnswerStillNamesNoDestination: the element names a destination in a
// destination-scoped report, and "the probe should name it too" reads as consistency. It
// is not: an element's own status says how much is wrong and never whose product is
// affected, and the probe takes counts precisely so that answering otherwise is
// impossible rather than merely discouraged. A destination-scoped report and a status
// answer address the same fact to different questions.
func TestTheStatusAnswerStillNamesNoDestination(t *testing.T) {
	probe := MDFUnreachableProbe(func() (int, int) { return 1, 2 })

	fault := probe()
	if fault == nil {
		t.Fatal("an unreachable destination is not reported in the status answer at all")
	}
	for _, secret := range []string{didAgencyA, didAgencyB, endpointA} {
		if strings.Contains(fault.ErrorDescription, secret) {
			t.Errorf("the status answer names %q: %s", secret, fault.ErrorDescription)
		}
	}
	if !strings.Contains(fault.ErrorDescription, "1 of 2") {
		t.Errorf("the status answer no longer says how many: %s", fault.ErrorDescription)
	}
}

// TestAnEndpointWithNoIdentifierIsStillReported: there is nothing destination-scoped to
// say about an endpoint the ADMF did not create, and it does not follow that the element
// should say *nothing*. A delivery it cannot make is exactly what the
// network-element-level condition is for, and every task delivering to this element's
// configured default endpoint has no DID — the commonest configuration there is.
//
// The scope changes with what can be named. Whether the fault is reported at all
// does not.
func TestAnEndpointWithNoIdentifierIsStillReported(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	dests := &reachability{}
	// Built through DestinationHealthOf rather than by hand, so the join upstream of the
	// watcher is exercised too: a hand-built DID-less entry would pin the watcher's branch
	// even if the join dropped the endpoint entirely. A task naming no destination
	// resolves no delivery record at all, so what stands in for production here is a task
	// with no Deliveries and a resolver that returns the element's configured endpoint,
	// which is what delivery does.
	task := types.InterceptTask{XID: testXID}
	health := func() []DestinationHealth {
		return DestinationHealthOf([]types.InterceptTask{task}, types.DeliveryX2,
			func(types.InterceptTask) []string { return []string{endpointA} },
			func(string) bool { return dests.down.Load() })
	}
	w := NewDestinationWatcher(health, r, time.Millisecond)
	clock := holdClockOn(w)

	dests.down.Store(true)
	w.sample()

	reports := admf.reports()
	if len(reports) != 1 {
		t.Fatalf("an unreachable configured endpoint produced %d reports, want 1 — the fault is reported by nobody", len(reports))
	}
	for _, want := range []string{
		`xsi:type="ns1:ReportNEIssueRequest"`,
		"<ns1:typeOfNeIssueMessage>" + neIssueFaultReport + "</ns1:typeOfNeIssueMessage>",
		NEIssueMDFUnreachable,
	} {
		if !strings.Contains(reports[0], want) {
			t.Errorf("the element-level report is missing %q\n%s", want, reports[0])
		}
	}
	// And it names no destination, because there is none to name.
	if strings.Contains(reports[0], "dId") {
		t.Errorf("an endpoint with no provisioned identifier was reported with one\n%s", reports[0])
	}

	// The ending too, at the same scope.
	dests.down.Store(false)
	clock.recover(t, w, admf)

	reports = admf.reports()
	if len(reports) != 2 {
		t.Fatalf("recovery produced %d reports in all, want 2", len(reports))
	}
	if !strings.Contains(reports[1], "<ns1:typeOfNeIssueMessage>"+neIssueFaultCleared+"</ns1:typeOfNeIssueMessage>") {
		t.Errorf("the clearing report is not a FaultCleared\n%s", reports[1])
	}
}

// TestAReportRefusedInsideA200IsNotRecordedAsDelivered is what "a fault is recorded as
// reported only once the provisioning function has been told" means on this transport.
//
// Clause 7.2.2.2 is explicit that HTTP codes indicate HTTP-level errors only and that an
// X1-level error "shall be … returned as a HTTP 200 OK response". A reporter that committed
// on the status alone would record a 200 wrapping an ErrorResponse as delivered: it would
// stop re-reporting the fault while it held, and then send an AllClear retracting a fault
// the ADMF had discarded. The two sides' lists of what is wrong would disagree, in the one
// direction neither can detect.
//
// Two properties: the fault stays eligible at the next observation, and no retraction is
// sent for a fault that was never accepted.
func TestAReportRefusedInsideA200IsNotRecordedAsDelivered(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
		refuse = true
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		mu.Lock()
		bodies = append(bodies, string(body))
		refusing := refuse
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		if refusing {
			// A refusal the specification requires to arrive inside a 200.
			_, _ = w.Write([]byte(admfErrorResponse(t, body, 1000, "not accepted"))) //nolint:errcheck // test handler

			return
		}
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	}))
	t.Cleanup(srv.Close)

	rep := NewReporter(srv.URL, "admfID", "neID", nil)

	// The condition is observed and reported, and the ADMF refuses it.
	rep.NotifyDestinationFault(didAgencyA, TaskReportTerminatingFault, "delivery failed")

	countRequests := func(want string) int {
		mu.Lock()
		defer mu.Unlock()

		n := 0
		for _, b := range bodies {
			if strings.Contains(b, want) {
				n++
			}
		}

		return n
	}
	if n := countRequests("ReportDestinationIssueRequest"); n != 1 {
		t.Fatalf("sent %d destination reports, want 1", n)
	}

	// The fault ends. Nothing may be retracted, because nothing was accepted: an AllClear
	// here retracts a fault the ADMF discarded, and an ADMF that receives a retraction for
	// a fault it never had cannot tell that from a fault it has lost track of.
	rep.NotifyDestinationClear(didAgencyA, TaskReportTerminatingFault)
	if n := countRequests(TaskReportAllClear); n != 0 {
		t.Errorf("sent %d retractions for a report the ADMF refused", n)
	}

	// And the condition is still eligible: observed again, it is reported again rather than
	// suppressed as a repeat of a report that never landed. The throttle is bypassed by
	// clearing lastSent, which is what a new observation window would do.
	rep.mu.Lock()
	rep.lastSent = map[reportKey]time.Time{}
	rep.mu.Unlock()

	mu.Lock()
	refuse = false
	mu.Unlock()

	rep.NotifyDestinationFault(didAgencyA, TaskReportTerminatingFault, "delivery failed")
	if n := countRequests("ReportDestinationIssueRequest"); n != 2 {
		t.Errorf("sent %d destination reports in total, want 2 — a fault the ADMF refused was "+
			"recorded as reported, so it is never told again while the condition holds", n)
	}

	// Now that one was accepted, the retraction goes.
	rep.NotifyDestinationClear(didAgencyA, TaskReportTerminatingFault)
	if n := countRequests(TaskReportAllClear); n != 1 {
		t.Errorf("sent %d retractions after an accepted report, want 1: a fault that ends must be "+
			"reported as having ended", n)
	}
}

// TestOneUnnamedDestinationDownLeavesTheFaultStanding is the aggregation, and it pins both
// orders because the outcome must not depend on the order a sample visits the endpoints:
// reported per entry, with one un-identified endpoint down and one healthy, whichever came
// last would decide whether the fault stood.
//
// An element-scoped report names no destination by construction, so there is exactly one
// state to be in: a fault while any such destination is unreachable, and a clear only when
// none is.
func TestOneUnnamedDestinationDownLeavesTheFaultStanding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		health []DestinationHealth
	}{
		{"the unreachable one first", []DestinationHealth{
			{DID: "", Unreachable: true},
			{DID: "", Unreachable: false},
		}},
		{"the healthy one first", []DestinationHealth{
			{DID: "", Unreachable: false},
			{DID: "", Unreachable: true},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admf := newCollectingADMF(t)
			rep := NewReporter(admf.srv.URL, "admfID", "neID", nil)

			w := NewDestinationWatcher(func() []DestinationHealth { return tc.health }, rep, 0)
			w.sample()

			counts := admf.counting(NEIssueMDFUnreachable, "FaultCleared")
			if counts[NEIssueMDFUnreachable] != 1 {
				t.Errorf("reported the fault %d times, want 1: one of this element's delivery "+
					"destinations is unreachable and the ADMF was not told",
					counts[NEIssueMDFUnreachable])
			}
			if counts["FaultCleared"] != 0 {
				t.Errorf("sent %d retractions while a destination is still unreachable: the "+
					"healthy endpoint's clear retracted the unreachable one's fault, and which of "+
					"the two won was decided by the order the sample visited them",
					counts["FaultCleared"])
			}
		})
	}
}

// And the other edge: once none of them is unreachable, the fault is retracted exactly once.
func TestTheUnnamedFaultClearsWhenNoneIsUnreachable(t *testing.T) {
	admf := newCollectingADMF(t)
	rep := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := []DestinationHealth{{DID: "", Unreachable: true}, {DID: "", Unreachable: false}}
	w := NewDestinationWatcher(func() []DestinationHealth { return health }, rep, 0)
	clock := holdClockOn(w)

	w.sample()
	if n := admf.counting(NEIssueMDFUnreachable)[NEIssueMDFUnreachable]; n != 1 {
		t.Fatalf("reported the fault %d times, want 1", n)
	}

	// Both recover.
	health[0].Unreachable = false
	clock.recover(t, w, admf)

	if n := admf.counting("FaultCleared")["FaultCleared"]; n != 1 {
		t.Errorf("sent %d retractions after every un-identified destination recovered, want 1: an "+
			"element that reports every beginning and no ending leaves an ADMF holding a list that "+
			"only grows", n)
	}
}
