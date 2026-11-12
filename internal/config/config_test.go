package config

import (
	"strings"
	"testing"
)

func cfgWith(net string) string {
	return `{"uids":[1000],"networks":[` + net + `]}`
}

func TestParseValidNetwork(t *testing.T) {
	cfg, err := Parse([]byte(cfgWith(`{"name":"lan","parent":"dummy0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"}`)))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Networks["lan"]
	if n.Parent != "dummy0" || n.Gateway.String() != "10.254.1.14" || !cfg.UIDs[1000] {
		t.Fatalf("unexpected config %+v", cfg)
	}
	if cfg.Socket == "" || cfg.State == "" {
		t.Fatal("defaults not applied")
	}
}

func TestParseIPvlan(t *testing.T) {
	cfg, err := Parse([]byte(cfgWith(`{"name":"lan","parent":"dummy0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"ipvlan"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Networks["lan"].Driver != DriverIPvlan {
		t.Fatalf("driver %q", cfg.Networks["lan"].Driver)
	}
}

func TestParseRefuses(t *testing.T) {
	cases := map[string]string{
		"unknown driver":   `{"name":"lan","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"vxlan"}`,
		"gateway outside":  `{"name":"lan","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.2.1","driver":"macvlan"}`,
		"gateway network":  `{"name":"lan","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.0","driver":"macvlan"}`,
		"gateway bcast":    `{"name":"lan","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.15","driver":"macvlan"}`,
		"host bits":        `{"name":"lan","parent":"d0","prefix":"10.254.1.1/28","gateway":"10.254.1.14","driver":"macvlan"}`,
		"ipv6":             `{"name":"lan","parent":"d0","prefix":"fd00::/64","gateway":"fd00::1","driver":"macvlan"}`,
		"bad parent":       `{"name":"lan","parent":"-x","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"}`,
		"empty name":       `{"name":"","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"}`,
		"defined twice":    `{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"},{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"}`,
		"slash-31 no host": `{"name":"lan","parent":"d0","prefix":"10.254.1.0/31","gateway":"10.254.1.1","driver":"macvlan"}`,
	}
	for name, net := range cases {
		if _, err := Parse([]byte(cfgWith(net))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(`{"uids":[0]}`)); err == nil || !strings.Contains(err.Error(), "uid 0") {
		t.Errorf("uid 0: %v", err)
	}
	if _, err := Parse([]byte(`{"uid":[1000]}`)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := Parse([]byte(`{"uids":[]}`)); err == nil {
		t.Error("empty allowlist accepted")
	}
}

func TestAllowRoot(t *testing.T) {
	off, err := Parse([]byte(`{"uids":[1000]}`))
	if err != nil {
		t.Fatal(err)
	}
	if off.Allowed(0) || !off.Allowed(1000) || off.Allowed(1001) {
		t.Errorf("without allowRoot: %+v", off.UIDs)
	}
	on, err := Parse([]byte(`{"uids":[1000],"allowRoot":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !on.Allowed(0) || !on.Allowed(1000) || on.Allowed(1001) {
		t.Errorf("with allowRoot: %+v", on)
	}
	rootOnly, err := Parse([]byte(`{"allowRoot":true}`))
	if err != nil || !rootOnly.Allowed(0) {
		t.Errorf("allowRoot without uids: %v", err)
	}
	if _, err := Parse([]byte(`{"uids":[0],"allowRoot":true}`)); err == nil {
		t.Error("uid 0 in uids accepted")
	}
}

// The kernel allows one rx handler per device, so macvlan and ipvlan cannot share a parent.
func TestParseRefusesMixedDriversOnParent(t *testing.T) {
	_, err := Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"},
		{"name":"b","parent":"d0","prefix":"10.254.2.0/28","gateway":"10.254.2.14","driver":"ipvlan"}]}`))
	if err == nil {
		t.Fatal("macvlan and ipvlan on one parent admitted")
	}
}

func TestParseOverlappingPrefixOnParent(t *testing.T) {
	net := func(name, parent, prefix, gw string) string {
		return `{"name":"` + name + `","parent":"` + parent + `","prefix":"` + prefix + `","gateway":"` + gw + `","driver":"macvlan"}`
	}
	refused := map[string]string{
		"nested, wide first":   net("a", "d0", "10.254.1.0/24", "10.254.1.1") + "," + net("b", "d0", "10.254.1.0/28", "10.254.1.14"),
		"nested, narrow first": net("a", "d0", "10.254.1.0/28", "10.254.1.14") + "," + net("b", "d0", "10.254.1.0/24", "10.254.1.1"),
		"equal":                net("a", "d0", "10.254.1.0/28", "10.254.1.14") + "," + net("b", "d0", "10.254.1.0/28", "10.254.1.14"),
	}
	for name, nets := range refused {
		_, err := Parse([]byte(`{"uids":[1000],"networks":[` + nets + `]}`))
		for _, want := range []string{`network "a"`, `network "b"`} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: want error naming %s, got %v", name, want, err)
			}
		}
	}
	admitted := map[string]string{
		"disjoint on one parent":   net("a", "d0", "10.254.1.0/28", "10.254.1.14") + "," + net("b", "d0", "10.254.1.16/28", "10.254.1.30"),
		"same prefix, two parents": net("a", "d0", "10.254.1.0/28", "10.254.1.14") + "," + net("b", "d1", "10.254.1.0/28", "10.254.1.14"),
		// The layout test/accept.sh uses on one parent.
		"accept layout": net("lab", "d0", "10.254.1.0/28", "10.254.1.14") + "," +
			`{"name":"lab2","parent":"d0","prefix":"10.254.1.16/28","gateway":"10.254.1.30","driver":"macvlan","ranges":["10.254.1.17-10.254.1.20"]}` + "," +
			`{"name":"lab6","parent":"d0","prefix":"10.254.1.48/28","gateway":"10.254.1.62","driver":"macvlan","prefix6":"fd77:254::/64","gateway6":"fd77:254::1"}`,
	}
	for name, nets := range admitted {
		if _, err := Parse([]byte(`{"uids":[1000],"networks":[` + nets + `]}`)); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
}

func TestParseRefusesOverlappingPrefix6OnParent(t *testing.T) {
	_, err := Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan","prefix6":"fd00:10::/64"},
		{"name":"b","parent":"d0","prefix":"10.254.2.0/28","gateway":"10.254.2.14","driver":"macvlan","prefix6":"fd00:10::/48"}]}`))
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlapping prefix6 on one parent: %v", err)
	}
	_, err = Parse([]byte(`{"uids":[1000],"networks":[
		{"name":"a","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan","prefix6":"fd00:10::/64"},
		{"name":"b","parent":"d0","prefix":"10.254.2.0/28","gateway":"10.254.2.14","driver":"macvlan","prefix6":"fd00:11::/64"}]}`))
	if err != nil {
		t.Fatalf("disjoint prefix6 refused: %v", err)
	}
}

func TestParseIPv6(t *testing.T) {
	cfg, err := Parse([]byte(cfgWith(`{"name":"lan","parent":"dummy0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan","prefix6":"fd00:1::/64","gateway6":"fd00:1::1"}`)))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Networks["lan"]
	if !n.HasIPv6() || n.Prefix6.String() != "fd00:1::/64" || n.Gateway6.String() != "fd00:1::1" {
		t.Fatalf("unexpected IPv6 %+v", n)
	}
	plain, err := Parse([]byte(cfgWith(`{"name":"lan","parent":"dummy0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Networks["lan"].HasIPv6() {
		t.Fatal("network without prefix6 has IPv6")
	}
}

func TestParseNoSLAAC(t *testing.T) {
	cfg, err := Parse([]byte(cfgWith(`{"name":"lan","parent":"dummy0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan","noSLAAC":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Networks["lan"].NoSLAAC {
		t.Error("noSLAAC not set on an IPv4-only network")
	}
	plain, err := Parse([]byte(cfgWith(`{"name":"lan","parent":"dummy0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Networks["lan"].NoSLAAC {
		t.Error("noSLAAC set without the option")
	}
}

func TestParseIPv6Refuses(t *testing.T) {
	base := `"name":"lan","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan"`
	cases := map[string]string{
		"global without allowGlobal6":  `"prefix6":"2001:db8:1::/64"`,
		"gateway6 without prefix6":     `"gateway6":"fd00:1::1"`,
		"allowGlobal6 without prefix6": `"allowGlobal6":true`,
		"shorter than /48":             `"prefix6":"fd00::/47"`,
		"longer than /64":              `"prefix6":"fd00:1::/80"`,
		"host bits":                    `"prefix6":"fd00:1::5/64"`,
		"link-local":                   `"prefix6":"fe80::/64","allowGlobal6":true`,
		"multicast":                    `"prefix6":"ff02::/64","allowGlobal6":true`,
		"v4-mapped":                    `"prefix6":"::ffff:0:0/96"`,
		"gateway6 outside":             `"prefix6":"fd00:1::/64","gateway6":"fd00:2::1"`,
		"gateway6 anycast":             `"prefix6":"fd00:1::/64","gateway6":"fd00:1::"`,
		"gateway6 is prefix":           `"prefix6":"fd00:1::/64","gateway6":"fd00:1::"`,
		"noSLAAC with prefix6":         `"prefix6":"fd00:1::/64","noSLAAC":true`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(cfgWith("{" + base + "," + extra + "}"))); err == nil {
				t.Fatalf("%s admitted", extra)
			}
		})
	}
	_, err := Parse([]byte(cfgWith(`{` + base + `,"prefix6":"fd00:1::/64","noSLAAC":true}`)))
	if err == nil || !strings.Contains(err.Error(), "noSLAAC") {
		t.Errorf("noSLAAC with prefix6: %v", err)
	}
}

func TestParseIPv6GlobalWithAllowGlobal6(t *testing.T) {
	if _, err := Parse([]byte(cfgWith(`{"name":"lan","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"macvlan","prefix6":"2001:db8:1::/64","allowGlobal6":true}`))); err != nil {
		t.Fatal(err)
	}
}
