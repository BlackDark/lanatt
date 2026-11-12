// Package config loads the root-owned daemon config: allowlist and networks.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"

	"lanatt/internal/wire"
)

// DriverMacvlan creates a bridge-mode macvlan: each attachment has its own MAC.
const DriverMacvlan = "macvlan"

// DriverIPvlan creates an L2-mode ipvlan: attachments share the parent's MAC, so a join
// carries no MAC. See ADR-0011.
const DriverIPvlan = "ipvlan"

// Network is an administrator-defined LAN that callers join by name.
type Network struct {
	Name    string
	Parent  string
	Prefix  netip.Prefix
	Gateway netip.Addr
	Driver  string
	// Ranges limits which addresses callers may claim; empty means the whole prefix.
	Ranges []Range
	// Prefix6 and Gateway6 enable IPv6; zero means none. See ADR-0012.
	Prefix6  netip.Prefix
	Gateway6 netip.Addr
	// NoSLAAC stops SLAAC from the LAN on an IPv4-only network's attachments. See ADR-0012.
	NoSLAAC bool
}

// Config is the validated daemon config.
type Config struct {
	Socket string
	State  string
	UIDs   map[uint32]bool
	// AllowRoot lets UID 0 callers, the hooks of rootful containers, join any pid's netns.
	AllowRoot bool
	Networks  map[string]Network
}

type rawNetwork struct {
	Name    string   `json:"name"`
	Parent  string   `json:"parent"`
	Prefix  string   `json:"prefix"`
	Gateway string   `json:"gateway"`
	Driver  string   `json:"driver"`
	Ranges  []string `json:"ranges"`
	// Prefix6 switches IPv6 on; see ADR-0012.
	Prefix6      string `json:"prefix6"`
	Gateway6     string `json:"gateway6"`
	AllowGlobal6 bool   `json:"allowGlobal6"`
	NoSLAAC      bool   `json:"noSLAAC"`
}

type rawConfig struct {
	Socket    string       `json:"socket"`
	State     string       `json:"state"`
	UIDs      []uint32     `json:"uids"`
	AllowRoot bool         `json:"allowRoot"`
	Networks  []rawNetwork `json:"networks"`
}

const defaultState = "/run/lanatt/claims.json"

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,14}$`)

// Allowed reports whether uid may call the daemon.
func (c Config) Allowed(uid uint32) bool {
	return c.UIDs[uid] || (uid == 0 && c.AllowRoot)
}

// ValidInterfaceName reports whether name is a safe Linux interface name. "all" and "default"
// are not devices: their net.ipv6.conf entries set every interface in the netns.
func ValidInterfaceName(name string) bool {
	return ifaceName.MatchString(name) && name != "all" && name != "default"
}

// Load reads and validates the config file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}

// Parse validates config JSON.
func Parse(data []byte) (Config, error) {
	var raw rawConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	cfg := Config{Socket: raw.Socket, State: raw.State, UIDs: map[uint32]bool{}, AllowRoot: raw.AllowRoot, Networks: map[string]Network{}}
	if cfg.Socket == "" {
		cfg.Socket = wire.DefaultSocket
	}
	if cfg.State == "" {
		cfg.State = defaultState
	}
	for _, uid := range raw.UIDs {
		if uid == 0 {
			return Config{}, errors.New(`config: uid 0 cannot be in uids, set "allowRoot": true instead`)
		}
		cfg.UIDs[uid] = true
	}
	if len(cfg.UIDs) == 0 && !cfg.AllowRoot {
		return Config{}, errors.New("config: uids is empty, so every caller would be refused")
	}
	driverOn := map[string]string{}
	// Claims are checked per network, so overlapping prefixes on one parent would hand out the same
	// address on one segment unseen.
	v4On := map[string][]placed{}
	// DAD catches a duplicate IPv6 address only while both networks are up.
	v6On := map[string][]placed{}
	for _, rn := range raw.Networks {
		n, err := parseNetwork(rn)
		if err != nil {
			return Config{}, fmt.Errorf("config: network %q: %w", rn.Name, err)
		}
		if _, dup := cfg.Networks[n.Name]; dup {
			return Config{}, fmt.Errorf("config: network %q defined twice", n.Name)
		}
		if other, ok := driverOn[n.Parent]; ok && other != n.Driver {
			return Config{}, fmt.Errorf("config: network %q: parent %q already carries %s networks; macvlan and ipvlan cannot share a parent", n.Name, n.Parent, other)
		}
		driverOn[n.Parent] = n.Driver
		if other, ok := overlapping(v4On[n.Parent], n.Prefix); ok {
			return Config{}, fmt.Errorf("config: network %q: prefix %s overlaps %s of network %q on parent %q", n.Name, n.Prefix, other.prefix, other.name, n.Parent)
		}
		v4On[n.Parent] = append(v4On[n.Parent], placed{n.Name, n.Prefix})
		if n.HasIPv6() {
			if other, ok := overlapping(v6On[n.Parent], n.Prefix6); ok {
				return Config{}, fmt.Errorf("config: network %q: prefix6 %s overlaps %s of network %q on parent %q", n.Name, n.Prefix6, other.prefix, other.name, n.Parent)
			}
			v6On[n.Parent] = append(v6On[n.Parent], placed{n.Name, n.Prefix6})
		}
		cfg.Networks[n.Name] = n
	}
	return cfg, nil
}

// placed is a prefix already taken on a parent, and the network that took it.
type placed struct {
	name   string
	prefix netip.Prefix
}

// overlapping returns the first taken prefix that overlaps p.
func overlapping(taken []placed, p netip.Prefix) (placed, bool) {
	for _, t := range taken {
		if t.prefix.Overlaps(p) {
			return t, true
		}
	}
	return placed{}, false
}

func parseNetwork(rn rawNetwork) (Network, error) {
	if rn.Name == "" {
		return Network{}, errors.New("name is empty")
	}
	if rn.Driver != DriverMacvlan && rn.Driver != DriverIPvlan {
		return Network{}, fmt.Errorf("driver %q is not supported, use %q or %q", rn.Driver, DriverMacvlan, DriverIPvlan)
	}
	if !ValidInterfaceName(rn.Parent) {
		return Network{}, fmt.Errorf("parent %q is not an interface name", rn.Parent)
	}
	prefix, err := netip.ParsePrefix(rn.Prefix)
	if err != nil {
		return Network{}, fmt.Errorf("prefix: %w", err)
	}
	if !prefix.Addr().Is4() {
		return Network{}, errors.New("prefix must be IPv4")
	}
	if prefix != prefix.Masked() {
		return Network{}, fmt.Errorf("prefix %s has host bits set", prefix)
	}
	if prefix.Bits() > 30 {
		return Network{}, fmt.Errorf("prefix %s has no room for hosts", prefix)
	}
	gw, err := netip.ParseAddr(rn.Gateway)
	if err != nil {
		return Network{}, fmt.Errorf("gateway: %w", err)
	}
	n := Network{Name: rn.Name, Parent: rn.Parent, Prefix: prefix, Gateway: gw, Driver: rn.Driver, NoSLAAC: rn.NoSLAAC}
	if !n.IsHost(gw) {
		return Network{}, fmt.Errorf("gateway %s is not a host address in %s", gw, prefix)
	}
	if n.Ranges, err = parseRanges(n, rn.Ranges); err != nil {
		return Network{}, err
	}
	if err := parseIPv6(&n, rn); err != nil {
		return Network{}, err
	}
	return n, nil
}

// parseIPv6 reads the optional IPv6 part of a network. Without prefix6 the network has no
// IPv6, so gateway6 and allowGlobal6 would do nothing and are refused.
func parseIPv6(n *Network, rn rawNetwork) error {
	if rn.Prefix6 == "" {
		if rn.Gateway6 != "" || rn.AllowGlobal6 {
			return errors.New("gateway6 and allowGlobal6 need prefix6")
		}
		return nil
	}
	p, err := netip.ParsePrefix(rn.Prefix6)
	if err != nil {
		return fmt.Errorf("prefix6: %w", err)
	}
	if !p.Addr().Is6() || p.Addr().Is4In6() {
		return fmt.Errorf("prefix6 %s must be IPv6", p)
	}
	if p.Bits() < 48 || p.Bits() > 64 {
		return fmt.Errorf("prefix6 %s must be /48 to /64", p)
	}
	if p != p.Masked() {
		return fmt.Errorf("prefix6 %s has host bits set", p)
	}
	a := p.Addr()
	if a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsLoopback() || a.IsUnspecified() {
		return fmt.Errorf("prefix6 %s is not a unicast LAN prefix", p)
	}
	if !rn.AllowGlobal6 && a.As16()[0] != 0xfd {
		return fmt.Errorf("prefix6 %s is not ULA (fd00::/8); set allowGlobal6 for a global prefix", p)
	}
	if rn.NoSLAAC {
		// prefix6 networks already take no SLAAC; the option would do nothing.
		return errors.New("noSLAAC is for IPv4-only networks; prefix6 already takes no SLAAC")
	}
	n.Prefix6 = p
	if rn.Gateway6 == "" {
		return nil
	}
	gw, err := netip.ParseAddr(rn.Gateway6)
	if err != nil {
		return fmt.Errorf("gateway6: %w", err)
	}
	if !n.IsHost6(gw) {
		return fmt.Errorf("gateway6 %s is not a host address in %s", gw, p)
	}
	n.Gateway6 = gw
	return nil
}

// HasIPv6 reports whether the network carries IPv6 addresses.
func (n Network) HasIPv6() bool {
	return n.Prefix6.IsValid()
}

// IsHost6 reports whether a is in Prefix6 and is not its subnet-router anycast address.
func (n Network) IsHost6(a netip.Addr) bool {
	return n.HasIPv6() && n.Prefix6.Contains(a) && a != n.Prefix6.Masked().Addr()
}

// IsHost reports whether a is inside the prefix and is neither its network nor broadcast address.
func (n Network) IsHost(a netip.Addr) bool {
	return a.Is4() && n.Prefix.Contains(a) && a != n.Prefix.Addr() && a != broadcast(n.Prefix)
}

func broadcast(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	for i := p.Bits(); i < 32; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	return netip.AddrFrom4(b)
}
