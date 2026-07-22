# `nightgauge forge` reference

The forge abstraction. **Every** forge operation in a skill MUST route through
this surface — never a bare `gh`/`glab`. This keeps skills forge-agnostic so
`IB_FORGE=gitlab` works without editing any skill (ADR-008, #3363).

## Command groups

```
nightgauge forge auth        # Token status + config-file token management
nightgauge forge issue       # close | comment | create | edit | list | reopen | view
nightgauge forge pr          # checks | close | comment | create | edit | list | merge | view
nightgauge forge project     # field-get | field-list | field-set | item-add | item-list | item-remove*
nightgauge forge label       # CRUD + add/remove on issues and PRs
nightgauge forge repo        # Repository metadata
nightgauge forge graphql     # Raw GraphQL query/mutation against the active forge
nightgauge forge webhook     # Manage forge webhook receivers
```

`*` `project item-remove` is **not yet supported** — do not depend on it.

## Common examples

```bash
# Auth / identity
nightgauge forge auth status
nightgauge forge repo view --repo "$REPO" --json nameWithOwner -q .nameWithOwner

# Issues
nightgauge forge issue view <number> --repo "$REPO" --json title,body,labels
nightgauge forge issue create --repo "$REPO" --title "…" --body-file body.md
nightgauge forge issue edit <number> --repo "$REPO" --add-assignee @me
nightgauge forge issue comment <number> --repo "$REPO" --body "…"

# PRs
nightgauge forge pr create --repo "$REPO" --base main --head "$BRANCH" --title "…" --body-file b.md
nightgauge forge pr checks <number> --repo "$REPO"
nightgauge forge pr merge <number> --repo "$REPO" --squash

# Project board
nightgauge forge project item-add --repo "$REPO" --project <N> <issue>
nightgauge forge project field-set …
```

## The `graphql` carve-out

Project **view-create / link / list** have no dedicated subcommand — route them
through `nightgauge forge graphql` (ADR-008 carve-out). Flags follow the
`gh api graphql` convention:

```bash
nightgauge forge graphql -f query='mutation { … }' -F number=42
nightgauge forge graphql --query-file ./q.graphql
```

GitLab caveat: the GitLab adapter does **not** yet expose a GraphQL transport —
`forge graphql` against `--forge gitlab` returns `ErrUnsupported`.

## Gotchas

- The `no-direct-gh` lint (`scripts/lint-skills/no-direct-gh.sh`, wired into
  `.github/workflows/lint.yml`) fails CI if a non-allowlisted `skills/*/SKILL.md`
  contains a bare `gh ` call. Legacy exceptions live in
  `scripts/lint-skills/allowlist.txt` — adding to it needs PR review.
- CE-vs-EE GitLab feature differences (scoped labels, iterations, push rules) are
  documented in `docs/FORGE_ABSTRACTION.md#7-ce-vs-ee-feature-matrix-gitlab`.
- Authoritative design: `docs/FORGE_ABSTRACTION.md`; migration table:
  `docs/decisions/008-skill-forge-cli.md`.
