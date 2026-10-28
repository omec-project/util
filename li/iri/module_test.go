// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This file is the one reader of the published module that the audits share: the drift audits,
// the tag audit, the bounds and enumeration checks and the zero sweep all take the module's
// SEQUENCEs, CHOICEs and ENUMERATEDs from parseModule. Each used to carry a parser of its own,
// and two parsers of one file can disagree — one stripping comments and one not, one reading a
// definition another skips — so that two audits each passing said different things about the
// same module.

// modulePath is the TS 33.128 ASN.1 module this package holds. It is the same file the X1
// schema validation reads, so a claim about what the specification permits and the artifact
// the claim came from are one thing rather than two.
const modulePath = "testdata/asn1/TS33128Payloads.asn"

// moduleSource is the module's text. A check that cannot run is not a check that passes: the
// module is vendored beside these tests so its absence is a failure and never a skip.
func moduleSource(t *testing.T) []byte {
	t.Helper()

	src, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatalf("read %s: %v — the vendored TS 33.128 module is what these audits compare "+
			"against; without it nothing is being checked at all", modulePath, err)
	}

	return src
}

// asn1Lines is the module with its comments stripped and its lines trimmed.
func asn1Lines(t *testing.T) []string {
	t.Helper()

	lines := strings.Split(string(moduleSource(t)), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, strings.TrimSpace(line))
	}

	return out
}

// asn1TypedField is one member of a module SEQUENCE or one alternative of a module CHOICE, with
// its declared type and whether the module marks it OPTIONAL.
type asn1TypedField struct {
	name     string
	tag      int
	typeName string
	optional bool
}

// asn1Enumerator is one value of a module ENUMERATED.
type asn1Enumerator struct {
	name  string
	value int64
}

// asn1Module is every SEQUENCE, CHOICE and ENUMERATED the module defines, keyed by type name.
// Members and enumerators are in declaration order.
type asn1Module struct {
	sequences map[string][]asn1TypedField
	choices   map[string][]asn1TypedField
	enums     map[string][]asn1Enumerator
}

var (
	// The module writes every SEQUENCE, CHOICE and ENUMERATED with the opening brace on the
	// line after the name, and its closing brace on a line of its own.
	reDefinition = regexp.MustCompile(`^(\w+)\s*::=\s*(SEQUENCE|CHOICE|ENUMERATED)\s*$`)
	reMember     = regexp.MustCompile(`^(\w+)\s*\[(\d+)\]\s*(\w*)(.*)$`)
	reEnumerator = regexp.MustCompile(`([A-Za-z][\w-]*)\s*\((\d+)\)`)
)

// parseModule reads the module once into its three kinds of definition. It fails rather than
// returning an empty kind, since an audit over an empty map passes against anything.
func parseModule(t *testing.T) asn1Module {
	t.Helper()

	m := asn1Module{
		sequences: map[string][]asn1TypedField{},
		choices:   map[string][]asn1TypedField{},
		enums:     map[string][]asn1Enumerator{},
	}

	var current, kind string
	for _, line := range asn1Lines(t) {
		if d := reDefinition.FindStringSubmatch(line); d != nil {
			current, kind = d[1], d[2]
			switch kind {
			case "SEQUENCE":
				m.sequences[current] = nil
			case "CHOICE":
				m.choices[current] = nil
			case "ENUMERATED":
				m.enums[current] = nil
			}

			continue
		}
		if current == "" {
			continue
		}
		if line == "}" {
			current = ""

			continue
		}

		if kind == "ENUMERATED" {
			for _, e := range reEnumerator.FindAllStringSubmatch(line, -1) {
				v, err := strconv.ParseInt(e[2], 10, 64)
				if err != nil {
					t.Fatalf("%s: %q is not a number", current, e[2])
				}
				m.enums[current] = append(m.enums[current], asn1Enumerator{name: e[1], value: v})
			}

			continue
		}

		f := reMember.FindStringSubmatch(strings.TrimSuffix(line, ","))
		if f == nil {
			continue
		}
		tag, err := strconv.Atoi(f[2])
		if err != nil {
			t.Fatalf("%s: unparsable tag %q", current, f[2])
		}
		field := asn1TypedField{
			name:     f[1],
			tag:      tag,
			typeName: f[3],
			optional: strings.Contains(strings.ToUpper(f[4]), "OPTIONAL"),
		}
		if kind == "SEQUENCE" {
			m.sequences[current] = append(m.sequences[current], field)
		} else {
			m.choices[current] = append(m.choices[current], field)
		}
	}

	for what, n := range map[string]int{
		"SEQUENCE": len(m.sequences), "CHOICE": len(m.choices), "ENUMERATED": len(m.enums),
	} {
		if n == 0 {
			t.Fatalf("parsed no %s definitions from %s — the parser and the module disagree", what, modulePath)
		}
	}

	return m
}

// moduleSequences is every SEQUENCE's members, keyed by type name.
func moduleSequences(t *testing.T) map[string][]asn1TypedField {
	t.Helper()

	return parseModule(t).sequences
}

// moduleChoices is every CHOICE's alternatives, keyed by type name.
func moduleChoices(t *testing.T) map[string][]asn1TypedField {
	t.Helper()

	return parseModule(t).choices
}

// moduleEnumerators is the named ENUMERATEDs' values, keyed by type name and then by the
// identifier the module gives each value. It fails on a name that is not an ENUMERATED, or one
// that parsed to no values.
func moduleEnumerators(t *testing.T, names ...string) map[string]map[string]int64 {
	t.Helper()

	enums := parseModule(t).enums
	out := map[string]map[string]int64{}
	for _, name := range names {
		values, ok := enums[name]
		if !ok {
			t.Fatalf("%s is not an ENUMERATED in %s", name, modulePath)
		}
		if len(values) == 0 {
			t.Fatalf("%s parsed to no values, so a check against it would pass against anything", name)
		}
		out[name] = map[string]int64{}
		for _, e := range values {
			out[name][e.name] = e.value
		}
	}

	return out
}
