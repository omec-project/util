// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
)

// TestABusyServerAnswers503: the server serves at most maxConcurrentRequests at once. A request
// beyond that waits for a slot, and is answered 503 only if none comes free in time.
func TestABusyServerAnswers503(t *testing.T) {
	post := func(srv *Server) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(activateXML)))

		return rec
	}
	busy := func(wait time.Duration) *Server {
		srv := testServer(store.New())
		srv.admissionWait = wait
		for range maxConcurrentRequests {
			srv.admission <- struct{}{}
		}

		return srv
	}

	t.Run("no slot comes free", func(t *testing.T) {
		rec := post(busy(10 * time.Millisecond))

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Error("no Retry-After")
		}
	})

	t.Run("a slot comes free while it waits", func(t *testing.T) {
		srv := busy(time.Minute)
		done := make(chan *httptest.ResponseRecorder)
		go func() { done <- post(srv) }()

		<-srv.admission

		if rec := <-done; rec.Code != http.StatusOK {
			t.Errorf("status %d, want 200", rec.Code)
		}
		if n := len(srv.admission); n != maxConcurrentRequests-1 {
			t.Errorf("%d slots held after the request, want %d", n, maxConcurrentRequests-1)
		}
	})
}

// TestASlotIsReleasedWhateverTheAnswer: a request answered with a top-level error, and one
// answered, leave no slot held.
func TestASlotIsReleasedWhateverTheAnswer(t *testing.T) {
	srv := testServer(store.New())
	for _, body := range []string{"not xml", activateXML} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body)))
	}

	if n := len(srv.admission); n != 0 {
		t.Errorf("%d slots held after the requests, want 0", n)
	}
}

// blockingWriter holds Write until release is closed, standing in for a peer that reads slowly.
type blockingWriter struct {
	*httptest.ResponseRecorder
	in, release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	close(b.in)
	<-b.release

	return b.ResponseRecorder.Write(p)
}

// TestASlotIsNotHeldWhileTheAnswerIsWritten: a peer reading its answer slowly holds no slot.
func TestASlotIsNotHeldWhileTheAnswerIsWritten(t *testing.T) {
	srv := testServer(store.New())
	w := &blockingWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(activateXML)))
		close(done)
	}()

	<-w.in
	if n := len(srv.admission); n != 0 {
		t.Errorf("%d slots held while the answer is written, want 0", n)
	}
	close(w.release)
	<-done
}

// TestABodyIsReadBeforeASlotIsTaken: with every slot held, a body too large is still refused as
// such, because the body is read before a slot is asked for.
func TestABodyIsReadBeforeASlotIsTaken(t *testing.T) {
	srv := testServer(store.New())
	srv.admissionWait = 10 * time.Millisecond
	for range maxConcurrentRequests {
		srv.admission <- struct{}{}
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/",
		strings.NewReader(strings.Repeat(" ", maxRequestBytes+1))))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}
