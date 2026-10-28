// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

// Package store maintains the set of active Lawful Interception tasks for a
// network function, indexed by target identifier. It lets the NF find the tasks
// that may concern a subject locally, with no external lookup at interception time.
package store

import (
	"slices"
	"strings"
	"sync"

	"github.com/omec-project/util/li/types"
)

// Store is a concurrency-safe set of active interception tasks. The zero value
// is not usable; construct with New.
type Store struct {
	mu       sync.RWMutex
	byXID    map[types.XID]types.InterceptTask
	byTarget map[types.TargetIdentifier]map[types.XID]struct{}
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		byXID:    make(map[types.XID]types.InterceptTask),
		byTarget: make(map[types.TargetIdentifier]map[types.XID]struct{}),
	}
}

// Activate adds or replaces a task (X1 ActivateTask / ModifyTask). It re-indexes
// by target so a ModifyTask that changes the target identifier is handled
// correctly. A task with an empty XID is rejected and reported false.
func (s *Store) Activate(task types.InterceptTask) bool {
	if task.XID == "" {
		return false
	}
	// Cloned on the way in as well as on the way out: a store sharing backing arrays
	// with whoever handed the task over lets that caller mutate a warrant's targets
	// outside the lock, which is a data race and cross-warrant corruption. Whether a
	// caller keeps a reference is not something the store can see.
	task = cloneTask(task)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.unindex(task.XID) // drop any prior indexing for this XID
	task.State = types.TaskActive
	s.byXID[task.XID] = task
	// Indexed under every one of its identifiers, so Match finds the task by any of
	// them. A task repeating an identifier indexes once under it: the index is a set.
	for _, id := range task.Targets {
		set := s.byTarget[id]
		if set == nil {
			set = make(map[types.XID]struct{})
			s.byTarget[id] = set
		}
		set[task.XID] = struct{}{}
	}

	return true
}

// Deactivate removes a task (X1 DeactivateTask). After this the task produces no
// further intercept product. Reports whether the task existed.
func (s *Store) Deactivate(xid types.XID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byXID[xid]; !ok {
		return false
	}
	s.unindex(xid)
	delete(s.byXID, xid)
	return true
}

// unindex removes every target-index entry for xid using its currently stored
// task. It reads the identifiers from the stored task rather than from a caller's
// copy, so a ModifyTask that changes the criteria cannot leave the task indexed
// under an identifier it no longer has — which would keep interception running on
// superseded criteria. The caller must hold the write lock.
func (s *Store) unindex(xid types.XID) {
	prev, ok := s.byXID[xid]
	if !ok {
		return
	}
	for _, id := range prev.Targets {
		set := s.byTarget[id]
		if set == nil {
			continue
		}
		delete(set, xid)
		if len(set) == 0 {
			delete(s.byTarget, id)
		}
	}
}

// DeactivateAll removes every active task and its target index. It backs the bulk
// purges: X1 DeactivateAllTasks, and the clause 6.6.2 keepalive procedure, under which
// the NE deactivates all tasking when the controlling ADMF goes silent, so warrants
// never outlive an operational controller. It reports nothing per task; a caller that
// must undo each task's side effects takes a Snapshot first.
func (s *Store) DeactivateAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.byXID)
	clear(s.byTarget)
}

// Get returns the active task for an XID.
func (s *Store) Get(xid types.XID) (types.InterceptTask, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byXID[xid]
	return cloneTask(t), ok
}

// cloneTask copies every slice field of t (Targets, TargetElements, DIDs, Products,
// Deliveries), so neither the store nor its caller shares backing arrays with the other.
// The value fields are copied by the return-by-value. A nil slice stays nil, and so does
// an empty non-nil one: the copy of nothing is nil, so a caller must test len, not nil.
// A slice field added to InterceptTask has to be added here too;
// TestEveryReferenceFieldIsCloned enforces that.
func cloneTask(t types.InterceptTask) types.InterceptTask {
	if t.Products != nil {
		t.Products = append([]types.ProductType(nil), t.Products...)
	}
	if t.Deliveries != nil {
		t.Deliveries = append([]types.DeliveryEndpoint(nil), t.Deliveries...)
	}
	if t.Targets != nil {
		t.Targets = append([]types.TargetIdentifier(nil), t.Targets...)
	}
	if t.DIDs != nil {
		t.DIDs = append([]string(nil), t.DIDs...)
	}
	if t.TargetElements != nil {
		t.TargetElements = append([]string(nil), t.TargetElements...)
	}
	return t
}

// Snapshot returns a copy of every active task. It backs the bulk purges: the
// caller reports each task's removal (x1's OnTaskChange and OnPurge) over the snapshot
// so a purge undoes each task's side effects (e.g. UPF CC duplication), not just the
// store entries. The result is a fresh slice usable without holding any lock, ordered
// by XID (see Match on why the order is fixed rather than a map's).
func (s *Store) Snapshot() []types.InterceptTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.InterceptTask, 0, len(s.byXID))
	for _, task := range s.byXID {
		out = append(out, cloneTask(task))
	}
	sortByXID(out)
	return out
}

// Match returns the active tasks naming the given identifier among their Targets,
// ordered by XID, once each however many of its Targets equal it. The result is a
// fresh slice the caller may use without holding any lock; nil if no task names it.
//
// It is an index lookup, not a match decision. A task's criteria are conjunctive, so a
// task naming this identifier and another is the subject's only if the subject also
// presents the other: a caller deciding a subject passes each returned task through
// InterceptTask.MatchedBy with every identifier the subject holds.
//
// The order is fixed deliberately. Both indexes are maps, so an unsorted result
// varies between calls on the same set of tasks, and a caller that acts on one
// element of it — a triggered CC-POI choosing which warrant a duplicated packet
// belongs to, say — would attribute successive packets of one session to different
// warrants at random, splitting the product so that no agency receives a complete
// stream. Callers that use every element are unaffected either way.
func (s *Store) Match(id types.TargetIdentifier) []types.InterceptTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.byTarget[id]
	if len(set) == 0 {
		return nil
	}
	out := make([]types.InterceptTask, 0, len(set))
	for xid := range set {
		out = append(out, cloneTask(s.byXID[xid]))
	}
	sortByXID(out)
	return out
}

// sortByXID orders tasks by their X1 identifier, which is unique per task and so
// a total order.
func sortByXID(tasks []types.InterceptTask) {
	slices.SortFunc(tasks, func(a, b types.InterceptTask) int {
		return strings.Compare(string(a.XID), string(b.XID))
	})
}

// Len returns the number of active tasks.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byXID)
}
