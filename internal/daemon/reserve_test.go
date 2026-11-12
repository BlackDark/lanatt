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

func TestSettleCommitsClaimAndFinishDropsReservation(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.settle(p[0]); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.pending, p[0]) {
		t.Fatal("settle dropped the reservation: the device is still up")
	}
	s.finish(1, p)
	if len(s.pending) != 0 {
		t.Fatalf("reservation left after finish: %v", s.pending)
	}
	saved, err := claims.Load(s.cfg.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Claims) != 1 || saved.Claims[0].ID != "c1" {
		t.Fatalf("saved claims %v", saved.Claims)
	}
}

func TestSettledClaimKeepsAddress(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.settle(p[0]); err != nil {
		t.Fatal(err)
	}
	s.finish(1, p)
	// Planned against the saved state directly: reserve would prune this fake owner first.
	joins := attachReq("c2", "10.254.1.2", "02:00:00:00:00:03").Joins
	if _, err := claims.Plan(s.state, s.cfg.Networks, 1000, "c2", 2, joins); err == nil {
		t.Fatal("settled claim released its address")
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

func TestWithoutJoinedKeepsOtherClaims(t *testing.T) {
	mine := claims.Claim{ID: "c1", Netns: 1, Network: "a", Interface: "lan0"}
	other := claims.Claim{ID: "c2", Netns: 2, Network: "a", Interface: "lan0"}
	got := withoutJoined(claims.State{Claims: []claims.Claim{mine, other}}, []claims.Planned{{Claim: mine}})
	if len(got.Claims) != 1 || got.Claims[0] != other {
		t.Fatalf("got %v", got.Claims)
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
	if err := s.settle(p[0]); err != errCancelled {
		t.Fatalf("settle after stop: %v, want errCancelled", err)
	}
	if len(s.state.Claims) != 0 {
		t.Fatalf("claim committed after stop: %v", s.state.Claims)
	}
	s.finish(1, p)
	if len(s.pending) != 0 {
		t.Fatalf("reservation left after finish: %v", s.pending)
	}
}

func TestSettleFailureKeepsReservation(t *testing.T) {
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
	if err := s.settle(p[0]); err == nil {
		t.Fatal("settle saved into a blocked directory")
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
	if err := s.settle(p[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.detach(1000, "c1"); err != nil {
		t.Fatal(err)
	}
	if !s.complete(1, p) {
		t.Fatal("attach reported success after a stop")
	}
	if !slices.Contains(s.pending, p[0]) {
		t.Fatal("refused attach released its reservation before its devices were undone")
	}
}
