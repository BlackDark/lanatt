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
	"sync"
	"time"

	"lanatt/internal/claims"
	"lanatt/internal/config"
	"lanatt/internal/wire"
)

// Server guards claims and in-progress attachments with mu. Kernel work runs outside
// mu, so each attachment holds its reservations until completion or rollback.
type Server struct {
	cfg   config.Config
	boot  string
	mu    sync.Mutex
	state claims.State
	busy  map[uint64]*attachment
	conns chan struct{}
	// degraded means memory is ahead of the state file: admissions wait until a save succeeds.
	degraded bool
	// retiring holds the tokens whose removal is running: their netns and address stay held.
	retiring map[string]bool
	// remove overrides removeOwned in tests.
	remove func(claims.Pending) (bool, error)
}

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

// commit saves next and makes it the state. A refused save changes nothing.
func (s *Server) commit(next claims.State) error {
	if err := claims.Save(s.cfg.State, next); err != nil {
		return err
	}
	s.state = next
	s.degraded = false
	return nil
}
