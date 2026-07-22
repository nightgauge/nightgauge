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
