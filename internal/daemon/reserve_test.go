package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"lanatt/internal/claims"
	"lanatt/internal/config"
	"lanatt/internal/wire"
)

func reserveServer(t *testing.T) *Server {
	t.Helper()
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"},
		{"name":"b","parent":"d0","prefix":"10.254.2.0/28","gateway":"10.254.2.14","driver":"macvlan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.State = filepath.Join(t.TempDir(), "claims.json")
	return &Server{cfg: cfg, boot: "boot"}
}

func attachReq(id, addr, mac string) wire.Request {
	return attachReqOn("a", id, addr, mac)
}

func attachReqOn(network, id, addr, mac string) wire.Request {
	return wire.Request{Op: wire.OpAttach, ID: id, Pid: 1, Joins: []wire.Join{{Network: network, Address: addr, MAC: mac}}}
}

func TestPendingReservationKeepsParent(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"},
		{"name":"b","parent":"d0","prefix":"10.254.2.0/28","gateway":"10.254.2.14","driver":"macvlan"},
		{"name":"c","parent":"d1","prefix":"10.254.3.0/28","gateway":"10.254.3.14","driver":"macvlan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.State = filepath.Join(t.TempDir(), "claims.json")
	s := &Server{cfg: cfg, boot: "boot"}
	const mac = "02:00:00:00:00:02"
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", mac), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if p[0].Parent != "d0" {
		t.Fatalf("pending parent %q, want d0", p[0].Parent)
	}
	if _, err := s.reserve(1000, attachReqOn("b", "c2", "10.254.2.2", mac), target{netns: 2, start: "1"}); err == nil {
		t.Error("MAC reserved on d0 admitted on the same parent")
	}
	if _, err := s.reserve(1000, attachReqOn("c", "c3", "10.254.3.2", mac), target{netns: 3, start: "1"}); err != nil {
		t.Errorf("MAC reserved on d0 refused on d1: %v", err)
	}
}

func TestReserveHoldsAddressUntilFinished(t *testing.T) {
	s := reserveServer(t)
	one := target{netns: 1, start: "1"}
	two := target{netns: 2, start: "1"}

	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), one)
	if err != nil || len(p) != 1 {
		t.Fatalf("reserve: %v %v", p, err)
	}
	if _, err := s.reserve(1000, attachReq("c2", "10.254.1.2", "02:00:00:00:00:03"), two); err == nil {
		t.Fatal("second netns got a reserved address")
	}
	// Another network, so only the busy check can refuse it.
	other := attachReqOn("b", "c1", "10.254.2.2", "02:00:00:00:00:04")
	if _, err := s.reserve(1000, other, one); err == nil {
		t.Fatal("second attach to a busy netns admitted")
	}
	s.finish(1, p)
	if _, err := s.reserve(1000, attachReq("c2", "10.254.1.2", "02:00:00:00:00:03"), two); err != nil {
		t.Fatalf("address not released by finish: %v", err)
	}
}

func TestCompleteCommitsClaimAndDropsReservation(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.complete(1, p); err != nil {
		t.Fatal(err)
	}
	if len(s.pending) != 0 {
		t.Fatalf("reservation left after complete: %v", s.pending)
	}
	saved, err := claims.Load(s.cfg.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Claims) != 1 || saved.Claims[0].ID != "c1" {
		t.Fatalf("saved claims %v", saved.Claims)
	}
}

func TestCompletedClaimKeepsAddress(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.complete(1, p); err != nil {
		t.Fatal(err)
	}
	// Planned against the saved state directly: reserve would prune this fake owner first.
	joins := attachReq("c2", "10.254.1.2", "02:00:00:00:00:03").Joins
	if _, err := claims.Plan(s.state, s.cfg.Networks, 1000, "c2", 2, joins); err == nil {
		t.Fatal("completed claim released its address")
	}
}

func TestCheckJoinRefusesCancelledAndLate(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkJoin(p[0], time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("live join refused: %v", err)
	}
	if err := s.checkJoin(p[0], time.Now().Add(-time.Second)); err != errTimedOut {
		t.Fatalf("late join: %v, want errTimedOut", err)
	}
	if err := s.detach(1000, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.checkJoin(p[0], time.Now().Add(time.Minute)); err != errCancelled {
		t.Fatalf("cancelled join: %v, want errCancelled", err)
	}
}

func TestDetachCancelsButKeepsReservationUntilFinish(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.detach(1000, "c1"); err != nil {
		t.Fatal(err)
	}
	// The device is still up: its address must stay held until the attach finishes.
	if !slices.Contains(s.pending, p[0]) {
		t.Fatal("reservation dropped before the attach finished")
	}
	if len(s.state.Claims) != 0 {
		t.Fatalf("claim committed after stop: %v", s.state.Claims)
	}
	s.finish(1, p)
	if len(s.pending) != 0 {
		t.Fatalf("reservation left after finish: %v", s.pending)
	}
}

func TestCompleteFailureKeepsReservation(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	// Save fails from here on: a regular file stands where the state directory must be.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg.State = filepath.Join(blocker, "claims.json")
	if err := s.complete(1, p); err == nil {
		t.Fatal("complete saved into a blocked directory")
	}
	if !slices.Contains(s.pending, p[0]) {
		t.Fatal("reservation dropped before the claim was saved")
	}
}

func TestCompleteRefusesAfterStop(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.detach(1000, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.complete(1, p); err != errCancelled {
		t.Fatalf("complete after stop: %v, want errCancelled", err)
	}
	if !slices.Contains(s.pending, p[0]) {
		t.Fatal("refused attach released its reservation before its devices were undone")
	}
}
