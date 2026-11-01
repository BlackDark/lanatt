package daemon

import "testing"

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
