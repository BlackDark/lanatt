# Claims belong to a UID and are live only while their owner process holds the netns

A claim records the UID that made it. A stop hook drops only that UID's claims for the container id, so one allowlisted user cannot free another user's address. ADR-0006 drops a claim whose owner is gone on restart. lanatt also does this before every attach, because the kernel reuses netns inodes: a stale claim would otherwise make a new workload look already joined, and the hook would succeed with no interface.

A claim also records its owner: the attach request's pid, that pid's start time, and the boot id. It is live only if the boot id matches, the owner pid still has the same start time, and the owner is still in the claim's netns. An inode match alone is not enough, because an unrelated workload can land on a reused inode and make a dead claim look live. Claims without an owner, such as those saved before this rule, are dropped on the first start.

State lives under `/run/lanatt` (tmpfs). Attachments do not survive a reboot, so claims must not either; the owner check guards the cases where a reboot is not involved. Each join is saved as soon as it succeeds, so an attachment in a netns always has a claim.

On start, lanatt deletes host macvlans that carry its temporary name, `lt<8 hex><n>`. Such a device exists only if the daemon died between creating it and moving it into the netns. A config with an empty allowlist fails at load, because that daemon would refuse every caller.

## Considered options

- Prune only on restart, as ADR-0006 alone says. Rejected: inode reuse lets a dead workload's claim pass as a live one.
- Inode match only (the earlier rule). Rejected: a new workload on a reused inode keeps a dead claim alive, and the real workload is refused on every start.
- Boot id only. Rejected: it does not catch reuse within one boot.
- Boot id plus any process in the netns. Rejected: an unrelated process in a reused netns still passes.
- Detach by container id alone. Rejected: callers pick the id, so one user could name another's.
- Leave stray temporary devices for the administrator. Rejected: each holds a MAC on the parent and nothing else ever removes it.
