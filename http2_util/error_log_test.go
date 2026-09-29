// SPDX-FileCopyrightText: 2026 Martin Matyas
// SPDX-License-Identifier: Apache-2.0

package http2_util

import (
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestClosedBeforeHandshake(t *testing.T) {
	t.Parallel()

	cases := []struct {
		msg  string
		want bool
	}{
		{"http: TLS handshake error from 10.255.1.11:56078: EOF", true},
		{"http: TLS handshake error from [fd00::1]:40000: EOF", true},
		{"http: TLS handshake error from 10.255.1.11:56078: remote error: tls: bad certificate", false},
		{"http: TLS handshake error from 10.255.1.11:56078: tls: first record does not look like a TLS handshake", false},
		{"http: Accept error: accept tcp [::]:29510: accept4: too many open files; retrying in 5ms", false},
		{"http2: server: error reading preface from client 10.255.1.11:56078: EOF", false},
	}
	for _, c := range cases {
		if got := closedBeforeHandshake(c.msg); got != c.want {
			t.Errorf("closedBeforeHandshake(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestNewServerSetsErrorLog(t *testing.T) {
	t.Parallel()

	server, err := NewServer("127.0.0.1:0", "", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	if server.ErrorLog == nil {
		t.Fatal("NewServer must set ErrorLog, or net/http logs through the standard library logger")
	}
}

// TestServerErrorLogOnTLSServer drives a real TLS server: a connection closed
// before the handshake, as a TCP probe does, logs nothing, and a failed
// handshake still reaches the zap logger.
func TestServerErrorLogOnTLSServer(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zapcore.DebugLevel)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ts.Config.ErrorLog = log.New(serverErrorLogWriter{log: zap.New(core).Sugar()}, "", 0)
	ts.StartTLS()
	defer ts.Close()
	addr := ts.Listener.Addr().String()
	var dialer net.Dialer
	droppedBefore := DroppedHandshakeProbes()

	// A TCP probe: connect and close without a handshake.
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()

	// A peer that is not speaking TLS: the handshake fails and must be logged.
	conn, err = dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err = conn.Write([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for logs.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Give the probe's connection time to be logged too, had it not been dropped.
	time.Sleep(200 * time.Millisecond)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1 (the failed handshake only): %v", len(entries), entries)
	}
	if msg := entries[0].Message; !strings.HasPrefix(msg, handshakeErrorPrefix) || strings.HasSuffix(msg, closedBeforeHandshakeSuffix) {
		t.Fatalf("logged %q, want the failed handshake", msg)
	}
	if entries[0].Level != zapcore.ErrorLevel {
		t.Fatalf("logged at %s, want %s", entries[0].Level, zapcore.ErrorLevel)
	}

	deadline = time.Now().Add(5 * time.Second)
	for DroppedHandshakeProbes() == droppedBefore && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := DroppedHandshakeProbes() - droppedBefore; got != 1 {
		t.Fatalf("DroppedHandshakeProbes increased by %d, want 1", got)
	}
}
