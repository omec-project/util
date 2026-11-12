// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"errors"
	"net"
	"testing"
)

// failingBatchSender is a batch-capable sender whose every delivery fails without saying how
// many units it cost, as a sender other than *Client might.
type failingBatchSender struct{}

func (failingBatchSender) Send(*PDU) error        { return errors.New("refused") }
func (failingBatchSender) SendBatch([]*PDU) error { return errors.New("refused") }
func (failingBatchSender) Close() error           { return nil }

// TestAFailedDeliveryCountsEveryUnitItLost: a failed delivery is accounted per product unit,
// not per call. The delivery worker writes up to maxDeliveryBatch units at once, so one error
// covers many units; counted per error, a thousand units lost to a refusing destination would
// read as a few dozen failures, and nothing could recover the number.
func TestAFailedDeliveryCountsEveryUnitItLost(t *testing.T) {
	free, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	_ = free.Close()

	five := []*PDU{product(), product(), product(), product(), product()}

	c := clientTo(t, addr, KeepaliveConfig{Disabled: true})
	if n := LostUnits(c.SendBatch(five)); n != 5 {
		t.Errorf("a batch of 5 to a destination that is not listening counts %d lost units, want 5", n)
	}

	c2 := clientTo(t, addr, KeepaliveConfig{Disabled: true})
	if n := LostUnits(c2.SendBatch([]*PDU{nil, product(), product(), product()})); n != 4 {
		t.Errorf("a batch of 3 deliverable units and one nil, none delivered, counts %d lost, want 4", n)
	}

	a := NewAsyncSender(failingBatchSender{}, 8, nil, nil)
	t.Cleanup(func() { _ = a.Close() })

	if n := LostUnits(a.send([]*PDU{product(), product(), product(), product()})); n != 4 {
		t.Errorf("a batch of 4 failed by a sender that gives no count counts %d lost, want 4", n)
	}
	if n := LostUnits(a.send([]*PDU{product()})); n != 1 {
		t.Errorf("a failed single unit counts %d lost, want 1", n)
	}

	f := NewAsyncSender(&failingFirstSender{}, 8, nil, nil)
	t.Cleanup(func() { _ = f.Close() })

	if n := LostUnits(f.send([]*PDU{product(), product(), product()})); n != 1 {
		t.Errorf("one failed unit of three, delivered one at a time, counts %d lost, want 1", n)
	}

	if !errors.Is(lostUnits(2, ErrUnitDropped), ErrUnitDropped) {
		t.Error("a counted error hides the failure it counts from errors.Is")
	}
}

// TestUnitsInCountsTheResentTail: when a write stops inside a unit and the resend fails, what
// is lost is every unit from that one to the end of the batch, not one unit.
func TestUnitsInCountsTheResentTail(t *testing.T) {
	ends := []int{10, 20, 30, 40}

	for _, tt := range []struct {
		name    string
		written int
		want    int
	}{
		{"stopped inside the second unit", 15, 3},
		{"stopped at a boundary", 20, 2},
		{"stopped inside the last unit", 35, 1},
		{"nothing written", 0, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := make([]byte, 40)
			rest := resumeAt(b, ends, tt.written)

			if got := unitsIn(ends, len(b), len(rest)); got != tt.want {
				t.Errorf("unitsIn = %d, want %d", got, tt.want)
			}
		})
	}

	if got := unitsIn(nil, 40, 40); got != 1 {
		t.Errorf("a single-unit write counts %d units, want 1", got)
	}
	if got := unitsIn(ends, 40, 0); got != 0 {
		t.Errorf("an empty tail counts %d units, want 0", got)
	}
}
