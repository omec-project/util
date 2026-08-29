// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package flowdesc

import (
	"testing"
)

const (
	testIP100 = "60.60.0.100"
	testIP1   = "60.60.0.1"
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

	if err := rule.SetProtocol(0xfc); err != nil {
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

func TestIPFilterRuleDecode(t *testing.T) {
	testCases := map[string]struct {
		filterRule string
		action     Action
		dir        Direction
		src        string
		srcPorts   string
		dst        string
		dstPorts   string
		proto      uint8
	}{
		"fully": {
			filterRule: "permit out ip from 60.60.0.100 8080 to 60.60.0.1 80",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP100,
			srcPorts:   "8080",
			dst:        testIP1,
			dstPorts:   "80",
		},
		"withoutPorts": {
			filterRule: "permit out ip from 60.60.0.100 to 60.60.0.1",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP100,
			srcPorts:   "",
			dst:        testIP1,
			dstPorts:   "",
		},
		"withoutOnePorts": {
			filterRule: "permit out ip from 60.60.0.100 8080 to 60.60.0.1",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP100,
			srcPorts:   "8080",
			dst:        testIP1,
			dstPorts:   "",
		},
		"withSrcAny": {
			filterRule: "permit out ip from any to 60.60.0.1 8080",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        ipAny,
			srcPorts:   "",
			dst:        testIP1,
			dstPorts:   "8080",
		},
		"withDstAny": {
			filterRule: "permit out ip from 60.60.0.1 8080 to any",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP1,
			srcPorts:   "8080",
			dst:        ipAny,
			dstPorts:   "",
		},
		"withAssigned": {
			filterRule: "permit out ip from assigned to 60.60.0.1 8080",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        ipAssigned,
			srcPorts:   "",
			dst:        testIP1,
			dstPorts:   "8080",
		},
	}

	for testName, expected := range testCases {
		t.Run(testName, func(t *testing.T) {
			r, err := Decode(expected.filterRule)
			if expected.action != r.GetAction() {
				t.Fatalf("expected action %v, got %v", expected.action, r.GetAction())
			}
			if expected.dir != r.GetDirection() {
				t.Fatalf("expected direction %v, got %v", expected.dir, r.GetDirection())
			}
			if expected.proto != r.GetProtocol() {
				t.Fatalf("expected protocol %v, got %v", expected.proto, r.GetProtocol())
			}
			if expected.src != r.GetSourceIP() {
				t.Fatalf("expected source IP %v, got %v", expected.src, r.GetSourceIP())
			}
			if expected.srcPorts != r.GetSourcePorts() {
				t.Fatalf("expected source ports %v, got %v", expected.srcPorts, r.GetSourcePorts())
			}
			if expected.dst != r.GetDestinationIP() {
				t.Fatalf("expected destination IP %v, got %v", expected.dst, r.GetDestinationIP())
			}
			if expected.dstPorts != r.GetDestinationPorts() {
				t.Fatalf("expected destination ports %v, got %v", expected.dstPorts, r.GetDestinationPorts())
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestIPFilterRuleSwapSourceAndDestination(t *testing.T) {
	testCases := map[string]struct {
		filterRule string
		action     Action
		dir        Direction
		src        string
		srcPorts   string
		dst        string
		dstPorts   string
		proto      uint8
	}{
		"fully": {
			filterRule: "permit out ip from 60.60.0.100 8080 to 60.60.0.1 80",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP1,
			srcPorts:   "80",
			dst:        testIP100,
			dstPorts:   "8080",
		},
		"withoutPorts": {
			filterRule: "permit out ip from 60.60.0.100 to 60.60.0.1",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP1,
			srcPorts:   "",
			dst:        testIP100,
			dstPorts:   "",
		},
		"withoutOnePorts": {
			filterRule: "permit out ip from 60.60.0.100 8080 to 60.60.0.1",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP1,
			srcPorts:   "",
			dst:        testIP100,
			dstPorts:   "8080",
		},
		"withSrcAny": {
			filterRule: "permit out ip from any to 60.60.0.1 8080",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP1,
			srcPorts:   "8080",
			dst:        ipAny,
			dstPorts:   "",
		},
		"withDstAny": {
			filterRule: "permit out ip from 60.60.0.1 8080 to any",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        ipAny,
			srcPorts:   "",
			dst:        testIP1,
			dstPorts:   "8080",
		},
		"withAssigned": {
			filterRule: "permit out ip from assigned to 60.60.0.1 8080",
			action:     Permit,
			dir:        Out,
			proto:      ProtocolNumberAny,
			src:        testIP1,
			srcPorts:   "8080",
			dst:        ipAssigned,
			dstPorts:   "",
		},
	}

	for testName, expected := range testCases {
		t.Run(testName, func(t *testing.T) {
			r, err := Decode(expected.filterRule)
			r.SwapSourceAndDestination()
			if expected.action != r.GetAction() {
				t.Fatalf("expected action %v, got %v", expected.action, r.GetAction())
			}
			if expected.dir != r.GetDirection() {
				t.Fatalf("expected direction %v, got %v", expected.dir, r.GetDirection())
			}
			if expected.proto != r.GetProtocol() {
				t.Fatalf("expected protocol %v, got %v", expected.proto, r.GetProtocol())
			}
			if expected.src != r.GetSourceIP() {
				t.Fatalf("expected source IP %v, got %v", expected.src, r.GetSourceIP())
			}
			if expected.srcPorts != r.GetSourcePorts() {
				t.Fatalf("expected source ports %v, got %v", expected.srcPorts, r.GetSourcePorts())
			}
			if expected.dst != r.GetDestinationIP() {
				t.Fatalf("expected destination IP %v, got %v", expected.dst, r.GetDestinationIP())
			}
			if expected.dstPorts != r.GetDestinationPorts() {
				t.Fatalf("expected destination ports %v, got %v", expected.dstPorts, r.GetDestinationPorts())
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
