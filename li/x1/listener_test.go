// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestListenerBoundsEveryPhase: net/http applies no deadline unless one is set, so
// without these an unauthenticated peer could open a connection, stall the TLS handshake,
// and hold it for as long as it liked. The assertion is on the server the network
// functions actually get, since a server constructed by hand would simply omit them.
func TestListenerBoundsEveryPhase(t *testing.T) {
	srv := NewListener(http.NewServeMux(), nil)

	// ReadHeaderTimeout is the one that closes the demonstrated attack: net/http
	// derives the TLS handshake deadline from it, and the stall happens before any
	// request exists to apply the others to.
	for _, f := range []struct {
		name string
		got  time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout},
		{"ReadTimeout", srv.ReadTimeout},
		{"WriteTimeout", srv.WriteTimeout},
		{"IdleTimeout", srv.IdleTimeout},
	} {
		if f.got <= 0 {
			t.Errorf("%s is %v, so a stalled peer holds the connection indefinitely", f.name, f.got)
		}
	}

	// The other half of what this constructor exists to guarantee: handshake
	// failures must not reach the general operator log.
	if srv.ErrorLog == nil || srv.ErrorLog.Writer() != io.Discard {
		t.Error("server errors are not discarded; a failed handshake would name the LI domain in the operator log")
	}
}
