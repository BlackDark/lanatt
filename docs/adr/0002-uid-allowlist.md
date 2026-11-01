# Callers are an allowlist, and the netns must be theirs

The daemon can put a new host on the physical LAN. It accepts a request only from a UID on an allowlist, and only when that UID owns the target workload netns.

## Considered options

- Any local user who can reach the daemon. Rejected: any account could take an address on the LAN.
- A per-UID list of allowed addresses and MACs. Deferred: one admin on one host does not need it yet.
