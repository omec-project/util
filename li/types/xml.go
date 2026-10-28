// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"encoding/xml"
)

// xmlElement gives the ETSI TS 103 221-1 element name for an identifier type that
// the schema carries as a plain `TargetIdentifier` arm. The names are the schema's,
// which are lowercase-initial — table 6.2.1.2-2 names the types (IPv4Address,
// TCPPort) and the elements are spelled differently, so the two are not
// interchangeable.
//
// Where a type stands for several elements this names the commonest; TargetIdentifier's
// Element chooses among them by the value, and is what a renderer calls.
//
// It reports false for the LI_T3 criteria — FSEID, FTEID, PDRID, QERID, NetworkInstance,
// GTPTunnelDirection and PDR. All but one have no plain schema element and travel in the
// 3GPP LI_T3 extension; FTEID also has one, gtpuTunnelId, but carries only the TEID there
// and its value may be "TEID@address", which that element cannot hold. None of them is rendered
// as a plain element. An X1 task answer reports them in the LI_T3 extension form, which
// li/x1 builds itself (extensionTargetArm); and the X2 Matched Target Identifier never carries
// them, since they reach a CC-POI, which reports over X3 and names no target identifier there.
//
// This lives on the leaf package both interfaces depend on, because two interfaces
// render these names: the X1 responses that tell an ADMF what tasking an element
// holds, and the X2 conditional attribute that tells a mediation function which
// identity matched. A second copy of the mapping would be free to disagree with the
// first, and nothing would notice.
func (t TargetIdentifierType) xmlElement() (string, bool) {
	switch t {
	case TargetSUPI:
		return "supiimsi", true
	case TargetPEI:
		return "peiImei", true
	case TargetGPSI:
		return "gpsiMsisdn", true
	case TargetUEIPv4:
		return "ipv4Address", true
	case TargetUEIPv6:
		return "ipv6Address", true
	case TargetTCPPort:
		return "tcpPort", true
	case TargetUDPPort:
		return "udpPort", true
	default:
		return "", false
	}
}

// XMLFragment renders this identifier in the form ETSI TS 103 221-2 clause 5.3.18
// requires of the Matched and Other Target Identifier conditional attributes: the
// contents of a TS 103 221-1 `TargetIdentifier` without the enclosing tag, encoded
// in UTF-8. The clause's own example is `<imsi>204081234567890</imsi>`.
//
// Unprefixed, deliberately. Inside an X1 document the element names carry the
// prefixes that document binds; lifted into a PDU header the fragment stands alone,
// so a prefix here would be undeclared. The unprefixed form is what the clause
// illustrates.
//
// It reports false rather than guessing for an identifier with no plain element —
// the same rule the X1 renderer follows, and for the same reason: an identifier
// reported as nothing is visibly missing, while one reported as something else is
// not.
func (t TargetIdentifier) XMLFragment() (string, bool) {
	el, ok := t.Element()
	if !ok {
		return "", false
	}

	return "<" + el + ">" + escapeXMLText(t.Value) + "</" + el + ">", true
}

// Element gives the TS 103 221-1 element this identifier is rendered as, choosing among
// the several elements its type folds together by the shape of the value.
//
// Three types stand for more than one element: a SUPI is an IMSI or an NAI, a PEI an
// IMEI or an IMEISV, a GPSI an MSISDN or an NAI. Naming one element for all of them
// would put an NAI inside supiimsi and a sixteen-digit IMEISV inside peiImei — both
// invalid against the schema, so a mediation function validating the attribute would
// discard it. The value says which it is: the numeric elements are all digits within a
// fixed length, and anything else is the NAI form.
//
// Values are the bare identifiers X1 tasking carries. A value still carrying a TS 29.571
// prefix such as "imsi-" is not all digits, so it renders as the NAI form: the caller
// strips the prefix before building the identifier.
//
// It reports false for a value that fits no element of its type — a PEI that is neither
// fourteen nor sixteen digits, say — rather than rendering it into one it does not fit.
func (t TargetIdentifier) Element() (string, bool) {
	switch t.Type {
	case TargetSUPI:
		switch {
		case digitsBetween(t.Value, 6, 15):
			return "supiimsi", true
		case t.Value != "" && !allDigits(t.Value):
			return "supinai", true
		}

		return "", false
	case TargetPEI:
		switch {
		case digitsBetween(t.Value, 14, 14):
			return "peiImei", true
		case digitsBetween(t.Value, 16, 16):
			return "peiImeisv", true
		}

		return "", false
	case TargetGPSI:
		switch {
		case digitsBetween(t.Value, 1, 15):
			return "gpsiMsisdn", true
		case t.Value != "" && !allDigits(t.Value):
			return "gpsiNai", true
		}

		return "", false
	}

	return t.Type.xmlElement()
}

// digitsBetween reports whether s is all decimal digits, with a length in [lo, hi].
func digitsBetween(s string, lo, hi int) bool {
	return len(s) >= lo && len(s) <= hi && allDigits(s)
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return s != ""
}

// escapeXMLText escapes text for inclusion in an XML element body.
func escapeXMLText(s string) string {
	var b bytes.Buffer
	//nolint:errcheck // writing to a bytes.Buffer cannot fail
	_ = xml.EscapeText(&b, []byte(s))

	return b.String()
}
