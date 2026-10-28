// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

// selfSignedServer returns a self-signed server certificate for 127.0.0.1.
// (Mutual-auth behavior is covered by the mtls package; here we exercise the
// transport, so the client uses InsecureSkipVerify.)
func selfSignedServer(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mdf-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// readPDU reads one self-delimiting X2/X3 PDU from a stream, the way an MDF would.
func readPDU(conn net.Conn) (*PDU, error) {
	var lenHdr [12]byte // version(2)+type(2)+headerLen(4)+payloadLen(4)
	if _, err := io.ReadFull(conn, lenHdr[:]); err != nil {
		return nil, err
	}
	headerLen := binary.BigEndian.Uint32(lenHdr[4:8])
	payloadLen := binary.BigEndian.Uint32(lenHdr[8:12])
	rest := make([]byte, int(headerLen)+int(payloadLen)-len(lenHdr))
	if _, err := io.ReadFull(conn, rest); err != nil {
		return nil, err
	}
	p, _, err := Unmarshal(append(lenHdr[:], rest...))
	return p, err
}

func TestClientSendAndRedial(t *testing.T) {
	t.Parallel()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	received := make(chan *PDU, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					p, err := readPDU(c)
					if err != nil {
						return
					}
					received <- p
				}
			}(conn)
		}
	}()

	client := NewClient(ln.Addr().String(), &tls.Config{InsecureSkipVerify: true}, KeepaliveConfig{Disabled: true})
	defer client.Close()

	pdu := &PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable, Payload: []byte("xiri-payload")}

	recv := func(what string) *PDU {
		select {
		case p := <-received:
			return p
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for %s", what)
			return nil
		}
	}

	// First send: lazy connect.
	if err := client.Send(pdu); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if got := recv("first PDU"); got.Type != PDUTypeX2 || !bytes.Equal(got.Payload, pdu.Payload) {
		t.Errorf("first PDU = %+v", got)
	}

	// Second send: reuses the open connection.
	if err := client.Send(pdu); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	recv("second PDU (connection reuse)")

	// Close is terminal: a Send after it is refused rather than delivered over a fresh
	// connection.
	//
	// A closed client is one whose caller has finished with the destination: a purge, a
	// reconfiguration, an element shutting down. Delivering product to it afterwards, on a
	// connection opened *after* the decision to stop, is the delivery mechanism outliving
	// the authority for it. AsyncSender guards this for pooled callers too, but the client
	// should not depend on its wrapper for a property of its own lifecycle.
	client.Close()
	if err := client.Send(pdu); err == nil {
		t.Error("a Send after Close delivered product on a new connection")
	}

	select {
	case got := <-received:
		t.Errorf("the destination received a PDU after the client was closed: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSendMarshalError(t *testing.T) {
	// GTP-U is not allowed on an X2 PDU — Marshal fails, Send surfaces it before dialing.
	c := NewClient("127.0.0.1:1", &tls.Config{}, KeepaliveConfig{Disabled: true})
	if err := c.Send(&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormatGTPU, Direction: DirectionNotApplicable}); err == nil {
		t.Error("Send accepted an invalid PDU")
	}
	// And a PDU this element could not frame says nothing about the destination: no attempt
	// was made. Reporting the MDF unreachable for it would make an element faulty over a
	// fault of its own that the ADMF cannot act on.
	if c.Unreachable() {
		t.Error("an unframeable PDU left the destination reported as unreachable")
	}
}

// TestSendBatchReportsMarshalFailure: a PDU that cannot be framed is intercept
// product lost. The batch around it is still delivered, but the loss must be
// returned rather than swallowed — an AsyncSender turns that error into the ADMF
// fault report that is this plane's only way of saying anything.
func TestSendBatchReportsMarshalFailure(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	received := make(chan *PDU, 4)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			p, err := readPDU(conn)
			if err != nil {
				return
			}
			received <- p
		}
	}()

	client := NewClient(ln.Addr().String(), &tls.Config{InsecureSkipVerify: true}, KeepaliveConfig{Disabled: true})
	defer client.Close()

	good := &PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionNotApplicable, Payload: []byte{0x45, 0x00}}
	// SIP is an X2-only payload format, so this one cannot be framed as X3.
	bad := &PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatSIP, Direction: DirectionNotApplicable, Payload: []byte("INVITE")}

	if err := client.SendBatch([]*PDU{good, bad, good}); err == nil {
		t.Fatal("SendBatch discarded an unframeable PDU without reporting it")
	}

	// The rest of the batch must still have been delivered.
	for i := range 2 {
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for framable PDU %d of 2", i+1)
		}
	}
}

// TestSendBatchReportsANilPDU: a nil entry is treated as the loss it is, not as a
// reason to fault the network function. SendBatch is exported and dereferences a
// slice its caller owns, so this is the defence in depth behind AsyncSender.Send, which
// refuses a nil before it can be queued.
func TestSendBatchReportsANilPDU(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	received := make(chan *PDU, 4)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			p, err := readPDU(conn)
			if err != nil {
				return
			}
			received <- p
		}
	}()

	client := NewClient(ln.Addr().String(), &tls.Config{InsecureSkipVerify: true}, KeepaliveConfig{Disabled: true})
	defer client.Close()

	good := &PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionNotApplicable, Payload: []byte{0x45, 0x00}}

	if err := client.SendBatch([]*PDU{good, nil, good}); err == nil {
		t.Fatal("SendBatch dropped a nil PDU without reporting it")
	}

	for i := range 2 {
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for deliverable PDU %d of 2", i+1)
		}
	}
}

// TestUnreachableFollowsTheLastAttempt covers the state a POI's fault probe reports, on
// both edges — which are not symmetric in how they are noticed. Stuck off means an element
// that cannot deliver answers healthy, which is invisible and the reason the status answer
// exists at all. Stuck on means every element reports itself faulty, which discredits the
// field immediately.
//
// The destination is unreachable first and reachable second, so each answer follows an
// attempt whose outcome the test knows. Nothing clears the state explicitly: a delivery
// that succeeds is the only thing that can, and that is the property being pinned.
func TestUnreachableFollowsTheLastAttempt(t *testing.T) {
	// A free address with nothing on it: taking the listener away again is how the MDF is
	// "not running yet" for the first half of this test.
	free, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	if closeErr := free.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	client := NewClient(addr, &tls.Config{InsecureSkipVerify: true}, KeepaliveConfig{Disabled: true})
	defer client.Close()
	// The second delivery follows the failed one at once; what is pinned here is the flag
	// following each attempt, not the redial interval, which has a test of its own.
	client.redialInterval = 0

	if client.Unreachable() {
		t.Error("a client that has attempted no delivery reports its destination unreachable; " +
			"an element with nothing to send has not found the MDF unreachable, it has not looked")
	}

	pdu := &PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable, Payload: []byte("xiri-payload")}
	if sendErr := client.Send(pdu); sendErr == nil {
		t.Fatal("Send succeeded with nothing listening")
	}
	if !client.Unreachable() {
		t.Error("a failed delivery left the destination reported as reachable; an element " +
			"losing product would answer that nothing is wrong")
	}

	ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{selfSignedServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					if _, readErr := readPDU(c); readErr != nil {
						return
					}
				}
			}(conn)
		}
	}()

	if sendErr := client.Send(pdu); sendErr != nil {
		t.Fatalf("Send to a listening MDF: %v", sendErr)
	}
	if client.Unreachable() {
		t.Error("the destination is still reported unreachable after a delivered PDU; nothing " +
			"else clears this, so the element would stay faulty for the life of the process")
	}
}

// TestUnreachableDoesNotWaitForADeliveryInFlight is the rule a probe cannot break by
// accident: it answers from state, and never behind the lock a dial is held under.
//
// A probe runs on the X1 request goroutine. One that waited for a delivery in progress
// would hold a provisioning function's answer for as long as the dial timeout, and with a
// short enough timeout at the ADMF a working element would look dead while it was merely
// asking itself a slow question.
func TestUnreachableDoesNotWaitForADeliveryInFlight(t *testing.T) {
	c := NewClient("198.51.100.1:1", &tls.Config{}, KeepaliveConfig{Disabled: true})

	// Stands in for the dial or write a delivery is inside: both hold this, and a dial is
	// bounded by ten seconds, not by anything the ADMF would wait for.
	c.mu.Lock()
	defer c.mu.Unlock()

	answered := make(chan bool, 1)
	go func() { answered <- c.Unreachable() }()

	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("Unreachable blocked on a delivery in flight; a fault probe consulting it would " +
			"hold up the answer to a provisioning function")
	}
}
