# Examples

Set up the host once, as root:

```sh
install -D -m 644 examples/config.json /etc/lanatt/config.json   # edit uids, allowRoot, parent, prefix, gateway
install -D -m 644 examples/podman/containers.conf.d/lanatt.conf /etc/containers/containers.conf.d/lanatt.conf
install -D -m 644 -t /etc/containers/oci/hooks.d examples/podman/hooks.d/*.json
# The hook runs on the host: install the release binary (amd64 or arm64) and check it.
arch=$(dpkg --print-architecture)   # or: uname -m, mapped to amd64/arm64
base=https://github.com/BlackDark/lanatt/releases/latest/download
cd "$(mktemp -d)" && curl -fsSLO "$base/lanatt-linux-$arch" -O "$base/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS && install -m 755 "lanatt-linux-$arch" /usr/local/bin/lanatt
```

Daemon as a rootful Quadlet:

```sh
install -D -m 644 examples/quadlet/lanatt.container /etc/containers/systemd/lanatt.container
systemctl daemon-reload && systemctl start lanatt
```

Workload as the allowlisted user, with plain Podman:

```sh
podman run -d --network none \
  --annotation 'io.lanatt.attachments=[{"network":"lan","address":"10.0.1.64","mac":"02:42:0a:00:01:40"}]' \
  docker.io/library/nginx:1.29-alpine
```

Workload as a rootless Quadlet. It needs `loginctl enable-linger <user>` to start at boot:

```sh
install -D -m 644 examples/quadlet/web.container ~/.config/containers/systemd/web.container
systemctl --user daemon-reload && systemctl --user start web
```

## Docker

Docker has no hook directory. `lanatt-runc` is a runtime wrapper: on `create`, it adds the lanatt hooks to an annotated bundle, then runs `runc`. The name after `lanatt-` picks the runtime it runs, so `lanatt-crun` runs `crun`. Install one symlink per runtime, as root:

```sh
ln -sf lanatt /usr/local/bin/lanatt-runc
ln -sf lanatt /usr/local/bin/lanatt-crun   # only if you use crun
```

Register them in the daemon config (`lanatt` and `lanatt-crun` in [`daemon.json`](docker/daemon.json)). Rootful Docker reads `/etc/docker/daemon.json`; rootless Docker reads `~/.config/docker/daemon.json`. Then restart Docker:

```sh
install -D -m 644 examples/docker/daemon.json ~/.config/docker/daemon.json
systemctl --user restart docker
docker run -d --runtime lanatt --network none \
  --annotation 'io.lanatt.attachments=[{"network":"lan","address":"10.0.1.66","mac":"02:42:0a:00:01:42"}]' \
  docker.io/library/nginx:1.29-alpine
```

Compose: `docker compose -f examples/docker/compose.yaml up -d`.

The lanatt daemon itself can run under Docker with the same flags as Podman (see [`docs/usage.md`](../docs/usage.md#container-image)) and the three volumes. That form is untested.

## Host shim

The host cannot reach its own macvlan attachments, and neither can containers whose traffic leaves through the host (engine bridge networks, host network). That matters when the host uses a service on an attachment, such as a LAN-wide DNS server. A shim is a macvlan on the same parent, owned by the host. lanatt does not create it (ADR-0006): it must exist before the daemon starts and outlive the daemon.

[`shim/lanatt-shim@.service`](shim/lanatt-shim@.service) creates it at boot, independent of the network manager. The instance name is the parent NIC:

```sh
install -m 644 examples/shim/lanatt-shim@.service /etc/systemd/system/
install -D -m 644 examples/shim/shim-eth0.env /etc/lanatt/shim-eth0.env   # edit SHIM, ADDRESS, ROUTE
systemctl daemon-reload && systemctl enable --now lanatt-shim@eth0
```

- `ADDRESS` is a free LAN address for the host's side, outside every lanatt range. It is a `/32`, so the host's normal LAN route stays on the parent.
- `ROUTE` is what goes through the shim, normally the lanatt ranges (`10.0.1.64/29` covers `.64`–`.71`).

Tested on Debian 13 with systemd-networkd: the host and a Docker bridge container reach an attachment through the shim, also after a reboot, and the host's own LAN traffic is unaffected. NetworkManager users can create the same macvlan with `nmcli connection add type macvlan dev eth0 mode bridge ...` instead.

## Testing

Test from another LAN host: the host cannot reach its own macvlan attachments. `test/engines-e2e.sh` installs these files on a test VM and checks every workload.
