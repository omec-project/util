// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package http2_util

import (
	"net/http"
	"testing"
	"time"
)

func TestNewServerEnablesSupportedProtocols(t *testing.T) {
	t.Parallel()

	server, err := NewServer("127.0.0.1:0", "", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	if server.Protocols == nil {
		t.Fatal("NewServer left Protocols unset")
	}

	if !server.Protocols.HTTP1() {
		t.Fatal("NewServer must keep HTTP/1 enabled")
	}

	if !server.Protocols.HTTP2() {
		t.Fatal("NewServer must keep TLS HTTP/2 enabled")
	}

	if !server.Protocols.UnencryptedHTTP2() {
		t.Fatal("NewServer must keep h2c enabled")
	}

	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("NewServer idle timeout = %s, want %s", server.IdleTimeout, 60*time.Second)
	}
}
