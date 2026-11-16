// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
)

// Generic Objects divide in two, and TS 103 221-1 divides them, so this element does too.
//
// GetAllGenericObjectDetails sits in clause 6.4.1's unqualified "The following requests and
// responses shall be supported", alongside the other seven interrogation messages, and its
// response has an answer for an element in our position: the object list "May be omitted if
// Generic Objects are not supported by the NE". The six object CRUD messages of clause 6.8 are
// qualified — DeleteAllObjects "shall be supported *if* the implementation supports Generic
// Objects", and an element that cannot store an object "shall reject the CreateObjectRequest
// with an appropriate error response".
//
// So the query is answered and the CRUD is refused. The two tests below pin each half, because
// each has a plausible wrong answer: refusing the query fails a mandatory message, and
// answering the CRUD claims a capability this element does not have.

// TestGenericObjectQueryIsAnsweredWithoutClaimingSupport covers the half that is easy to get
// backwards — answering must not amount to claiming support.
func TestGenericObjectQueryIsAnsweredWithoutClaimingSupport(t *testing.T) {
	const req = "GetAllGenericObjectDetailsRequest"

	srv := testServer(store.New())
	srv.now = func() time.Time { return zeroTailInstant }

	resp, err := srv.Process(request(req, ""), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	m := resp.Messages[0]
	if m.ErrorInformation != nil {
		t.Fatalf("a request clause 6.4.1 requires every implementation to support was refused: %+v",
			m.ErrorInformation)
	}
	if want := strings.Replace(req, "Request", "Response", 1); m.Type != want {
		t.Errorf("type = %q, want %q", m.Type, want)
	}

	out, err := marshalResponse(resp)
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}
	rendered := string(out)

	// Absent, not empty. That is the schema's own distinction and the whole reason this can be
	// answered honestly: an empty list says Generic Objects are implemented and none is held,
	// and only an absent one says they are not implemented.
	if strings.Contains(rendered, "listOfGenericObjectResponseDetails") {
		t.Error("the answer carries an object list, which asserts that Generic Objects are " +
			"implemented and that none is held — a different and false claim")
	}
	// This response type is X1ResponseMessage plus the optional list, and nothing else; an
	// acknowledgement in it does not validate.
	if strings.Contains(rendered, "<ns1:oK>") {
		t.Error("the answer carries an oK, which this response type does not define")
	}
}

// TestGenericObjectCRUDIsRefused pins the other half. An acknowledgement here would tell a
// provisioning function its object had been stored, and it would then read back nothing and
// conclude its own write had failed.
func TestGenericObjectCRUDIsRefused(t *testing.T) {
	// The clause 6.8 set, in the schema's order.
	for _, req := range []string{
		"CreateObjectRequest", "ModifyObjectRequest", "GetObjectRequest",
		"DeleteObjectRequest", "ListObjectsOfTypeRequest", "DeleteAllObjectsRequest",
	} {
		t.Run(req, func(t *testing.T) {
			srv := testServer(store.New())
			srv.now = func() time.Time { return zeroTailInstant }

			resp, err := srv.Process(request(req, ""), admfPeer(t))
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			m := resp.Messages[0]
			if m.ErrorInformation == nil {
				t.Fatalf("want a refusal, got %+v", m)
			}
			if m.ErrorInformation.ErrorCode != errCodeUnsupportedRequest {
				t.Errorf("code = %d, want %d", m.ErrorInformation.ErrorCode, errCodeUnsupportedRequest)
			}

			out, err := marshalResponse(resp)
			if err != nil {
				t.Fatalf("marshalResponse: %v", err)
			}
			// The refusal names the request by the schema's own enumerated value rather than
			// falling back to ExtendedRequestMessageType. The six object types are in the XSD's
			// RequestMessageType enumeration even though table 6.7-1 of the document omits
			// them, and clause 7.2.1 makes the XSD authoritative where the two disagree.
			want := "<ns1:requestMessageType>" + strings.TrimSuffix(req, "Request") +
				"</ns1:requestMessageType>"
			if !strings.Contains(string(out), want) {
				t.Errorf("the refusal does not name the request as %s", want)
			}
		})
	}
}
