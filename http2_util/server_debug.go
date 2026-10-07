// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

//go:build debug
// +build debug

package http2_util

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
)

type zeroSource struct{}

func (zeroSource) Read(b []byte) (n int, err error) {
	for i := range b {
		b[i] = 0
	}
	return len(b), nil
}

// NewServer sets up the HTTP server used by the control-plane services in debug
// builds. It forces deterministic TLS randomness (see zeroSource) so captured
// traffic can be decrypted, and optionally configures TLS key logging.
//
// If tlskeylog is empty, key logging is disabled and the server still starts.
// If tlskeylog cannot be opened, NewServer still returns a usable server without
// KeyLogWriter configured, along with the corresponding error so the caller can
// decide whether to continue.
func NewServer(bindAddr string, tlskeylog string, handler http.Handler) (server *http.Server, err error) {
	if handler == nil {
		return nil, fmt.Errorf("server needs handler to handle request")
	}

	server = &http.Server{
		Addr: bindAddr,
		TLSConfig: &tls.Config{
			Rand: zeroSource{},
		},
		Handler:  handler,
		ErrorLog: newServerErrorLog(),
	}

	if tlskeylog != "" {
		var keylogFile *os.File
		keylogFile, err = os.OpenFile(tlskeylog, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return server, fmt.Errorf("create pre-master-secret log [%s] fail: %w", tlskeylog, err)
		}
		server.TLSConfig.KeyLogWriter = keylogFile
	}

	return
}
