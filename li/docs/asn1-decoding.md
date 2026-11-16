<!--
SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
SPDX-License-Identifier: Apache-2.0
-->

# ASN.1 decoding: none is shipped, and what one must carry

The `li` packages **encode** TS 33.128 xIRI records and do not decode them. None of them
contains a DER or BER decoder; the only parsing of an encoding is in tests, over this
library's own output, through the standard library. This note says why, and lists the bounds a
decoder must carry, so that a parse path added later — here, or in a mediation-side tool built
against these packages — starts from them.

## Why there is no decoder

- **No caller needs one.** An element here is the point of interception: it produces xIRI and
  never receives it. `x1` parses XML, and `x2x3` parses the ETSI TS 103 221-2 framing header
  by hand; neither parses an ASN.1 encoding. The mediation function that receives xIRI is a
  third-party system, so decoding it is not this library's job.
- **A round trip is the weaker test.** It still passes when encode and decode change together,
  which is exactly the failure that corrupts a receiver while looking healthy from inside the
  element. The encoder is pinned instead by byte fixtures (`iri/testdata/golden.txt`, every
  record in more than one form) and by a check that derives each emitted tag from the
  declarations (`iri/emit_tags_test.go`).
- **A parser nobody calls is still an attack surface somebody maintains.** Its hardening has to
  be kept correct with no caller to show when it is not.

## What would have to be true to add one

A decoder belongs here when something here parses xIRI a peer sent: an X2 receiver, a
mediation-side tool built against these packages, or a test that cannot be expressed against
fixtures. Its input is then peer-supplied, and it SHALL carry every bound below, each with a
test that shows it is load-bearing. The test has to assert that the refusal *names the bound*:
an unbounded decoder also fails on most of these inputs, by running off the end of the
message, so a test that asserts only that an error was returned passes against the defect.

## The bounds, and the defect each prevents

They apply to any BER parse of the TS33128Payloads structures, and to the X2/X3 element
framing around them.

| Bound | Value | The defect without it |
|---|---|---|
| Declared-length ceiling | 16 MiB (`16 << 20`), checked **before** allocating | A decoder that allocates straight from the wire's length field can be made to attempt a multi-gigabyte allocation, or to panic, by one small message with a crafted length: a remote denial of service. |
| Cumulative budget through indefinite length | 16 MiB, the same ceiling, **across the whole construct** | The indefinite-length form has no declared length, so a decoder buffers until an end-of-contents marker arrives, and an unterminated construct grows the buffer without limit — bypassing the ceiling above simply by choosing the other length form. Bounding each nested element separately does not close it, because a sender reaches any total by nesting. |
| Nesting depth through indefinite length | 64 | End-of-contents detection that recurses once per nested indefinite construct, with the depth the sender's to choose, exhausts the decoding goroutine's stack at two bytes per level — which on a delivery or signalling goroutine takes the network function with it. Real records nest a handful deep. |
| A child's declared length inside an indefinite construct | the same 16 MiB ceiling, before the child is read | As the first row, reached from inside the second. |
| Primitives with no contents octets | refused | X.690 requires a BOOLEAN to have one contents octet and an INTEGER at least one. Indexing a BOOLEAN's first octet without checking it exists is a panic, and sign-extending an empty INTEGER yields a confident zero: a wrong value with no error, which is worse. |

A codec rejects malformed input rather than failing on it, for every length form as well as
every length.
