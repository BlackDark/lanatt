# The join is an annotation on the netns owner

The `createRuntime` hook reads an OCI annotation from the container that owns the workload netns and sends that, plus the netns, to the daemon. The annotation lists the networks to join, the host address, the MAC, an optional interface name, and whether this attachment takes the default route. Missing interface names are `lan0`, `lan1`, and so on. The daemon sets the address and the routes after the move. The owning host uid cannot enter the netns, so the hook cannot do that itself. Network definitions, including the gateway, stay in root-owned config. The administrator provides that gateway. lanatt does not create the host shim. A shim is a macvlan on the same parent: an attachment can reach that address, and it cannot reach an address configured on the parent itself.

A second hook on stop drops the daemon's claim. The interface itself dies with the netns. The claim is stored so a daemon restart still knows which addresses are taken, and a claim whose owner is gone is dropped (ADR-0007).

## Considered options

- A root-owned file keyed by container name. Rejected: the engine assigns those names.
- The workload process calling the daemon after it has started. Rejected: that is later than the hook, so the process can come up before the attachment exists.
- lanatt creating the host shim for each network. Rejected: the shim must exist before any workload starts and must survive a reboot, which is host network configuration.

The shim is admin-owned host configuration. `examples/shim/` ships a systemd unit that creates it at boot.
