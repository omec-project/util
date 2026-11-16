// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// detailsAnswer activates a task carrying the given target identifier, then asks the
// element what it holds and returns the marshalled answer.
func detailsAnswer(t *testing.T, srv *Server, target string) string {
	t.Helper()

	mustAck(t, srv, withTarget(target))

	resp, err := srv.Process([]byte(getAllDetailsXML), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	out, err := marshalResponse(resp)
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}

	return string(out)
}

// TestAnswersUseTheElementTheRequestUsed: the identifier types fold several elements into
// one, and an answer rendered from the type alone reported a supinai as supiimsi, a
// sixteen-digit IMEISV inside peiImei and an E.164 number as an MSISDN. The first two are
// invalid against the schema, so an ADMF validating the details answer — the one it
// audits with — discarded the whole answer; the third validates and claims an identity
// the ADMF never sent.
func TestAnswersUseTheElementTheRequestUsed(t *testing.T) {
	requireXmllint(t)

	for _, c := range []struct{ element, value string }{
		{"supinai", "alice@example.com"},
		{"peiImeisv", "3578400512345678"},
		{"peiImei", "35784005123456"},
		{"e164Number", "2125552368"},
		{"imsi", "262019876543210"},
		{"gtpuTunnelId", "16777217"},
	} {
		t.Run(c.element, func(t *testing.T) {
			out := detailsAnswer(t, testServer(store.New()),
				"<ns1:"+c.element+">"+c.value+"</ns1:"+c.element+">")

			if want := "<ns1:" + c.element + ">" + c.value + "</ns1:" + c.element + ">"; !strings.Contains(out, want) {
				t.Errorf("the answer does not carry %s as requested:\n%s", want, out)
			}
			if problems := validateAgainstSchema(t, []byte(out)); len(problems) > 0 {
				t.Errorf("the answer is invalid against the schema: %v\n%s", problems, out)
			}
		})
	}
}

// TestTargetValuesOutsideTheirSchemaTypeAreRefused: every typed arm is a TS 103 280
// pattern or range type, and a value outside it was acknowledged, stored as a criterion no
// session can match, and echoed back as what the element holds. A value violating its own
// type is a schema error, 1010, by the package's rule.
func TestTargetValuesOutsideTheirSchemaTypeAreRefused(t *testing.T) {
	for _, c := range []struct{ name, target string }{
		{"supiimsi with letters", "<ns1:supiimsi>not-a-number</ns1:supiimsi>"},
		{"imsi too short", "<ns1:imsi>1</ns1:imsi>"},
		{"peiImei of fifteen digits", "<ns1:peiImei>357840051234567</ns1:peiImei>"},
		{"peiImeisv of fourteen digits", "<ns1:peiImeisv>35784005123456</ns1:peiImeisv>"},
		{"gpsiMsisdn with a plus", "<ns1:gpsiMsisdn>+4915123456789</ns1:gpsiMsisdn>"},
		{"e164Number of sixteen digits", "<ns1:e164Number>1234567890123456</ns1:e164Number>"},
		{"gtpuTunnelId not a number", "<ns1:gtpuTunnelId>abc</ns1:gtpuTunnelId>"},
		{"gtpuTunnelId negative", "<ns1:gtpuTunnelId>-5</ns1:gtpuTunnelId>"},
		{"tcpPort 1, which the type excludes", "<ns1:tcpPort>1</ns1:tcpPort>"},
		{"udpPort past the range", "<ns1:udpPort>70000</ns1:udpPort>"},
		{"ipv4Address octet past 255", "<ns1:ipv4Address>10.0.0.256</ns1:ipv4Address>"},
		{"ipv4Address with three octets", "<ns1:ipv4Address>10.0.1</ns1:ipv4Address>"},
		{"ipv6Address with a zone", "<ns1:ipv6Address>fe80::1%eth0</ns1:ipv6Address>"},
		{"ipv6Address IPv4-mapped", "<ns1:ipv6Address>::ffff:10.0.0.1</ns1:ipv6Address>"},
		// xs:token collapses XML whitespace only; a no-break space is part of the value.
		{"supiimsi after a no-break space", "<ns1:supiimsi>\u00a0262019876543210</ns1:supiimsi>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			m := serve(t, testServer(st), withTarget(c.target))

			if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
				t.Fatalf("want a 1010 refusal, got %+v", m.ErrorInformation)
			}
			if _, held := st.Get(testXID); held {
				t.Error("a refused task was stored")
			}
		})
	}
}

// TestEquivalentSpellingsAreStoredCanonically: matching compares strings, and the schema
// admits several spellings of one value — surrounding whitespace on a token, a sign or
// leading zeros on an integer, the compressed and upper-case forms of an IPv6 address. A
// task stored as spelled never equalled the identifier a consumer built in another.
func TestEquivalentSpellingsAreStoredCanonically(t *testing.T) {
	for _, c := range []struct {
		name, target string
		want         types.TargetIdentifier
	}{
		{
			"token whitespace", "<ns1:supiimsi> 262019876543210 </ns1:supiimsi>",
			types.TargetIdentifier{Type: types.TargetSUPI, Value: "262019876543210"},
		},
		{
			"integer sign and leading zeros", "<ns1:tcpPort>+0443</ns1:tcpPort>",
			types.TargetIdentifier{Type: types.TargetTCPPort, Value: "443"},
		},
		{
			"TEID leading zeros", "<ns1:gtpuTunnelId>007</ns1:gtpuTunnelId>",
			types.TargetIdentifier{Type: types.TargetFTEID, Value: "7"},
		},
		{
			// The schema's IPv4Address pattern admits a leading zero in an octet.
			"IPv4 zero-padded octets", "<ns1:ipv4Address>010.000.000.001</ns1:ipv4Address>",
			types.TargetIdentifier{Type: types.TargetUEIPv4, Value: "10.0.0.1"},
		},
		{
			"IPv6 compressed upper case", "<ns1:ipv6Address>2001:DB8::9</ns1:ipv6Address>",
			types.TargetIdentifier{Type: types.TargetUEIPv6, Value: "2001:0db8:0000:0000:0000:0000:0000:0009"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			mustAck(t, testServer(st), withTarget(c.target))

			task, ok := st.Get(testXID)
			if !ok {
				t.Fatal("task not stored")
			}
			if len(task.Targets) != 1 || task.Targets[0] != c.want {
				t.Errorf("stored %+v, want %+v", task.Targets, c.want)
			}
		})
	}
}

// TestAGtpuTunnelIdPastATEIDIsRefused: the schema's GtpTunnelId reaches 4294967296, one
// past what a TEID holds. Schema-valid, so not 1010 — but no tunnel carries it, and a task
// acknowledged on it would produce nothing. Refused with the operation's own code.
func TestAGtpuTunnelIdPastATEIDIsRefused(t *testing.T) {
	st := store.New()
	m := serve(t, testServer(st), withTarget("<ns1:gtpuTunnelId>4294967296</ns1:gtpuTunnelId>"))

	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeActivateFailed {
		t.Fatalf("want a 3000 refusal, got %+v", m.ErrorInformation)
	}
	if _, held := st.Get(testXID); held {
		t.Error("a task naming no possible tunnel was stored")
	}
}

// TestAModelledAndAnUnmodelledArmTogetherAreRefused: encoding/xml drops an element no
// field claims, so without counting it an IMSI beside a macAddress would count as one arm,
// and the element would narrow the interception to the IMSI and acknowledge it -- the
// element deciding the scope of a warrant, which two modelled arms are refused for.
func TestAModelledAndAnUnmodelledArmTogetherAreRefused(t *testing.T) {
	st := store.New()
	m := serve(t, testServer(st), withTarget(
		"<ns1:imsi>262019876543210</ns1:imsi><ns1:macAddress>00-11-22-33-44-55</ns1:macAddress>"))

	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeSchemaError {
		t.Fatalf("want a 1010 refusal, got %+v", m.ErrorInformation)
	}
	if _, held := st.Get(testXID); held {
		t.Error("the task was stored, narrowed to the arm the element understood")
	}
}
