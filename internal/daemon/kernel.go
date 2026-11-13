package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
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

// configure renames the device tmp to its interface and sets it up. Default routes change last.
// If a later step fails after one changed, the families that changed are restored: the device
// is removed by the caller, which would otherwise take the new default with it.
func configure(ns *os.File, n config.Network, p claims.Planned, tmp string) (undo defaultUndo, err error) {
	if _, err := inNetns(ns, "link", "set", "dev", tmp, "name", p.Interface); err != nil {
		return defaultUndo{}, err
	}
	// Set the IPv6 policy before the link can receive router advertisements.
	for _, kv := range ipv6Sysctls(n, p) {
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
		if err == nil {
			return
		}
		// A failed restore keeps undo, so abort retries it and reports the error.
		if rerr := undo.restore(ns); rerr != nil {
			err = errors.Join(err, rerr)
			return
		}
		undo = defaultUndo{}
	}()
	replaced, wrote, err := setDefault(ns, "-4", n.Gateway, p.Interface, p.Default)
	if err != nil {
		return undo, err
	}
	if wrote {
		undo.written["-4"] = replaced
	}
	if useStaticDefault6(n, p) {
		replaced, wrote, err = setDefault(ns, "-6", n.Gateway6, p.Interface, p.Default)
		if wrote {
			undo.written["-6"] = replaced
		}
	}
	return undo, err
}

// useStaticDefault6 leaves route discovery to RA on SLAAC networks unless the
// join explicitly selects a configured IPv6 gateway.
func useStaticDefault6(n config.Network, p claims.Planned) bool {
	return n.Gateway6.IsValid() && p.Address6.IsValid() && (!n.AllowSLAAC || p.Default)
}

// ipv6Sysctls enables IPv6 and DAD for static addressing even when the workload
// disabled them. Plain networks leave their inherited settings alone.
func ipv6Sysctls(n config.Network, p claims.Planned) [][2]string {
	if n.HasIPv6() {
		if !n.AllowSLAAC {
			return [][2]string{{"disable_ipv6", "0"}, {"accept_ra", "0"}, {"autoconf", "0"}, {"accept_dad", "1"}, {"dad_transmits", "1"}}
		}
		// Accept RA even in a forwarding netns. Keep autonomous prefix learning
		// on when a configured gateway takes the place of the advertised default.
		defaultRouter := "1"
		if useStaticDefault6(n, p) {
			defaultRouter = "0"
		}
		return [][2]string{
			{"disable_ipv6", "0"}, {"accept_dad", "1"}, {"dad_transmits", "1"},
			{"accept_ra", "2"}, {"accept_ra_pinfo", "1"}, {"autoconf", "1"},
			{"accept_ra_defrtr", defaultRouter},
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
