// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
)

// emptyRequestXML is an X1Request carrying no message, which the schema forbids: both the
// request container and the response require at least one.
const emptyRequestXML = `<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10"/>`

// TestARequestWithNoMessageIsATopLevelError: an X1Response with no message would not
// validate, so the clause 6.1 top-level error is the only well-formed answer.
func TestARequestWithNoMessageIsATopLevelError(t *testing.T) {
	srv := testServer(store.New())

	if _, err := srv.Process([]byte(emptyRequestXML), admfPeer(t)); err == nil {
		t.Fatal("a request carrying no message was processed")
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(emptyRequestXML)))

	if rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200: the fault is X1-level (clause 7.2.2.2)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "X1TopLevelErrorResponse") {
		t.Errorf("want the clause 6.1 top-level error, got:\n%s", rec.Body.String())
	}
}

// TestAnOversizedRequestIsRefusedWhole: a body over the limit is an HTTP-level error.
// Parsing the prefix that fits would answer a request the peer did not send.
func TestAnOversizedRequestIsRefusedWhole(t *testing.T) {
	srv := testServer(store.New())

	rec := httptest.NewRecorder()
	body := strings.NewReader(strings.Repeat(" ", maxRequestBytes+1))
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", body))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestOnlyPOSTIsAccepted: the 405 names the method that is allowed, as HTTP requires.
func TestOnlyPOSTIsAccepted(t *testing.T) {
	srv := testServer(store.New())

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want %q", got, http.MethodPost)
	}
}

// TestAMissingMandatoryIdentifierIsASchemaError: xId and dId are mandatory wherever these
// messages carry them, so a message without one violates its own schema — 1010, not the
// generic 1000, and not 2020 "no such task", which would send the ADMF to activate a task
// that would fail for the same reason.
func TestAMissingMandatoryIdentifierIsASchemaError(t *testing.T) {
	const taskWithoutXID = `
    <ns1:taskDetails>
      <ns1:targetIdentifiers>
        <ns1:targetIdentifier>
          <ns1:e164Number>2125552368</ns1:e164Number>
        </ns1:targetIdentifier>
      </ns1:targetIdentifiers>
      <ns1:deliveryType>X2andX3</ns1:deliveryType>
      <ns1:listOfDIDs>
        <ns1:dId>` + testDIDInActivateXML + `</ns1:dId>
      </ns1:listOfDIDs>
    </ns1:taskDetails>`

	for _, c := range []struct{ msgType, body string }{
		{"DeactivateTaskRequest", ""},
		{"GetTaskDetailsRequest", ""},
		{"GetDestinationDetailsRequest", ""},
		{"ActivateTaskRequest", taskWithoutXID},
		{"ModifyTaskRequest", taskWithoutXID},
	} {
		t.Run(c.msgType, func(t *testing.T) {
			m := serve(t, testServer(store.New()), string(request(c.msgType, c.body)))

			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Errorf("want a %d refusal, got %+v", errCodeSchemaError, m.ErrorInformation)
			}
		})
	}
}
