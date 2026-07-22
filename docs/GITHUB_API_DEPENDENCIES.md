# GitHub API Dependencies

This document records which GitHub GraphQL API features Nightgauge depends
on, their availability status, and the risk level if they are deprecated or
renamed.

> **Automated validation**: `internal/github/schema_validation_test.go` asserts
> that all fields listed here are present in our query structs with the correct
> names and pagination arguments. Update both files together when the API
> changes.

---

## Summary Table

| Feature           | Type     | GitHub Availability | Risk if Removed | Used In                                    |
| ----------------- | -------- | ------------------- | --------------- | ------------------------------------------ |
| `subIssues`       | Query    | GA (2024)           | Critical        | Issue board, epic tracking, cross-repo     |
| `blockedBy`       | Query    | GA (2024)           | Critical        | Board lock icons, dependency ordering      |
| `blocking`        | Query    | GA (2024)           | Critical        | Board lock icons, dependency ordering      |
| `addSubIssue`     | Mutation | GA (2024)           | High            | Sub-issue linking (`scripts/`)             |
| `removeSubIssue`  | Mutation | GA (2024)           | High            | Sub-issue unlinking (`scripts/`)           |
| `addBlockedBy`    | Mutation | GA (2024)           | High            | Blocking relationship setup (`scripts/`)   |
| `removeBlockedBy` | Mutation | GA (2024)           | High            | Blocking relationship teardown             |
| `ProjectV2`       | Query    | GA (2023)           | Critical        | Board queries (all project board features) |
| `ProjectV2 Views` | REST API | GA (2026)           | Medium          | repo-init standard view creation           |
| `node(id:)`       | Query    | Stable              | High            | Cross-repo epic progress lookups           |

---

## Feature Details

### `subIssues` — Hierarchical Issue Relationships

**GraphQL path**: `Issue.subIssues(first: N).nodes[]`

**What it does**: Returns the list of child issues linked as sub-issues of a
parent issue. This is GitHub's native parent/child issue hierarchy (distinct
from task list checkboxes in the issue body).

**Where we use it**:

- `issueQuery` — fetching full issue detail with sub-issues
- `projectItemContent` — board item display in the Ready Items tree view
- `nodeQuery` — cross-repo epic progress (`GetEpicProgress`)

**Pagination**: `first: 50` — supports epics with up to 50 sub-issues. Increase
if epic size limits grow.

**Risk**: Critical. Removing `subIssues` breaks epic tracking entirely. The
board would show epics with no sub-issues and 0% progress.

---

### `blockedBy` — Issues Blocked By Another Issue

**GraphQL path**: `Issue.blockedBy(first: N).nodes[]`

**What it does**: Returns the list of issues that block this issue. Used to
determine whether an issue is currently blocked (any open blocker = blocked).

**Where we use it**:

- `issueQuery` — fetching blocking state for a specific issue
- `projectItemContent` — board item display (determines 🔒 lock icon)

**Pagination**: `first: 10` — assumes issues have at most 10 blockers.

**Risk**: Critical. Without `blockedBy`, lock icons disappear and the pipeline
cannot enforce sequential epic ordering based on blocking relationships.

---

### `blocking` — Issues This Issue Blocks

**GraphQL path**: `Issue.blocking(first: N).nodes[]`

**What it does**: Returns the list of issues that this issue is currently
blocking. Used for informational display on board items.

**Where we use it**:

- `issueQuery` — fetching outgoing blocking relationships
- `projectItemContent` — board item display

**Pagination**: `first: 10`

**Risk**: Critical (same as `blockedBy`).

---

### `addSubIssue` / `removeSubIssue` — Sub-Issue Link Mutations

**GraphQL mutations**: `addSubIssue(input: $input)`,
`removeSubIssue(input: $input)`

**What they do**: Link/unlink an issue as a child of another issue.

**Where we use them**: Setup scripts (`scripts/`) for creating epic structures;
`internal/github/issues.go` `AddSubIssue`/`RemoveSubIssue`.

**Risk**: High. Scripts that set up new epics would need REST API workarounds if
these mutations are removed.

---

### `addBlockedBy` / `removeBlockedBy` — Blocking Relationship Mutations

**GraphQL mutations**: `addBlockedBy(input: $input)`,
`removeBlockedBy(input: $input)`

**What they do**: Create/remove a blocking relationship between two issues.
`addBlockedBy(issueId: X, blockingIssueId: Y)` makes Y block X.

**Where we use them**: Setup scripts for sequential epic ordering;
`internal/github/issues.go` `AddBlockedBy`/`RemoveBlockedBy`.

**Input structure**:

```graphql
input AddBlockedByInput {
  issueId: ID! # the issue that gets blocked
  blockingIssueId: ID! # the issue that does the blocking
  clientMutationId: String
}
```

**Risk**: High. Sequential epic dependency enforcement breaks without these.

---

### `ProjectV2` — Projects v2 Board

**GraphQL path**:
`organization(login:).projectV2(number:).items(first:,after:,query:)`

**What it does**: Fetches project board items with optional server-side
filtering by status (`query: "status:Ready is:open"`).

**Where we use it**: `projectV2Query`, `projectV2FilteredQuery` — the board
service that powers all tree views in the VSCode sidebar.

**Risk**: Critical. This is the entire board data source.

---

### `ProjectV2 Views` — Project Board View Management

**REST API path**:
`POST /orgs/{org}/projectsV2/{project_number}/views` (org-owned) or
`POST /users/{user_id}/projectsV2/{project_number}/views` (user-owned)

**Required header**: `X-GitHub-Api-Version: 2026-03-10`

**What it does**: Creates project board views with specified layout (`board`,
`table`, `roadmap`), optional filter, and visible fields.

**Where we use it**: `repo-init` skill (Phase 5.2) — creates standard views
(Backlog, Priority board, Team items, Roadmap, My items) during repository
onboarding.

**Limitations**: The REST API supports **create only** — no list, update, or
delete endpoints. Use GraphQL `projectV2.views` query to list existing views
for idempotency checks.

**Risk**: Medium. Views are created once during repo-init. If the API changes,
existing views are unaffected — only new repo onboarding would break.

---

### `node(id:)` — Cross-Repository Lookups

**GraphQL path**: `node(id: $id) { ... on Issue { ... } }`

**What it does**: Looks up any GitHub object by its global node ID. Used for
cross-repo epic progress where we have node IDs but not owner/repo/number.

**Where we use it**: `nodeQuery` — `GetEpicProgress` for multi-repo workspaces.

**Risk**: High. Cross-repo epic tracking would require fallback to REST API.

---

## Deprecation Monitoring

### How to respond to a GitHub API deprecation notice

1. Add the deprecated field name to `deprecatedFields` in
   `internal/github/schema_validation_test.go`. The `TestNoDeprecatedFieldsUsed`
   test will then fail on every CI run, making the deprecation impossible to
   ignore.
2. Update the affected struct tags in `internal/github/types.go`.
3. Update this document — move the feature entry to the **Removed / Replaced**
   section below and add the migration date.
4. Update the test's `wantTag` value in `TestPaginationArguments` and
   `TestMutationDependencies` if argument names changed.

### Removed / Replaced features

_None as of 2026-03._

---

## Schema Introspection

To verify the current GitHub API schema for any of these fields, run:

```bash
# Check if subIssues exists on Issue type
gh api graphql -f query='
  query {
    __type(name: "Issue") {
      fields { name isDeprecated deprecationReason }
    }
  }
' | jq '.data.__type.fields[] | select(.name == "subIssues")'

# Check blockedBy / blocking
gh api graphql -f query='
  query {
    __type(name: "Issue") {
      fields { name isDeprecated deprecationReason }
    }
  }
' | jq '.data.__type.fields[] | select(.name | test("block"; "i"))'

# Check addBlockedBy mutation exists
gh api graphql -f query='
  query {
    __type(name: "Mutation") {
      fields { name isDeprecated deprecationReason }
    }
  }
' | jq '.data.__type.fields[] | select(.name | test("BlockedBy|SubIssue"; ""))'
```

Expected output for healthy dependencies: `isDeprecated: false` on all returned
fields.

---

---

## Token Scope Requirements

Different GitHub API operations require different OAuth token scopes. Use this
table to ensure your Personal Access Token (PAT) includes the required scopes.

### Scope Requirements by Operation

| Operation                  | Required Scope | Notes                                          |
| -------------------------- | -------------- | ---------------------------------------------- |
| Fetch issues and PRs       | `repo`         | Read repository content                        |
| Create/update issues       | `repo`         | Write to repository                            |
| Manage project board items | `project`      | Read/write GitHub Projects v2                  |
| Query project board views  | `project`      | List views, filter by status                   |
| Read org memberships       | `read:org`     | Query user org memberships (multi-org routing) |
| Push commits               | `repo`         | Write access to repository                     |
| Create/merge pull requests | `repo`         | Read/write PRs                                 |
| Manage releases            | `repo`         | Create/update releases                         |
| Add blocking relationships | `repo`         | Create `blockedBy` / `blocking` links          |
| Cross-repo node lookups    | `repo`         | `node(id:)` query across repositories          |

### Recommended Minimum Scopes for Pipeline Operations

**Classic PAT** (Settings → Developer settings → Personal access tokens → Classic):

- `repo` — Full control of private/public repos (issues, PRs, code, releases)
- `project` — Read/write GitHub Projects v2 boards
- `read:org` — Query org membership (required for multi-org token routing)

**Fine-grained PAT** (Settings → Developer settings → Personal access tokens → Fine-grained):

- Repository permissions:
  - `Contents` — Read/Write
  - `Issues` — Read/Write
  - `Pull requests` — Read/Write
  - `Metadata` — Read (required by all fine-grained PATs)
- Organization permissions:
  - `GitHub Projects` — Read/Write
  - `Members` — Read (for org membership queries)

> **Fine-grained PATs do not return `X-OAuth-Scopes` headers.** The pipeline's
> `ValidateTokenScopes()` check silently skips scope validation for fine-grained
> PATs. Ensure permissions are set correctly at token creation time.

### Token Configuration

Configure tokens in `.nightgauge/config.yaml` using the `github_auth`
section. See [CONFIGURATION.md § github_auth](CONFIGURATION.md#github_auth) for
single-org and multi-org setup guides.

For CI/CD environments, see [CI/CD Runbook](./CI_CD_RUNBOOK.md).

---

## References

- `internal/github/types.go` — all query/mutation struct definitions
- `internal/github/schema_validation_test.go` — automated schema validation
- `internal/github/issues.go` — `GetIssue`, `AddSubIssue`, `AddBlockedBy`
- `internal/github/board.go` — `fetchAllItemsInternal` (board data)
- `internal/github/client.go` — `ValidateTokenScopes`, `NewClientFromConfig`
- `pkg/types/types.go` — `SubIssueRef`, `BlockingRef` data shapes
- [GitHub GraphQL API Explorer](https://docs.github.com/en/graphql/overview/explorer)
- [GitHub Projects V2 documentation](https://docs.github.com/en/graphql/reference/objects#projectv2)
- [GitHub PAT Scopes Reference](https://docs.github.com/en/developers/apps/building-oauth-apps/scopes-for-oauth-apps)
