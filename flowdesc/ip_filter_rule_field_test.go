// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package flowdesc

import (
	"testing"
)

func TestBuildIPFilterRuleFromField(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		configList   IPFilterRuleFieldList
		ipFilterRule string
	}{
		{
			name:         "default",
			configList:   IPFilterRuleFieldList{},
			ipFilterRule: "permit out ip from any to any",
		},
		{
			name: "srcIP",
			configList: IPFilterRuleFieldList{
				&IPFilterProto{
					Proto: 17,
				},
				&IPFilterSourceIP{
					Src: "192.168.0.0/24",
				},
			},
			ipFilterRule: "permit out 17 from 192.168.0.0/24 to any",
		},
		{
			name: "dstIP",
			configList: IPFilterRuleFieldList{
				&IPFilterProto{
					Proto: 17,
				},
				&IPFilterSourceIP{
					Src: "192.168.0.0/24",
				},
				&IPFilterDestinationIP{
					Src: "10.60.0.0/16",
				},
			},
			ipFilterRule: "permit out 17 from 192.168.0.0/24 to 10.60.0.0/16",
		},
		{
			name: "SinglePort",
			configList: IPFilterRuleFieldList{
				&IPFilterProto{
					Proto: 17,
				},
				&IPFilterSourceIP{
					Src: "192.168.0.0/24",
				},
				&IPFilterSourcePorts{
					Ports: "3000",
				},
				&IPFilterDestinationIP{
					Src: "10.60.0.0/16",
				},
			},
			ipFilterRule: "permit out 17 from 192.168.0.0/24 3000 to 10.60.0.0/16",
		},
		{
			name: "PortRange",
			configList: IPFilterRuleFieldList{
				&IPFilterProto{
					Proto: ProtocolNumberAny,
				},
				&IPFilterSourceIP{
					Src: "192.168.0.0/24",
				},
				&IPFilterSourcePorts{
					Ports: "3000",
				},
				&IPFilterDestinationIP{
					Src: "10.60.0.0/16",
				},
				&IPFilterDestinationPorts{
					Ports: "10000,65535",
				},
			},
			ipFilterRule: "permit out ip from 192.168.0.0/24 3000 to 10.60.0.0/16 10000,65535",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ipFilterRule, err := BuildIPFilterRuleFromField(tc.configList)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			filterRuleContent, err := Encode(ipFilterRule)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.ipFilterRule != filterRuleContent {
				t.Fatalf("expected %v, got %v", tc.ipFilterRule, filterRuleContent)
			}
		})
	}
}
