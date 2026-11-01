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

func TestParseRefuses(t *testing.T) {
	cases := map[string]string{
		"ipvlan driver":    `{"name":"lan","parent":"d0","prefix":"10.254.1.0/28","gateway":"10.254.1.14","driver":"ipvlan"}`,
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
