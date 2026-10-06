// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package flowdesc

import (
	"testing"
)

func TestIPFilterRuleEncode(t *testing.T) {
	testStr1 := "permit out ip from any to assigned 655"

	rule := NewIPFilterRule()
	if rule == nil {
		t.Fatal("IP Filter Rule Create Error")
	}

	if err := rule.SetAction(Permit); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := rule.SetDirection(Out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := rule.SetSourceIP(ipAny); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := rule.SetDestinationIP(ipAssigned); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := rule.SetDestinationPorts("655"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := Encode(rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != testStr1 {
		t.Fatalf("Encode error, \n\t expect: %s,\n\t    get: %s", testStr1, result)
	}
}
