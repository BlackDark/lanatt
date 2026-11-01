# Issue tracker: GitHub Issues

Issues and specs live in GitHub Issues on `BlackDark/lanatt`. Use the `gh` CLI.

## Conventions

- One issue per ticket. A spec is an issue too; its tickets link back to it.
- Triage state is a label (see `triage-labels.md`).
- Conversation goes in issue comments.
- Reference issues from code and docs as `#N`.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## When a skill says "publish to the issue tracker"

`gh issue create -R BlackDark/lanatt -t <title> -F <body.md> -l <label>`.

## When a skill says "fetch the relevant ticket"

`gh issue view <N> -R BlackDark/lanatt --comments`.
