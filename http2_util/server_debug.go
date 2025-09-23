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

type ZeroSource struct{}

func (ZeroSource) Read(b []byte) (n int, err error) {
	for i := range b {
		b[i] = 0
	}
	return len(b), nil
}

func NewServer(bindAddr string, tlskeylog string, handler http.Handler) (server *http.Server, err error) {
	keylogFile, err := os.OpenFile(tlskeylog, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, fmt.Errorf("server need handler")
	}
	server = &http.Server{
		Addr: bindAddr,
		TLSConfig: &tls.Config{
			KeyLogWriter: keylogFile,
			Rand:         ZeroSource{},
		},
		Handler: handler,
	}
	return
}
