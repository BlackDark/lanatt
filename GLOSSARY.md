# lanatt

lanatt gives a rootless workload its own presence on a LAN.

## Language

**Network**:
A named LAN an administrator offers to callers. It has a prefix and a gateway. Callers join it. They do not define it.
_Avoid_: subnet, VLAN

**Attachment**:
One workload netns joined to one network, with one IPv4 host address in that prefix and, on macvlan, one MAC address. The same join with the same address is the same attachment. A different address, or an address or MAC another attachment on that network already has, is refused. ipvlan attachments have no MAC.
_Avoid_: IP, request, interface

**Workload netns**:
The network namespace an attachment is placed into. The join is requested by the container that owns that netns. Containers that share it, as in a pod, do not request their own.
_Avoid_: pod, container
