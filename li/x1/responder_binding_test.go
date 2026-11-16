// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// liCert issues a certificate from a throwaway LI CA. binding, when non-empty, is
// placed as an annex G certificate binding URI; uid, when non-empty, as the Subject
// UID relative distinguished name. Both forms are what clause 8.2.4 accepts, and the
// point of the tests below is which of them an element consults.
func liCert(t *testing.T, uid, binding string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	tmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "li-peer"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	if uid != "" {
		tmpl.Subject.ExtraNames = []pkix.AttributeTypeAndValue{{Type: oidUID, Value: uid}}
	}
	if binding != "" {
		u, err := url.Parse(binding)
		if err != nil {
			t.Fatal(err)
		}
		tmpl.URIs = []*url.URL{u}
	}

	leaf, key := issueCert(t, tmpl, nil, nil)

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}, pool
}

// poiNEID is the element identifier the stub POI asserts about itself. Constant,
// because what varies across these cases is what its *certificate* binds — the point
// being that the assertion is the part any endpoint can make correctly.
const poiNEID = "upf-1"

// answeringPOI is a triggered POI that acknowledges whatever it is asked, over TLS,
// presenting the given certificate. What it asserts about itself in the message is
// always correct — the question is whether its *certificate* backs the assertion.
func answeringPOI(t *testing.T, cert tls.Certificate, pool *x509.CertPool) (*httptest.Server, *tls.Config) {
	t.Helper()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		txID := between(string(body), "<ns1:x1TransactionId>", "</ns1:x1TransactionId>")
		reqType := between(string(body), `xsi:type="ns1:`, `"`)

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test handler
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Response xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <ns1:x1ResponseMessage xsi:type="ns1:` + strings.Replace(reqType, "Request", "Response", 1) + `">
    <ns1:admfIdentifier>smf-1</ns1:admfIdentifier>
    <ns1:neIdentifier>` + poiNEID + `</ns1:neIdentifier>
    <ns1:messageTimestamp>2026-08-18T00:00:00.000000Z</ns1:messageTimestamp>
    <ns1:version>` + supportedVersion + `</ns1:version>
    <ns1:x1TransactionId>` + txID + `</ns1:x1TransactionId>
    <ns1:oK>AcknowledgedAndCompleted</ns1:oK>
  </ns1:x1ResponseMessage>
</ns1:X1Response>`))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func between(s, open, closing string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, closing)
	if j < 0 {
		return ""
	}

	return rest[:j]
}

// TestAnAnswerIsRefusedUnlessTheCertificateBindsTheAddressedElement: the requester binds
// the answer to the peer's certificate, as the server binds every inbound message.
//
// Verifying the chain and the hostname is not enough. A hostname binds a name; the annex G
// binding binds an X1 identifier, and inside one LI trust domain the holder of any CA-issued
// certificate whose SANs cover the dialled address could assert the configured neIdentifier
// and convince a triggering function that a trigger was installed at an element that never
// received it. Nothing downstream would reveal that — the product that would be missing is
// never produced.
func TestAnAnswerIsRefusedUnlessTheCertificateBindsTheAddressedElement(t *testing.T) {
	const neID = "upf-1"

	trigger := Trigger{
		XID:           types.XID("11111111-1111-4111-8111-111111111111"),
		ProductID:     types.XID("22222222-2222-4222-8222-222222222222"),
		CorrelationID: 7,
		SEID:          0x2632898145f4d191,
		DIDs:          []string{"33333333-3333-4333-8333-333333333333"},
	}

	t.Run("bound in the NE role", func(t *testing.T) {
		cert, pool := liCert(t, "", certBindingURNPrefix+roleNE+":"+neID)
		srv, clientTLS := answeringPOI(t, cert, pool)

		r := NewRequester(srv.URL, "smf-1", neID, clientTLS)
		if err := r.ActivateTask(trigger); err != nil {
			t.Fatalf("an answer from the element that was addressed was refused: %v", err)
		}
	})

	t.Run("valid certificate binding a different element", func(t *testing.T) {
		// Issued by the same CA, valid for the dialled address, and binding some other
		// element's identifier. Everything TLS checks passes.
		cert, pool := liCert(t, "", certBindingURNPrefix+roleNE+":upf-9")
		srv, clientTLS := answeringPOI(t, cert, pool)

		r := NewRequester(srv.URL, "smf-1", neID, clientTLS)
		err := r.ActivateTask(trigger)
		if err == nil {
			t.Fatal("tasking was recorded as installed on the word of an element whose " +
				"certificate binds a different identifier")
		}
		if !strings.Contains(err.Error(), "peer certificate") {
			t.Errorf("refused for the wrong reason: %v", err)
		}
	})

	t.Run("bound in the ADMF role rather than the NE role", func(t *testing.T) {
		// The role is part of the binding: a certificate saying this identifier is an
		// ADMF says nothing about it as a network element.
		cert, pool := liCert(t, "", certBindingURNPrefix+roleADMF+":"+neID)
		srv, clientTLS := answeringPOI(t, cert, pool)

		r := NewRequester(srv.URL, "smf-1", neID, clientTLS)
		if err := r.ActivateTask(trigger); err == nil {
			t.Fatal("an answer bound in the wrong role was accepted")
		}
	})

	t.Run("bound by Subject UID", func(t *testing.T) {
		// The other form clause 8.2.4 admits, which must work equally.
		cert, pool := liCert(t, neID, "")
		srv, clientTLS := answeringPOI(t, cert, pool)

		r := NewRequester(srv.URL, "smf-1", neID, clientTLS)
		if err := r.ActivateTask(trigger); err != nil {
			t.Fatalf("an answer bound by Subject UID was refused: %v", err)
		}
	})
}

// TestAnUnparseableRequestFromAURIBoundPeerIsAnsweredWithItsIdentifier is clause 6.1's
// own provision, applied to both of the forms this element accepts.
//
// A request that cannot be parsed still has to be answered, and the admfIdentifier
// cannot come from the message — so it comes from configuration, or from the peer's
// certificate: "extracting the identifier of the Requester from the X.509 certificate
// if necessary". This element authenticates a peer by either a Subject UID or an annex G
// binding URI, so it must identify one by either too: a peer carrying its identity solely
// in the binding URI would otherwise be answered with an empty admfIdentifier, and could
// not tell which peer the answer was about.
func TestAnUnparseableRequestFromAURIBoundPeerIsAnsweredWithItsIdentifier(t *testing.T) {
	const admfID = "admf-7"

	for _, tc := range []struct {
		name, uid, binding string
	}{
		{name: "identified by an annex G binding URI", binding: certBindingURNPrefix + roleADMF + ":" + admfID},
		{name: "identified by a Subject UID", uid: admfID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, _ := liCert(t, tc.uid, tc.binding)

			// No configured ADMF identifier, which is the case that sends this to the
			// certificate at all.
			srv := testServer(nil)
			answer := srv.topLevelError(cert.Leaf)

			if !strings.Contains(string(answer), "<ns1:admfIdentifier>"+admfID+"</ns1:admfIdentifier>") {
				t.Errorf("the peer was not named in the answer:\n%s", answer)
			}
			// requireXmllint before asking: without the validator this returns a
			// diagnostic rather than nothing, so a run on a machine that has no libxml2
			// would report the answer as schema-invalid when it had not been checked at
			// all. The conformance CI job sets LI_REQUIRE_SCHEMA_VALIDATION and fails
			// rather than skipping, which is where the guarantee lives.
			requireXmllint(t)

			// The answer is schema-valid either way — an empty admfIdentifier validates —
			// and asserted anyway, because the answer to a fault must not itself be one.
			if problems := validateAgainstSchema(t, answer); len(problems) > 0 {
				t.Errorf("the answer to an unparseable request is itself schema-invalid: %v", problems)
			}
		})
	}
}

// TestAProvisionedCorrelationIDFollowsTheElement is the requirement's conclusion that a
// field's disposition can depend on which element is asked.
//
// Only a content POI acts on it: the IRI-POIs send a zero correlation and the session's
// F-SEID respectively. The value decides how a mediation function joins an element's
// records to a warrant's content, so accepting it where it is ignored would acknowledge an
// ADMF provisioning it per clause 6.2.1.2 and give it a different interception from the one
// it authorised — a silent divergence no channel could report.
//
// Neither uniform answer is available. Refusing everywhere would refuse tasking an ADMF
// may legitimately send to several elements at once; accepting everywhere is the
// divergence. So the element's role is part of the test.
func TestAProvisionedCorrelationIDFollowsTheElement(t *testing.T) {
	withCorrelation := strings.Replace(activateXML,
		"<ns1:targetIdentifiers>",
		"<ns1:correlationID>2752413510594253201</ns1:correlationID>\n    <ns1:targetIdentifiers>", 1)

	t.Run("refused at an element whose correlation is per session", func(t *testing.T) {
		srv := testServer(store.New())

		resp, err := srv.Process([]byte(withCorrelation), admfPeer(t))
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		m := resp.Messages[0]
		if m.OK == ackOK {
			t.Fatal("a correlation value this element cannot honour was accepted; its records " +
				"would carry one the ADMF did not ask for, with nothing to indicate the substitution")
		}
		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeActivateFailed {
			t.Errorf("refused with %+v, want code %d — the registry's own \"details of why the "+
				"Task cannot be activated\"", m.ErrorInformation, errCodeActivateFailed)
		}
	})

	t.Run("accepted at an element that stamps it", func(t *testing.T) {
		srv := testServer(store.New(), HonoursCorrelationID())

		resp, err := srv.Process([]byte(withCorrelation), admfPeer(t))
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if m := resp.Messages[0]; m.OK != ackOK {
			t.Fatalf("a content POI refused a value it acts on: %+v", m)
		}
	})
}
