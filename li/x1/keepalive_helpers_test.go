// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import "time"

// purgeIfLapsed clears all tasking when no X1 request has arrived within timeout. It is the
// purge the procedure performs, with the decision expressed as a window rather than as the
// instant of a fault report, so a test can drive the purge without running the procedure.
func (s *Server) purgeIfLapsed(timeout time.Duration) {
	s.purgeIfSilent(func(lastSeen, now time.Time) bool { return now.Sub(lastSeen) > timeout })
}
