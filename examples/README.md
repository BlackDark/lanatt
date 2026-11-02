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

The lanatt daemon itself can run under Docker with the same flags as Podman: `--network host --pid host --privileged` and the three volumes.

## Testing

Test from another LAN host: the host cannot reach its own macvlan attachments. `test/engines-e2e.sh` installs these files on a test VM and checks every workload.
