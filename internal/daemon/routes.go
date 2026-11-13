package daemon

import (
	"errors"
	"net/netip"
	"os"
	"strings"
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

// restoreDefaults runs restoreCommands and returns any required restore failures.
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
