// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"slices"
	"testing"
)

// TestXMLFragmentMatchesTheClausesForm pins the form ETSI TS 103 221-2
// clause 5.3.18 requires: the contents of a TargetIdentifier without the enclosing
// tag, UTF-8, unprefixed. The clause illustrates it with an IMSI, which is the first
// case here.
func TestXMLFragmentMatchesTheClausesForm(t *testing.T) {
	for _, tc := range []struct {
		id   TargetIdentifier
		want string
	}{
		{TargetIdentifier{Type: TargetSUPI, Value: "208930100007488"}, "<supiimsi>208930100007488</supiimsi>"},
		{TargetIdentifier{Type: TargetPEI, Value: "35784005123456"}, "<peiImei>35784005123456</peiImei>"},
		{TargetIdentifier{Type: TargetGPSI, Value: "9000000002"}, "<gpsiMsisdn>9000000002</gpsiMsisdn>"},
	} {
		got, ok := tc.id.XMLFragment()
		if !ok {
			t.Errorf("%s has no fragment", tc.id.Type)

			continue
		}
		if got != tc.want {
			t.Errorf("fragment = %q, want %q", got, tc.want)
		}
	}
}

// TestXMLFragmentEscapes: the value reaches the wire inside an element body, and an
// identifier is provisioned by a peer rather than by us.
func TestXMLFragmentEscapes(t *testing.T) {
	got, ok := TargetIdentifier{Type: TargetSUPI, Value: `1<2&"3"`}.XMLFragment()
	if !ok {
		t.Fatal("no fragment")
	}
	if want := "<supinai>1&lt;2&amp;&#34;3&#34;</supinai>"; got != want {
		t.Errorf("fragment = %q, want %q", got, want)
	}
}

// TestPacketCriteriaHaveNoFragment is the guard against reporting an identifier as
// something it is not. The packet-detection criteria are arms of a 3GPP extension,
// not plain elements, and they task a CC-POI over LI_T3 — where TS 33.128
// table 5.3.3-2 requires no target identifier at all. So no attribute ever needs
// them, and rendering one as a plain element would tell a mediation function the
// interception matched a subscriber identity when it matched a tunnel or a port.
func TestPacketCriteriaHaveNoFragment(t *testing.T) {
	for _, typ := range []TargetIdentifierType{
		TargetFSEID, TargetFTEID, TargetPDRID, TargetQERID,
		TargetNetworkInstance, TargetGTPTunnelDirection, TargetPDR,
	} {
		if got, ok := (TargetIdentifier{Type: typ, Value: "1"}).XMLFragment(); ok {
			t.Errorf("%s rendered as %q, want no fragment", typ, got)
		}
	}
}

// TestXMLElementCoversEveryPlainArm: every identifier type the schema carries as a plain
// element has a name. The subscriber identities must, because an xIRI matched on one
// would otherwise carry no matched identity; UEIPv4/UEIPv6 and the ports are plain arms
// in the schema too, even though they arrive as packet criteria on LI_T3.
func TestXMLElementCoversEveryPlainArm(t *testing.T) {
	for typ, want := range map[TargetIdentifierType]string{
		TargetSUPI: "supiimsi", TargetPEI: "peiImei", TargetGPSI: "gpsiMsisdn",
		TargetUEIPv4: "ipv4Address", TargetUEIPv6: "ipv6Address",
		TargetTCPPort: "tcpPort", TargetUDPPort: "udpPort",
	} {
		if got, ok := typ.xmlElement(); !ok || got != want {
			t.Errorf("%s has element %q (ok=%v), want %q", typ, got, ok, want)
		}
	}
}

// TestXMLFragmentsDropsWhatItCannotRender: a list renders each identifier that has an
// element and drops the rest, rather than substituting something it is not.
func TestXMLFragmentsDropsWhatItCannotRender(t *testing.T) {
	got := XMLFragments([]TargetIdentifier{
		{Type: TargetSUPI, Value: "262019876543210"},
		{Type: TargetFSEID, Value: "42"},
		{Type: TargetPEI, Value: "357840051234567"}, // fifteen digits: no element fits
		{Type: TargetGPSI, Value: "4915123456789"},
	})
	want := []string{"<supiimsi>262019876543210</supiimsi>", "<gpsiMsisdn>4915123456789</gpsiMsisdn>"}
	if !slices.Equal(got, want) {
		t.Errorf("XMLFragments = %q, want %q", got, want)
	}
	if got := XMLFragments(nil); got != nil {
		t.Errorf("XMLFragments(nil) = %q, want nil", got)
	}
}

// TestFoldedIdentifierTypesRenderTheElementTheirValueFits: a SUPI, a PEI and a GPSI each
// stand for more than one schema element, and the attribute must name the one the value
// is. Naming one element per type would put a sixteen-digit IMEISV inside peiImei, which
// the schema limits to fourteen digits, and an NAI inside supiimsi, which is all digits —
// so a mediation function validating the attribute would discard it.
func TestFoldedIdentifierTypesRenderTheElementTheirValueFits(t *testing.T) {
	for _, tc := range []struct {
		id   TargetIdentifier
		want string // "" when no element fits
	}{
		{TargetIdentifier{Type: TargetSUPI, Value: "208930100007488"}, "supiimsi"},
		{TargetIdentifier{Type: TargetSUPI, Value: "alice@example.com"}, "supinai"},
		{TargetIdentifier{Type: TargetSUPI, Value: "12345"}, ""},
		{TargetIdentifier{Type: TargetPEI, Value: "35784005123456"}, "peiImei"},
		{TargetIdentifier{Type: TargetPEI, Value: "3578400512345678"}, "peiImeisv"},
		{TargetIdentifier{Type: TargetPEI, Value: "357840051234567"}, ""},
		{TargetIdentifier{Type: TargetGPSI, Value: "9000000002"}, "gpsiMsisdn"},
		{TargetIdentifier{Type: TargetGPSI, Value: "ext@example.com"}, "gpsiNai"},
		{TargetIdentifier{Type: TargetUEIPv4, Value: "10.0.0.1"}, "ipv4Address"},
	} {
		got, ok := tc.id.Element()
		if tc.want == "" {
			if ok {
				t.Errorf("%s %q rendered as %q; it fits no element of its type and must render as nothing",
					tc.id.Type, tc.id.Value, got)
			}

			continue
		}
		if !ok || got != tc.want {
			t.Errorf("%s %q rendered as %q (ok=%v), want %q", tc.id.Type, tc.id.Value, got, ok, tc.want)
		}
	}
}
