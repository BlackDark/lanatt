<div align="center">

# lanatt

**Give rootless Podman and Docker containers their own IP address on your LAN (macvlan), without running them as root.**<br>
A small root daemon and an OCI hook. The container stays rootless; only the daemon is privileged.

[![CI](https://github.com/BlackDark/lanatt/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackDark/lanatt/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BlackDark/lanatt)](https://github.com/BlackDark/lanatt/releases/latest)
[![License](https://img.shields.io/github/license/BlackDark/lanatt)](LICENSE)

</div>

## Why

A rootless container cannot create a macvlan, because the host NIC is outside its network namespace. To give such a container its own LAN address today, you run it rootful. lanatt moves only that one privileged step into a root daemon.

- **Rootless workloads on the LAN**: a container gets its own IPv4 address and MAC. Other hosts reach it directly.
- **The admin defines the networks**: callers choose a named network, an address and a MAC. They never choose the NIC.
- **Joined before start**: the attachment exists before the workload process runs. A refused join fails the start.
- **No duplicate addresses**: lanatt refuses an address or MAC that another container already holds. The kernel alone would allow it.

## How it works

1. The administrator lists networks and allowed UIDs in `/etc/lanatt/config.json`.
2. A container carries the annotation `io.lanatt.attachments`: network, IPv4 address, MAC.
3. Before the workload starts, the engine runs `lanatt hook create` as the container's user. The hook sends the join to the daemon over a Unix socket.
4. The daemon checks the caller's UID, creates a macvlan on the parent NIC, moves it into the container's netns, and sets the address and routes.
5. On stop, `lanatt hook stop` releases the address. The interface disappears with the netns.

## Install

Run the daemon (rootful, host network and pid namespace, three capabilities):

```bash
podman run -d --name lanatt --network host --pid host \
  --cap-drop=ALL --cap-add=NET_ADMIN,SYS_ADMIN,SYS_PTRACE \
  --security-opt apparmor=unconfined --security-opt no-new-privileges --read-only \
  -v /etc/lanatt:/etc/lanatt:ro -v /run/lanatt:/run/lanatt -v /var/lib/lanatt:/var/lib/lanatt \
  ghcr.io/blackdark/lanatt:latest
```

Then, once per host:

1. Write `/etc/lanatt/config.json`: [example](examples/config.json).
2. Install the `lanatt` binary from the [latest release](https://github.com/BlackDark/lanatt/releases/latest) to `/usr/local/bin`, because the hook runs on the host.
3. Wire it into your engine: Podman hook files, or the `lanatt-runc` runtime for Docker.

Step-by-step commands, and a Quadlet unit for the daemon, are in [examples/](examples/README.md).

## Usage

Podman, as an allowed user:

```bash
podman run -d --network none \
  --annotation 'io.lanatt.attachments=[{"network":"lan","address":"10.0.1.64","mac":"02:42:0a:00:01:40"}]' \
  docker.io/library/nginx:1.29-alpine
```

Docker, as an allowed user:

```bash
docker run -d --runtime lanatt --network none \
  --annotation 'io.lanatt.attachments=[{"network":"lan","address":"10.0.1.66","mac":"02:42:0a:00:01:42"}]' \
  docker.io/library/nginx:1.29-alpine
```

The container now answers on its address from any other host on the LAN.

## Features

- **Engines**: Podman, Podman Quadlets, Docker and Docker Compose, rootless and rootful (`"allowRoot": true`). Docker can wrap `runc` or `crun`. One binary is both the hook and the daemon.
- **Several networks**: one container can join several networks. Interfaces are named `lan0`, `lan1`, … unless you name them.
- **Routes**: lanatt adds a default route via the network gateway when the container has none. Set `"default": true` on a join to replace an existing one.
- **Address ranges**: each network can limit which addresses callers may claim, so lanatt stays out of your DHCP pool and away from static hosts.
- **Access control**: a UID allowlist. A caller can only join its own containers, never the host netns. Root is trusted only with `allowRoot`.
- **Survives restarts**: claims are stored on disk. Claims of containers that are gone are dropped.
- **Images**: `linux/amd64` and `linux/arm64` on GHCR.

Field-by-field reference: [docs/usage.md](docs/usage.md).

## FAQ

### Can a rootless Podman container get its own LAN IP with macvlan?

Yes. Install the lanatt hook files and add the `io.lanatt.attachments` annotation. See [examples/](examples/README.md).

### Does it work with rootless Docker?

Yes, through the `lanatt-runc` runtime: run the container with `--runtime lanatt`. A Docker network plugin cannot do this, and [ADR-0008](docs/adr/0008-engine-wiring.md) explains why.

### Why can't the host reach the container?

Macvlan does not let the host reach its own attachments through the parent NIC. Containers on the same host that use an engine bridge network are affected too. Give the host a macvlan shim on the same parent: [examples/](examples/README.md#host-shim) has a systemd unit that survives reboots. Other LAN hosts and other lanatt containers reach the attachment without one.

### Does it work with rootful containers?

Yes, with `"allowRoot": true` in the config. Rootful containers then use the same annotation and get the same duplicate-address protection as rootless ones. See [ADR-0009](docs/adr/0009-root-callers.md).

### Does it do IPv6, DHCP or ipvlan?

Not in v1. Addresses are static IPv4, and only the `macvlan` driver is accepted.

## Development

Requires [mise](https://mise.jdx.dev).

```bash
git clone https://github.com/BlackDark/lanatt && cd lanatt
mise install
mise run check
```

`go test` cannot create network devices. Kernel and engine tests run as root on a VM: see [docs/usage.md](docs/usage.md#acceptance). Design decisions are in [docs/adr/](docs/adr/), and domain terms are in [GLOSSARY.md](GLOSSARY.md).

---

<div align="center"><sub>

[Report a bug](https://github.com/BlackDark/lanatt/issues/new) · License: [MIT](LICENSE)

</sub></div>
