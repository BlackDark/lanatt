# A durable journal owes every CNI ADD a DEL

Each CNI attachment has a root-owned record, written before ADD runs and updated when ADD succeeds. A crash, timeout or malformed result therefore leaves a record that says a DEL is owed, and startup reconciliation pays it from the stored config and result. The record is removed only after DEL succeeds. The journal is independent of the claims: claims answer "is this address taken", the journal answers "what must be undone".

The CNI container id is derived from the UID and the caller's container id. Hashing keeps it in the permitted charset and stops one UID from naming another's attachment. Every store operation takes the UID, so a caller cannot read or release another UID's record. Each attachment has its own lock, held across the whole Attach, Release or Reconcile step, including ADD and DEL. A store-wide lock guards the record files. Records and the journal directory are synced on every change, and the journal must be owned by the daemon and not writable by others. The check does not cover the directory above the journal, so it must sit on a root-owned path.

A repeat ADD for a container with a record is refused, so a plugin never sees two ADDs for one id. A failed commit after a successful ADD is rolled back at once with DEL; if that DEL also fails, the record stays owed and Reconcile finishes it. If the journal cannot be written at all, the record stays `intent` and Reconcile pays the DEL. Only when the commit write lands and the follow-up write also fails can a record stay `added` while its netns is up; Reconcile keeps it, and an operator must act.

## Considered options

- Prune records by netns liveness, as claims do. Rejected: pruning before DEL leaks host-local allocations.
- Keep no record and rely on the plugin's DEL. Rejected: a crash between ADD and any save leaves nothing to retry.
- Run DEL only on stop. Rejected: a crash on the daemon's own startup would never be cleaned.
