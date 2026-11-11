package claimlist

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"lanatt/internal/claims"
)

const boot = "boot-now"

func claim(id, claimBoot string, pid int) claims.Claim {
	return claims.Claim{
		ID: id, UID: 1001, Netns: 4026532504, Network: "lan", Interface: "lan0",
		Address: netip.MustParseAddr("10.0.1.51"), MAC: "02:42:0a:00:01:33",
		BootID: claimBoot, OwnerPid: pid, OwnerStart: "1",
	}
}

// fakeReason maps a claim id to its dead reason; ids not listed are alive.
func fakeReason(dead map[string]string) func(claims.Claim) string {
	return func(c claims.Claim) string { return dead[c.ID] }
}

func fakeComm(pid int) string { return map[int]string{42: "traefik"}[pid] }

func TestRowsJudgesEachClaim(t *testing.T) {
	s := claims.State{Claims: []claims.Claim{
		claim("alive", boot, 42),
		claim("gone", boot, 43),
	}}
	rows := Rows(s, fakeReason(map[string]string{"gone": "owner gone"}), fakeComm)
	if len(rows) != 2 {
		t.Fatalf("rows: %d", len(rows))
	}
	if !rows[0].Alive || rows[0].OwnerComm != "traefik" || rows[0].Reason != "" {
		t.Errorf("alive row: %+v", rows[0])
	}
	if rows[1].Alive || rows[1].OwnerComm != "" || rows[1].Reason != "owner gone" {
		t.Errorf("dead row: %+v", rows[1])
	}
}

func TestTableShowsOwnerAndReason(t *testing.T) {
	rows := []Row{
		{Claim: claim("a6eabd1adc12ffff", boot, 42), Alive: true, OwnerComm: "traefik"},
		{Claim: claim("b7fbce2bed23", "old", 43), Reason: "other boot"},
		{Claim: claim("c8acdf3cfe34", "", 0), Reason: "no owner recorded"},
	}
	var buf bytes.Buffer
	if err := WriteTable(&buf, rows, boot); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header and 3 rows:\n%s", buf.String())
	}
	want := [][]string{
		{"ID", "UID", "NETWORK", "ADDRESS", "MAC", "IFACE", "NETNS", "BOOT", "OWNER", "REASON"},
		{"a6eabd1adc12", "1001", "lan", "10.0.1.51", "02:42:0a:00:01:33", "lan0", "4026532504", "this", "42/traefik", "-"},
		{"b7fbce2bed23", "1001", "lan", "10.0.1.51", "02:42:0a:00:01:33", "lan0", "4026532504", "previous", "none", "other boot"},
		{"c8acdf3cfe34", "1001", "lan", "10.0.1.51", "02:42:0a:00:01:33", "lan0", "4026532504", "unknown", "none", "no owner recorded"},
	}
	for i, line := range lines {
		if got := strings.Fields(line); strings.Join(got, " ") != strings.Join(want[i], " ") {
			t.Errorf("row %d:\n got %v\nwant %v", i, got, want[i])
		}
	}
}

func TestEmptyState(t *testing.T) {
	rows := Rows(claims.State{Claims: []claims.Claim{}}, fakeReason(nil), fakeComm)
	var table, js bytes.Buffer
	if err := WriteTable(&table, rows, boot); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(table.String(), "\n"); n != 1 {
		t.Errorf("empty table has %d lines, want header only", n)
	}
	if err := WriteJSON(&js, rows); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(js.String()) != "[]" {
		t.Errorf("empty json: %q", js.String())
	}
}

func TestJSONHasClaimFieldsAndLiveness(t *testing.T) {
	rows := Rows(claims.State{Claims: []claims.Claim{claim("alive", boot, 42)}}, fakeReason(nil), fakeComm)
	var buf bytes.Buffer
	if err := WriteJSON(&buf, rows); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	r := got[0]
	for k, v := range map[string]any{
		"id": "alive", "uid": float64(1001), "interface": "lan0", "boot_id": boot,
		"owner_pid": float64(42), "owner_start": "1", "alive": true, "reason": "", "owner_comm": "traefik",
	} {
		if r[k] != v {
			t.Errorf("json %s = %v, want %v", k, r[k], v)
		}
	}
}
