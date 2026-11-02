package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// Range is an inclusive span of IPv4 addresses a network hands out.
type Range struct {
	First, Last netip.Addr
}

func (r Range) contains(a netip.Addr) bool {
	return r.First.Compare(a) <= 0 && a.Compare(r.Last) <= 0
}

func (r Range) String() string { return r.First.String() + "-" + r.Last.String() }

// InRanges reports whether a may be claimed: any address when the network has no ranges.
func (n Network) InRanges(a netip.Addr) bool {
	if len(n.Ranges) == 0 {
		return true
	}
	for _, r := range n.Ranges {
		if r.contains(a) {
			return true
		}
	}
	return false
}

// parseRanges accepts "a-b" and CIDR entries; each must be host addresses of n, exclude the
// gateway, and not overlap another.
func parseRanges(n Network, raw []string) ([]Range, error) {
	out := make([]Range, 0, len(raw))
	for _, s := range raw {
		r, err := parseRange(s)
		if err != nil {
			return nil, err
		}
		if !n.IsHost(r.First) || !n.IsHost(r.Last) {
			return nil, fmt.Errorf("range %s is not inside the host addresses of %s", s, n.Prefix)
		}
		if r.contains(n.Gateway) {
			return nil, fmt.Errorf("range %s contains the gateway %s", s, n.Gateway)
		}
		for _, o := range out {
			if r.First.Compare(o.Last) <= 0 && o.First.Compare(r.Last) <= 0 {
				return nil, fmt.Errorf("range %s overlaps %s", s, o)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func parseRange(s string) (Range, error) {
	if first, last, ok := strings.Cut(s, "-"); ok {
		a, err := netip.ParseAddr(strings.TrimSpace(first))
		if err != nil {
			return Range{}, fmt.Errorf("range %q: %w", s, err)
		}
		b, err := netip.ParseAddr(strings.TrimSpace(last))
		if err != nil {
			return Range{}, fmt.Errorf("range %q: %w", s, err)
		}
		if !a.Is4() || !b.Is4() || b.Less(a) {
			return Range{}, fmt.Errorf("range %q must be two IPv4 addresses, low first", s)
		}
		return Range{First: a, Last: b}, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return Range{}, fmt.Errorf("range %q is neither a-b nor CIDR: %w", s, err)
	}
	if !p.Addr().Is4() || p != p.Masked() {
		return Range{}, fmt.Errorf("range %q must be an IPv4 prefix without host bits", s)
	}
	return Range{First: p.Addr(), Last: broadcast(p)}, nil
}
