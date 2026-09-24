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
nightgauge git repo-slug                                   # current repo as owner/name
nightgauge forge repo view --repo "$REPO" --json | jq -r .owner   # --repo is required

# Issues
nightgauge forge issue view <number> --repo "$REPO" --json
nightgauge forge issue view <number> --repo "$REPO" --json | jq -r .nodeId
nightgauge forge issue create --repo-id <REPO_NODE_ID> --title "…" --body "$(cat body.md)" \
  --labels type:bug,priority:high
nightgauge forge issue edit --node-id <NODE_ID> --body "Updated body"
nightgauge forge issue comment --subject-id <NODE_ID> --body "…"

# PRs
nightgauge forge pr create --repo-id <REPO_NODE_ID> --base main --head "$BRANCH" --title "…" \
  --body "$(cat b.md)"
nightgauge forge pr checks <number> --repo "$REPO"
nightgauge forge pr merge --node-id <PR_NODE_ID> --strategy squash

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

- There is no `-q`/`--jq` on any `forge` verb, and no `--body-file`: pipe
  `--json` output to `jq -r '<expr>'`, and pass a file as `--body "$(cat f)"`.
- The `no-direct-gh` lint (`scripts/lint-skills/no-direct-gh.sh`, wired into
  `.github/workflows/lint.yml`) fails CI if a non-allowlisted `skills/*/SKILL.md`
  contains a bare `gh ` call. Legacy exceptions live in
  `scripts/lint-skills/allowlist.txt` — adding to it needs PR review.
- CE-vs-EE GitLab feature differences (scoped labels, iterations, push rules) are
  documented in `docs/FORGE_ABSTRACTION.md#7-ce-vs-ee-feature-matrix-gitlab`.
- Authoritative design: `docs/FORGE_ABSTRACTION.md`; migration table:
  `docs/decisions/008-skill-forge-cli.md`.
