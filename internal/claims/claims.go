// Package claims holds the attachment rules: which joins are admitted, refused, or repeats.
package claims

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"lanatt/internal/config"
	"lanatt/internal/wire"
)

// Claim is one attachment the daemon has made. Netns inode plus network is its identity.
// The owner fields name the process that asked for it; the claim lives only while that
// process still exists in the same netns, in the same boot.
type Claim struct {
	ID      string `json:"id"`
	UID     uint32 `json:"uid"`
	Netns   uint64 `json:"netns"`
	Network string `json:"network"`
	// Parent is the NIC the device was made on. It is kept when the network is edited, so the MAC
	// check still names that NIC. Empty in state saved before this field.
	Parent    string     `json:"parent"`
	Address   netip.Addr `json:"address"`
	Address6  netip.Addr `json:"address6"`
	MAC       string     `json:"mac"`
	Interface string     `json:"interface"`
	BootID    string     `json:"boot_id"`
	OwnerPid  int        `json:"owner_pid"`
	// OwnerStart is the owner's start time, so a reused pid does not own the claim.
	OwnerStart string `json:"owner_start"`
}

// Owner is the process and boot a new claim is made under.
type Owner struct {
	BootID string
	Pid    int
	Start  string
}

// State is every live claim and every device whose removal is not yet verified.
type State struct {
	Claims  []Claim   `json:"claims"`
	Pending []Pending `json:"pending,omitempty"`
}

// Planned is an admitted join. Existing means the attachment is already in place.
type Planned struct {
	Claim
	Default  bool
	Existing bool
}

// Plan admits or refuses all joins for one workload netns. It does not change s.
func Plan(s State, nets map[string]config.Network, uid uint32, id string, netns uint64, joins []wire.Join) ([]Planned, error) {
	if len(joins) == 0 {
		return nil, errors.New("no joins")
	}
	used := map[string]bool{}
	for _, c := range s.Claims {
		if c.Netns == netns {
			used[c.Interface] = true
		}
	}
	seen := map[string]bool{}
	defaults := 0
	held := make([]Claim, len(s.Claims), len(s.Claims)+len(joins))
	copy(held, s.Claims)
	plans := make([]Planned, 0, len(joins))
	for _, j := range joins {
		if seen[j.Network] {
			return nil, fmt.Errorf("network %q joined twice in one request", j.Network)
		}
		seen[j.Network] = true
		if j.Default {
			defaults++
		}
		if defaults > 1 {
			return nil, errors.New("more than one join asks for the default route")
		}
		// Earlier joins of this request count as claims, so a MAC may not repeat on one parent in one request.
		p, err := planOne(held, nets, uid, id, netns, j)
		if err != nil {
			return nil, fmt.Errorf("network %q: %w", j.Network, err)
		}
		if !p.Existing && p.Interface != "" {
			if used[p.Interface] {
				return nil, fmt.Errorf("network %q: interface %q is already used in this netns", j.Network, p.Interface)
			}
			used[p.Interface] = true
		}
		if !p.Existing {
			held = append(held, p.Claim)
		}
		plans = append(plans, p)
	}
	for i := range plans {
		if plans[i].Interface == "" {
			plans[i].Interface = nextName(used)
			used[plans[i].Interface] = true
		}
	}
	return plans, nil
}

func planOne(held []Claim, nets map[string]config.Network, uid uint32, id string, netns uint64, j wire.Join) (Planned, error) {
	n, ok := nets[j.Network]
	if !ok {
		return Planned{}, errors.New("unknown network")
	}
	addr, err := netip.ParseAddr(j.Address)
	if err != nil {
		return Planned{}, fmt.Errorf("address: %w", err)
	}
	if !n.IsHost(addr) {
		return Planned{}, fmt.Errorf("address %s is not a host address in %s", addr, n.Prefix)
	}
	if addr == n.Gateway {
		return Planned{}, fmt.Errorf("address %s is the gateway", addr)
	}
	if !n.InRanges(addr) {
		return Planned{}, fmt.Errorf("address %s is outside the network's ranges", addr)
	}
	addr6, err := planAddr6(n, j.Address6)
	if err != nil {
		return Planned{}, err
	}
	mac, err := joinMAC(n, j.MAC)
	if err != nil {
		return Planned{}, err
	}
	if j.Interface != "" && !config.ValidInterfaceName(j.Interface) {
		return Planned{}, fmt.Errorf("interface %q is not a valid name", j.Interface)
	}
	want := Claim{ID: id, UID: uid, Netns: netns, Network: n.Name, Parent: n.Parent, Address: addr, Address6: addr6, MAC: mac, Interface: j.Interface}
	for _, c := range held {
		if c.Network == n.Name && c.Netns == netns {
			if c.Address != addr || c.Address6 != addr6 || c.MAC != mac {
				identity := fmt.Sprintf("%s %s", c.Address, c.MAC)
				if c.Address6.IsValid() {
					identity += " " + c.Address6.String()
				}
				return Planned{}, fmt.Errorf("netns already joined with %s", identity)
			}
			if j.Interface != "" && j.Interface != c.Interface {
				return Planned{}, fmt.Errorf("netns already joined as interface %q", c.Interface)
			}
			return Planned{Claim: c, Default: j.Default, Existing: true}, nil
		}
	}
	for _, c := range held {
		if c.Network == n.Name && c.Address == addr {
			return Planned{}, fmt.Errorf("address %s is taken by %s", addr, Holder(c))
		}
		if c.Network == n.Name && addr6.IsValid() && c.Address6 == addr6 {
			return Planned{}, fmt.Errorf("address6 %s is taken by %s", addr6, Holder(c))
		}
		// The check is per parent NIC. Two NICs on one switch are not checked (known gap, ADR-0012).
		if mac != "" && c.MAC == mac && mayShareParent(c, n.Parent) {
			return Planned{}, fmt.Errorf("MAC %s is taken by %s", mac, Holder(c))
		}
	}
	return Planned{Claim: want, Default: j.Default}, nil
}

// mayShareParent reports whether claim c may hold its MAC on parent. Current config is not
// used: a live device stays on the parent it was made on. A claim without a recorded parent
// may be on any parent, so it counts everywhere.
func mayShareParent(c Claim, parent string) bool {
	return c.Parent == "" || c.Parent == parent
}

// planAddr6 checks an optional IPv6 address. Zero means the join asks for none.
func planAddr6(n config.Network, s string) (netip.Addr, error) {
	if s == "" {
		return netip.Addr{}, nil
	}
	if !n.HasIPv6() {
		return netip.Addr{}, errors.New("address6 needs prefix6 on the network")
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("address6: %w", err)
	}
	if !n.IsHost6(a) {
		return netip.Addr{}, fmt.Errorf("address6 %s is not a host address in %s", a, n.Prefix6)
	}
	if a == n.Gateway6 {
		return netip.Addr{}, fmt.Errorf("address6 %s is the gateway6", a)
	}
	return a, nil
}

// joinMAC is the MAC a claim records. An ipvlan attachment has the parent's MAC, so it has
// none of its own and must not name one.
func joinMAC(n config.Network, s string) (string, error) {
	if n.Driver == config.DriverIPvlan {
		if s != "" {
			return "", errors.New("MAC is not set on an ipvlan network: attachments share the parent's MAC")
		}
		return "", nil
	}
	return parseMAC(s)
}

func parseMAC(s string) (string, error) {
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("MAC %q is not a 48-bit address", s)
	}
	if hw[0]&1 != 0 {
		return "", fmt.Errorf("MAC %s is multicast", hw)
	}
	if string(hw) == string(make([]byte, 6)) {
		return "", fmt.Errorf("MAC %s is all zeros", hw)
	}
	return hw.String(), nil
}

func nextName(used map[string]bool) string {
	for i := 0; ; i++ {
		name := fmt.Sprintf("lan%d", i)
		if !used[name] {
			return name
		}
	}
}

// Own stamps the new claims in plans with the process that asked for them.
func Own(plans []Planned, o Owner) []Planned {
	for i := range plans {
		if !plans[i].Existing {
			plans[i].BootID = o.BootID
			plans[i].OwnerPid = o.Pid
			plans[i].OwnerStart = o.Start
		}
	}
	return plans
}

// Add records the new claims among plans.
func (s State) Add(plans []Planned) State {
	out := s.clone()
	for _, p := range plans {
		if !p.Existing {
			out.Claims = append(out.Claims, p.Claim)
		}
	}
	return out
}

// Drop removes every claim uid made for container id.
func (s State) Drop(uid uint32, id string) State {
	out := s.clone()
	out.Claims = []Claim{}
	for _, c := range s.Claims {
		if c.ID != id || c.UID != uid {
			out.Claims = append(out.Claims, c)
		}
	}
	return out
}

// Prune removes claims that alive rejects and returns them.
func (s State) Prune(alive func(Claim) bool) (State, []Claim) {
	out := State{Claims: []Claim{}, Pending: s.Pending}
	var dropped []Claim
	for _, c := range s.Claims {
		if alive(c) {
			out.Claims = append(out.Claims, c)
		} else {
			dropped = append(dropped, c)
		}
	}
	return out, dropped
}

// Holder names a claim in a refusal, so the caller can find what holds the address or MAC.
func Holder(c Claim) string {
	s := fmt.Sprintf("claim %s (uid %d, netns %d, network %s", ShortID(c.ID), c.UID, c.Netns, c.Network)
	if c.Interface != "" {
		s += ", " + c.Interface
	}
	if c.OwnerPid > 0 {
		s += fmt.Sprintf(", pid %d", c.OwnerPid)
	}
	return s + ")"
}

// Addresses lists the claim's addresses: IPv4, then IPv6 when set.
func Addresses(c Claim) string {
	if c.Address6.IsValid() {
		return c.Address.String() + " " + c.Address6.String()
	}
	return c.Address.String()
}

// ShortID is the container id as `podman ps` shows it.
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
