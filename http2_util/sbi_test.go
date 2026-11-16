// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package http2_util

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingLogger struct {
	msgs []string
	mu   sync.Mutex
}

func (l *recordingLogger) Warnf(template string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, fmt.Sprintf(template, args...))
}

func (l *recordingLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.msgs...)
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// selfSigned writes a certificate for 127.0.0.1 and its key to dir and returns their paths and a
// client that trusts it.
func selfSigned(t *testing.T, dir string) (certPath, keyPath string, client *http.Client) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

// serveInBackground runs Serve and returns the channel that receives its result and a stop function.
func serveInBackground(t *testing.T, server *http.Server, scheme, pemPath, keyPath string, log Logger) (<-chan error, func()) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- Serve(server, scheme, pemPath, keyPath, log) }()
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("server shutdown: %v", err)
		}
	}
	t.Cleanup(stop)
	return result, stop
}

func waitUntilServing(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not come up at %s", url)
}

func TestServe_HTTP_ServesAndShutdownReturnsErrServerClosed(t *testing.T) {
	t.Setenv(KeyLogFileEnv, "")
	addr := freeAddr(t)
	server, err := NewSBIServer(addr, okHandler, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, stop := serveInBackground(t, server, "http", "", "", nil)

	waitUntilServing(t, http.DefaultClient, "http://"+addr)
	stop()
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
}

func TestServe_HTTPS_ServesAndWritesKeyLogAndWarns(t *testing.T) {
	dir := t.TempDir()
	keyLog := filepath.Join(dir, "keys.log")
	t.Setenv(KeyLogFileEnv, keyLog)
	certPath, keyPath, client := selfSigned(t, dir)
	log := &recordingLogger{}

	addr := freeAddr(t)
	server, err := NewSBIServer(addr, okHandler, log)
	if err != nil {
		t.Fatal(err)
	}
	serveInBackground(t, server, "https", certPath, keyPath, log)
	waitUntilServing(t, client, "https://"+addr)

	info, err := os.Stat(keyLog)
	if err != nil {
		t.Fatalf("key log file was not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("key log file is empty after a TLS handshake")
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key log file mode = %v, want 0600", info.Mode().Perm())
	}
	var warned bool
	for _, m := range log.messages() {
		if strings.Contains(m, "TLS key logging is enabled") && strings.Contains(m, keyLog) {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no key logging warning naming the file, got %q", log.messages())
	}
}

func TestServe_HTTPS_WithoutKeyLogDoesNotWarn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(KeyLogFileEnv, "")
	certPath, keyPath, client := selfSigned(t, dir)
	log := &recordingLogger{}

	addr := freeAddr(t)
	server, err := NewSBIServer(addr, okHandler, log)
	if err != nil {
		t.Fatal(err)
	}
	serveInBackground(t, server, "https", certPath, keyPath, log)
	waitUntilServing(t, client, "https://"+addr)

	if msgs := log.messages(); len(msgs) != 0 {
		t.Errorf("unexpected log messages: %q", msgs)
	}
}

func TestServe_HTTPS_MissingCertificateIsAnError(t *testing.T) {
	t.Setenv(KeyLogFileEnv, "")
	server, err := NewSBIServer(freeAddr(t), okHandler, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(server, "https", "/nonexistent/cert.pem", "/nonexistent/key.pem", nil); err == nil {
		t.Fatal("expected an error for missing TLS files")
	}
}

func TestServe_InvalidSchemeIsAnError(t *testing.T) {
	t.Setenv(KeyLogFileEnv, "")
	server, err := NewSBIServer(freeAddr(t), okHandler, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, scheme := range []string{"", "ftp", "HTTP"} {
		if err := Serve(server, scheme, "", "", nil); err == nil || !strings.Contains(err.Error(), "invalid server scheme") {
			t.Errorf("scheme %q: err = %v, want invalid server scheme", scheme, err)
		}
	}
}

func TestNewSBIServer_UnwritableKeyLogIsAWarningNotAnError(t *testing.T) {
	t.Setenv(KeyLogFileEnv, filepath.Join(t.TempDir(), "missing-dir", "keys.log"))
	log := &recordingLogger{}

	server, err := NewSBIServer("127.0.0.1:0", okHandler, log)
	if err != nil || server == nil {
		t.Fatalf("server=%v err=%v, want a usable server", server, err)
	}
	if server.TLSConfig != nil && server.TLSConfig.KeyLogWriter != nil {
		t.Error("key logging must not be enabled when the file cannot be created")
	}
	if msgs := log.messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "initialize HTTP server") {
		t.Errorf("log messages = %q, want one warning", msgs)
	}
}

func TestNewSBIServer_NilHandlerIsAnError(t *testing.T) {
	t.Setenv(KeyLogFileEnv, "")
	if server, err := NewSBIServer("127.0.0.1:0", nil, nil); err == nil || server != nil {
		t.Errorf("server=%v err=%v, want an error", server, err)
	}
}

func TestListenAndServeSBI_InvalidScheme(t *testing.T) {
	t.Setenv(KeyLogFileEnv, "")
	if err := ListenAndServeSBI("127.0.0.1:0", okHandler, "gopher", "", "", nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestServe_TypedNilLoggerDoesNotPanic(t *testing.T) {
	var l *recordingLogger // typed nil: a non-nil Logger interface holding a nil pointer
	if hasLogger(l) {
		t.Error("hasLogger(typed nil) = true, want false")
	}
	if hasLogger(nil) {
		t.Error("hasLogger(nil) = true, want false")
	}
	if !hasLogger(&recordingLogger{}) {
		t.Error("hasLogger(real logger) = false, want true")
	}
}

func TestServe_ClosesKeyLogFileOnReturn(t *testing.T) {
	t.Setenv(KeyLogFileEnv, filepath.Join(t.TempDir(), "keys.log"))
	server, err := NewSBIServer(freeAddr(t), okHandler, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := server.TLSConfig.KeyLogWriter.(*os.File)
	if !ok {
		t.Fatalf("KeyLogWriter is %T, want *os.File", server.TLSConfig.KeyLogWriter)
	}
	if err := Serve(server, "ftp", "", "", nil); err == nil {
		t.Fatal("expected an invalid scheme error")
	}
	if _, err := f.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("write after Serve returned: err = %v, want os.ErrClosed", err)
	}
}

func TestServe_KeepsKeyLogOpenUntilInFlightConnectionsFinish(t *testing.T) {
	t.Setenv(KeyLogFileEnv, filepath.Join(t.TempDir(), "keys.log"))
	addr := freeAddr(t)
	server, err := NewSBIServer(addr, okHandler, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := server.TLSConfig.KeyLogWriter.(*os.File)
	pemPath, keyPath, client := selfSigned(t, t.TempDir())
	result, _ := serveInBackground(t, server, "https", pemPath, keyPath, nil)
	waitUntilServing(t, client, "https://"+addr)

	// A connection whose TLS handshake has not started yet, like one still draining at shutdown.
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// Times out: the pending connection is not idle yet.
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v, want context.DeadlineExceeded", err)
	}
	if err := <-result; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve = %v, want http.ErrServerClosed", err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatalf("key log closed while a connection was in flight: %v", err)
	}

	_ = conn.Close()
	_ = server.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := f.Write([]byte("x")); errors.Is(err, os.ErrClosed) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("key log not closed after the last connection finished")
}

func TestNewSBIServer_KeyLogIsAppendedNotTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.log")
	if err := os.WriteFile(path, []byte("earlier secrets\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyLogFileEnv, path)
	server, err := NewSBIServer(freeAddr(t), okHandler, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeKeyLogWriter(server)
	if _, err = server.TLSConfig.KeyLogWriter.Write([]byte("new secrets\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "earlier secrets\nnew secrets\n"; string(got) != want {
		t.Errorf("key log = %q, want %q", got, want)
	}
}

func closeKeyLogWriter(server *http.Server) {
	if c, ok := server.TLSConfig.KeyLogWriter.(io.Closer); ok {
		_ = c.Close()
	}
}
