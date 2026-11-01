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
	"sync"
	"time"

	"lanatt/internal/claims"
	"lanatt/internal/config"
	"lanatt/internal/wire"
)

// Server serialises every request; one request at a time touches claims and the kernel.
type Server struct {
	cfg   config.Config
	mu    sync.Mutex
	state claims.State
	conns chan struct{}
}

const (
	maxRequest = 64 << 10
	maxConns   = 32
)

// New reaps leftover temporary devices, loads stored claims, and drops those whose netns is gone.
func New(cfg config.Config) (*Server, error) {
	if err := reapTemps(); err != nil {
		return nil, fmt.Errorf("reap temporary devices: %w", err)
	}
	st, err := claims.Load(cfg.State)
	if err != nil {
		return nil, err
	}
	alive, err := liveNetns()
	if err != nil {
		return nil, err
	}
	st = st.Prune(alive)
	if err := claims.Save(cfg.State, st); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, state: st, conns: make(chan struct{}, maxConns)}, nil
}

// Listen opens the socket. Any local user may connect; peer credentials decide.
func (s *Server) Listen() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(s.cfg.Socket), 0o755); err != nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	// A netns inode can be reused, so a dead workload's claim must not look like this one's.
	alive, err := liveNetns()
	if err != nil {
		return err
	}
	if err := s.commit(s.state.Prune(alive)); err != nil {
		return err
	}
	plans, err := claims.Plan(s.state, s.cfg.Networks, uid, req.ID, t.netns, req.Joins)
	if err != nil {
		return err
	}
	for i, p := range plans {
		if p.Existing {
			continue
		}
		tmp := TempName(t.netns, i)
		if err := join(t.ns, s.cfg.Networks[p.Network], p, tmp); err != nil {
			return fmt.Errorf("network %q: %w", p.Network, err)
		}
		// Saved per join: an attachment in the netns must never be unclaimed.
		if err := s.commit(s.state.Add([]claims.Planned{p})); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) detach(uid uint32, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(s.state.Drop(uid, id))
}

func (s *Server) commit(next claims.State) error {
	if err := claims.Save(s.cfg.State, next); err != nil {
		return err
	}
	s.state = next
	return nil
}
