package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// join creates the device on the host under tmp, marks it with token, moves it into the netns,
// and configures it there. On failure the device is left for the caller to remove by token: the
// caller holds the record that finds it.
func join(ns *os.File, n config.Network, p claims.Planned, tmp, token string) (defaultUndo, error) {
	if _, err := run(nil, ipBin, linkAddArgs(n, p, tmp)...); err != nil {
		return defaultUndo{}, err
	}
	if _, err := run(nil, ipBin, "link", "set", "dev", tmp, "alias", aliasPrefix+token); err != nil {
		return defaultUndo{}, err
	}
	if _, err := run(ns, ipBin, "link", "set", "dev", tmp, "netns", nsPath); err != nil {
		return defaultUndo{}, err
	}
	return configure(ns, n, p, tmp)
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

// defaultUndo puts back the netns default routes a join changed. The zero value does nothing.
type defaultUndo struct {
	dev string
	// written maps each family the join wrote to the route it replaced, or nil if it added one.
	written map[string][]string
}

// restore is best effort: the caller deletes the device next, so it must try first.
func (u defaultUndo) restore(ns *os.File) error {
	return restoreDefaults(ns, u.dev, u.written)
}

// configure renames the device tmp to its interface and sets it up. Default routes change last.
// If a later step fails after one changed, the families that changed are restored: the device
// is removed by the caller, which would otherwise take the new default with it.
func configure(ns *os.File, n config.Network, p claims.Planned, tmp string) (undo defaultUndo, err error) {
	if _, err := inNetns(ns, "link", "set", "dev", tmp, "name", p.Interface); err != nil {
		return defaultUndo{}, err
	}
	// Set before the link is up, so no RA is ever processed.
	for _, kv := range ipv6Sysctls(n) {
		if err := setIPv6(ns, p.Interface, kv[0], kv[1]); err != nil {
			return defaultUndo{}, err
		}
	}
	addr := fmt.Sprintf("%s/%d", p.Address, n.Prefix.Bits())
	if _, err := inNetns(ns, "link", "set", "dev", p.Interface, "up"); err != nil {
		return defaultUndo{}, err
	}
	if _, err := inNetns(ns, "addr", "add", addr, "dev", p.Interface); err != nil {
		return defaultUndo{}, err
	}
	if p.Address6.IsValid() {
		addr6 := fmt.Sprintf("%s/%d", p.Address6, n.Prefix6.Bits())
		if _, err := inNetns(ns, "addr", "add", addr6, "dev", p.Interface); err != nil {
			return defaultUndo{}, err
		}
		if err := waitDAD(ns, p.Interface, p.Address6); err != nil {
			return defaultUndo{}, err
		}
	}
	undo = defaultUndo{dev: p.Interface, written: map[string][]string{}}
	defer func() {
		if err != nil {
			// Restored here, so the caller must not restore them again.
			if rerr := undo.restore(ns); rerr != nil {
				log.Printf("restore defaults on %s: %v", p.Interface, rerr)
			}
			undo = defaultUndo{}
		}
	}()
	replaced, wrote, err := setDefault(ns, "-4", n.Gateway, p.Interface, p.Default)
	if err != nil {
		return undo, err
	}
	if wrote {
		undo.written["-4"] = replaced
	}
	// A v6 default needs both a gateway6 and this attachment's own address6; otherwise the
	// attachment is connected-route only in IPv6.
	if n.Gateway6.IsValid() && p.Address6.IsValid() {
		replaced, wrote, err = setDefault(ns, "-6", n.Gateway6, p.Interface, p.Default)
		if wrote {
			undo.written["-6"] = replaced
		}
	}
	return undo, err
}

// setDefault applies ADR-0005 to one family. It reports the route it replaced, if any, and
// whether it wrote one. family is "-4" or "-6": the query and the change name the family, since
// `ip route show default` without one mixes both. The replaced route keeps its metric, so it is
// the route the netns prefers.
func setDefault(ns *os.File, family string, gw netip.Addr, dev string, wantDefault bool) ([]string, bool, error) {
	out, err := inNetns(ns, family, "route", "show", "default")
	if err != nil {
		return nil, false, err
	}
	cur := firstRoute(out)
	verb := "add"
	var extra []string
	switch DecideRoute(cur != nil, wantDefault) {
	case RouteKeep:
		return nil, false, nil
	case RouteReplace:
		verb = "replace"
		if m := routeMetric(cur); m != "" {
			extra = []string{"metric", m}
		}
	}
	args := append([]string{family, "route", verb, "default", "via", gw.String(), "dev", dev}, extra...)
	if _, err := inNetns(ns, args...); err != nil {
		return nil, false, err
	}
	return cur, true, nil
}

// firstRoute is the netns's first default route in `ip route show` output, or nil if none.
func firstRoute(out string) []string {
	routes := parseRoutes(out)
	if len(routes) == 0 {
		return nil
	}
	return routes[0]
}

// restoreCommand is one `ip` call of a restore. A required call that fails is reported.
type restoreCommand struct {
	args     []string
	required bool
}

// restoreCommands undoes each family the join wrote: the new default goes from dev, then the
// route it replaced comes back. The removal is optional: it fails when nothing is there.
func restoreCommands(dev string, written map[string][]string) []restoreCommand {
	var cmds []restoreCommand
	for _, family := range []string{"-4", "-6"} {
		replaced, ok := written[family]
		if !ok {
			continue
		}
		cmds = append(cmds, restoreCommand{args: []string{family, "route", "del", "default", "dev", dev}})
		if replaced != nil {
			cmds = append(cmds, restoreCommand{args: append([]string{family, "route", "add"}, replaced...), required: true})
		}
	}
	return cmds
}

// restoreDefaults runs restoreCommands. The join error is the one to report, so it returns the
// restore errors only for the caller's log.
func restoreDefaults(ns *os.File, dev string, written map[string][]string) error {
	var errs []error
	for _, c := range restoreCommands(dev, written) {
		if _, err := inNetns(ns, c.args...); err != nil && c.required {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// parseRoutes turns `ip route show default` output into route arguments, one route per entry.
// A multipath route continues on indented lines; those join their route. State flags and
// lifetimes are dropped: they are not part of a route's spec.
func parseRoutes(out string) [][]string {
	var routes [][]string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		args := specArgs(f)
		if (line[0] == ' ' || line[0] == '\t') && len(routes) > 0 {
			routes[len(routes)-1] = append(routes[len(routes)-1], args...)
		} else {
			routes = append(routes, args)
		}
	}
	return routes
}

// specArgs drops the tokens of a route line that `ip route add` rejects or ignores.
func specArgs(f []string) []string {
	var out []string
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case "linkdown", "dead", "pervasive", "offload", "trap", "unresolved", "notify", "error",
			"rt_offload", "rt_offload_failed", "rt_trap":
		case "expires":
			i++
		default:
			out = append(out, f[i])
		}
	}
	return out
}

// routeMetric is the metric of a route's arguments, or "" if it has none.
func routeMetric(route []string) string {
	for i := 0; i+1 < len(route); i++ {
		if route[i] == "metric" {
			return route[i+1]
		}
	}
	return ""
}

// ipv6Sysctls are the net.ipv6.conf.<iface> settings for a new attachment. A network with
// prefix6 takes only the static addresses lanatt adds (ADR-0012). The caller's netns may
// have turned IPv6 off or DAD down: disable_ipv6 would fail the address add, and waitDAD would
// trust an unprobed address. So those are forced to the values the join needs.
// NoSLAAC only stops RA and autoconf; link-local stays. Other networks write nothing: the
// daemon's /proc/sys is read-only by default, and IPv4 joins must not depend on it.
func ipv6Sysctls(n config.Network) [][2]string {
	if n.HasIPv6() {
		return [][2]string{
			{"disable_ipv6", "0"}, {"accept_ra", "0"}, {"autoconf", "0"},
			{"accept_dad", "1"}, {"dad_transmits", "1"},
		}
	}
	if n.NoSLAAC {
		return [][2]string{{"accept_ra", "0"}, {"autoconf", "0"}}
	}
	return nil
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
			return fmt.Errorf("address6 %s still tentative after %s: is the parent link up?", addr, dadTimeout)
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

// childTimeout bounds one ip or nsenter call. A hung child would hold its netns busy for good.
const childTimeout = 10 * time.Second

func run(ns *os.File, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), childTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if ns != nil {
		cmd.ExtraFiles = []*os.File{ns}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
