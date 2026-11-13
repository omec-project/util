// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// This file measures what a peer actually returns before anything is made to
// depend on it.
//
// A check that becomes a refusal has to be grounded in what is observed on the wire, not in
// what the code is expected to send: a required check against a field the peer never sends
// is a permanent refusal by construction, and on the withdrawal path a permanent refusal
// holds open both the withdrawal and the fail-safe that would have completed it.

// echoedEnvelope drives one real request through a real Server and returns the
// response envelope the requester received.
func echoedEnvelope(t *testing.T, send func(*Requester) error) X1ResponseMessage {
	t.Helper()

	st := store.New()
	// The trigger's destination is declared, because a task naming one this element
	// cannot resolve is refused — and what this test is about is the response envelope,
	// not destination resolution.
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), HonoursCorrelationID(),
		WithConfiguredDestinations(ConfiguredDestination{
			DID:          testTrigger().DIDs[0],
			DeliveryType: deliveryX3Only,
			Address:      "10.0.2.5:9000",
		}))
	peer := certWithUID(t, "smf-1")

	var raw string
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
		raw = string(out)
		_, _ = w.Write(out) //nolint:errcheck // test handler
	}))

	if err := send(req); err != nil {
		t.Fatalf("request: %v", err)
	}

	var out X1Response
	if err := xml.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decoding the response this element returned: %v\n%s", err, raw)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("response carried %d messages, want 1\n%s", len(out.Messages), raw)
	}

	return out.Messages[0]
}

// TestEveryResponseThisElementReturnsCarriesTheFullEnvelope measures the envelope of every
// answer this package's Server returns.
//
// In SD-Core the peer answering a Requester is this package's own Server: the SMF's CC
// triggering function points a Requester at a UPF's LI_T3 endpoint, and the UPF runs a
// Server. The reference simulator used for interoperability is an ADMF and MDF — an X1
// client toward these network functions, never the responder to a request they send. So
// the fields below can be required rather than checked only when present. The responder's
// URL is deployment configuration, so a third-party CC-POI is possible in principle; none
// is exercised here.
func TestEveryResponseThisElementReturnsCarriesTheFullEnvelope(t *testing.T) {
	cases := []struct {
		request  string
		response string
		// acknowledges records whether this response type carries an `oK` at all.
		// It is not a detail: `oK` is *not* a member of the schema's
		// X1ResponseMessage base type, so it belongs to particular response types
		// rather than to responses in general — GetAllDetailsResponse carries
		// neStatusDetails and three lists and no acknowledgement. A validator that
		// required one would refuse every details answer, permanently, which is the
		// shape of failure this section exists to find before it is built.
		acknowledges bool
		send         func(*Requester) error
	}{
		{"CreateDestinationRequest", "CreateDestinationResponse", true, func(r *Requester) error {
			return r.CreateDestination(Destination{
				DID:          "33333333-3333-4333-8333-333333333333",
				DeliveryType: deliveryX3Only,
				Address:      "10.0.2.5",
				Port:         9000,
			})
		}},
		{"ActivateTaskRequest", "ActivateTaskResponse", true, func(r *Requester) error {
			return r.ActivateTask(testTrigger())
		}},
		{"DeactivateTaskRequest", "DeactivateTaskResponse", true, func(r *Requester) error {
			if err := r.ActivateTask(testTrigger()); err != nil {
				return err
			}

			return r.DeactivateTask(testTrigger().XID)
		}},
		{"KeepaliveRequest", "KeepaliveResponse", true, func(r *Requester) error {
			return r.Keepalive()
		}},
		{"GetAllDetailsRequest", "GetAllDetailsResponse", false, func(r *Requester) error {
			_, err := r.ReportedTasks()

			return err
		}},
	}

	for _, c := range cases {
		t.Run(c.request, func(t *testing.T) {
			m := echoedEnvelope(t, c.send)

			// The five fields below are the schema's X1ResponseMessage base type, so
			// every response type extends them and every one of them is mandatory. That
			// is what makes each a required check rather than an "if present, must
			// match" one.
			if got := localType(m.Type); got != c.response {
				t.Errorf("response type = %q, want %q", got, c.response)
			}
			if m.AdmfIdentifier != "smf-1" {
				t.Errorf("admfIdentifier = %q, want the requester's own identifier echoed back", m.AdmfIdentifier)
			}
			if m.NeIdentifier != "upf-1" {
				t.Errorf("neIdentifier = %q, want the element that was addressed", m.NeIdentifier)
			}
			if m.Version != supportedVersion {
				t.Errorf("version = %q, want %q", m.Version, supportedVersion)
			}
			if !types.ValidUUID(m.X1TransactionID) {
				t.Errorf("x1TransactionId = %q, which is not the TS 103 280 UUID the schema restricts it to", m.X1TransactionID)
			}
			if m.MessageTimestamp == "" {
				t.Error("messageTimestamp is absent, and the schema's base type makes it mandatory")
			}

			if got := m.OK != ""; got != c.acknowledges {
				t.Errorf("carries an acknowledgement = %v, want %v — this is the field a validator must not require of every response", got, c.acknowledges)
			}
		})
	}
}

// TestTheTransactionIdentifierIsEchoedExactly is the other half of the same
// measurement, and it cannot be done by comparing two fields of one response: the
// value has to be the one the *request* carried.
func TestTheTransactionIdentifierIsEchoedExactly(t *testing.T) {
	st := store.New()
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), HonoursCorrelationID())
	peer := certWithUID(t, "smf-1")

	var sent, echoed string
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		var in X1Request
		if err := xml.Unmarshal(body, &in); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		if len(in.Messages) == 1 {
			sent = in.Messages[0].X1TransactionID
		}
		resp, err := srv.Process(body, peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		out, _ := marshalResponse(resp) //nolint:errcheck // test handler
		var decoded X1Response
		if err := xml.Unmarshal(out, &decoded); err == nil && len(decoded.Messages) == 1 {
			echoed = decoded.Messages[0].X1TransactionID
		}
		_, _ = w.Write(out) //nolint:errcheck // test handler
	}))

	if err := req.Keepalive(); err != nil {
		t.Fatalf("Keepalive: %v", err)
	}
	if sent == "" {
		t.Fatal("the request carried no x1TransactionId, so this measures nothing")
	}
	if echoed != sent {
		t.Errorf("x1TransactionId echoed as %q, sent as %q — an equality check on the echo would be a permanent refusal", echoed, sent)
	}
}

// TestTheRequesterAlwaysGeneratesAConformantTransactionIdentifier pins the
// property that makes an equality check on the echo sound, rather than leaving it
// a coincidence two files apart.
//
// A conformant server that receives a *non*-conformant x1TransactionId is required
// to answer with a different one — echoTransactionID substitutes a fresh UUID,
// because echoing the malformed value would make the whole response
// schema-invalid. So a strict equality check is correct only for a requester that
// always sends a value inside the TS 103 280 format. If Requester.header ever
// stops generating one, this fails here rather than as a permanent refusal in a
// deployment.
func TestTheRequesterAlwaysGeneratesAConformantTransactionIdentifier(t *testing.T) {
	r := NewRequester("https://upf-1:8443/X1/NE", "smf-1", "upf-1", nil)

	for range 64 {
		h := r.header("KeepaliveRequest")
		if !types.ValidUUID(h.TxID) {
			t.Fatalf("header generated %q, which the schema does not admit — a conformant peer would be obliged to substitute a different value, and an equality check would then refuse a correct response", h.TxID)
		}
		if echoTransactionID(h.TxID) != h.TxID {
			t.Fatalf("a conformant server would substitute for %q rather than echo it", h.TxID)
		}
	}
}

// TestASingleRequestIsAnsweredWithASingleMessage: whether either peer
// ever returns more than one message to one request. Measured, because the binding refuses
// a multi-message response and that refusal would be wrong if the responder
// legitimately batched.
//
// TS 103 221-1 clause 6.1 settles the reading and the measurement agrees with it:
// a ResponseContainer holds "all the responses to the requests in the container",
// so a container carrying one request is answered by a container carrying one
// response. The schema permits `maxOccurs="unbounded"`, which is what makes this
// worth asserting rather than assuming.
func TestASingleRequestIsAnsweredWithASingleMessage(t *testing.T) {
	st := store.New()
	// The trigger's destination is declared: a task naming one this element cannot
	// resolve is refused, and what this test is about is the single-message rule.
	srv := NewServer(st, "upf-1", WithADMF("smf-1"), HonoursCorrelationID(),
		WithConfiguredDestinations(ConfiguredDestination{
			DID:          testTrigger().DIDs[0],
			DeliveryType: deliveryX3Only,
			Address:      "10.0.2.5:9000",
		}))
	peer := certWithUID(t, "smf-1")

	var counted int
	req, _ := requesterTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp, err := srv.Process(body, peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		counted = len(resp.Messages)
		out, _ := marshalResponse(resp) //nolint:errcheck // test handler
		_, _ = w.Write(out)             //nolint:errcheck // test handler
	}))

	if err := req.ActivateTask(testTrigger()); err != nil {
		t.Fatalf("ActivateTask: %v", err)
	}
	if counted != 1 {
		t.Errorf("one request was answered with %d messages; the single-message rule is reopened", counted)
	}
}

// TestNoDeployedADMFSendsAShapeThisElementRefuses is
// answered from the test material rather than from a live ADMF, which is stated
// so the limit of the evidence is visible.
//
// The two shapes in question are a TargetIdentifier populating more than one arm,
// which is refused, and a UPFLIT3TargetIdentifier list of more than one, every member
// of which becomes a criterion. This walks every X1 request fixture in the package —
// including the ones transcribed from the reference simulator's examples, the only
// third-party ADMF these packages have exchanged X1 with — and reports what they
// carry.
func TestNoDeployedADMFSendsAShapeThisElementRefuses(t *testing.T) {
	fixtures := map[string]string{
		"activate (reference simulator)": activateXML,
		"getAllDetails":                  getAllDetailsXML,
	}

	for name, body := range fixtures {
		var in X1Request
		if err := xml.Unmarshal([]byte(body), &in); err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}
		for _, m := range in.Messages {
			if m.TaskDetails == nil {
				continue
			}
			for _, id := range m.TaskDetails.TargetIdentifiers {
				if n := populatedArms(id); n > 1 {
					t.Errorf("%s: a target identifier populates %d arms, so refusing multi-arm choices would break a shape this project's own test material sends", name, n)
				}
				if id.Extension != nil && id.Extension.UPFT3 != nil {
					if n := len(id.Extension.UPFT3.Identifiers); n > 1 {
						t.Logf("%s: a UPFLIT3TargetIdentifier list of %d — the list handling is exercised by this fixture", name, n)
					}
				}
			}
		}
	}
}

// TestTargetIdentifierFixturesAreSingleArm restates the same measurement over the
// identifier types the round-trip test covers, since those are generated by this
// element and so say what it emits rather than what it accepts.
func TestTargetIdentifierFixturesAreSingleArm(t *testing.T) {
	for _, id := range []types.TargetIdentifier{
		{Type: types.TargetSUPI, Value: "262019876543210"},
		{Type: types.TargetUEIPv4, Value: "10.250.0.9"},
		{Type: types.TargetFSEID, Value: "14426627323429955319"},
	} {
		rendered := targetXML(id)
		if strings.Count(rendered, "<ns1:targetIdentifier>") > 1 {
			t.Errorf("%v rendered more than one identifier", id)
		}
	}
}
