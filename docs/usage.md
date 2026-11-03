# Using lanatt

One binary, two roles.

## Daemon (root)

```sh
lanatt daemon -config /etc/lanatt/config.json
```

```json
{
  "socket": "/run/lanatt/lanatt.sock",
  "state": "/var/lib/lanatt/claims.json",
  "uids": [1000],
  "allowRoot": false,
  "networks": [
    {"name": "lab", "parent": "eno1", "prefix": "192.168.10.0/24", "gateway": "192.168.10.1", "driver": "macvlan",
     "ranges": ["192.168.10.200-192.168.10.219"]}
  ]
}
```

- `socket` and `state` default to the values above.
- `uids` is the allowlist. `0` is refused there. An empty list fails at load unless `allowRoot` is set.
- `allowRoot` (default `false`) accepts UID 0 callers, the hooks of rootful containers. A root caller may join any pid's netns except the host's; every other check applies. See ADR-0009.
- `driver` must be `macvlan`; the device is created in bridge mode.
- `ranges` (optional) limits which addresses callers may claim: `"a-b"` (inclusive) or CIDR entries. Each must hold only host addresses of `prefix`, exclude the gateway, and not overlap another. Without it, any host address in `prefix` except the gateway is allowed.
- `gateway` must be a host address in `prefix`. lanatt does not create the host shim.
- On start, and before each attach, claims whose netns no longer exists are dropped.
- On start, host macvlans named `lt<8 hex><n>` are deleted. They are lanatt's temporary devices, left behind only if the daemon died between creating one and moving it into the netns. Do not give your own devices names of that form.
- The caller must be the real host UID of the target pid. Stop drops only the caller's own claims for that container id.

The daemon calls `/usr/sbin/ip` and `/usr/bin/nsenter`.

## Hook (caller UID)

Register as OCI hooks:

- `createRuntime`: `lanatt hook create [-socket /run/lanatt/lanatt.sock]`
- `poststop`: `lanatt hook stop [-socket /run/lanatt/lanatt.sock]`

The hook reads the OCI state on stdin and the annotation `io.lanatt.attachments` from the bundle's `config.json`. No annotation: exit 0, the daemon is not contacted. A refusal exits 1, so the engine does not start the workload.

```json
[
  {"network": "lab", "address": "192.168.10.200", "mac": "02:42:c0:a8:0a:c8"},
  {"network": "iot", "address": "10.20.0.50", "mac": "02:42:0a:14:00:32", "interface": "iot", "default": true}
]
```

- `interface` defaults to `lan0`, `lan1`, … in list order.
- `default: true` replaces the netns default route with one via the network gateway. Without it, a default route is added only when the netns has none.
- The same join again is success. A different address or MAC for the same network and netns, or an address or MAC another netns holds on that network, is refused.

## Container image

The image runs the daemon. Linux `amd64` and `arm64`, published to GHCR when a release-please release PR is merged. It needs the host netns, the host pid namespace, root, and three capabilities:

```sh
podman run -d --name lanatt --network host --pid host \
  --cap-drop=ALL --cap-add=NET_ADMIN,SYS_ADMIN,SYS_PTRACE \
  --security-opt apparmor=unconfined --security-opt no-new-privileges --read-only \
  -v /etc/lanatt:/etc/lanatt:ro -v /run/lanatt:/run/lanatt -v /var/lib/lanatt:/var/lib/lanatt \
  ghcr.io/blackdark/lanatt:<version>
```

- `NET_ADMIN` creates the macvlan and configures it. `SYS_ADMIN` enters the workload netns (`setns`). `SYS_PTRACE` opens another user's `/proc/<pid>/ns/net`. Dropping any one of them breaks the join; this was checked one by one.
- AppArmor: Podman's `containers-default` profile denies reading another process's `/proc/<pid>/ns/net` even with `SYS_PTRACE`, so the container runs unconfined. Docker applies its own `docker-default` profile; running the daemon under Docker with these flags is untested.
- The root filesystem can be read-only: the daemon writes only to the `/run/lanatt` and `/var/lib/lanatt` mounts.

Set `"socket": "/run/lanatt/lanatt.sock"` in the config so the socket appears on the host through the `/run/lanatt` mount.

## Engines

The hook runs on the host. Each release attaches `lanatt-linux-amd64`, `lanatt-linux-arm64` and `SHA256SUMS`; install the binary as `/usr/local/bin/lanatt` (see [`examples/`](../examples/README.md)). The binary is the same as the one in the image.

- **Podman** finds hooks through `hooks_dir`. Its built-in default is only `/usr/share/containers/oci/hooks.d`, which belongs to packages. lanatt's hooks go in `/etc/containers/oci/hooks.d`, which a `containers.conf.d` drop-in adds. Rootful and rootless Podman both read it. A rootful container's hook runs as UID 0: it needs `"allowRoot": true`, and without it an annotated rootful container fails to start.
- **Docker** has no hook directory. `lanatt-runc`, a symlink to `lanatt`, is registered as a Docker runtime. On `create` it adds the lanatt hooks to an annotated bundle, then runs `runc`. Unannotated bundles pass through unchanged.
  - **Target runtime:** the wrapper runs the name after `lanatt-`, looked up on `PATH`, so `lanatt-crun` runs `crun`. Register one symlink per target. `LANATT_RUNTIME`, a bare name or an absolute path, overrides the symlink name for callers that can set the environment; Docker cannot.

Ready-to-install files for Podman, Quadlet, Docker and Compose are in [`examples/`](../examples/README.md).

The host cannot reach its own macvlan attachments. Test from another LAN host.

## Performance

An attachment is a kernel macvlan, so its traffic takes the kernel's normal path, like the host's own NIC. No user-space forwarder sits in between, unlike Podman's rootless networks:

| Path | Datapath |
|---|---|
| lanatt attachment | Kernel macvlan |
| `--network host`, systemd socket activation | Kernel, the host's own sockets |
| `pasta` (rootless default since Podman 5.0) | User space, no NAT |
| `slirp4netns` (legacy, planned for removal in Podman 6) | User space with NAT, the slowest |

The Podman-side ordering comes from the references below. lanatt's own throughput has not been benchmarked.

- **Fastest:** run the workload with `--network none`, so the attachment is its only network and every packet, inbound and outbound, stays in the kernel. The examples do this.
- **Keeping an engine network** (`pasta` or a custom network) next to the attachment: the default route stays on the engine network unless the join sets `"default": true`. Without that flag, internet-bound traffic goes through `pasta`; LAN traffic for the prefix still uses the attachment.
- Port publishing (`-p`, `PublishPort=`) does not apply to the attachment: services listen on the attachment's address directly.
- The MTU is the parent NIC's.

## References

Rootless Podman networking, for workloads that combine lanatt with Podman's own networks:

- [eriksjolund/podman-networking-docs](https://github.com/eriksjolund/podman-networking-docs): methods compared by performance, source address preservation and binding, with Quadlet examples. Covers socket activation and pasta options such as `--map-gw` and `-T`.
- [Podman pasta vs slirp4netns](https://sanj.dev/post/podman-pasta-vs-slirp4netns-networking/): why pasta avoids slirp4netns's user-space NAT, how to check the active backend, and common pasta pitfalls (a `127.0.0.53` DNS loop with systemd-resolved, broken IPv6 paths, VPN interface choice).
- [How to use pasta networking with Podman](https://oneuptime.com/blog/post/2026-03-17-use-pasta-networking-podman/view): installing `passt`, passing pasta options, port forwarding and IPv6.

## Acceptance

`test/accept.sh` drives the daemon through the hook against a dummy parent and `10.254.1.0/28`. Root on a test VM only (`VM=<host>`):

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/lanatt ./cmd/lanatt
scp /tmp/lanatt test/accept.sh root@$VM:/tmp/
ssh root@$VM bash /tmp/accept.sh /tmp/lanatt
```

Engines end to end. This uses the real LAN, so use only the addresses you have been given (`10.0.1.64`–`69`). It installs `examples/` on the VM, starts the daemon as a rootful Quadlet, and starts nginx as a rootless user with `podman run` on `.64`, a Quadlet on `.65`, Docker Compose on `.66`, and Docker with `lanatt-crun` on `.69`. With `allowRoot`, it also starts rootful Podman on `.67` and rootful Docker on `.68`. It needs `podman`, `docker.io`, `docker-compose`, `rootlesskit`, `slirp4netns`, `uidmap` and `dbus-user-session`.

```sh
docker buildx build --platform linux/amd64 -o type=docker,dest=/tmp/lanatt.tar -t localhost/lanatt:dev .
scp /tmp/lanatt.tar test/engines-e2e.sh root@$VM:/tmp/ && scp -r examples root@$VM:/tmp/lanatt-ex
ssh root@$VM bash /tmp/engines-e2e.sh up /tmp/lanatt.tar /tmp/lanatt-ex
for a in 64 65 66; do curl -s http://10.0.1.$a/ | grep title; done
ssh root@$VM bash /tmp/engines-e2e.sh down
```
