// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"fmt"
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// containerOf is one X1Request carrying a message of each type given, each with only the
// header.
func containerOf(msgTypes ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<ns1:X1Request xmlns:ns1="http://uri.etsi.org/03221/X1/2017/10" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`)
	for i, msgType := range msgTypes {
		fmt.Fprintf(&b, `<ns1:x1RequestMessage xsi:type="ns1:%s"><ns1:admfIdentifier>admfID</ns1:admfIdentifier>`+
			`<ns1:neIdentifier>neID</ns1:neIdentifier><ns1:messageTimestamp>2026-01-01T00:00:00.000000Z</ns1:messageTimestamp>`+
			`<ns1:version>v1.6.1</ns1:version><ns1:x1TransactionId>aaaaaaaa-aaaa-4aaa-8aaa-%012d</ns1:x1TransactionId>`+
			`</ns1:x1RequestMessage>`, msgType, i)
	}
	b.WriteString(`</ns1:X1Request>`)

	return []byte(b.String())
}

// repeated is msgType n times.
func repeated(msgType string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = msgType
	}

	return out
}

// tasked is a store holding n tasks.
func tasked(n int) *store.Store {
	st := store.New()
	for i := range n {
		st.Activate(types.InterceptTask{
			XID:     types.XID(fmt.Sprintf("50b93d1e-1b53-4d63-aacb-%012d", i)),
			Targets: []types.TargetIdentifier{supiTarget(fmt.Sprintf("%015d", i))},
		})
	}

	return st
}

// TestAContainerOfInterrogationsIsBounded: one authenticated container of three thousand
// GetAllDetails against an element holding two thousand tasks asks for six million task
// entries — gigabytes, built before a byte is written. The answers stop projecting state once
// the container has projected maxProjectedEntries, and every message after that is refused
// with 1000 rather than dropped, so the ADMF still has an answer per request.
func TestAContainerOfInterrogationsIsBounded(t *testing.T) {
	const tasks, messages = 2000, 3000

	srv := testServer(tasked(tasks))
	resp, err := srv.Process(containerOf(repeated("GetAllDetailsRequest", messages)...), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(resp.Messages) != messages {
		t.Fatalf("%d answers to %d messages", len(resp.Messages), messages)
	}

	projected, answered, refused := 0, 0, 0
	for _, m := range resp.Messages {
		projected += projectedEntries(m)
		switch {
		case m.ErrorInformation == nil:
			answered++
			if len(m.Tasks) != tasks {
				t.Fatalf("an answered GetAllDetails lists %d tasks, want all %d", len(m.Tasks), tasks)
			}
		case m.ErrorInformation.ErrorCode == errCodeGeneric && m.RequestType == "GetAllDetailsRequest":
			refused++
		default:
			t.Fatalf("unexpected answer: %+v", *m.ErrorInformation)
		}
	}

	if limit := maxProjectedEntries + tasks; projected > limit {
		t.Errorf("the container projected %d entries, want at most %d", projected, limit)
	}
	if answered == 0 || answered+refused != messages {
		t.Errorf("%d answered and %d refused of %d", answered, refused, messages)
	}
}

// TestTheFirstInterrogationIsAlwaysAnswered: the bound applies across a container, never to
// one answer. An ADMF must be able to audit an element however much it holds, so a single
// GetAllDetails is answered in full even when that alone exceeds the bound.
func TestTheFirstInterrogationIsAlwaysAnswered(t *testing.T) {
	tasks := maxProjectedEntries + 1

	resp, err := testServer(tasked(tasks)).Process(containerOf("GetAllTaskDetailsRequest"), admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if m := resp.Messages[0]; m.ErrorInformation != nil || len(m.Tasks) != tasks {
		t.Errorf("the only interrogation in its container was not answered in full: %d tasks, error %v",
			len(m.Tasks), m.ErrorInformation)
	}
}

// TestTheBoundRefusesOnlyInterrogations: a container whose answers have reached the bound still
// has its provisioning acted on. Only a request that would project more state is refused.
func TestTheBoundRefusesOnlyInterrogations(t *testing.T) {
	body := containerOf("GetAllTaskDetailsRequest", "GetAllTaskDetailsRequest", "KeepaliveRequest")

	resp, err := testServer(tasked(maxProjectedEntries)).Process(body, admfPeer(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("%d answers, want 3", len(resp.Messages))
	}
	if resp.Messages[1].ErrorInformation == nil {
		t.Error("the second interrogation was answered after the first reached the bound")
	}
	if ka := resp.Messages[2]; ka.ErrorInformation != nil || ka.OK != ackOK {
		t.Errorf("a keepalive after the bound was not acknowledged: %+v", ka)
	}
}
