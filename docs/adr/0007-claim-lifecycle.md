# Claims belong to a UID and are live only while their owner process holds the netns

A claim records the UID that made it. A stop hook drops only that UID's claims for the container id, so one allowlisted user cannot free another user's address. ADR-0006 drops a claim whose owner is gone on restart. lanatt also does this before every attach, because the kernel reuses netns inodes: a stale claim would otherwise make a new workload look already joined, and the hook would succeed with no interface.

A claim also records its owner: the attach request's pid, that pid's start time, and the boot id. It is live only if the boot id matches, the owner pid still has the same start time, and the owner is still in the claim's netns. An inode match alone is not enough, because an unrelated workload can land on a reused inode and make a dead claim look live. Claims without an owner, such as those saved before this rule, are dropped on the first start.

State lives under `/run/lanatt` (tmpfs). Attachments do not survive a reboot, so claims must not either. The owner check handles exits and PID reuse within a boot.

## Cleanup-pending records

Each device has a pending record before creation. The record reserves its netns, network addresses and MAC on the recorded parent. It also stores its temporary host name and a random per-device token. The daemon sets the link alias to `lanatt:<token>` before moving the device into the workload namespace. An unmarked link at the temporary name is ambiguous: the daemon may have crashed before setting its alias, or creation may have failed because the name was already taken. The record stays pending in that case. Startup no longer deletes links based on the temporary naming pattern.

- Intent: save each record before creating its device. A failed save refuses creation.
- Complete: after every join succeeds, one save publishes all new claims and drops their pending records. A restart before this save finds pending records without completed claims for the incomplete attach.
- Abort: after a join or completion fails, restore any changed default routes and attempt deletion in reverse join order using the saved intents. Return the original failure along with any cleanup or save error.
- Retry: startup retries all pending records, stop retries records for its container, and attach retries conflicting records before admitting a request. An active attach owns its pending records until it finishes.
- Retirement: drop the record after deleting the marked link or verifying its absence in the host and target namespaces. Drop earlier-boot records because their devices cannot survive a reboot.

Recovery searches for the target namespace through tasks, open descriptors and nsfs bind mounts. If inspection cannot reach the namespace and the host check did not find and delete the marked device, the record keeps its reservation. A search can miss live references, including references held only by the kernel. An owner exit cannot verify device removal.

If saving the removal of a pending record fails, the last durable state still contains the intent. The daemon refuses further attaches until it saves its updated state. See the [operator recovery procedure](../usage.md#recovering-pending-cleanup) for records that cannot be cleared automatically.

## Attach timing

Conflicting cleanup runs during admission, before the join budget starts. The socket deadline limits the client's wait. It does not cancel daemon work, so admission can continue retrying cleanup after the client times out. The Linux daemon tests exercise this through a real socket with two conflicting records.

## Considered options

- Prune only on restart, as ADR-0006 alone says. Rejected: inode reuse lets a dead workload's claim pass as a live one.
- Inode match only (the earlier rule). Rejected: a new workload on a reused inode keeps a dead claim alive, and the real workload is refused on every start.
- Boot id only. Rejected: it does not catch reuse within one boot.
- Boot id plus any process in the netns. Rejected: an unrelated process in a reused netns still passes.
- Detach by container id alone. Rejected: callers pick the id, so one user could name another's.
- Delete every host link that matches the temporary naming pattern. Rejected: the name alone cannot establish ownership.
