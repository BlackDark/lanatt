<div align="center">

# lanatt

**Give rootless Podman and Docker containers their own IP address on your LAN (macvlan), without running them as root.**<br>
A small root daemon and an OCI hook. The container stays rootless; only the daemon is privileged.

[![CI](https://github.com/BlackDark/lanatt/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackDark/lanatt/actions/workflows/ci.yml)

</div>

## Why

Rootless containers cannot create a macvlan, because the host NIC is outside their network namespace. Today the fix is to run those containers rootful.

- **Rootless workloads on the LAN**: a container gets its own IPv4 address and MAC, reachable from other hosts.
- **The admin decides the networks**: callers pick a named network, an address and a MAC. They never pick the NIC.
- **No surprises at start**: the join happens before the workload process runs, and a refused join fails the start.
- **Clash-free**: lanatt refuses an address or MAC another container already holds, which the kernel itself allows.

## Install

```bash
podman run -d --name lanatt --network host --pid host --privileged \
  -v /etc/lanatt:/etc/lanatt:ro -v /run/lanatt:/run/lanatt -v /var/lib/lanatt:/var/lib/lanatt \
  ghcr.io/blackdark/lanatt:latest
```

Then write `/etc/lanatt/config.json`, install the hook, and annotate a container. See [docs/usage.md](docs/usage.md).

## FAQ

### Can a rootless Podman container get its own LAN IP with macvlan?

Yes. Add the `io.lanatt.attachments` annotation. The lanatt hook asks the daemon to move a macvlan into the container's netns.

### Can the host reach the container?

Not through the parent NIC: that is how macvlan works. Give the host a macvlan shim on the same parent, or test from another LAN host.

### Does it do IPv6, DHCP or ipvlan?

No, not in v1.

## Development

Requires [mise](https://mise.jdx.dev).

```bash
mise install
mise run check
```

Kernel-level tests run as root on a VM: see [docs/usage.md](docs/usage.md#acceptance).
