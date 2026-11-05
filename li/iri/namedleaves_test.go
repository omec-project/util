// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// unrestrictedBareFields are the record fields whose Go declaration is a bare builtin, listed
// with what the TS 33.128 module says about them.
//
// Each entry is a claim that there is nothing to check, not that checking was skipped. The two
// are indistinguishable from the code — a bare `int64` looks the same either way — which is why
// they are written down here rather than inferred.
var unrestrictedBareFields = map[string]string{
	// `uplinkVolume [7] INTEGER OPTIONAL` — no range in the module, in any of the four records
	// that carry it.
	"SMFPDUSessionRelease.UplinkVolume":   "INTEGER, unconstrained",
	"SMFPDUSessionRelease.DownlinkVolume": "INTEGER, unconstrained",
	// `nRPPaMessage [6] OCTET STRING OPTIONAL` — no SIZE. These are transport containers whose
	// length is whatever the positioning protocol produced.
	"AMFPositioningInfoTransfer.NRPPaMessage": "OCTET STRING, unconstrained",
	"AMFPositioningInfoTransfer.LPPMessage":   "OCTET STRING, unconstrained",
	// `xIRIPayloadOID [1] RELATIVE-OID` — an OID has no SIZE constraint.
	"XIRIPayload.OID": "RELATIVE-OID, no size constraint",
}

// TestEveryRestrictedLeafIsNamed is what keeps constraints.go's claim true.
//
// The constraint tables are keyed on type, so a leaf declared as a bare `[]byte`, `int`, `int64`
// or `string` cannot be reached by them however well its restriction is documented in the
// comment beside it — and nothing would fail: the restriction would be written down, so a
// reader checking whether it was recorded would find that it was.
//
// This test refuses the shape rather than the omission. A field added as a bare builtin fails
// here, and the author either names its type or records in the map above what the module says
// about it — which is a decision either way, made once, at the moment the field is added.
func TestEveryRestrictedLeafIsNamed(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "iri.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing iri.go: %v", err)
	}

	bare := map[string]bool{"[]byte": true, "int": true, "int64": true, "string": true}
	seen := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, f := range st.Fields.List {
			typeName := typeExprString(f.Type)
			if !bare[typeName] {
				continue
			}
			for _, name := range f.Names {
				key := spec.Name.Name + "." + name.Name
				seen[key] = true
				if _, allowed := unrestrictedBareFields[key]; !allowed {
					t.Errorf("%s is declared as a bare %s, so any SIZE or range its definition "+
						"carries is enforced by nothing: the constraint tables are keyed on type. "+
						"Give it a named type, or record here what the module says about it",
						key, typeName)
				}
			}
		}

		return true
	})

	// A stale entry is the other direction of the same problem: it reads as a considered
	// exemption for a field that is named or gone, and the next author to add a bare field of
	// that name inherits an exemption nobody granted it.
	for key := range unrestrictedBareFields {
		if !seen[key] {
			t.Errorf("%s is listed as an unrestricted bare field and is not one any more; remove "+
				"the entry rather than leaving an exemption lying where a future field can find it",
				key)
		}
	}
}

// typeExprString renders the type expressions this check needs to recognise. Anything else
// answers "" and is ignored, which is the safe direction: a named type, a struct or a slice of
// records is not what this test is looking for.
func typeExprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		// A pointer field is how an OPTIONAL member says "present, and equal to my
		// type's zero value" (see SUPIUnauthenticatedIndication). The pointer expresses
		// presence, not a type, so *int is as bare as int and its restriction is
		// enforced by exactly as little. Without this arm every pointer field would be a
		// hole in the scan.
		return typeExprString(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.ArrayType:
		if t.Len != nil {
			return ""
		}
		if elem, ok := t.Elt.(*ast.Ident); ok {
			return "[]" + elem.Name
		}
	}

	return ""
}

// TestTheLeafScanIsNotVacuous: the check above is a scan, and a scan that matches nothing passes.
func TestTheLeafScanIsNotVacuous(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "iri.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing iri.go: %v", err)
	}

	structs, fields := 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if st, ok := spec.Type.(*ast.StructType); ok {
			structs++
			fields += len(st.Fields.List)
		}

		return true
	})

	// iri.go defines the seventeen records plus their component SEQUENCEs.
	if structs < 25 || fields < 100 {
		t.Errorf("the scan found %d structs and %d fields in iri.go; the record definitions "+
			"hold far more, so it is checking something other than them", structs, fields)
	}
}
