// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package x2x3

import "syscall"

// capBuffer leaves the socket's buffers as the platform sizes them; see the unix variant.
func capBuffer(bool) func(network, address string, rc syscall.RawConn) error { return nil }
