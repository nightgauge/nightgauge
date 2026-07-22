---
paths:
  - "claude-plugins/**"
  - "scripts/**"
  - "cmd/**"
  - "internal/**"
  - ".nightgauge/**"
---

# Scripts, Plugins, and Go Binary Rules

## Deterministic Layer Architecture

All deterministic pipeline logic lives in the compiled Go binary
(`cmd/nightgauge/`). Shell scripts in
`claude-plugins/nightgauge/hooks/` are thin 3-line wrappers that `exec` the
Go binary. See [docs/GO_BINARY.md](../../docs/GO_BINARY.md) for the full CLI
reference.

## Issue Creation (ALWAYS Sync to Project Board)

When creating GitHub issues—whether using `/nightgauge:issue-create` skill
or manually via `gh issue create`—you **MUST** add them to the project board.
Use the Go binary's board operations or `gh` CLI for project sync.

**Labels are for classification, not field mapping:**

| Label Type    | Purpose                         |
| ------------- | ------------------------------- |
| `type:*`      | Issue classification (required) |
| `component:*` | Component scope (optional)      |

Priority and Size are project board fields set directly — not derived from
labels.

**Never create issues without syncing to the project board.** Issues not in the
project board are invisible to the team's workflow.

### Multi-Repo Routing (MANDATORY for Epics — #3232)

When creating an epic with sub-issues in a multi-repo workspace, EVERY
`nightgauge project add` call **MUST** pass explicit `--repo` and
`--project` flags derived from `.vscode/nightgauge-workspace.yaml`. The
binary's defaults are a footgun: pre-#3232 they silently dropped cross-repo
issues into the primary project. Defaults are forbidden by skill discipline —
always pass both flags.

For sub-issues whose title or body matches a `routing.patterns[].keywords`
entry, file the issue in the matched `preferred_repo` (`gh issue create
--repo <owner/repo>`), not in the epic's repo. Native GitHub `addSubIssue`
linking **works across repos within the same org** — use it for cross-repo sub-issues
so epic rollups count them, AND include a `Part of <owner>/<repo>#<n>` body
annotation so the relationship is visible in the issue text itself.

After creation, audit each sub-issue's project membership via GraphQL
(`projectItems`) against the routing manifest. Mismatches are fatal — never
report success on a misrouted issue.

The `/nightgauge:issue-create` skill encodes this discipline in Phase
2.4, Phase 3, Phase 4, and Phase 4.8. When creating issues outside that skill,
follow the same five-step contract:

1. Score sub-issue content against `routing.patterns[].keywords`.
2. Map `target_repo` → `target_project` via the workspace yaml's
   `repositories[].project_number` (fallback: `gh project list`).
3. Create with `gh issue create --repo <target>` for cross-repo sub-issues;
   include `Part of <owner>/<repo>#<epic>` in the body.
4. Sync with `nightgauge project add <num> --repo <target> --project
<proj>`.
5. Audit via GraphQL `projectItems` query — assert each issue's actual project
   matches the expected target.

## Epic Sub-Issue Linking (MANDATORY)

When assigning issues to an epic, you **MUST** use GitHub's native sub-issue
linking via GraphQL `addSubIssue` mutation. The extension's tree view uses
GitHub's native parent/sub-issue relationship, not labels.

**Complete epic setup checklist:**

1. Create the epic issue with `type:epic` label
2. Create sub-issues
3. Link each sub-issue to the epic via `addSubIssue` GraphQL mutation:
   ```bash
   gh api graphql -f query='mutation { addSubIssue(input: {
     issueId: "<epic_node_id>", subIssueId: "<sub_issue_node_id>"
   }) { clientMutationId } }'
   ```
4. Add epic AND all sub-issues to the project board via `addProjectV2ItemById`
5. Set project board Status field: **start all issues in "Backlog"** — the
   autonomous scheduler only dispatches "Ready" items, so issues must NOT be
   set to Ready until all relationships are configured (step 6-7).
6. For sequential phases, set up `blockedBy` relationships:

   ```bash
   # Preferred: CLI command (resolves node IDs internally)
   nightgauge issue add-blocked-by <blocked-number> <blocker-number>

   # Fallback: raw GraphQL (use EXACTLY these field names — do not substitute)
   gh api graphql -f query='mutation { addBlockedBy(input: {
     issueId: "<blocked_node_id>",
     blockingIssueId: "<blocker_node_id>"
   }) { clientMutationId } }'
   ```

7. **After all relationships are set**, promote issues from Backlog to Ready:
   ```bash
   nightgauge project sync-status <number> ready
   ```
   This two-step flow (Backlog → relationships → Ready) eliminates the race
   condition where the autonomous scheduler dispatches issues before
   `blockedBy` relationships are applied.

**Get node IDs** (required for mutations):

```bash
gh api graphql -f query='query {
  repository(owner: "OWNER", name: "REPO") {
    issue(number: NUM) { id }
  }
}'
```

**Never skip steps 4-5.** Issues not on the project board with a Status are
invisible to the extension's tree views.

## Determinism Requirements

All code in `internal/` MUST be deterministic:

- Fixed input → fixed output (no AI/LLM calls)
- Predictable, testable, debuggable
- Zero LLM tokens consumed
- Execute in milliseconds, not seconds

Use Go binary for: project board sync, JSON validation, state file updates, hook
evaluation. Use AI skills for: code generation, requirement analysis, PR
descriptions.

## Go Binary Modifications

When modifying the Go binary:

- Add tests for all new functionality (`*_test.go`)
- Run `go test ./...` before committing
- Run `go build ./cmd/nightgauge/` to verify compilation
- Follow existing patterns in `internal/` packages

## Epic Sub-Issue Ordering

When creating sub-issues for epics, follow the wave-based dependency ordering
standard documented in
[CONTRIBUTING.md](../../CONTRIBUTING.md#epic--issue-ordering):

- Every sub-issue must have a `Depends on:` line (use `None` if independent)
- Every sub-issue must declare its wave (`Part of #NNN (Wave N)`)
- Epic bodies must list sub-issues grouped by dependency wave, not arbitrary
  order

## References

- [docs/ARCHITECTURE.md](../../docs/ARCHITECTURE.md) — Architecture overview
- [docs/GO_BINARY.md](../../docs/GO_BINARY.md) — Go binary CLI reference
- [CONTRIBUTING.md](../../CONTRIBUTING.md) — Plugin command guidelines and epic
  ordering standard
