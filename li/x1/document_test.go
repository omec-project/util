// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
)

// TestABodyThatIsNotOneDocumentIsATopLevelError: xml.Unmarshal reads the first element and
// stops, so a body carrying more than one request, or text around one, was answered as the
// first request alone. Such a body is not a well-formed document and holds no request to answer.
func TestABodyThatIsNotOneDocumentIsATopLevelError(t *testing.T) {
	second := strings.Replace(activateXML, `<?xml version="1.0" encoding="UTF-8"?>`, "", 1)

	for name, body := range map[string]string{
		"a second request after the first": activateXML + second,
		"text after the root":              activateXML + "\nx",
		"an element after the root":        activateXML + "<x/>",
		"text before the root":             strings.Replace(activateXML, "<ns1:X1Request", "x<ns1:X1Request", 1),
	} {
		t.Run(name, func(t *testing.T) {
			st := store.New()

			if _, err := testServer(st).Process([]byte(body), admfPeer(t)); err == nil {
				t.Fatal("processed")
			}
			if _, ok := st.Get(testXID); ok {
				t.Error("the task was activated")
			}
		})
	}
}

// TestWhatXMLAllowsAroundTheRootIsAccepted: whitespace, comments and processing instructions
// may follow the root element, and a document type declaration may precede it.
func TestWhatXMLAllowsAroundTheRootIsAccepted(t *testing.T) {
	body := strings.Replace(activateXML, "<ns1:X1Request", "<!DOCTYPE X1Request>\n<!-- c -->\n<ns1:X1Request", 1) +
		"\n<!-- trailing -->\n<?pi data?>\n\t "
	st := store.New()

	if _, err := testServer(st).Process([]byte(body), admfPeer(t)); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if _, ok := st.Get(testXID); !ok {
		t.Error("the task was not activated")
	}
}

// TestAByteOrderMarkMayPrecedeTheDocument: a UTF-8 byte order mark is not part of the document,
// and serialisers emit one.
func TestAByteOrderMarkMayPrecedeTheDocument(t *testing.T) {
	bom := "\xef\xbb\xbf"
	for name, body := range map[string]string{
		"with a declaration":    bom + activateXML,
		"without a declaration": bom + strings.Replace(activateXML, `<?xml version="1.0" encoding="UTF-8"?>`+"\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			st := store.New()

			if _, err := testServer(st).Process([]byte(body), admfPeer(t)); err != nil {
				t.Fatalf("Process: %v", err)
			}
			if _, ok := st.Get(testXID); !ok {
				t.Error("the task was not activated")
			}
		})
	}
}

// TestAnAnswerThatIsNotOneDocumentIsRefused: the requester reads the answers it receives as the
// server reads requests, so an answer followed by another is not an acknowledgement.
func TestAnAnswerThatIsNotOneDocumentIsRefused(t *testing.T) {
	answer := func(trailer string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)                     //nolint:errcheck // test handler
			_, _ = w.Write([]byte(conformantResponse(t, body, //nolint:errcheck // test handler
				`<oK>AcknowledgedAndCompleted</oK>`) + trailer))
		})
	}

	req, _ := requesterTo(t, answer("\n<!-- c -->\n"))
	if err := req.Keepalive(); err != nil {
		t.Fatalf("a single answer was refused: %v", err)
	}

	req, _ = requesterTo(t, answer(`<X1Response xmlns="http://uri.etsi.org/03221/X1/2017/10"/>`))
	if err := req.Keepalive(); err == nil {
		t.Error("an answer followed by another was accepted")
	}
}
