// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/xml"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// bindingURN builds the annex G certificate binding URN for a role/identifier.
func bindingURN(t *testing.T, role, identifier string) *url.URL {
	t.Helper()
	u, err := url.Parse(certBindingURNPrefix + role + ":" + identifier)
	if err != nil {
		t.Fatalf("parse binding URN: %v", err)
	}
	return u
}

// issueCert creates a certificate from tmpl, signed by parent (self-signed when
// parent is nil), and returns it parsed as it would be received over TLS —
// parsing matters, since the Subject UID and SAN URI are only populated by the
// x509 parser, not carried over from the template.
func issueCert(t *testing.T, tmpl *x509.Certificate, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl.SerialNumber = big.NewInt(time.Now().UnixNano())
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(time.Hour)
	signer, signerKey := parent, parentKey
	if signer == nil {
		signer, signerKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert, key
}

// certWithUID returns a certificate binding identifier via a Subject UID RDN —
// the first of the two forms TS 103 221-1 clause 8.2.4 accepts.
func certWithUID(t *testing.T, identifier string) *x509.Certificate {
	t.Helper()
	cert, _ := issueCert(t, &x509.Certificate{
		Subject: pkix.Name{
			CommonName: "li-peer",
			ExtraNames: []pkix.AttributeTypeAndValue{{Type: oidUID, Value: identifier}},
		},
	}, nil, nil)
	return cert
}

// admfPeer is the certificate the ADMF the other tests speak as would present:
// bound to the "admfID" identifier their request bodies assert.
func admfPeer(t *testing.T) *x509.Certificate {
	t.Helper()
	return certWithUID(t, "admfID")
}

// certWithBindingURN returns a certificate binding identifier via an annex G
// subjectAltName URN — the second accepted form.
func certWithBindingURN(t *testing.T, role, identifier string) *x509.Certificate {
	t.Helper()
	cert, _ := issueCert(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: "li-peer"},
		URIs:    []*url.URL{bindingURN(t, role, identifier)},
	}, nil, nil)
	return cert
}

// TestCertBindsAcceptedForms checks both forms clause 8.2.4 allows, and that a
// certificate carrying neither — or the right form with the wrong value, role,
// or URN shape — does not bind.
func TestCertBindsAcceptedForms(t *testing.T) {
	tests := []struct {
		name string
		cert *x509.Certificate
		want bool
	}{
		{"subject UID matches", certWithUID(t, "admfID"), true},
		{"binding URN matches", certWithBindingURN(t, roleADMF, "admfID"), true},
		{"subject UID is a different identifier", certWithUID(t, "otherADMF"), false},
		{"binding URN is a different identifier", certWithBindingURN(t, roleADMF, "otherADMF"), false},
		{"binding URN carries the NE role", certWithBindingURN(t, "NE", "admfID"), false},
		{"no binding of any kind", certWithUID(t, ""), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := certBinds(tc.cert, roleADMF, "admfID"); got != tc.want {
				t.Errorf("certBinds = %v, want %v", got, tc.want)
			}
		})
	}

	// A URN that is not under the ETSI TC LI cert-binding prefix must not bind,
	// however similar it looks.
	u, err := url.Parse("urn:etsi:li:103221-1:cert-binding-x:ADMF:admfID")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cert, _ := issueCert(t, &x509.Certificate{URIs: []*url.URL{u}}, nil, nil)
	if certBinds(cert, roleADMF, "admfID") {
		t.Error("a URN outside the cert-binding namespace must not bind")
	}
}

// processWith runs body against a fresh server holding st, as peer.
func processWith(t *testing.T, st *store.Store, peer *x509.Certificate, body string, opts ...Option) *X1Response {
	t.Helper()
	resp, err := testServer(st, opts...).Process([]byte(body), peer)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(resp.Messages))
	}
	return resp
}

// assertRejected checks the single response message is an ErrorResponse carrying
// code, and that no tasking was applied.
func assertRejected(t *testing.T, resp *X1Response, st *store.Store, code int) {
	t.Helper()
	m := resp.Messages[0]
	if m.Type != "ErrorResponse" || m.ErrorInformation == nil {
		t.Fatalf("want ErrorResponse, got %+v", m)
	}
	if m.ErrorInformation.ErrorCode != code {
		t.Errorf("error code = %d, want %d", m.ErrorInformation.ErrorCode, code)
	}
	if m.OK != "" {
		t.Errorf("rejected message must not acknowledge, got OK=%q", m.OK)
	}
	if st.Len() != 0 {
		t.Errorf("rejected request must not task the NE, store len=%d", st.Len())
	}
}

// TestAuthenticationRejectsUnboundPeer covers the defect this check exists for:
// a chain-valid certificate is not authentication. Every case here would have
// been accepted when the handshake was the only gate.
func TestAuthenticationRejectsUnboundPeer(t *testing.T) {
	tests := []struct {
		name string
		peer *x509.Certificate
		body string
		opts []Option
		code int
	}{
		{
			name: "certificate binds a different ADMF identifier",
			peer: certWithUID(t, "rogueADMF"),
			body: activateXML,
			code: errCodeADMFCertMismatch,
		},
		{
			name: "no client certificate at all",
			peer: nil,
			body: activateXML,
			code: errCodeADMFCertMismatch,
		},
		{
			name: "certificate carries no binding",
			peer: certWithUID(t, ""),
			body: activateXML,
			code: errCodeADMFCertMismatch,
		},
		{
			name: "bound identity is not the responsible ADMF",
			peer: certWithUID(t, "rogueADMF"),
			body: strings.Replace(activateXML, "<ns1:admfIdentifier>admfID</ns1:admfIdentifier>",
				"<ns1:admfIdentifier>rogueADMF</ns1:admfIdentifier>", 1),
			opts: []Option{WithADMF("admfID")},
			code: errCodeUnexpectedADMF,
		},
		{
			name: "request addressed to a different network element",
			peer: certWithUID(t, "admfID"),
			body: strings.Replace(activateXML, "<ns1:neIdentifier>neID</ns1:neIdentifier>",
				"<ns1:neIdentifier>someOtherNE</ns1:neIdentifier>", 1),
			code: errCodeUnexpectedNE,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New()
			assertRejected(t, processWith(t, st, tc.peer, tc.body, tc.opts...), st, tc.code)
		})
	}
}

// TestAuthenticationAcceptsBoundPeer checks both binding forms admit a request,
// including with the responsible ADMF configured.
func TestAuthenticationAcceptsBoundPeer(t *testing.T) {
	for _, tc := range []struct {
		name string
		peer *x509.Certificate
	}{
		{"subject UID", certWithUID(t, "admfID")},
		{"binding URN", certWithBindingURN(t, roleADMF, "admfID")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New()
			resp := processWith(t, st, tc.peer, activateXML, WithADMF("admfID"))
			if resp.Messages[0].OK != "AcknowledgedAndCompleted" {
				t.Fatalf("want acknowledgement, got %+v", resp.Messages[0])
			}
			if _, ok := st.Get(testXID); !ok {
				t.Error("task not activated")
			}
		})
	}
}

// TestUnauthenticatedRequestDoesNotResetWatchdog checks an unauthenticated peer
// cannot hold the keepalive fail-safe open. Were unauthenticated traffic to count
// as ADMF liveness, anyone able to reach the X1 port could keep warrants alive
// indefinitely after the real ADMF went dark, defeating the purge.
func TestUnauthenticatedRequestDoesNotResetWatchdog(t *testing.T) {
	st := store.New()
	st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("imsi-1")}})
	srv := testServer(st)

	base := time.Now()
	srv.now = func() time.Time { return base }
	srv.recordActivity()

	// An unauthenticated keepalive arrives well after the window would lapse.
	base = base.Add(time.Minute)
	if _, err := srv.Process([]byte(keepaliveXML), certWithUID(t, "rogueADMF")); err != nil {
		t.Fatalf("Process: %v", err)
	}
	srv.purgeIfLapsed(time.Second)
	if st.Len() != 0 {
		t.Error("tasking survived: an unauthenticated message reset the watchdog")
	}
}

// TestServeHTTPRefusesAnUnverifiedCertificate: NewListener requires a verified client
// certificate, but Server is an http.Handler and can be mounted on a server that only requests
// one. A self-signed certificate naming the configured ADMF is then presented and never
// verified, and must authenticate nothing.
func TestServeHTTPRefusesAnUnverifiedCertificate(t *testing.T) {
	serverCert, serverKey := issueCert(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "ne"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, nil, nil)
	roots := x509.NewCertPool()
	roots.AddCert(serverCert)

	// Self-signed, so nothing the server holds could have verified it.
	clientCert, clientKey := issueCert(t, &x509.Certificate{
		Subject: pkix.Name{
			CommonName: "admf",
			ExtraNames: []pkix.AttributeTypeAndValue{{Type: oidUID, Value: "admfID"}},
		},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, nil, nil)

	st := store.New()
	ts := httptest.NewUnstartedServer(testServer(st, WithADMF("admfID")))
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverCert.Raw}, PrivateKey: serverKey}},
		ClientAuth:   tls.RequestClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	ts.StartTLS()
	defer ts.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{{Certificate: [][]byte{clientCert.Raw}, PrivateKey: clientKey}},
		MinVersion:   tls.VersionTLS12,
	}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/X1/NE", strings.NewReader(activateXML))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()

	var decoded X1Response
	if err := xml.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(decoded.Messages))
	}
	if m := decoded.Messages[0]; m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != errCodeADMFCertMismatch {
		t.Errorf("an unverified certificate naming the ADMF was answered %+v; want error %d", m, errCodeADMFCertMismatch)
	}
	if _, ok := st.Get(testXID); ok {
		t.Error("an unverified certificate tasked the element")
	}
}

// TestServeHTTPMutualTLS drives the whole path a deployed NE runs — a real mTLS
// handshake, the peer certificate taken from the connection, then the clause
// 8.2.4 check — for both an authorized ADMF and a rogue holder of a certificate
// from the same LI CA. The rogue case is the deployment risk: with X1 exposed
// outside the cluster, CA issuance alone must not confer tasking authority.
func TestServeHTTPMutualTLS(t *testing.T) {
	caTmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "LI CA"},
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	ca, caKey := issueCert(t, caTmpl, nil, nil)

	serverCert, serverKey := issueCert(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "ne"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, caKey)

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	for _, tc := range []struct {
		name       string
		clientUID  string
		wantOK     bool
		wantCode   int
		wantTasked bool
	}{
		{name: "authorized ADMF", clientUID: "admfID", wantOK: true, wantTasked: true},
		{name: "rogue holder of an LI CA certificate", clientUID: "rogueADMF", wantCode: errCodeADMFCertMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientCert, clientKey := issueCert(t, &x509.Certificate{
				Subject: pkix.Name{
					CommonName: "admf",
					ExtraNames: []pkix.AttributeTypeAndValue{{Type: oidUID, Value: tc.clientUID}},
				},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}, ca, caKey)

			st := store.New()
			ts := httptest.NewUnstartedServer(testServer(st, WithADMF("admfID")))
			ts.TLS = &tls.Config{
				Certificates: []tls.Certificate{{Certificate: [][]byte{serverCert.Raw}, PrivateKey: serverKey}},
				ClientCAs:    pool,
				ClientAuth:   tls.RequireAndVerifyClientCert,
				MinVersion:   tls.VersionTLS12,
			}
			ts.StartTLS()
			defer ts.Close()

			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
				RootCAs:      pool,
				Certificates: []tls.Certificate{{Certificate: [][]byte{clientCert.Raw}, PrivateKey: clientKey}},
				MinVersion:   tls.VersionTLS12,
			}}}
			httpReq, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/X1/NE", strings.NewReader(activateXML))
			if reqErr != nil {
				t.Fatalf("build request: %v", reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/xml")
			res, err := client.Do(httpReq)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer res.Body.Close()

			var decoded X1Response
			if err := xml.NewDecoder(res.Body).Decode(&decoded); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(decoded.Messages) != 1 {
				t.Fatalf("got %d messages, want 1", len(decoded.Messages))
			}
			m := decoded.Messages[0]
			if tc.wantOK {
				if m.OK != "AcknowledgedAndCompleted" {
					t.Errorf("want acknowledgement, got %+v", m)
				}
			} else if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != tc.wantCode {
				t.Errorf("want error code %d, got %+v", tc.wantCode, m)
			}
			if tasked := st.Len() > 0; tasked != tc.wantTasked {
				t.Errorf("tasked = %v, want %v", tasked, tc.wantTasked)
			}
		})
	}
}

// TestUnsupportedRequestUsesStandardCode checks an unrecognised request type is
// refused with the reserved code rather than an invented one. An ADMF reads these
// numerically, so a code outside the standard's table is one it cannot interpret.
func TestUnsupportedRequestUsesStandardCode(t *testing.T) {
	st := store.New()
	// A type this element genuinely does not implement.
	body := strings.Replace(activateXML, "ActivateTaskRequest", "SomeFutureRequest", 1)
	assertRejected(t, processWith(t, st, admfPeer(t), body), st, errCodeUnsupportedRequest)
}

// TestAuthFailureIsReported: a refused provisioning attempt was
// refused correctly and then recorded nowhere. This interface keeps deliberately
// out of operator logs, so the callback is the only thing standing between an
// attack on LI provisioning and complete silence.
func TestAuthFailureIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		peer *x509.Certificate
		want int // 0 = no report expected
	}{
		{"unbound peer is reported", certWithUID(t, "rogueADMF"), errCodeADMFCertMismatch},
		{"no certificate is reported", nil, errCodeADMFCertMismatch},
		{"a legitimate ADMF is not", certWithUID(t, "admfID"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			st := store.New()
			processWith(t, st, tc.peer, activateXML,
				WithADMF("admfID"),
				OnAuthFailure(func(code int) { got = append(got, code) }))

			if tc.want == 0 {
				if len(got) != 0 {
					t.Fatalf("reported %v for an authorised ADMF; the ADMF would be told it is attacking itself", got)
				}

				return
			}

			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("reported %v, want exactly [%d]", got, tc.want)
			}
		})
	}
}
