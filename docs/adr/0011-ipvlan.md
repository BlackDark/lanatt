# ipvlan networks in L2 mode

A network may use the `ipvlan` driver in L2 mode as well as `macvlan`. ipvlan attachments take no MAC: they share the parent's link address, so the caller's `mac` must be empty and the claims do not check MACs for them.

The use case is a switch or Wi-Fi network that limits MACs per port, so macvlan attachments are dropped. The cost is that all attachments on a parent share one MAC, so a switch or DHCP server cannot tell them apart by MAC.

## Decisions

- Driver is a property of the network (ADR-0003). `macvlan` and `ipvlan` are both accepted; any other driver is refused at load.
- ipvlan in L2 mode, not L3 or L3S. L2 keeps ARP and broadcast working on the LAN, as macvlan does. L3 needs routes on the parent and ignores broadcast.
- A `mac` on an ipvlan network is refused. A MAC has no effect there, and a caller that sends one probably expects it to be applied.
- A parent carries one driver. The kernel allows one rx handler per device: `ip link add ... type ipvlan` on a parent that has macvlan uppers fails with `EBUSY`. The config refuses such a mix at load, so the failure never reaches a join.
- Temporary devices (`lt<8 hex><n>`) are reaped for both drivers.

## Verified

On a Debian test VM, dummy parent, root:

- ipvlan join without MAC: admitted. Two netns on the same parent: both join.
- ipvlan join with a MAC: refused.
- Two ipvlan slaves on one parent ping each other (ARP resolves, 0% loss).
- Host to slave and slave to host: 100% loss. The host cannot reach its ipvlan attachments, as with macvlan. Issue #21 asked whether it could; the answer is no.
- Multicast from a slave to the parent's host: not received. Slave-to-slave multicast: not tested.
- macvlan and ipvlan on one parent: `EBUSY` from the kernel, refused by config.

Not verified: a real NIC parent, and reachability from another LAN host. Before relying on ipvlan on a LAN, test from a workstation on a sanctioned address.

## Considered options

- Let the caller send a MAC and ignore it. Rejected: silent.
- Allow L3 mode. Rejected: needs routing on the parent and breaks broadcast.
- Share a parent between drivers by picking the driver per join. Rejected: the kernel does not allow it.
