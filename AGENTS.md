# lanatt

Root daemon that gives a rootless workload its own presence on a LAN. Domain words are in `GLOSSARY.md`. Decisions are in `docs/adr/`.

## Verify

`mise run check` green before every commit.

## Agent skills

### Issue tracker

GitHub Issues on `BlackDark/lanatt`. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical role names, as issue labels. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` and `docs/adr/`. See `docs/agents/domain.md`.

## Stack

Follows https://github.com/BlackDark/agent-presets/blob/main/stack/core.md.
Deviations:
- Go instead of TypeScript. This is a privileged host daemon with no UI; see `docs/adr/0004-go.md`.
- OCI image although the daemon manages the host: it runs with `--network host --pid host --privileged`. The runtime is `debian:trixie-slim`, not distroless, because the daemon runs `ip` and `nsenter`.
- No version file: release-please keeps the version in `.release-please-manifest.json`. Merging its release PR tags `vX.Y.Z`, then `release.yml` publishes the image.
- One runner builds both image targets: Go cross-compiles natively, and only the final `apt-get` runs under QEMU. No native arm64 runner.

## Map

- `cmd/lanatt`: one binary, `daemon` (root) and `hook create|stop` (caller UID).
- `internal/claims`: the attachment rules, pure. `internal/daemon`: socket, peer credentials, `ip`/`nsenter`.
- `test/accept.sh`: kernel acceptance on a dummy parent. `test/engines-e2e.sh`: rootless Podman, Quadlet and Docker on the real LAN, using `examples/`.
- `internal/runtime`: `lanatt-runc`, the Docker runtime wrapper that adds the hooks.

## Gotchas

- `.private/` (gitignored) holds machine- and network-specific notes: the test VM, its LAN, and the addresses free for tests. Read `.private/README.md` when it exists. Never commit its contents or host names from it.
- `go test` cannot create macvlans. Kernel and engine behaviour is proven only as root on a Debian test VM (`$VM`). Build on the workstation and copy the binary or image over.
- `test/engines-e2e.sh` uses the real LAN: only addresses the user has handed out. Everything else uses the dummy parent and `10.254.1.0/28`.
- The host cannot reach its own macvlan attachments. Test reachability from another LAN host.
- Podman's default `hooks_dir` is only `/usr/share/containers/oci/hooks.d`. A hook in `/etc/containers/oci/hooks.d` is ignored until the `examples/podman/containers.conf.d` drop-in is installed.

## Every change

Same PR, only what the change touches:
- tests for the changed behaviour
- docs in `docs/` that describe it
- README when a user-visible feature changes

## Code style

- Data in, data out: pure functions, side effects (I/O, time, randomness) at the edges and passed in.
- Expected failures are values (`error` in Go); panic for bugs only.
- Composition over inheritance: no class hierarchies. Go methods are fine.
- Small units: split a file past ~400 lines, a function past ~60.
- Names explain what, comments explain why.
- Refactors keep behaviour and land in their own commit.

## Stewardship

You own the codebase's health, not only the task: leave every touched area cleaner than you found it.
- Small fixes beside the change (dead code, stale comment or doc, missing test, unclear name): fix them in their own commit in the same PR.
- Whatever your change makes obsolete (code, flags, config, docs, deps) goes in the same PR.
- Larger problems you notice: report them to the user with `file:line` and a proposal; the PR stays on topic.

## Git

- Conventional Commits. One topic per PR.
- Merge: rebase when the commits are logically separate, squash otherwise.
- PR already merged → branch anew from `origin/main`, open a new PR.
- Force-push: `--force-with-lease` on own branches only.

## Boundaries

- `sudo`, system packages, global installs: the user runs them; ask with what and why.
- Running user processes stay up; test on other ports.
- Language: code, comments, docs, commits in English.
