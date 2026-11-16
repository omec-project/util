// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// TestAnLIT3CriterionValueOutsideItsTypeIsRefused: every LI_T3 criterion value is held to its
// schema type, as the plain arms are. A value that is not one would be stored as a criterion
// no packet carries, and since every criterion must match, the task would be acknowledged and
// intercept nothing. A modelled arm beside one this element does not model is refused for the
// opposite reason: decoding would drop the unknown arm and apply fewer criteria than ordered,
// which widens the interception.
func TestAnLIT3CriterionValueOutsideItsTypeIsRefused(t *testing.T) {
	for _, c := range []struct {
		name, xml string
		code      int
	}{
		{
			"a modelled arm beside an unmodelled one",
			ext(`<ns2:FSEID><ns2:SEID>42</ns2:SEID></ns2:FSEID><ns2:NewCriterion>1</ns2:NewCriterion>`),
			errCodeSchemaError,
		},
		{
			"an FTEID address that is not an address",
			ext(`<ns2:FTEID><ns2:TEID>5</ns2:TEID><ns2:IPv4Address>foo</ns2:IPv4Address></ns2:FTEID>`),
			errCodeSchemaError,
		},
		{
			"an FTEID IPv6 address with a zone",
			ext(`<ns2:FTEID><ns2:TEID>5</ns2:TEID><ns2:IPv6Address>fe80::1%eth0</ns2:IPv6Address></ns2:FTEID>`),
			errCodeSchemaError,
		},
		{
			"a NetworkInstance that is not hexBinary",
			ext(`<ns2:NetworkInstance>zz1</ns2:NetworkInstance>`), errCodeSchemaError,
		},
		{"a PDR of an odd number of digits", ext(`<ns2:PDR>a01</ns2:PDR>`), errCodeSchemaError},
		// Schema-valid, but no criterion this element can apply: the operation's refusal.
		{"an FTEID with TEID 0", ext(`<ns2:FTEID><ns2:TEID>0</ns2:TEID></ns2:FTEID>`), errCodeActivateFailed},
		{
			"an FTEID naming both families",
			ext(`<ns2:FTEID><ns2:TEID>5</ns2:TEID><ns2:IPv4Address>10.0.0.1</ns2:IPv4Address>` +
				`<ns2:IPv6Address>2001:db8::1</ns2:IPv6Address></ns2:FTEID>`),
			errCodeActivateFailed,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			m := serve(t, testServer(st), withTarget(c.xml))

			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != c.code {
				t.Fatalf("want a %d refusal, got %+v", c.code, m.ErrorInformation)
			}
			if _, held := st.Get(testXID); held {
				t.Error("a refused task was stored")
			}
		})
	}
}

// TestAnFTEIDAddressIsStoredCanonically: the address an F-TEID criterion names is stored in
// one spelling, so a CC-POI comparing it with the address it holds finds the tunnel however
// the triggering function wrote it — "010.000.000.001" is what the schema's pattern admits for
// 10.0.0.1, and a consumer parsing it as written would refuse it.
func TestAnFTEIDAddressIsStoredCanonically(t *testing.T) {
	for _, c := range []struct{ name, addr, want string }{
		{
			"zero-padded IPv4",
			`<ns2:IPv4Address>010.000.000.001</ns2:IPv4Address>`, "5@10.0.0.1",
		},
		{
			"compressed IPv6",
			`<ns2:IPv6Address>2001:db8::1</ns2:IPv6Address>`, "5@2001:0db8:0000:0000:0000:0000:0000:0001",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			mustAck(t, testServer(st), withTarget(ext(`<ns2:FTEID><ns2:TEID>5</ns2:TEID>`+c.addr+`</ns2:FTEID>`)))

			task, ok := st.Get(testXID)
			if !ok {
				t.Fatal("task not stored")
			}
			want := types.TargetIdentifier{Type: types.TargetFTEID, Value: c.want}
			if len(task.Targets) != 1 || task.Targets[0] != want {
				t.Errorf("stored %+v, want %+v", task.Targets, want)
			}
		})
	}
}
