# lanatt

Root daemon that gives a rootless workload its own presence on a LAN. Domain words are in `GLOSSARY.md`. Decisions are in `docs/adr/`.

## Verify

`mise run check` green before every commit.

## Agent skills

### Issue tracker

Local markdown under `.scratch/`. No git remote yet. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical role names, stored as a `Status:` line. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` and `docs/adr/`. See `docs/agents/domain.md`.

## Stack

Follows https://github.com/BlackDark/agent-presets/blob/main/stack/core.md.
Deviations: Go instead of TypeScript. This is a privileged host daemon with no UI; see `docs/adr/0004-go.md`.

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
