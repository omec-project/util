// SPDX-FileCopyrightText: 2026 Martin Matyas
// SPDX-License-Identifier: Apache-2.0

package http2_util

import (
	"log"
	"strings"
	"sync/atomic"

	"github.com/omec-project/util/logger"
	"go.uber.org/zap"
)

// handshakeErrorPrefix and closedBeforeHandshakeSuffix match the line net/http
// logs for a connection that was closed before the TLS handshake: a TCP probe,
// a load balancer's TCP health check or a port scan.
const (
	handshakeErrorPrefix        = "http: TLS handshake error from "
	closedBeforeHandshakeSuffix = ": EOF"
)

// serverErrorLogWriter receives the error log of an http.Server. log.Logger
// calls Write once per line, so each call is one message.
type serverErrorLogWriter struct {
	log *zap.SugaredLogger
}

var droppedHandshakeProbes atomic.Uint64

// DroppedHandshakeProbes reports how many connections were closed before the
// TLS handshake and were therefore dropped from the error log rather than
// logged. Callers that want this in their own metrics should poll it
// periodically; it is not, and must not become, labeled by remote address,
// since that would let a probing peer drive unbounded label cardinality.
func DroppedHandshakeProbes() uint64 {
	return droppedHandshakeProbes.Load()
}

// Write drops the line of a connection closed before the TLS handshake and
// passes every other server error, a failed handshake with a real peer among
// them, to the zap logger.
func (w serverErrorLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")
	if closedBeforeHandshake(msg) {
		droppedHandshakeProbes.Add(1)
		return len(p), nil
	}
	w.log.Error(msg)
	return len(p), nil
}

func closedBeforeHandshake(msg string) bool {
	return strings.HasPrefix(msg, handshakeErrorPrefix) && strings.HasSuffix(msg, closedBeforeHandshakeSuffix)
}

// newServerErrorLog returns the error log for the servers built here. Without
// one, net/http writes its errors through the standard library logger, outside
// the NF's log format and level.
func newServerErrorLog() *log.Logger {
	return log.New(serverErrorLogWriter{log: logger.UtilLog}, "", 0)
}
