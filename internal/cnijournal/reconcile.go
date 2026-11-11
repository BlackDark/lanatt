package cnijournal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Attach runs ADD under the journal and returns its result. Any failure after ADD ran
// leaves the record owing DEL, and DEL is attempted at once. If that DEL fails the record
// stays, and Reconcile retries it.
func (s *Store) Attach(ctx context.Context, run Runner, r Record) (json.RawMessage, error) {
	defer s.lock(r.UID, r.ContainerID)()
	r, err := s.Begin(r)
	if err != nil {
		return nil, err
	}
	res, addErr := run.Add(ctx, r)
	if addErr == nil {
		commitErr := s.commit(r.UID, r.ContainerID, res)
		if commitErr == nil {
			return res, nil
		}
		addErr = commitErr
		r.Result = res
	}
	// DEL must not be cut short by the ADD context, which may be what timed out.
	delCtx := context.WithoutCancel(ctx)
	return nil, errors.Join(addErr, s.fail(r.UID, r.ContainerID), s.finish(delCtx, run, r, false))
}

// Release undoes uid's attachment for containerID, if it has one. gone says the netns no longer exists.
func (s *Store) Release(ctx context.Context, run Runner, uid uint32, containerID string, gone bool) error {
	defer s.lock(uid, containerID)()
	r, ok, err := s.Get(uid, containerID)
	if err != nil || !ok {
		return err
	}
	if r.State == StateAdded {
		// Persist the owed DEL first, so a release interrupted by a crash is retried at startup.
		if err := s.fail(uid, containerID); err != nil {
			return err
		}
		r.State = StateCleanup
	}
	return s.finish(ctx, run, r, gone)
}

// Reconcile finishes the DEL owed by each record: intent and cleanup always, added only when
// its netns is gone. Records it cannot finish stay for the next run. Unreadable files are
// reported and left in place. Each record is re-read under its lock, so it is safe alongside
// Attach and Release.
func (s *Store) Reconcile(ctx context.Context, run Runner, gone func(Record) bool) error {
	recs, errs := s.list()
	for _, snap := range recs {
		if err := s.reconcileOne(ctx, run, snap, gone); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Store) reconcileOne(ctx context.Context, run Runner, snap Record, gone func(Record) bool) error {
	defer s.lock(snap.UID, snap.ContainerID)()
	r, ok, err := s.Get(snap.UID, snap.ContainerID)
	if err != nil || !ok {
		return err
	}
	g := gone(r)
	if r.State == StateAdded && !g {
		return nil
	}
	return s.finish(ctx, run, r, g)
}

// finish runs DEL from the stored record and removes it only once DEL succeeded.
func (s *Store) finish(ctx context.Context, run Runner, r Record, gone bool) error {
	if err := run.Del(ctx, r, gone); err != nil {
		return fmt.Errorf("del %s: %w", r.ContainerID, err)
	}
	return s.remove(r)
}
