package daemon

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"lanatt/internal/claims"
	"lanatt/internal/config"
)

func TestDecideRoute(t *testing.T) {
	cases := []struct {
		has, want bool
		out       RouteAction
	}{
		{false, false, RouteAdd},
		{true, false, RouteKeep},
		{true, true, RouteReplace},
		{false, true, RouteReplace},
	}
	for _, c := range cases {
		if got := DecideRoute(c.has, c.want); got != c.out {
			t.Errorf("DecideRoute(%v,%v)=%v want %v", c.has, c.want, got, c.out)
		}
	}
}

func TestLinkAddArgsPerDriver(t *testing.T) {
	p := claims.Planned{Claim: claims.Claim{MAC: "02:42:0a:00:01:33"}}
	mac := strings.Join(linkAddArgs(config.Network{Parent: "eth0", Driver: config.DriverMacvlan}, p, "lt1"), " ")
	if mac != "link add link eth0 name lt1 address 02:42:0a:00:01:33 type macvlan mode bridge" {
		t.Errorf("macvlan: %s", mac)
	}
	ipv := strings.Join(linkAddArgs(config.Network{Parent: "eth0", Driver: config.DriverIPvlan}, p, "lt1"), " ")
	if ipv != "link add link eth0 name lt1 type ipvlan mode l2" {
		t.Errorf("ipvlan: %s", ipv)
	}
}

func TestIPv6SysctlsPerNetwork(t *testing.T) {
	v4 := config.Network{}
	if got := ipv6Sysctls(v4, claims.Planned{}); len(got) != 0 {
		t.Errorf("IPv4-only: %v", got)
	}
	dual := config.Network{Prefix6: netip.MustParsePrefix("fd00:1::/64")}
	want := [][2]string{{"disable_ipv6", "0"}, {"accept_ra", "0"}, {"autoconf", "0"}, {"accept_dad", "1"}, {"dad_transmits", "1"}}
	if got := ipv6Sysctls(dual, claims.Planned{}); !slices.Equal(got, want) {
		t.Errorf("dual-stack: %v", got)
	}
	noSLAAC := config.Network{NoSLAAC: true}
	if got := ipv6Sysctls(noSLAAC, claims.Planned{}); !slices.Equal(got, [][2]string{{"accept_ra", "0"}, {"autoconf", "0"}}) {
		t.Errorf("noSLAAC: %v", got)
	}
}

func TestDADState(t *testing.T) {
	addr := netip.MustParseAddr("2001:db8:77::20")
	cases := map[string]struct{ out, want string }{
		"preferred":  {"    inet6 2001:db8:77::20/64 scope global \n       valid_lft forever preferred_lft forever\n", ""},
		"tentative":  {"    inet6 2001:db8:77::20/64 scope global tentative \n", "tentative"},
		"dadfailed":  {"    inet6 2001:db8:77::20/64 scope global dadfailed tentative \n", "dadfailed"},
		"other addr": {"    inet6 2001:db8:77::21/64 scope global dadfailed \n", "missing"},
		"no output":  {"", "missing"},
	}
	for name, c := range cases {
		if got := dadState(c.out, addr); got != c.want {
			t.Errorf("%s: got %q want %q", name, got, c.want)
		}
	}
}

func TestSLAACDefaultPolicy(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		slaac, replace, gw, addr bool
		wantStatic               bool
	}{
		{"static route fallback", false, false, true, true, true},
		{"RA with configured gateway", true, false, true, true, false},
		{"explicit override", true, true, true, true, true},
		{"no gateway", true, true, false, true, false},
		{"no static address", true, true, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := config.Network{Prefix6: netip.MustParsePrefix("fd00:1::/64"), AllowSLAAC: tt.slaac}
			p := claims.Planned{Default: tt.replace}
			if tt.gw {
				n.Gateway6 = netip.MustParseAddr("fd00:1::1")
			}
			if tt.addr {
				p.Address6 = netip.MustParseAddr("fd00:1::2")
			}
			if got := useStaticDefault6(n, p); got != tt.wantStatic {
				t.Errorf("static default = %v, want %v", got, tt.wantStatic)
			}
			if !tt.slaac {
				return
			}
			settings := make(map[string]string)
			for _, kv := range ipv6Sysctls(n, p) {
				settings[kv[0]] = kv[1]
			}
			for key, want := range map[string]string{
				"disable_ipv6": "0", "accept_ra": "2", "accept_ra_pinfo": "1",
				"autoconf": "1", "accept_dad": "1", "dad_transmits": "1",
			} {
				if got := settings[key]; got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			wantRouter := "1"
			if tt.wantStatic {
				wantRouter = "0"
			}
			if settings["accept_ra_defrtr"] != wantRouter {
				t.Errorf("unexpected RA default policy: %v", settings)
			}
		})
	}
}
