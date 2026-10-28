// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// TestAReconciliationAnswerLargerThanARequestIsRead: a GetAllDetailsResponse grows with the
// tasking an element holds, so reading it under the server's request limit would refuse the
// reconciliation answer of an element holding more than about 1,200 tasks — on every attempt,
// with an error that reads like a malformed peer.
func TestAReconciliationAnswerLargerThanARequestIsRead(t *testing.T) {
	const tasks = 2000

	st := store.New()
	for i := range tasks {
		st.Activate(types.InterceptTask{
			XID:      types.XID(NewUUID()),
			Targets:  []types.TargetIdentifier{{Type: types.TargetFSEID, Value: strconv.Itoa(i + 1)}},
			Products: []types.ProductType{types.ProductCC},
		})
	}
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), HonoursCorrelationID())
	peer := certWithUID(t, "smf-1")

	var answered atomic.Int64
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp, err := srv.Process(b, peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		out, _ := marshalResponse(resp) //nolint:errcheck // test handler
		answered.Store(int64(len(out)))
		_, _ = w.Write(out) //nolint:errcheck // test handler
	}))

	xids, err := req.TaskXIDs()
	if err != nil {
		t.Fatalf("TaskXIDs over %d tasks: %v", tasks, err)
	}
	if len(xids) != tasks {
		t.Errorf("reported %d tasks, want %d", len(xids), tasks)
	}
	if n := answered.Load(); n <= maxRequestBytes {
		t.Fatalf("the answer was %d bytes, not past the request limit; this test exercises nothing", n)
	}
}

// TestAnAnswerPastTheResponseLimitIsRefusedAsTooLarge: an answer over the limit is refused
// with an error saying so, rather than being cut at the limit and misreported as malformed.
func TestAnAnswerPastTheResponseLimitIsRefusedAsTooLarge(t *testing.T) {
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>`))               //nolint:errcheck // test handler
		_, _ = w.Write([]byte(strings.Repeat(" ", maxResponseBytes))) //nolint:errcheck // test handler
	}))

	_, err := req.ReportedTasks()
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want one saying the response exceeds the limit", err)
	}
}

// TestAPeerAnsweringInItsOwnVersionIsAccepted: table 6.1-1 defines Version as the version of
// the specification the sender used to encode the message, so a peer answers in its own
// version rather than echoing this element's. Within major version 1 (clause 4.5) that answer
// is readable and is accepted; another major version, or no version, is refused.
func TestAPeerAnsweringInItsOwnVersionIsAccepted(t *testing.T) {
	answering := func(version string) func(string) string {
		return func(s string) string {
			return strings.Replace(s, "<version>"+supportedVersion+"</version>", "<version>"+version+"</version>", 1)
		}
	}

	for _, v := range []string{"v1.21.1", "v1.6.2"} {
		req, _ := requesterTo(t, answeringWith(t, answering(v)))
		if err := req.ActivateTask(testTrigger()); err != nil {
			t.Errorf("an answer in version %s was refused: %v", v, err)
		}
	}

	for _, v := range []string{"v2.0.0", "", "1.21.1"} {
		req, _ := requesterTo(t, answeringWith(t, answering(v)))

		var re *ResponseError
		if err := req.ActivateTask(testTrigger()); !errors.As(err, &re) || re.Field != "version" {
			t.Errorf("an answer in version %q: err = %v, want a *ResponseError naming version", v, err)
		}
	}
}

// TestANon200AnswerNamesThePeerAndKeepsTheConnection: a non-200 answer is a transport fault
// whichever party gave it — an NE answering a requester, or an ADMF answering a reporter —
// and its body is drained so the connection carries the next request rather than being torn
// down. The body is larger than the transport's own read-ahead, so an undrained one costs a
// connection per request.
func TestANon200AnswerNamesThePeerAndKeepsTheConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.Repeat("x", 32<<10))) //nolint:errcheck // test handler
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	req := NewRequester(srv.URL, "smf-1", "upf-1", nil)
	for range 3 {
		err := req.Keepalive()
		if err == nil || !strings.Contains(err.Error(), "peer returned status 503") {
			t.Fatalf("err = %v, want one naming the peer's status", err)
		}
	}

	if n := conns.Load(); n != 1 {
		t.Errorf("three refused requests opened %d connections, want 1: the refusal's body was "+
			"not drained, so each request costs a new connection", n)
	}
}

// answeringADMF is an ADMF that accepts every report, over TLS, presenting the given
// certificate.
func answeringADMF(t *testing.T, cert tls.Certificate, pool *x509.CertPool) (*httptest.Server, *tls.Config) {
	t.Helper()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)                 //nolint:errcheck // test handler
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// TestAReportIsCommittedOnlyOnTheResponsibleADMFsWord: the reporter binds the ADMF's answer
// to its certificate in the ADMF role, as the requester does an NE's. An answer from another
// party inside the LI domain must not record a fault as reported, or the element stops
// re-reporting a fault the ADMF never heard.
func TestAReportIsCommittedOnlyOnTheResponsibleADMFsWord(t *testing.T) {
	const admfID = "admf-1"

	for _, tc := range []struct {
		name, binding string
		accepted      bool
	}{
		{"bound in the ADMF role", certBindingURNPrefix + roleADMF + ":" + admfID, true},
		{"bound in the NE role", certBindingURNPrefix + roleNE + ":" + admfID, false},
		{"bound to another ADMF", certBindingURNPrefix + roleADMF + ":admf-9", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, pool := liCert(t, "", tc.binding)
			srv, clientTLS := answeringADMF(t, cert, pool)

			r := NewReporter(srv.URL, admfID, "upf-1", clientTLS)
			err := r.ReportTaskIssue(NewUUID(), TaskReportNonTerminatingFault, "test")

			if tc.accepted && err != nil {
				t.Errorf("a report the responsible ADMF accepted was refused: %v", err)
			}
			if !tc.accepted {
				if err == nil {
					t.Fatal("a report was committed on the word of a party whose certificate does " +
						"not bind the responsible ADMF")
				}
				if !strings.Contains(err.Error(), "peer certificate") {
					t.Errorf("refused for the wrong reason: %v", err)
				}
			}
		})
	}
}

// TestNotifyTaskSendsOneTaskReport: the fire-and-forget form sends the report naming the task
// and discards only the outcome.
func TestNotifyTaskSendsOneTaskReport(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
	xid := NewUUID()

	r.NotifyTask(xid, TaskReportTerminatingFault, "trigger refused")

	reports := admf.reports()
	if len(reports) != 1 {
		t.Fatalf("sent %d reports, want 1", len(reports))
	}
	for _, want := range []string{
		`xsi:type="ns1:ReportTaskIssueRequest"`,
		"<ns1:xId>" + xid + "</ns1:xId>",
		"<ns1:taskReportType>" + TaskReportTerminatingFault + "</ns1:taskReportType>",
	} {
		if !strings.Contains(reports[0], want) {
			t.Errorf("the task report is missing %q\n%s", want, reports[0])
		}
	}
}

// TestNotifyTaskAsyncIsThrottledPerTaskAndCondition: the asynchronous form throttles per
// (xid, condition), so a condition recurring for one task is one report, while another
// condition about it, or the same condition about another task, is reported too.
func TestNotifyTaskAsyncIsThrottledPerTaskAndCondition(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
	xidA, xidB := NewUUID(), NewUUID()

	notify := func(xid, condition string) {
		r.NotifyTaskAsync(xid, TaskReportNonTerminatingFault, condition, condition+": detail")
		awaitSettled(t, r, reportKey{scope: scopeTask, id: xid, condition: condition})
	}
	notify(xidA, TaskIssueNoTrafficSelected)
	notify(xidA, TaskIssueNoTrafficSelected)
	notify(xidA, TaskIssueDuplicationNotProgrammed)
	notify(xidB, TaskIssueNoTrafficSelected)

	got := admf.counting(xidA, xidB)
	if got[xidA] != 2 || got[xidB] != 1 {
		t.Errorf("sent %d reports about the first task and %d about the second, want 2 and 1",
			got[xidA], got[xidB])
	}

	// Nothing to report against: no XID, or no reporter configured.
	before := len(admf.reports())
	r.NotifyTaskAsync("", TaskReportNonTerminatingFault, TaskIssueNoTrafficSelected, "x")
	(*Reporter)(nil).NotifyTaskAsync(xidA, TaskReportNonTerminatingFault, TaskIssueNoTrafficSelected, "x")
	if n := len(admf.reports()) - before; n != 0 {
		t.Errorf("sent %d reports with no task or no reporter, want 0", n)
	}
}

// TestANilReporterSendsNothing: every Reporter method is safe on a nil receiver, so a network
// function with no ADMF configured need not guard its report sites.
func TestANilReporterSendsNothing(t *testing.T) {
	var r *Reporter

	if err := r.ReportTaskIssue(NewUUID(), TaskReportNonTerminatingFault, "x"); err != nil {
		t.Errorf("ReportTaskIssue on a nil reporter: %v", err)
	}
	if err := r.ReportDestinationIssue(NewUUID(), TaskReportNonTerminatingFault, "x"); err != nil {
		t.Errorf("ReportDestinationIssue on a nil reporter: %v", err)
	}
	if err := r.ReportNEIssue(NEIssueMDFUnreachable, "x"); err != nil {
		t.Errorf("ReportNEIssue on a nil reporter: %v", err)
	}
	if err := r.reportNEIssueAs(NEIssueMDFUnreachable, neIssueFaultCleared, 0, "x"); err != nil {
		t.Errorf("reportNEIssueAs on a nil reporter: %v", err)
	}
}

// TestTheConditionsTableNamesEveryCondition: report.go's doc comment states, for every
// element-scoped condition, whether it is a state or an event and which mechanism carries
// it. A condition added without an entry is one whose mechanism nobody decided.
func TestTheConditionsTableNamesEveryCondition(t *testing.T) {
	src, err := os.ReadFile("report.go")
	if err != nil {
		t.Fatal(err)
	}

	for condition := range neIssueEncodings {
		row := regexp.MustCompile(`(?m)^//\t` + regexp.QuoteMeta(condition) + `\s+(state|event)\s+(probe|push|both)\s`)
		if !row.Match(src) {
			t.Errorf("%s has no row in the conditions table in report.go", condition)
		}
	}
}

// TestAnElementFaultMustNameADeclaredCondition: a probe's condition leads the description an
// ADMF correlates with pushed reports, so one outside the declared vocabulary is refused rather
// than invented.
func TestAnElementFaultMustNameADeclaredCondition(t *testing.T) {
	if got := NEFault(NEIssueMDFUnreachable, "1 of 2"); got.ErrorDescription != NEIssueMDFUnreachable+": 1 of 2" {
		t.Errorf("a declared condition rendered as %q", got.ErrorDescription)
	}
	if got := NEFault("somethingInvented", "detail"); strings.Contains(got.ErrorDescription, "somethingInvented") {
		t.Errorf("an undeclared condition was carried as %q", got.ErrorDescription)
	}
}
