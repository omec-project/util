// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// keepaliveXML is a TS 103 221-1 KeepaliveRequest (ADMF→NE).
const keepaliveXML = `<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:KeepaliveRequest">
    <ns1:admfIdentifier>admfID</ns1:admfIdentifier>
    <ns1:neIdentifier>neID</ns1:neIdentifier>
    <ns1:version>v1.6.1</ns1:version>
    <ns1:x1TransactionId>tx-ka</ns1:x1TransactionId>
  </ns1:x1RequestMessage>
</ns1:X1Request>`

func supiTarget(v string) types.TargetIdentifier {
	return types.TargetIdentifier{Type: types.TargetSUPI, Value: v}
}

// TestReportNEIssue checks the NE-initiated fault report is a well-formed X1
// ReportNEIssueRequest POSTed to the ADMF, carrying NE-level status only.
func TestReportNEIssue(t *testing.T) {
	var got []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // test handler read
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(admfResponse(t, got))) //nolint:errcheck // test handler
	}))
	defer ts.Close()

	r := NewReporter(ts.URL, "admfID", "neID", nil)
	if err := r.ReportNEIssue(NEIssueX3EgressDown, "X3 egress socket unavailable"); err != nil {
		t.Fatalf("ReportNEIssue: %v", err)
	}

	body := string(got)
	for _, want := range []string{
		`xsi:type="ns1:ReportNEIssueRequest"`,
		"<ns1:neIdentifier>neID</ns1:neIdentifier>",
		// TypeOfNEIssueMessage is a closed enumeration: the condition string itself
		// would make the whole fault channel schema-invalid.
		"<ns1:typeOfNeIssueMessage>FaultReport</ns1:typeOfNeIssueMessage>",
		// The condition still has to reach the ADMF — in the field that permits it.
		"x3EgressDown",
		"X3 egress socket unavailable",
		// And as specific a code as the registry offers.
		"<ns1:issueCode>9020</ns1:issueCode>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report body missing %q\n%s", want, body)
		}
	}
	var probe struct{}
	if err := xml.Unmarshal(got, &probe); err != nil {
		t.Errorf("report body is not valid XML: %v", err)
	}
}

// TestKeepaliveWatchdogPurgesTasking checks the TS 103 221-1 fail-safe: if no X1
// message arrives within the window, all tasking is purged.
func TestKeepaliveWatchdogPurgesTasking(t *testing.T) {
	st := store.New()
	st.Activate(types.InterceptTask{XID: "a", Targets: []types.TargetIdentifier{supiTarget("1")}})
	srv := testServer(st)
	now := time.Now()
	srv.now = func() time.Time { return now }
	srv.recordActivity() // ADMF last seen now

	now = now.Add(3 * time.Second) // within a 5s window
	srv.purgeIfLapsed(5 * time.Second)
	if len(st.Match(supiTarget("1"))) != 1 {
		t.Fatal("purged tasking while still within the keepalive window")
	}

	now = now.Add(5 * time.Second) // now 8s idle > 5s
	srv.purgeIfLapsed(5 * time.Second)
	if len(st.Match(supiTarget("1"))) != 0 {
		t.Error("keepalive lapsed but tasking was not purged")
	}
}

// TestKeepalivePurgeRunsTeardown verifies the fail-safe purge runs the per-task
// lifecycle callback (so a POI tears down product it applied elsewhere, e.g. UPF
// CC duplication), clears the store, and is a no-op on subsequent lapsed ticks
// once the store is empty.
func TestKeepalivePurgeRunsTeardown(t *testing.T) {
	st := store.New()
	st.Activate(types.InterceptTask{XID: "a", Targets: []types.TargetIdentifier{supiTarget("1")}, Products: []types.ProductType{types.ProductCC}})
	st.Activate(types.InterceptTask{XID: "b", Targets: []types.TargetIdentifier{supiTarget("2")}, Products: []types.ProductType{types.ProductCC}})
	var torn []types.XID
	srv := testServer(st, OnTaskChange(func(prev, next *types.InterceptTask) {
		if next == nil {
			torn = append(torn, prev.XID)
		}
	}))
	now := time.Now()
	srv.now = func() time.Time { return now }
	srv.recordActivity()

	now = now.Add(10 * time.Second) // lapsed past a 5s window
	srv.purgeIfLapsed(5 * time.Second)
	if st.Len() != 0 {
		t.Fatalf("store not purged: %d tasks remain", st.Len())
	}
	if len(torn) != 2 {
		t.Fatalf("the teardown ran %d times, want 2 (one per purged task)", len(torn))
	}

	// A second lapsed tick must not re-run the hook (nothing left to tear down).
	torn = nil
	now = now.Add(10 * time.Second)
	srv.purgeIfLapsed(5 * time.Second)
	if len(torn) != 0 {
		t.Errorf("second purge re-ran the teardown %d times, want 0", len(torn))
	}
}

// TestKeepaliveResetsWatchdog checks that an inbound KeepaliveRequest is
// acknowledged and resets the watchdog.
func TestKeepaliveResetsWatchdog(t *testing.T) {
	st := store.New()
	st.Activate(types.InterceptTask{XID: "a", Targets: []types.TargetIdentifier{supiTarget("1")}})
	srv := testServer(st)
	now := time.Now()
	srv.now = func() time.Time { return now }
	srv.recordActivity()

	now = now.Add(4 * time.Second)
	resp, err := srv.Process([]byte(keepaliveXML), admfPeer(t))
	if err != nil {
		t.Fatalf("keepalive: %v", err)
	}
	if resp.Messages[0].Type != "KeepaliveResponse" || resp.Messages[0].OK == "" {
		t.Errorf("keepalive response = %+v, want KeepaliveResponse/OK", resp.Messages[0])
	}

	now = now.Add(4 * time.Second) // 4s since the keepalive, < 5s window
	srv.purgeIfLapsed(5 * time.Second)
	if len(st.Match(supiTarget("1"))) != 1 {
		t.Error("keepalive should have reset the watchdog, but tasking was purged")
	}
}

// activateXML is the ETSI TS 103 221-1 ActivateTaskRequest from the reference simulator's
// sim-to-ne examples — an independent implementation's real output.
//
// Its dId is *not* theirs. The example says `pre-shared-did`, which is not the UUID the
// schema types a DId as, so it is replaced here with one. An independent implementation's
// example is authority for element names and structure -- which is what it is used for --
// and not a conformance oracle; keeping their literal value would make this element's own
// test material an unreliable guide to what a conformant ADMF sends.
const activateXML = `<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:ActivateTaskRequest">
    <ns1:admfIdentifier>admfID</ns1:admfIdentifier>
    <ns1:neIdentifier>neID</ns1:neIdentifier>
    <ns1:messageTimestamp>2017-10-06T18:46:21.247432Z</ns1:messageTimestamp>
    <ns1:version>v1.6.1</ns1:version>
    <ns1:x1TransactionId>3741800e-971b-4aa9-85f4-466d2b1adc7f</ns1:x1TransactionId>
    <ns1:taskDetails>
      <ns1:xId>50b93d1e-1b53-4d63-aacb-e4d99811bc0b</ns1:xId>
      <ns1:targetIdentifiers>
        <ns1:targetIdentifier>
          <ns1:e164Number>2125552368</ns1:e164Number>
        </ns1:targetIdentifier>
      </ns1:targetIdentifiers>
      <ns1:deliveryType>X2andX3</ns1:deliveryType>
      <ns1:listOfDIDs>
        <ns1:dId>7d1c2f60-8a4e-4a1e-9f3b-2c5d6e7f8091</ns1:dId>
      </ns1:listOfDIDs>
    </ns1:taskDetails>
  </ns1:x1RequestMessage>
</ns1:X1Request>`

const testXID = types.XID("50b93d1e-1b53-4d63-aacb-e4d99811bc0b")

// testDIDInActivateXML is the destination identifier the shared activateXML fixture
// names. TS 33.128 marks ListOfDIDs mandatory in every ActivateTask table it defines, so
// the fixture keeps naming one -- and the element refuses a task naming a destination
// identifier it cannot resolve, because storing it with the subset means an agency the
// warrant names receives nothing while provisioning reports success.
//
// So the fixture's DID is declared in the element's configuration, which is the supported
// arrangement for a destination agreed out of band.
const testDIDInActivateXML = "7d1c2f60-8a4e-4a1e-9f3b-2c5d6e7f8091"

// testServer is NewServer with that destination declared, for the tests whose subject is
// something other than destination resolution.
func testServer(st *store.Store, opts ...Option) *Server {
	return NewServer(st, "neID", append([]Option{WithConfiguredDestinations(ConfiguredDestination{
		DID:          testDIDInActivateXML,
		DeliveryType: "X2andX3",
		Address:      "10.0.60.122:42069",
	})}, opts...)...)
}

func TestProcessActivate(t *testing.T) {
	st := store.New()
	srv := testServer(st)

	resp, err := srv.Process([]byte(activateXML), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	task, ok := st.Get(testXID)
	if !ok {
		t.Fatal("task was not activated in the store")
	}
	if task.Targets[0].Type != types.TargetGPSI || task.Targets[0].Value != "2125552368" {
		t.Errorf("target = %+v, want GPSI 2125552368", task.Targets[0])
	}
	if !task.WantsProduct(types.ProductIRI) || !task.WantsProduct(types.ProductCC) {
		t.Errorf("products = %v, want IRI+CC (X2andX3)", task.Products)
	}
	if m := st.Match(task.Targets[0]); len(m) != 1 || m[0].XID != testXID {
		t.Errorf("Match(target) = %+v", m)
	}

	if len(resp.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(resp.Messages))
	}
	rm := resp.Messages[0]
	if rm.Type != "ActivateTaskResponse" || rm.OK != ackOK {
		t.Errorf("response = %+v", rm)
	}
	if rm.X1TransactionID != "3741800e-971b-4aa9-85f4-466d2b1adc7f" || rm.NeIdentifier != "neID" {
		t.Errorf("response header = %+v", rm)
	}
}

// TestParseTargetIdentifierTypes locks in the target-identifier element names
// against the ETSI TS 103 221-1 XSD (verified verbatim: supiimsi/supinai/
// peiImei/peiImeisv/gpsiMsisdn/imsi/e164Number -- the casing is the schema's own).
func TestParseTargetIdentifierTypes(t *testing.T) {
	cases := []struct {
		elem, val string
		want      types.TargetIdentifierType
	}{
		{"supiimsi", "262019876543210", types.TargetSUPI},
		{"supinai", "user@example.com", types.TargetSUPI},
		{"peiImei", "35342500000001", types.TargetPEI},
		{"peiImeisv", "3534250000000151", types.TargetPEI},
		{"gpsiMsisdn", "4915123456789", types.TargetGPSI},
	}
	for _, c := range cases {
		st := store.New()
		srv := testServer(st)
		body := strings.Replace(activateXML,
			"<ns1:e164Number>2125552368</ns1:e164Number>",
			"<ns1:"+c.elem+">"+c.val+"</ns1:"+c.elem+">", 1)
		if _, err := srv.Process([]byte(body), admfPeer(t)); err != nil {
			t.Fatalf("%s: Process: %v", c.elem, err)
		}
		task, ok := st.Get(testXID)
		if !ok {
			t.Fatalf("%s: task not activated", c.elem)
		}
		if task.Targets[0].Type != c.want || task.Targets[0].Value != c.val {
			t.Errorf("%s → target %+v, want {%s %q}", c.elem, task.Targets[0], c.want, c.val)
		}
	}
}

func TestProcessDeactivate(t *testing.T) {
	st := store.New()
	st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{{Type: types.TargetGPSI, Value: "2125552368"}}})
	srv := testServer(st)

	deactivateXML := string(request("DeactivateTaskRequest", `
    <ns1:xId>`+string(testXID)+`</ns1:xId>`))

	resp, err := srv.Process([]byte(deactivateXML), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if _, ok := st.Get(testXID); ok {
		t.Error("task still present after DeactivateTask")
	}
	if resp.Messages[0].Type != "DeactivateTaskResponse" || resp.Messages[0].OK != ackOK {
		t.Errorf("response = %+v", resp.Messages[0])
	}
}

// TestServeHTTPWithoutCertificateRejected checks the handler is fail-closed when
// no client certificate reaches it — the shape of a request arriving over plain
// HTTP, or through a proxy that terminated TLS instead of passing it through.
// The response must still be well-formed X1 the ADMF can decode.
// The authorized round trip over real mutual TLS is TestServeHTTPMutualTLS.
func TestServeHTTPWithoutCertificateRejected(t *testing.T) {
	st := store.New()
	ts := httptest.NewServer(testServer(st))
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/X1/NE", strings.NewReader(activateXML))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	// The response body must be a well-formed X1Response our own decoder accepts.
	var decoded X1Response
	if err := xml.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("response is not valid X1Response XML: %v", err)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(decoded.Messages))
	}
	if m := decoded.Messages[0]; m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeADMFCertMismatch {
		t.Errorf("want error %d, got %+v", errCodeADMFCertMismatch, m)
	}
	if _, ok := st.Get(testXID); ok {
		t.Error("task activated without an authenticated ADMF")
	}
}

func TestRejectsUnknownAndBadTarget(t *testing.T) {
	st := store.New()
	srv := testServer(st)

	// Unknown request type -> ErrorResponse, nothing stored.
	unknown := strings.Replace(activateXML, "ActivateTaskRequest", "FrobnicateRequest", 1)
	resp, err := srv.Process([]byte(unknown), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if resp.Messages[0].Type != errorResponse || resp.Messages[0].ErrorInformation == nil {
		t.Errorf("unknown type should yield ErrorResponse, got %+v", resp.Messages[0])
	}
	if st.Len() != 0 {
		t.Errorf("store should be empty after error, len=%d", st.Len())
	}
}

// TestModifyUnknownTaskRefused: a ModifyTaskRequest naming tasking this element does
// not hold must not create it -- an ADMF correcting a warrant that had been lost, to a
// restart say, would be told the correction succeeded while the element invented a task
// from whatever the modify happened to carry. It is refused with "XID does not exist on
// NE", which is the answer that sends the ADMF to activate the warrant properly.
func TestModifyUnknownTaskRefused(t *testing.T) {
	st := store.New()
	srv := testServer(st)

	modifyXML := strings.Replace(activateXML, "ActivateTaskRequest", "ModifyTaskRequest", 1)
	resp, err := srv.Process([]byte(modifyXML), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	m := resp.Messages[0]
	if m.Type != errorResponse || m.ErrorInformation == nil {
		t.Fatalf("modify of an unheld task = %+v, want ErrorResponse", m)
	}
	if m.ErrorInformation.ErrorCode != errCodeNoSuchTask {
		t.Errorf("error code = %d, want %d (XID does not exist on NE)", m.ErrorInformation.ErrorCode, errCodeNoSuchTask)
	}
	if st.Len() != 0 {
		t.Errorf("modify created %d tasks, want 0 — it must not invent tasking", st.Len())
	}
}

// TestAReplayedActivationIsAcknowledged: an ADMF that lost the answer to an activation
// resends it, and the identical task is what this element already holds — so it is
// acknowledged, and nothing changes: no second notification to the POI, the same task
// in the store. A new x1TransactionId does not make it a different task.
func TestAReplayedActivationIsAcknowledged(t *testing.T) {
	st := store.New()
	var changes int
	srv := testServer(st, OnTaskChange(func(_, _ *types.InterceptTask) { changes++ }))

	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	held, _ := st.Get(testXID)

	replay := strings.Replace(activateXML, "3741800e-971b-4aa9-85f4-466d2b1adc7f",
		"9f0c4e1a-5b2d-4c3e-8f7a-1d2e3f4a5b6c", 1)
	resp, err := srv.Process([]byte(replay), admfPeer(t))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if m := resp.Messages[0]; m.OK != ackOK {
		t.Fatalf("replayed activation = %+v, want acknowledged", m)
	}
	if again, _ := st.Get(testXID); !reflect.DeepEqual(again, held) || st.Len() != 1 {
		t.Errorf("a replay changed the store: held %+v, now %+v (%d tasks)", held, again, st.Len())
	}
	if changes != 1 {
		t.Errorf("the POI was notified %d times, want once: a replay is not a change", changes)
	}
}

// TestADifferentTaskUnderAHeldXIDIsRefused is TS 103 221-1 table 6.2.1.1-2: "it is an
// error if the XID is already present at the NE", answered 2010. Replacing the held task
// would let an ADMF believe it activated a warrant while it overwrote another; a change
// to a held task is what ModifyTask is for.
func TestADifferentTaskUnderAHeldXIDIsRefused(t *testing.T) {
	st := store.New()
	srv := testServer(st)

	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	held, _ := st.Get(testXID)

	retargeted := strings.Replace(activateXML, "2125552368", "5551234567", 1)
	resp, err := srv.Process([]byte(retargeted), admfPeer(t))
	if err != nil {
		t.Fatalf("re-activate: %v", err)
	}
	m := resp.Messages[0]
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeXIDExists {
		t.Fatalf("a different task under a held XID = %+v, want error %d", m, errCodeXIDExists)
	}
	if now, _ := st.Get(testXID); !reflect.DeepEqual(now, held) {
		t.Errorf("the refused activation changed the held task: %+v, want %+v", now, held)
	}
}

// TestMissingNeIdentifierRefused: neIdentifier is mandatory in the schema, so a
// request without one carries no evidence it was meant for this element. Waving
// it through would let tasking intended for a different network element be
// applied here.
func TestMissingNeIdentifierRefused(t *testing.T) {
	st := store.New()
	srv := testServer(st)

	noNE := strings.Replace(activateXML, "<ns1:neIdentifier>neID</ns1:neIdentifier>", "", 1)
	resp, err := srv.Process([]byte(noNE), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	m := resp.Messages[0]
	if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeUnexpectedNE {
		t.Errorf("missing neIdentifier = %+v, want error %d", m, errCodeUnexpectedNE)
	}
	if st.Len() != 0 {
		t.Errorf("task applied without an NE identifier, len=%d", st.Len())
	}
}

// TestTaskDetailsQueryAnswersWhatIsHeld: a network element keeps its
// tasking in memory, so a restart discards every warrant an ADMF provisioned and
// nothing pushes that fact anywhere. The ADMF's only recourse is to ask — so the
// answer has to be truthful about an absence, not merely about a presence.
func TestTaskDetailsQueryAnswersWhatIsHeld(t *testing.T) {
	st := store.New()
	peer := admfPeer(t)

	// A details query carries its xId at message level, not inside taskDetails, so
	// it is built rather than derived from the activate fixture.
	query := func(msgType, xid string) X1ResponseMessage {
		t.Helper()

		body := `<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:` + msgType + `">
    <ns1:admfIdentifier>admfID</ns1:admfIdentifier>
    <ns1:neIdentifier>neID</ns1:neIdentifier>
    <ns1:messageTimestamp>2017-10-06T18:46:21.247432Z</ns1:messageTimestamp>
    <ns1:version>v1.6.1</ns1:version>
    <ns1:x1TransactionId>3741800e-971b-4aa9-85f4-466d2b1adc7f</ns1:x1TransactionId>
    <ns1:xId>` + xid + `</ns1:xId>
  </ns1:x1RequestMessage>
</ns1:X1Request>`

		return processWith(t, st, peer, body).Messages[0]
	}

	const taskedXID = "50b93d1e-1b53-4d63-aacb-e4d99811bc0b"

	// Nothing tasked: asking about a warrant must say so rather than acknowledge.
	m := query("GetTaskDetailsRequest", taskedXID)
	if m.Type != errorResponse || m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeNoSuchTask {
		t.Errorf("query against an untasked element = %+v, want error %d", m, errCodeNoSuchTask)
	}

	// Now task it, and the same query must report the task.
	processWith(t, st, peer, activateXML)
	if st.Len() != 1 {
		t.Fatalf("setup: store holds %d tasks, want 1", st.Len())
	}

	m = query("GetTaskDetailsRequest", taskedXID)
	if m.Type != "GetTaskDetailsResponse" || len(m.Tasks) != 1 {
		t.Fatalf("query = %+v, want one task in a GetTaskDetailsResponse", m)
	}

	// The answer has to reach the wire, not just the struct: an ADMF compares it
	// against what it believes it provisioned.
	out, err := marshalResponse(&X1Response{Messages: []X1ResponseMessage{m}})
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}
	// taskStatus is a complex type carrying provisioningStatus, whose enumeration is
	// awaitingProvisioning / failed / complete -- not a string such as "Active", which the
	// schema does not define. What makes this form trustworthy is not this assertion but
	// TestRenderedResponsesValidate, which checks it against the published XSD.
	for _, want := range []string{
		"<ns1:taskResponseDetails>", "<ns1:taskDetails>",
		"<ns1:provisioningStatus>complete</ns1:provisioningStatus>",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("response missing %s\ngot:\n%s", want, out)
		}
	}

	// GetAllDetails reports everything held, which is what an audit needs.
	m = query("GetAllDetailsRequest", taskedXID)
	if m.Type != "GetAllDetailsResponse" || len(m.Tasks) != 1 {
		t.Errorf("GetAllDetails = %+v, want one task", m)
	}
}

// declaredNEIssues returns the condition string of every NEIssue constant declared in
// report.go, parsed from the source.
//
// The alternative is a list in the test, and the failure mode of a list in a test is that it
// is the thing an author adding a condition forgets — which is not hypothetical here: it had
// happened six times.
func declaredNEIssues(t *testing.T) []string {
	t.Helper()

	src, err := os.ReadFile("report.go")
	if err != nil {
		t.Fatalf("reading report.go: %v", err)
	}

	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\tNEIssue\w+\s*=\s*"([^"]+)"`).FindAllSubmatch(src, -1) {
		out = append(out, string(m[1]))
	}

	return out
}

// TestNEIssueEncodingsAreConformant checks every condition this implementation can
// report against the enumeration TS 103 221-1 actually defines. The failure this
// guards against is not subtle but was invisible: a value outside the enumeration
// is discarded by a conformant ADMF, so the fault it describes is never heard —
// and the test rig, which accepts anything, cannot tell the difference.
func TestNEIssueEncodingsAreConformant(t *testing.T) {
	allowed := map[string]bool{
		neIssueWarning: true, neIssueFaultCleared: true,
		neIssueFaultReport: true, neIssueAlert: true,
	}

	// Read out of report.go rather than listed here, because the list this replaced had
	// fallen six conditions behind the constants — including x2DeliveryLost, which two
	// suites exercise end to end. Nothing failed: an unlisted condition is simply never
	// checked, so the test went on claiming to cover "every condition this implementation
	// can report" while a new one could be added, reported in production, and discarded by
	// a conformant ADMF without this ever going red.
	conditions := declaredNEIssues(t)
	if len(conditions) < 20 {
		t.Fatalf("parsed %d conditions out of report.go; the declarations must have moved, and a "+
			"parse that finds too few is a test that checks too little", len(conditions))
	}

	// issueCode is conditional, not mandatory: it is required when the condition
	// appears in the registry's issue-code section. A refused provisioning attempt
	// does not — that section has nothing security-related — so omitting it is the
	// conformant choice, and borrowing an unrelated code would misdescribe it. Every
	// other condition must still carry one.
	codeOptional := map[string]bool{NEIssueX1AuthFailed: true}

	for _, c := range conditions {
		e, known := neIssueEncodings[c]
		if !known {
			t.Errorf("%s has no encoding, so it would be reported only generically", c)

			continue
		}

		if !allowed[e.kind] {
			t.Errorf("%s reports type %q, which is not one of the four the schema permits", c, e.kind)
		}

		if e.code == 0 && !codeOptional[c] {
			t.Errorf("%s carries no issue code; the standard asks for the most specific available", c)
		}
	}

	// An unknown condition must still produce a valid message: reporting a fault
	// less specifically beats having it discarded.
	if fallback := encodeNEIssue("something-added-later"); !allowed[fallback.kind] {
		t.Errorf("an unmapped condition yields type %q, which the schema does not permit", fallback.kind)
	}
}

// activateWithServiceTypesXML narrows a task to a CSP service type. TS 33.128
// clause 5.2.4 lets the LIPF send this, and states that a POI receiving a
// ServiceType it does not support "shall reject the task with an appropriate
// error" — because the alternative is delivering every service when a narrower
// set was authorised, which is more product than the warrant allows.
const activateWithServiceTypesXML = `<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:ActivateTaskRequest">
    <ns1:admfIdentifier>admfID</ns1:admfIdentifier>
    <ns1:neIdentifier>neID</ns1:neIdentifier>
    <ns1:version>v1.6.1</ns1:version>
    <ns1:x1TransactionId>tx-svc</ns1:x1TransactionId>
    <ns1:taskDetails>
      <ns1:xId>50b93d1e-1b53-4d63-aacb-e4d99811bc0b</ns1:xId>
      <ns1:targetIdentifiers>
        <ns1:targetIdentifier><ns1:supiimsi>208930100007488</ns1:supiimsi></ns1:targetIdentifier>
      </ns1:targetIdentifiers>
      <ns1:deliveryType>X2Only</ns1:deliveryType>
      <ns1:listOfServiceTypes><ns1:serviceType>Data</ns1:serviceType></ns1:listOfServiceTypes>
    </ns1:taskDetails>
  </ns1:x1RequestMessage>
</ns1:X1Request>`

func TestActivateWithServiceTypeScopingIsRefused(t *testing.T) {
	st := store.New()
	srv := testServer(st)

	resp, err := srv.Process([]byte(activateWithServiceTypesXML), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Refusals travel in the response, not as a Go error, so the ADMF sees a
	// well-formed ErrorResponse it can act on. assertRejected also checks nothing
	// was tasked: a refusal that half-applied would be worse than either outcome.
	// 3050 is the registry's own "Unsupported ServiceType". It stood in as the generic
	// 1080 until TS 103 221-1 table 6.7-3 was to hand, with a note in the code to
	// substitute the specific value once confirmed rather than invent one.
	assertRejected(t, resp, st, errCodeBadServiceType)
}

// taskChangeLog records what the lifecycle callback was told, in order. Both sides
// are copied out: they are pointers to the caller's values, valid only for the
// duration of the call.
type taskChangeLog struct {
	prev, next []*types.InterceptTask
}

func (l *taskChangeLog) record(prev, next *types.InterceptTask) {
	l.prev = append(l.prev, cloneTaskPtr(prev))
	l.next = append(l.next, cloneTaskPtr(next))
}

func cloneTaskPtr(t *types.InterceptTask) *types.InterceptTask {
	if t == nil {
		return nil
	}
	c := *t

	return &c
}

// TestOnTaskChangeCarriesBothSides: a ModifyTask keeps the XID, so the previous
// and the next task are the same key. Reporting them as two independent events
// under that key asks a POI to infer an ordering the provisioning interface never
// stated — and where the POI installs state for the new task and removes state for
// the old, the removal can reclaim what the installation just created.
func TestOnTaskChangeCarriesBothSides(t *testing.T) {
	st := store.New()
	var log taskChangeLog
	srv := testServer(st, OnTaskChange(log.record))

	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if len(log.prev) != 1 || log.prev[0] != nil || log.next[0] == nil {
		t.Fatalf("activation reported as prev=%v next=%v, want (nil, task)", log.prev, log.next)
	}
	if log.next[0].XID != testXID {
		t.Errorf("activation carried XID %q, want %q", log.next[0].XID, testXID)
	}

	// A retarget: one event, both sides, so the POI reconciles rather than sequences.
	retargetXML := strings.Replace(
		strings.Replace(activateXML, "ActivateTaskRequest", "ModifyTaskRequest", 1),
		"2125552368", "5551234567", 1)
	if _, err := srv.Process([]byte(retargetXML), admfPeer(t)); err != nil {
		t.Fatalf("retarget: %v", err)
	}
	if len(log.prev) != 2 {
		t.Fatalf("retarget fired %d events, want 1", len(log.prev)-1)
	}
	if log.prev[1] == nil || log.prev[1].Targets[0].Value != "2125552368" {
		t.Errorf("retarget prev = %+v, want the task as it was, targeting 2125552368", log.prev[1])
	}
	if log.next[1] == nil || log.next[1].Targets[0].Value != "5551234567" {
		t.Errorf("retarget next = %+v, want the task as it becomes, targeting 5551234567", log.next[1])
	}

	// An ActivateTask naming a held XID with a different task is refused with 2010, so
	// it changes nothing and the POI hears nothing.
	conflictXML := strings.Replace(activateXML, "2125552368", "5555550000", 1)
	if _, err := srv.Process([]byte(conflictXML), admfPeer(t)); err != nil {
		t.Fatalf("conflicting activate: %v", err)
	}
	if len(log.prev) != 2 {
		t.Fatalf("a refused activation over a held XID fired %d events, want none", len(log.prev)-2)
	}

	// Removal is the same event with no next.
	deactivateXML := string(request("DeactivateTaskRequest", `
    <ns1:xId>`+string(testXID)+`</ns1:xId>`))
	if _, err := srv.Process([]byte(deactivateXML), admfPeer(t)); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if len(log.prev) != 3 || log.prev[2] == nil || log.next[2] != nil {
		t.Fatalf("deactivation reported as prev=%v next=%v, want (task, nil)", log.prev[2], log.next[2])
	}
}

// TestExactReplayIsANoOp: re-provisioning is how a provisioning function restores
// tasking after an element restarts, so it must not be refused — and it must not
// be mistaken for a change. An interception that never stopped has no beginning to
// report, and state that is already correct has nothing to tear down.
func TestExactReplayIsANoOp(t *testing.T) {
	st := store.New()
	var log taskChangeLog
	srv := testServer(st, OnTaskChange(log.record))

	for i := range 3 {
		if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
			t.Fatalf("activate %d: %v", i, err)
		}
	}
	if len(log.prev) != 1 {
		t.Errorf("three identical activations fired %d events, want 1 — the ADMF's recovery "+
			"path would re-emit a start of interception on every replay", len(log.prev))
	}
	if st.Len() != 1 {
		t.Errorf("store holds %d tasks after a replay, want 1", st.Len())
	}
}

// TestProductChangeReachesThePOI covers the two modifications that a "something changed"
// derived from the target identifiers alone would miss: adding CC would never begin
// content interception for sessions the target already had, and removing it would leave
// content interception running after the authority for it was withdrawn.
func TestProductChangeReachesThePOI(t *testing.T) {
	st := store.New()
	var log taskChangeLog
	srv := testServer(st, OnTaskChange(log.record))

	// IRI only to begin with.
	iriOnly := strings.Replace(activateXML, "<ns1:deliveryType>X2andX3</ns1:deliveryType>",
		"<ns1:deliveryType>X2Only</ns1:deliveryType>", 1)
	if _, err := srv.Process([]byte(iriOnly), admfPeer(t)); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Add CC, target untouched.
	addCC := strings.Replace(activateXML, "ActivateTaskRequest", "ModifyTaskRequest", 1)
	if _, err := srv.Process([]byte(addCC), admfPeer(t)); err != nil {
		t.Fatalf("add CC: %v", err)
	}
	if len(log.prev) != 2 {
		t.Fatalf("adding CC to a live task fired %d events, want 1", len(log.prev)-1)
	}
	if log.prev[1].WantsProduct(types.ProductCC) || !log.next[1].WantsProduct(types.ProductCC) {
		t.Errorf("the products change is not visible in the event: prev=%v next=%v",
			log.prev[1].Products, log.next[1].Products)
	}

	// And back: removing CC is a lifecycle transition too, which is what withdraws
	// the UPF's duplication.
	removeCC := strings.Replace(iriOnly, "ActivateTaskRequest", "ModifyTaskRequest", 1)
	if _, err := srv.Process([]byte(removeCC), admfPeer(t)); err != nil {
		t.Fatalf("remove CC: %v", err)
	}
	if len(log.prev) != 3 {
		t.Fatalf("removing CC from a live task fired %d events, want 1", len(log.prev)-2)
	}
	if !log.prev[2].WantsProduct(types.ProductCC) || log.next[2].WantsProduct(types.ProductCC) {
		t.Errorf("the products change is not visible in the event: prev=%v next=%v",
			log.prev[2].Products, log.next[2].Products)
	}
}

// TestPurgeReasonNamesThePath: a fail-safe purge means the controlling function
// stopped answering, which an operator must investigate. An expected withdrawal is
// not that, and an element that reports both the same way teaches its operator to
// ignore the channel the durability of withdrawal depends on.
func TestPurgeReasonNamesThePath(t *testing.T) {
	st := store.New()
	var reasons []PurgeReason
	var torn []types.XID
	srv := testServer(st,
		OnTaskChange(func(prev, next *types.InterceptTask) {
			if next == nil {
				torn = append(torn, prev.XID)
			}
		}),
		OnPurge(func(task types.InterceptTask, reason PurgeReason) {
			if _, held := st.Get(task.XID); held {
				t.Errorf("the purge of %s was reported while the task was still held", task.XID)
			}
			reasons = append(reasons, reason)
		}),
	)

	deactivateXML := string(request("DeactivateTaskRequest", `
    <ns1:xId>`+string(testXID)+`</ns1:xId>`))

	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := srv.Process([]byte(deactivateXML), admfPeer(t)); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if len(reasons) != 1 || reasons[0] != PurgeWithdrawal {
		t.Fatalf("an explicit deactivation reported %v, want one PurgeWithdrawal", reasons)
	}

	// Bulk deactivation: expected too, and its own reason.
	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("re-activate: %v", err)
	}
	srv.deactivateAll()
	if len(reasons) != 2 || reasons[1] != PurgeBulkDeactivate {
		t.Fatalf("bulk deactivation reported %v, want PurgeBulkDeactivate", reasons)
	}

	// Only the fail-safe can produce PurgeKeepaliveLapse.
	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("re-activate: %v", err)
	}
	now := time.Now()
	srv.now = func() time.Time { return now }
	srv.recordActivity()
	now = now.Add(10 * time.Second)
	srv.purgeIfLapsed(5 * time.Second)
	if len(reasons) != 3 || reasons[2] != PurgeKeepaliveLapse {
		t.Fatalf("the keepalive fail-safe reported %v, want PurgeKeepaliveLapse", reasons)
	}

	// Every path tore the task down, whatever it was called.
	if len(torn) != 3 {
		t.Errorf("teardown ran %d times over three removals", len(torn))
	}
}
