// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// answeringAs is a stub NE whose answer is bound to the request — its transaction identifier,
// identities and version echoed — and is of the given response type, carrying payload. An empty
// responseType is the one the request calls for.
//
// It reports a request it cannot parse with Errorf, for the handler goroutine it runs on.
func answeringAs(t *testing.T, responseType, payload string) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler

		var in X1Request
		if err := xml.Unmarshal(body, &in); err != nil || len(in.Messages) != 1 {
			t.Errorf("stub could not read the request it is answering: %v", err)

			return
		}
		m := in.Messages[0]
		typ := responseType
		if typ == "" {
			typ = responseTypeFor(localType(m.Type))
		}

		//nolint:errcheck // test handler
		_, _ = w.Write([]byte(`<?xml version="1.0"?><X1Response xmlns="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
			`<x1ResponseMessage xsi:type="` + typ + `">` +
			`<admfIdentifier>` + m.AdmfIdentifier + `</admfIdentifier>` +
			`<neIdentifier>` + m.NeIdentifier + `</neIdentifier>` +
			`<messageTimestamp>` + m.MessageTimestamp + `</messageTimestamp>` +
			`<version>` + m.Version + `</version>` +
			`<x1TransactionId>` + m.X1TransactionID + `</x1TransactionId>` +
			payload +
			`</x1ResponseMessage></X1Response>`))
	})
}

// TestTheRequesterReadsOnlyTheSchemasAcknowledgements: the schema's OK type has two values, and
// only "AcknowledgedAndCompleted" says the request was done. "Acknowledged" says it was received
// and is pending — a triggering function that recorded its trigger as installed on it would
// believe in an interception the POI has not yet arranged — so it is ErrAcknowledgedPending,
// which a caller can tell from a refusal. Any other value is not an acknowledgement at all.
func TestTheRequesterReadsOnlyTheSchemasAcknowledgements(t *testing.T) {
	for _, c := range []struct {
		ok      string
		want    error // nil: success
		pending bool
	}{
		{ok: `<oK>AcknowledgedAndCompleted</oK>`},
		{ok: `<oK>Acknowledged</oK>`, pending: true},
		{ok: `<oK>Yes</oK>`, want: errors.New("refused")},
		{ok: `<oK>acknowledgedandcompleted</oK>`, want: errors.New("refused")},
		{ok: ``, want: errors.New("refused")},
	} {
		req, _ := requesterTo(t, answeringAs(t, "", c.ok))
		err := req.ActivateTask(testTrigger())

		switch {
		case c.pending:
			if !errors.Is(err, ErrAcknowledgedPending) {
				t.Errorf("%q: ActivateTask = %v, want ErrAcknowledgedPending", c.ok, err)
			}
		case c.want == nil:
			if err != nil {
				t.Errorf("%q: ActivateTask = %v, want success", c.ok, err)
			}
		default:
			if err == nil || errors.Is(err, ErrAcknowledgedPending) {
				t.Errorf("%q: ActivateTask = %v, want an error that is not the pending one", c.ok, err)
			}
		}
	}
}

// TestAPendingKeepaliveIsAnAnswer: a keepalive asks only whether the NE is there, and an
// "Acknowledged" answer says it is.
func TestAPendingKeepaliveIsAnAnswer(t *testing.T) {
	req, _ := requesterTo(t, answeringAs(t, "", `<oK>Acknowledged</oK>`))
	if err := req.Keepalive(); err != nil {
		t.Errorf("Keepalive answered Acknowledged = %v, want success", err)
	}
}

// TestAnErrorResponseWithoutAReasonIsRefused: the schema makes errorInformation mandatory on an
// ErrorResponse. One without it says nothing — and must not be read as the acknowledgement it
// is not, nor as a refusal with a code nobody gave — whatever else it carries: an oK, or a task
// list, is not what an ErrorResponse holds, and taking either would be reading an answer to a
// different question.
func TestAnErrorResponseWithoutAReasonIsRefused(t *testing.T) {
	const requestType = `<requestMessageType>ActivateTask</requestMessageType>`

	for _, payload := range []string{requestType, requestType + `<oK>AcknowledgedAndCompleted</oK>`} {
		req, _ := requesterTo(t, answeringAs(t, errorResponse, payload))
		err := req.ActivateTask(testTrigger())
		var refused *RequestError
		if err == nil || errors.As(err, &refused) {
			t.Errorf("ActivateTask answered by an ErrorResponse carrying %q = %v, want a malformed-answer error",
				payload, err)
		}
	}

	req, _ := requesterTo(t, answeringAs(t, errorResponse,
		`<requestMessageType>GetAllDetails</requestMessageType><listOfTaskResponseDetails/>`))
	if _, err := req.ReportedTasks(); err == nil {
		t.Error("ReportedTasks accepted an ErrorResponse without errorInformation")
	}
}

// TestAGetAllDetailsWithoutATaskListIsNotAnEmptyOne: listOfTaskResponseDetails is mandatory on
// GetAllDetailsResponse. An answer that omits it has not said the NE holds nothing, and a
// triggering function reconciling after a restart would otherwise conclude there is nothing to
// withdraw.
func TestAGetAllDetailsWithoutATaskListIsNotAnEmptyOne(t *testing.T) {
	const status = `<neStatusDetails><neStatus>OK</neStatus></neStatusDetails>`

	req, _ := requesterTo(t, answeringAs(t, "", status+`<listOfDestinationResponseDetails/>`))
	if _, err := req.ReportedTasks(); err == nil {
		t.Error("ReportedTasks accepted an answer without listOfTaskResponseDetails")
	}
	req, _ = requesterTo(t, answeringAs(t, "", status+`<listOfDestinationResponseDetails/>`))
	if _, err := req.TaskXIDs(); err == nil {
		t.Error("TaskXIDs accepted an answer without listOfTaskResponseDetails")
	}

	// And an empty list is an answer: the NE holds nothing.
	req, _ = requesterTo(t, answeringAs(t, "",
		status+`<listOfTaskResponseDetails/><listOfDestinationResponseDetails/>`))
	xids, err := req.TaskXIDs()
	if err != nil || len(xids) != 0 {
		t.Errorf("an empty task list read as %v, %v; want no tasks and no error", xids, err)
	}
}

// reportAnsweredWith is a Reporter whose ADMF answers every report with a bound envelope of the
// given type carrying payload, or the type the request calls for when responseType is empty.
func reportAnsweredWith(t *testing.T, responseType, payload string) *Reporter {
	t.Helper()

	admf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		m, ok := parseReportRequest(t, body)
		if !ok {
			return
		}
		typ := responseType
		if typ == "" {
			typ = strings.TrimSuffix(m.local, "Request") + "Response"
		}
		_, _ = w.Write([]byte(admfEnvelope(m, typ, payload))) //nolint:errcheck // test handler
	}))
	t.Cleanup(admf.Close)

	return NewReporter(admf.URL, "admfID", "neID", nil)
}

// TestAReportIsAcceptedOnlyByAnAcknowledgement: a report is committed — recorded as told, so it
// is not repeated and can later be retracted — only on an answer that accepts it. A bound
// response carrying neither an oK nor an error accepts nothing, and neither does a value outside
// the response type's OK enumeration. ReportNEIssueResponse's is OKAckAndComplete, which has no
// "Acknowledged"; the other two report types accept either value.
func TestAReportIsAcceptedOnlyByAnAcknowledgement(t *testing.T) {
	for _, c := range []struct {
		name    string
		payload string
		task    bool // the ReportTaskIssue answer is accepted
		ne      bool // the ReportNEIssue answer is accepted
	}{
		{"completed", `<x1:oK>AcknowledgedAndCompleted</x1:oK>`, true, true},
		{"acknowledged", `<x1:oK>Acknowledged</x1:oK>`, true, false},
		{"another value", `<x1:oK>Received</x1:oK>`, false, false},
		{"no oK", ``, false, false},
	} {
		r := reportAnsweredWith(t, "", c.payload)
		if err := r.ReportTaskIssue(taskID, TaskReportWarning, "details"); (err == nil) != c.task {
			t.Errorf("%s: ReportTaskIssue = %v, want accepted %v", c.name, err, c.task)
		}
		if err := r.ReportDestinationIssue("did", TaskReportWarning, "details"); (err == nil) != c.task {
			t.Errorf("%s: ReportDestinationIssue = %v, want accepted %v", c.name, err, c.task)
		}
		if err := r.reportNEIssueAs(NEIssueInvalidConfig, neIssueFaultReport, issueCodeNonTerminatingFault, "d"); (err == nil) != c.ne {
			t.Errorf("%s: ReportNEIssue = %v, want accepted %v", c.name, err, c.ne)
		}
	}

	r := reportAnsweredWith(t, errorResponse, `<x1:requestMessageType>ReportTaskIssue</x1:requestMessageType>`)
	if err := r.ReportTaskIssue(taskID, TaskReportWarning, "details"); err == nil {
		t.Error("a report answered by an ErrorResponse without errorInformation was accepted")
	}
}
