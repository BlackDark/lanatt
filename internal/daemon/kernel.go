package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"lanatt/internal/claims"
	"lanatt/internal/config"
)

const (
	ipBin      = "/usr/sbin/ip"
	nsenterBin = "/usr/bin/nsenter"
	// The netns fd is passed to every child as fd 3.
	nsPath = "/proc/self/fd/3"
)

// RouteAction is what join does with the default route.
type RouteAction int

const (
	RouteKeep RouteAction = iota
	RouteAdd
	RouteReplace
)

// DecideRoute follows ADR-0005.
func DecideRoute(hasDefault, wantDefault bool) RouteAction {
	switch {
	case wantDefault:
		return RouteReplace
	case hasDefault:
		return RouteKeep
	default:
		return RouteAdd
	}
}

// join creates the macvlan on the host, moves it into the netns, and configures it there.
func join(ns *os.File, n config.Network, p claims.Planned, tmp string) error {
	if _, err := run(nil, ipBin, "link", "add", "link", n.Parent, "name", tmp,
		"address", p.MAC, "type", n.Driver, "mode", "bridge"); err != nil {
		return err
	}
	if _, err := run(ns, ipBin, "link", "set", "dev", tmp, "netns", nsPath); err != nil {
		_, _ = run(nil, ipBin, "link", "del", "dev", tmp)
		return err
	}
	name := tmp
	err := configure(ns, n, p, &name)
	if err != nil {
		_, _ = inNetns(ns, "link", "del", "dev", name)
	}
	return err
}

// configure sets *name to the device's current name so a failure deletes only our device.
func configure(ns *os.File, n config.Network, p claims.Planned, name *string) error {
	if _, err := inNetns(ns, "link", "set", "dev", *name, "name", p.Interface); err != nil {
		return err
	}
	*name = p.Interface
	addr := fmt.Sprintf("%s/%d", p.Address, n.Prefix.Bits())
	if _, err := inNetns(ns, "link", "set", "dev", p.Interface, "up"); err != nil {
		return err
	}
	if _, err := inNetns(ns, "addr", "add", addr, "dev", p.Interface); err != nil {
		return err
	}
	out, err := inNetns(ns, "-4", "route", "show", "default")
	if err != nil {
		return err
	}
	gw := n.Gateway.String()
	switch DecideRoute(strings.TrimSpace(out) != "", p.Default) {
	case RouteAdd:
		_, err = inNetns(ns, "route", "add", "default", "via", gw, "dev", p.Interface)
	case RouteReplace:
		_, err = inNetns(ns, "route", "replace", "default", "via", gw, "dev", p.Interface)
	}
	return err
}

func inNetns(ns *os.File, args ...string) (string, error) {
	return run(ns, nsenterBin, append([]string{"--net=" + nsPath, ipBin}, args...)...)
}

func run(ns *os.File, bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	if ns != nil {
		cmd.ExtraFiles = []*os.File{ns}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
