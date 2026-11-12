package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lanatt/internal/claims"
)

// failingRemove stands for a device that will not go: removal is never verified.
func failingRemove(calls *int) func(claims.Pending) (bool, error) {
	return func(claims.Pending) (bool, error) {
		*calls++
		return false, errors.New("delete refused")
	}
}

func goneRemove(calls *int) func(claims.Pending) (bool, error) {
	return func(claims.Pending) (bool, error) {
		*calls++
		return true, nil
	}
}

// intendOne reserves and records one join as attach would, returning its plan and record.
func intendOne(t *testing.T, s *Server, id, addr, mac string, netns uint64) (claims.Planned, claims.Pending) {
	t.Helper()
	p, err := s.reserve(1000, attachReq(id, addr, mac), target{netns: netns, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.intend(p[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	return p[0], rec
}

func TestOwnedNameMatchesOnlyOwnToken(t *testing.T) {
	out := `[
		{"ifname":"lan0","ifalias":"lanatt:tok1"},
		{"ifname":"lan1","ifalias":"lanatt:tok2"},
		{"ifname":"lan0","ifalias":"other"}
	]`
	if name, ok, err := ownedName(out, "tok2", ""); err != nil || !ok || name != "lan1" {
		t.Fatalf("own token: %q %v %v", name, ok, err)
	}
	if _, ok, _ := ownedName(out, "tok3", ""); ok {
		t.Fatal("unknown token matched a device")
	}
}

func TestOwnedNameHostMatchesTempWithoutAlias(t *testing.T) {
	out := `[{"ifname":"lt0000000102","ifalias":""},{"ifname":"lt0000000103","ifalias":"lanatt:x"}]`
	if name, ok, _ := ownedName(out, "tok", "lt0000000102"); !ok || name != "lt0000000102" {
		t.Fatalf("temp without alias: %q %v", name, ok)
	}
	if _, ok, _ := ownedName(out, "tok", "lt0000000103"); ok {
		t.Fatal("temp name with another token's alias matched")
	}
}

func TestAbortKeepsRecordWhenRemovalFails(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	if err := s.settle(p); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("network \"a\": boom")
	err := s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, cause)
	if err == nil || !strings.Contains(err.Error(), cause.Error()) || !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("abort error %v: want both cause and cleanup failure", err)
	}
	if len(s.state.Claims) != 0 {
		t.Fatalf("claim kept after abort: %v", s.state.Claims)
	}
	if !s.state.HasPending(p.Netns, p.Network) {
		t.Fatal("record dropped while its device may exist")
	}
	saved, err := claims.Load(s.cfg.State)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.HasPending(p.Netns, p.Network) {
		t.Fatal("record not saved")
	}
}

func TestRetryAfterRemovalFailureReportsNotJoined(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, errors.New("boom"))
	s.finish(1, []claims.Planned{p})
	// Same netns, same address: the failed join must not look already joined.
	if _, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"}); err == nil {
		t.Fatal("retry admitted over an unremoved device")
	}
	if calls == 0 {
		t.Fatal("conflicting record was not retried")
	}
}

func TestRetrySucceedsOnceRemovalVerified(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, errors.New("boom"))
	s.finish(1, []claims.Planned{p})

	s.remove = goneRemove(&calls)
	if _, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"}); err != nil {
		t.Fatalf("verified removal still refused: %v", err)
	}
	if len(s.state.Pending) != 0 {
		t.Fatalf("record kept after verified removal: %v", s.state.Pending)
	}
}

func TestRecordSurvivesRestartUntilVerified(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, errors.New("boom"))

	// A new daemon loads the state file: the record must still be there.
	st, err := claims.Load(s.cfg.State)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Server{cfg: s.cfg, boot: s.boot, state: st}
	restarted.remove = failingRemove(&calls)
	if err := restarted.recoverPending(); err == nil {
		t.Fatal("unverified record reported clean at restart")
	}
	restarted.remove = goneRemove(&calls)
	if err := restarted.recoverPending(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.state.Pending) != 0 {
		t.Fatal("verified record kept at restart")
	}
}

func TestEarlierBootRecordRetiredWithoutRemoval(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, errors.New("boom"))
	calls = 0

	restarted := &Server{cfg: s.cfg, boot: "new-boot", state: s.state}
	restarted.remove = failingRemove(&calls)
	if err := restarted.recoverPending(); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("removal attempted for an earlier boot: %d", calls)
	}
	if len(restarted.state.Pending) != 0 {
		t.Fatal("earlier-boot record kept")
	}
}

func TestDetachKeepsUnverifiedRecord(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, errors.New("boom"))
	s.finish(1, []claims.Planned{p})

	if err := s.detach(1000, "c1"); err == nil {
		t.Fatal("stop reported clean with an unverified device")
	}
	if !s.state.HasPending(1, "a") {
		t.Fatal("stop dropped the record")
	}
	s.remove = goneRemove(&calls)
	if err := s.detach(1000, "c1"); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Pending) != 0 {
		t.Fatal("stop kept a verified record")
	}
}

func TestIntendRefusedWithoutDevice(t *testing.T) {
	s := reserveServer(t)
	p, err := s.reserve(1000, attachReq("c1", "10.254.1.2", "02:00:00:00:00:02"), target{netns: 1, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg.State = filepath.Join(blocker, "claims.json")
	if _, err := s.intend(p[0], 0); err == nil {
		t.Fatal("intent recorded into a blocked directory")
	}
	if len(s.state.Pending) != 0 {
		t.Fatal("refused intent left a record in memory")
	}
}

func TestSaveFailureDegradesUntilSaved(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	good := s.cfg.State
	s.cfg.State = filepath.Join(blocker, "claims.json")
	s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, errors.New("boom"))
	if !s.degraded {
		t.Fatal("refused save did not degrade the server")
	}
	if calls != 0 {
		t.Fatalf("device removed although the save of its claim-less state failed: %d", calls)
	}
	s.finish(1, []claims.Planned{p})
	other := attachReqOn("b", "c2", "10.254.2.2", "02:00:00:00:00:09")
	if _, err := s.reserve(1000, other, target{netns: 2, start: "1"}); err == nil {
		t.Fatal("degraded server admitted an attach")
	}

	s.cfg.State = good
	if _, err := s.reserve(1000, other, target{netns: 2, start: "1"}); err != nil {
		t.Fatalf("save restored but attach refused: %v", err)
	}
	if s.degraded {
		t.Fatal("degraded not cleared by a successful save")
	}
}

func TestSettleKeepsRecordUntilComplete(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, _ := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	if err := s.settle(p); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Claims) != 1 || len(s.state.Pending) != 1 {
		t.Fatalf("after settle: claims %v pending %v", s.state.Claims, s.state.Pending)
	}
	if err := s.complete(1, []claims.Planned{p}); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Claims) != 1 || len(s.state.Pending) != 0 {
		t.Fatalf("after complete: claims %v pending %v", s.state.Claims, s.state.Pending)
	}
}

func TestCompleteSaveFailureKeepsRecord(t *testing.T) {
	s := reserveServer(t)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	if err := s.settle(p); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg.State = filepath.Join(blocker, "claims.json")
	if err := s.complete(1, []claims.Planned{p}); err == nil {
		t.Fatal("complete succeeded into a blocked directory")
	}
	if len(s.state.Pending) != 1 || s.state.Pending[0].Token != rec.Token {
		t.Fatalf("record lost on failed complete: %v", s.state.Pending)
	}
}

func TestIntendTokensAreDistinct(t *testing.T) {
	s := reserveServer(t)
	_, a := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	q, err := s.reserve(1000, attachReq("c2", "10.254.1.3", "02:00:00:00:00:03"), target{netns: 2, start: "1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.intend(q[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Token == b.Token || a.Temp == b.Temp {
		t.Fatalf("tokens collide: %v %v", a, b)
	}
}

func TestRetireRefusesConcurrentRemoval(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	_, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	if !s.claimRetire(rec) {
		t.Fatal("first claim refused")
	}
	if err := s.retire([]claims.Pending{rec}); err == nil {
		t.Fatal("retire ran while another removal of the record was in progress")
	}
	if calls != 0 {
		t.Fatalf("device touched by a second retire: %d calls", calls)
	}
	s.releaseRetire(rec)
	if err := s.retire([]claims.Pending{rec}); err == nil || calls != 1 {
		t.Fatalf("retire after release: err %v calls %d", err, calls)
	}
}

func TestReserveRefusedWhileConflictRetires(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	_, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	s.finish(1, nil)
	s.claimRetire(rec)
	if _, err := s.reserve(1000, attachReq("c2", "10.254.1.2", "02:00:00:00:00:04"), target{netns: 2, start: "1"}); err == nil {
		t.Fatal("attach admitted over a record whose removal is running")
	}
}

func TestParseNsfsMountsPicksNetnsBinds(t *testing.T) {
	out := "36 25 0:33 / /run/netns/a rw shared:1 - nsfs nsfs rw\n" +
		"37 25 0:34 / /var/lib/x\\040y rw - ext4 /dev/sda1 rw\n" +
		"38 25 0:35 / /run/user/0/netns/b rw - nsfs nsfs rw\n" +
		"bad line\n"
	got := parseNsfsMounts(out)
	if len(got) != 2 || got[0] != "/run/netns/a" || got[1] != "/run/user/0/netns/b" {
		t.Fatalf("got %v", got)
	}
}

func TestNoReferenceMeansAbsentNetns(t *testing.T) {
	pids, err := procPids()
	if err != nil {
		t.Skip("needs linux /proc")
	}
	// A live pid we cannot read is an error by design; the test needs root (as on the VM).
	for _, pid := range pids {
		if f, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid)); err != nil {
			t.Skipf("needs read access to every /proc/<pid>/ns/net: %v", err)
		} else {
			f.Close()
		}
	}
	if _, err := openNetns(^uint64(0)); !errors.Is(err, errNoReference) {
		t.Fatalf("unreferenced netns: %v, want errNoReference", err)
	}
}

func TestDetachKeepsClaimOfBusyNetns(t *testing.T) {
	s := reserveServer(t)
	var calls int
	s.remove = failingRemove(&calls)
	p, rec := intendOne(t, s, "c1", "10.254.1.2", "02:00:00:00:00:02", 1)
	if err := s.settle(p); err != nil {
		t.Fatal(err)
	}
	if err := s.detach(1000, "c1"); err != nil {
		t.Fatal(err)
	}
	// The device is up until abort: its claim must not go before its record is written.
	if len(s.state.Claims) != 1 {
		t.Fatalf("detach dropped a busy claim: %v", s.state.Claims)
	}
	if err := s.abort(nil, []joinedPlan{{plan: p, rec: rec}}, errCancelled); err == nil {
		t.Fatal("abort reported clean with an unverified device")
	}
	if len(s.state.Claims) != 0 || !s.state.HasPending(1, "a") {
		t.Fatalf("abort left claims %v, pending %v", s.state.Claims, s.state.Pending)
	}
}
