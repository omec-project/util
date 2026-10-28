// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// refusingTLSListener accepts TCP connections and closes them at once, so every TLS
// handshake — and every dial — fails. accepts counts the dials that reached it.
func refusingTLSListener(t *testing.T) (string, *atomic.Int64) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var accepts atomic.Int64

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_ = conn.Close()
		}
	}()

	return ln.Addr().String(), &accepts
}

// TestARefusingDestinationIsNotRedialledPerDelivery: after a failed dial, deliveries fail
// without dialling until minRedialInterval has passed, so an unreachable mediation function
// costs one dial per interval instead of one per batch at whatever rate product arrives.
func TestARefusingDestinationIsNotRedialledPerDelivery(t *testing.T) {
	addr, accepts := refusingTLSListener(t)
	c := clientTo(t, addr, KeepaliveConfig{Disabled: true})
	c.redialInterval = 300 * time.Millisecond

	for range 20 {
		err := c.Send(product())
		if err == nil {
			t.Fatal("a delivery to a destination that refuses every handshake succeeded")
		}
		if n := LostUnits(err); n != 1 {
			t.Errorf("a failed single delivery counts %d lost units, want 1", n)
		}
	}

	if n := accepts.Load(); n != 1 {
		t.Errorf("20 deliveries to a refusing destination dialled it %d times, want 1: every "+
			"delivery redialled, so an outage costs a dial and a handshake per batch", n)
	}
	if !c.Unreachable() {
		t.Error("a delivery refused without a dial left the destination reported reachable")
	}

	time.Sleep(c.redialInterval + 50*time.Millisecond)

	if err := c.Send(product()); err == nil {
		t.Fatal("the destination still refuses, but the delivery succeeded")
	}
	if n := accepts.Load(); n != 2 {
		t.Errorf("after the redial interval the destination was dialled %d times in all, want 2: "+
			"a destination that recovers must be tried again", n)
	}
}
