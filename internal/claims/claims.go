// Package claims holds the attachment rules: which joins are admitted, refused, or repeats.
package claims

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"

	"lanatt/internal/config"
	"lanatt/internal/wire"
)

// Claim is one attachment the daemon has made. Netns inode plus network is its identity.
type Claim struct {
	ID        string     `json:"id"`
	UID       uint32     `json:"uid"`
	Netns     uint64     `json:"netns"`
	Network   string     `json:"network"`
	Address   netip.Addr `json:"address"`
	MAC       string     `json:"mac"`
	Interface string     `json:"interface"`
}

// State is every live claim.
type State struct {
	Claims []Claim `json:"claims"`
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
		p, err := planOne(s, nets, uid, id, netns, j)
		if err != nil {
			return nil, fmt.Errorf("network %q: %w", j.Network, err)
		}
		if !p.Existing && p.Interface != "" {
			if used[p.Interface] {
				return nil, fmt.Errorf("network %q: interface %q is already used in this netns", j.Network, p.Interface)
			}
			used[p.Interface] = true
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

func planOne(s State, nets map[string]config.Network, uid uint32, id string, netns uint64, j wire.Join) (Planned, error) {
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
	mac, err := parseMAC(j.MAC)
	if err != nil {
		return Planned{}, err
	}
	if j.Interface != "" && !config.ValidInterfaceName(j.Interface) {
		return Planned{}, fmt.Errorf("interface %q is not a valid name", j.Interface)
	}
	want := Claim{ID: id, UID: uid, Netns: netns, Network: n.Name, Address: addr, MAC: mac, Interface: j.Interface}
	for _, c := range s.Claims {
		if c.Network != n.Name {
			continue
		}
		if c.Netns == netns {
			if c.Address != addr || c.MAC != mac {
				return Planned{}, fmt.Errorf("netns already joined with %s %s", c.Address, c.MAC)
			}
			if j.Interface != "" && j.Interface != c.Interface {
				return Planned{}, fmt.Errorf("netns already joined as interface %q", c.Interface)
			}
			return Planned{Claim: c, Default: j.Default, Existing: true}, nil
		}
		if c.Address == addr {
			return Planned{}, fmt.Errorf("address %s is taken", addr)
		}
		if c.MAC == mac {
			return Planned{}, fmt.Errorf("MAC %s is taken", mac)
		}
	}
	return Planned{Claim: want, Default: j.Default}, nil
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

// Add records the new claims among plans.
func (s State) Add(plans []Planned) State {
	out := State{Claims: append([]Claim(nil), s.Claims...)}
	for _, p := range plans {
		if !p.Existing {
			out.Claims = append(out.Claims, p.Claim)
		}
	}
	return out
}

// Drop removes every claim uid made for container id.
func (s State) Drop(uid uint32, id string) State {
	return s.keep(func(c Claim) bool { return c.ID != id || c.UID != uid })
}

// Prune removes claims whose netns is no longer alive.
func (s State) Prune(alive map[uint64]bool) State {
	return s.keep(func(c Claim) bool { return alive[c.Netns] })
}

func (s State) keep(f func(Claim) bool) State {
	out := State{Claims: []Claim{}}
	for _, c := range s.Claims {
		if f(c) {
			out.Claims = append(out.Claims, c)
		}
	}
	return out
}

// Load reads the state file. A missing file is an empty state.
func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Claims: []Claim{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("state %s: %w", path, err)
	}
	return s, nil
}

// Save writes the state file atomically.
func Save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
