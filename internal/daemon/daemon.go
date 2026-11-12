// Package daemon is the root side: it checks the caller and joins the workload netns.
package daemon

import (
	"encoding/json"
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

// New reaps leftover temporary devices, loads stored claims, and drops those no live workload holds.
func New(cfg config.Config) (*Server, error) {
	if err := reapTemps(); err != nil {
		return nil, fmt.Errorf("reap temporary devices: %w", err)
	}
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
	log.Printf("claims: %d loaded", len(st.Claims))
	st = prune(st, boot)
	if err := claims.Save(cfg.State, st); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, boot: boot, state: st, conns: make(chan struct{}, maxConns)}, nil
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
	_ = json.NewEncoder(conn).Encode(resp)
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
	dec := json.NewDecoder(io.LimitReader(conn, maxRequest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
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
	// All or nothing: a failed request leaves no device or claim of its own behind.
	until := time.Now().Add(attachBudget)
	var joined []joinedPlan
	for i, p := range plans {
		if err := s.checkJoin(p, until); err != nil {
			s.rollback(t.ns, joined)
			return err
		}
		undo, err := join(t.ns, s.cfg.Networks[p.Network], p, TempName(t.netns, i))
		if err != nil {
			s.rollback(t.ns, joined)
			return fmt.Errorf("network %q: %w", p.Network, err)
		}
		if err := s.settle(p); err != nil {
			undoJoin(t.ns, undo, p.Interface)
			s.rollback(t.ns, joined)
			return err
		}
		joined = append(joined, joinedPlan{plan: p, undo: undo})
	}
	if s.complete(t.netns, plans) {
		s.rollback(t.ns, joined)
		return errCancelled
	}
	released = true
	return nil
}

// complete ends an attach that joined everything. A stop that landed since the last join
// cancels it instead: the caller rolls back, and nothing is reported as attached. The check and
// the release share one lock, so a stop is either before the success or after it.
func (s *Server) complete(netns uint64, plans []claims.Planned) (cancelled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.busy[netns]; a != nil && a.stopped {
		return true
	}
	s.release(netns, plans)
	return false
}

// joinedPlan is a join this attach made, with what it takes to undo its default routes.
type joinedPlan struct {
	plan claims.Planned
	undo defaultUndo
}

// reserve plans req under mu and holds the new joins as pending. One netns runs one attach at a
// time, so a retry cannot see a half-made attachment as already in place.
func (s *Server) reserve(uid uint32, req wire.Request, t target) ([]claims.Planned, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[t.netns] != nil {
		return nil, errors.New("an attach to this netns is already in progress")
	}
	// A netns inode can be reused, so a dead workload's claim must not look like this one's.
	if err := s.commit(prune(s.state, s.boot)); err != nil {
		return nil, err
	}
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

// settle makes one join's claim durable. The reservation stays until finish: the device is up
// until the attach ends, so its address must stay held.
func (s *Server) settle(p claims.Planned) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active(p) {
		return errCancelled
	}
	return s.commit(s.state.Add([]claims.Planned{p}))
}

// rollback undoes the joins of a failed attach, newest first. Claims are forgotten first. If that
// save fails, the stale claim holds its address until its owner exits: the safe side, with no
// device left unclaimed. The devices go either way.
func (s *Server) rollback(ns *os.File, joined []joinedPlan) {
	if len(joined) == 0 {
		return
	}
	plans := make([]claims.Planned, 0, len(joined))
	for _, j := range joined {
		plans = append(plans, j.plan)
	}
	s.mu.Lock()
	if err := s.commit(withoutJoined(s.state, plans)); err != nil {
		log.Printf("rollback: %v", err)
	}
	s.mu.Unlock()
	for i := len(joined) - 1; i >= 0; i-- {
		undoJoin(ns, joined[i].undo, joined[i].plan.Interface)
	}
}

// withoutJoined is s minus the claims of ps.
func withoutJoined(s claims.State, ps []claims.Planned) claims.State {
	next := claims.State{Claims: []claims.Claim{}}
	for _, c := range s.Claims {
		if !slices.ContainsFunc(ps, func(p claims.Planned) bool { return p.Claim == c }) {
			next.Claims = append(next.Claims, c)
		}
	}
	return next
}

// undoJoin restores the defaults a join replaced, then deletes its device. Each failure is
// logged: a device that will not go is the one thing the operator must see.
func undoJoin(ns *os.File, undo defaultUndo, iface string) {
	if err := undo.restore(ns); err != nil {
		log.Printf("restore defaults on %s: %v", iface, err)
	}
	if err := dropDevice(ns, iface); err != nil {
		log.Printf("drop device %s: %v", iface, err)
	}
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
	defer s.mu.Unlock()
	// Stop an attach still running for this container, including one that only repeats joins.
	// Its reservations stay until it finishes, since its devices are up until then.
	for _, a := range s.busy {
		if a.uid == uid && a.id == id {
			a.stopped = true
		}
	}
	return s.commit(s.state.Drop(uid, id))
}

func (s *Server) commit(next claims.State) error {
	if err := claims.Save(s.cfg.State, next); err != nil {
		return err
	}
	s.state = next
	return nil
}
