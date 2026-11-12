#!/bin/bash
# Acceptance run for lanatt. Root on a test VM only. Parent is a dummy, never eth0.
# Usage: accept.sh /path/to/lanatt
set -u
BIN=${1:?usage: accept.sh /path/to/lanatt}
WORK=/tmp/lanatt-accept
SOCK=$WORK/lanatt.sock
STATE=$WORK/claims.json
CFG=$WORK/config.json
PARENT=latparent
PARENT2=latparent2 # ipvlan cannot share a parent with macvlan
U=lanattu
X=lanattx
GW=10.254.1.14

pass=0
fail=0
ok() { echo "PASS  $1"; pass=$((pass + 1)); }
bad() { echo "FAIL  $1"; fail=$((fail + 1)); }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }

PIDS=()
DAEMON=
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  [[ -n "$DAEMON" ]] && kill "$DAEMON" 2>/dev/null && wait "$DAEMON" 2>/dev/null
  ip link del "$PARENT" 2>/dev/null || true
  ip link del "$PARENT2" 2>/dev/null || true
  rm -rf "$WORK"
  for u in "$U" "$X"; do
    if id "$u" >/dev/null 2>&1; then userdel -r "$u" >/dev/null 2>&1 || userdel "$u" >/dev/null 2>&1 || true; fi
  done
}
trap cleanup EXIT
cleanup

for u in "$U" "$X"; do useradd --system --create-home --shell /bin/bash "$u"; done
mkdir -p "$WORK"
chmod 755 "$WORK"
ip link add "$PARENT" type dummy
ip link set "$PARENT" up
ip link add "$PARENT2" type dummy
ip link set "$PARENT2" up

cat >"$CFG" <<EOF
{"socket":"$SOCK","state":"$STATE","uids":[$(id -u "$U")],
 "networks":[{"name":"lab","parent":"$PARENT","prefix":"10.254.1.0/28","gateway":"$GW","driver":"macvlan"},
  {"name":"lab2","parent":"$PARENT","prefix":"10.254.1.16/28","gateway":"10.254.1.30","driver":"macvlan","ranges":["10.254.1.17-10.254.1.20"]},
  {"name":"lab6","parent":"$PARENT","prefix":"10.254.1.48/28","gateway":"10.254.1.62","driver":"macvlan","prefix6":"fd77:254::/64","gateway6":"fd77:254::1"},
  {"name":"lab4","parent":"$PARENT","prefix":"10.254.1.64/28","gateway":"10.254.1.78","driver":"macvlan","noSLAAC":true},
  {"name":"vl","parent":"$PARENT2","prefix":"10.254.1.32/28","gateway":"10.254.1.46","driver":"ipvlan"}]}
EOF

start_daemon() {
  "$BIN" daemon -config "$CFG" >>"$WORK/daemon.log" 2>&1 &
  DAEMON=$!
  for _ in $(seq 50); do [[ -S "$SOCK" ]] && return 0; sleep 0.1; done
  echo "daemon did not start"; cat "$WORK/daemon.log"; exit 1
}
stop_daemon() { kill "$DAEMON"; wait "$DAEMON" 2>/dev/null; DAEMON=; }

# Rootless netns owned by user $1. With $2=def it already has a default route.
NSPID=
netns() {
  local setup='exec sleep 300'
  if [[ "${2:-}" == def ]]; then
    setup='ip link add d0 type dummy && ip link set d0 up && ip addr add 192.0.2.2/32 dev d0 && ip route add default via 192.0.2.1 dev d0 onlink && exec sleep 300'
  fi
  runuser -u "$1" -- unshare --user --map-root-user --net sh -c "$setup" &
  PIDS+=("$!")
  NSPID=
  for _ in $(seq 50); do
    local p
    p=$(pgrep -n -u "$1" -x sleep)
    if [[ -n "$p" && " ${PIDS[*]} " != *" $p "* && "$(readlink /proc/$p/ns/net 2>/dev/null)" != "$(readlink /proc/self/ns/net)" ]]; then
      NSPID=$p
      PIDS+=("$p")
      return 0
    fi
    sleep 0.1
  done
  echo "netns for $1 did not appear"; exit 1
}

# hook USER STAGE ID PID ANNOTATION-JSON-OR-EMPTY
hook() {
  local user=$1 stage=$2 id=$3 pid=$4 ann=$5 b="$WORK/bundle-$3"
  mkdir -p "$b"
  if [[ -n "$ann" ]]; then
    printf '{"annotations":{"io.lanatt.attachments":%s}}' "$(printf '%s' "$ann" | sed 's/\\/\\\\/g; s/"/\\"/g; s/^/"/; s/$/"/')" >"$b/config.json"
  else
    echo '{"annotations":{}}' >"$b/config.json"
  fi
  chmod -R a+rX "$b"
  printf '{"ociVersion":"1.0.2","id":"%s","status":"creating","pid":%s,"bundle":"%s"}' "$id" "$pid" "$b" |
    runuser -u "$user" -- "$BIN" hook "$stage" -socket "$SOCK" 2>>"$WORK/hook.err"
}
join() { printf '[{"network":"lab","address":"%s","mac":"%s"%s}]' "$1" "$2" "${3:-}"; }
in_ns() { nsenter -t "$1" --net ip "${@:2}"; }

ip link add lt0000abcd0 link "$PARENT" type macvlan mode bridge
start_daemon
check "leftover temporary macvlan reaped at start" "! ip link show lt0000abcd0 >/dev/null 2>&1"

# 1. A rootless netns gains the attachment, and a default route via the gateway.
netns "$U"; A=$NSPID
check "rootless netns joins" "hook $U create ca $A '$(join 10.254.1.2 02:42:0a:fe:01:02)'"
check "address in netns" "in_ns $A -4 -br addr show lan0 | grep -q 10.254.1.2/28"
check "MAC in netns" "in_ns $A link show lan0 | grep -q 02:42:0a:fe:01:02"
check "connected route" "in_ns $A route show | grep -q '10.254.1.0/28 dev lan0'"
check "empty netns gains default via gateway" "in_ns $A route show default | grep -q 'via $GW dev lan0'"
check "successful join leaves no pending record" "test -f $STATE && ! grep -q pending $STATE"
# 2. The same join again succeeds.
check "retry succeeds" "hook $U create ca $A '$(join 10.254.1.2 02:42:0a:fe:01:02)'"
check "retry adds no interface" "[[ \$(in_ns $A -br link show type macvlan | wc -l) -eq 1 ]]"

# 3. A second netns with the same address is refused.
netns "$U"; B=$NSPID
check "same address from another netns refused" "! hook $U create cb $B '$(join 10.254.1.2 02:42:0a:fe:01:03)'"
check "refusal names the holder" "grep -q 'address 10.254.1.2 is taken by claim' '$WORK/hook.err'"
check "same MAC from another netns refused" "! hook $U create cb $B '$(join 10.254.1.3 02:42:0a:fe:01:02)'"
check "refused netns has no attachment" "! in_ns $B link show lan0 >/dev/null 2>&1"
check "no macvlan left on host" "! ip -br link show type macvlan | grep -q ."

# 4. A netns with a default route keeps it.
netns "$U" def; C=$NSPID
check "netns with default joins" "hook $U create cc $C '$(join 10.254.1.4 02:42:0a:fe:01:04)'"
check "existing default kept" "in_ns $C route show default | grep -q 'via 192.0.2.1 dev d0'"

# 5. A join marked default replaces the existing default.
netns "$U" def; D=$NSPID
check "default join succeeds" "hook $U create cd $D '$(join 10.254.1.5 02:42:0a:fe:01:05 ',"default":true,"interface":"uplink"')'"
check "default replaced" "in_ns $D route show default | grep -q 'via $GW dev uplink' && ! in_ns $D route show default | grep -q 192.0.2.1"

# 6. Stop drops the claim; the address joins from a new netns.
check "stop succeeds" "hook $U stop ca 0 '$(join 10.254.1.2 02:42:0a:fe:01:02)'"
kill "$A"; sleep 0.3
check "address free after stop" "hook $U create cb $B '$(join 10.254.1.2 02:42:0a:fe:01:02)'"

# 7. A UID that is not allowlisted is refused.
netns "$X"; E=$NSPID
check "non-allowlisted uid refused" "! hook $X create ce $E '$(join 10.254.1.6 02:42:0a:fe:01:06)'"

# 8. Another user's netns is refused.
check "netns of another uid refused" "! hook $U create ce $E '$(join 10.254.1.6 02:42:0a:fe:01:06)'"

# 9. The host netns is refused.
runuser -u "$U" -- sleep 300 &
PIDS+=("$!"); sleep 0.3
H=$(pgrep -n -u "$U" -x sleep); PIDS+=("$H")
check "host netns refused" "! hook $U create ch $H '$(join 10.254.1.7 02:42:0a:fe:01:07)'"

# 10. No annotation leaves the workload alone, even without a daemon.
netns "$U"; F=$NSPID
check "no annotation succeeds" "hook $U create cf $F ''"
check "no annotation adds nothing" "! in_ns $F link show lan0 >/dev/null 2>&1"

# 11. Several joins in one annotation get lan0, lan1.
netns "$U"; G=$NSPID
two='[{"network":"lab","address":"10.254.1.8","mac":"02:42:0a:fe:01:08"},{"network":"lab2","address":"10.254.1.18","mac":"02:42:0a:fe:01:18"}]'
check "two networks in one annotation" "hook $U create cg $G '$two'"
check "first is lan0" "in_ns $G -4 -br addr show lan0 | grep -q 10.254.1.8/28"
check "second is lan1" "in_ns $G -4 -br addr show lan1 | grep -q 10.254.1.18/28"

# 11b. lab2 only hands out .17-.20.
netns "$U"; K=$NSPID
check "address outside ranges refused" "! hook $U create ck $K '[{\"network\":\"lab2\",\"address\":\"10.254.1.25\",\"mac\":\"02:42:0a:fe:01:25\"}]'"
check "address inside ranges joins" "hook $U create ck $K '[{\"network\":\"lab2\",\"address\":\"10.254.1.19\",\"mac\":\"02:42:0a:fe:01:19\"}]'"

# 12. A requested name that the netns already uses is refused and the device survives.
netns "$U" def; I=$NSPID
check "clashing interface name refused" "! hook $U create ci $I '$(join 10.254.1.9 02:42:0a:fe:01:0a ',"interface":"d0"')'"
check "existing device untouched" "in_ns $I -4 -br addr show d0 | grep -q 192.0.2.2"
check "failed join left no temporary device" "in_ns $I -o link show >/dev/null && ! in_ns $I -o link show | grep -qE ': lt[0-9a-f]{8}[0-9]+[@:]'"
check "failed join left no pending record" "test -f $STATE && ! grep -q pending $STATE"
check "address free after failed join" "hook $U create ci $I '$(join 10.254.1.9 02:42:0a:fe:01:0a)'"

# 13. Claims survive a restart; a dead netns's claim is dropped.
stop_daemon
kill "$C"; sleep 0.3
start_daemon
check "claim survives restart" "! hook $U create cf $F '$(join 10.254.1.5 02:42:0a:fe:01:09)'"
check "dead netns claim dropped" "hook $U create cf $F '$(join 10.254.1.4 02:42:0a:fe:01:04)'"
check "startup logs the dropped claim" "grep -q 'claims: .* loaded' '$WORK/daemon.log' && grep -q 'dropped claim .* 10.254.1.4: owner gone' '$WORK/daemon.log'"

# 14. Root callers: refused unless allowRoot; then any pid's netns, never the host netns.
unshare --net sleep 300 &
R=$!; PIDS+=("$R"); sleep 0.3
check "root refused without allowRoot" "! hook root create cr $R '$(join 10.254.1.10 02:42:0a:fe:01:10)'"
stop_daemon
sed -i 's/"uids":/"allowRoot":true,"uids":/' "$CFG"
start_daemon
check "root netns joins with allowRoot" "hook root create cr $R '$(join 10.254.1.10 02:42:0a:fe:01:10)'"
check "root netns has the address" "in_ns $R -4 -br addr show lan0 | grep -q 10.254.1.10/28"
netns "$U"; J=$NSPID
check "root joins another uid's netns" "hook root create cj $J '$(join 10.254.1.11 02:42:0a:fe:01:11)'"
check "user cannot detach root's claim" "hook $U stop cj 0 '$(join 10.254.1.11 02:42:0a:fe:01:11)' && grep -q 10.254.1.11 $STATE"
check "root detaches its claim" "hook root stop cj 0 '$(join 10.254.1.11 02:42:0a:fe:01:11)' && ! grep -q 10.254.1.11 $STATE"
sleep 300 &
RH=$!; PIDS+=("$RH")
check "host netns refused for root" "! hook root create crh $RH '$(join 10.254.1.12 02:42:0a:fe:01:12)'"
unshare --net sleep 300 &
R2=$!; PIDS+=("$R2"); sleep 0.3
check "address clash refused for root" "! hook root create crx $R2 '$(join 10.254.1.2 02:42:0a:fe:01:13)'"

# 15. A claim whose owner died is dropped even when another process still holds its netns
# inode; a new workload on the same address must join. lab2 hands out .17-.20.
netns "$U"; L=$NSPID
check "stale owner joins" "hook $U create cl $L '[{\"network\":\"lab2\",\"address\":\"10.254.1.20\",\"mac\":\"02:42:0a:fe:01:20\"}]'"
nsenter -t "$L" --net sleep 300 &
HOLD=$!; PIDS+=("$HOLD"); sleep 0.3
kill "$L"; sleep 0.3
check "inode still held after owner exit" "readlink /proc/$HOLD/ns/net >/dev/null"
netns "$U"; M=$NSPID
check "new workload gets the stale address" "hook $U create cm $M '[{\"network\":\"lab2\",\"address\":\"10.254.1.20\",\"mac\":\"02:42:0a:fe:01:21\"}]'"
check "new workload has the address" "in_ns $M -4 -br addr show lan0 | grep -q 10.254.1.20/28"

# 16. `lanatt claims` shows the owner, then the reason once the owner is gone, and never writes.
netns "$U"; Q=$NSPID
check "owner joins for claims listing" "hook $U create cq $Q '[{\"network\":\"lab2\",\"address\":\"10.254.1.17\",\"mac\":\"02:42:0a:fe:01:17\"}]'"
cp "$STATE" "$WORK/state.before"
check "claims lists the live owner" "$BIN claims -config $CFG | grep -E '10\.254\.1\.17 .* $Q/sleep .*-\$'"
check "claims -json marks it alive" "$BIN claims -config $CFG -json | grep -q '\"alive\": true'"
kill "$Q"; sleep 0.3
check "claims shows owner none with reason" "$BIN claims -config $CFG | grep -E '10\.254\.1\.17 .* none .* owner gone\$'"
check "claims leaves the state file unchanged" "cmp -s '$STATE' '$WORK/state.before'"

# 17. ipvlan: attachments share the parent MAC, so no MAC is taken or required.
netns "$U"; V1=$NSPID
check "ipvlan join without MAC" "hook $U create cv1 $V1 '[{\"network\":\"vl\",\"address\":\"10.254.1.33\"}]'"
check "ipvlan device in netns" "in_ns $V1 -d link show lan0 | grep -q 'ipvlan'"
netns "$U"; V2=$NSPID
check "second ipvlan netns on the same parent joins" "hook $U create cv2 $V2 '[{\"network\":\"vl\",\"address\":\"10.254.1.34\"}]'"
netns "$U"; V3=$NSPID
check "ipvlan join with a MAC refused" "! hook $U create cv3 $V3 '[{\"network\":\"vl\",\"address\":\"10.254.1.35\",\"mac\":\"02:42:0a:fe:01:35\"}]'"

# 18. IPv6 (ADR-0012): static ULA address, no SLAAC, v6 default via gateway6, DAD refusal.
netns "$U"; S1=$NSPID
check "dual-stack join" "hook $U create cs1 $S1 '[{\"network\":\"lab6\",\"address\":\"10.254.1.50\",\"mac\":\"02:42:0a:fe:01:50\",\"address6\":\"fd77:254::50\"}]'"
check "accept_ra is 0 in the netns" "[[ \$(nsenter -t $S1 --net cat /proc/sys/net/ipv6/conf/lan0/accept_ra) == 0 ]]"
check "accept_dad is 1 in the netns" "[[ \$(nsenter -t $S1 --net cat /proc/sys/net/ipv6/conf/lan0/accept_dad) == 1 ]]"
check "disable_ipv6 is 0 in the netns" "[[ \$(nsenter -t $S1 --net cat /proc/sys/net/ipv6/conf/lan0/disable_ipv6) == 0 ]]"
check "autoconf is 0 in the netns" "[[ \$(nsenter -t $S1 --net cat /proc/sys/net/ipv6/conf/lan0/autoconf) == 0 ]]"
check "dad_transmits is 1 in the netns" "[[ \$(nsenter -t $S1 --net cat /proc/sys/net/ipv6/conf/lan0/dad_transmits) == 1 ]]"
check "the only global v6 address is the static one" "[[ \$(in_ns $S1 -6 -o addr show dev lan0 scope global | awk '{print \$4}') == fd77:254::50/64 ]]"
check "v6 default via gateway6" "in_ns $S1 -6 route show default | grep -q 'via fd77:254::1 dev lan0'"
check "v4 default still via gateway" "in_ns $S1 -4 route show default | grep -q 'via 10.254.1.62 dev lan0'"
netns "$U"; S2=$NSPID
ip link add dupv link "$PARENT" type macvlan mode bridge
ip link set dupv netns "$S2"
in_ns $S2 link set dupv name dupv0
in_ns $S2 addr add fd77:254::51/64 dev dupv0
in_ns $S2 link set dupv0 up
sleep 2
netns "$U"; S3=$NSPID
check "duplicate address6 on the LAN refused by DAD" "! hook $U create cs3 $S3 '[{\"network\":\"lab6\",\"address\":\"10.254.1.51\",\"mac\":\"02:42:0a:fe:01:51\",\"address6\":\"fd77:254::51\"}]'"
check "refusal names the DAD duplicate" "grep -q 'address6 fd77:254::51 is already in use on the LAN' '$WORK/hook.err'"
check "DAD refusal leaves no device in the netns" "[[ \$(in_ns $S3 -br link show | grep -c lan0) -eq 0 ]]"
check "DAD refusal writes no claim" "! grep -q 'fd77:254::51' '$STATE'"

# 19. default: true replaces an existing v6 default and keeps its metric (a route at metric 1024 would lose to it).
netns "$U"; S4=$NSPID
in_ns $S4 link add d0 type dummy
in_ns $S4 link set d0 up
in_ns $S4 -6 route add default via fd77:255::1 dev d0 metric 100 onlink
check "replacing join with default true" "hook $U create cs4 $S4 '[{\"network\":\"lab6\",\"address\":\"10.254.1.60\",\"mac\":\"02:42:0a:fe:01:60\",\"address6\":\"fd77:254::60\",\"default\":true}]'"
check "one v6 default left, on lan0" "[[ \$(in_ns $S4 -6 route show default | wc -l) -eq 1 ]] && in_ns $S4 -6 route show default | grep -q 'via fd77:254::1 dev lan0.* metric 100'"

# 20. noSLAAC (ADR-0012): an IPv4-only attachment gets no RA or autoconf, and keeps link-local.
netns "$U"; S5=$NSPID
check "noSLAAC join" "hook $U create cs5 $S5 '[{\"network\":\"lab4\",\"address\":\"10.254.1.65\",\"mac\":\"02:42:0a:fe:01:65\"}]'"
check "accept_ra is 0 for noSLAAC" "[[ \$(nsenter -t $S5 --net cat /proc/sys/net/ipv6/conf/lan0/accept_ra) == 0 ]]"
check "autoconf is 0 for noSLAAC" "[[ \$(nsenter -t $S5 --net cat /proc/sys/net/ipv6/conf/lan0/autoconf) == 0 ]]"
check "link-local kept for noSLAAC" "in_ns $S5 -6 -o addr show dev lan0 scope link | grep -q 'fe80::'"
check "v4 default via gateway for noSLAAC" "in_ns $S5 -4 route show default | grep -q 'via 10.254.1.78 dev lan0'"
check "stop noSLAAC" "hook $U stop cs5 0 '[{\"network\":\"lab4\",\"address\":\"10.254.1.65\",\"mac\":\"02:42:0a:fe:01:65\"}]'"

# 21. RouteKeep (ADR-0005): a join without default keeps the netns's existing v6 default.
netns "$U"; S6=$NSPID
in_ns $S6 link add d1 type dummy
in_ns $S6 link set d1 up
in_ns $S6 -6 route add default via fd77:255::2 dev d1 metric 100 onlink
check "join without default with a v6 default present" "hook $U create cs6 $S6 '[{\"network\":\"lab6\",\"address\":\"10.254.1.52\",\"mac\":\"02:42:0a:fe:01:52\",\"address6\":\"fd77:254::66\"}]'"
check "one v6 default left, still on d1" "[[ \$(in_ns $S6 -6 route show default | wc -l) -eq 1 ]] && in_ns $S6 -6 route show default | grep -q 'dev d1'"
check "no v6 default via lan0" "! in_ns $S6 -6 route show default | grep -q 'dev lan0'"
check "stop keeps the v6 default" "hook $U stop cs6 0 '[{\"network\":\"lab6\",\"address\":\"10.254.1.52\",\"mac\":\"02:42:0a:fe:01:52\",\"address6\":\"fd77:254::66\"}]' && in_ns $S6 -6 route show default | grep -q 'dev d1'"

echo
echo "hook errors seen:"; sed 's/^/  /' "$WORK/hook.err"
echo "$pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
