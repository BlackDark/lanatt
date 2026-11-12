package daemon

import (
	"slices"
	"strings"
	"testing"
)

func TestRouteMetric(t *testing.T) {
	cases := map[string]string{
		"default via fd77:254::1 dev lan0 proto static metric 100":          "100",
		"default via fe80::1 dev eth0 proto ra metric 1024 expires 1798sec": "1024",
		"default via 10.0.0.1 dev eth0":                                     "",
	}
	for line, want := range cases {
		if got := routeMetric(strings.Fields(line)); got != want {
			t.Errorf("%q: metric %q, want %q", line, got, want)
		}
	}
}

func TestParseRoutes(t *testing.T) {
	single := "default via fe80::1 dev eth0 proto ra metric 1024 expires 1798sec linkdown\n"
	want := []string{"default", "via", "fe80::1", "dev", "eth0", "proto", "ra", "metric", "1024"}
	if got := parseRoutes(single); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("single: %v", got)
	}
	multi := "default proto static metric 100 \n\tnexthop via 10.0.0.1 dev eth0 weight 1 dead\n\tnexthop via 10.0.0.2 dev eth1 weight 1\n"
	got := parseRoutes(multi)
	wantMulti := []string{"default", "proto", "static", "metric", "100",
		"nexthop", "via", "10.0.0.1", "dev", "eth0", "weight", "1",
		"nexthop", "via", "10.0.0.2", "dev", "eth1", "weight", "1"}
	if len(got) != 1 || !slices.Equal(got[0], wantMulti) {
		t.Errorf("multipath: %v", got)
	}
	if got := parseRoutes(""); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

func TestRestoreCommands(t *testing.T) {
	replaced := []string{"default", "via", "fe80::1", "dev", "eth0", "metric", "1024"}
	// -4 is absent: the join never wrote it, so it stays as it is.
	got := restoreCommands("lan0", map[string][]string{"-6": replaced})
	want := []restoreCommand{
		{args: []string{"-6", "route", "del", "default", "dev", "lan0"}},
		{args: []string{"-6", "route", "add", "default", "via", "fe80::1", "dev", "eth0", "metric", "1024"}, required: true},
	}
	checkCommands(t, got, want)

	// A default the join added has nothing to put back.
	added := restoreCommands("lan0", map[string][]string{"-4": nil})
	checkCommands(t, added, []restoreCommand{{args: []string{"-4", "route", "del", "default", "dev", "lan0"}}})
}

func checkCommands(t *testing.T, got, want []restoreCommand) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("commands %v, want %v", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i].args, want[i].args) || got[i].required != want[i].required {
			t.Errorf("command %d: %v, want %v", i, got[i], want[i])
		}
	}
}
