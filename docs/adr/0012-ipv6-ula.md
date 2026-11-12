# Static IPv6 on opt-in networks, ULA by default

IPv6 is off unless a network sets `prefix6`. It is static, like IPv4: the admin defines the prefix and an optional gateway, the caller asks for an address per join. Global prefixes are refused unless the admin sets `allowGlobal6`.

The reasons are measured, not guessed. On a dual-stack LAN a fresh netns has `accept_ra=1`. A macvlan joined with no configuration then gets a SLAAC address and a default route from the router. That is uncontrolled: lanatt does not own the address or the route (ADR-0005). A static join with `accept_ra` still at 1 got both the static address and the SLAAC address, plus the RA route. The LAN's global prefix had short lifetimes, so a static address in it breaks when the ISP renews. The stable LAN ULA prefix does not.

## Decisions

- **Opt-in.** `prefix6` is the switch. No boolean `ipv6` flag, so there is no invalid combination to check. Without `prefix6`, `gateway6` and `allowGlobal6` are errors.
- **ULA by default.** `prefix6` must be a `/48` to `/64` inside `fd00::/8`. `allowGlobal6: true` admits any other unicast prefix. Link-local, multicast, loopback and unspecified prefixes are refused either way.
- **Static addresses.** `address6` per join, in `prefix6`, not the subnet-router address and not the gateway. Clash rules match IPv4: an address or MAC held by another netns on the network is refused.
- **Attachment sysctls.** Networks with `prefix6` set `disable_ipv6=0`, `accept_ra=0`, `autoconf=0`, `accept_dad=1` and `dad_transmits=1` before the link comes up, so no RA is processed and DAD runs even if the caller's netns disabled it. Networks without it write nothing: IPv4 joins do not need a writable `/proc/sys`. Known gap: their attachments keep host SLAAC on a dual-stack LAN. Follow-up: a per-network opt-out.
- **Container.** The daemon's `/proc/sys` is read-only by default. `prefix6` needs `--security-opt unmask=/proc/sys` (verified on the VM: the write fails without it).
- **DAD.** After `addr add`, lanatt waits until the address is preferred. `dadfailed` refuses the join: the device is deleted and no claim is written.
- **Routes per family** (ADR-0005). A v6 default needs `gateway6` and the attachment's `address6`. `default: true` replaces the default in each family the attachment serves.
- **No `ranges6` in v1.** The whole `prefix6` is claimable. Keep the admin's hosts out of it by choosing the prefix.

## Limits

- ULA addresses reach the LAN only. The workload has no internet over IPv6 unless the router routes ULA through NAT66, which is the router's job.
- No DNS over IPv6 from the attachment: `accept_ra=0` drops RDNSS, and DHCPv6 is not implemented (#20).
- The RA-learned MTU (1492 measured on the test LAN) does not apply to static routes. The link keeps its parent MTU (1500). Off-link traffic could hit a smaller path MTU.

## Verified

Test VM, dummy parent, `test/accept.sh` scenario 18: a dual-stack join gets `accept_ra=0`, exactly one static v6 address and a v6 default via `gateway6`; a duplicate address6 on the LAN is refused by DAD, and nothing is left behind. A static ULA address on the real LAN was reachable from another LAN host. Unit tests cover the config and claims rules.

## Considered options

- Accept SLAAC on the attachment. Rejected: an address and a default route lanatt does not own.
- Let the caller pick global prefixes by default. Rejected: a global prefix changes with the ISP, and a ULA address keeps working when it does.
- A boolean `ipv6` flag next to `prefix6`. Rejected: two fields that must agree.
- DHCPv6. Deferred with #20: it needs a client that lives as long as the workload.
