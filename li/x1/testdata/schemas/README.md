<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# Vendored X1 schemas

These are the published ETSI schemas the X1 implementation is validated against. They are
here so that the validation runs without network access, and so that a schema revision
arrives as a reviewable diff rather than as a test whose behaviour changes on its own.

**Do not edit them.** `TestRenderedResponsesValidate` checks each file against a SHA-256
digest pinned in `schema_validation_test.go`, so a local edit — including a well-meant one to add a `schemaLocation` — fails the
build. The imports are resolved by `validate.xsd` instead.

| File | Target namespace | Schema version |
|---|---|---|
| `TS_103_221_01.xsd` | `http://uri.etsi.org/03221/X1/2017/10` | 1.19.1 |
| `TS_103_280.xsd` | `http://uri.etsi.org/03280/common/2017/07` | 2.12.1 |
| `TS_103_221_01_HashedID.xsd` | `http://uri.etsi.org/03221/X1/2017/10/HashedID` | 1.10.1 |
| `urn_3GPP_ns_li_3GPPX1Extensions.xsd` | `urn:3GPP:ns:li:3GPPX1Extensions:r18:v6` | 3GPP Rel-18 |

The first three were fetched 2026-08-12 from the ETSI Forge schema repository, branch
`cr/103120/088`:

```
https://forge.etsi.org/rep/li/schemas-definitions/-/raw/cr/103120/088/103221-1/TS_103_221_01.xsd
https://forge.etsi.org/rep/li/schemas-definitions/-/raw/cr/103120/088/103280/TS_103_280.xsd
https://forge.etsi.org/rep/li/schemas-definitions/-/raw/cr/103120/088/103221-1/TS_103_221_01_HashedID.xsd
```

The fourth is a 3GPP schema, not an ETSI one: it defines the `targetIdentifierExtension`
content a CC-TF sends in an LI_T3 `ActivateTask`, which TS 103 221-1 leaves as a strict
wildcard. It comes from the TS 33.128 attachments archive, which is inside the specification
zip rather than published as a standalone file:

```
https://www.3gpp.org/ftp/Specs/archive/33_series/33.128/33128-ig0.zip
  └ 33128-ig0-attachments.zip
      └ urn_3GPP_ns_li_3GPPX1Extensions.xsd
```

That same attachments archive holds `TS33128Payloads.asn`, the module the xIRI records are
validated against (vendored under `li/iri/testdata/asn1`; 3GPP's forge publishes later
revisions of it, which are not byte-identical).

## A version mismatch to be aware of

The schema is 1.19.1; the published specification text is **v1.21.1 (2025-08)**. Where the
two could disagree, the prose is normative and the schema is what a peer's validator
actually runs. Both were consulted for this implementation: the field descriptions and
M/C/O markers come from the prose, the element names and structures from the schema.

Nothing has been found where they conflict. If something is, the prose wins for behaviour
and the schema still governs what validates, and the conflict is recorded in
`li/x1/CONFORMANCE.md` rather than resolved silently here.

## `validate.xsd`

`TS_103_221_01.xsd` imports its two dependencies **without** `schemaLocation`, so it cannot
be handed to a validator on its own. `validate.xsd` is ours, not ETSI's: it imports four
schemas with explicit locations — those three, and the 3GPP LI_T3 extension schema the
detection criteria are defined in — giving the validator everything it needs while leaving the
published files untouched.
