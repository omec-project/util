// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// The interrogation answers are five views of two collections, and their worth to a
// provisioning function depends entirely on their agreeing.
//
// An element that reports a task under GetAllDetails and omits it from GetAllTaskDetails is
// worse than one that answers neither: the ADMF cannot tell which answer to believe, and
// nothing outside the element could diagnose the divergence. The same goes for a destination
// reported one way in the list and another way on its own.
//
// They agree today by construction — every task goes through taskResponseDetails and every
// destination through destinationResponseDetails. That is what this test is for: it does not
// prove the current code right so much as make the next answer, or the next rendering path
// given to an existing one, have to keep agreeing.

// namedBlocks returns each <ns1:name>…</ns1:name> block in doc, with per-line indentation
// removed so that identical content nested at different depths compares equal. It has to:
// GetTaskDetails renders a task two levels shallower than GetAllDetails does, and that is a
// difference in position, not in what is being said about the task.
func namedBlocks(doc, name string) []string {
	opening, closing := "<ns1:"+name+">", "</ns1:"+name+">"

	var out []string
	for rest := doc; ; {
		i := strings.Index(rest, opening)
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, closing)
		if j < 0 {
			return out
		}

		lines := strings.Split(rest[:j+len(closing)], "\n")
		for k, l := range lines {
			lines[k] = strings.TrimSpace(l)
		}
		out = append(out, strings.Join(lines, "\n"))
		rest = rest[j+len(closing):]
	}
}

func TestInterrogationAnswersDescribeTheSameState(t *testing.T) {
	const (
		taskBlock = "taskResponseDetails"
		destBlock = "destinationResponseDetails"
	)

	st := store.New()
	srv := NewServer(st, "neID")
	srv.now = func() time.Time { return zeroTailInstant }

	// Two of each, not one: a renderer that emits only the first entry passes every
	// single-item comparison. The answers are ordered — tasks by XID, destinations by DID —
	// so comparing them as sequences is deterministic rather than incidentally lucky.
	tasks := []struct {
		xid    types.XID
		target string
	}{
		{"11111111-1111-4111-8111-111111111111", "262019876543210"},
		{"22222222-2222-4222-8222-222222222222", "262019876543211"},
	}
	for _, task := range tasks {
		st.Activate(types.InterceptTask{
			XID:      task.xid,
			Targets:  []types.TargetIdentifier{{Type: types.TargetSUPI, Value: task.target}},
			Products: []types.ProductType{types.ProductIRI},
		})
	}
	dests := []struct{ did, addr string }{
		{testDID, "10.0.60.122:42069"},
		{"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "10.0.60.123:42069"},
	}
	for _, d := range dests {
		srv.destinations[d.did] = heldDestination{DeliveryType: deliveryX2Only, Address: d.addr}
	}

	answer := func(msgType, extra string) string {
		t.Helper()
		resp, err := srv.Process(request(msgType, extra), admfPeer(t))
		if err != nil {
			t.Fatalf("%s: %v", msgType, err)
		}
		out, err := marshalResponse(resp)
		if err != nil {
			t.Fatalf("%s: marshalResponse: %v", msgType, err)
		}

		return string(out)
	}

	// GetAllDetails is the reference, because it is the answer that reports everything and
	// the one the other projections were extracted from.
	all := answer("GetAllDetailsRequest", "")

	allTasks := namedBlocks(all, taskBlock)
	if len(allTasks) != len(tasks) {
		t.Fatalf("GetAllDetails reports %d task(s), want %d", len(allTasks), len(tasks))
	}
	allDests := namedBlocks(all, destBlock)
	if len(allDests) != len(dests) {
		t.Fatalf("GetAllDetails reports %d destination(s), want %d", len(allDests), len(dests))
	}

	// The whole-collection answers.
	if got := namedBlocks(answer("GetAllTaskDetailsRequest", ""), taskBlock); !slices.Equal(got, allTasks) {
		t.Errorf("GetAllTaskDetails and GetAllDetails describe the tasks differently\n"+
			"GetAllTaskDetails:\n%s\n\nGetAllDetails:\n%s",
			strings.Join(got, "\n"), strings.Join(allTasks, "\n"))
	}
	if got := namedBlocks(answer("GetAllDestinationDetailsRequest", ""), destBlock); !slices.Equal(got, allDests) {
		t.Errorf("GetAllDestinationDetails and GetAllDetails describe the destinations differently\n"+
			"GetAllDestinationDetails:\n%s\n\nGetAllDetails:\n%s",
			strings.Join(got, "\n"), strings.Join(allDests, "\n"))
	}

	// The single-entry answers, each of which must be one entry of the corresponding list.
	got := namedBlocks(answer("GetTaskDetailsRequest", "\n    <ns1:xId>"+string(tasks[0].xid)+"</ns1:xId>"), taskBlock)
	if len(got) != 1 || got[0] != allTasks[0] {
		t.Errorf("GetTaskDetails describes a task differently from GetAllDetails\n"+
			"GetTaskDetails:\n%s\n\nGetAllDetails:\n%s",
			strings.Join(got, "\n"), allTasks[0])
	}
	got = namedBlocks(answer("GetDestinationDetailsRequest", "\n    <ns1:dId>"+dests[0].did+"</ns1:dId>"), destBlock)
	if len(got) != 1 || got[0] != allDests[0] {
		t.Errorf("GetDestinationDetails describes a destination differently from GetAllDetails\n"+
			"GetDestinationDetails:\n%s\n\nGetAllDetails:\n%s",
			strings.Join(got, "\n"), allDests[0])
	}

	// ListAllDetails reports identifiers rather than details, so it agrees by naming exactly
	// what the detail answers described — no more, since an identifier with no details behind
	// it would send an ADMF asking about a task this element does not hold.
	list := answer("ListAllDetailsRequest", "")
	for _, task := range tasks {
		if !strings.Contains(list, string(task.xid)) {
			t.Errorf("ListAllDetails omits %s, which the detail answers report", task.xid)
		}
	}
	for _, d := range dests {
		if !strings.Contains(list, d.did) {
			t.Errorf("ListAllDetails omits %s, which the detail answers report", d.did)
		}
	}
	if n := strings.Count(list, "<ns1:xId>"); n != len(tasks) {
		t.Errorf("ListAllDetails names %d task identifier(s), want %d", n, len(tasks))
	}
	if n := strings.Count(list, "<ns1:dId>"); n != len(dests) {
		t.Errorf("ListAllDetails names %d destination identifier(s), want %d", n, len(dests))
	}
}
