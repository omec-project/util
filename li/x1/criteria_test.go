// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// The element names below are taken from the published schemas — the plain
// identifiers from ETSI TS 103 221-1 (TS_103_221_01.xsd) and the extension arms
// from the 3GPP LI_T3 extension schema, namespace
// urn:3GPP:ns:li:3GPPX1Extensions:r18:v6. They are pinned here as *wire text*
// rather than as Go structs, because a struct tag typo is invisible to a test that
// builds the message through the same struct: encoding/xml silently drops an
// element no field claims, so a mis-tagged criterion would parse as absent and the
// task would be accepted intercepting less than it was told to.
const extNS = `xmlns:ns2="urn:3GPP:ns:li:3GPPX1Extensions:r18:v6"`

// withTarget replaces the fixture's e164Number identifier with the given XML,
// keeping the rest of a real ActivateTaskRequest intact.
func withTarget(inner string) string {
	body := strings.Replace(activateXML,
		"<ns1:e164Number>2125552368</ns1:e164Number>", inner, 1)

	// The extension arms live in the 3GPP namespace, declared on the envelope the
	// way a real requester declares it.
	return strings.Replace(body,
		`xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"`,
		`xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" `+extNS, 1)
}

// ext wraps one extension criterion in the targetIdentifierExtension element.
func ext(inner string) string {
	return `<ns1:targetIdentifierExtension>
            <ns1:Owner>3GPP</ns1:Owner>
            <ns2:UPFLIT3TargetIdentifierExtensions>
              <ns2:UPFLIT3TargetIdentifier>` + inner + `</ns2:UPFLIT3TargetIdentifier>
            </ns2:UPFLIT3TargetIdentifierExtensions>
          </ns1:targetIdentifierExtension>`
}

// TestParseLIT3Criteria drives every criterion of TS 33.128 table 6.2.3-7 through
// the listener as XML and checks the identifier the CC-POI ends up holding. Each
// case fails if its arm is dropped from the parser: the criterion parses as absent,
// the task is refused, and Process reports it.
func TestParseLIT3Criteria(t *testing.T) {
	cases := []struct {
		name string
		xml  string
		want types.TargetIdentifier
	}{
		// The plain TS 103 221-1 identifiers. gtpuTunnelId is the same criterion as
		// the extension's FTEID — the schema types it as a bare integer, so it maps
		// to the same identifier type with no address part.
		{
			"GTP Tunnel ID", `<ns1:gtpuTunnelId>2415919105</ns1:gtpuTunnelId>`,
			types.TargetIdentifier{Type: types.TargetFTEID, Value: "2415919105"},
		},
		{
			"UE IPv4 address", `<ns1:ipv4Address>10.250.0.9</ns1:ipv4Address>`,
			types.TargetIdentifier{Type: types.TargetUEIPv4, Value: "10.250.0.9"},
		},
		{
			"UE IPv6 address", `<ns1:ipv6Address>2001:db8::9</ns1:ipv6Address>`,
			// Stored in the schema's expanded spelling whatever form the peer sent, so the
			// answers render a value the schema's IPv6 pattern accepts.
			types.TargetIdentifier{Type: types.TargetUEIPv6, Value: "2001:0db8:0000:0000:0000:0000:0000:0009"},
		},
		{
			"UE TCP port", `<ns1:tcpPort>443</ns1:tcpPort>`,
			types.TargetIdentifier{Type: types.TargetTCPPort, Value: "443"},
		},
		{
			"UE UDP port", `<ns1:udpPort>2152</ns1:udpPort>`,
			types.TargetIdentifier{Type: types.TargetUDPPort, Value: "2152"},
		},

		// The seven 3GPP extension arms.
		{
			"PFCP Session ID", ext(`<ns2:FSEID><ns2:SEID>14426627323429955319</ns2:SEID></ns2:FSEID>`),
			types.TargetIdentifier{Type: types.TargetFSEID, Value: "14426627323429955319"},
		},
		{
			"F-TEID, TEID only", ext(`<ns2:FTEID><ns2:TEID>16777217</ns2:TEID></ns2:FTEID>`),
			types.TargetIdentifier{Type: types.TargetFTEID, Value: "16777217"},
		},
		{
			"F-TEID with address",
			ext(`<ns2:FTEID><ns2:TEID>16777217</ns2:TEID><ns2:IPv4Address>10.76.0.2</ns2:IPv4Address></ns2:FTEID>`),
			types.TargetIdentifier{Type: types.TargetFTEID, Value: "16777217@10.76.0.2"},
		},
		{
			"PDR ID", ext(`<ns2:PDRID>3</ns2:PDRID>`),
			types.TargetIdentifier{Type: types.TargetPDRID, Value: "3"},
		},
		{
			"QER ID", ext(`<ns2:QERID>7</ns2:QERID>`),
			types.TargetIdentifier{Type: types.TargetQERID, Value: "7"},
		},
		{
			// xs:hexBinary, so the value is the encoded octets, normalised to lower
			// case so that the same instance written either way compares equal.
			"Network Instance", ext(`<ns2:NetworkInstance>696E7465726E6574</ns2:NetworkInstance>`),
			types.TargetIdentifier{Type: types.TargetNetworkInstance, Value: "696e7465726e6574"},
		},
		{
			"GTP Tunnel Direction", ext(`<ns2:GTPTunnelDirection>Outbound</ns2:GTPTunnelDirection>`),
			types.TargetIdentifier{Type: types.TargetGTPTunnelDirection, Value: GTPDirectionOutbound},
		},
		{
			"PDR", ext(`<ns2:PDR>0A01</ns2:PDR>`),
			types.TargetIdentifier{Type: types.TargetPDR, Value: "0a01"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := testServer(st)
			if _, err := srv.Process([]byte(withTarget(c.xml)), admfPeer(t)); err != nil {
				t.Fatalf("Process: %v", err)
			}
			task, ok := st.Get(testXID)
			if !ok {
				t.Fatal("task not activated — the criterion did not parse")
			}
			if len(task.Targets) != 1 || task.Targets[0] != c.want {
				t.Fatalf("targets = %+v, want exactly [%+v]", task.Targets, c.want)
			}
			// The CC-POI finds its task by the criterion, so an identifier that parses
			// but does not index is still useless.
			if m := st.Match(c.want); len(m) != 1 || m[0].XID != testXID {
				t.Errorf("Match(%+v) = %+v, want the task", c.want, m)
			}
		})
	}
}

// TestRefuseCriteriaOutsideTheTable checks that a criterion the CC-POI cannot
// evaluate is refused rather than accepted and ignored. Accepting it would leave
// the triggering function believing an interception is running that collects
// nothing, with no way to discover otherwise.
func TestRefuseCriteriaOutsideTheTable(t *testing.T) {
	// An identifier type this element does not support is 3010, the registry's own entry; a
	// supported type carrying a value no packet could match is the operation's generic 3000;
	// and a criterion outside its own schema -- an empty choice, a mandatory child missing, a
	// value outside an enumeration -- is 1010, refused before anything asks whether it could
	// be honoured.
	cases := []struct {
		name, xml string
		code      int
	}{
		// Stands for any TS 103 221-1 identifier this element does not model:
		// encoding/xml drops an element no field claims, so the task must be refused
		// rather than accepted carrying no criterion at all.
		{"unmodelled identifier", `<ns1:unmodelledIdentifier>10.250.0.9</ns1:unmodelledIdentifier>`, errCodeUnsupportedTarget},
		{"empty extension", ext(``), errCodeSchemaError},
		{"extension owner only", `<ns1:targetIdentifierExtension><ns1:Owner>3GPP</ns1:Owner></ns1:targetIdentifierExtension>`, errCodeUnsupportedTarget},
		// The schema makes the SEID mandatory, so an F-SEID without one is malformed; one of 0
		// conforms and selects nothing, since no session has it.
		{"FSEID without a SEID", ext(`<ns2:FSEID><ns2:IPv4Address>10.76.0.2</ns2:IPv4Address></ns2:FSEID>`), errCodeSchemaError},
		{"FSEID with SEID 0", ext(`<ns2:FSEID><ns2:SEID>0</ns2:SEID></ns2:FSEID>`), errCodeActivateFailed},
		// Outside the closed enumeration: neither direction, so it would either
		// match nothing or both.
		{"direction outside the enumeration", ext(`<ns2:GTPTunnelDirection>Uplink</ns2:GTPTunnelDirection>`), errCodeSchemaError},
		{"empty direction", ext(`<ns2:GTPTunnelDirection/>`), errCodeSchemaError},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := testServer(st)
			resp, err := srv.Process([]byte(withTarget(c.xml)), admfPeer(t))
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			assertRejected(t, resp, st, c.code)
			if _, ok := st.Get(testXID); ok {
				t.Error("task was stored despite the criterion being refused")
			}
		})
	}
}

// TestSeveralCriteriaInOneTask checks the list form end to end: two criteria on one
// task both reach the store, and the task is found by either. A triggering function
// describing the same traffic two ways must not have one description dropped.
func TestSeveralCriteriaInOneTask(t *testing.T) {
	st := store.New()
	srv := testServer(st)
	body := withTarget(`<ns1:ipv4Address>10.250.0.9</ns1:ipv4Address>
        </ns1:targetIdentifier>
        <ns1:targetIdentifier>
          ` + ext(`<ns2:FSEID><ns2:SEID>14426627323429955319</ns2:SEID></ns2:FSEID>`))
	if _, err := srv.Process([]byte(body), admfPeer(t)); err != nil {
		t.Fatalf("Process: %v", err)
	}

	task, ok := st.Get(testXID)
	if !ok {
		t.Fatal("task not activated")
	}
	if len(task.Targets) != 2 {
		t.Fatalf("targets = %+v, want both criteria", task.Targets)
	}
	for _, id := range task.Targets {
		if m := st.Match(id); len(m) != 1 {
			t.Errorf("Match(%+v) = %+v, want the task", id, m)
		}
	}
}

// TestOneBadCriterionRefusesTheTask checks that a task carrying one evaluable and
// one unevaluable criterion is refused whole. Keeping the good one would narrow the
// interception below what was ordered while answering that it had been applied.
func TestOneBadCriterionRefusesTheTask(t *testing.T) {
	st := store.New()
	srv := testServer(st)
	body := withTarget(`<ns1:ipv4Address>10.250.0.9</ns1:ipv4Address>
        </ns1:targetIdentifier>
        <ns1:targetIdentifier>
          ` + ext(`<ns2:FSEID><ns2:SEID>0</ns2:SEID></ns2:FSEID>`))
	resp, err := srv.Process([]byte(body), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	assertRejected(t, resp, st, errCodeActivateFailed)
	if _, ok := st.Get(testXID); ok {
		t.Error("task stored with one criterion dropped")
	}
	if m := st.Match(types.TargetIdentifier{Type: types.TargetUEIPv4, Value: "10.250.0.9"}); m != nil {
		t.Errorf("the evaluable criterion was indexed anyway: %+v", m)
	}
}

// TestCanApplyRefusesBeforeAcknowledging checks that a POI's own refusal reaches
// the requester as a refusal and leaves nothing stored. The whole point of asking
// before acknowledging is that a task in the store is one this element reports as
// active — so an approval it cannot honour is undiscoverable from outside.
func TestCanApplyRefusesBeforeAcknowledging(t *testing.T) {
	st := store.New()
	var asked []types.XID
	srv := testServer(st, CanApply(func(task types.InterceptTask) error {
		asked = append(asked, task.XID)

		return fmt.Errorf("this datapath holds no state for that criterion")
	}))

	resp, err := srv.Process([]byte(activateXML), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	assertRejected(t, resp, st, errCodeActivateFailed)
	if len(asked) != 1 || asked[0] != testXID {
		t.Errorf("CanApply asked about %v, want just %q", asked, testXID)
	}
	// The reason must travel, or the requesting function has nothing to report.
	if d := resp.Messages[0].ErrorInformation.ErrorDescription; !strings.Contains(d, "no state for that criterion") {
		t.Errorf("error description = %q, want the check's own reason", d)
	}
	if _, ok := st.Get(testXID); ok {
		t.Error("a refused task was stored")
	}
}

// TestARefusedModificationFromCanApplyCarriesTheModifyCode covers the other arm of
// the code the refusal resolves to. The two are picked by one condition, and a test
// for the first proves nothing about the second — which is how a two-setting helper
// has hidden a defect in this project before.
//
// 3001 is "Generic ModifyTask failure" where 3000 is the activation's. An ADMF reads
// the code before the text, and one that names the wrong operation is as unhelpful as
// one that names nothing.
func TestARefusedModificationFromCanApplyCarriesTheModifyCode(t *testing.T) {
	st := store.New()
	// Approves the activation so there is tasking to modify, then refuses.
	var asked int
	srv := testServer(st, CanApply(func(types.InterceptTask) error {
		asked++
		if asked == 1 {
			return nil
		}

		return fmt.Errorf("this datapath holds no state for that criterion")
	}))

	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("Process (activate): %v", err)
	}

	body := strings.Replace(activateXML, "ActivateTaskRequest", "ModifyTaskRequest", 1)
	resp, err := srv.Process([]byte(body), admfPeer(t))
	if err != nil {
		t.Fatalf("Process (modify): %v", err)
	}
	if got := resp.Messages[0].ErrorInformation; got == nil || got.ErrorCode != errCodeModifyFailed {
		t.Fatalf("got %+v, want error %d", got, errCodeModifyFailed)
	}
}

// TestCanApplyApprovalStores checks the other half: an approving check does not
// change what an acknowledged activation does.
func TestCanApplyApprovalStores(t *testing.T) {
	st := store.New()
	srv := testServer(st, CanApply(func(types.InterceptTask) error { return nil }))
	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if _, ok := st.Get(testXID); !ok {
		t.Error("an approved task was not stored")
	}
}

// TestReportedIdentifiersRoundTrip is the property that makes GetTaskDetails and
// GetAllDetails worth answering: what the element reports it holds must be the same
// identifier it was given. Rendering an identifier as an element name plus a value
// could not do that for the LI_T3 criteria — they are arms of a 3GPP extension, not
// plain elements — and criteria with no mapping of their own were reported as
// `supiimsi`, telling an auditing ADMF the element was tasked by a subscriber when it
// was tasked by a tunnel or a direction.
//
// Each case is activated, reported, and the report parsed back with the same parser
// that reads a request. A rendering that is merely plausible fails here.
func TestReportedIdentifiersRoundTrip(t *testing.T) {
	criteria := []types.TargetIdentifier{
		{Type: types.TargetSUPI, Value: "262019876543210"},
		{Type: types.TargetPEI, Value: "35342500000001"},
		{Type: types.TargetGPSI, Value: "4915123456789"},
		{Type: types.TargetUEIPv4, Value: "10.250.0.9"},
		{Type: types.TargetUEIPv6, Value: "2001:0db8:0000:0000:0000:0000:0000:0009"},
		{Type: types.TargetTCPPort, Value: "443"},
		{Type: types.TargetUDPPort, Value: "2152"},
		{Type: types.TargetFSEID, Value: "14426627323429955319"},
		{Type: types.TargetFTEID, Value: "16777217"},
		{Type: types.TargetFTEID, Value: "16777217@10.76.0.2"},
		{Type: types.TargetPDRID, Value: "3"},
		{Type: types.TargetQERID, Value: "7"},
		{Type: types.TargetNetworkInstance, Value: "696e7465726e6574"},
		{Type: types.TargetGTPTunnelDirection, Value: GTPDirectionOutbound},
		{Type: types.TargetPDR, Value: "0a01"},
	}

	for _, id := range criteria {
		t.Run(string(id.Type)+"/"+id.Value, func(t *testing.T) {
			st := store.New()
			srv := testServer(st)
			if !st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{id}}) {
				t.Fatal("Activate failed")
			}

			resp, err := srv.Process([]byte(getAllDetailsXML), admfPeer(t))
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			out, err := marshalResponse(resp)
			if err != nil {
				t.Fatalf("marshalResponse: %v", err)
			}

			// Parsed with the *request-side* identifier type, which is the point: what
			// the element reports has to be readable by the same code that reads what an
			// ADMF sends. An identifier rendered under the wrong element name is worse
			// than none, and only a real parse catches that.
			var reported struct {
				Messages []struct {
					Tasks []struct {
						Details struct {
							TargetIdentifiers []TargetIdentifier `xml:"targetIdentifiers>targetIdentifier"`
						} `xml:"taskDetails"`
					} `xml:"listOfTaskResponseDetails>taskResponseDetails"`
				} `xml:"x1ResponseMessage"`
			}
			if uerr := xml.Unmarshal(out, &reported); uerr != nil {
				t.Fatalf("the reported task is not parseable XML: %v\n%s", uerr, out)
			}
			if len(reported.Messages) != 1 || len(reported.Messages[0].Tasks) != 1 {
				t.Fatalf("want one reported task, got %s", out)
			}
			ids := reported.Messages[0].Tasks[0].Details.TargetIdentifiers
			if len(ids) != 1 {
				t.Fatalf("want one reported identifier, got %s", out)
			}

			got, err := mapTarget(ids[0])
			if err != nil {
				t.Fatalf("the reported identifier does not parse back: %v\n%s", err, out)
			}
			// One identifier in, one criterion out. mapTarget returns a slice because
			// the LI_T3 extension arm is a list, but each case here activates a single
			// criterion, so a second would mean the round trip invented one.
			if len(got) != 1 {
				t.Fatalf("one identifier parsed back as %d criteria\n%s", len(got), out)
			}
			if got[0] != id {
				t.Errorf("reported %+v, want %+v\n%s", got[0], id, out)
			}
		})
	}
}

// getAllDetailsXML asks the element what tasking it holds.
const getAllDetailsXML = `<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:GetAllDetailsRequest">
    <ns1:admfIdentifier>admfID</ns1:admfIdentifier>
    <ns1:neIdentifier>neID</ns1:neIdentifier>
    <ns1:messageTimestamp>2026-01-01T00:00:00.000000Z</ns1:messageTimestamp>
    <ns1:version>v1.6.1</ns1:version>
    <ns1:x1TransactionId>99999999-9999-4999-8999-999999999999</ns1:x1TransactionId>
  </ns1:x1RequestMessage>
</ns1:X1Request>`
