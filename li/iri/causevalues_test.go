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

// valuesPath is the file the constants live in, read by this test rather than duplicated into
// it.
const valuesPath = "causevalues.go"

// causeGroups are the five HandoverCause groups, as the module names them.
var causeGroups = []string{"CauseRadioNetwork", "CauseTransport", "CauseNas", "CauseProtocol", "CauseMisc"}

// causeGroupsFromModule is the five groups' values from the module, keyed by the identifier the
// module gives each. Read every run rather than kept as a fixture, because a fixture is a second
// transcription and would need the same test.
func causeGroupsFromModule(t *testing.T) map[string]map[string]int64 {
	t.Helper()

	return moduleEnumerators(t, causeGroups...)
}

// goIdentifier is how causevalues.go names one of the module's identifiers: the group, then
// the identifier with its hyphens removed and each part capitalised.
func goIdentifier(group, asn1Name string) string {
	var b strings.Builder
	b.WriteString(group)
	for _, part := range strings.Split(asn1Name, "-") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}

	return b.String()
}

// declaredCauseValues reads the constants out of causevalues.go itself.
//
// **Parsed, not listed.** A list here would be a third transcription of the same values — the
// module, the constants, and the test's copy — and the one most likely to be forgotten, since
// nothing fails when a constant is added and the list is not. Reading the source means the only
// two things that can disagree are the two that matter.
func declaredCauseValues(t *testing.T) map[string]int64 {
	t.Helper()

	src, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("reading %s: %v", valuesPath, err)
	}

	decl := regexp.MustCompile(`(?m)^\s*(Cause\w+)\s+(Cause\w+)\s*=\s*(\d+)\s*$`)
	out := map[string]int64{}
	for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
		v, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil {
			t.Fatalf("%s: %q is not a number", m[1], m[3])
		}
		out[m[1]] = v
	}
	if len(out) == 0 {
		t.Fatalf("no constants parsed from %s, so every assertion below would pass against "+
			"nothing", valuesPath)
	}

	return out
}

// TestCauseValuesMatchTheModule is what makes the transcription checkable rather than trusted.
//
// The values in causevalues.go are transcribed by hand from the module, and a transcription is
// only as good as the care taken over it. This reads the module every run and
// compares it in both directions: a value the module gives that no constant declares, and a
// constant whose value the module does not agree with. Either is a transcription that has
// drifted, and drift here is not a compile error — it is a handover record carrying a cause the
// receiver reads as a different cause.
func TestCauseValuesMatchTheModule(t *testing.T) {
	module := causeGroupsFromModule(t)
	declared := declaredCauseValues(t)

	for group, values := range module {
		for asn1Name, want := range values {
			name := goIdentifier(group, asn1Name)
			got, ok := declared[name]
			if !ok {
				t.Errorf("%s(%d) is defined in the module and declared by no constant; an "+
					"element cannot name a cause this package does not", asn1Name, want)

				continue
			}
			if got != want {
				t.Errorf("%s = %d, and the module gives %s(%d) — a record built from this "+
					"constant would carry a cause the receiver reads as a different one",
					name, got, asn1Name, want)
			}
		}
	}

	// And the other direction: a constant naming something the module does not define. It
	// would be a value no conformant receiver can interpret, offered to elements as though
	// the specification permitted it.
	for group, values := range module {
		for name := range declared {
			if !strings.HasPrefix(name, group) {
				continue
			}
			found := false
			for asn1Name := range values {
				if goIdentifier(group, asn1Name) == name {
					found = true

					break
				}
			}
			// A prefix match is not enough on its own: CauseNas* is a prefix of nothing else,
			// but CauseRadioNetwork* and CauseProtocol* have no overlap either, so an
			// unmatched name in a group's namespace is a name that group does not define.
			if !found && groupOf(name) == group {
				t.Errorf("%s is declared and %s does not define it", name, group)
			}
		}
	}
}

// groupOf is which group a constant name belongs to — the longest group prefix it carries, so
// that a name is attributed to exactly one group.
func groupOf(name string) string {
	best := ""
	for _, g := range causeGroups {
		if strings.HasPrefix(name, g) && len(g) > len(best) {
			best = g
		}
	}

	return best
}

// TestTheModuleParseIsNotVacuous guards the guard. A parser that silently matched nothing
// would make every assertion above pass, which is the failure mode of a test that reads its
// own input.
func TestTheModuleParseIsNotVacuous(t *testing.T) {
	module := causeGroupsFromModule(t)

	for group, want := range map[string]int{
		"CauseRadioNetwork": 51, "CauseTransport": 2,
		"CauseNas": 4, "CauseProtocol": 7, "CauseMisc": 6,
	} {
		if got := len(module[group]); got != want {
			t.Errorf("%s parsed to %d values, want %d — if the module changed, the counts here "+
				"and the constants both need revisiting", group, got, want)
		}
	}
}
