# GitHub GraphQL API Schema Reference

> **Source of truth** for all GitHub GraphQL API usage in Nightgauge.
> Introspected from the live GitHub API on 2026-03-11.
>
> **Rule**: Before writing any GraphQL mutation or query — in Go, TypeScript, or
> scripts — check this document first. Never guess type names, field names, or
> input shapes. If a type is not listed here, introspect it:
>
> ```bash
> gh api graphql -f query='{ __type(name: "TypeName") { name inputFields { name type { name kind ofType { name } } } } }'
> ```
>
> **Go `shurcooL/graphql` library rule**: The Go struct name for input types
> MUST exactly match the GitHub GraphQL input type name. The library derives the
> GraphQL type name from the Go struct name. For example:
> `AddProjectV2ItemByIdInput` (correct) vs `addProjectItemInput` (WRONG —
> would send `AddProjectItemInput` which doesn't exist).

---

## Table of Contents

- [Mutations We Use](#mutations-we-use)
- [Mutation Input Types](#mutation-input-types)
- [Available Mutations (Not Yet Used)](#available-mutations-not-yet-used)
- [Key Object Types](#key-object-types)
- [Enum Types](#enum-types)
- [Query Patterns](#query-patterns)
- [Transport Classification — GraphQL vs REST](#transport-classification--graphql-vs-rest)
- [Go Struct ↔ GraphQL Type Mapping](#go-struct--graphql-type-mapping)
- [Validation](#validation)

---

## Mutations We Use

| Mutation                        | Input Type                           | Go Function         | File         |
| ------------------------------- | ------------------------------------ | ------------------- | ------------ |
| `addProjectV2ItemById`          | `AddProjectV2ItemByIdInput`          | `AddItem()`         | `project.go` |
| `updateProjectV2ItemFieldValue` | `UpdateProjectV2ItemFieldValueInput` | `updateField()`     | `project.go` |
| `createIssue`                   | `CreateIssueInput`                   | `CreateIssue()`     | `issues.go`  |
| `closeIssue`                    | `CloseIssueInput`                    | `CloseIssue()`      | `issues.go`  |
| `addLabelsToLabelable`          | `AddLabelsToLabelableInput`          | `AddLabels()`       | `issues.go`  |
| `removeLabelsFromLabelable`     | `RemoveLabelsFromLabelableInput`     | `RemoveLabels()`    | `issues.go`  |
| `addSubIssue`                   | `AddSubIssueInput`                   | `AddSubIssue()`     | `issues.go`  |
| `removeSubIssue`                | `RemoveSubIssueInput`                | `RemoveSubIssue()`  | `issues.go`  |
| `addBlockedBy`                  | `AddBlockedByInput`                  | `AddBlockedBy()`    | `issues.go`  |
| `removeBlockedBy`               | `RemoveBlockedByInput`               | `RemoveBlockedBy()` | `issues.go`  |
| `createPullRequest`             | `CreatePullRequestInput`             | `CreatePR()`        | `prs.go`     |
| `mergePullRequest`              | `MergePullRequestInput`              | `MergePR()`         | `prs.go`     |
| `deleteRef`                     | `DeleteRefInput`                     | `DeleteRef()`       | `prs.go`     |

---

## Mutation Input Types

### AddProjectV2ItemByIdInput

```graphql
input AddProjectV2ItemByIdInput {
  clientMutationId: String
  projectId: ID! # ProjectV2 node ID
  contentId: ID! # Issue or PR node ID
}
```

### UpdateProjectV2ItemFieldValueInput

```graphql
input UpdateProjectV2ItemFieldValueInput {
  clientMutationId: String
  projectId: ID! # ProjectV2 node ID
  itemId: ID! # ProjectV2Item node ID
  fieldId: ID! # Field node ID (from project fields query)
  value: ProjectV2FieldValue!
}
```

### ProjectV2FieldValue

```graphql
input ProjectV2FieldValue {
  text: String # For TEXT fields
  number: Float # For NUMBER fields
  date: Date # For DATE fields (YYYY-MM-DD)
  singleSelectOptionId: String # For SINGLE_SELECT (Status, Priority, Size)
  iterationId: String # For ITERATION fields
}
```

### CreateIssueInput

```graphql
input CreateIssueInput {
  clientMutationId: String
  repositoryId: ID! # Repository node ID
  title: String!
  body: String
  assigneeIds: [ID!]
  milestoneId: ID
  labelIds: [ID!]
  projectIds: [ID!] # Legacy projects (deprecated)
  projectV2Ids: [ID!] # ProjectV2 boards — adds issue to board on creation
  issueTemplate: String
  issueTypeId: ID # Issue type (if repo uses issue types)
  parentIssueId: ID # Parent issue — creates sub-issue relationship on creation
  agentAssignment: AgentAssignmentInput # Copilot agent assignment
}
```

### CloseIssueInput

```graphql
input CloseIssueInput {
  clientMutationId: String
  issueId: ID!
  stateReason: IssueClosedStateReason # COMPLETED | NOT_PLANNED | DUPLICATE
  duplicateIssueId: ID # Required when stateReason is DUPLICATE
}
```

### AddLabelsToLabelableInput

```graphql
input AddLabelsToLabelableInput {
  clientMutationId: String
  labelableId: ID! # Issue or PR node ID
  labelIds: [ID!]! # Label node IDs (not label names)
}
```

### RemoveLabelsFromLabelableInput

```graphql
input RemoveLabelsFromLabelableInput {
  clientMutationId: String
  labelableId: ID!
  labelIds: [ID!]!
}
```

### AddSubIssueInput

```graphql
input AddSubIssueInput {
  clientMutationId: String
  issueId: ID! # Parent issue node ID
  subIssueId: ID # Child issue node ID (use this OR subIssueUrl)
  subIssueUrl: String # Child issue URL (alternative to subIssueId)
  replaceParent: Boolean # If true, moves sub-issue from current parent
}
```

### RemoveSubIssueInput

```graphql
input RemoveSubIssueInput {
  clientMutationId: String
  issueId: ID! # Parent issue node ID
  subIssueId: ID! # Child issue node ID to remove
}
```

### AddBlockedByInput

```graphql
input AddBlockedByInput {
  clientMutationId: String
  issueId: ID! # The issue that IS blocked
  blockingIssueId: ID! # The issue that BLOCKS it
}
```

### RemoveBlockedByInput

```graphql
input RemoveBlockedByInput {
  clientMutationId: String
  issueId: ID! # The issue that was blocked
  blockingIssueId: ID! # The issue that was blocking it
}
```

### CreatePullRequestInput

```graphql
input CreatePullRequestInput {
  clientMutationId: String
  repositoryId: ID!
  baseRefName: String! # Target branch (e.g., "main")
  headRefName: String! # Source branch (e.g., "feat/my-feature")
  headRepositoryId: ID # For cross-repo PRs
  title: String!
  body: String
  maintainerCanModify: Boolean
  draft: Boolean
}
```

### MergePullRequestInput

```graphql
input MergePullRequestInput {
  clientMutationId: String
  pullRequestId: ID!
  commitHeadline: String
  commitBody: String
  expectedHeadOid: GitObjectID # Safety check — merge fails if HEAD moved
  mergeMethod: PullRequestMergeMethod # MERGE | SQUASH | REBASE
  authorEmail: String
}
```

### DeleteRefInput

```graphql
input DeleteRefInput {
  clientMutationId: String
  refId: ID! # Ref node ID (from repository.ref query)
}
```

### UpdateIssueInput

```graphql
input UpdateIssueInput {
  clientMutationId: String
  id: ID! # Issue node ID
  title: String
  body: String
  assigneeIds: [ID!]
  milestoneId: ID
  labelIds: [ID!] # Replaces ALL labels (not additive)
  state: IssueState # OPEN | CLOSED
  projectIds: [ID!] # Legacy projects
  issueTypeId: ID
  agentAssignment: AgentAssignmentInput
}
```

### ReopenIssueInput

```graphql
input ReopenIssueInput {
  clientMutationId: String
  issueId: ID!
}
```

### ReprioritizeSubIssueInput

```graphql
input ReprioritizeSubIssueInput {
  clientMutationId: String
  issueId: ID! # Parent issue node ID
  subIssueId: ID! # Sub-issue to reorder
  afterId: ID # Place after this sub-issue (null = move to start)
  beforeId: ID # Place before this sub-issue
}
```

### DeleteProjectV2ItemInput

```graphql
input DeleteProjectV2ItemInput {
  clientMutationId: String
  projectId: ID! # ProjectV2 node ID
  itemId: ID! # ProjectV2Item node ID
}
```

---

## Available Mutations (Not Yet Used)

Potentially useful mutations we don't currently use but may need:

| Mutation                        | Input Type                           | Purpose                              |
| ------------------------------- | ------------------------------------ | ------------------------------------ |
| `reopenIssue`                   | `ReopenIssueInput`                   | Reopen a closed issue                |
| `updateIssue`                   | `UpdateIssueInput`                   | Update issue title/body/labels/state |
| `reprioritizeSubIssue`          | `ReprioritizeSubIssueInput`          | Reorder sub-issues within parent     |
| `deleteProjectV2Item`           | `DeleteProjectV2ItemInput`           | Remove item from project board       |
| `archiveProjectV2Item`          | `ArchiveProjectV2ItemInput`          | Archive (hide) board item            |
| `createRef`                     | `CreateRefInput`                     | Create git branch via API            |
| `enablePullRequestAutoMerge`    | `EnablePullRequestAutoMergeInput`    | Enable auto-merge on PR              |
| `addPullRequestReview`          | `AddPullRequestReviewInput`          | Programmatic PR review               |
| `createLabel`                   | `CreateLabelInput`                   | Create repo labels                   |
| `deleteLabel`                   | `DeleteLabelInput`                   | Delete repo labels                   |
| `convertPullRequestToDraft`     | `ConvertPullRequestToDraftInput`     | Convert PR to draft                  |
| `markPullRequestReadyForReview` | `MarkPullRequestReadyForReviewInput` | Mark draft PR as ready               |
| `createProjectV2StatusUpdate`   | `CreateProjectV2StatusUpdateInput`   | Post status updates to project board |
| `linkProjectV2ToRepository`     | `LinkProjectV2ToRepositoryInput`     | Link repo to project board           |

---

## Key Object Types

### Issue

```graphql
type Issue {
  id: ID!
  number: Int!
  title: String!
  body: String!
  state: IssueState!                    # OPEN | CLOSED
  stateReason: IssueStateReason         # REOPENED | NOT_PLANNED | COMPLETED | DUPLICATE
  url: URI!
  createdAt: DateTime!
  updatedAt: DateTime!
  closedAt: DateTime
  closed: Boolean!
  locked: Boolean!
  isPinned: Boolean
  issueType: IssueType                  # GitHub issue types feature
  parent: Issue                         # Parent issue (sub-issue relationship)

  # Relationships
  author: Actor
  editor: Actor
  milestone: Milestone
  repository: Repository!
  duplicateOf: Issue

  # Collections (all use connection pagination)
  assignees(first, last, after, before): UserConnection!
  labels(first, last, after, before, orderBy): LabelConnection
  comments(first, last, after, before, orderBy): IssueCommentConnection!
  participants(first, last, after, before): UserConnection!

  # Sub-issues (epic hierarchy)
  subIssues(first, last, after, before): IssueConnection!
  subIssuesSummary: SubIssuesSummary    # { total, completed, percentCompleted }

  # Blocking/dependency relationships
  blockedBy(first, last, after, before, orderBy): IssueConnection!
  blocking(first, last, after, before, orderBy): IssueConnection!
  issueDependenciesSummary: IssueDependenciesSummary

  # Tracking (task list checkboxes — different from sub-issues)
  trackedInIssues(first, last, after, before): IssueConnection!
  trackedIssues(first, last, after, before): IssueConnection!
  trackedIssuesCount(states): Int

  # Project boards
  projectItems(first, last, after, before, includeArchived): ProjectV2ItemConnection!
  projectV2(number): ProjectV2
  projectsV2(first, last, after, before, query, orderBy): ProjectV2Connection!

  # PR references
  closedByPullRequestsReferences(first, last, after, before): PullRequestConnection

  # Branches
  linkedBranches(first, last, after, before): LinkedBranchConnection

  # Timeline
  timelineItems(first, last, after, before, since, skip, itemTypes): IssueTimelineItemsConnection!
}
```

### PullRequest

```graphql
type PullRequest {
  id: ID!
  number: Int!
  title: String!
  body: String!
  state: PullRequestState!              # OPEN | CLOSED | MERGED
  url: URI!
  isDraft: Boolean!
  mergeable: MergeableState!            # MERGEABLE | CONFLICTING | UNKNOWN
  mergeStateStatus: MergeStateStatus!   # CLEAN | DIRTY | BLOCKED | BEHIND | UNSTABLE | HAS_HOOKS | UNKNOWN
  reviewDecision: PullRequestReviewDecision  # APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED

  # Branch info
  baseRefName: String!
  headRefName: String!
  baseRefOid: GitObjectID!
  headRefOid: GitObjectID!
  baseRef: Ref
  headRef: Ref
  baseRepository: Repository
  headRepository: Repository

  # Merge info
  merged: Boolean!
  mergedAt: DateTime
  mergedBy: Actor
  mergeCommit: Commit
  canBeRebased: Boolean!
  autoMergeRequest: AutoMergeRequest

  # Metadata
  additions: Int!
  deletions: Int!
  changedFiles: Int!
  createdAt: DateTime!
  updatedAt: DateTime!

  # Collections
  commits(first, last, after, before): PullRequestCommitConnection!
  files(first, last, after, before): PullRequestChangedFileConnection
  labels(first, last, after, before, orderBy): LabelConnection
  assignees(first, last, after, before): UserConnection!
  reviewRequests(first, last, after, before): ReviewRequestConnection
  reviews(first, last, after, before, states, author): PullRequestReviewConnection
  latestReviews(first, last, after, before): PullRequestReviewConnection
  comments(first, last, after, before, orderBy): IssueCommentConnection!
  closingIssuesReferences(first, last, after, before): IssueConnection

  # CI status (accessed via commits.nodes[0].commit)
  statusCheckRollup: StatusCheckRollup  # Direct access on PR object

  # Project boards
  projectItems(first, last, after, before, includeArchived): ProjectV2ItemConnection!
}
```

### ProjectV2

```graphql
type ProjectV2 {
  id: ID!
  number: Int!
  title: String!
  closed: Boolean!
  public: Boolean!
  url: URI!
  readme: String
  shortDescription: String

  # Items — supports server-side filtering via `query` parameter
  items(first, last, after, before, orderBy, query): ProjectV2ItemConnection!
  # Example queries: "status:Ready is:open", "status:In progress", "is:closed"

  # Fields (Status, Priority, Size, etc.)
  fields(first, last, after, before, orderBy): ProjectV2FieldConfigurationConnection!
  field(name): ProjectV2FieldConfiguration  # Lookup by name

  # Views and workflows
  views(first, last, after, before, orderBy): ProjectV2ViewConnection!
  view(number): ProjectV2View
  workflows(first, last, after, before, orderBy): ProjectV2WorkflowConnection!
  workflow(number): ProjectV2Workflow

  # Status updates
  statusUpdates(first, last, after, before, orderBy): ProjectV2StatusUpdateConnection

  # Linked repos and teams
  repositories(first, last, after, before, orderBy): RepositoryConnection!
  teams(first, last, after, before, orderBy): TeamConnection!

  owner: ProjectV2Owner!
  creator: Actor
}
```

### ProjectV2Item

```graphql
type ProjectV2Item {
  id: ID!
  type: ProjectV2ItemType!             # ISSUE | PULL_REQUEST | DRAFT_ISSUE | REDACTED
  isArchived: Boolean!
  createdAt: DateTime!
  updatedAt: DateTime!

  # The actual issue/PR
  content: ProjectV2ItemContent        # Union: Issue | PullRequest | DraftIssue

  # Field values
  fieldValues(first, last, after, before, orderBy): ProjectV2ItemFieldValueConnection!
  fieldValueByName(name): ProjectV2ItemFieldValue  # Lookup by field name

  project: ProjectV2!
  creator: Actor
}
```

### ProjectV2 Field Types

```graphql
# Base field (Title, Assignees, etc.)
type ProjectV2Field {
  id: ID!
  name: String!
  dataType: ProjectV2FieldType!
}

# Single select (Status, Priority, Size)
type ProjectV2SingleSelectField {
  id: ID!
  name: String!
  dataType: ProjectV2FieldType!
  options(names: [String!]): [ProjectV2SingleSelectFieldOption!]!
}

type ProjectV2SingleSelectFieldOption {
  id: String!
  name: String!
  # Also has: color, description, nameHTML
}

# Iteration field (Sprint, etc.)
type ProjectV2IterationField {
  id: ID!
  name: String!
  dataType: ProjectV2FieldType!
  configuration: ProjectV2IterationFieldConfiguration!
}
```

### Field Value Types (returned in fieldValues connection)

```graphql
# Discriminate via __typename
union ProjectV2ItemFieldValue =
  | ProjectV2ItemFieldTextValue # { text, field { name } }
  | ProjectV2ItemFieldNumberValue # { number, field { name } }
  | ProjectV2ItemFieldDateValue # { date, field { name } }
  | ProjectV2ItemFieldSingleSelectValue # { name, optionId, field { name } }
  | ProjectV2ItemFieldIterationValue # { title, startDate, duration, field { name } }
  | ProjectV2ItemFieldLabelValue # { labels { nodes { name } } }
  | ProjectV2ItemFieldMilestoneValue # { milestone { title } }
  | ProjectV2ItemFieldPullRequestValue # { pullRequests { nodes { number } } }
  | ProjectV2ItemFieldRepositoryValue # { repository { nameWithOwner } }
  | ProjectV2ItemFieldUserValue # { users { nodes { login } } }
```

---

## Enum Types

### IssueState

```
OPEN | CLOSED
```

### IssueClosedStateReason

```
COMPLETED | NOT_PLANNED | DUPLICATE
```

### IssueStateReason

```
REOPENED | NOT_PLANNED | COMPLETED | DUPLICATE
```

### PullRequestState

```
OPEN | CLOSED | MERGED
```

### PullRequestMergeMethod

```
MERGE | SQUASH | REBASE
```

### PullRequestReviewDecision

```
CHANGES_REQUESTED | APPROVED | REVIEW_REQUIRED
```

### MergeableState

```
MERGEABLE | CONFLICTING | UNKNOWN
```

### MergeStateStatus

```
CLEAN      # All checks pass, no conflicts
DIRTY      # Has conflicts
BLOCKED    # Branch protection blocks merge
BEHIND     # Base branch has moved ahead
UNSTABLE   # Checks are failing
HAS_HOOKS  # Pre-receive hooks need to run
UNKNOWN    # Not yet computed
```

### ProjectV2FieldType

```
ASSIGNEES | LINKED_PULL_REQUESTS | REVIEWERS | LABELS | MILESTONE
REPOSITORY | TITLE | TEXT | SINGLE_SELECT | NUMBER | DATE | ITERATION
TRACKS | TRACKED_BY | ISSUE_TYPE | PARENT_ISSUE | SUB_ISSUES_PROGRESS
```

### ProjectV2ItemType

```
ISSUE | PULL_REQUEST | DRAFT_ISSUE | REDACTED
```

---

## Query Patterns

### Get issue with all relationships

```graphql
query ($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    issue(number: $number) {
      id
      number
      title
      body
      state
      url
      labels(first: 20) {
        nodes {
          name
        }
      }
      assignees(first: 10) {
        nodes {
          login
        }
      }
      subIssues(first: 50) {
        nodes {
          id
          number
          title
          state
          url
        }
      }
      blockedBy(first: 10) {
        nodes {
          number
          title
          state
        }
      }
      blocking(first: 10) {
        nodes {
          number
          title
          state
        }
      }
    }
  }
}
```

### Get project board items (server-side filtered)

```graphql
query ($owner: String!, $projectNumber: Int!, $cursor: String) {
  organization(login: $owner) {
    projectV2(number: $projectNumber) {
      items(first: 100, after: $cursor, query: "status:Ready is:open") {
        pageInfo {
          hasNextPage
          endCursor
        }
        nodes {
          id
          content {
            __typename
            ... on Issue {
              number
              title
              state
              url
              repository {
                nameWithOwner
              }
              labels(first: 20) {
                nodes {
                  name
                }
              }
              subIssues(first: 50) {
                nodes {
                  number
                  title
                  state
                }
              }
              blockedBy(first: 10) {
                nodes {
                  number
                  title
                  state
                }
              }
              blocking(first: 10) {
                nodes {
                  number
                  title
                  state
                }
              }
            }
            ... on PullRequest {
              number
              title
              state
              url
            }
          }
          fieldValues(first: 20) {
            nodes {
              __typename
              ... on ProjectV2ItemFieldSingleSelectValue {
                name
                field {
                  ... on ProjectV2SingleSelectField {
                    name
                  }
                }
              }
              ... on ProjectV2ItemFieldTextValue {
                text
                field {
                  ... on ProjectV2Field {
                    name
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}
```

### Get project fields (for field IDs and option IDs)

```graphql
query ($owner: String!, $projectNumber: Int!) {
  organization(login: $owner) {
    projectV2(number: $projectNumber) {
      fields(first: 30) {
        nodes {
          __typename
          ... on ProjectV2Field {
            id
            name
            dataType
          }
          ... on ProjectV2SingleSelectField {
            id
            name
            dataType
            options {
              id
              name
            }
          }
          ... on ProjectV2IterationField {
            id
            name
            dataType
            configuration {
              iterations {
                id
                title
                startDate
                duration
              }
            }
          }
        }
      }
    }
  }
}
```

### Count items by status (aliased query)

```graphql
query ($owner: String!, $projectNumber: Int!) {
  organization(login: $owner) {
    projectV2(number: $projectNumber) {
      ready: items(query: "status:Ready is:open") {
        totalCount
      }
      inProgress: items(query: "status:In progress is:open") {
        totalCount
      }
      inReview: items(query: "status:In review is:open") {
        totalCount
      }
      done: items(query: "status:Done") {
        totalCount
      }
      backlog: items(query: "status:Backlog is:open") {
        totalCount
      }
    }
  }
}
```

### Get PR with CI status

```graphql
query ($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      id
      number
      title
      state
      url
      mergeable
      isDraft
      reviewDecision
      headRefName
      baseRefName
      labels(first: 20) {
        nodes {
          name
        }
      }
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup {
              state
            }
          }
        }
      }
    }
  }
}
```

### Cross-repo node lookup

```graphql
query ($id: ID!) {
  node(id: $id) {
    __typename
    ... on Issue {
      id
      number
      title
      state
      repository {
        nameWithOwner
      }
      subIssues(first: 50) {
        nodes {
          number
          title
          state
        }
      }
    }
  }
}
```

---

## Go Struct ↔ GraphQL Type Mapping

The `shurcooL/graphql` library derives GraphQL type names from Go struct names.
**The Go struct name MUST exactly match the GraphQL type.**

| Go Struct Name                       | GraphQL Type                         | Status  |
| ------------------------------------ | ------------------------------------ | ------- |
| `AddProjectV2ItemByIdInput`          | `AddProjectV2ItemByIdInput`          | Correct |
| `UpdateProjectV2ItemFieldValueInput` | `UpdateProjectV2ItemFieldValueInput` | Correct |

### Common Mistakes

- `addProjectItemInput` → sends `AddProjectItemInput` → **WRONG** (should be
  `AddProjectV2ItemByIdInput`)
- `createIssueInput` → sends `CreateIssueInput` → **happens to work** because
  the library capitalizes the first letter, but prefer matching exactly
- When adding new mutations, always name the Go input struct to exactly match
  the GraphQL input type from the schema

---

## Transport Classification — GraphQL vs REST

> **Deliverable of [#849](https://github.com/nightgauge/nightgauge/issues/849)
> AC 1** (epic #842, the GitHub API budget). Every GraphQL call site in
> `internal/github` is classified below. Re-derived and probed against the live
> API on 2026-08-25 — **not** carried forward from the issue body.

### Why the classification exists

The two rate-limit buckets are wildly unbalanced. GraphQL is the one Nightgauge
exhausts; REST sits near-idle:

```
graphql:  367 / 5000 used
core:      22 / 5000 used
```

The obvious reading is "move points to the idle bucket". That is the weaker
half of the argument, and taking it alone leads to lateral churn. **The real
win is conditional GET**, and it only exists on REST:

```
# Unconditional GET, twice — billed each time
HTTP 200  X-RateLimit-Remaining: 4987
HTTP 200  X-RateLimit-Remaining: 4986

# Same URL with If-None-Match, three times — billed ZERO times
HTTP 304  X-RateLimit-Remaining: 4986
HTTP 304  X-RateLimit-Remaining: 4986
HTTP 304  X-RateLimit-Remaining: 4986
```

Measured 2026-08-25 against `/repos/nightgauge/nightgauge/pulls/924/reviews`.
A GraphQL POST can never do this. The client's ETag layer (#486,
`rateLimitHeaderTransport`) attaches `If-None-Match` to every GET on the API
host automatically, so a migrated read gets this with no call-site work.

**So the ranking rule is: migrate reads that repeat WITHIN ONE PROCESS
lifetime first.** A call made once per run saves one point — the bucket move,
and nothing more. A call made every ten seconds until a human or a bot answers
saves one point per tick, for as long as the wait lasts.

The "within one process" qualifier is load-bearing and easy to miss. **The ETag
cache is in-memory and hangs off a single `*Client`** (`installHeaderInterceptor`),
so it is not shared between separate `nightgauge` invocations. A read repeated
across CLI runs gets the bucket move and a cold cache; a read repeated inside a
poll loop, or inside `nightgauge serve`, gets the 304s. Claiming a migrated
one-shot read is "free" is wrong — it is cheaper, and free only in the daemon.

### The three verdicts

| Verdict                 | Meaning                                                                                               |
| ----------------------- | ----------------------------------------------------------------------------------------------------- |
| **requires-GraphQL**    | REST cannot answer it at all, or cannot answer it without losing a fact the caller depends on         |
| **GraphQL-by-batching** | REST could answer it, but only in several round trips, or without a filter GitHub applies server-side |
| **better-as-REST**      | REST answers it in one call, and the read is repeated often enough that ETag conditioning pays        |

### ProjectV2 — requires-GraphQL, without exception

**ProjectV2 has no REST API.** Not a preference, a hard floor: every board
read, field read, field write, item add and item lookup must be GraphQL.

| Call site                                                                                                         | File                |
| ----------------------------------------------------------------------------------------------------------------- | ------------------- |
| `queryProjectItems`, `queryProjectItemsFiltered`, `queryProjectFieldsFull`, `queryProjectUpdatedAt`               | `project_query.go`  |
| `AddItem`, `findItemID`, `updateField`, `createField`, `replaceFieldOptions`, `ResolveProject`, `getItemEstimate` | `project.go`        |
| `FetchRepositoryLinkedProjects`, `FetchProjectLinkedRepos`                                                        | `project_repos.go`  |
| `ViewService.List`                                                                                                | `views.go`          |
| `BoardService.CountsByStatus`                                                                                     | `board.go`          |
| `findProjectItemID`                                                                                               | `epic.go`           |
| `ProjectNumbersForIssue`                                                                                          | `issue_projects.go` |

This is where the GraphQL budget actually goes, and it is why #847's change
probe — making the expensive board read _conditional_ rather than moving it —
was the right shape for the biggest single consumer. **The lesson generalises:
when a call is requires-GraphQL, the only lever left is not making it.**

### Non-project call sites

| Call site                                                   | Verdict                         | Why                                                                                                                                        |
| ----------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `SecurityService.ListOpenAlerts`                            | **requires-GraphQL**            | `dependabotUpdate` has no REST equivalent — see below                                                                                      |
| `IssueService.GetIssue`                                     | **GraphQL-by-batching**         | One document carries the issue, labels, sub-issues and blockedBy; REST needs 3–4 calls                                                     |
| `IssueService.GetIssuesByNumbers`                           | **GraphQL-by-batching**         | N issues in ONE aliased request. REST is strictly N calls                                                                                  |
| `IssueService.GetEpicProgress`                              | **GraphQL-by-batching**         | Sub-issue rollup in one hop                                                                                                                |
| `IssueService.SearchIssues`                                 | **GraphQL-by-batching**         | `search()` applies the query server-side                                                                                                   |
| `IssueService.ListIssues`, `ListIssuesExcludingLabels`      | **GraphQL-by-batching**         | Label filtering and node IDs in one page                                                                                                   |
| `IssueService.GetRepoLabels`, `LabelService.List`           | **better-as-REST** ✅ migrated  | `GET /repos/{o}/{r}/labels?per_page=100`. The deferral was wrong — REST reports `node_id`, so the mutations they feed did not have to move |
| `PRService.GetPR`                                           | **GraphQL-by-batching**         | PR + labels + `statusCheckRollup` in one document                                                                                          |
| `PRService.ListPRs`                                         | **GraphQL-by-batching**         | Same, per page                                                                                                                             |
| `PRService.ListMergedPRHeads`                               | **GraphQL-by-batching**         | `states: MERGED` is a SERVER-SIDE filter REST lacks — see below                                                                            |
| `PRService.CommitParents`                                   | **better-as-REST** ✅ migrated  | `GET /repos/{o}/{r}/commits/{sha}`, per-branch; bucket move always, 304s only under `serve`                                                |
| `PRService.DeleteBranch`                                    | requires-GraphQL (read)         | Resolves a ref node ID for `deleteRef`                                                                                                     |
| `RulesetService.hasCopilotReviewed`                         | **better-as-REST** ✅ migrated  | POLLED read — the strongest ETag case in the tree                                                                                          |
| `RepoService.RepoMetadata`                                  | **requires-GraphQL** ✅ settled | REST reports a `default_branch` for a repo that has none — see below                                                                       |
| `Client.GetRepositoryID`                                    | **better-as-REST** ✅ migrated  | `GET /repos/{o}/{r}` returns `node_id`. Same correction as the labels row: no mutation had to move with it                                 |
| `Client.GetRateLimit`                                       | **either — no gain**            | The GraphQL `rateLimit` query is genuinely free                                                                                            |
| `Client.ExecuteGraphQL`                                     | requires-GraphQL                | It _is_ the pass-through transport for `forge graphql`                                                                                     |
| All `IssueService` / `PRService` / `LabelService` mutations | **coupled**                     | See _Node-ID coupling_ below                                                                                                               |

### Node-ID coupling — the real reason the mutation surface has not moved

Every mutation in `issues.go`, `prs.go` and `labels.go` takes a `graphql.ID`.
Those IDs come from a GraphQL read, so a mutation cannot be migrated on its
own — its ID source has to move with it, and every other consumer of that read
has to keep working. **This, not the API surface, is what makes AC 2 a
land-alone change.**

One correction worth recording, because it dissolves the obvious objection:
**REST returns node IDs.** `GET /repos/{o}/{r}/issues/{n}` includes `node_id`
(verified live 2026-08-25), so "we need the node ID" is not by itself a reason
to keep a READ on GraphQL. The coupling is a migration-ordering constraint, not
a capability one.

**And it constrains WRITES only.** Two rows of the table above carried
"(deferred) — moves with the mutations that consume it" for exactly one
sentence's worth of reasoning, and that reasoning did not survive the
correction: `GET /repos/{o}/{r}` and `GET /repos/{o}/{r}/labels` both report
`node_id` (verified live 2026-08-26, and pinned by
`TestGetRepositoryID_UsesREST` / `TestListRepoLabels_UsesRESTWithFullPage`), so
`createIssue`, `createPullRequest`, `createLabel`, `addLabelsToLabelable` and
`removeLabelsFromLabelable` consume the migrated reads' output byte-for-byte
unchanged. Nothing downstream can tell which transport produced the ID, so
there is no second write path and no dual-path drift to avoid.

**A deferral is a claim, and it ages.** Both rows were written in the same pass
that recorded "REST returns node IDs" three paragraphs below them — the
correction simply had not been applied back to the entries it invalidated. When
a worklist says "deferred", check whether its stated blocker is still the
blocker before scheduling work around it.

### `GetRateLimit` is free — a cheap disconfirmer, settled

The comment in `client.go` calls the `rateLimit` query free. It is, and the
obvious "we spend a GraphQL point to measure the GraphQL budget" finding is
**not real**. Measured 2026-08-25 — three consecutive queries:

```
remaining=4931 cost=1
remaining=4931 cost=1
remaining=4931 cost=1
```

GitHub _reports_ `cost: 1` while never decrementing `remaining`. Note the
consequence for the ledger: it derives cost from the drop in
`X-RateLimit-Remaining`, so it prices this call at 0 — which is correct, and
disagrees with the number GitHub prints. **Do not "fix" that disagreement.**

### `ListOpenAlerts` is requires-GraphQL — correcting this issue's own worklist

#849's first-candidate analysis named `SecurityService.ListOpenAlerts` as the
unambiguous first migration, on the grounds that it already makes a REST call
on the empty path and that REST distinguishes "clean" from "forbidden" by
status code. Both of those statements are true. **The conclusion is still
wrong**, and the premise check that catches it takes one command.

The producers that consume this service read `Remediation` — the tri-state of
"a fix PR is open" / "the forge says a fix is not possible, and why" / "no fix
yet". That comes from GraphQL's `RepositoryVulnerabilityAlert.dependabotUpdate`.
REST's alert object does not carry it. Its complete key set, live:

```
assignees, auto_dismissed_at, created_at, dependency, dismissal_request,
dismissed_at, dismissed_by, dismissed_comment, dismissed_reason, fixed_at,
html_url, number, security_advisory, security_vulnerability, state,
updated_at, url
```

No link from an alert to the PR that fixes it, and no statement of why one does
not exist. Migrating would delete the silent-empty guard as advertised — and
also delete `attention`'s entire remediation surface (`dependabotalerts.go`,
`dependabotstale.go`). The header of `internal/github/security.go` documents
this; the issue analysis did not check it before naming the candidate.

**The generalisable part: a call site that already spends on two transports
looks like waste and may be paying for a fact only one of them has.** Read what
the caller consumes before pricing the call.

### `ListMergedPRHeads` — why a server-side filter is worth a bucket

REST can list closed PRs (`GET /pulls?state=closed`) but cannot filter to
MERGED. GraphQL's `states: MERGED` does it server-side. Since the index is one
page of 100 by deliberate design (see `mergedPRIndexSize`), migrating would
spend that fixed budget on closed-unmerged PRs and **shrink the window** —
paying a correctness cost for a bucket saving. Server-side filtering is a real
GraphQL win and this is the clearest instance of it.

### `RepoMetadata` — the trap is real, and it reclassifies the call site

**Settled 2026-08-26. This read is requires-GraphQL; do not migrate it.**

#849 classified `RepoMetadata` better-as-REST because `GET /repos/{o}/{r}`
returns `full_name`, `owner.login`, `name` and `default_branch` in one
ETag-able call with no node ID needed. Every one of those statements is true.
The conclusion is still wrong, and the previous pass said so — it flagged that
the two APIs _might_ disagree about an empty repository and marked that half
**NOT verified**, because checking needs a repository with zero commits.

That probe has now been run. One throwaway repository, read by both APIs at the
same moment, before and after its first commit:

| State        | GraphQL `defaultBranchRef` | GraphQL `isEmpty` | REST `default_branch` | REST `size` | REST `pushed_at` |
| ------------ | -------------------------- | ----------------- | --------------------- | ----------- | ---------------- |
| zero commits | `null`                     | `true`            | `"main"`              | `0`         | creation time    |
| one commit   | `"main"`                   | `false`           | `"main"`              | `0`         | **unchanged**    |

**REST names a default branch for a repository that has no branch, and names it
identically in both states.** The field carries no information about whether the
ref exists.

That is not cosmetic. `attention/sweep.DefaultBranchHealth.Evaluate` treats an
empty `DefaultBranch` as its signal to decline to observe, and its own comment
explains why: guessing a branch name produces a 404 that reads as a producer
failing forever. A naive migration silently converts every empty repository
into a permanently failing producer — the exact failure the last pass feared,
now measured rather than suspected.

**And there is no cheap REST field to guard it with**, which is what closes the
question. The two obvious candidates both fail on the same probe: `size` is `0`
on a repository that HAS a commit, so it is not an emptiness test; `pushed_at`
is stamped at creation and did not update on push. The only REST guards left
are a _second_ call (`/commits` → 409, or `/branches`), which erases the saving
that motivated the migration in the first place.

So `isEmpty` joins `dependabotUpdate` as **a fact REST does not carry**, and
`RepoMetadata` joins `ListOpenAlerts` in the same bucket for the same reason.
The rule those two now share, stated once:

> A REST endpoint that returns the field you asked for has not necessarily
> answered your question. Check what the CALLER does with the field — here, a
> caller that depends on the field being _absent_.

The reasoning is repeated at the call site in `internal/github/repo.go`, not
only here: a doc stops a reader who goes looking, and a comment stops the one
who does not. Pinned by `TestRepoMetadata_EmptyRepoHasNoDefaultBranch`.

### Sub-issue and dependency links — REST endpoints the tree does not use

The previous pass found that `GET /repos/{o}/{r}/issues/{n}/sub_issues` and
`.../dependencies/blocked_by` answer 200, and noted the write counterparts were
documented but **not exercised**. They have now been exercised end to end
against throwaway repositories (2026-08-26). All four work, and all four bill
`core`:

| Operation         | Endpoint                                                        | Body / path parameter       |
| ----------------- | --------------------------------------------------------------- | --------------------------- |
| add sub-issue     | `POST /repos/{o}/{r}/issues/{n}/sub_issues`                     | `{"sub_issue_id": <db id>}` |
| remove sub-issue  | `DELETE /repos/{o}/{r}/issues/{n}/sub_issue`                    | `{"sub_issue_id": <db id>}` |
| add blocked-by    | `POST /repos/{o}/{r}/issues/{n}/dependencies/blocked_by`        | `{"issue_id": <db id>}`     |
| remove blocked-by | `DELETE /repos/{o}/{r}/issues/{n}/dependencies/blocked_by/{id}` | database id in the PATH     |

Three properties that decide whether the write surface can move:

1. **They take the DATABASE id, not the node ID.** So migrating a mutation does
   not merely relocate the node-ID coupling — it dissolves it, provided the id
   source moves too.
2. **They work cross-repository.** A parent in one repository accepted a child
   from another, for both sub-issues and blocked-by, and the GET response
   carries `.repository.full_name` — which is exactly what `types.SubIssueRef.Repo`
   needs. This matters because `internal/audit/issue_creator.go` resolves
   `epicRepo` and `subRepo` independently, so cross-repo linking is a live path
   in the tree and not a hypothetical.
3. **A 404 from these routes means the referenced ISSUE is absent, not that the
   route is.** See the discriminator below.

### Probing whether a write endpoint exists, without writing anything

Confirming a mutating endpoint normally means performing a mutation. It does
not have to. GitHub returns an endpoint-specific `documentation_url` for a
route it matched, and the generic `https://docs.github.com/rest` for one it did
not:

```
POST .../issues/842/sub_issues        {"sub_issue_id": 999999999999}
  → 404  documentation_url: .../rest/issues/sub-issues#add-sub-issue      ← route EXISTS
POST .../issues/842/not_a_real_endpoint
  → 404  documentation_url: https://docs.github.com/rest                  ← route does not
```

Send a request that cannot succeed — a well-formed body naming an object that
does not exist — and read which of the two you get. That distinguishes "this
endpoint is not available to me" from "my arguments were wrong" for zero
mutations and one request. Worth reaching for before deciding an API cannot do
something.

### What has been migrated

| Call site                                                                              | Was                                                       | Now                                                    |
| -------------------------------------------------------------------------------------- | --------------------------------------------------------- | ------------------------------------------------------ |
| `RulesetService.hasCopilotReviewed`                                                    | GraphQL `pullRequest.reviews(first: 10)`                  | `GET /repos/{o}/{r}/pulls/{n}/reviews?per_page=100`    |
| `PRService.CommitParents`                                                              | GraphQL `repository.object(oid:)`                         | `GET /repos/{o}/{r}/commits/{sha}`                     |
| `ProjectService.AddBlockedByNumber` / `RemoveBlockedByNumber` — **ID resolution only** | 2× `IssueService.GetIssue` (a full GraphQL document each) | 2× `GET /repos/{o}/{r}/issues/{n}` (`resolveIssueRef`) |
| `Client.GetRepositoryID`                                                               | GraphQL `repository(owner:, name:) { id }`                | `GET /repos/{o}/{r}` (`node_id`)                       |
| `IssueService.GetRepoLabels`, `LabelService.List`                                      | GraphQL `repository.labels(first: 100)`                   | `GET /repos/{o}/{r}/labels?per_page=100`               |

The label reads keep the predecessor's one-page cap deliberately: `first: 100`
and `per_page=100` truncate a >100-label repository identically, so a behaviour
change cannot hide inside the transport change.

**One thing the transports do NOT agree on is order** — GraphQL returned
GitHub's default labels first, REST returns them alphabetically. Both were run
against the same repository and returned the identical set of 30 labels, and
all three consumers match by name (`Create`'s idempotency check, `Rename`'s
lookup, and the map `GetRepoLabels` builds), so nothing can observe it but the
`label list` printout. Recorded because it was found by diffing the two
binaries' live output, not by a unit test: a fixture-shaped test asserts the
order its own fixture has. `GetRepoLabels` also keeps its
per-`IssueService` memo, which is why its ETag win only appears in a process
that reads more than one repository's labels.

Ledger before/after, one `nightgauge worktree sweep --dry-run` over the
workspace (**AC 3**):

| Metric                | Before | After |
| --------------------- | ------ | ----- |
| GraphQL calls         | 4      | 3     |
| GraphQL points billed | 3      | 2     |
| REST (core) calls     | 0      | 1     |

`hasCopilotReviewed` does not appear in a sweep — it runs in the PR merge path
— and is the larger saving of the two, because it is the polled one.

The label read is measured on its own, because no sweep reads labels. One
`nightgauge label list --repo nightgauge/nightgauge`, same tree, two binaries,
`NIGHTGAUGE_GITHUB_API_LOG` on:

| Binary | Bucket    | Request                                   |
| ------ | --------- | ----------------------------------------- |
| before | `graphql` | `POST /graphql` (`repository`)            |
| after  | `core`    | `GET /repos/nightgauge/nightgauge/labels` |

Both lines report `cost: 0`, and that is the ledger working as designed rather
than a null result: cost is derived from the DROP in `X-RateLimit-Remaining`
between two observations, and a one-shot CLI run makes exactly one. The bucket
is the measurement here — `remaining` was 4949/5000 on `graphql` before and
4935/5000 on `core` after, which is the point of the move.

**The blockedBy ID-resolution migration, and why only half of it moved.**
`AddBlockedByNumber` and `RemoveBlockedByNumber` each issued **three** GraphQL
calls: two full `GetIssue` documents and one mutation. The two reads existed
only to reach a node ID and a parent number — and REST reports both in one
conditional-GET-able call, so they became `resolveIssueRef`. Per call site
that is 3 GraphQL points → 1, with the two survivors moved to the idle `core`
bucket and ETag-able under `nightgauge serve`. It is the first cash-in of the
"REST returns node IDs" finding above.

**The mutation deliberately stayed on GraphQL.** Migrating it means switching
to database ids, and every other caller of `IssueService.AddBlockedBy` would
still be passing node IDs — two write paths creating the same edge, which is
the Read-Through/dual-path shape `docs/FAILURE_TAXONOMY.md` says to never
resolve by "keeping them in sync". The write surface moves as one piece, with
its id source, in the client-family unification (#849 AC 2 / #848 AC 2).

**One trap this migration hit, and it is not obvious:** the two APIs disagree
about what a number means. GraphQL's `repository.issue(number:)` errors
`NOT_FOUND` for a pull request number; REST's `/issues/{n}` **returns the pull
request**, because GitHub models PRs as issues. Verified live on PR #925. A
straight port therefore _widens_ the contract in silence — `issue
add-blocked-by 925 …` starts succeeding where it used to fail loudly. REST
marks the difference with a `pull_request` key on the object, so the rejection
costs no extra call, but it has to be written down. Pinned by
`TestResolveIssueRef_RejectsAPullRequestNumber`.

> Generalisable: when a read moves transport, check what each API does with an
> input that is **not valid** — not just what they do with a valid one. The
> happy paths agreeing says nothing about the error paths agreeing.

**Two traps the earlier migration hit, recorded so the next one does not:**

1. **An unknown commit SHA returns 422, not 404.** GraphQL expressed "no such
   object" as a null field; REST splits it across two statuses, and 422 (`No
commit found for SHA`) is the one the merged-PR door actually meets. A
   migration handling only 404 turns every index miss into a sweep failure.
   Pinned by `TestCommitParents_UnknownSHAIsNotAnError`.
2. **A page bound can be a correctness bound.** The GraphQL predecessor of the
   Copilot probe selected `reviews(first: 10)` — on a PR with ten human reviews
   Copilot's is the eleventh, invisible, and the poll runs to its deadline
   reporting a timeout for a review that exists. REST costs the same request at
   `per_page=100`.

### The remaining worklist (AC 2)

Ordered by value, not by ease:

1. **Sub-issues and blocked-by now have REST endpoints** — and the tree does not
   use them. `GET /repos/{o}/{r}/issues/{n}/sub_issues` and
   `.../dependencies/blocked_by` both answer 200 on the core bucket (verified
   2026-08-25); these were GraphQL-only when `issues.go` was written, and the
   tree has moved since. Their write counterparts **have since been exercised
   end to end** — see _Sub-issue and dependency links_ above for the four
   endpoints, their database-id parameters, and the cross-repository result.
   **Re-check the premise before building** — that is how this entry was found.
2. **Two client families exist, and the raw one bypasses `forge`/`boardcache`
   entirely.** Measured 2026-08-25 rather than carried forward: **15**
   construction sites outside the forge adapters build a `BoardService`
   directly (`internal/doctor`, `internal/state`, `internal/depgraph`,
   `internal/ipc/server.go`, and several inside `internal/github` itself), and
   `internal/ipc/server.go` alone builds 12 `IssueService`, 7 `PRService`, 4
   `ProjectService`, 2 `BoardService` and 1 `EpicService` inline. Unifying the
   two families is AC 2's real work — high conflict surface, land it alone, and
   do **not** bolt a cache onto the raw path (see `docs/FAILURE_TAXONOMY.md`
   § Read-Through Cache Without Write Interception).

   Note this corrects an inherited figure. The previous framing said "~10
   mutating IPC verbs each build their own `gh.NewBoardService`"; the bypass is
   real and larger than that, but only 2 of those sites are `BoardService` and
   the rest are other services. Counting took one command.

3. ~~**`RepoMetadata`**~~ — **settled: requires-GraphQL, do not migrate.** The
   empty-repository contract was probed and REST lost; see the `RepoMetadata`
   section above. This entry outlived its own resolution by two sessions, which
   is the argument for striking a worklist line the moment it is answered
   rather than leaving it to read as open work.
4. ~~**Labels and `GetRepositoryID`**~~ — **the READS are migrated.** The
   mutations they feed stayed on GraphQL and did not have to move; see
   _Node-ID coupling_ above for why that deferral was wrong.

---

## Validation

- **Automated**: `internal/github/schema_validation_test.go` validates query
  struct fields against expectations
- **Manual introspection**: Use `gh api graphql` to check types before coding
- **Dependency tracking**: See `docs/GITHUB_API_DEPENDENCIES.md` for risk
  assessment

### Quick introspection commands

```bash
# Check a mutation's input type
gh api graphql -f query='{ __type(name: "AddSubIssueInput") { name inputFields { name type { name kind ofType { name } } } } }'

# Check an object type's fields
gh api graphql -f query='{ __type(name: "Issue") { fields { name type { name } args { name } } } }'

# Check available mutations matching a pattern
gh api graphql -f query='{ __schema { mutationType { fields { name } } } }' \
  --jq '.data.__schema.mutationType.fields[].name' | grep -i project

# Check enum values
gh api graphql -f query='{ __type(name: "PullRequestMergeMethod") { enumValues { name } } }'
```

---

## Changelog

| Date       | Change                                                    |
| ---------- | --------------------------------------------------------- |
| 2026-03-11 | Initial schema introspection from live GitHub GraphQL API |
| 2026-08-25 | Transport classification of every call site (#849 AC 1)   |
