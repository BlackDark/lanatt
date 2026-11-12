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

func TestStaleTemps(t *testing.T) {
	out := "7: ltf00003290@latparent: <BROADCAST,MULTICAST> mtu 1500 qdisc noop state DOWN\\    link/ether 02:42:0a:fe:01:0a brd ff:ff:ff:ff:ff:ff\n" +
		"8: shim0@eno1: <BROADCAST,MULTICAST,UP> mtu 1500\n" +
		"9: ltnothex1@eno1: <BROADCAST> mtu 1500\n" +
		"10: lt0000000a12: <BROADCAST> mtu 1500\n"
	got := StaleTemps(out)
	if len(got) != 2 || got[0] != "ltf00003290" || got[1] != "lt0000000a12" {
		t.Fatalf("got %v", got)
	}
	if name := TempName(0x1f0000329, 3); name != "ltf00003293" || len(StaleTemps("1: "+name+": x")) != 1 {
		t.Fatalf("TempName %q not recognised", name)
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
	if got := ipv6Sysctls(v4); len(got) != 0 {
		t.Errorf("IPv4-only: %v", got)
	}
	dual := config.Network{Prefix6: netip.MustParsePrefix("fd00:1::/64")}
	want := [][2]string{{"disable_ipv6", "0"}, {"accept_ra", "0"}, {"autoconf", "0"}, {"accept_dad", "1"}, {"dad_transmits", "1"}}
	if got := ipv6Sysctls(dual); !slices.Equal(got, want) {
		t.Errorf("dual-stack: %v", got)
	}
	noSLAAC := config.Network{NoSLAAC: true}
	if got := ipv6Sysctls(noSLAAC); !slices.Equal(got, [][2]string{{"accept_ra", "0"}, {"autoconf", "0"}}) {
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
