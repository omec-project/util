// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"crypto/tls"
	"io"
	"net"
	"sync/atomic"
	"testing"
)

// writeCountingConn counts the writes made through it once counting is switched on, and whether
// it was closed.
type writeCountingConn struct {
	net.Conn
	counting atomic.Bool
	writes   atomic.Int64
	closed   atomic.Bool
}

func (w *writeCountingConn) Write(b []byte) (int, error) {
	if w.counting.Load() {
		w.writes.Add(1)
	}

	return w.Conn.Write(b)
}

func (w *writeCountingConn) Close() error {
	w.closed.Store(true)

	return w.Conn.Close()
}

// handshakenPipe is a completed TLS session over a synchronous pipe whose far end drains, and the
// near end's transport, which counts writes once counting is switched on.
func handshakenPipe(t *testing.T) (*tls.Conn, *writeCountingConn) {
	t.Helper()

	near, far := net.Pipe()
	wire := &writeCountingConn{Conn: near}
	client := tls.Client(wire, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // test peer
	// No session tickets: over a synchronous pipe the server would block writing them to a
	// client that is not reading.
	server := tls.Server(far, &tls.Config{
		Certificates: []tls.Certificate{selfSignedServer(t)}, MinVersion: tls.VersionTLS12,
		SessionTicketsDisabled: true,
	})

	handshaken := make(chan error, 1)
	go func() { handshaken <- server.HandshakeContext(t.Context()) }()
	if err := client.HandshakeContext(t.Context()); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-handshaken; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, far) }() //nolint:errcheck // the peer drains until the pipe closes
	t.Cleanup(func() { _ = far.Close() })

	return client, wire
}

// TestAnAbandonedConnectionSendsNoCloseNotify: a connection dropped after a failure is closed
// without the close_notify alert closing a *tls.Conn would write first. Against a peer that has
// stopped reading that write waited out crypto/tls's five-second deadline, under the client's
// lock. The peer here reads, so the alert would be written at once and counted.
func TestAnAbandonedConnectionSendsNoCloseNotify(t *testing.T) {
	client, wire := handshakenPipe(t)

	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	t.Cleanup(func() { _ = c.Close() })
	heldConn(c, client)

	wire.counting.Store(true)
	c.mu.Lock()
	c.dropLocked()
	c.mu.Unlock()

	if n := wire.writes.Load(); n != 0 {
		t.Errorf("%d write(s) on a connection being abandoned: a close_notify was sent, which "+
			"waits out a five-second deadline against a peer that has stopped reading", n)
	}
	if !wire.closed.Load() {
		t.Error("the abandoned connection's transport was not closed")
	}
}

// TestCloseStillEndsTheSessionInOrder: Client.Close is the orderly end of a client, not an
// abandonment, so it still sends the close_notify a peer reading the session expects.
func TestCloseStillEndsTheSessionInOrder(t *testing.T) {
	client, wire := handshakenPipe(t)

	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	heldConn(c, client)

	wire.counting.Store(true)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if n := wire.writes.Load(); n != 1 {
		t.Errorf("%d write(s) as the client closed, want the one close_notify", n)
	}
	if !wire.closed.Load() {
		t.Error("the connection's transport was not closed")
	}
}
