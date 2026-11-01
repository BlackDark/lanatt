# Claims belong to a UID and are checked against live netns on every attach

A claim records the UID that made it. A stop hook drops only that UID's claims for the container id, so one allowlisted user cannot free another user's address. ADR-0006 drops a claim whose netns is gone on restart. lanatt also does this before every attach, because the kernel reuses netns inodes: a stale claim would otherwise make a new workload look already joined, and the hook would succeed with no interface. Each join is saved as soon as it succeeds, so an attachment in a netns always has a claim.

On start, lanatt deletes host macvlans that carry its temporary name, `lt<8 hex><n>`. Such a device exists only if the daemon died between creating it and moving it into the netns. A config with an empty allowlist fails at load, because that daemon would refuse every caller.

## Considered options

- Prune only on restart, as ADR-0006 alone says. Rejected: inode reuse lets a dead workload's claim pass as a live one.
- Detach by container id alone. Rejected: callers pick the id, so one user could name another's.
- Leave stray temporary devices for the administrator. Rejected: each holds a MAC on the parent and nothing else ever removes it.
