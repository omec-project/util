// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

func testTrigger() Trigger {
	return Trigger{
		XID:           "11111111-1111-4111-8111-111111111111",
		ProductID:     "22222222-2222-4222-8222-222222222222",
		CorrelationID: 0x2632898145f4d191,
		SEID:          14426627323429955319,
		SEIDAddress:   "10.0.1.5",
		DIDs:          []string{"33333333-3333-4333-8333-333333333333"},
	}
}

// conformantResponse answers requestBody the way a conformant peer does: the
// response type derived from the request type, and the envelope fields of the
// schema's X1ResponseMessage base type — echoed where the peer echoes them,
// stated where it states them. payload is the type-specific body.
//
// A bare `<oK>` is not a response any conformant NE sends, and building the
// requester's response validation against such a stub would be building it against a
// fiction: the stub would go on passing while a real peer's answer was refused.
func conformantResponse(t *testing.T, requestBody []byte, payload string) string {
	t.Helper()

	var in X1Request
	if err := xml.Unmarshal(requestBody, &in); err != nil {
		t.Fatalf("stub could not parse the request it is answering: %v", err)
	}
	if len(in.Messages) != 1 {
		t.Fatalf("stub received %d request messages, want 1", len(in.Messages))
	}
	m := in.Messages[0]

	return `<?xml version="1.0"?><X1Response xmlns="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<x1ResponseMessage xsi:type="` + responseTypeFor(localType(m.Type)) + `">` +
		`<admfIdentifier>` + m.AdmfIdentifier + `</admfIdentifier>` +
		`<neIdentifier>` + m.NeIdentifier + `</neIdentifier>` +
		`<messageTimestamp>` + m.MessageTimestamp + `</messageTimestamp>` +
		`<version>` + m.Version + `</version>` +
		`<x1TransactionId>` + m.X1TransactionID + `</x1TransactionId>` +
		payload +
		`</x1ResponseMessage></X1Response>`
}

// acknowledging is the stub every happy-path test wants: a conformant envelope
// carrying an acknowledgement.
func acknowledging(t *testing.T) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)                                                            //nolint:errcheck // test handler
		_, _ = w.Write([]byte(conformantResponse(t, body, `<oK>AcknowledgedAndCompleted</oK>`))) //nolint:errcheck // test handler
	})
}

// requesterTo returns a Requester pointed at h, plus a pointer to the last body
// h received, so a test can assert both the wire form and the outcome.
func requesterTo(t *testing.T, h http.Handler) (*Requester, *string) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		body = string(b)
		// Put the body back: the handler under test has to read it too.
		r.Body = io.NopCloser(bytes.NewReader(b))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewRequester(srv.URL, "smf-1", "upf-1", nil), &body
}

// TestTriggerWireForm pins the element names and namespaces against the official
// schemas (TS_103_221_01.xsd and urn_3GPP_ns_li_3GPPX1Extensions.xsd). These are
// the details a round-trip through our own codec cannot police — the same gap
// that let a set of wrong field tags survive.
func TestTriggerWireForm(t *testing.T) {
	okHandler := acknowledging(t)
	req, body := requesterTo(t, okHandler)
	if err := req.ActivateTask(testTrigger()); err != nil {
		t.Fatalf("ActivateTask: %v", err)
	}

	for _, want := range []string{
		`xsi:type="ns1:ActivateTaskRequest"`,
		`xmlns:ext="urn:3GPP:ns:li:3GPPX1Extensions:r18:v6"`,
		`<ns1:Owner>3GPP</ns1:Owner>`,
		`<ext:UPFLIT3TargetIdentifierExtensions>`,
		`<ext:UPFLIT3TargetIdentifier>`,
		`<ext:SEID>14426627323429955319</ext:SEID>`,
		`<ext:IPv4Address>10.0.1.5</ext:IPv4Address>`,
		`<ns1:deliveryType>X3Only</ns1:deliveryType>`,
		// Decimal, not hex: the schema types correlationID as
		// xs:nonNegativeInteger.
		`<ns1:correlationID>2752413510594253201</ns1:correlationID>`,
		`<ns1:productID>22222222-2222-4222-8222-222222222222</ns1:productID>`,
	} {
		if !strings.Contains(*body, want) {
			t.Errorf("request body missing %s\ngot:\n%s", want, *body)
		}
	}

	// taskDetails is an xs:sequence, so correlationID must precede productID and
	// both must follow listOfDIDs. A schema-validating NE rejects any other order.
	dids := strings.Index(*body, "<ns1:listOfDIDs>")
	corr := strings.Index(*body, "<ns1:correlationID>")
	prod := strings.Index(*body, "<ns1:productID>")
	if dids >= corr || corr >= prod {
		t.Errorf("taskDetails element order violates the xs:sequence: listOfDIDs=%d correlationID=%d productID=%d", dids, corr, prod)
	}
}

// TestTriggerRoundTripThroughListener sends a trigger to our own X1 listener and
// checks the task it stores. This is what proves the requester and the NE agree:
// the CC-POI must end up holding the warrant XID and the correlation value, keyed
// by a target it can match on the datapath.
func TestTriggerRoundTripThroughListener(t *testing.T) {
	st := store.New()
	// The listener authenticates its peer per clause 8.2.4, so the trigger has to
	// present a certificate binding the identifier the requester asserts — being
	// the triggering function is not authority in itself. httptest serves plain
	// HTTP, so the peer certificate is supplied to Process directly, as the other
	// listener tests do.
	// The trigger's destination is declared, because a task naming one this element
	// cannot resolve is refused — and what this test is about is the trigger round trip,
	// not destination resolution.
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), HonoursCorrelationID(),
		WithConfiguredDestinations(ConfiguredDestination{
			DID:          testTrigger().DIDs[0],
			DeliveryType: deliveryX3Only,
			Address:      "192.0.2.1:42069",
		}))
	peer := certWithUID(t, "smf-1")
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp, err := srv.Process(body, peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := marshalResponse(resp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(out) //nolint:errcheck // test handler
	}))

	tr := testTrigger()
	if err := req.ActivateTask(tr); err != nil {
		t.Fatalf("ActivateTask: %v", err)
	}

	got, ok := st.Get(tr.XID)
	if !ok {
		t.Fatal("listener did not store the trigger task")
	}
	if got.Targets[0].Type != types.TargetFSEID || got.Targets[0].Value != "14426627323429955319" {
		t.Errorf("target = %+v, want FSEID 14426627323429955319", got.Targets[0])
	}
	if got.CorrelationID != tr.CorrelationID {
		t.Errorf("CorrelationID = %d, want %d", got.CorrelationID, tr.CorrelationID)
	}
	if got.ProductID != tr.ProductID {
		t.Errorf("ProductID = %q, want %q", got.ProductID, tr.ProductID)
	}
	// The whole point: product is labelled with the warrant, not with the
	// trigger task that installed it.
	if got.DeliveryXID() != tr.ProductID {
		t.Errorf("DeliveryXID() = %q, want the warrant XID %q", got.DeliveryXID(), tr.ProductID)
	}
	if !got.WantsProduct(types.ProductCC) || got.WantsProduct(types.ProductIRI) {
		t.Errorf("products = %v, want CC only", got.Products)
	}

	// A matching lookup by detection criterion is how the CC-POI will find this
	// task when a duplicated packet arrives tagged with its F-SEID.
	if n := len(st.Match(got.Targets[0])); n != 1 {
		t.Errorf("Match by F-SEID returned %d tasks, want 1", n)
	}

	if err := req.DeactivateTask(tr.XID); err != nil {
		t.Fatalf("DeactivateTask: %v", err)
	}
	if _, ok := st.Get(tr.XID); ok {
		t.Error("task still present after DeactivateTask")
	}
}

// TestCreateDestinationWireForm pins destinationDetails against its xs:sequence
// and checks that the address lands in the right TS 103 280 arm — the delivery
// address is mandatory, so an NE that cannot parse it has nowhere to send product.
func TestCreateDestinationWireForm(t *testing.T) {
	okHandler := acknowledging(t)

	t.Run("ipv4", func(t *testing.T) {
		req, body := requesterTo(t, okHandler)
		err := req.CreateDestination(Destination{
			DID:          "33333333-3333-4333-8333-333333333333",
			DeliveryType: "X3Only",
			Address:      "192.0.2.1",
			Port:         42069,
		})
		if err != nil {
			t.Fatalf("CreateDestination: %v", err)
		}
		for _, want := range []string{
			`xsi:type="ns1:CreateDestinationRequest"`,
			`<ns1:dId>33333333-3333-4333-8333-333333333333</ns1:dId>`,
			`<ns1:deliveryType>X3Only</ns1:deliveryType>`,
			`<ns1:ipAddressAndPort>`,
			`<c:IPv4Address>192.0.2.1</c:IPv4Address>`,
			// The schema's Port is a CHOICE of child elements, not a number in the
			// element's text. X2/X3 are carried over TCP, so the arm is TCPPort.
			`<c:TCPPort>42069</c:TCPPort>`,
		} {
			if !strings.Contains(*body, want) {
				t.Errorf("request body missing %s\ngot:\n%s", want, *body)
			}
		}
	})

	// TS 103 280 types an IPv6Address as `([0-9a-f]{4}:){7}([0-9a-f]{4})`, so the
	// compressed literal Go hands back from net.IP.String is the one form the schema
	// refuses: the assertion is the expanded form, which is what the schema accepts rather
	// than whatever the code happens to produce.
	t.Run("ipv6 picks the other arm, expanded as the schema types it", func(t *testing.T) {
		req, body := requesterTo(t, okHandler)
		if err := req.CreateDestination(Destination{
			DID:          "33333333-3333-4333-8333-333333333333",
			DeliveryType: "X3Only",
			Address:      "2001:db8::1",
			Port:         42069,
		}); err != nil {
			t.Fatalf("CreateDestination: %v", err)
		}
		if !strings.Contains(*body, `<c:IPv6Address>2001:0db8:0000:0000:0000:0000:0000:0001</c:IPv6Address>`) {
			t.Errorf("IPv6 address did not use the IPv6Address arm in its expanded form\ngot:\n%s", *body)
		}
	})

	t.Run("rejects incomplete or unknown", func(t *testing.T) {
		req, _ := requesterTo(t, okHandler)
		for name, d := range map[string]Destination{
			"no DID":           {DeliveryType: "X3Only", Address: "10.0.0.1", Port: 1},
			"no address":       {DID: "d", DeliveryType: "X3Only", Port: 1},
			"no port":          {DID: "d", DeliveryType: "X3Only", Address: "10.0.0.1"},
			"bad deliveryType": {DID: "d", DeliveryType: "X4Only", Address: "10.0.0.1", Port: 1},
		} {
			if err := req.CreateDestination(d); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	})
}

// TestDestinationProvisioningResolvesDIDs covers the provisioning half of the
// trigger flow: a task carries DIDs, not addresses, so a destination installed
// with CreateDestination is what tells the POI where its product goes.
func TestDestinationProvisioningResolvesDIDs(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), HonoursCorrelationID())
	peer := certWithUID(t, "smf-1")
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

	const did = "33333333-3333-4333-8333-333333333333"
	if err := req.CreateDestination(Destination{
		DID: did, DeliveryType: "X3Only", Address: "192.0.2.1", Port: 42069,
	}); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	// Re-creating the same DID must fail, so a misconfiguration cannot quietly
	// redirect an agency's product (TS 103 221-1 clause 6.3.1.1).
	if err := req.CreateDestination(Destination{
		DID: did, DeliveryType: "X3Only", Address: "10.0.0.9", Port: 1,
	}); err == nil {
		t.Error("re-creating an existing DID was accepted")
	}

	tr := testTrigger()
	tr.DIDs = []string{did}
	if err := req.ActivateTask(tr); err != nil {
		t.Fatalf("ActivateTask: %v", err)
	}

	task, ok := st.Get(tr.XID)
	if !ok {
		t.Fatal("task not stored")
	}
	if len(task.Deliveries) != 1 {
		t.Fatalf("Deliveries = %+v, want the one provisioned destination", task.Deliveries)
	}
	if got := task.Deliveries[0].Address; got != "192.0.2.1:42069" {
		t.Errorf("destination address = %q, want 192.0.2.1:42069", got)
	}
	if got := task.Deliveries[0].Type; got != types.DeliveryX3 {
		t.Errorf("destination type = %q, want X3", got)
	}

	// A DID nobody provisioned fails the task. An ADMF may legitimately task an IRI-POI
	// whose MDF address comes from configuration, and that is served by declaring the
	// destination in the element's configuration or by naming no destination at all.
	// What is refused is an identifier the element genuinely cannot place: accepting it
	// would leave the triggering function believing content interception is running while
	// the element has nowhere to send the product, which is the state this whole interface
	// exists to make visible.
	tr2 := testTrigger()
	tr2.XID = "44444444-4444-4444-8444-444444444444"
	tr2.DIDs = []string{"55555555-5555-4555-8555-555555555555"}
	err := req.ActivateTask(tr2)
	if err == nil {
		t.Fatal("ActivateTask with an unprovisioned DID was acknowledged: the triggering function " +
			"is told the interception is running and the product has nowhere to go")
	}
	var refused *RequestError
	if !errors.As(err, &refused) || refused.Code != errCodeNoSuchDID {
		t.Errorf("ActivateTask failed with %v, want a refusal carrying %d (dId does not exist "+
			"on the NE)", err, errCodeNoSuchDID)
	}
	if _, held := st.Get(tr2.XID); held {
		t.Error("a task naming an unresolvable destination was stored")
	}
}

// TestXIDBytes checks the conversion every X2/X3 sender labels product through,
// including the zero case an MDF cannot attribute.
func TestXIDBytes(t *testing.T) {
	xid := types.XID("26328981-45f4-4191-8000-000000000000")
	if types.XID("").Bytes() != [16]byte{} || !types.XID("").IsZero() {
		t.Error("empty XID did not convert to the zero header field")
	}
	if !types.XID("not-a-uuid").IsZero() {
		t.Error("an unparseable XID did not report itself as zero")
	}
	if xid.IsZero() {
		t.Error("a valid XID reported itself as zero")
	}
	want := [16]byte{0x26, 0x32, 0x89, 0x81, 0x45, 0xf4, 0x41, 0x91, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if got := xid.Bytes(); got != want {
		t.Errorf("Bytes() = %x, want %x", got, want)
	}
}

// TestTriggerRejectsUnattributableTask covers the fields whose absence is what
// made this interface necessary in the first place. The requester refuses to send a
// trigger that would make the POI emit product no MDF can attribute, rather than
// leaving it to be noticed downstream — where nothing notices.
func TestTriggerRejectsUnattributableTask(t *testing.T) {
	req, _ := requesterTo(t, http.NotFoundHandler())

	tests := map[string]func(*Trigger){
		"no XID":           func(tr *Trigger) { tr.XID = "" },
		"no ProductID":     func(tr *Trigger) { tr.ProductID = "" },
		"zero correlation": func(tr *Trigger) { tr.CorrelationID = 0 },
		"no SEID":          func(tr *Trigger) { tr.SEID = 0 },
		"no destination":   func(tr *Trigger) { tr.DIDs = nil },
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			tr := testTrigger()
			breakIt(&tr)
			if err := req.ActivateTask(tr); err == nil {
				t.Error("ActivateTask accepted a trigger that cannot produce attributable product")
			}
		})
	}
}

// TestTriggerSurfacesErrorCode checks that an NE's refusal reaches the caller as
// a code it can act on — e.g. the identity-binding codes of clause 8.2.4 — and
// that a response with neither acknowledgement nor error is not read as success.
func TestTriggerSurfacesErrorCode(t *testing.T) {
	t.Run("error response", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
			//nolint:errcheck // test handler
			_, _ = w.Write([]byte(conformantResponse(t, body,
				`<errorInformation><errorCode>1030</errorCode><errorDescription>identity mismatch</errorDescription></errorInformation>`)))
		})
		req, _ := requesterTo(t, h)
		err := req.ActivateTask(testTrigger())
		var re *RequestError
		if !asRequestError(err, &re) {
			t.Fatalf("err = %v, want *RequestError", err)
		}
		if re.Code != 1030 {
			t.Errorf("Code = %d, want 1030", re.Code)
		}
	})

	t.Run("empty acknowledgement", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)                           //nolint:errcheck // test handler
			_, _ = w.Write([]byte(conformantResponse(t, body, ``))) //nolint:errcheck // test handler
		})
		req, _ := requesterTo(t, h)
		if err := req.ActivateTask(testTrigger()); err == nil {
			t.Error("a response carrying neither oK nor errorInformation was read as success")
		}
	})
}

// asRequestError is errors.As specialised to *RequestError, kept local so the
// test does not depend on the errors package's generic form for one assertion.
func asRequestError(err error, out **RequestError) bool {
	re, ok := err.(*RequestError)
	if ok {
		*out = re
	}
	return ok
}

// TestKeepaliveIsAcknowledged covers the message that makes a POI's fail-safe
// usable: without it, tasking either lapses whenever no new task arrives or never
// lapses at all.
func TestKeepaliveIsAcknowledged(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), HonoursCorrelationID())
	peer := certWithUID(t, "smf-1")
	req, body := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp, err := srv.Process(b, peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, _ := marshalResponse(resp) //nolint:errcheck // test handler
		_, _ = w.Write(out)             //nolint:errcheck // test handler
	}))

	if err := req.Keepalive(); err != nil {
		t.Fatalf("Keepalive: %v", err)
	}
	if !strings.Contains(*body, `xsi:type="ns1:KeepaliveRequest"`) {
		t.Errorf("wrong message type sent:\n%s", *body)
	}
}

// TestTaskXIDsReportsWhatThePOIHolds is what a restarted requester needs: it has
// no record of what it installed, the NE still holds all of it, and tasking nobody
// can withdraw must not exist.
func TestTaskXIDsReportsWhatThePOIHolds(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), RequireResolvableDIDs(), HonoursCorrelationID())
	peer := certWithUID(t, "smf-1")
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp, err := srv.Process(b, peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, _ := marshalResponse(resp) //nolint:errcheck // test handler
		_, _ = w.Write(out)             //nolint:errcheck // test handler
	}))

	// Nothing tasked yet.
	xids, err := req.TaskXIDs()
	if err != nil {
		t.Fatalf("TaskXIDs on an empty element: %v", err)
	}
	if len(xids) != 0 {
		t.Errorf("reported %v, want nothing", xids)
	}

	const did = "33333333-3333-4333-8333-333333333333"
	if createErr := req.CreateDestination(Destination{
		DID: did, DeliveryType: "X3Only", Address: "192.0.2.1", Port: 42069,
	}); createErr != nil {
		t.Fatalf("CreateDestination: %v", createErr)
	}

	tr := testTrigger()
	tr.DIDs = []string{did}
	if activateErr := req.ActivateTask(tr); activateErr != nil {
		t.Fatalf("ActivateTask: %v", activateErr)
	}

	xids, err = req.TaskXIDs()
	if err != nil {
		t.Fatalf("TaskXIDs: %v", err)
	}
	if len(xids) != 1 || xids[0] != tr.XID {
		t.Errorf("reported %v, want [%s]", xids, tr.XID)
	}

	// And it must be actionable: the XID reported has to be the one that withdraws
	// the task, or a reconciling requester cannot clean up after itself.
	if err := req.DeactivateTask(xids[0]); err != nil {
		t.Fatalf("DeactivateTask on a reported XID: %v", err)
	}
	if st.Len() != 0 {
		t.Errorf("store holds %d tasks after withdrawing the reported XID, want 0", st.Len())
	}
}

// TestTriggerAddressArmFollowsTheAddress pins the FSEID's address to the element
// its own family names, and to the form that element's type accepts.
//
// Two properties. A TS 103 280 address is a choice of an IPv4 and an IPv6 element,
// and the trigger and CreateDestination must both pick the arm from the address. And
// TS 103 280 types an IPv6Address as `([0-9a-f]{4}:){7}([0-9a-f]{4})`, so the "::"
// compression and dropped leading zeros that Go's net.IP.String produces are both
// refused. The expanded literals below are the assertion — writing "2001:db8::5" here
// would pin the refused form.
//
// Asserted on the rendered request rather than through the schema, so the case is
// pinned on a machine with no xmllint as well; TestOriginatedRequestsValidate
// carries the schema half.
func TestTriggerAddressArmFollowsTheAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		want    string
		notWant string
	}{
		{"IPv4", "10.0.1.5", "<ext:IPv4Address>10.0.1.5</ext:IPv4Address>", "IPv6Address"},
		{
			"IPv6 compressed is expanded", "2001:db8::5",
			"<ext:IPv6Address>2001:0db8:0000:0000:0000:0000:0000:0005</ext:IPv6Address>", "IPv4Address",
		},
		{
			"IPv6 already expanded is unchanged", "2001:0db8:0000:0000:0000:0000:0000:0005",
			"<ext:IPv6Address>2001:0db8:0000:0000:0000:0000:0000:0005</ext:IPv6Address>", "IPv4Address",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, body := requesterTo(t, acknowledging(t))
			tr := testTrigger()
			tr.SEIDAddress = tc.address

			if err := req.ActivateTask(tr); err != nil {
				t.Fatalf("ActivateTask: %v", err)
			}
			if !strings.Contains(*body, tc.want) {
				t.Errorf("trigger for %s does not carry %s\n%s", tc.address, tc.want, *body)
			}
			if strings.Contains(*body, tc.notWant) {
				t.Errorf("trigger for %s carries a %s element, which is the wrong arm of the choice\n%s",
					tc.address, tc.notWant, *body)
			}
		})
	}
}

// TestTriggerWithNoAddressCarriesNeitherArm keeps the optionality: the address is
// minOccurs="0" in the schema, and a trigger without one must emit no element at
// all rather than an empty one.
func TestTriggerWithNoAddressCarriesNeitherArm(t *testing.T) {
	req, body := requesterTo(t, acknowledging(t))
	tr := testTrigger()
	tr.SEIDAddress = ""

	if err := req.ActivateTask(tr); err != nil {
		t.Fatalf("ActivateTask: %v", err)
	}
	if strings.Contains(*body, "IPv4Address") || strings.Contains(*body, "IPv6Address") {
		t.Errorf("trigger with no SEIDAddress carries an address element\n%s", *body)
	}
}
