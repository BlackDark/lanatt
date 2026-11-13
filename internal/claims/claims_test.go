package claims

import (
	"encoding/json/v2"
	"net/netip"
	"path/filepath"
	"strings"
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
		"all interface":   {Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Interface: "all"},
		"default iface":   {Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Interface: "default"},
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
	if _, err := Plan(s, nets(t), 1000, "d", 2, []wire.Join{other}); err == nil || !strings.Contains(err.Error(), "MAC "+j1.MAC+" is taken") {
		t.Errorf("same MAC on another network of the same parent: %v", err)
	}
}

func TestSameMACOnOtherParentAdmitted(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"},
		{"name":"c","parent":"d1","prefix":"10.254.3.0/28","gateway":"10.254.3.14","driver":"macvlan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := admit(t, State{}, "c", 1, j1)
	if _, err := Plan(s, cfg.Networks, 1000, "d", 2, []wire.Join{{Network: "c", Address: "10.254.3.2", MAC: j1.MAC}}); err != nil {
		t.Errorf("same MAC on another parent refused: %v", err)
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
	if got, dropped := s.Prune(func(c Claim) bool { return c.ID == "c" }); len(got.Claims) != 1 || got.Claims[0].ID != "c" || len(dropped) != 1 || dropped[0].ID != "d" {
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

func TestOwnStampsOnlyNewClaims(t *testing.T) {
	owner := Owner{BootID: "boot", Pid: 42, Start: "777"}
	s := admit(t, State{}, "c", 1, j1)
	plans, err := Plan(s, nets(t), 1000, "c", 1, []wire.Join{j1})
	if err != nil {
		t.Fatal(err)
	}
	if got := Own(plans, owner)[0]; got.OwnerPid != 0 {
		t.Errorf("existing claim restamped: %+v", got)
	}
	plans, err = Plan(State{}, nets(t), 1000, "d", 2, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: "02:00:00:00:00:04"}})
	if err != nil {
		t.Fatal(err)
	}
	c := Own(plans, owner)[0].Claim
	if c.BootID != "boot" || c.OwnerPid != 42 || c.OwnerStart != "777" {
		t.Errorf("new claim not owned: %+v", c)
	}
}

func TestRefusalNamesHolder(t *testing.T) {
	s := State{Claims: []Claim{{
		ID: "a6eabd1adc12ffff", UID: 1001, Netns: 4026532504, Network: "a",
		Address: netip.MustParseAddr("10.254.1.2"), MAC: "02:00:00:00:00:02",
		Interface: "lan0", OwnerPid: 4711,
	}}}
	cases := map[string]struct {
		join wire.Join
		want string
	}{
		"address": {
			wire.Join{Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:09"},
			`network "a": address 10.254.1.2 is taken by claim a6eabd1adc12 (uid 1001, netns 4026532504, network a, lan0, pid 4711)`,
		},
		"MAC": {
			wire.Join{Network: "a", Address: "10.254.1.3", MAC: "02:00:00:00:00:02"},
			`network "a": MAC 02:00:00:00:00:02 is taken by claim a6eabd1adc12 (uid 1001, netns 4026532504, network a, lan0, pid 4711)`,
		},
	}
	for name, c := range cases {
		_, err := Plan(s, nets(t), 1000, "c", 2, []wire.Join{c.join})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: got %v\nwant %s", name, err, c.want)
		}
	}
}

func TestHolderWithoutInterface(t *testing.T) {
	// A claim planned earlier in the same request has no interface yet.
	c := Claim{ID: "short", UID: 1, Netns: 2, Network: "x"}
	if got := Holder(c); got != "claim short (uid 1, netns 2, network x)" {
		t.Errorf("got %q", got)
	}
}

func TestHolderWithoutOwnerPid(t *testing.T) {
	c := Claim{ID: "short", UID: 1, Netns: 2, Network: "x", Interface: "lan1"}
	if got := Holder(c); got != "claim short (uid 1, netns 2, network x, lan1)" {
		t.Errorf("got %q", got)
	}
}

func ipvlanNets(t *testing.T) map[string]config.Network {
	t.Helper()
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"v","parent":"d0","prefix":"10.254.3.0/28","gateway":"10.254.3.14","driver":"ipvlan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Networks
}

// An ipvlan attachment has the parent's MAC: it records none, names none, and does not clash on MAC.
func TestIPvlanHasNoMAC(t *testing.T) {
	nets := ipvlanNets(t)
	s := State{}
	p, err := Plan(s, nets, 1000, "c", 1, []wire.Join{{Network: "v", Address: "10.254.3.2"}})
	if err != nil {
		t.Fatal(err)
	}
	s = s.Add(p)
	if _, err := Plan(s, nets, 1000, "d", 2, []wire.Join{{Network: "v", Address: "10.254.3.3"}}); err != nil {
		t.Errorf("second ipvlan attachment refused: %v", err)
	}
	if _, err := Plan(State{}, nets, 1000, "c", 1, []wire.Join{{Network: "v", Address: "10.254.3.2", MAC: "02:00:00:00:00:02"}}); err == nil {
		t.Error("ipvlan join with a MAC admitted")
	}
}

func TestMacvlanStillNeedsMAC(t *testing.T) {
	if _, err := Plan(State{}, nets(t), 1000, "c", 1, []wire.Join{{Network: "a", Address: "10.254.1.2"}}); err == nil {
		t.Error("macvlan join without a MAC admitted")
	}
}

// nets6 is the fixture for IPv6 joins: a dual-stack network "a" and an IPv4-only "b".
func nets6() map[string]config.Network {
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan","prefix6":"fd00:1::/64","gateway6":"fd00:1::1"},
		{"name":"b","parent":"d1","prefix":"10.254.2.0/28","gateway":"10.254.2.14","driver":"macvlan"}]}`))
	if err != nil {
		panic(err)
	}
	return cfg.Networks
}

func plan6(s State, ns uint64, j wire.Join) (Planned, error) {
	p, err := Plan(s, nets6(), 1000, "c", ns, []wire.Join{j})
	if err != nil {
		return Planned{}, err
	}
	return p[0], nil
}

var v6 = wire.Join{Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Address6: "fd00:1::2"}

func TestAddress6Refusals(t *testing.T) {
	cases := map[string]wire.Join{
		"no prefix6":      {Network: "b", Address: "10.254.2.2", MAC: "02:00:00:00:00:02", Address6: "fd00:2::2"},
		"outside prefix6": {Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Address6: "fd00:9::2"},
		"is the gateway6": {Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Address6: "fd00:1::1"},
		"subnet anycast":  {Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Address6: "fd00:1::"},
		"not an address":  {Network: "a", Address: "10.254.1.2", MAC: "02:00:00:00:00:02", Address6: "nope"},
	}
	for name, j := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := plan6(State{Claims: []Claim{}}, 1, j); err == nil {
				t.Fatalf("%s admitted", name)
			}
		})
	}
}

func TestAddress6AdmittedAndRecorded(t *testing.T) {
	p, err := plan6(State{Claims: []Claim{}}, 1, v6)
	if err != nil {
		t.Fatal(err)
	}
	if p.Address6 != netip.MustParseAddr("fd00:1::2") {
		t.Fatalf("address6 %s", p.Address6)
	}
	v4only, err := plan6(State{Claims: []Claim{}}, 2, wire.Join{Network: "a", Address: "10.254.1.3", MAC: "02:00:00:00:00:03"})
	if err != nil {
		t.Fatal(err)
	}
	if v4only.Address6.IsValid() {
		t.Fatal("join without address6 got one")
	}
}

func TestAddress6JSONRoundTrip(t *testing.T) {
	c := Claim{ID: "c", Address: netip.MustParseAddr("10.254.1.2"), Address6: netip.MustParseAddr("fd00:1::2")}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Claim
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Address6 != c.Address6 {
		t.Fatalf("address6 %s after round trip, want %s", back.Address6, c.Address6)
	}
}

func TestAddresses(t *testing.T) {
	v4 := Claim{Address: netip.MustParseAddr("10.254.1.2")}
	if got := Addresses(v4); got != "10.254.1.2" {
		t.Errorf("IPv4 only: %q", got)
	}
	dual := v4
	dual.Address6 = netip.MustParseAddr("fd00:1::2")
	if got := Addresses(dual); got != "10.254.1.2 fd00:1::2" {
		t.Errorf("dual: %q", got)
	}
}

func TestAddress6Clash(t *testing.T) {
	s := State{Claims: []Claim{}}
	p, err := Plan(s, nets6(), 1000, "c", 1, []wire.Join{v6})
	if err != nil {
		t.Fatal(err)
	}
	s = s.Add(p)
	other := v6
	other.Address = "10.254.1.3"
	other.MAC = "02:00:00:00:00:03"
	if _, err := Plan(s, nets6(), 1000, "d", 2, []wire.Join{other}); err == nil {
		t.Fatal("duplicate address6 from another netns admitted")
	}
}

func TestAddress6RetryMustMatch(t *testing.T) {
	s := State{Claims: []Claim{}}
	p, err := Plan(s, nets6(), 1000, "c", 1, []wire.Join{v6})
	if err != nil {
		t.Fatal(err)
	}
	s = s.Add(p)
	again, err := Plan(s, nets6(), 1000, "c", 1, []wire.Join{v6})
	if err != nil || !again[0].Existing {
		t.Fatalf("same retry not Existing: %v", err)
	}
	noV6 := v6
	noV6.Address6 = ""
	if _, err := Plan(s, nets6(), 1000, "c", 1, []wire.Join{noV6}); err == nil {
		t.Fatal("retry that drops address6 admitted")
	}
}

func TestStaleClaimBlocksMAC(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	s.Claims[0].Network = "gone"
	if _, err := Plan(s, nets(t), 1000, "d", 2, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: j1.MAC}}); err == nil {
		t.Error("MAC of a claim on an unconfigured network admitted")
	}
}

func TestMACRefusalsNameHolder(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	// Same netns, other network: the MAC is refused, not the netns re-join.
	if _, err := Plan(s, nets(t), 1000, "c", 1, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: j1.MAC}}); err == nil || !strings.Contains(err.Error(), "MAC 02:00:00:00:00:02 is taken by claim") {
		t.Errorf("same netns, other network: %v", err)
	}
}

func TestSameMACOnTwoParentsWithinOneRequest(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"},
		{"name":"c","parent":"d1","prefix":"10.254.3.0/28","gateway":"10.254.3.14","driver":"macvlan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	joins := []wire.Join{
		{Network: "a", Address: "10.254.1.2", MAC: j1.MAC},
		{Network: "c", Address: "10.254.3.2", MAC: j1.MAC},
	}
	if _, err := Plan(State{}, cfg.Networks, 1000, "c", 1, joins); err != nil {
		t.Errorf("same MAC on two parents in one request refused: %v", err)
	}
}

func TestMACRepeatedWithinOneRequest(t *testing.T) {
	joins := []wire.Join{
		{Network: "a", Address: "10.254.1.2", MAC: j1.MAC},
		{Network: "b", Address: "10.254.2.2", MAC: j1.MAC},
	}
	if _, err := Plan(State{}, nets(t), 1000, "c", 1, joins); err == nil || !strings.Contains(err.Error(), "MAC "+j1.MAC+" is taken") {
		t.Errorf("same MAC on two networks of one parent in one request: %v", err)
	}
}

// State saved before the MAC rule can hold one MAC twice; a re-join of the holder must still succeed
// whatever the claim order.
func TestRejoinIgnoresLegacyDuplicateMAC(t *testing.T) {
	own := admit(t, State{}, "c", 1, j1).Claims[0]
	other := Claim{ID: "d", UID: 1000, Netns: 2, Network: "b", Address: netip.MustParseAddr("10.254.2.2"), MAC: j1.MAC, Interface: "lan0"}
	sameNetns := other
	sameNetns.Netns, sameNetns.ID = 1, "c"
	for _, claims := range [][]Claim{{other, own}, {own, other}, {sameNetns, own}, {own, sameNetns}} {
		p, err := Plan(State{Claims: claims}, nets(t), 1000, "c", 1, []wire.Join{j1})
		if err != nil || !p[0].Existing {
			t.Errorf("re-join with legacy duplicate MAC %+v: existing=%v err=%v", claims, err == nil && p[0].Existing, err)
		}
	}
}

// netsOn parses networks given as name=parent pairs; prefixes are fixed per name.
func netsOn(t *testing.T, pairs map[string]string) map[string]config.Network {
	t.Helper()
	prefix := map[string]string{"a": "10.254.1.0/28", "b": "10.254.2.0/28", "c": "10.254.3.0/28"}
	gw := map[string]string{"a": "10.254.1.14", "b": "10.254.2.14", "c": "10.254.3.14"}
	var parts []string
	for name, parent := range pairs {
		parts = append(parts, `{"name":"`+name+`","parent":"`+parent+`","prefix":"`+prefix[name]+`","gateway":"`+gw[name]+`","driver":"macvlan"}`)
	}
	cfg, err := config.Parse([]byte(`{"uids":[1000],"networks":[` + strings.Join(parts, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Networks
}

func TestReparentedClaimStillBlocksOriginalParent(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1) // network a on d0
	// The admin moves a to d1 and restarts; the live device is still on d0.
	after := netsOn(t, map[string]string{"a": "d1", "b": "d0"})
	if _, err := Plan(s, after, 1000, "d", 2, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: j1.MAC}}); err == nil {
		t.Error("MAC on the original parent admitted after re-parent")
	}
	if _, err := Plan(s, after, 1000, "d", 2, []wire.Join{{Network: "a", Address: "10.254.1.3", MAC: j1.MAC}}); err != nil {
		t.Errorf("MAC on the new parent refused: %v", err)
	}
}

func TestRemovedNetworkKeepsRecordedParent(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1) // network a on d0
	gone := netsOn(t, map[string]string{"b": "d0", "c": "d1"})
	if _, err := Plan(s, gone, 1000, "d", 2, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: j1.MAC}}); err == nil {
		t.Error("MAC of a removed network's claim did not block its parent")
	}
	if _, err := Plan(s, gone, 1000, "d", 2, []wire.Join{{Network: "c", Address: "10.254.3.2", MAC: j1.MAC}}); err != nil {
		t.Errorf("removed network's claim blocked an unrelated parent: %v", err)
	}
}

func TestLegacyClaimWithoutParentBlocksEveryParent(t *testing.T) {
	legacy := `{"claims":[{"id":"c","uid":1000,"netns":1,"network":"a","address":"10.254.1.2","address6":"","mac":"02:00:00:00:00:02","interface":"lan0"}]}`
	var s State
	if err := json.Unmarshal([]byte(legacy), &s); err != nil {
		t.Fatal(err)
	}
	if s.Claims[0].Parent != "" {
		t.Fatalf("legacy claim got parent %q", s.Claims[0].Parent)
	}
	for _, pair := range []map[string]string{{"a": "d0", "b": "d1"}, {"a": "d1", "b": "d0"}} {
		_, err := Plan(s, netsOn(t, pair), 1000, "d", 2, []wire.Join{{Network: "b", Address: "10.254.2.2", MAC: j1.MAC}})
		if err == nil {
			t.Errorf("legacy MAC admitted with config %v", pair)
		}
	}
}

func TestRetryKeepsStoredParent(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1) // a on d0
	p, err := Plan(s, netsOn(t, map[string]string{"a": "d1"}), 1000, "c", 1, []wire.Join{j1})
	if err != nil {
		t.Fatalf("retry after re-parent refused: %v", err)
	}
	if !p[0].Existing || p[0].Parent != "d0" {
		t.Errorf("retry: existing=%v parent=%q, want existing on d0", p[0].Existing, p[0].Parent)
	}
}

func TestLegacyRetryStaysExisting(t *testing.T) {
	legacy := State{Claims: []Claim{{ID: "c", UID: 1000, Netns: 1, Network: "a", Address: netip.MustParseAddr("10.254.1.2"), MAC: j1.MAC, Interface: "lan0"}}}
	p, err := Plan(legacy, nets(t), 1000, "c", 1, []wire.Join{j1})
	if err != nil || !p[0].Existing || p[0].Parent != "" {
		t.Errorf("legacy retry: existing=%v parent=%q err=%v", err == nil && p[0].Existing, p[0].Parent, err)
	}
}

func TestClaimRecordsParentInJSON(t *testing.T) {
	s := admit(t, State{}, "c", 1, j1)
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"parent":"d0"`) {
		t.Errorf("parent not saved: %s", data)
	}
}

func TestPlanLeavesClaimBackingArrayIntact(t *testing.T) {
	held := admit(t, State{}, "existing", 1, j1).Claims[0]
	spare := Claim{ID: "spare slot"}
	backing := []Claim{held, spare}
	s := State{Claims: backing[:1], Pending: []Pending{pendingRec(3, "a", "pending")}}
	joins := []wire.Join{
		{Network: "a", Address: "10.254.1.3", MAC: "02:00:00:00:00:03"},
		{Network: "b", Address: "10.254.2.4", MAC: "02:00:00:00:00:04"},
	}
	plans, err := Plan(s, nets(t), 1000, "new", 2, joins)
	if err != nil || len(plans) != len(joins) {
		t.Fatalf("plan returned %v, %v", plans, err)
	}
	if backing[0] != held || backing[1] != spare || s.Pending[0] != pendingRec(3, "a", "pending") {
		t.Fatalf("planning changed caller state: %v, %v", backing, s.Pending)
	}
}
