package claims

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func pendingRec(netns uint64, network, token string) Pending {
	return Pending{Claim: Claim{ID: "c1", Netns: netns, Network: network, Address: netip.MustParseAddr("10.0.0.2")}, Token: token}
}

func TestPruneKeepsPending(t *testing.T) {
	s := State{Claims: []Claim{{ID: "gone", Netns: 1}}, Pending: []Pending{pendingRec(2, "a", "t")}}
	out, dropped := s.Prune(func(Claim) bool { return false })
	if len(out.Claims) != 0 || len(dropped) != 1 {
		t.Fatalf("claims: %v dropped %v", out.Claims, dropped)
	}
	if len(out.Pending) != 1 || out.Pending[0].Token != "t" {
		t.Fatalf("prune dropped a record: %v", out.Pending)
	}
}

func TestAddAndRemovePendingLeaveOriginal(t *testing.T) {
	s := State{Pending: []Pending{pendingRec(1, "a", "t1")}}
	added := s.AddPending(pendingRec(1, "b", "t2"))
	if len(s.Pending) != 1 || len(added.Pending) != 2 {
		t.Fatalf("AddPending changed its input or lost the record: %v %v", s.Pending, added.Pending)
	}
	removed := added.RemovePending(1, "a")
	if len(removed.Pending) != 1 || removed.Pending[0].Network != "b" {
		t.Fatalf("RemovePending: %v", removed.Pending)
	}
	if len(added.Pending) != 2 {
		t.Fatal("RemovePending changed its input")
	}
}

func TestRemoveTokenKeepsNewerRecordOfSameNetwork(t *testing.T) {
	s := State{Pending: []Pending{pendingRec(1, "a", "old"), pendingRec(1, "a", "new")}}
	got := s.RemoveToken("old")
	if len(got.Pending) != 1 || got.Pending[0].Token != "new" {
		t.Fatalf("RemoveToken dropped the newer record: %v", got.Pending)
	}
}

func TestRecordRoundTripsThroughSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.json")
	rec := pendingRec(7, "a", "tok")
	rec.Temp = "lt000000071"
	if err := Save(path, State{Claims: []Claim{}, Pending: []Pending{rec}}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pending) != 1 || got.Pending[0] != rec {
		t.Fatalf("loaded %v, want %v", got.Pending, rec)
	}
}

func TestLegacyStateLoadsWithoutPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.json")
	if err := os.WriteFile(path, []byte(`{"claims":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pending) != 0 {
		t.Fatalf("legacy state has records: %v", got.Pending)
	}
}
