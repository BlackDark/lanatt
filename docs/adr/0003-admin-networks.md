# Networks are admin-defined

A caller joins a named network. The administrator defines that network: which LAN, its prefix, and its gateway. The caller supplies the host address and the MAC. v1 networks are macvlan in bridge mode, so multicast still reaches the attachment. Amended by ADR-0011: ipvlan (L2) networks also exist, and take no MAC. The driver is a property of the network, so a later driver does not become a new kind of object.

Two networks on one parent cannot have overlapping `prefix`: claims are checked per network, so a clash would go unseen. Refused at load. The IPv6 rule is in ADR-0012.

## Considered options

- Each request names the parent NIC, the prefix, and the gateway. Rejected: a caller could then place an attachment on any NIC the host has.
