// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package x2x3

import "syscall"

// cappedBuffer is the kernel buffer the partial-write tests give each socket. Left to
// autotuning, Linux loopback grants megabytes, enough to take a whole test batch, so a peer
// that stops reading never stalls the write and there is no partial write to examine.
const cappedBuffer = 64 * 1024

// capBuffer returns a dialer or listener Control that caps the socket's send buffer (send) or
// receive buffer (!send) at cappedBuffer.
func capBuffer(send bool) func(network, address string, rc syscall.RawConn) error {
	opt := syscall.SO_RCVBUF
	if send {
		opt = syscall.SO_SNDBUF
	}

	return func(_, _ string, rc syscall.RawConn) error {
		var setErr error
		if err := rc.Control(func(fd uintptr) {
			setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, opt, cappedBuffer)
		}); err != nil {
			return err
		}

		return setErr
	}
}
