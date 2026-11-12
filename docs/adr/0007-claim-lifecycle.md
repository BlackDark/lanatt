# Claims belong to a UID and are live only while their owner process holds the netns

A claim records the UID that made it. A stop hook drops only that UID's claims for the container id, so one allowlisted user cannot free another user's address. ADR-0006 drops a claim whose owner is gone on restart. lanatt also does this before every attach, because the kernel reuses netns inodes: a stale claim would otherwise make a new workload look already joined, and the hook would succeed with no interface.

A claim also records its owner: the attach request's pid, that pid's start time, and the boot id. It is live only if the boot id matches, the owner pid still has the same start time, and the owner is still in the claim's netns. An inode match alone is not enough, because an unrelated workload can land on a reused inode and make a dead claim look live. Claims without an owner, such as those saved before this rule, are dropped on the first start.

State lives under `/run/lanatt` (tmpfs). Attachments do not survive a reboot, so claims must not either; the owner check guards the cases where a reboot is not involved. Each join is saved as soon as it succeeds, so an attachment in a netns always has a claim.

On start, lanatt deletes host devices that carry its temporary name, `lt<8 hex><n>`. Such a device exists only if the daemon died between creating it and moving it into the netns. A config with an empty allowlist fails at load, because that daemon would refuse every caller.

## Cleanup-pending records

A device whose removal is not verified is not a claim: it is a `pending` record. Each record holds the claim's reservation (netns, network, address, address6, MAC, parent), the attach's token and the device's temporary host name. The token is the device's alias, `lanatt:<token>`, set after creation. A device with that alias is ours. On the host, a device named as the record's temp name (`lt<token><i>`) with no other alias is ours too: the name carries the token, and only the host has that name, since the device is renamed on move.

Transitions, each saved before the next step:

- Intent: a record is saved before the device is created. A refused save creates nothing.
- Settle: one save adds the claim and drops the record. The claim now finds the device.
- Abort (failed attach or stop during one): one save drops the claims and keeps or re-adds the records of the joined devices. If that save fails, nothing is removed: the devices stay up under their records. Otherwise each device is removed by token, in the host namespace and then in its netns, with the kernel work outside the lock. The record goes only once the device is verified absent.
- Retry: a record conflicting with an attach (same netns, or same address, address6 or MAC on its parent) is retried first. A stop retries the records of its container; start retries all.
- Retirement: a record from an earlier boot is dropped without a removal attempt; the reboot took its netns.

A record is verified absent when no device carries its token in the host namespace or in its netns, and the scan finds no visible reference to the netns: no task in it, no fd to it, and no nsfs bind mount in any task's mount namespace. A scan that finds none is taken as destroyed: the devices went with it. A task exiting mid-scan retries the scan, and a scan that keeps racing keeps the record. Owner exit alone proves nothing; the scan does not look at the owner.

Known gap: the kernel can hold a netns with no visible reference (for example TIME_WAIT sockets). Its device, and address, survive until freed, and a conflicting attach in that window can be admitted. Accepted: no check here can see that reference, and waiting on a timer would only move the window.

If a save fails after a device changed, memory keeps the change, and attaches are refused until a save succeeds.

Tests cover: removal failure keeps the record and reservation, retry after a verified removal, restart with a record, earlier-boot retirement, stop with an unverified record, a refused intent save, a degraded save, and settle dropping the record. `test/accept.sh` scenario 12 covers a join that fails after the device moved: its device is removed by token.

## Considered options

- Prune only on restart, as ADR-0006 alone says. Rejected: inode reuse lets a dead workload's claim pass as a live one.
- Inode match only (the earlier rule). Rejected: a new workload on a reused inode keeps a dead claim alive, and the real workload is refused on every start.
- Boot id only. Rejected: it does not catch reuse within one boot.
- Boot id plus any process in the netns. Rejected: an unrelated process in a reused netns still passes.
- Detach by container id alone. Rejected: callers pick the id, so one user could name another's.
- Leave stray temporary devices for the administrator. Rejected: each holds a MAC on the parent and nothing else ever removes it.
