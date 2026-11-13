// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingADMF is an ADMF a test can make hold a request open, refuse it, or answer
// it. The fault channel's new obligations are all about what it learns from its own
// sends, so a stub that only records what arrived cannot drive any of them.
type blockingADMF struct {
	srv *httptest.Server

	// A cancellable context as the release gate: cancel broadcasts to every held handler
	// and is idempotent, so the cleanup can call it whether or not the test already has.
	release context.Context
	unhold  context.CancelFunc

	status   atomic.Int32
	hold     atomic.Bool
	inFlight atomic.Int32
	peak     atomic.Int32
	total    atomic.Int32
}

func newBlockingADMF(t *testing.T) *blockingADMF {
	t.Helper()

	a := &blockingADMF{}
	a.release, a.unhold = context.WithCancel(context.Background())
	a.status.Store(http.StatusOK)
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		n := a.inFlight.Add(1)
		for {
			peak := a.peak.Load()
			if n <= peak || a.peak.CompareAndSwap(peak, n) {
				break
			}
		}
		a.total.Add(1)

		if a.hold.Load() {
			<-a.release.Done()
		}
		a.inFlight.Add(-1)
		status := int(a.status.Load())
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
		}
	}))
	// Unblock before Close: Close waits for handlers to return, so a held request
	// would deadlock the cleanup rather than fail the test.
	t.Cleanup(func() {
		a.unblock()
		a.srv.Close()
	})

	return a
}

func (a *blockingADMF) unblock() {
	a.hold.Store(false)
	a.unhold()
}

// awaitTotal waits for n requests to have reached the ADMF.
func (a *blockingADMF) awaitTotal(t *testing.T, n int32) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.total.Load() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("only %d reports reached the ADMF, want %d", a.total.Load(), n)
}

// awaitSettled waits for the reporter to have finished with k, so a test can ask what
// the send established rather than what it attempted.
func awaitSettled(t *testing.T, r *Reporter, k reportKey) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		sending := r.sending[k]
		r.mu.Unlock()
		if !sending {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the report never settled")
}

// fixedClock gives a test control of the throttle window without sleeping through it.
func fixedClock(r *Reporter) (advance func(time.Duration)) {
	var (
		mu  sync.Mutex
		now = time.Now().UTC()
	)
	r.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		return now
	}

	return func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
}

// TestAConditionAtPacketRateCostsOneAttempt is the property that makes NotifyAsync
// usable where `go Notify` is not, and it is a property of the *ordering* of the
// throttle against the dispatch rather than of either on its own.
//
// The UPF reports x3FramingLost from the loop that receives copies, so the condition
// recurs per dropped packet. Spawning first and deciding inside the goroutine would
// spawn one per copy — each taking the reporter's mutex, discovering it is throttled
// and exiting — which trades a stall for unbounded goroutine churn on the one path
// that must stay cheap. Deciding first costs a mutex for every copy after the first.
//
// The ADMF holds its answer throughout, because that is when the distinction is
// visible: while an attempt is outstanding the key stays reserved, so the burst
// produces exactly one attempt however long the ADMF takes.
func TestAConditionAtPacketRateCostsOneAttempt(t *testing.T) {
	admf := newBlockingADMF(t)
	admf.hold.Store(true)

	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
	advance := fixedClock(r)

	const burst = 10000

	goroutinesBefore := runtime.NumGoroutine()
	peakGoroutines := 0
	sample := func() {
		if n := runtime.NumGoroutine() - goroutinesBefore; n > peakGoroutines {
			peakGoroutines = n
		}
	}

	// **Sampled during the burst and not after it**, which is the difference between
	// a test that drives this property and one that only looks as if it does. The
	// goroutines `go Notify` spawns are short-lived — each finds the key reserved,
	// takes the mutex and exits — so a count taken once the loop has finished sees
	// them already gone and passes against the shape that was rejected. Measured this
	// way the two are two orders of magnitude apart: 1 against roughly 160.
	//
	// A synchronous reporter never returns from the first of these at all, because the
	// ADMF is holding it: that failure mode is a hung test, which is precisely what
	// the read loop would do to the datapath queue behind it.
	start := time.Now()
	for i := range burst {
		r.NotifyAsync(NEIssueX3FramingLost, "content copies dropped before framing")
		if i%10 == 0 {
			sample()
		}
	}
	elapsed := time.Since(start)
	sample()

	if elapsed > 2*time.Second {
		t.Errorf("%d reports cost the observing path %s; it must not wait on the ADMF", burst, elapsed)
	}
	// Generous, because it is a mutation detector and not a budget: the dispatch holds this
	// at 1, and an unbounded `go Notify` would spawn one per copy.
	if peakGoroutines > 25 {
		t.Errorf("the burst reached %d concurrent goroutines; the dispatch is not bounded by the condition", peakGoroutines)
	}

	admf.awaitTotal(t, 1)
	if peak := admf.peak.Load(); peak != 1 {
		t.Errorf("%d concurrent attempts reached the ADMF, want 1", peak)
	}

	admf.unblock()
	k := reportKey{scope: scopeElement, condition: NEIssueX3FramingLost}
	awaitSettled(t, r, k)

	// The window starts at the delivered send, so the condition recurring inside it is
	// still one report.
	r.NotifyAsync(NEIssueX3FramingLost, "content copies dropped before framing")
	awaitSettled(t, r, k)
	if total := admf.total.Load(); total != 1 {
		t.Errorf("%d reports inside one throttle window, want 1", total)
	}

	// And past it, the same condition is reported again: a throttle is a rate limit,
	// not a record that the fault has been dealt with.
	advance(reportThrottle + time.Second)
	r.NotifyAsync(NEIssueX3FramingLost, "content copies dropped before framing")
	admf.awaitTotal(t, 2)
}

// TestAFailedReportIsNotRecordedAsMade pins the accuracy half: the element's account
// of what it has told the ADMF must not diverge from what the ADMF heard.
//
// Committing in front of the send would make it diverge twice over. The condition would be
// suppressed for a window although nothing was delivered, and — the lasting half — it
// would enter the reported set, so if it then cleared, the element would retract a fault
// the ADMF had never been told about.
func TestAFailedReportIsNotRecordedAsMade(t *testing.T) {
	admf := newBlockingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	admf.status.Store(http.StatusInternalServerError)
	r.NotifyElementFault(NEIssueMDFUnreachable, "MDF3 delivery failing")
	admf.awaitTotal(t, 1)

	// The condition goes away before anyone re-observes it. Nothing may be retracted,
	// because there is nothing at the ADMF for a retraction to refer to.
	sent := admf.total.Load()
	r.NotifyElementClear(NEIssueMDFUnreachable)
	if got := admf.total.Load(); got != sent {
		t.Errorf("a fault the ADMF never received was retracted (%d messages, want %d)", got, sent)
	}

	// And the same condition observed again is reported again rather than suppressed
	// as a repeat of the report that failed. No clock advance: the window never
	// started, because nothing was delivered.
	admf.status.Store(http.StatusOK)
	r.NotifyElementFault(NEIssueMDFUnreachable, "MDF3 delivery failing")
	admf.awaitTotal(t, sent+1)
}

// TestAFailedRetractionLeavesTheFaultRetractable is the other direction, and the one
// with no self-correction: a fault is re-observed and reported again, but nothing
// re-observes a fault that has gone away. Forgetting it in front of the send would leave
// the ADMF holding a fault this element believed it had cleared and would never mention.
func TestAFailedRetractionLeavesTheFaultRetractable(t *testing.T) {
	admf := newBlockingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	r.NotifyElementFault(NEIssueX3EgressDown, "X3 egress socket unavailable")
	admf.awaitTotal(t, 1)

	admf.status.Store(http.StatusInternalServerError)
	r.NotifyElementClear(NEIssueX3EgressDown)
	admf.awaitTotal(t, 2)

	admf.status.Store(http.StatusOK)
	r.NotifyElementClear(NEIssueX3EgressDown)
	admf.awaitTotal(t, 3)

	// Retracted once it has actually been retracted, and not before: a fourth attempt
	// finds nothing to clear.
	before := admf.total.Load()
	r.NotifyElementClear(NEIssueX3EgressDown)
	if got := admf.total.Load(); got != before {
		t.Errorf("a fault already retracted was retracted again (%d messages, want %d)", got, before)
	}
}

// admfResponse is the answer a conformant ADMF returns to a report: the response type
// derived from the request's, the five fields of the schema's X1ResponseMessage base
// type — the sender's echoed back, its own stated — and the oK every report's response
// type carries.
//
// A 200 with an empty body is not a response any conformant ADMF sends, and the reporter
// refuses it as unattributable: an element that read a bare 200 as acceptance would record a
// fault as reported, stop re-reporting it, and later retract a fault the ADMF never had. A
// stub that is not conformant tests this element against a fiction.
//
// It runs on the stub's HTTP handler goroutine, where t.FailNow is not permitted, so a request
// it cannot parse fails the test with Errorf and is answered with nothing.
func admfResponse(t *testing.T, request []byte) string {
	t.Helper()

	m, ok := parseReportRequest(t, request)
	if !ok {
		return ""
	}

	return admfEnvelope(m, strings.TrimSuffix(m.local, "Request")+"Response", `<x1:oK>AcknowledgedAndCompleted</x1:oK>`)
}

// admfErrorResponse is the same envelope carrying an X1-level refusal, which clause 7.2.2.2
// requires to be returned inside a HTTP 200: an ErrorResponse naming the request type it
// refuses, as the schema requires of one, and the error information.
func admfErrorResponse(t *testing.T, request []byte, code int, description string) string {
	t.Helper()

	m, ok := parseReportRequest(t, request)
	if !ok {
		return ""
	}

	return admfEnvelope(m, "ErrorResponse",
		`<x1:requestMessageType>`+strings.TrimSuffix(m.local, "Request")+`</x1:requestMessageType>`+
			`<x1:errorInformation><x1:errorCode>`+strconv.Itoa(code)+`</x1:errorCode>`+
			`<x1:errorDescription>`+description+`</x1:errorDescription></x1:errorInformation>`)
}

// reportRequest is the part of a report an ADMF stub echoes back.
type reportRequest struct {
	local                                     string // the request type, without its prefix
	admf, ne, timestamp, version, transaction string
}

// parseReportRequest reads the one request message a report carries. It reports a failure
// with Errorf, for the handler goroutines it runs on.
func parseReportRequest(t *testing.T, request []byte) (reportRequest, bool) {
	t.Helper()

	var in struct {
		Messages []struct {
			Type            string `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
			AdmfIdentifier  string `xml:"admfIdentifier"`
			NeIdentifier    string `xml:"neIdentifier"`
			Timestamp       string `xml:"messageTimestamp"`
			Version         string `xml:"version"`
			X1TransactionID string `xml:"x1TransactionId"`
		} `xml:"x1RequestMessage"`
	}
	if err := xml.Unmarshal(request, &in); err != nil {
		t.Errorf("ADMF stub could not parse the report it is answering: %v", err)

		return reportRequest{}, false
	}
	if len(in.Messages) != 1 {
		t.Errorf("ADMF stub received %d request messages, want 1", len(in.Messages))

		return reportRequest{}, false
	}
	m := in.Messages[0]

	local := m.Type
	if i := strings.LastIndex(local, ":"); i >= 0 {
		local = local[i+1:]
	}

	return reportRequest{
		local: local, admf: m.AdmfIdentifier, ne: m.NeIdentifier,
		timestamp: m.Timestamp, version: m.Version, transaction: m.X1TransactionID,
	}, true
}

// admfEnvelope renders a response message of the given type answering m, with body after the
// base fields.
func admfEnvelope(m reportRequest, responseType, body string) string {
	return `<?xml version="1.0"?><x1:X1Response xmlns:x1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<x1:x1ResponseMessage xsi:type="x1:` + responseType + `">` +
		`<x1:admfIdentifier>` + m.admf + `</x1:admfIdentifier>` +
		`<x1:neIdentifier>` + m.ne + `</x1:neIdentifier>` +
		`<x1:messageTimestamp>` + m.timestamp + `</x1:messageTimestamp>` +
		`<x1:version>` + m.version + `</x1:version>` +
		`<x1:x1TransactionId>` + m.transaction + `</x1:x1TransactionId>` +
		body +
		`</x1:x1ResponseMessage></x1:X1Response>`
}
