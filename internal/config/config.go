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

// DriverMacvlan is the only driver v1 accepts.
const DriverMacvlan = "macvlan"

// Network is an administrator-defined LAN that callers join by name.
type Network struct {
	Name    string
	Parent  string
	Prefix  netip.Prefix
	Gateway netip.Addr
	Driver  string
}

// Config is the validated daemon config.
type Config struct {
	Socket   string
	State    string
	UIDs     map[uint32]bool
	Networks map[string]Network
}

type rawNetwork struct {
	Name    string `json:"name"`
	Parent  string `json:"parent"`
	Prefix  string `json:"prefix"`
	Gateway string `json:"gateway"`
	Driver  string `json:"driver"`
}

type rawConfig struct {
	Socket   string       `json:"socket"`
	State    string       `json:"state"`
	UIDs     []uint32     `json:"uids"`
	Networks []rawNetwork `json:"networks"`
}

const defaultState = "/var/lib/lanatt/claims.json"

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,14}$`)

// ValidInterfaceName reports whether name is a safe Linux interface name.
func ValidInterfaceName(name string) bool { return ifaceName.MatchString(name) }

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
	cfg := Config{Socket: raw.Socket, State: raw.State, UIDs: map[uint32]bool{}, Networks: map[string]Network{}}
	if cfg.Socket == "" {
		cfg.Socket = wire.DefaultSocket
	}
	if cfg.State == "" {
		cfg.State = defaultState
	}
	for _, uid := range raw.UIDs {
		if uid == 0 {
			return Config{}, errors.New("config: uid 0 cannot be allowlisted")
		}
		cfg.UIDs[uid] = true
	}
	if len(cfg.UIDs) == 0 {
		return Config{}, errors.New("config: uids is empty, so every caller would be refused")
	}
	for _, rn := range raw.Networks {
		n, err := parseNetwork(rn)
		if err != nil {
			return Config{}, fmt.Errorf("config: network %q: %w", rn.Name, err)
		}
		if _, dup := cfg.Networks[n.Name]; dup {
			return Config{}, fmt.Errorf("config: network %q defined twice", n.Name)
		}
		cfg.Networks[n.Name] = n
	}
	return cfg, nil
}

func parseNetwork(rn rawNetwork) (Network, error) {
	if rn.Name == "" {
		return Network{}, errors.New("name is empty")
	}
	if rn.Driver != DriverMacvlan {
		return Network{}, fmt.Errorf("driver %q is not supported, only %q", rn.Driver, DriverMacvlan)
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
	n := Network{Name: rn.Name, Parent: rn.Parent, Prefix: prefix, Gateway: gw, Driver: rn.Driver}
	if !n.IsHost(gw) {
		return Network{}, fmt.Errorf("gateway %s is not a host address in %s", gw, prefix)
	}
	return n, nil
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
