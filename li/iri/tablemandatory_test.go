// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"reflect"
	"strings"
	"testing"
)

// someTunnelInfo is the least gTPTunnelInfo a session record may carry, for the tests whose
// subject is another field: the payload tables make it mandatory (tableMandatoryFields).
var someTunnelInfo = GTPTunnelInfo{FiveGSGTPTunnels: FiveGSGTPTunnels{ULNGUUPTunnelInformation: FTEID{TEID: 1}}}

// TestATableMandatoryFieldIsRefusedWhenUnset: a member the module marks OPTIONAL and a payload
// table marks M is refused when unset, so a record that would decode cleanly without it is not
// emitted as the table's record. Each entry must name an OPTIONAL member of a record, since a
// mandatory one is refused by the module's own rule, and the golden fixtures must hold it
// present in every form (requiredByTheTables).
func TestATableMandatoryFieldIsRefusedWhenUnset(t *testing.T) {
	samples := goldenSamples()
	for key := range tableMandatoryFields {
		record, field, _ := strings.Cut(key, ".")
		sample, ok := samples[record]
		if !ok {
			t.Errorf("%s: no record %s", key, record)
			continue
		}
		sf, ok := reflect.TypeOf(sample).FieldByName(field)
		if !ok {
			t.Errorf("%s: %s has no field %s", key, record, field)
			continue
		}
		if !isOptional(sf) {
			t.Errorf("%s is mandatory in the module, so the entry is redundant", key)
		}
		if !requiredByTheTables[key] {
			t.Errorf("%s is refused when unset, and requiredByTheTables does not list it", key)
		}

		if _, err := EncodeXIRI(sample); err != nil {
			t.Fatalf("%s: the populated record was refused: %v", record, err)
		}
		unset := reflect.New(reflect.TypeOf(sample)).Elem()
		unset.Set(reflect.ValueOf(sample))
		unset.FieldByName(field).SetZero()
		_, err := EncodeXIRI(unset.Interface())
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s unset: EncodeXIRI = %v, want a refusal naming it", key, err)
		}
	}
	if len(tableMandatoryFields) != 3 {
		t.Errorf("tableMandatoryFields has %d entries; gTPTunnelInfo in the three session records "+
			"is the 3 this was written against", len(tableMandatoryFields))
	}
}
