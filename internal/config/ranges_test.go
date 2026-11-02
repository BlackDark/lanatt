package config

import (
	"net/netip"
	"testing"
)

func netWith(ranges string) string {
	return `{"uids":[1000],"networks":[{"name":"lan","parent":"d0","prefix":"10.0.1.0/24","gateway":"10.0.1.1","driver":"macvlan","ranges":` + ranges + `}]}`
}

func TestRanges(t *testing.T) {
	cfg, err := Parse([]byte(netWith(`["10.0.1.64-10.0.1.69","10.0.1.128/30"]`)))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Networks["lan"]
	for addr, want := range map[string]bool{
		"10.0.1.64": true, "10.0.1.69": true, "10.0.1.63": false, "10.0.1.70": false,
		"10.0.1.129": true, "10.0.1.131": true, "10.0.1.132": false,
	} {
		if got := n.InRanges(netip.MustParseAddr(addr)); got != want {
			t.Errorf("InRanges(%s)=%v", addr, got)
		}
	}
	none, err := Parse([]byte(netWith(`[]`)))
	if err != nil || !none.Networks["lan"].InRanges(netip.MustParseAddr("10.0.1.200")) {
		t.Errorf("no ranges must allow the whole prefix: %v", err)
	}
}

func TestRangesRefused(t *testing.T) {
	for name, r := range map[string]string{
		"outside prefix":   `["10.0.2.1-10.0.2.5"]`,
		"contains gateway": `["10.0.1.1-10.0.1.5"]`,
		"broadcast":        `["10.0.1.250-10.0.1.255"]`,
		"reversed":         `["10.0.1.9-10.0.1.5"]`,
		"overlap":          `["10.0.1.64-10.0.1.69","10.0.1.68/30"]`,
		"host bits":        `["10.0.1.65/30"]`,
		"garbage":          `["nope"]`,
		"ipv6":             `["fd00::1-fd00::5"]`,
	} {
		if _, err := Parse([]byte(netWith(r))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
