# Static IPv6 on opt-in networks, ULA by default

A network sets `prefix6` to offer static IPv6. The admin defines the prefix and an optional gateway, and the caller asks for an address per join. The admin can also opt in to LAN SLAAC. Global prefixes are refused unless the admin sets `allowGlobal6`.

The reasons are measured, not guessed. On a dual-stack LAN a fresh netns has `accept_ra=1`. A macvlan joined with no configuration then gets a SLAAC address and a default route from the router. That is uncontrolled: lanatt does not own the address or the route (ADR-0005). A static join with `accept_ra` still at 1 got both the static address and the SLAAC address, plus the RA route. The LAN's global prefix had short lifetimes, so a static address in it breaks when the ISP renews. The stable LAN ULA prefix does not.

## Decisions

- **Opt-in.** `prefix6` is the switch. `allowSLAAC: true` requires `prefix6`. `noSLAAC` remains limited to IPv4-only networks. Without `prefix6`, `gateway6` and `allowGlobal6: true` are errors.
- **ULA by default.** `prefix6` must be a `/48` to `/64` inside `fd00::/8`. `allowGlobal6: true` admits any other unicast prefix. Link-local, multicast, loopback and unspecified prefixes are refused either way.
- **Static addresses.** `address6` per join, in `prefix6`, not the subnet-router address and not the gateway. Clash rules match IPv4: an address held by another netns on the network is refused. A MAC held by another attachment is refused on the parent NIC that attachment was made on. Each claim records its parent, so a later config edit does not change it. Known gap: two parent NICs on one switch are not checked against each other. A claim saved before parents were recorded blocks its MAC on every parent.
- **Attachment sysctls.** By default, networks with `prefix6` set `disable_ipv6=0`, `accept_ra=0`, `autoconf=0`, `accept_dad=1` and `dad_transmits=1` before the link comes up, so no RA is processed and DAD runs even if the caller's netns disabled it. With `allowSLAAC`, the interface accepts RA even when forwarding is enabled, learns autonomous prefixes and keeps static DAD enabled. Networks with `noSLAAC` set `accept_ra=0` and `autoconf=0` only, so link-local stays. Other networks write nothing: IPv4 joins do not need a writable `/proc/sys`. Their attachments keep host SLAAC on a dual-stack LAN.
- **Container.** The daemon's `/proc/sys` is read-only by default. `prefix6` and `noSLAAC` need `--security-opt unmask=/proc/sys`. Verified on the VM for `prefix6`: the write fails without it.
- **DAD.** After `addr add`, lanatt waits until the address is preferred. `dadfailed` refuses the join: the device is deleted and no claim is written.
- **Routes per family** (ADR-0005). A configured v6 default needs `gateway6` and the attachment's `address6`. In SLAAC mode, learn the default from RA unless `default: true` selects that configured gateway. For that override, disable RA default-router learning and ignore RA Route Information Options for `::/0` on the attachment. Continue learning autonomous prefixes. IPv4 routing follows ADR-0005.
- **Overlap.** Two networks on one parent cannot have overlapping `prefix6`: the same ULA address would sit on one segment, and DAD sees it only while both are up. The IPv4 rule is in ADR-0003.
- **No `ranges6` in v1.** The whole `prefix6` is claimable. Keep the admin's hosts out of it by choosing the prefix.

## Limits

- Static ULA addresses give LAN reachability. With SLAAC enabled, global addressing and reachability depend on the LAN router.
- lanatt does not configure a DNS resolver or implement DHCPv6 (#20).
- Static-only attachments keep the parent MTU. SLAAC attachments can learn the advertised MTU.

## Verified

Test VM, dummy parent, `test/accept.sh` scenarios 18, 20 and 21 (`noSLAAC` writes, RouteKeep keeps the v6 default): a dual-stack join gets `accept_ra=0`, exactly one static v6 address and a v6 default via `gateway6`; a duplicate address6 on the LAN is refused by DAD, and nothing is left behind. A static ULA address on the real LAN was reachable from another LAN host. Unit tests cover the config and claims rules.

The dummy-LAN suite also verifies SLAAC, RA default learning, explicit override after a later RA, and refusal of a duplicate static ULA. Real-LAN testing on the spike VM verified inbound reachability from a separate LAN host and outbound global IPv6 in both route modes.

## Considered options

- Accept SLAAC on the attachment. Originally rejected because lanatt could not own the learned address or default route. Accepted as an administrator opt-in so a macvlan-only workload can keep a stable ULA while its global address follows changes to the LAN’s advertised prefix. The router owns the learned configuration in this mode.
- Let the caller pick global prefixes by default. Rejected: a global prefix changes with the ISP, and a ULA address keeps working when it does.
- A boolean `ipv6` flag next to `prefix6`. Rejected: two fields that must agree.
- DHCPv6. Deferred with #20: it needs a client that lives as long as the workload.
