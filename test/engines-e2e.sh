#!/bin/bash
# Engine end-to-end on a test VM, as root. It installs examples/ as documented. The daemon
# runs as a rootful Quadlet. A rootless user then starts nginx three ways:
#   podman run on .64, a rootless Quadlet on .65, rootless Docker Compose on .66,
#   and rootless Docker with crun as the target runtime on .69.
# Usage: engines-e2e.sh up IMAGE-TAR EXAMPLES-DIR   then curl .64/.65/.66 from another LAN host
#        engines-e2e.sh down
set -euo pipefail

U=lanattdemo
IMAGE=localhost/lanatt:dev
NGINX=docker.io/library/nginx:1.29-alpine
SOCK=/run/lanatt/lanatt.sock
DBIN=/usr/local/lib/lanatt-e2e

uid() { id -u "$U"; }
as_user() {
  (cd /tmp && runuser -u "$U" -- env XDG_RUNTIME_DIR="/run/user/$(uid)" \
    DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$(uid)/bus" \
    DOCKER_HOST="unix:///run/user/$(uid)/docker.sock" PATH="$DBIN:/usr/local/bin:/usr/bin:/usr/sbin:/bin" "$@")
}
wait_for() { for _ in $(seq 100); do eval "$1" && return 0; sleep 0.2; done; echo "timeout: $1" >&2; return 1; }
show() {
  local pid=$1
  echo "--- $2 (pid $pid, user $(ps -o user= -p "$pid"))"
  nsenter -t "$pid" --net ip -4 -br addr show lan0
  nsenter -t "$pid" --net ip route show default
}

up() {
  local tar=$1 ex=$2
  podman load -q -i "$tar"
  cid=$(podman create "$IMAGE")
  podman cp "$cid:/usr/local/bin/lanatt" /usr/local/bin/lanatt
  podman rm -f "$cid" >/dev/null
  ln -sf lanatt /usr/local/bin/lanatt-runc
  ln -sf lanatt /usr/local/bin/lanatt-crun

  id "$U" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash "$U"
  loginctl enable-linger "$U"
  wait_for '[[ -S /run/user/$(uid)/bus ]]'

  # Host setup, as in examples/README.md.
  sed "s/\"uids\": \[1000\]/\"uids\": [$(uid)]/" "$ex/config.json" | install -D -m 644 /dev/stdin /etc/lanatt/config.json
  install -D -m 644 "$ex/podman/containers.conf.d/lanatt.conf" /etc/containers/containers.conf.d/lanatt.conf
  install -D -m 644 -t /etc/containers/oci/hooks.d "$ex"/podman/hooks.d/*.json
  sed "s|^Image=.*|Image=$IMAGE|" "$ex/quadlet/lanatt.container" >/etc/containers/systemd/lanatt.container
  systemctl daemon-reload
  systemctl start lanatt
  wait_for "[[ -S $SOCK ]]"

  # 1. Plain rootless Podman.
  as_user podman run -d --name web --network none \
    --annotation 'io.lanatt.attachments=[{"network":"lan","address":"10.0.1.64","mac":"02:42:0a:00:01:40"}]' \
    "$NGINX" >/dev/null
  show "$(as_user podman inspect -f '{{.State.Pid}}' web)" "podman run"

  # 2. Rootless Quadlet.
  as_user install -D -m 644 "$ex/quadlet/web.container" "/home/$U/.config/containers/systemd/web-quadlet.container"
  sed -i 's/^ContainerName=web$/ContainerName=web-quadlet/' "/home/$U/.config/containers/systemd/web-quadlet.container"
  as_user systemctl --user daemon-reload
  as_user systemctl --user start web-quadlet
  show "$(as_user podman inspect -f '{{.State.Pid}}' web-quadlet)" "rootless quadlet"

  # 3. Rootless Docker with the lanatt-runc runtime.
  # The setup tool wants dockerd-rootless.sh, docker and dockerd in one directory.
  install -D -m 755 /usr/share/docker.io/contrib/dockerd-rootless.sh "$DBIN/dockerd-rootless.sh"
  ln -sf /usr/bin/docker "$DBIN/docker"
  ln -sf /usr/sbin/dockerd "$DBIN/dockerd"
  as_user install -D -m 644 "$ex/docker/daemon.json" "/home/$U/.config/docker/daemon.json"
  as_user bash /usr/share/docker.io/contrib/dockerd-rootless-setuptool.sh install --skip-iptables >/dev/null 2>&1
  wait_for 'as_user docker info >/dev/null 2>&1'
  as_user docker compose -f "$ex/docker/compose.yaml" -p lanatt up -d --quiet-pull 2>/dev/null
  show "$(as_user docker inspect -f '{{.State.Pid}}' lanatt-web-1)" "rootless docker compose"

  # 4. Rootless Docker with crun as the target runtime.
  as_user docker run -d --name web-crun --runtime lanatt-crun --network none \
    --annotation 'io.lanatt.attachments=[{"network":"lan","address":"10.0.1.69","mac":"02:42:0a:00:01:45"}]' \
    "$NGINX" >/dev/null
  echo "--- web-crun runtime: $(as_user docker inspect -f '{{.HostConfig.Runtime}}' web-crun)"
  # runc keeps state.json in its state dir; crun keeps status. Docker names the root runtime-runc either way.
  state="/run/user/$(uid)/docker/runtime-runc/moby/$(as_user docker inspect -f '{{.Id}}' web-crun)"
  if [[ -e $state/status && ! -e $state/state.json ]]; then echo "--- web-crun was created by crun"; else
    echo "--- web-crun was NOT created by crun: $(ls "$state")"; exit 1; fi
  show "$(as_user docker inspect -f '{{.State.Pid}}' web-crun)" "rootless docker, lanatt-crun"

  echo "--- claims"; cat /var/lib/lanatt/claims.json
}

down() {
  if id "$U" >/dev/null 2>&1; then
    as_user docker rm -f web-crun >/dev/null 2>&1 || true
    as_user docker compose -p lanatt down -t 2 >/dev/null 2>&1 || true
    as_user systemctl --user stop web-quadlet 2>/dev/null || true
    as_user podman rm -f -t 2 web >/dev/null 2>&1 || true
    echo "--- claims after stop"; cat /var/lib/lanatt/claims.json 2>/dev/null || true
    if [[ -f /var/lib/lanatt/claims.json ]] && grep -q '"address"' /var/lib/lanatt/claims.json; then
      echo "FAIL: claims left after every container stopped" >&2
      leftover=1
    fi
    as_user bash /usr/share/docker.io/contrib/dockerd-rootless-setuptool.sh uninstall --skip-iptables >/dev/null 2>&1 2>&1 || true
    loginctl disable-linger "$U" || true
    loginctl terminate-user "$U" 2>/dev/null || true
    pkill -u "$U" 2>/dev/null || true
    sleep 2
    userdel -r "$U" 2>/dev/null || true
  fi
  systemctl stop lanatt 2>/dev/null || true
  rm -f /etc/containers/systemd/lanatt.container
  systemctl daemon-reload
  podman rmi -f "$IMAGE" >/dev/null 2>&1 || true
  rm -rf /etc/lanatt /run/lanatt /var/lib/lanatt /etc/containers/oci /etc/containers/containers.conf.d \
    /usr/local/bin/lanatt /usr/local/bin/lanatt-runc /usr/local/bin/lanatt-crun "$DBIN"
  [[ -z ${leftover:-} ]]
}

case ${1:-} in
  up) up "${2:?image tar}" "${3:?examples dir}" ;;
  down) down ;;
  *) echo "usage: $0 up IMAGE-TAR EXAMPLES-DIR | down" >&2; exit 2 ;;
esac
