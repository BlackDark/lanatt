package claims

import (
	"path/filepath"
	"testing"

	"lanatt/internal/config"
	"lanatt/internal/wire"
)

func nets(t *testing.T) map[string]config.Network {
	t.Helper()
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"},
		{"name":"b","parent":"d0","prefix":"10.254.2.0/28","gateway":"10.254.2.14","driver":"macvlan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Networks
}

func admit(t *testing.T, s State, id string, ns uint64, joins ...wire.Join) State {
	t.Helper()
	p, err := Plan(s, nets(t), 1000, id, ns, joins)
	if err != nil {
		t.Fatal(err)
	}
	return s.Add(p)
}

var j1 = wire.Join{Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02"}

func TestPlanRefusesBadJoins(t *testing.T) {
	cases := map[string]wire.Join{
		"unknown network": {Network: "x", Address: "10.254.1.2", MAC: "02:00:00:00:00:02"},
		"outside":         {Network: "a", Address: "10.254.9.2", MAC: "02:00:00:00:00:02"},
		"prefix address":  {Network: "a", Address: "10.254.1.0", MAC: "02:00:00:00:00:02"},
		"broadcast":       {Network: "a", Address: "10.254.1.15", MAC: "02:00:00:00:00:02"},
		"gateway":         {Network: "a", Address: "10.254.1.14", MAC: "02:00:00:00:00:02"},
		"multicast MAC":   {Network: "a", Address: "10.254.1.2", MAC: "01:00:5e:00:00:02"},
		"zero MAC":        {Network: "a", Address: "10.254.1.2", MAC: "00:00:00:00:00:00"},
		"bad MAC":         {Network: "a", Address: "10.254.1.2", MAC: "nope"},
		"bad interface":   {Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Interface: "a/b"},
	}
	for name, j := range cases {
		if _, err := Plan(State{}, nets(t), 1000, "c", 1, []wire.Join{j}); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
	if _, err := Plan(State{}, nets(t), 1000, "c", 1, nil); err == nil {
		t.Error("empty join list admitted")
	}
	if _, err := Plan(State{}, nets(t), 1000, "c", 1, []wire.Join{j1, j1}); err == nil {
		t.Error("same network twice in one request admitted")
	}
}

func TestRepeatJoinIsExisting(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	p, err := Plan(s, nets(t), 1000, "c", 1, []wire.Join{j1})
	if err != nil {
		t.Fatal(err)
	}
	if !p[0].Existing || len(s.Add(p).Claims) != 1 {
		t.Fatalf("repeat not treated as existing: %+v", p)
	}
}

func TestSameNetnsDifferentIdentityRefused(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	for _, j := range []wire.Join{
		{Network: "a", Address: "10.254.1.3", MAC: j1.MAC},
		{Network: "a", Address: j1.Address, MAC: "02:00:00:00:00:03"},
	} {
		if _, err := Plan(s, nets(t), 1000, "c", 1, []wire.Join{j}); err == nil {
			t.Errorf("%+v admitted", j)
		}
	}
}

func TestOtherNetnsClashRefused(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	for _, j := range []wire.Join{
		{Network: "a", Address: j1.Address, MAC: "02:00:00:00:00:09"},
		{Network: "a", Address: "10.254.1.9", MAC: j1.MAC},
	} {
		if _, err := Plan(s, nets(t), 1000, "d", 2, []wire.Join{j}); err == nil {
			t.Errorf("%+v admitted", j)
		}
	}
	other := wire.Join{Network: "b", Address: "10.254.2.2", MAC: j1.MAC}
	if _, err := Plan(s, nets(t), 1000, "d", 2, []wire.Join{other}); err != nil {
		t.Errorf("same MAC on another network refused: %v", err)
	}
}

func TestInterfaceNames(t *testing.T) {
	s := admit(t, State{}, "c", 1, wire.Join{Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Interface: "lan0"})
	p, err := Plan(s, nets(t), 1000, "c", 1, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: "02:00:00:00:00:04"}})
	if err != nil {
		t.Fatal(err)
	}
	if p[0].Interface != "lan1" {
		t.Errorf("got %q, want lan1", p[0].Interface)
	}
	p, err = Plan(State{}, nets(t), 1000, "c", 1, []wire.Join{
		{Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02"},
		{Network: "b", Address: "10.254.2.2", MAC: "02:00:00:00:00:04", Interface: "lan0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p[0].Interface != "lan1" || p[1].Interface != "lan0" {
		t.Errorf("got %q %q", p[0].Interface, p[1].Interface)
	}
	if _, err := Plan(s, nets(t), 1000, "c", 1, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: "02:00:00:00:00:04", Interface: "lan0"}}); err == nil {
		t.Error("claimed interface name reused")
	}
}

func TestDropAndPrune(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	s = admit(t, s, "d", 2, wire.Join{Network: "a", Address: "10.254.1.3", MAC: "02:00:00:00:00:03"})
	if got := s.Drop(1000, "c"); len(got.Claims) != 1 || got.Claims[0].ID != "d" {
		t.Errorf("drop: %+v", got)
	}
	if got := s.Drop(1001, "c"); len(got.Claims) != 2 {
		t.Errorf("another uid dropped a claim: %+v", got)
	}
	if got := s.Prune(map[uint64]bool{1: true}); len(got.Claims) != 1 || got.Claims[0].ID != "c" {
		t.Errorf("prune: %+v", got)
	}
	if _, err := Plan(s.Drop(1000, "c"), nets(t), 1000, "e", 3, []wire.Join{j1}); err != nil {
		t.Errorf("address not free after drop: %v", err)
	}
}

func TestRetryWithOtherInterfaceRefused(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	again := j1
	again.Interface = "eth7"
	if _, err := Plan(s, nets(t), 1000, "c", 1, []wire.Join{again}); err == nil {
		t.Error("retry renaming the interface admitted")
	}
}

func TestOneDefaultPerRequest(t *testing.T) {
	_, err := Plan(State{}, nets(t), 1000, "c", 1, []wire.Join{
		{Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Default: true},
		{Network: "b", Address: "10.254.2.2", MAC: "02:00:00:00:00:04", Default: true},
	})
	if err == nil {
		t.Error("two default joins admitted")
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "claims.json")
	empty, err := Load(path)
	if err != nil || len(empty.Claims) != 0 {
		t.Fatalf("missing file: %+v %v", empty, err)
	}
	s := admit(t, State{}, "c", 1, j1)
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Claims) != 1 || got.Claims[0] != s.Claims[0] {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestOutsideRangesRefused(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan","ranges":["10.254.1.2-10.254.1.4"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(State{}, cfg.Networks, 1000, "c", 1, []wire.Join{{Network: "a", Address: "10.254.1.5", MAC: "02:00:00:00:00:05"}}); err == nil {
		t.Error("address outside ranges admitted")
	}
	if _, err := Plan(State{}, cfg.Networks, 1000, "c", 1, []wire.Join{{Network: "a", Address: "10.254.1.3", MAC: "02:00:00:00:00:03"}}); err != nil {
		t.Errorf("address inside ranges refused: %v", err)
	}
}
