package daemon

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"lanatt/internal/claims"
)

// joinedPlan is a join this attach made, with what it takes to undo its default routes and the
// record that finds its device.
type joinedPlan struct {
	undo defaultUndo
	rec  claims.Pending
}

// intend records a device before it is created, so a crash or failed removal still finds it.
// A refused save creates nothing.
func (s *Server) intend(p claims.Planned) (claims.Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active(p) {
		return claims.Pending{}, errCancelled
	}
	token, err := freeToken(s.state.Pending)
	if err != nil {
		return claims.Pending{}, err
	}
	rec := claims.Pending{Claim: p.Claim, Token: token, Temp: "lt" + token[:12]}
	return rec, s.commit(s.state.AddPending(rec))
}

// abort undoes a failed attach newest first. Each intent is already durable, and complete
// is the only path that publishes claims. The active attach owns its records during rollback.
func (s *Server) abort(ns *os.File, joined []joinedPlan, cause error) error {
	errs := []error{cause}
	for i := len(joined) - 1; i >= 0; i-- {
		j := joined[i]
		if err := j.undo.restore(ns); err != nil {
			errs = append(errs, err)
		}
		gone, err := s.removeDevice(j.rec, ns)
		if !gone {
			errs = append(errs, fmt.Errorf("cleanup pending for %s: %w", claims.ShortID(j.rec.ID), err))
			continue
		}
		if err := s.dropRecord(j.rec); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dropRecord forgets rec once its device is verified gone. Takes mu.
func (s *Server) dropRecord(rec claims.Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitForced(s.state.RemoveToken(rec.Token))
}

// retire removes each record's device and drops the record once its removal is verified. A
// record it could not verify keeps its record. Takes mu per record, not around the kernel work.
func (s *Server) retire(recs []claims.Pending) error {
	var errs []error
	for _, rec := range recs {
		started, err := s.claimRetire(rec)
		if err != nil {
			errs = append(errs, err)
		}
		if !started {
			continue
		}
		gone, err := s.removeDevice(rec, nil)
		if !gone {
			errs = append(errs, fmt.Errorf("cleanup pending for %s: %w", claims.ShortID(rec.ID), err))
		} else if err := s.dropRecord(rec); err != nil {
			errs = append(errs, err)
		}
		s.releaseRetire(rec)
	}
	return errors.Join(errs...)
}

// claimRetire rechecks the record after the caller releases mu to start cleanup.
func (s *Server) claimRetire(rec claims.Pending) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.state.Pending, rec) {
		return false, nil
	}
	if s.retiring[rec.Token] || s.busy[rec.Netns] != nil {
		return false, fmt.Errorf("cleanup pending for %s: attachment or removal in progress", claims.ShortID(rec.ID))
	}
	if s.retiring == nil {
		s.retiring = map[string]bool{}
	}
	s.retiring[rec.Token] = true
	return true, nil
}

// releaseRetire ends the removal claimRetire marked.
func (s *Server) releaseRetire(rec claims.Pending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.retiring, rec.Token)
}

// removeDevice reports whether rec's device is verified absent, using ns as its netns if given.
// A record from an earlier boot is retired: its netns and devices went with the reboot.
func (s *Server) removeDevice(rec claims.Pending, ns *os.File) (bool, error) {
	if rec.BootID != s.boot {
		return true, nil
	}
	if s.remove != nil {
		return s.remove(rec)
	}
	return removeOwned(rec, ns)
}

// commitForced is commit for a change whose devices already moved: memory always takes it. A
// refused save leaves the disk behind memory, so admissions wait until a save succeeds.
func (s *Server) commitForced(next claims.State) error {
	s.state = next
	return s.save()
}

// save writes the state. A failure marks the server degraded until a later save succeeds.
func (s *Server) save() error {
	if err := claims.Save(s.cfg.State, s.state); err != nil {
		s.degraded = true
		return err
	}
	s.degraded = false
	return nil
}

// recoverPending retries every record at start. Records it cannot verify stay for the next
// attach or stop that conflicts with them.
func (s *Server) recoverPending() error {
	return s.retire(s.state.Pending)
}
