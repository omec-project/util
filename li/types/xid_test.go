// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package types

import "testing"

// TestOnlyTheCanonicalXIDConvertsToHeaderBytes: tasks are keyed by the XID string, so
// two identifiers differing only in case or hyphenation are two tasks. Converting both
// to the same sixteen header bytes would label two warrants' product identically. Only
// the canonical form — the one X1 admits — converts; any other is the zero XID, which a
// caller checks for with IsZero before delivering.
func TestOnlyTheCanonicalXIDConvertsToHeaderBytes(t *testing.T) {
	const canonical = "abcdef01-2345-4678-9abc-def012345678"

	got := XID(canonical).Bytes()
	if got == ([16]byte{}) {
		t.Fatal("the canonical XID converted to the zero XID")
	}
	if got[0] != 0xab || got[15] != 0x78 {
		t.Errorf("canonical XID converted to %x", got)
	}

	for _, v := range []string{
		"ABCDEF01-2345-4678-9ABC-DEF012345678",  // upper case
		"abcdef0123454678" + "9abcdef012345678", // no hyphens
		"abcdef012-345-4678-9abc-def012345678",  // hyphens misplaced
		"not-a-uuid",
	} {
		if b := XID(v).Bytes(); b != ([16]byte{}) {
			t.Errorf("XID %q converted to %x; a non-canonical form must not share a warrant's "+
				"header bytes", v, b)
		}
	}
}

// TestValidUUIDIsTheSchemasUUID: lowercase 8-4-4-4-12 only, the form X1 admits and the only one
// XID.Bytes converts, so the two cannot disagree about a task identifier.
func TestValidUUIDIsTheSchemasUUID(t *testing.T) {
	for s, want := range map[string]bool{
		"50b93d1e-1b53-4d63-aacb-e4d99811bc0b":  true,
		"50B93D1E-1B53-4D63-AACB-E4D99811BC0B":  false,
		"50b93d1e1b534d63aacbe4d99811bc0b":      false,
		"50b93d1e-1b53-4d63-aacb-e4d99811bc0":   false,
		"50b93d1e-1b53-4d63-aacb-e4d99811bc0bz": false,
		"g0b93d1e-1b53-4d63-aacb-e4d99811bc0b":  false,
		"":                                      false,
	} {
		if got := ValidUUID(s); got != want {
			t.Errorf("ValidUUID(%q) = %v, want %v", s, got, want)
		}
		if got := !XID(s).IsZero(); got != want {
			t.Errorf("XID(%q) converts = %v, want %v: X1's check and the conversion disagree", s, got, want)
		}
	}
}
