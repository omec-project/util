// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAReportInFlightWhenItsTaskEndsRecordsNothing: a task's first fault report is on the
// wire when the task is deactivated. The report settles after the task is gone, and
// recording it then would leave a fault under an XID the ADMF has deactivated — inherited by
// a task re-activated under that XID, which the watcher would then send an AllClear for a
// fault it never had.
func TestAReportInFlightWhenItsTaskEndsRecordsNothing(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	admf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		close(arrived)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	}))
	t.Cleanup(admf.Close)

	r := NewReporter(admf.URL, "admfID", "neID", nil)
	k := reportKey{scope: scopeTask, id: string(testXID), condition: TaskIssueDuplicationNotProgrammed}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.NotifyTaskFault(string(testXID), TaskIssueDuplicationNotProgrammed, "a test")
	}()

	<-arrived
	r.forgetTask(string(testXID)) // the task is deactivated while its report is on the wire
	close(release)
	<-done

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reported[k] {
		t.Error("a report that settled after its task ended was recorded, so a task re-activated " +
			"under the same XID inherits a fault it never had")
	}
	if r.forgotten[k] || r.sending[k] {
		t.Errorf("the key was left marked (forgotten=%v sending=%v) after the report settled",
			r.forgotten[k], r.sending[k])
	}
}
