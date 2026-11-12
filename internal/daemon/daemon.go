// Package daemon is the root side: it checks the caller and joins the workload netns.
package daemon

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"lanatt/internal/claims"
	"lanatt/internal/config"
	"lanatt/internal/wire"
)

// Server guards claims with mu. A join's kernel work runs outside mu: pending holds the
// admitted joins still in progress, so later requests plan around them. busy holds each netns
// with an attach in progress, so a second attach there is refused.
type Server struct {
	cfg     config.Config
	boot    string
	mu      sync.Mutex
	state   claims.State
	pending []claims.Planned
	busy    map[uint64]*attachment
	conns   chan struct{}
	// degraded means memory is ahead of the state file: admissions wait until a save succeeds.
	degraded bool
	// retiring holds the tokens whose removal is running: their netns and address stay held.
	retiring map[string]bool
	// remove overrides removeOwned in tests.
	remove func(claims.Pending) (bool, error)
}

// attachment is an attach in progress in one netns. A stop for its container sets stopped.
type attachment struct {
	uid     uint32
	id      string
	stopped bool
}

// errCancelled means a stop arrived while the attach was joining. Its claims were dropped.
var errCancelled = errors.New("attach cancelled by a stop for the same container")

// errTimedOut means the attach ran past its budget. The hook's socket deadline is 30s, and a
// join can take DAD time, so no new join starts after attachBudget.
var errTimedOut = errors.New("attach ran past its time budget")

// attachBudget bounds when a join may start. A join is one DAD wait plus a few ip calls, so one
// that starts in budget normally ends well before the 30s deadline.
const attachBudget = 20 * time.Second

const (
	maxRequest = 64 << 10
	maxConns   = 32
)

// New loads stored state, prunes dead claims, and retries pending cleanup.
func New(cfg config.Config) (*Server, error) {
	if err := prepareSocketDir(cfg.Socket); err != nil {
		return nil, err
	}
	st, err := claims.Load(cfg.State)
	if err != nil {
		return nil, err
	}
	boot, err := BootID()
	if err != nil {
		return nil, err
	}
	log.Printf("claims: %d loaded, %d pending", len(st.Claims), len(st.Pending))
	st = prune(st, boot)
	if err := claims.Save(cfg.State, st); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, boot: boot, state: st, conns: make(chan struct{}, maxConns)}
	if err := s.recoverPending(); err != nil {
		log.Printf("%v", err)
	}
	return s, nil
}

// prepareSocketDir makes the socket's directory traversable. Rootless hooks must reach the
// socket through it; it must exist before the state file, or Save would create it 0700.
func prepareSocketDir(socket string) error {
	return os.MkdirAll(filepath.Dir(socket), 0o755)
}

// prune drops the claims no live workload holds, and logs each one with its reason.
func prune(s claims.State, boot string) claims.State {
	out, dropped := s.Prune(LiveClaim(boot))
	for _, c := range dropped {
		log.Printf("dropped claim %s %s: %s", claims.ShortID(c.ID), claims.Addresses(c), DeadReason(c, boot))
	}
	return out
}

// Listen opens the socket. Any local user may connect; peer credentials decide.
func (s *Server) Listen() (net.Listener, error) {
	if err := prepareSocketDir(s.cfg.Socket); err != nil {
		return nil, err
	}
	if err := os.Remove(s.cfg.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", s.cfg.Socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(s.cfg.Socket, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve handles connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		s.conns <- struct{}{}
		go func() {
			defer func() { <-s.conns }()
			s.serveConn(conn)
		}()
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	resp := wire.Response{OK: true}
	if err := s.respond(conn); err != nil {
		resp = wire.Response{Error: err.Error()}
	}
	_ = json.MarshalWrite(conn, resp)
}

func (s *Server) respond(conn net.Conn) error {
	uid, err := peerUID(conn)
	if err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	if !s.cfg.Allowed(uid) {
		log.Printf("uid=%d refused: not allowed", uid)
		return fmt.Errorf("uid %d is not allowed", uid)
	}
	var req wire.Request
	dec := jsontext.NewDecoder(io.LimitReader(conn, maxRequest))
	if err := json.UnmarshalDecode(dec, &req, json.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("bad request: %w", err)
	}
	err = s.handle(uid, req)
	log.Printf("uid=%d op=%s id=%q pid=%d err=%v", uid, req.Op, req.ID, req.Pid, err)
	return err
}

func (s *Server) handle(uid uint32, req wire.Request) error {
	if req.ID == "" {
		return errors.New("container id is empty")
	}
	switch req.Op {
	case wire.OpAttach:
		return s.attach(uid, req)
	case wire.OpDetach:
		return s.detach(uid, req.ID)
	default:
		return fmt.Errorf("unknown op %q", req.Op)
	}
}

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
			s.finish(t.netns, plans)
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
	if err := s.complete(t.netns, plans); err != nil {
		return s.abort(t.ns, joined, err)
	}
	released = true
	return nil
}

// complete ends an attach that joined everything: its records go in the same save that makes its
// claims final. A stop that landed since the last join cancels it instead. A refused save leaves
// the records, and the caller aborts. The check, save and release share one lock, so a stop is
// either before the success or after it.
func (s *Server) complete(netns uint64, plans []claims.Planned) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.busy[netns]; a != nil && a.stopped {
		return errCancelled
	}
	next := s.state.Add(plans)
	for _, p := range plans {
		next = next.RemovePending(p.Netns, p.Network)
	}
	if err := s.commit(next); err != nil {
		return err
	}
	s.release(netns, plans)
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
	plans, err := claims.Plan(s.state.Add(s.pending), s.cfg.Networks, uid, req.ID, t.netns, req.Joins)
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
	s.busy[t.netns] = &attachment{uid: uid, id: req.ID}
	s.pending = append(s.pending, fresh...)
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
	return a != nil && !a.stopped && slices.Contains(s.pending, p)
}

// finish ends an attach: its netns is free again and its reservations are dropped.
func (s *Server) finish(netns uint64, plans []claims.Planned) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.release(netns, plans)
}

// release is finish with mu held.
func (s *Server) release(netns uint64, plans []claims.Planned) {
	delete(s.busy, netns)
	for _, p := range plans {
		s.pending = dropPending(s.pending, p)
	}
}

// dropPending removes p from pending. A Planned is comparable: one netns holds one claim per network.
func dropPending(pending []claims.Planned, p claims.Planned) []claims.Planned {
	out := pending[:0]
	for _, q := range pending {
		if q != p {
			out = append(out, q)
		}
	}
	return out
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

// commit saves next and makes it the state. A refused save changes nothing.
func (s *Server) commit(next claims.State) error {
	if err := claims.Save(s.cfg.State, next); err != nil {
		return err
	}
	s.state = next
	s.degraded = false
	return nil
}
