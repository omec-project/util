// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// answeringWith replaces one envelope field of an otherwise conformant response,
// so a test can put exactly one thing wrong and see it refused.
func answeringWith(t *testing.T, replace func(string) string) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		_, _ = w.Write([]byte(replace(conformantResponse(t, body, `<oK>AcknowledgedAndCompleted</oK>`))))
	})
}

// TestAWellFormedAcknowledgementFromTheWrongElementIsRefused is the case this
// whole section exists for.
//
// A misroute or a stale DNS record puts an endpoint in the path that is not the
// UPF the trigger was addressed to. It holds LI-domain credentials, so the
// handshake succeeds, and it answers `OK`. Accepted, that answer would have the
// triggering function record content interception as installed at a point of
// interception that never received the trigger -- and nothing downstream reveals it,
// because the product that would be missing is never produced.
func TestAWellFormedAcknowledgementFromTheWrongElementIsRefused(t *testing.T) {
	req, _ := requesterTo(t, answeringWith(t, func(resp string) string {
		return strings.Replace(resp, "<neIdentifier>upf-1</neIdentifier>",
			"<neIdentifier>upf-2</neIdentifier>", 1)
	}))

	err := req.ActivateTask(testTrigger())
	if err == nil {
		t.Fatal("a well-formed acknowledgement naming a different NE was accepted as installed tasking")
	}

	var re *ResponseError
	if !errors.As(err, &re) {
		t.Fatalf("err = %T (%v), want *ResponseError — an answer that cannot be attributed is an element-level condition, not a task refusal", err, err)
	}
	if re.Field != "neIdentifier" {
		t.Errorf("Field = %q, want %q so an operator is told which field disagreed", re.Field, "neIdentifier")
	}
}

// TestEachEnvelopeFieldIsChecked walks the rest of the binding. Each case puts one
// field wrong and nothing else, so a failure names the check that is missing
// rather than "validation".
func TestEachEnvelopeFieldIsChecked(t *testing.T) {
	cases := []struct {
		name    string
		field   string
		corrupt func(string) string
	}{
		{
			"a response to a different question", "response type",
			func(s string) string {
				return strings.Replace(s, `xsi:type="ActivateTaskResponse"`, `xsi:type="DeactivateTaskResponse"`, 1)
			},
		},
		{
			"an answer to a request this element did not send", "x1TransactionId",
			func(s string) string {
				i := strings.Index(s, "<x1TransactionId>")
				j := strings.Index(s, "</x1TransactionId>")

				return s[:i] + "<x1TransactionId>99999999-9999-4999-8999-999999999999" + s[j:]
			},
		},
		{
			"an answer addressed to a different requester", "admfIdentifier",
			func(s string) string {
				return strings.Replace(s, "<admfIdentifier>smf-1</admfIdentifier>",
					"<admfIdentifier>smf-2</admfIdentifier>", 1)
			},
		},
		{
			"a version this element does not speak", "version",
			func(s string) string {
				return strings.Replace(s, "<version>"+supportedVersion+"</version>",
					"<version>v2.0.0</version>", 1)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := requesterTo(t, answeringWith(t, c.corrupt))

			var re *ResponseError
			if err := req.ActivateTask(testTrigger()); !errors.As(err, &re) {
				t.Fatalf("err = %v, want a *ResponseError naming %s", err, c.field)
			}
			if re.Field != c.field {
				t.Errorf("Field = %q, want %q", re.Field, c.field)
			}
		})
	}
}

// TestAResponseCarryingSeveralMessagesIsRefused: every request this element sends
// asks one question, so a container carrying several answers cannot be attributed
// to it. Taking the first would be choosing which one to believe.
func TestAResponseCarryingSeveralMessagesIsRefused(t *testing.T) {
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		one := conformantResponse(t, body, `<oK>AcknowledgedAndCompleted</oK>`)
		// Duplicate the message inside the same container.
		i := strings.Index(one, "<x1ResponseMessage")
		j := strings.Index(one, "</X1Response>")
		_, _ = w.Write([]byte(one[:j] + one[i:j] + one[j:])) //nolint:errcheck // test handler
	}))

	var re *ResponseError
	if err := req.ActivateTask(testTrigger()); !errors.As(err, &re) {
		t.Fatalf("err = %v, want a *ResponseError", err)
	} else if re.Field != "message count" {
		t.Errorf("Field = %q, want %q", re.Field, "message count")
	}
}

// TestTheDetailsReaderIsBoundToo. `ReportedTasks` had none of `send`'s checks —
// not even the one `send` did have — so the two readers disagreed about how much
// of an envelope to believe. One validator, called by both, is what stops that
// recurring; this asserts the second caller actually goes through it.
func TestTheDetailsReaderIsBoundToo(t *testing.T) {
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp := conformantResponse(t, body, `<listOfTaskResponseDetails/>`)
		resp = strings.Replace(resp, "<neIdentifier>upf-1</neIdentifier>",
			"<neIdentifier>upf-2</neIdentifier>", 1)
		_, _ = w.Write([]byte(resp)) //nolint:errcheck // test handler
	}))

	var re *ResponseError
	if _, err := req.ReportedTasks(); !errors.As(err, &re) {
		t.Fatalf("err = %v, want a *ResponseError — a details answer from the wrong element is how a reconciliation learns the wrong thing", err)
	}
}

// TestADetailsAnswerIsNotRefusedForCarryingNoAcknowledgement is the check that
// would have been a permanent refusal, and is the reason section 1 measured
// before section 3 built.
//
// `oK` is not a member of the schema's X1ResponseMessage base type, and
// GetAllDetailsResponse does not extend it with one. A validator that required an
// acknowledgement of every response would refuse every details answer this
// element ever receives — and on a reconciliation path that means never learning
// what a POI holds.
func TestADetailsAnswerIsNotRefusedForCarryingNoAcknowledgement(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "upf-1", WithADMF("smf-1"))
	peer := certWithUID(t, "smf-1")
	if !st.Activate(types.InterceptTask{
		XID:     "11111111-1111-4111-8111-111111111111",
		Targets: []types.TargetIdentifier{{Type: types.TargetFSEID, Value: "42"}},
	}) {
		t.Fatal("Activate failed")
	}

	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp, err := srv.Process(body, peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		out, _ := marshalResponse(resp) //nolint:errcheck // test handler
		_, _ = w.Write(out)             //nolint:errcheck // test handler
	}))

	xids, err := req.TaskXIDs()
	if err != nil {
		t.Fatalf("TaskXIDs: %v — a details answer carries no oK, and requiring one refuses every reconciliation", err)
	}
	if len(xids) != 1 {
		t.Errorf("reported %d tasks, want 1", len(xids))
	}
}

// TestAnUnparseableRequestIsAnsweredWithATopLevelError: clause 6.1 defines the response to
// a request that cannot be parsed, and clause 7.2.2.2 rules out the alternative: "HTTP error
// codes shall only be used to indicate HTTP-level errors, and shall not be used to indicate
// errors with the X1 responses themselves." A request that arrived intact and could not be
// parsed is an X1-level error, so the answer is a 200 carrying the defined response — not a
// 400 carrying the XML decoder's own message.
func TestAnUnparseableRequestIsAnsweredWithATopLevelError(t *testing.T) {
	srv := NewServer(store.New(), "upf-1", WithADMF("smf-1"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/X1/NE",
		strings.NewReader("this is not XML at all")))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: an X1-level error is not an HTTP-level one", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "X1TopLevelErrorResponse") {
		t.Fatalf("answer is not the clause 6.1 response:\n%s", body)
	}
	for _, want := range []string{
		"<ns1:admfIdentifier>smf-1</ns1:admfIdentifier>",
		"<ns1:neIdentifier>upf-1</ns1:neIdentifier>",
		"<ns1:version>" + supportedVersion + "</ns1:version>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("answer missing %s\n%s", want, body)
		}
	}
	if strings.Contains(body, "x1TransactionId") {
		t.Error("a TopLevelError carries no x1TransactionId: table 6.1-1 says it 'shall be omitted for TopLevelError situations', and there was no readable request to take one from")
	}
	// The decoder's own message must not be on the wire. It says nothing an ADMF
	// can act on and it is not a thing this interface should be emitting.
	if strings.Contains(body, "EOF") || strings.Contains(body, "XML syntax") {
		t.Errorf("the answer leaks the decoder's error text:\n%s", body)
	}
}

// TestATopLevelErrorIsReportedAsUnattributable is the other side of the same
// message: the requester has to recognise it rather than call it malformed. It
// means the *peer* could not parse what this element sent, which is an
// element-level condition whose cause is on this side.
func TestATopLevelErrorIsReportedAsUnattributable(t *testing.T) {
	srv := NewServer(store.New(), "upf-1", WithADMF("smf-1"))
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(srv.topLevelError(nil)) //nolint:errcheck // test handler
	}))

	var re *ResponseError
	if err := req.ActivateTask(testTrigger()); !errors.As(err, &re) {
		t.Fatalf("err = %v, want a *ResponseError", err)
	} else if re.Got != "TopLevelError" {
		t.Errorf("Got = %q, want %q", re.Got, "TopLevelError")
	}
}

// ── A choice with more than one arm ──────────────────────────────

// TestAMultiArmTargetIdentifierIsRefused. The schema defines TargetIdentifier as
// an xs:choice, so a message populating two arms is invalid against it and no
// reading of it is authoritative. Accepting it as whichever arm a switch reached
// first meant the element deciding the scope of an interception the provisioning
// function had ordered.
func TestAMultiArmTargetIdentifierIsRefused(t *testing.T) {
	srv := NewServer(store.New(), "upf-1", WithADMF("admfID"))
	resp, err := srv.Process([]byte(multiArmTargetXML), certWithUID(t, "admfID"))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(resp.Messages))
	}

	m := resp.Messages[0]
	if m.ErrorInformation == nil {
		t.Fatal("a target identifier populating two arms of a choice was accepted")
	}
	// A schema error, not a task failure: the two are distinguished by whether the
	// message is well formed, and a violated choice is not. The existing ordering
	// puts malformed before unhonourable for exactly this reason.
	if m.ErrorInformation.ErrorCode != errCodeSchemaError {
		t.Errorf("errorCode = %d, want %d (schema error)", m.ErrorInformation.ErrorCode, errCodeSchemaError)
	}
	if _, ok := srv.store.Get("11111111-1111-4111-8111-111111111111"); ok {
		t.Error("the task was stored despite the refusal")
	}
}

// TestPopulatedArmsCountsEveryArm guards the guard. If a switch's ordering decided the
// answer, a counter that shared the switch's blind spots would inherit them — and a new arm
// added to the struct and not added to the counter would be silently exempt.
func TestPopulatedArmsCountsEveryArm(t *testing.T) {
	if n := populatedArms(TargetIdentifier{SUPIIMSI: "262019876543210"}); n != 1 {
		t.Errorf("one populated arm counted as %d", n)
	}
	if n := populatedArms(TargetIdentifier{}); n != 0 {
		t.Errorf("an empty identifier counted as %d", n)
	}
	if n := populatedArms(TargetIdentifier{
		SUPIIMSI: "262019876543210", IPv4Address: "10.250.0.9",
	}); n != 2 {
		t.Errorf("two populated arms counted as %d", n)
	}
	if n := populatedArms(TargetIdentifier{
		IPv4Address: "10.250.0.9",
		Extension:   &TargetIdentifierExtension{Owner: ExtensionOwner3GPP},
	}); n != 2 {
		t.Errorf("a plain arm plus an extension counted as %d", n)
	}
}

// TestTheChoiceRuleDoesNotForbidSeveralIdentifiers keeps the choice rule from being read as
// reversing the documented precedence. A task's targetIdentifiers *list* carrying
// a subscriber identifier and a packet criterion as separate entries is
// legitimate and OR-combined; what is refused is two arms inside one identifier.
func TestTheChoiceRuleDoesNotForbidSeveralIdentifiers(t *testing.T) {
	td := TaskDetails{
		XID: "11111111-1111-4111-8111-111111111111",
		TargetIdentifiers: []TargetIdentifier{
			{SUPIIMSI: "262019876543210"},
			{IPv4Address: "10.250.0.9"},
		},
	}
	if err := malformedTaskIdentifiers(td); err != nil {
		t.Errorf("two separate single-arm identifiers were refused: %v", err)
	}
}

// ── Every extension identifier is a criterion ────────────────────

// TestEveryExtensionIdentifierBecomesACriterion. UPFLIT3TargetIdentifier is a
// SEQUENCE OF CHOICE, and every member joins the task's criteria, all of which are
// required to match (TS 103 221-1 table 6.2.1.2-1). Mapping only the first would run
// an interception other than the one ordered while acknowledging it.
func TestEveryExtensionIdentifierBecomesACriterion(t *testing.T) {
	pdr := uint32(3)
	qer := uint32(7)
	got, err := mapExtensionTarget(&TargetIdentifierExtension{
		Owner: ExtensionOwner3GPP,
		UPFT3: &UPFLIT3Extensions{Identifiers: []UPFLIT3Identifier{
			{FSEID: &FSEID{SEID: 42}},
			{PDRID: &pdr},
			{QERID: &qer},
		}},
	})
	if err != nil {
		t.Fatalf("mapExtensionTarget: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("a list of three yielded %d criteria: %+v", len(got), got)
	}
	for i, want := range []types.TargetIdentifier{
		{Type: types.TargetFSEID, Value: "42"},
		{Type: types.TargetPDRID, Value: "3"},
		{Type: types.TargetQERID, Value: "7"},
	} {
		if got[i] != want {
			t.Errorf("criterion %d = %+v, want %+v", i, got[i], want)
		}
	}
}

// TestOneUnmappableMemberRefusesTheWholeTask applies the rule taskFromDetails
// already states for the outer list, one level deeper where it was not applied:
// keeping the members we understand and dropping the rest narrows the
// interception below what was ordered while answering that it was applied.
func TestOneUnmappableMemberRefusesTheWholeTask(t *testing.T) {
	_, err := mapExtensionTarget(&TargetIdentifierExtension{
		Owner: ExtensionOwner3GPP,
		UPFT3: &UPFLIT3Extensions{Identifiers: []UPFLIT3Identifier{
			{FSEID: &FSEID{SEID: 42}},
			// An FSEID criterion carrying no SEID: well formed, and it resolves to
			// nothing, so mapUPFLIT3Identifier refuses it rather than defaulting it.
			{FSEID: &FSEID{}},
		}},
	})
	if err == nil {
		t.Fatal("a list containing an unmappable member was accepted with that member dropped")
	}
	if !strings.Contains(err.Error(), "2 of 2") {
		t.Errorf("err = %v, want it to name which member of how many — an operator debugging an ADMF has nothing else to go on", err)
	}
}

// TestAnEmptyExtensionListIsStillRefused: unchanged behaviour, asserted so that
// widening the list handling did not quietly turn "no criteria" into "no
// criteria, accepted".
func TestAnEmptyExtensionListIsStillRefused(t *testing.T) {
	if _, err := mapExtensionTarget(&TargetIdentifierExtension{
		Owner: ExtensionOwner3GPP,
		UPFT3: &UPFLIT3Extensions{},
	}); err == nil {
		t.Error("an empty LI_T3 identifier list was accepted")
	}
	if _, err := mapExtensionTarget(&TargetIdentifierExtension{Owner: ExtensionOwner3GPP}); err == nil {
		t.Error("an extension carrying no LI_T3 identifiers at all was accepted")
	}
}

// multiArmTargetXML populates two arms of one targetIdentifier, which the
// schema's xs:choice says cannot occur.
const multiArmTargetXML = `<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:ActivateTaskRequest">
    <ns1:admfIdentifier>admfID</ns1:admfIdentifier>
    <ns1:neIdentifier>upf-1</ns1:neIdentifier>
    <ns1:messageTimestamp>2026-01-01T00:00:00.000000Z</ns1:messageTimestamp>
    <ns1:version>v1.6.1</ns1:version>
    <ns1:x1TransactionId>44444444-4444-4444-8444-444444444444</ns1:x1TransactionId>
    <ns1:taskDetails>
      <ns1:xId>11111111-1111-4111-8111-111111111111</ns1:xId>
      <ns1:targetIdentifiers>
        <ns1:targetIdentifier>
          <ns1:supiimsi>262019876543210</ns1:supiimsi>
          <ns1:ipv4Address>10.250.0.9</ns1:ipv4Address>
        </ns1:targetIdentifier>
      </ns1:targetIdentifiers>
      <ns1:deliveryType>X2Only</ns1:deliveryType>
      <ns1:listOfDIDs>
        <ns1:dId>33333333-3333-4333-8333-333333333333</ns1:dId>
      </ns1:listOfDIDs>
    </ns1:taskDetails>
  </ns1:x1RequestMessage>
</ns1:X1Request>`

// TestAMultiArmLIT3CriterionIsRefused is the same-class check on the inner choice.
//
// UPFLIT3TargetIdentifier is a sequence *of* choices: several members are valid,
// several arms of one member are not. Guarding the outer choice and not this one
// would leave the gap at the level where a CC-POI actually reads its detection
// criteria.
func TestAMultiArmLIT3CriterionIsRefused(t *testing.T) {
	pdr := uint32(3)
	td := TaskDetails{
		XID: "11111111-1111-4111-8111-111111111111",
		TargetIdentifiers: []TargetIdentifier{{
			Extension: &TargetIdentifierExtension{
				Owner: ExtensionOwner3GPP,
				UPFT3: &UPFLIT3Extensions{Identifiers: []UPFLIT3Identifier{
					{FSEID: &FSEID{SEID: 42}},
					{FSEID: &FSEID{SEID: 43}, PDRID: &pdr},
				}},
			},
		}},
	}

	err := malformedTaskIdentifiers(td)
	if err == nil {
		t.Fatal("an LI_T3 detection criterion populating two arms of a choice was accepted")
	}
	if code := errorCode(err); code != errCodeSchemaError {
		t.Errorf("code = %d, want %d (schema error) — a violated choice is a malformed message, not a task this element cannot honour", code, errCodeSchemaError)
	}
	if !strings.Contains(err.Error(), "2 of 2") {
		t.Errorf("err = %v, want it to name which criterion of how many", err)
	}
}

// TestSeveralSingleArmLIT3CriteriaArePermitted is the other side, and the reason
// the check counts arms per member rather than members per list: a list of several
// criteria is exactly what the structure is for.
func TestSeveralSingleArmLIT3CriteriaArePermitted(t *testing.T) {
	pdr := uint32(3)
	td := TaskDetails{
		XID: "11111111-1111-4111-8111-111111111111",
		TargetIdentifiers: []TargetIdentifier{{
			Extension: &TargetIdentifierExtension{
				Owner: ExtensionOwner3GPP,
				UPFT3: &UPFLIT3Extensions{Identifiers: []UPFLIT3Identifier{
					{FSEID: &FSEID{SEID: 42}},
					{PDRID: &pdr},
					{NetworkInstance: "696e7465726e6574"},
				}},
			},
		}},
	}

	if err := malformedTaskIdentifiers(td); err != nil {
		t.Errorf("three single-arm criteria were refused: %v", err)
	}
}

// TestPopulatedLIT3ArmsCountsEveryArm: all seven arms the schema defines, so an
// arm added to the struct and not to the counter fails here rather than being
// silently exempt.
func TestPopulatedLIT3ArmsCountsEveryArm(t *testing.T) {
	pdr := uint32(3)
	qer := uint32(7)

	for _, c := range []struct {
		name string
		id   UPFLIT3Identifier
		want int
	}{
		{"none", UPFLIT3Identifier{}, 0},
		{"FSEID", UPFLIT3Identifier{FSEID: &FSEID{SEID: 42}}, 1},
		{"PDRID", UPFLIT3Identifier{PDRID: &pdr}, 1},
		{"QERID", UPFLIT3Identifier{QERID: &qer}, 1},
		{"FTEID", UPFLIT3Identifier{FTEID: &FTEID{TEID: 1}}, 1},
		{"NetworkInstance", UPFLIT3Identifier{NetworkInstance: "696e7465726e6574"}, 1},
		{"GTPTunnelDirection", UPFLIT3Identifier{GTPTunnelDirection: GTPDirectionOutbound}, 1},
		{"PDR", UPFLIT3Identifier{PDR: "0a01"}, 1},
		{"two", UPFLIT3Identifier{PDRID: &pdr, QERID: &qer}, 2},
	} {
		if got := populatedLIT3Arms(c.id); got != c.want {
			t.Errorf("%s: counted %d arms, want %d", c.name, got, c.want)
		}
	}
}
