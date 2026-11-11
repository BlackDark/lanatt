package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
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

// TempName is the host-side name a device has until it is moved into the netns.
func TempName(netns uint64, i int) string {
	return fmt.Sprintf("lt%08x%d", uint32(netns), i)
}

var tempName = regexp.MustCompile(`^lt[0-9a-f]{8}[0-9]+$`)

// StaleTemps picks lanatt's temporary devices out of `ip -o link show`. On the host, any such
// device was left by a crash between create and move.
func StaleTemps(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(f[1], ":"), "@")
		if tempName.MatchString(name) {
			names = append(names, name)
		}
	}
	return names
}

// reapTemps deletes temporary devices a crashed daemon left on the host, of any driver.
func reapTemps() error {
	out, err := run(nil, ipBin, "-o", "link", "show")
	if err != nil {
		return err
	}
	for _, name := range StaleTemps(out) {
		if _, err := run(nil, ipBin, "link", "del", "dev", name); err != nil {
			return err
		}
	}
	return nil
}

// join creates the device on the host, moves it into the netns, and configures it there.
func join(ns *os.File, n config.Network, p claims.Planned, tmp string) error {
	if _, err := run(nil, ipBin, linkAddArgs(n, p, tmp)...); err != nil {
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

// linkAddArgs builds `ip link add` for the network's driver. Macvlan takes the MAC and runs in
// bridge mode; ipvlan takes no MAC and runs in L2 mode (ADR-0011).
func linkAddArgs(n config.Network, p claims.Planned, tmp string) []string {
	args := []string{"link", "add", "link", n.Parent, "name", tmp}
	if n.Driver == config.DriverIPvlan {
		return append(args, "type", "ipvlan", "mode", "l2")
	}
	return append(args, "address", p.MAC, "type", "macvlan", "mode", "bridge")
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
