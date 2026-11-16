// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package li

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// dispositions are the per-interface conformance documents. Every interface these
// packages claim conformance for has one, and the index names them all — so a
// disposition added, renamed or moved without the index following leaves the index
// pointing at nothing, and these tests say so.
var dispositions = []string{
	"x1/CONFORMANCE.md",
	"x2x3/CONFORMANCE.md",
	"iri/CONFORMANCE.md",
}

const index = "CONFORMANCE.md"

// TestEveryDispositionOnDiskIsListed: the check below starts from the list above, so a
// disposition added beside the others and left out of the list would escape it.
func TestEveryDispositionOnDiskIsListed(t *testing.T) {
	found, err := filepath.Glob("*/CONFORMANCE.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range found {
		if !slices.Contains(dispositions, path) {
			t.Errorf("%s is a disposition the list in this test does not name, so nothing checks that %s indexes it", path, index)
		}
	}
}

func TestEveryDispositionExistsAndIsIndexed(t *testing.T) {
	body, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("reading %s: %v", index, err)
	}
	text := string(body)

	for _, path := range dispositions {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: %v — an interface these packages claim conformance for has no disposition", path, err)

			continue
		}
		if !strings.Contains(text, path) {
			t.Errorf("%s exists but %s does not reference it, so a reader of the index cannot reach it", path, index)
		}
	}
}

// conformancePath matches a path ending in CONFORMANCE.md wherever it appears: in a link
// target, in backticks, followed by an anchor or by punctuation.
var conformancePath = regexp.MustCompile(`[\w./-]*CONFORMANCE\.md`)

// referencedDispositions returns every CONFORMANCE.md path text names, other than the
// index itself.
func referencedDispositions(text string) []string {
	var out []string
	for _, path := range conformancePath.FindAllString(text, -1) {
		if path != index {
			out = append(out, path)
		}
	}

	return out
}

// TestTheIndexReferencesNothingMissing is the other direction, and the one that
// catches a rename: the index naming a document that is not there is a broken link.
func TestTheIndexReferencesNothingMissing(t *testing.T) {
	body, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("reading %s: %v", index, err)
	}

	for _, path := range referencedDispositions(string(body)) {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s names %q, which is not there: %v", index, path, err)
		}
	}
}

// TestReferencesAreFoundWhateverFollowsThem: a path followed by an anchor, by
// punctuation or by Markdown emphasis is still a reference to that file.
func TestReferencesAreFoundWhateverFollowsThem(t *testing.T) {
	text := "see [x](gone1/CONFORMANCE.md), gone2/CONFORMANCE.md, " +
		"[y](gone3/CONFORMANCE.md#top) and **gone4/CONFORMANCE.md**. Also CONFORMANCE.md."

	got := referencedDispositions(text)
	want := []string{"gone1/CONFORMANCE.md", "gone2/CONFORMANCE.md", "gone3/CONFORMANCE.md", "gone4/CONFORMANCE.md"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("found %q, want %q", got, want)
	}
}
