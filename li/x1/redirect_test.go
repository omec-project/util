// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestOutboundX1RequestsDoNotFollowARedirect: a peer that answers 307 or 308 must not receive
// this element's request body a second time at an address of its choosing. net/http follows
// redirects by default and re-sends a POST's body on both codes; only the final hop's
// certificate would ever be checked against the peer this element meant to reach, after the
// body had gone. The redirect is the answer, and it is a failed request.
func TestOutboundX1RequestsDoNotFollowARedirect(t *testing.T) {
	var elsewhere atomic.Int32

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, code)
		}))
		t.Cleanup(redirector.Close)

		if err := NewRequester(redirector.URL, "admfID", "neID", nil).Keepalive(); err == nil {
			t.Errorf("a %d answer to a request was taken as success", code)
		}

		err := NewReporter(redirector.URL, "admfID", "neID", nil).
			ReportTaskIssue(NewUUID(), TaskReportNonTerminatingFault, "test")
		if err == nil {
			t.Errorf("a %d answer to a report was taken as delivered", code)
		}
	}

	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the redirect target received %d requests: a request body went to an address "+
			"the peer chose, not the one this element was configured with", n)
	}
}
