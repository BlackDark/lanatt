//go:build linux

package daemon

import (
	"os"
	"testing"

	"lanatt/internal/claims"
)

// selfClaim is a claim the test process itself owns, so the /proc checks run for real.
func selfClaim(t *testing.T) claims.Claim {
	t.Helper()
	start, err := startTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ns, err := nsID("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	return claims.Claim{BootID: "boot", OwnerPid: os.Getpid(), OwnerStart: start, Netns: ns.ino}
}

func TestDeadReason(t *testing.T) {
	const gone = 1<<22 + 1 // above pid_max on Linux, so /proc has no entry
	cases := map[string]struct {
		mut  func(*claims.Claim)
		want string
	}{
		"alive":         {func(*claims.Claim) {}, ""},
		"legacy":        {func(c *claims.Claim) { c.OwnerPid, c.OwnerStart = 0, "" }, "no owner recorded"},
		"other boot":    {func(c *claims.Claim) { c.BootID = "old" }, "other boot"},
		"owner gone":    {func(c *claims.Claim) { c.OwnerPid = gone }, "owner gone"},
		"pid reused":    {func(c *claims.Claim) { c.OwnerStart = "0" }, "owner pid reused"},
		"netns changed": {func(c *claims.Claim) { c.Netns++ }, "netns changed"},
	}
	for name, c := range cases {
		cl := selfClaim(t)
		c.mut(&cl)
		if got := DeadReason(cl, "boot"); got != c.want {
			t.Errorf("%s: got %q want %q", name, got, c.want)
		}
	}
}

func TestLiveClaimPrunes(t *testing.T) {
	keep := selfClaim(t)
	drop := keep
	drop.OwnerStart = "0"
	s := claims.State{Claims: []claims.Claim{keep, drop}}
	got, _ := s.Prune(LiveClaim("boot"))
	if len(got.Claims) != 1 || got.Claims[0] != keep {
		t.Fatalf("prune kept %+v", got.Claims)
	}
}
