# Using lanatt

One binary, two roles.

## Daemon (root)

```sh
lanatt daemon -config /etc/lanatt/config.json
```

```json
{
  "socket": "/run/lanatt.sock",
  "state": "/var/lib/lanatt/claims.json",
  "uids": [1000],
  "networks": [
    {"name": "lab", "parent": "eno1", "prefix": "192.168.10.0/24", "gateway": "192.168.10.1", "driver": "macvlan"}
  ]
}
```

- `socket` and `state` default to the values above.
- `uids` is the allowlist. UID 0 is refused. An empty list fails at load.
- `driver` must be `macvlan`; the device is created in bridge mode.
- `gateway` must be a host address in `prefix`. lanatt does not create the host shim.
- On start, and before each attach, claims whose netns no longer exists are dropped.
- On start, host macvlans named `lt<8 hex><n>` are deleted. They are lanatt's temporary devices, left behind only if the daemon died between creating one and moving it into the netns. Do not give your own devices names of that form.
- The caller must be the real host UID of the target pid. Stop drops only the caller's own claims for that container id.

The daemon calls `/usr/sbin/ip` and `/usr/bin/nsenter`.

## Hook (caller UID)

Register as OCI hooks:

- `createRuntime`: `lanatt hook create [-socket /run/lanatt.sock]`
- `poststop`: `lanatt hook stop [-socket /run/lanatt.sock]`

The hook reads the OCI state on stdin and the annotation `io.lanatt.attachments` from the bundle's `config.json`. No annotation: exit 0, the daemon is not contacted. A refusal exits 1, so the engine does not start the workload.

```json
[
  {"network": "lab", "address": "192.168.10.50", "mac": "02:42:c0:a8:0a:32"},
  {"network": "iot", "address": "10.20.0.50", "mac": "02:42:0a:14:00:32", "interface": "iot", "default": true}
]
```

- `interface` defaults to `lan0`, `lan1`, … in list order.
- `default: true` replaces the netns default route with one via the network gateway. Without it, a default route is added only when the netns has none.
- The same join again is success. A different address or MAC for the same network and netns, or an address or MAC another netns holds on that network, is refused.

## Acceptance

`test/accept.sh` drives the daemon through the hook against a dummy parent and `10.254.1.0/28`. Root on a test VM only:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/lanatt ./cmd/lanatt
scp /tmp/lanatt test/accept.sh root@:/tmp/
ssh root@ bash /tmp/accept.sh /tmp/lanatt
```
