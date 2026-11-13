<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# Vendored TS 33.128 ASN.1 module

`TS33128Payloads.asn` is the machine-readable payload module published as an attachment to
3GPP TS 33.128. `module_test.go` parses it once for every audit that reads it: the drift
audit enumerates the fields each xIRI record defines, so that a field the module declares and
this project never populates fails a test rather than going unnoticed, and the tag, bounds and
enumeration audits hold the encoder to the module's tags, sizes, ranges and values.

## Provenance

| | |
|---|---|
| Specification | 3GPP TS 33.128 **V18.16.0** |
| Module header | `{… threeGPP(4) ts33128(19) r18(18) version15(15)}` |
| Archive | `33128-ig0.zip` → `33128-ig0-attachments.zip` |
| SHA-256 of `TS33128Payloads.asn` | `e41ab9258f8ea3296a28dcfefd0c93254093475ca87f2caf291c814e4039a965` |

Fetch the archive from
`https://www.3gpp.org/ftp/Specs/archive/33_series/33.128/33128-ig0.zip`; it holds the
specification and a nested `33128-ig0-attachments.zip`, and `TS33128Payloads.asn` is in the
nested archive. Note that `www.3gpp.org` may serve 403 to automated clients; if it does,
download the archive with a browser.

The digest above is of the module file itself, as extracted from the archive, and is what
makes a re-fetch checkable. If it changes, the module changed, and `asn1_drift_test.go` is
the thing that will tell you what that meant.

## Licensing

The module is a 3GPP work and carries `LicenseRef-3GPP` in a `.license` sidecar rather than
a header — a header would alter the file and break the digest. The same arrangement covers
the 3GPP schema vendored under `x1/testdata/schemas/`; see `LICENSES/LicenseRef-3GPP.txt`
for the notice and the republication exception the vendoring rests on.

## What this module is not

It is not the authority on which fields are **mandatory**. The M/C/O markers live in the
payload tables of TS 33.128 clause 6.2, which are prose, and they do not always agree with
the module's `OPTIONAL` markers: `gTPTunnelInfo` is `OPTIONAL` in the module and **M** in
three payload tables. A record can omit a field the tables mark M and still decode cleanly
against this module; `CONFORMANCE.md` records where the two disagree. Use this module to
enumerate fields, and the payload tables to judge them.
