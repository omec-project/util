// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package http2_util

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"sync"
)

// KeyLogFileEnv names the environment variable that enables TLS key logging. Key logging is a
// debugging aid and is off unless the operator sets it.
const KeyLogFileEnv = "SSLKEYLOGFILE"

// Logger is the part of a (zap sugared) logger that the helpers in this file use.
type Logger interface {
	Warnf(template string, args ...any)
}

// hasLogger reports whether log can be called: neither a nil interface nor an interface holding a nil
// pointer, such as a nil *zap.SugaredLogger.
func hasLogger(log Logger) bool {
	if log == nil {
		return false
	}
	v := reflect.ValueOf(log)
	return v.Kind() != reflect.Pointer || !v.IsNil()
}

// NewSBIServer is NewServer for a service-based interface: TLS key logging is taken from the
// SSLKEYLOGFILE environment variable. A key log file that cannot be created is not fatal, the server
// is returned without key logging and the problem is logged as a warning. An error is only returned
// if no usable server could be built. log may be nil.
func NewSBIServer(bindAddr string, handler http.Handler, log Logger) (*http.Server, error) {
	server, err := NewServer(bindAddr, os.Getenv(KeyLogFileEnv), handler)
	if server == nil {
		return nil, fmt.Errorf("initialize HTTP server failed: %w", err)
	}
	if err != nil && hasLogger(log) {
		log.Warnf("initialize HTTP server: %+v", err)
	}
	return server, nil
}

// Serve runs server for the given scheme and blocks until it fails or is shut down. The scheme is
// "http", or "https" with the certificate and private key in the pem and key files. Any other scheme
// is an error. The error is that of the underlying ListenAndServe, so a graceful shutdown returns
// http.ErrServerClosed. Serve takes ownership of the TLS key log file, if any, so the server must not
// be served again. The file is closed once Serve has returned and every connection accepted by the
// server has finished, because Serve returns as soon as Shutdown closes the listener while in-flight
// TLS handshakes still need to write their keys. log may be nil.
func Serve(server *http.Server, scheme, pem, key string, log Logger) error {
	defer trackKeyLog(server).serveReturned()

	switch scheme {
	case "http":
		return server.ListenAndServe()
	case "https":
		if hasLogger(log) && server.TLSConfig != nil && server.TLSConfig.KeyLogWriter != nil {
			log.Warnf("TLS key logging is enabled (%s): SBI traffic is decryptable by anyone who can "+
				"read this file", keyLogName(server.TLSConfig.KeyLogWriter))
		}
		return server.ListenAndServeTLS(pem, key)
	default:
		return fmt.Errorf("invalid server scheme %q", scheme)
	}
}

// keyLogName returns the path of the key log writer, or a placeholder if it is not a file.
func keyLogName(w io.Writer) string {
	if f, ok := w.(interface{ Name() string }); ok {
		return f.Name()
	}
	return "unnamed writer"
}

// keyLogCloser closes the TLS key log writer of a server after the server has stopped accepting
// connections and the last connection it accepted has finished.
type keyLogCloser struct {
	closer io.Closer

	mu       sync.Mutex
	conns    int
	returned bool
	closed   bool
}

// trackKeyLog returns a keyLogCloser for server and hooks server.ConnState to count its connections,
// keeping any hook already set. The result is a no-op if server has no closable key log writer.
func trackKeyLog(server *http.Server) *keyLogCloser {
	k := &keyLogCloser{}
	if server.TLSConfig == nil {
		return k
	}
	c, ok := server.TLSConfig.KeyLogWriter.(io.Closer)
	if !ok {
		return k
	}
	k.closer = c

	prev := server.ConnState
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			k.mu.Lock()
			k.conns++
			k.mu.Unlock()
		case http.StateHijacked, http.StateClosed:
			k.mu.Lock()
			k.conns--
			k.closeIfDoneLocked()
			k.mu.Unlock()
		}
		if prev != nil {
			prev(conn, state)
		}
	}
	return k
}

// serveReturned records that the server no longer accepts connections.
func (k *keyLogCloser) serveReturned() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.returned = true
	k.closeIfDoneLocked()
}

func (k *keyLogCloser) closeIfDoneLocked() {
	if k.closer == nil || k.closed || !k.returned || k.conns > 0 {
		return
	}
	k.closed = true
	_ = k.closer.Close()
}

// ListenAndServeSBI builds the server with NewSBIServer and runs it with Serve.
func ListenAndServeSBI(bindAddr string, handler http.Handler, scheme, pem, key string, log Logger) error {
	server, err := NewSBIServer(bindAddr, handler, log)
	if err != nil {
		return err
	}
	return Serve(server, scheme, pem, key, log)
}
