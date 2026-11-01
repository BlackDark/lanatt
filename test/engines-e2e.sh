#!/bin/bash
# Podman end-to-end on a test VM, as root: the lanatt image runs the daemon (rootful),
# a rootless user starts nginx with the annotation, and nginx answers on its own LAN address.
# Usage: podman-e2e.sh up IMAGE-TAR ADDRESS   then curl http://ADDRESS from another LAN host
#        podman-e2e.sh down
set -euo pipefail

U=lanattdemo
PARENT=${PARENT:-eth0}
PREFIX=${PREFIX:-10.0.1.0/24}
GATEWAY=${GATEWAY:-10.0.1.1}
MAC=${MAC:-02:42:0a:00:01:40}
IMAGE=localhost/lanatt:dev
NGINX=docker.io/library/nginx:1.29-alpine
# Rootless Podman 5.4 reads only this directory unless containers.conf sets hooks_dir.
HOOKS=/usr/share/containers/oci/hooks.d
SOCK=/run/lanatt/lanatt.sock

as_user() { (cd /tmp && runuser -u "$U" -- env XDG_RUNTIME_DIR="/run/user/$(id -u "$U")" "$@"); }

up() {
  local tar=$1 addr=$2
  podman load -q -i "$tar"
  # The hook runs on the host, so the same binary is copied out of the image.
  cid=$(podman create "$IMAGE")
  podman cp "$cid:/usr/local/bin/lanatt" /usr/local/bin/lanatt
  podman rm -f "$cid" >/dev/null

  id "$U" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash "$U"
  loginctl enable-linger "$U"
  for _ in $(seq 50); do [[ -d /run/user/$(id -u "$U") ]] && break; sleep 0.1; done

  mkdir -p /etc/lanatt /run/lanatt /var/lib/lanatt "$HOOKS"
  cat >/etc/lanatt/config.json <<EOF
{"socket":"$SOCK","state":"/var/lib/lanatt/claims.json","uids":[$(id -u "$U")],
 "networks":[{"name":"lan","parent":"$PARENT","prefix":"$PREFIX","gateway":"$GATEWAY","driver":"macvlan"}]}
EOF
  for stage in create stop; do
    oci=createRuntime
    [[ $stage == stop ]] && oci=poststop
    cat >"$HOOKS/lanatt-$stage.json" <<EOF
{"version":"1.0.0",
 "hook":{"path":"/usr/local/bin/lanatt","args":["lanatt","hook","$stage","-socket","$SOCK"]},
 "when":{"annotations":{"^io\\\\.lanatt\\\\.attachments\$":".*"}},
 "stages":["$oci"]}
EOF
  done

  podman rm -f lanatt >/dev/null 2>&1 || true
  podman run -d --name lanatt --network host --pid host --privileged \
    -v /etc/lanatt:/etc/lanatt:ro -v /run/lanatt:/run/lanatt -v /var/lib/lanatt:/var/lib/lanatt \
    "$IMAGE" >/dev/null
  for _ in $(seq 50); do [[ -S $SOCK ]] && break; sleep 0.1; done

  as_user podman rm -f web >/dev/null 2>&1 || true
  as_user podman run -d --name web --network none \
    --annotation "io.lanatt.attachments=[{\"network\":\"lan\",\"address\":\"$addr\",\"mac\":\"$MAC\"}]" \
    "$NGINX" >/dev/null
  pid=$(as_user podman inspect -f '{{.State.Pid}}' web)
  echo "nginx (rootless, uid $(id -u "$U"), pid $pid) netns:"
  nsenter -t "$pid" --net ip -br addr
  nsenter -t "$pid" --net ip route
  echo "claims:"; cat /var/lib/lanatt/claims.json
}

down() {
  if id "$U" >/dev/null 2>&1; then
    as_user podman rm -f -t 2 web >/dev/null 2>&1 || true
    echo "claims after stop:"; cat /var/lib/lanatt/claims.json 2>/dev/null || true
    loginctl disable-linger "$U" || true
    loginctl terminate-user "$U" 2>/dev/null || true
    pkill -u "$U" 2>/dev/null || true
    sleep 2
    userdel -r "$U" 2>/dev/null || true
  fi
  podman rm -f lanatt >/dev/null 2>&1 || true
  podman rmi -f "$IMAGE" >/dev/null 2>&1 || true
  rm -rf /etc/lanatt /run/lanatt /var/lib/lanatt "$HOOKS/lanatt-create.json" "$HOOKS/lanatt-stop.json" /usr/local/bin/lanatt
}

case ${1:-} in
  up) up "${2:?image tar}" "${3:?address}" ;;
  down) down ;;
  *) echo "usage: $0 up IMAGE-TAR ADDRESS | down" >&2; exit 2 ;;
esac
