// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// inlineMembers names, for each constrained type the module does not define under its own name,
// the member whose inline restriction it carries. Every other type in the tables is defined in
// the module under the name this package gives it.
var inlineMembers = map[string]string{
	"SliceServiceType":       "sliceServiceType",
	"SliceDifferentiator":    "sliceDifferentiator",
	"ServiceType":            "serviceType",
	"TEID":                   "tEID",
	"LCSCorrelationID":       "lcsCorrelationId",
	"RoutingIndicatorLength": "routingIndicatorLength",
}

// boundsOutsideTheModule are the constrained types whose bound the module does not state, each
// with the member it is declared inline in, the bound, and where the bound comes from instead.
// moduleBounds asserts the module still states none, so a bound the module later gains is
// compared rather than shadowed.
var boundsOutsideTheModule = map[string]struct {
	member string
	lo, hi int64
	source string
}{
	"ServiceRequestIdentity": {"serviceRequest", 1, 1, "table 6.2.2.2.13-1: encoding per TS 24.501 clause 9.7, a one-octet message type"},
	"ServiceAcceptIdentity":  {"serviceAccept", 1, 1, "table 6.2.2.2.13-1: encoding per TS 24.501 clause 9.7, a one-octet message type"},
}

var reBounds = regexp.MustCompile(`\((?:SIZE\()?\s*(\d+)\s*(?:\.\.\s*(\d+))?\s*\)`)

// moduleBounds reads a constrained type's bounds from the module: the `(lo..hi)` or `(SIZE(n))`
// on its own definition, or on the member it is declared inline in. A type in
// boundsOutsideTheModule takes its bound from there, once the module is shown to state none.
func moduleBounds(t *testing.T, src []byte, name string) (int64, int64) {
	t.Helper()

	line := regexp.MustCompile(`(?m)^` + name + ` ::= .*$`)
	if member, ok := inlineMembers[name]; ok {
		line = regexp.MustCompile(`(?m)^\s*` + member + `\s+\[\d+\][^\n]*$`)
	}
	if outside, ok := boundsOutsideTheModule[name]; ok {
		def := regexp.MustCompile(`(?m)^\s*` + outside.member + `\s+\[\d+\][^\n]*$`).Find(src)
		if def == nil {
			t.Fatalf("%s: the module has no member %s", name, outside.member)
		}
		if reBounds.Match(def) {
			t.Fatalf("%s: the module now bounds it (%q); compare against that rather than %s",
				name, def, outside.source)
		}

		return outside.lo, outside.hi
	}
	def := line.Find(src)
	if def == nil {
		t.Fatalf("%s: no definition in %s", name, modulePath)
	}

	m := reBounds.FindSubmatch(def)
	if m == nil {
		t.Fatalf("%s: %q carries no bounds", name, def)
	}
	lo, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	hi := lo
	if len(m[2]) > 0 {
		if hi, err = strconv.ParseInt(string(m[2]), 10, 64); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	return lo, hi
}

// TestEveryConstraintBoundIsEnforced holds every entry of the size and range tables to the
// module, and checks each is enforced at both of the module's bounds and one step outside each.
//
// The bounds come from the module rather than from the tables, so an entry widened or
// transcribed one off is caught by the comparison and by the value one step past the module's
// bound being accepted. Each value is checked as a mandatory member with zero forbidden, so the
// exemptions an optional member gets — empty or zero as absence — cannot mask a bound.
// (Enumerations are TestEnumBoundsMatchTheModule's.)
func TestEveryConstraintBoundIsEnforced(t *testing.T) {
	src := moduleSource(t)

	check := func(t *testing.T, v reflect.Value, wantOK bool, what string) {
		t.Helper()

		err := constraintOf(v, "x", true, true)
		switch {
		case wantOK && err != nil:
			t.Errorf("%s was refused: %v", what, err)
		case !wantOK && err == nil:
			t.Errorf("%s was accepted", what)
		}
	}
	agrees := func(t *testing.T, name string, tableLo, tableHi int64) (int64, int64) {
		t.Helper()

		lo, hi := moduleBounds(t, src, name)
		if tableLo != lo || tableHi != hi {
			t.Errorf("the table bounds %s to %d..%d, and the module defines %d..%d",
				name, tableLo, tableHi, lo, hi)
		}
		return lo, hi
	}

	for typ, c := range intConstraints {
		t.Run(typ.Name(), func(t *testing.T) {
			lo, hi := agrees(t, typ.Name(), c.min, c.max)
			at := func(n int64) reflect.Value {
				v := reflect.New(typ).Elem()
				v.SetInt(n)
				return v
			}
			check(t, at(lo-1), false, "one below the lower bound")
			check(t, at(lo), true, "the lower bound")
			check(t, at(hi), true, "the upper bound")
			check(t, at(hi+1), false, "one above the upper bound")
		})
	}

	for typ, c := range octetConstraints {
		t.Run(typ.Name(), func(t *testing.T) {
			lo, hi := agrees(t, typ.Name(), int64(c.min), int64(c.max))
			at := func(n int64) reflect.Value { return reflect.MakeSlice(typ, int(n), int(n)) }
			check(t, at(lo-1), false, "one octet short of the lower bound")
			check(t, at(lo), true, "the lower bound")
			check(t, at(hi), true, "the upper bound")
			check(t, at(hi+1), false, "one octet past the upper bound")
		})
	}

	strs := func(table map[reflect.Type]utf8Size, char string) {
		for typ, c := range table {
			t.Run(typ.Name(), func(t *testing.T) {
				lo, hi := agrees(t, typ.Name(), int64(c.min), int64(c.max))
				at := func(n int64) reflect.Value {
					v := reflect.New(typ).Elem()
					v.SetString(strings.Repeat(char, int(n)))
					return v
				}
				check(t, at(lo-1), false, "one character short of the lower bound")
				check(t, at(lo), true, "the lower bound")
				check(t, at(hi), true, "the upper bound")
				check(t, at(hi+1), false, "one character past the upper bound")
			})
		}
	}
	strs(numericConstraints, "1")
	strs(identityConstraints, "1")
	// A multi-byte character, so the bound is checked in characters, as the module's SIZE on a
	// UTF8String counts them, rather than in bytes.
	strs(utf8Constraints, "é")
}

// TestEnumBoundsMatchTheModule holds every enumConstraints entry, the five cause groups
// included, to the module: the bounds are the lowest and highest value the module's ENUMERATED of
// the same name defines, and every value between them the module leaves out must be in enumHoles
// — and refused — while every value enumHoles names must be one the module leaves out.
//
// The constants in causevalues.go and these bounds are two transcriptions of one enumeration and
// can drift apart from each other as well as from the module: a group extended in the module, its
// constants regenerated and its bound left at the old maximum would refuse a value the
// specification permits, which on this interface means refusing a record that should have been
// delivered.
func TestEnumBoundsMatchTheModule(t *testing.T) {
	enums := parseModule(t).enums

	checked := 0
	for typ, bounds := range enumConstraints {
		name := typ.Name()
		values, ok := enums[name]
		if !ok || len(values) == 0 {
			t.Errorf("%s is not defined as an ENUMERATED with values in %s", name, modulePath)
			continue
		}

		defined := map[int64]bool{}
		lo, hi := values[0].value, values[0].value
		for _, e := range values {
			defined[e.value] = true
			lo, hi = min(lo, e.value), max(hi, e.value)
		}
		if bounds.min != lo || bounds.max != hi {
			t.Errorf("enumConstraints bounds %s to %d..%d, and the module defines %d..%d",
				name, bounds.min, bounds.max, lo, hi)
		}

		for n := lo; n <= hi; n++ {
			v := reflect.New(typ).Elem()
			v.SetInt(n)
			err := constraintOf(v, "x", true, true)
			switch {
			case defined[n] && enumHoles[typ][n]:
				t.Errorf("enumHoles names %s %d, which the module defines", name, n)
			case !defined[n] && !enumHoles[typ][n]:
				t.Errorf("%s defines no %d and enumHoles does not name it", name, n)
			case defined[n] && err != nil:
				t.Errorf("%s %d is defined by the module and refused: %v", name, n, err)
			case !defined[n] && err == nil:
				t.Errorf("%s %d is not defined by the module and was accepted", name, n)
			}
		}
		checked++
	}
	for typ := range enumHoles {
		if _, ok := enumConstraints[typ]; !ok {
			t.Errorf("enumHoles names %s, which enumConstraints does not bound", typ.Name())
		}
	}

	// Every entry must have been compared: a parse that silently skipped one would leave its
	// bounds checked against nothing.
	if checked != len(enumConstraints) {
		t.Errorf("checked %d enumerations against the module; the table holds %d, so the parse "+
			"is looking at something else", checked, len(enumConstraints))
	}
}
