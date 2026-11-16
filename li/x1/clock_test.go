// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
)

// TestTheDefaultClocksKeepTheMonotonicReading pins the property the keepalive watchdog and the
// report throttles depend on: the instants they subtract carry Go's monotonic clock reading, so
// a step in the wall clock cannot move them.
//
// time.Time.UTC strips that reading, so a clock returning time.Now().UTC() would make the
// watchdog's lapse a wall-clock difference: a forward step larger than the keepalive window — a
// VM resumed, an NTP step — would purge every warrant at the next tick while the ADMF was alive,
// and a backward step would keep a dead ADMF's tasking past the window. A stripped time compares
// equal to its own Round(0), which is how the reading is detected here.
func TestTheDefaultClocksKeepTheMonotonicReading(t *testing.T) {
	for name, now := range map[string]func() time.Time{
		"server":    NewServer(store.New(), "neID").now,
		"reporter":  NewReporter("https://admf.invalid", "admfID", "neID", nil).now,
		"requester": NewRequester("https://ne.invalid", "admfID", "neID", nil).now,
	} {
		// == rather than Equal, deliberately: Equal ignores the monotonic reading, which is
		// the one difference this compares.
		if got := now(); got == got.Round(0) { //nolint:staticcheck // see above
			t.Errorf("the %s's clock carries no monotonic reading, so its elapsed-time "+
				"comparisons follow the wall clock and a clock step can purge or keep tasking", name)
		}
	}
}

// TestTimestampsAreStillRenderedInUTC: UTC is applied in the one place an instant is formatted,
// so a clock in any zone produces the "Z" form the schema's timestamps use.
func TestTimestampsAreStillRenderedInUTC(t *testing.T) {
	stockholm := time.FixedZone("CEST", 2*60*60)
	at := time.Date(2026, 10, 22, 14, 0, 0, 120000*1000, stockholm)

	got := x1Timestamp(at)
	if want := "2026-10-22T12:00:00.120000Z"; got != want {
		t.Errorf("x1Timestamp(%v) = %q, want %q", at, got, want)
	}

	if strings.Contains(x1Timestamp(monotonicNow()), "+") {
		t.Error("the default clock's instant was rendered with a zone offset, not in UTC")
	}
}
