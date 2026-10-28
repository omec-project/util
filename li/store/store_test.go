// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"testing"

	"github.com/omec-project/util/li/types"
)

func supi(v string) types.TargetIdentifier {
	return types.TargetIdentifier{Type: types.TargetSUPI, Value: v}
}

// TestMultiAgencyIsolation checks that two agencies tasking the same target
// under distinct warrants (XIDs) coexist, are matched together, and are removed
// independently — one agency's activity never affects the other's.
func TestMultiAgencyIsolation(t *testing.T) {
	st := New()
	target := supi("262019876543210")
	a := types.InterceptTask{XID: "agency-a", Targets: []types.TargetIdentifier{target}, Products: []types.ProductType{types.ProductIRI}}
	b := types.InterceptTask{XID: "agency-b", Targets: []types.TargetIdentifier{target}, Products: []types.ProductType{types.ProductIRI, types.ProductCC}}
	if !st.Activate(a) || !st.Activate(b) {
		t.Fatal("activate failed")
	}

	matches := st.Match(target)
	if len(matches) != 2 {
		t.Fatalf("Match returned %d tasks, want 2 (both agencies)", len(matches))
	}
	seen := map[types.XID]bool{}
	for _, m := range matches {
		seen[m.XID] = true
	}
	if !seen["agency-a"] || !seen["agency-b"] {
		t.Errorf("Match XIDs = %v, want both agency-a and agency-b", seen)
	}

	// Deactivating agency A must leave agency B's warrant fully intact.
	if !st.Deactivate("agency-a") {
		t.Fatal("deactivate agency-a failed")
	}
	after := st.Match(target)
	if len(after) != 1 || after[0].XID != "agency-b" {
		t.Errorf("after deactivating agency-a, Match = %+v, want only agency-b", after)
	}
	if _, ok := st.Get("agency-b"); !ok {
		t.Error("agency-b warrant lost when agency-a was deactivated")
	}
}

// TestMatchReturnsIsolatedSlices checks that a caller mutating a returned task's
// Products/Deliveries slices cannot corrupt the store's backing arrays.
func TestMatchReturnsIsolatedSlices(t *testing.T) {
	st := New()
	target := supi("262019876543210")
	st.Activate(types.InterceptTask{
		XID: "a", Targets: []types.TargetIdentifier{target},
		Products:   []types.ProductType{types.ProductIRI},
		Deliveries: []types.DeliveryEndpoint{{Type: types.DeliveryX2, Address: "mdf2:1"}},
	})

	got := st.Match(target)[0]
	got.Products[0] = types.ProductCC
	got.Products = append(got.Products, types.ProductCC)
	got.Deliveries[0].Address = "attacker:0"

	after := st.Match(target)[0]
	if len(after.Products) != 1 || after.Products[0] != types.ProductIRI {
		t.Errorf("store Products mutated via returned slice: %v", after.Products)
	}
	if after.Deliveries[0].Address != "mdf2:1" {
		t.Errorf("store Deliveries mutated via returned slice: %v", after.Deliveries)
	}
}

func TestActivateMatchDeactivate(t *testing.T) {
	s := New()
	target := supi("imsi-001010000000001")
	task := types.InterceptTask{
		XID:      "W1",
		Targets:  []types.TargetIdentifier{target},
		Products: []types.ProductType{types.ProductIRI, types.ProductCC},
	}

	if !s.Activate(task) {
		t.Fatal("Activate returned false for a valid task")
	}

	got := s.Match(target)
	if len(got) != 1 || got[0].XID != "W1" {
		t.Fatalf("Match(target) = %+v, want one task W1", got)
	}
	if got[0].State != types.TaskActive {
		t.Errorf("task state = %q, want %q", got[0].State, types.TaskActive)
	}
	if !got[0].WantsProduct(types.ProductCC) {
		t.Errorf("task should want CC product")
	}

	if m := s.Match(supi("other")); m != nil {
		t.Errorf("Match of non-target returned %+v, want nil", m)
	}

	if !s.Deactivate("W1") {
		t.Fatal("Deactivate returned false for an active task")
	}
	if m := s.Match(target); m != nil {
		t.Errorf("Match after Deactivate returned %+v, want nil", m)
	}
	if s.Len() != 0 {
		t.Errorf("Len after Deactivate = %d, want 0", s.Len())
	}
}

func TestActivateRejectsEmptyXID(t *testing.T) {
	s := New()
	if s.Activate(types.InterceptTask{Targets: []types.TargetIdentifier{supi("x")}}) {
		t.Error("Activate accepted a task with an empty XID")
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
}

func TestModifyTaskRetargets(t *testing.T) {
	s := New()
	oldT, newT := supi("old"), supi("new")
	s.Activate(types.InterceptTask{XID: "W1", Targets: []types.TargetIdentifier{oldT}})

	// X1 ModifyTask: same XID, different target.
	s.Activate(types.InterceptTask{XID: "W1", Targets: []types.TargetIdentifier{newT}})

	if m := s.Match(oldT); m != nil {
		t.Errorf("old target still matches after retarget: %+v", m)
	}
	if m := s.Match(newT); len(m) != 1 {
		t.Errorf("new target Match = %+v, want one task", m)
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1 (modify must not duplicate)", s.Len())
	}
}

func TestDeactivateUnknownXID(t *testing.T) {
	s := New()
	if s.Deactivate("nope") {
		t.Error("Deactivate of unknown XID returned true")
	}
}

// TestMatchOrderIsStable pins the ordering guarantee Match documents. Both store
// indexes are maps, so before this the result order varied between calls on an
// unchanged store — and a caller that acts on one element of it (the triggered
// CC-POI picking which warrant a duplicated packet belongs to) attributed
// successive packets of one session to different warrants at random, so that
// neither agency received a complete stream.
func TestMatchOrderIsStable(t *testing.T) {
	s := New()
	target := types.TargetIdentifier{Type: types.TargetSUPI, Value: "001010000000001"}
	for _, xid := range []types.XID{"c", "a", "d", "b"} {
		s.Activate(types.InterceptTask{XID: xid, Targets: []types.TargetIdentifier{target}})
	}

	want := []types.XID{"a", "b", "c", "d"}
	// Repeat: a single pass can agree with the expected order by luck.
	for i := range 20 {
		got := s.Match(target)
		if len(got) != len(want) {
			t.Fatalf("pass %d: matched %d tasks, want %d", i, len(got), len(want))
		}
		for j, xid := range want {
			if got[j].XID != xid {
				t.Fatalf("pass %d: Match()[%d] = %q, want %q", i, j, got[j].XID, xid)
			}
		}
	}

	snap := s.Snapshot()
	for j, xid := range want {
		if snap[j].XID != xid {
			t.Fatalf("Snapshot()[%d] = %q, want %q", j, snap[j].XID, xid)
		}
	}
}

// ipv4 builds a UE IPv4 address criterion, one of the LI_T3 packet detection
// criteria of TS 33.128 table 6.2.3-7.
func ipv4(v string) types.TargetIdentifier {
	return types.TargetIdentifier{Type: types.TargetUEIPv4, Value: v}
}

// TestEachCriterionIndexesTheTask: a task carrying several criteria is found by each of
// them, exactly once by each. Match is the index lookup that finds the candidates; the
// criteria are conjunctive, and InterceptTask.MatchedBy decides whether a candidate is
// the subject's.
func TestEachCriterionIndexesTheTask(t *testing.T) {
	s := New()
	seid := types.TargetIdentifier{Type: types.TargetFSEID, Value: "14426627323429955319"}
	addr := ipv4("10.250.0.9")
	if !s.Activate(types.InterceptTask{XID: "W1", Targets: []types.TargetIdentifier{seid, addr}}) {
		t.Fatal("activate failed")
	}

	for _, id := range []types.TargetIdentifier{seid, addr} {
		m := s.Match(id)
		if len(m) != 1 || m[0].XID != "W1" {
			t.Errorf("Match(%+v) = %+v, want the one task W1", id, m)
		}
	}
	if m := s.Match(ipv4("10.250.0.10")); m != nil {
		t.Errorf("Match on an untasked address = %+v, want nil", m)
	}

	// Every index entry must go, or the task keeps intercepting on a criterion it
	// no longer has.
	s.Deactivate("W1")
	for _, id := range []types.TargetIdentifier{seid, addr} {
		if m := s.Match(id); m != nil {
			t.Errorf("Match(%+v) after Deactivate = %+v, want nil", id, m)
		}
	}
}

// TestModifyNarrowsCriteria checks that dropping a criterion in a ModifyTask stops
// interception on it. Table 6.2.3-8 permits the criteria to change mid-task, and a
// stale index entry would keep collecting on superseded criteria — beyond what the
// triggering function now asks for.
func TestModifyNarrowsCriteria(t *testing.T) {
	s := New()
	kept, dropped := ipv4("10.250.0.9"), ipv4("10.250.0.10")
	s.Activate(types.InterceptTask{XID: "W1", Targets: []types.TargetIdentifier{kept, dropped}})
	s.Activate(types.InterceptTask{XID: "W1", Targets: []types.TargetIdentifier{kept}})

	if m := s.Match(kept); len(m) != 1 {
		t.Errorf("retained criterion Match = %+v, want the task", m)
	}
	if m := s.Match(dropped); m != nil {
		t.Errorf("dropped criterion still matches: %+v", m)
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1 — the modify must not duplicate the task", s.Len())
	}
}

// TestACandidateIsTheSubjectsOnlyWhenEveryCriterionHolds: Match finds a task by any one
// of its criteria, and the conjunction InterceptTask.Targets documents is then decided by
// MatchedBy. A subject presenting one of two criteria is found as a candidate and is not
// the task's subject.
func TestACandidateIsTheSubjectsOnlyWhenEveryCriterionHolds(t *testing.T) {
	s := New()
	both := types.InterceptTask{XID: "W1", Targets: []types.TargetIdentifier{supi("262019876543210"), ipv4("10.250.0.9")}}
	if !s.Activate(both) {
		t.Fatal("activate failed")
	}

	onlyAddress := []types.TargetIdentifier{ipv4("10.250.0.9"), supi("111111111111111")}
	candidates := s.Match(ipv4("10.250.0.9"))
	if len(candidates) != 1 {
		t.Fatalf("Match = %+v, want the one candidate", candidates)
	}
	if candidates[0].MatchedBy(onlyAddress) {
		t.Error("a subject presenting one of the task's two criteria was decided the task's subject")
	}
	if !candidates[0].MatchedBy([]types.TargetIdentifier{ipv4("10.250.0.9"), supi("262019876543210")}) {
		t.Error("a subject presenting both criteria was not decided the task's subject")
	}
}

// TestDeactivateAllEmptiesTheStoreAndItsIndex: the bulk purge must remove the target
// index along with the tasks. A purge that cleared only the tasks would leave Match
// answering from the index for tasks that no longer exist, and one that cleared only
// the index would leave Get and Snapshot reporting tasking the ADMF has withdrawn.
func TestDeactivateAllEmptiesTheStoreAndItsIndex(t *testing.T) {
	st := New()
	target := supi("262019876543210")
	for _, xid := range []types.XID{"a", "b"} {
		if !st.Activate(types.InterceptTask{XID: xid, Targets: []types.TargetIdentifier{target}}) {
			t.Fatalf("activate %s failed", xid)
		}
	}

	st.DeactivateAll()

	if n := st.Len(); n != 0 {
		t.Errorf("Len() = %d after DeactivateAll, want 0", n)
	}
	if got := st.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot() = %+v after DeactivateAll, want none", got)
	}
	if _, ok := st.Get("a"); ok {
		t.Error("Get found a task after DeactivateAll")
	}
	if len(st.byTarget) != 0 {
		t.Errorf("the target index holds %d entries after DeactivateAll, want 0", len(st.byTarget))
	}

	// The store stays usable: a task activated after the purge is found by its target.
	if !st.Activate(types.InterceptTask{XID: "c", Targets: []types.TargetIdentifier{target}}) {
		t.Fatal("activate after DeactivateAll failed")
	}
	if got := st.Match(target); len(got) != 1 || got[0].XID != "c" {
		t.Errorf("Match after re-activation = %+v, want only c", got)
	}
}
