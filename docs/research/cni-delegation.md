# CNI delegation research and proposed decision

Status: proposal for [#25](https://github.com/BlackDark/lanatt/issues/25); rootless namespace compatibility and lifecycle recovery require VM proof before acceptance. This does not decide [IPv6](https://github.com/BlackDark/lanatt/issues/19), [DHCP](https://github.com/BlackDark/lanatt/issues/20), or [ipvlan](https://github.com/BlackDark/lanatt/issues/21) for the built-in driver.

## Findings

- The [CNI specification](https://github.com/containernetworking/cni/blob/spec-v1.1.0/SPEC.md) defines an attachment by container ID and interface name. The runtime provides JSON on stdin and `CNI_COMMAND`, `CNI_CONTAINERID`, `CNI_IFNAME`, `CNI_NETNS` for ADD, plus a plugin search path. It must eventually issue DEL even after a failed ADD, persist the final result, and supply it as `prevResult` on DEL. DEL must tolerate repeats and a missing namespace. An ADD for the same tuple must not be repeated without DEL. An empty result on DEL should be tolerated, which matters after an interrupted ADD.
- [macvlan](https://www.cni.dev/plugins/current/main/macvlan/) creates distinct MACs on a master; [ipvlan](https://www.cni.dev/plugins/current/main/ipvlan/) shares the master's MAC and supports multiple modes. [bridge](https://www.cni.dev/plugins/current/main/bridge/) creates veths attached to a host bridge; reaching a LAN requires separate uplink configuration. These do not have equivalent LAN or isolation semantics. Plugin configuration and resulting routes can conflict with ADR-0005, so a route policy must be specified and tested per family rather than assumed.
- [static IPAM](https://www.cni.dev/plugins/current/ipam/static/) accepts IPv4 and IPv6 CIDRs; it does not provide allocation conflict protection. [host-local](https://www.cni.dev/plugins/current/ipam/host-local/) allocates both families and stores state on disk, guaranteeing uniqueness only within its local state. It cannot prevent collisions with other hosts, DHCP, or lanatt's own static claims. Its data directory and network name must remain stable and persistent across daemon and container restarts.
- [DHCP IPAM](https://www.cni.dev/plugins/current/ipam/dhcp/) needs a separately running daemon to renew leases, with a shared socket and namespace visibility. Do not assume daemon restart recovers leases or that ipvlan DHCP works: verify the exact plugin version and deployment. An ADD may block for lease acquisition; the existing 30-second socket deadline needs an explicit bounded timeout policy.
- A root-launched plugin targeting a netns owned by a rootless user namespace is plausible, not proven by the specification. Inheriting the held namespace fd as `/proc/self/fd/3` requires proof for the main plugin and any delegated process: descriptor inheritance, namespace ownership, and DHCP's separate daemon can invalidate that path. Never reconstruct a target from an untrusted PID after validation. A path handed to another process must identify the intended namespace in *that process's* mount and PID view.

## Proposed ADR (pending evidence)

Retain built-in macvlan as default. Add a separately configured, opt-in single-main-plugin CNI backend with administrator-owned configuration. Initially accept only explicit approved main plugins (`macvlan`, `ipvlan`, `bridge`) and IPAM (`static`, `host-local`, optionally DHCP after its own acceptance test); reject plugin chains, arbitrary nested plugin types and caller-supplied executable names or configuration. Pin supported CNI/plugin versions and validate the selected version and result. Adding a general plugin or chain later requires a separate security and lifecycle review.

Execute a fixed absolute executable from a trusted root-owned, non-user-writable plugin directory, with no shell, a minimal explicit environment including `CNI_PATH`, bounded output and timeout, and root-owned configuration and state. Validate ownership and writability of executable, directories, delegated IPAM binaries and their paths at the trust boundary; do not search caller-controlled PATH. CNI plugins are privileged host code, and even allowed plugins can mutate host networking or invoke IPAM: administrator control of plugin binaries and config is the security boundary. Define the image/host bind mounts and persistent state locations explicitly. Do not silently bundle or depend on host packages without an installation decision.

Preserve caller authorization and network admission. For static addresses, perform existing prefix and address clash checks before ADD, including cross-backend collisions; do not rely on static IPAM. Maintain MAC uniqueness only for drivers with independently assigned MACs; ipvlan's shared MAC cannot be a unique claim. For dynamic addresses, retain attachment identity and ownership, record and validate returned addresses, and reserve them against lanatt claims; if post-ADD validation fails, DEL and retain a cleanup record until it succeeds. This alone cannot prevent an external allocator handing out a static address: operators must partition pools or use a shared authoritative allocator. Specify supported MAC request capability and default-route policy; do not promise equivalent behavior until verified.

Use a durable, root-owned per-attachment journal. Before ADD, atomically persist and sync an intent containing generated stable CNI ID, UID, interface, network, exact effective config, plugin/version identity and invocation parameters. Mark completion and persist validated result before acknowledging success. On ADD error, timeout, malformed result, or interrupted intent, issue DEL, including without a result when unavailable; preserve the journal and retry failed cleanup. On stop and startup reconciliation, DEL using stored config and result, even if the namespace is gone; remove the record and release claims only after cleanup succeeds. Serialize per attachment and prevent a repeat ADD while an intent or cleanup remains. Generate IDs in the CNI permitted character set and namespace them across callers; never allow one UID to delete another's record. Avoid blindly reopening a stale namespace path or relying only on inode reuse-prone liveness. Plugin upgrades/removal and config edits must not strand old records: document retained binary compatibility and surface cleanup failures.

This journal is distinct from today's prune-on-dead-netns claim handling: pruning before DEL would leak host-local allocations. Crash recovery needs tests at intent persistence, during plugin execution, after ADD and before result persistence, and during DEL. Even with journaling there is no atomic transaction across a third-party plugin and the filesystem; interrupted ADD relies on the plugin honoring idempotent DEL with an absent result.

## Proof and implementation slices

1. On the authorized test VM, record kernel, runtime and plugin versions. Exercise macvlan, ipvlan and bridge against a real rootless workload netns, both static families, existing defaults and explicit route choice; verify namespace fd visibility in plugin descendants, failed ADD and missing-netns DEL. Check external reachability from a separate LAN host using only assigned addresses. Test host-local restart cleanup and deliberately killed ADD/DEL. Test DHCP daemon startup, renewal, daemon restart and lease cleanup independently. Record failures and exact commands without private machine or address details.
2. Implement durable intent and cleanup/reconciliation with fake executable tests and failure injection; preserve ownership, liveness, and existing builtin behavior. Define upgrade/migration and operator recovery for irrecoverable cleanup.
3. Implement a narrow static backend, version/result validation and safe execution; prove rootless kernel behavior and routes. Then add host-local with persistent storage and collision policy.
4. Decide DHCP daemon packaging and supervision only after renewal/restart proof; add DHCP separately. Resolve IPv6 DAD/SLAAC and ipvlan mode/host reachability as their own product decisions. Publish the installation and trust requirements before enabling any backend.

Research alone establishes no successful test on the test VM and does not establish IPv6 SLAAC, DHCP recovery, ipvlan reachability or multicast behavior. Those remain open acceptance criteria.

## Illustrative plugin configurations

These are upstream CNI plugin objects, not an accepted lanatt configuration schema. Replace addresses, names and interface names with administrator-assigned values. They illustrate independent combinations; none establishes lanatt route policy or a successful VM test.

macvlan with dual-stack static IPAM:

```json
{"cniVersion":"1.0.0","name":"lanatt-static","type":"macvlan","master":"eth0","mode":"bridge","ipam":{"type":"static","addresses":[{"address":"192.0.2.10/24","gateway":"192.0.2.1"},{"address":"2001:db8:1::10/64","gateway":"2001:db8:1::1"}]}}
```

ipvlan with host-local IPv4 and IPv6 ranges (persistent `dataDir`):

```json
{"cniVersion":"1.0.0","name":"lanatt-pool","type":"ipvlan","master":"eth0","mode":"l2","ipam":{"type":"host-local","dataDir":"/var/lib/cni/networks","ranges":[[{"subnet":"192.0.2.0/24","rangeStart":"192.0.2.20","rangeEnd":"192.0.2.30","gateway":"192.0.2.1"}],[{"subnet":"2001:db8:1::/64","rangeStart":"2001:db8:1::20","rangeEnd":"2001:db8:1::30","gateway":"2001:db8:1::1"}]]}}
```

bridge with DHCP (the bridge and LAN uplink must be administered separately, and a reachable DHCP daemon must already run):

```json
{"cniVersion":"1.0.0","name":"lanatt-bridge","type":"bridge","bridge":"lanbr0","ipam":{"type":"dhcp","daemonSocketPath":"/run/cni/dhcp.sock"}}
```

## Proposed implementation tickets

- **Durable CNI attachment lifecycle:** persist intent before invocation and result before success; retry idempotent DEL after failure, restart, missing netns and interrupted operations; enforce UID ownership and avoid stale-namespace reuse. Acceptance: injected crashes at every transition do not drop cleanup state or repeat ADD; retry succeeds after plugin availability returns.
- **Restricted CNI execution:** validate administrator configuration and trusted plugin/IPAM paths, version, environment, namespace access and output; constrain runtime and provide actionable failures. Acceptance: fake-plugin tests cover command arguments, process errors, timeouts, invalid results, unsafe paths and descriptor inheritance.
- **Static CNI attachment:** integrate macvlan, ipvlan and bridge with explicit route and claim policies and static IPv4/IPv6. Acceptance: actual rootless namespace VM tests cover each plugin, collision and UID checks, routes, rollback and stop/restart cleanup.
- **Host-local IPAM:** persist allocation data and enforce coordination with lanatt's claims and documented external address pools. Acceptance: dual-stack allocation, exhaustion, collision rollback, concurrent joins and recovery after daemon restart.
- **DHCP IPAM:** choose and document daemon installation, supervision and socket/namespace visibility; implement only after measured restart and renewal behavior. Acceptance: real server lease, renewal, daemon restart, timeout and cleanup on the VM; document observed limitations.
- **Independent IPv6 and ipvlan decisions:** settle SLAAC/DAD and per-family defaults for #19, and ipvlan modes, MAC semantics, multicast and host reachability for #21. Acceptance requires the measured VM behavior and corresponding operator documentation.

The current managed workspace has no usable privileges for kernel proof: it runs as UID 1000 with an empty capability bounding set, and `unshare -Urn` cannot write `/proc/self/uid_map`. The authorized test VM remains necessary. For a parent-process `/proc/<pid>/fd/<n>` namespace reference, verify access from each plugin and delegated daemon, descriptor lifetime throughout ADD, and PID namespace/mount visibility; do not assume this solves DHCP's longer-lived daemon requirements.
