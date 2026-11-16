// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package http2_util

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
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
// http.ErrServerClosed. Serve takes ownership of the TLS key log file, if any, and closes it when it
// returns, so the server must not be served again. log may be nil.
func Serve(server *http.Server, scheme, pem, key string, log Logger) error {
	defer closeKeyLog(server)

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

// closeKeyLog closes the TLS key log writer of server if it is closable.
func closeKeyLog(server *http.Server) {
	if server.TLSConfig == nil {
		return
	}
	if c, ok := server.TLSConfig.KeyLogWriter.(io.Closer); ok {
		_ = c.Close()
	}
}

// ListenAndServeSBI builds the server with NewSBIServer and runs it with Serve.
func ListenAndServeSBI(bindAddr string, handler http.Handler, scheme, pem, key string, log Logger) error {
	server, err := NewSBIServer(bindAddr, handler, log)
	if err != nil {
		return err
	}
	return Serve(server, scheme, pem, key, log)
}
