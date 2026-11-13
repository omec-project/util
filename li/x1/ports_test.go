// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"testing"

	"github.com/omec-project/util/li/store"
)

// TestEachPortArmIsHeldToItsOwnType: TS 103 280 types TCPPort as minExclusive 1 and UDPPort
// as minInclusive 0. A TCPPort of 0 or 1 is outside its type and malformed (1010); a UDPPort
// of 0 or 1 is inside its type but names no endpoint this element can deliver to -- X2 and X3
// are dialled over TCP, where both are outside the type -- so it is a destination that cannot
// be created (6000) rather than a malformed one. Acknowledged, a UDPPort of 1 was reported back
// as a TCPPort of 1, which made this element's own destination answers invalid.
func TestEachPortArmIsHeldToItsOwnType(t *testing.T) {
	for _, c := range []struct {
		name, port string
		code       int // 0 means acknowledged
	}{
		{"TCPPort 0", `<c:TCPPort>0</c:TCPPort>`, errCodeSchemaError},
		{"TCPPort 1", `<c:TCPPort>1</c:TCPPort>`, errCodeSchemaError},
		{"TCPPort 2", `<c:TCPPort>2</c:TCPPort>`, 0},
		{"UDPPort 0", `<c:UDPPort>0</c:UDPPort>`, errCodeCreateDestFailed},
		{"UDPPort 1", `<c:UDPPort>1</c:UDPPort>`, errCodeCreateDestFailed},
		{"UDPPort 2", `<c:UDPPort>2</c:UDPPort>`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := NewServer(store.New(), "neID")
			m := serve(t, srv, createDestinationXML(didAgencyA, deliveryX2Only, "10.0.60.122", c.port, ""))

			switch {
			case c.code == 0 && m.ErrorInformation != nil:
				t.Fatalf("refused %+v, want an acknowledgement", m.ErrorInformation)
			case c.code != 0 && (m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != c.code):
				t.Fatalf("got %+v, want error %d", m.ErrorInformation, c.code)
			}
		})
	}
}
