// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

//go:build !debug
// +build !debug

package http2_util

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"
)

// NewServer centralizes the default HTTP server setup used by the control-plane services.
// In non-debug builds it enables HTTP/1 and unencrypted HTTP/2 on the same listener,
// keeps the shared idle-timeout behavior, and optionally configures TLS key logging.
//
// If preMasterSecretLogPath cannot be opened, NewServer still returns a usable server
// without KeyLogWriter configured, along with the corresponding error so the caller can
// decide whether to continue.
func NewServer(bindAddr string, preMasterSecretLogPath string, handler http.Handler) (server *http.Server, err error) {
	if handler == nil {
		return nil, fmt.Errorf("server needs handler to handle request")
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server = &http.Server{
		Addr:        bindAddr,
		Handler:     handler,
		Protocols:   protocols,
		IdleTimeout: 1 * time.Millisecond,
	}

	if preMasterSecretLogPath != "" {
		preMasterSecretFile, err := os.OpenFile(preMasterSecretLogPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return server, fmt.Errorf("create pre-master-secret log [%s] fail: %s", preMasterSecretLogPath, err)
		}
		server.TLSConfig = &tls.Config{
			KeyLogWriter: preMasterSecretFile,
		}
	}

	return
}
