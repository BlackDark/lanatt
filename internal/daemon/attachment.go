package daemon

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"lanatt/internal/claims"
	"lanatt/internal/wire"
)

// attachment is an attach in progress in one netns. A stop for its container sets stopped.
type attachment struct {
	uid     uint32
	id      string
	plans   []claims.Planned
	stopped bool
}

// errCancelled means a stop arrived while the attach was joining. The attach rolls back.
var errCancelled = errors.New("attach cancelled by a stop for the same container")

// errTimedOut means the attach ran past its budget. The hook's socket deadline is 30s, and a
// join can take DAD time, so no new join starts after attachBudget.
var errTimedOut = errors.New("attach ran past its time budget")

// attachBudget bounds when a join may start. A join is one DAD wait plus a few ip calls, so one
// that starts in budget normally ends well before the 30s deadline.
const attachBudget = 20 * time.Second

func (s *Server) attach(uid uint32, req wire.Request) error {
	// Root may already create a macvlan anywhere, so a root caller is not bound to its own pids.
	t, err := openTarget(uid, req.Pid, uid == 0)
	if err != nil {
		return err
	}
	defer t.ns.Close()
	plans, err := s.reserve(uid, req, t)
	if err != nil {
		return err
	}
	// Runs after every device of the attach is undone, so each address stays held until then.
	// Success releases in complete instead, under the same lock as its check.
	released := false
	defer func() {
		if !released {
			s.finish(t.netns)
		}
	}()
	// Publish claims only after every join succeeds. Failed joins use their recorded intent
	// for rollback.
	until := time.Now().Add(attachBudget)
	var joined []joinedPlan
	for _, p := range plans {
		if err := s.checkJoin(p, until); err != nil {
			return s.abort(t.ns, joined, err)
		}
		rec, err := s.intend(p)
		if err != nil {
			return s.abort(t.ns, joined, err)
		}
		undo, err := join(t.ns, s.cfg.Networks[p.Network], p, rec.Temp, rec.Token)
		// Recorded even when join failed: its device may exist and must be found by token.
		joined = append(joined, joinedPlan{undo: undo, rec: rec})
		if err != nil {
			return s.abort(t.ns, joined, fmt.Errorf("network %q: %w", p.Network, err))
		}
	}
	if err := s.complete(t.netns); err != nil {
		return s.abort(t.ns, joined, err)
	}
	released = true
	return nil
}

// complete ends an attach that joined everything: its records go in the same save that makes its
// claims final. A stop that landed since the last join cancels it instead. A refused save leaves
// the records, and the caller aborts. The check, save and release share one lock, so a stop is
// either before the success or after it.
func (s *Server) complete(netns uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.busy[netns]
	if a == nil || a.stopped {
		return errCancelled
	}
	next := s.state.Add(a.plans)
	for _, p := range a.plans {
		next = next.RemovePending(p.Netns, p.Network)
	}
	if err := s.commit(next); err != nil {
		return err
	}
	delete(s.busy, netns)
	return nil
}

// reserve plans req under mu and holds the new joins as pending. One netns runs one attach at a
// time, so a retry cannot see a half-made attachment as already in place.
func (s *Server) reserve(uid uint32, req wire.Request, t target) ([]claims.Planned, error) {
	s.mu.Lock()
	conflicts, err := s.admitLocked(req, t.netns)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if len(conflicts) > 0 {
		s.mu.Unlock()
		if err := s.retire(conflicts); err != nil {
			return nil, err
		}
		return s.reserve(uid, req, t)
	}
	defer s.mu.Unlock()
	return s.planLocked(uid, req, t)
}

// admitLocked checks what planning needs: the netns is free, the state is saved, and it returns
// the records req conflicts with. A conflicting record on a busy netns refuses req. Caller holds mu.
func (s *Server) admitLocked(req wire.Request, netns uint64) ([]claims.Pending, error) {
	if s.busy[netns] != nil {
		return nil, errors.New("an attach to this netns is already in progress")
	}
	if s.degraded {
		if err := s.save(); err != nil {
			return nil, fmt.Errorf("state not saved, refusing attaches: %w", err)
		}
	}
	// A netns inode can be reused, so a dead workload's claim must not look like this one's.
	if err := s.commit(prune(s.state, s.boot)); err != nil {
		return nil, err
	}
	var conflicts []claims.Pending
	for _, rec := range s.state.Pending {
		if !s.conflicts(rec, req, netns) {
			continue
		}
		if s.retiring[rec.Token] {
			return nil, fmt.Errorf("cleanup pending for %s: removal in progress", claims.ShortID(rec.ID))
		}
		if s.busy[rec.Netns] != nil {
			return nil, fmt.Errorf("cleanup pending for %s: an attach to its netns is in progress", claims.ShortID(rec.ID))
		}
		conflicts = append(conflicts, rec)
	}
	return conflicts, nil
}

// planLocked plans req and holds its joins as pending. Caller holds mu.
func (s *Server) planLocked(uid uint32, req wire.Request, t target) ([]claims.Planned, error) {
	var reserved []claims.Planned
	for _, a := range s.busy {
		reserved = append(reserved, a.plans...)
	}
	plans, err := claims.Plan(s.state.Add(reserved), s.cfg.Networks, uid, req.ID, t.netns, req.Joins)
	if err != nil {
		return nil, err
	}
	plans = claims.Own(plans, claims.Owner{BootID: s.boot, Pid: req.Pid, Start: t.start})
	var fresh []claims.Planned
	for _, p := range plans {
		if !p.Existing {
			fresh = append(fresh, p)
		}
	}
	if s.busy == nil {
		s.busy = map[uint64]*attachment{}
	}
	s.busy[t.netns] = &attachment{uid: uid, id: req.ID, plans: fresh}
	return fresh, nil
}

// checkJoin refuses a join that a stop cancelled or that starts past the attach's budget.
func (s *Server) checkJoin(p claims.Planned, until time.Time) error {
	s.mu.Lock()
	active := s.active(p)
	s.mu.Unlock()
	if !active {
		return errCancelled
	}
	if time.Now().After(until) {
		return errTimedOut
	}
	return nil
}

// active reports whether p's attach is still running and not stopped, and p is still reserved.
// Caller holds mu.
func (s *Server) active(p claims.Planned) bool {
	a := s.busy[p.Netns]
	return a != nil && !a.stopped && slices.Contains(a.plans, p)
}

// finish ends an attach: its netns is free again and its reservations are dropped.
func (s *Server) finish(netns uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, netns)
}

func (s *Server) detach(uid uint32, id string) error {
	s.mu.Lock()
	// Stop an attach still running for this container, including one that only repeats joins.
	// Its reservations stay until it finishes, since its devices are up until then.
	for _, a := range s.busy {
		if a.uid == uid && a.id == id {
			a.stopped = true
		}
	}
	// Records of an attach still running are left to that attach.
	if err := s.commit(s.state.Drop(uid, id)); err != nil {
		s.mu.Unlock()
		return err
	}
	var recs []claims.Pending
	for _, rec := range s.state.Pending {
		if rec.UID == uid && rec.ID == id && s.busy[rec.Netns] == nil {
			recs = append(recs, rec)
		}
	}
	s.mu.Unlock()
	return s.retire(recs)
}
