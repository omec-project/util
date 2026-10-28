// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"crypto/tls"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// closingMDF is a mediation function that reads one PDU on its first connection and then
// closes it, as one restarting or behind an idle-timing load balancer does, and reads every
// PDU on any later connection. later counts what those later connections received.
type closingMDF struct {
	addr   string
	closed chan struct{}
	later  atomic.Int64
}

func newClosingMDF(t *testing.T) *closingMDF {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	m := &closingMDF{addr: ln.Addr().String(), closed: make(chan struct{})}

	go func() {
		first := true
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			if first {
				first = false

				go func(c net.Conn) {
					//nolint:errcheck // the close below is what this peer does either way
					_, _ = readPDU(c)
					_ = c.Close()
					close(m.closed)
				}(conn)

				continue
			}

			go func(c net.Conn) {
				defer c.Close()
				for {
					p, err := readPDU(c)
					if err != nil {
						return
					}
					if p.Type == PDUTypeX2 {
						m.later.Add(1)
					}
				}
			}(conn)
		}
	}()

	return m
}

// TestAConnectionThePeerClosedIsNotWrittenInto: a mediation function closes an idle
// connection, and the next deliveries must arrive or fail — never be written into the closed
// socket and reported delivered. The kernel accepts that write, the peer answers it with a
// reset, and only the write after it fails, so without the reader dropping the connection the
// first delivery after an idle close would vanish with a nil error.
//
// With the keepalive mechanism and without it: a mediation function that does not implement
// clause 6.2.4 is the reason the mechanism can be disabled, and it closes idle connections too.
func TestAConnectionThePeerClosedIsNotWrittenInto(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		ka   KeepaliveConfig
	}{
		{"keepalive running", KeepaliveConfig{}},
		{"keepalive disabled", KeepaliveConfig{Disabled: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newClosingMDF(t)
			c := clientTo(t, m.addr, tt.ka)

			if err := c.Send(product()); err != nil {
				t.Fatalf("the first delivery failed: %v", err)
			}

			select {
			case <-m.closed:
			case <-time.After(3 * time.Second):
				t.Fatal("the mediation function never closed the connection")
			}

			// Give the close time to arrive; the element is not required to have noticed
			// it within any bound, only to not lose a delivery over it.
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				c.mu.Lock()
				dropped := c.live == nil
				c.mu.Unlock()

				if dropped {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}

			delivered := 0
			for range 3 {
				if err := c.Send(product()); err == nil {
					delivered++
				}
				time.Sleep(50 * time.Millisecond)
			}

			eventually(t, "the deliveries reported as made to arrive", func() bool {
				return m.later.Load() >= int64(delivered)
			})
			time.Sleep(100 * time.Millisecond)

			if got := m.later.Load(); got != int64(delivered) {
				t.Errorf("%d deliveries after the mediation function closed the connection were "+
					"reported made, and %d arrived: a delivery written into a closed socket is lost "+
					"with no error, no drop and no fault", delivered, got)
			}
		})
	}
}
