# An attachment takes the default route only when it has to

Joining a network installs a connected route for that prefix. When the workload netns has no default route, lanatt adds one via the network gateway. The caller can mark the attachment as the default route and replace the one already there.

Amended by ADR-0012: the rule applies per address family. A v6 default is added or replaced only when the network has a `gateway6` and the attachment has an `address6`. Static-only `prefix6` networks disable RA. See [ADR-0012](0012-ipv6-ula.md) for default-route precedence with `allowSLAAC`.

## Considered options

- Never touch the default route. Rejected: a workload whose only network is the attachment would have no way off the prefix.
- Always install the gateway as the default route. Rejected: a workload that already has an engine network would lose that route.
