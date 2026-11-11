package daemon

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

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
	// Set before the link is up, so no RA is ever processed.
	for _, kv := range ipv6Sysctls(n) {
		if err := setIPv6(ns, p.Interface, kv[0], kv[1]); err != nil {
			return err
		}
	}
	addr := fmt.Sprintf("%s/%d", p.Address, n.Prefix.Bits())
	if _, err := inNetns(ns, "link", "set", "dev", p.Interface, "up"); err != nil {
		return err
	}
	if _, err := inNetns(ns, "addr", "add", addr, "dev", p.Interface); err != nil {
		return err
	}
	if p.Address6.IsValid() {
		addr6 := fmt.Sprintf("%s/%d", p.Address6, n.Prefix6.Bits())
		if _, err := inNetns(ns, "addr", "add", addr6, "dev", p.Interface); err != nil {
			return err
		}
		if err := waitDAD(ns, p.Interface, p.Address6); err != nil {
			return err
		}
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

// ipv6Sysctls are the net.ipv6.conf.<iface> settings for a new attachment. A network with
// prefix6 takes only the static addresses lanatt adds (ADR-0012); one without has no IPv6.
func ipv6Sysctls(n config.Network) [][2]string {
	if n.HasIPv6() {
		return [][2]string{{"accept_ra", "0"}, {"autoconf", "0"}}
	}
	return [][2]string{{"disable_ipv6", "1"}}
}

// dadTimeout bounds how long a join waits for duplicate address detection to finish.
const dadTimeout = 5 * time.Second

// waitDAD waits until addr is preferred. A duplicate on the LAN fails the join: the caller
// deletes the device, and no claim is written.
func waitDAD(ns *os.File, iface string, addr netip.Addr) error {
	deadline := time.Now().Add(dadTimeout)
	for {
		out, err := inNetns(ns, "-6", "addr", "show", "dev", iface)
		if err != nil {
			return err
		}
		switch state := dadState(out, addr); state {
		case "":
			return nil
		case "dadfailed":
			return fmt.Errorf("address6 %s is already in use on the LAN", addr)
		case "missing":
			return fmt.Errorf("address6 %s is not on %s", addr, iface)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("address6 %s still tentative after %s", addr, dadTimeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// dadState reads addr's flags from `ip -6 addr show` output: "dadfailed", "tentative",
// "missing", or "" once it is preferred.
func dadState(out string, addr netip.Addr) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "inet6" || !strings.HasPrefix(f[1], addr.String()+"/") {
			continue
		}
		for _, flag := range f {
			if flag == "dadfailed" {
				return "dadfailed"
			}
			if flag == "tentative" {
				return "tentative"
			}
		}
		return ""
	}
	return "missing"
}

// setIPv6 writes one net.ipv6.conf.<iface> sysctl inside the netns. ip(8) cannot set sysctls,
// so the write runs under nsenter; the value and key are positional arguments, never spliced.
func setIPv6(ns *os.File, iface, key, value string) error {
	const script = `printf %s "$2" > "/proc/sys/net/ipv6/conf/$1/$3"`
	_, err := run(ns, nsenterBin, "--net="+nsPath, "/bin/sh", "-c", script, "sh", iface, value, key)
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
