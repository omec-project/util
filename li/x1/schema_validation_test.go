// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// Every other test in this package renders a response and then reads it back with the
// same Go structs that produced it, which proves the renderer and the parser agree —
// something they do perfectly while both are wrong. A comment saying the element names come
// from the published schema is not a check.
//
// So this validates what we put on the wire against the schema ETSI publishes. It is the
// X1 counterpart of decoding xIRI against the published ASN.1 module, which is why the
// record side never drifted the way this side did.

const schemaDir = "testdata/schemas"

// zeroTailInstant is 2026-08-12T06:28:15.120000Z — chosen because its sub-second value ends
// in zeros, which is exactly the case a stripping formatter renders too short. The schema
// demands six fractional digits whatever the value.
var zeroTailInstant = time.Date(2026, 8, 12, 6, 28, 15, 120000*1000, time.UTC)

// testDID is a UUID, which is what the schema types a DId as.
const testDID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

// vendoredSchemaDigests pins the published files. A local edit — including a well-meant
// one adding the schemaLocation that validate.xsd supplies instead — silently changes what
// every assertion here means, so it fails the test rather than passing quietly.
var vendoredSchemaDigests = map[string]string{
	"TS_103_221_01.xsd":          "71f7993475293c7c44b403bd3ae627075e4484290ed53c52702b20063ceebdda",
	"TS_103_280.xsd":             "d2f977aeca79f8b49ade1b6a4aaecd2e8d807bee51569c2817088cae99a9e444",
	"TS_103_221_01_HashedID.xsd": "3279bf6317dc50a5eec75c8b720fe95dcb98270f3eab46808592a7cb5b1edb4b",
	// The 3GPP LI_T3 extension, from the TS 33.128 v18.16.0 attachments rather than the
	// ETSI forge. Without it the detection criteria a CC-TF sends cannot be validated:
	// TS 103 221-1's Extension type uses a strict wildcard, so an unknown extension
	// element is an error rather than something skipped.
	"urn_3GPP_ns_li_3GPPX1Extensions.xsd": "08eb7cb113a4ec8904344b9d59fdc13ea7fd1b46f36ca2ce0fc68de0a4f59c0d",
}

const requireValidationEnv = "LI_REQUIRE_SCHEMA_VALIDATION"

// requireXmllint reports whether a validator is available, loudly.
//
// A developer without libxml2 should not be blocked, so this skips by default — but the
// message says exactly what is not being checked. Where validation is guaranteed it fails
// instead, because a check that skips is not a check: a run that reports success because
// the validator was missing is indistinguishable from one that validated.
//
// The guarantee is an explicit opt-in rather than a test for CI, and that distinction is
// load-bearing. The shared `unit-test` reusable workflow runs on a runner image whose
// declared package list does not include libxml2-utils, so keying off CI would either fail
// that job or quietly depend on a transitive package, which can disappear and leave the
// validation skipping unnoticed. The `li-conformance` job in .github/workflows/main.yml installs the validator and sets this
// variable, so exactly one job promises to validate and fails if it cannot.
func requireXmllint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xmllint"); err == nil {
		return
	}
	const msg = "xmllint (libxml2) not installed: X1 output is NOT being validated against " +
		"the published schema by this run. Install libxml2-utils."
	if os.Getenv(requireValidationEnv) != "" {
		t.Fatalf("%s %s is set, so this is a failure rather than a skip — the job that sets "+
			"it exists to guarantee validation runs.", msg, requireValidationEnv)
	}
	t.Skipf("%s The conformance CI job sets %s and fails if the validator is absent.", msg, requireValidationEnv)
}

func checkVendoredSchemas(t *testing.T) {
	t.Helper()
	for name, want := range vendoredSchemaDigests {
		b, err := os.ReadFile(filepath.Join(schemaDir, name))
		if err != nil {
			t.Fatalf("vendored schema %s: %v", name, err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("vendored schema %s has been edited (sha256 %s, want %s) — the published "+
				"files must stay byte-identical to ETSI's; imports are resolved by validate.xsd",
				name, got, want)
		}
	}
}

// knownSchemaDefects is the set of schema violations this element is currently known to
// produce, keyed by the case that produces them and matched as substrings.
//
// It exists so these tests can be green without hiding anything. A permanently failing
// test gets ignored and then deleted; a test that silently tolerates whatever it finds
// stops being a check. This does neither:
//
//   - a violation not listed here fails the test, so no new defect can arrive unnoticed;
//   - a listed violation that no longer occurs *also* fails the test, so the list cannot
//     rot into a description of a past that has been fixed.
//
// Every entry names the repair it is waiting for. Emptying this map is the definition
// of X1 being schema-conformant, and its length is the honest measure of how far off that
// is.
//
// It is currently **empty**. An empty map is not a reason to delete it: a violation that is not listed here fails
// the test, which is what stops the next one arriving unnoticed.
var knownSchemaDefects = map[string][]string{}

// classify sorts the validator's complaints into those already known for this case and
// those that are new.
func classify(caseName string, problems []string) (known, unexpected []string, missing []string) {
	want := knownSchemaDefects[caseName]
	seen := make([]bool, len(want))

	for _, p := range problems {
		matched := false
		for i, w := range want {
			if strings.Contains(p, w) {
				seen[i], matched = true, true

				break
			}
		}
		if matched {
			known = append(known, p)
		} else {
			unexpected = append(unexpected, p)
		}
	}
	for i, ok := range seen {
		if !ok {
			missing = append(missing, want[i])
		}
	}

	return known, unexpected, missing
}

// report applies the baseline to one case's findings.
func report(t *testing.T, caseName string, problems []string) {
	t.Helper()
	known, unexpected, missing := classify(caseName, problems)

	for _, p := range unexpected {
		t.Errorf("NEW schema violation: %s", p)
	}
	for _, w := range missing {
		t.Errorf("known defect %q no longer occurs — remove it from knownSchemaDefects "+
			"rather than leaving the list describing a fixed past", w)
	}
	if len(known) > 0 {
		t.Logf("%d known violation(s), listed in knownSchemaDefects", len(known))
	}
}

// validateAgainstSchema runs the rendered message through the validator and returns the
// schema's own complaints, one per line, empty when it validates.
func validateAgainstSchema(t *testing.T, doc []byte) []string {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "x1-*.xml")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	if _, werr := f.Write(doc); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	_ = f.Close()

	// --noout so only diagnostics come back. CommandContext rather than Command so a
	// wedged validator fails the test instead of hanging it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xmllint", "--noout",
		"--schema", filepath.Join(schemaDir, "validate.xsd"), f.Name()).CombinedOutput()
	if err == nil {
		return nil
	}

	var problems []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		// Keep diagnostics only. xmllint echoes the offending source line and a caret
		// beneath it, which are not findings and would make one mistake look like many.
		if line == "" || strings.HasSuffix(line, "fails to validate") ||
			!strings.Contains(line, "error :") {
			continue
		}
		if i := strings.Index(line, "Schemas validity error : "); i >= 0 {
			line = line[i+len("Schemas validity error : "):]
		}
		problems = append(problems, line)
	}
	if len(problems) == 0 {
		problems = []string{fmt.Sprintf("xmllint failed with no parsable diagnostic: %v", err)}
	}

	return problems
}

// request builds an X1 request of the given type, carrying whatever body the type needs.
func request(msgType, body string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1RequestMessage xsi:type="ns1:` + msgType + `">
    <ns1:admfIdentifier>admfID</ns1:admfIdentifier>
    <ns1:neIdentifier>neID</ns1:neIdentifier>
    <ns1:messageTimestamp>2026-01-01T00:00:00.000000Z</ns1:messageTimestamp>
    <ns1:version>v1.6.1</ns1:version>
    <ns1:x1TransactionId>aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa</ns1:x1TransactionId>` + body + `
  </ns1:x1RequestMessage>
</ns1:X1Request>`)
}

// TestRenderedResponsesValidate drives a real request through Process for every response
// type this element produces, and validates what comes back.
//
// Reported per case rather than failing on the first, so one run gives the whole picture:
// the repair list should come from the validator, not from someone reading the schema
// once.
func TestRenderedResponsesValidate(t *testing.T) {
	requireXmllint(t)
	checkVendoredSchemas(t)

	supi := types.TargetIdentifier{Type: types.TargetSUPI, Value: "262019876543210"}

	// Tasks live in the store, destinations on the server, so a case's setup gets both.
	held := func(st *store.Store, _ *Server) {
		st.Activate(types.InterceptTask{
			XID: testXID, Targets: []types.TargetIdentifier{supi},
			Products: []types.ProductType{types.ProductIRI},
		})
	}
	// A task that names destinations, so the reported listOfDIDs is not empty in every
	// case: an element that rendered the element but never its contents would otherwise
	// validate throughout.
	heldWithDIDs := func(st *store.Store, srv *Server) {
		held(st, srv)
		task, _ := st.Get(testXID)
		task.DIDs = []string{testDID}
		st.Activate(task)
	}
	withDestination := func(_ *store.Store, srv *Server) {
		srv.destinations[testDID] = heldDestination{
			DeliveryType: deliveryX2Only, Address: "10.0.60.122:42069",
		}
	}
	// A destination the element's configuration declares, and one serving both
	// interfaces. Both are new rendering paths — the first carries provenance in its
	// friendlyName, the second reports the combined deliveryType — and the schema has an
	// opinion about each.
	withConfiguredDestination := func(_ *store.Store, srv *Server) {
		srv.configured["e0000000-0000-4000-8000-00000000000e"] = heldDestination{
			Address: "10.0.60.200:42069", DeliveryType: deliveryX2Only, Configured: true,
		}
	}
	withShadowedDestination := func(st *store.Store, srv *Server) {
		withConfiguredDestination(st, srv)
		srv.destinations["e0000000-0000-4000-8000-00000000000e"] = heldDestination{
			Address: "10.0.60.122:42069", DeliveryType: deliveryX2andX3,
			FriendlyName: "agency A",
		}
	}
	refuseRemoveAll := func(_ *store.Store, srv *Server) { srv.removeAllDestinationsDisabled = true }
	withFaultProbe := func(_ *store.Store, srv *Server) {
		srv.faultProbes = append(srv.faultProbes, func() *X1Error {
			return &X1Error{ErrorCode: 1000, ErrorDescription: "the mediation function is unreachable"}
		})
	}
	bothHeld := func(st *store.Store, srv *Server) {
		held(st, srv)
		withDestination(st, srv)
	}
	// A destination the delivery layer cannot currently reach, which is a *different*
	// rendering path from the one above: destinationDeliveryStatus carries deliveryFault and
	// listOfFaults is populated rather than empty. The element only emits this shape when a
	// destination is actually down, so it is the shape least likely to be exercised and the
	// worst to have wrong — an invalid answer at exactly the moment the ADMF is asking why
	// product has stopped.
	withUnreachableDestination := func(st *store.Store, srv *Server) {
		withDestination(st, srv)
		srv.destinationReachable = func(string) bool { return true }
	}

	cases := []struct {
		name  string
		setup func(*store.Store, *Server)
		req   []byte
		// peer presents the certificate the request arrives with. It defaults to a properly
		// bound ADMF, so the two cases that set it are the ones about a refused peer — the
		// path every other case authenticates past, and the one whose input this element has
		// not decided to trust yet.
		peer func(*testing.T) *x509.Certificate
	}{
		{name: "ActivateTaskResponse", req: []byte(activateXML)},
		{
			name: "DeactivateTaskResponse", setup: held,
			req: request("DeactivateTaskRequest", "\n    <ns1:xId>"+string(testXID)+"</ns1:xId>"),
		},
		{
			name: "GetTaskDetailsResponse", setup: held,
			req: request("GetTaskDetailsRequest", "\n    <ns1:xId>"+string(testXID)+"</ns1:xId>"),
		},
		{name: "GetAllDetailsResponse, with a task held", setup: held, req: request("GetAllDetailsRequest", "")},
		{name: "GetAllDetailsResponse, holding nothing", req: request("GetAllDetailsRequest", "")},
		{name: "KeepaliveResponse", req: request("KeepaliveRequest", "")},
		{name: "PingResponse", req: request("PingRequest", "")},
		{name: "CreateDestinationResponse", req: request("CreateDestinationRequest", `
    <ns1:destinationDetails>
      <ns1:dId>bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb</ns1:dId>
      <ns1:deliveryType>X2Only</ns1:deliveryType>
      <ns1:deliveryAddress>
        <ns1:ipAddressAndPort>
          <c:address xmlns:c="http://uri.etsi.org/03280/common/2017/07">
            <c:IPv4Address>10.0.0.1</c:IPv4Address>
          </c:address>
          <c:port xmlns:c="http://uri.etsi.org/03280/common/2017/07">42069</c:port>
        </ns1:ipAddressAndPort>
      </ns1:deliveryAddress>
    </ns1:destinationDetails>`)},
		// The refusal path: every error this element sends takes it. Deactivating an XID the
		// element does not hold is an error — TS 103 221-1 table 6.2.3-2: "it is an error if
		// the XID is not already present at the NE".
		{
			name: "ErrorResponse (no such task)",
			req:  request("DeactivateTaskRequest", "\n    <ns1:xId>cccccccc-cccc-4ccc-8ccc-cccccccccccc</ns1:xId>"),
		},
		{name: "DeactivateAllTasksResponse", setup: held, req: request("DeactivateAllTasksRequest", "")},
		{
			// The ErrorResponse path for a bulk operation that is present but switched off — a
			// different case from an unsupported request type.
			name:  "ErrorResponse (bulk operation not enabled)",
			setup: refuseRemoveAll,
			req:   request("RemoveAllDestinationsRequest", ""),
		},
		{name: "RemoveAllDestinationsResponse", req: request("RemoveAllDestinationsRequest", "")},
		{name: "ErrorResponse (Generic Object CRUD refused)", req: request("CreateObjectRequest", "")},
		{
			// A request type outside the schema's enumeration entirely, which is the path
			// that has to fall back to ExtendedRequestMessageType — echoing the peer's own
			// string would invalidate the very message that refuses it.
			name: "ErrorResponse (request type the schema does not define)",
			req:  request("SomethingElseRequest", ""),
		},
		{
			// Answered, not refused: clause 6.4.1 makes it mandatory. What makes the answer
			// honest is that the list is absent, and what makes it *valid* is that there is
			// no oK either — this type defines neither as present.
			name: "GetAllGenericObjectDetailsResponse, Generic Objects unsupported",
			req:  request("GetAllGenericObjectDetailsRequest", ""),
		},

		// The interrogation set, each in both states: holding something, and holding
		// nothing. The empty case is the one a restarted element is in, and the moment an
		// ADMF most needs a usable answer rather than an error.
		{name: "GetAllTaskDetailsResponse, with a task held", setup: heldWithDIDs, req: request("GetAllTaskDetailsRequest", "")},
		{name: "GetAllTaskDetailsResponse, holding nothing", req: request("GetAllTaskDetailsRequest", "")},
		{name: "GetAllDestinationDetailsResponse, with a destination", setup: withDestination, req: request("GetAllDestinationDetailsRequest", "")},
		{name: "GetAllDestinationDetailsResponse, a destination in delivery fault", setup: withUnreachableDestination, req: request("GetAllDestinationDetailsRequest", "")},
		{name: "GetDestinationDetailsResponse, a destination in delivery fault", setup: withUnreachableDestination, req: request("GetDestinationDetailsRequest", "\n    <ns1:dId>"+testDID+"</ns1:dId>")},
		{name: "GetAllDetailsResponse, a destination in delivery fault", setup: withUnreachableDestination, req: request("GetAllDetailsRequest", "")},
		{name: "GetAllDestinationDetailsResponse, holding nothing", req: request("GetAllDestinationDetailsRequest", "")},
		{
			name:  "GetAllDestinationDetailsResponse, a destination declared in configuration",
			setup: withConfiguredDestination, req: request("GetAllDestinationDetailsRequest", ""),
		},
		{
			name:  "GetAllDestinationDetailsResponse, a provisioned entry superseding a configured one",
			setup: withShadowedDestination, req: request("GetAllDestinationDetailsRequest", ""),
		},
		{name: "ListAllDetailsResponse, with a task and a destination", setup: bothHeld, req: request("ListAllDetailsRequest", "")},
		{name: "ListAllDetailsResponse, holding nothing", req: request("ListAllDetailsRequest", "")},
		{name: "GetNEStatusResponse, healthy", req: request("GetNEStatusRequest", "")},
		{
			// A Faults answer has a different shape from an OK one — listOfFaults carries
			// unresolvedFault children rather than being empty — so both need validating.
			name: "GetNEStatusResponse, with a condition holding", setup: withFaultProbe,
			req: request("GetNEStatusRequest", ""),
		},
		{
			name: "GetAllDetailsResponse, with a condition holding", setup: withFaultProbe,
			req: request("GetAllDetailsRequest", ""),
		},
		{
			name: "GetDestinationDetailsResponse", setup: withDestination,
			req: request("GetDestinationDetailsRequest",
				"\n    <ns1:dId>"+testDID+"</ns1:dId>"),
		},
		{
			name: "ErrorResponse (no such destination)",
			req: request("GetDestinationDetailsRequest",
				"\n    <ns1:dId>dddddddd-dddd-4ddd-8ddd-dddddddddddd</ns1:dId>"),
		},

		// The refusal path this element sends to a peer it will not talk to. It is the one
		// case where the request's contents have been trusted by nothing — the request type
		// has to reach the renderer from a message refused before anything else about it was
		// accepted.
		{
			name: "ErrorResponse (peer authentication failed)",
			req:  request("ActivateTaskRequest", ""),
			peer: func(t *testing.T) *x509.Certificate { return certWithUID(t, "impostor") },
		},
		{
			// The same refusal, to a peer that also supplied header values the schema
			// restricts. Both travel back in our answer, so both have to be values we are
			// willing to put our name to — see knownSchemaDefects, where this is baselined
			// rather than fixed.
			name: "ErrorResponse (peer authentication failed, malformed headers)",
			req: []byte(strings.NewReplacer(
				"v1.6.1", "not-a-version",
				"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "not-a-uuid",
			).Replace(string(request("ActivateTaskRequest", "")))),
			peer: func(t *testing.T) *x509.Certificate { return certWithUID(t, "impostor") },
		},
	}

	var failed int
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := store.New()
			srv := testServer(st)
			if c.setup != nil {
				c.setup(st, srv)
			}
			// A fixed instant whose microseconds end in four zeros. Go's
			// trailing-zero-stripping formats render this as ".12", so any regression to
			// one fails *every* case here rather than one run in ten.
			srv.now = func() time.Time { return zeroTailInstant }

			peer := admfPeer(t)
			if c.peer != nil {
				peer = c.peer(t)
			}
			resp, err := srv.Process(c.req, peer)
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			out, err := marshalResponse(resp)
			if err != nil {
				t.Fatalf("marshalResponse: %v", err)
			}

			problems := validateAgainstSchema(t, out)
			if len(problems) > 0 {
				failed++
			}
			report(t, c.name, problems)
		})
	}
	if failed > 0 {
		t.Logf("%d of %d response types do not validate against the published schema",
			failed, len(cases))
	}
}

// TestOriginatedRequestsValidate covers the other half of what this element puts on the
// wire: a *request* it originates — the LI_T3 trigger a CC-TF sends a UPF, and the fault
// reports by which an element tells an ADMF something is wrong — is checked only by whatever
// receives it. Two of the five timestamp sites are on this path.
//
// The bodies are captured from the real send paths rather than rendered from the templates
// directly, so what is validated is what a peer receives.
func TestOriginatedRequestsValidate(t *testing.T) {
	requireXmllint(t)
	checkVendoredSchemas(t)

	// One element across the whole exchange, not one per request. The sequence below is a
	// sequence — provision a destination, task against it, then withdraw *that* task — and a
	// fresh store per request would make the last step a deactivation of something that never
	// existed, which is refused with 2020.
	srv := NewServer(store.New(), "upf-1", WithADMF("smf-1"), HonoursCorrelationID())
	srv.now = func() time.Time { return zeroTailInstant }

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		resp, err := srv.Process(body, certWithUID(t, "smf-1"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		out, _ := marshalResponse(resp) //nolint:errcheck // test handler
		_, _ = w.Write(out)             //nolint:errcheck // test handler
	})

	t.Run("trigger: CreateDestination then ActivateTask then DeactivateTask", func(t *testing.T) {
		req, body := requesterTo(t, ok)
		req.now = func() time.Time { return zeroTailInstant }
		tr := testTrigger()

		for _, step := range []struct {
			name string
			send func() error
		}{
			{"CreateDestination", func() error {
				return req.CreateDestination(Destination{
					DID: tr.DIDs[0], DeliveryType: deliveryX3Only,
					Address: "10.0.60.122", Port: 42069,
				})
			}},
			{"ActivateTask", func() error { return req.ActivateTask(tr) }},
			{"DeactivateTask", func() error { return req.DeactivateTask(tr.XID) }},
		} {
			if err := step.send(); err != nil {
				t.Fatalf("%s: %v", step.name, err)
			}
			report(t, step.name, validateAgainstSchema(t, []byte(*body)))
		}
	})

	// The IPv6 arm of the same two addresses. Rare in practice — few deployments carry IPv6
	// PDU sessions — which is exactly why it needs a validator rather than a reviewer: an
	// IPv6 literal rendered into the IPv4Address element would pass everything else.
	t.Run("trigger and destination with IPv6 addresses", func(t *testing.T) {
		req, body := requesterTo(t, ok)
		req.now = func() time.Time { return zeroTailInstant }
		tr := testTrigger()
		tr.XID = "44444444-4444-4444-8444-444444444444"
		tr.DIDs = []string{"55555555-5555-4555-8555-555555555555"}
		tr.SEIDAddress = "2001:db8::5"

		if err := req.CreateDestination(Destination{
			DID: tr.DIDs[0], DeliveryType: deliveryX3Only,
			Address: "2001:db8::7b", Port: 42069,
		}); err != nil {
			t.Fatalf("CreateDestination: %v", err)
		}
		report(t, "CreateDestination/IPv6", validateAgainstSchema(t, []byte(*body)))

		if err := req.ActivateTask(tr); err != nil {
			t.Fatalf("ActivateTask: %v", err)
		}
		report(t, "ActivateTask/IPv6", validateAgainstSchema(t, []byte(*body)))
	})

	t.Run("fault reports to the ADMF", func(t *testing.T) {
		var body string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
			body = string(b)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(admfResponse(t, b))) //nolint:errcheck // test handler
		}))
		t.Cleanup(srv.Close)

		rep := NewReporter(srv.URL, "admfID", "neID", nil)
		rep.now = func() time.Time { return zeroTailInstant }

		// The message that says something is wrong must not itself be the one a
		// validating ADMF throws away.
		rep.Notify(NEIssueMDFUnreachable, "MDF3 X3 delivery failed")
		report(t, "ReportNEIssue", validateAgainstSchema(t, []byte(body)))

		// TaskReportTerminatingFault, not an NE-issue identifier: the two vocabularies are
		// different and the schema enumerates this one. "taskingAbsent", for instance, is
		// real, but it travels as a ReportNEIssue alert, not as a taskReportType.
		if err := rep.ReportTaskIssue(string(testXID), TaskReportTerminatingFault,
			"the triggered POI refused its trigger"); err != nil {
			t.Fatalf("ReportTaskIssue: %v", err)
		}
		report(t, "ReportTaskIssue", validateAgainstSchema(t, []byte(body)))

		// The destination-scoped report, and its clearing counterpart. Both are new
		// message shapes on a channel whose whole value is that a validating ADMF
		// accepts them, and AllClear is an enumeration value this element had declared
		// for a long time and never emitted — so nothing had ever checked that emitting
		// it produces a valid message.
		if err := rep.ReportDestinationIssue(didAgencyA, TaskReportNonTerminatingFault,
			"mdfUnreachable: delivery destination is unreachable"); err != nil {
			t.Fatalf("ReportDestinationIssue: %v", err)
		}
		report(t, "ReportDestinationIssue", validateAgainstSchema(t, []byte(body)))

		if err := rep.ReportDestinationIssue(didAgencyA, TaskReportAllClear,
			"mdfUnreachable: resolved"); err != nil {
			t.Fatalf("ReportDestinationIssue(AllClear): %v", err)
		}
		report(t, "ReportDestinationIssue/AllClear", validateAgainstSchema(t, []byte(body)))
	})
}

// TestX1TimestampAlwaysSixDigits pins the boundary values directly, independently of any
// message. The schema demands six fractional digits whatever the instant, and the three
// cases below are the ones a stripping formatter renders differently.
func TestX1TimestampAlwaysSixDigits(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{
			name: "no fractional part",
			when: time.Date(2026, 8, 12, 6, 28, 15, 0, time.UTC),
			want: "2026-08-12T06:28:15.000000Z",
		},
		{
			name: "one trailing zero",
			when: time.Date(2026, 8, 12, 6, 28, 15, 322190*1000, time.UTC),
			want: "2026-08-12T06:28:15.322190Z",
		},
		{
			name: "six significant digits",
			when: time.Date(2026, 8, 12, 6, 28, 15, 322191*1000, time.UTC),
			want: "2026-08-12T06:28:15.322191Z",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := x1Timestamp(c.when); got != c.want {
				t.Errorf("x1Timestamp = %q, want %q", got, c.want)
			}
		})
	}
}
