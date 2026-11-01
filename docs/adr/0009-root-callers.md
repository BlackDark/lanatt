# Root callers are opt-in and not bound to their own pids

This amends ADR-0002. A rootful container's hook runs as host UID 0. With `"allowRoot": true` in the config, the daemon accepts UID 0 callers, and a root caller may join any pid's netns, not only its own. Root can already create a macvlan anywhere, so trusting root adds no privilege on the host. This also covers Podman `--userns=auto`, where the container's processes run as a high host UID while the hook still runs as root.

Every other rule still applies to root: the host netns is refused, and the address, MAC and interface checks are the same. Claims made by root record UID 0, so only root can release them. `uids` still refuses `0`, so the only way to trust root is the explicit flag.

## Considered options

- Leave rootful containers to the engine's own macvlan. Rejected: Docker's built-in macvlan IPAM cannot see lanatt's claims, so mixing the two can hand out the same address twice. With lanatt for both, one annotation and one config cover rootful and rootless containers.
- Allow `0` in `uids`. Rejected: it would read like any other UID, but root skips the owner check, so the setting should say so.
- Hold root to the owner check. Rejected: a rootful container's processes can run as non-root host UIDs (`--userns=auto`, `--user`), so the check would refuse ordinary rootful containers.
