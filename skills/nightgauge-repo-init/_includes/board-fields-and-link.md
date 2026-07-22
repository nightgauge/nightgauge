# Board Field Validation, Repo Link & Views (Phases 4, 5, 5.2)

Procedural detail for Phase 4 (Project Board Field Validation), Phase 5 (Link
Repository to Project), and Phase 5.2 (Standard Project Board Views).

## Contents

- [Phase 4: Project Board Field Validation](#phase-4-project-board-field-validation)
- [Phase 5: Link Repository to Project](#phase-5-link-repository-to-project)
- [Phase 5.2: Standard Project Board Views](#phase-52-standard-project-board-views)

---

## Phase 4: Project Board Field Validation

Create or ensure all required project board fields exist using the Go binary's
`project ensure-fields` verb. This replaces inline GraphQL — the binary handles
owner-type branching, idempotency, and returns field IDs in a single call.

### Required Fields Matrix

| Field       | Type          | Required Options                             |
| ----------- | ------------- | -------------------------------------------- |
| Status      | SINGLE_SELECT | Backlog, Ready, In progress, In review, Done |
| Priority    | SINGLE_SELECT | P0, P1, P2, P3                               |
| Size        | SINGLE_SELECT | XS, S, M, L, XL                              |
| Start date  | DATE          | N/A                                          |
| Target date | DATE          | N/A                                          |
| Estimate    | NUMBER        | N/A                                          |

```bash
# Determine --owner-type flag from OWNER_TYPE resolved in Phase 1
ENSURE_OWNER_TYPE_FLAG="org"
if [ "$OWNER_TYPE" = "user" ]; then
  ENSURE_OWNER_TYPE_FLAG="user"
fi

# Create/ensure all required project board fields (idempotent)
ENSURE_RESULT=$(nightgauge project ensure-fields \
  --number "$PROJECT_NUMBER" \
  --owner "$PROJECT_OWNER" \
  --owner-type "$ENSURE_OWNER_TYPE_FLAG" \
  --json)

if [ $? -ne 0 ]; then
  echo "ERROR: Failed to ensure project board fields for project #$PROJECT_NUMBER"
  echo "$ENSURE_RESULT"
  exit 1
fi

echo "Field validation complete:"
echo "$ENSURE_RESULT" | jq -r '
  if (.created | length) > 0 then "  Created: \(.created | join(", "))" else empty end,
  if (.updated | length) > 0 then "  Updated: \(.updated | join(", "))" else empty end,
  if (.already | length) > 0 then "  Already: \(.already | join(", "))" else empty end'

# Extract field IDs from ensure-fields JSON output (no separate query needed)
STATUS_FIELD_ID=$(echo "$ENSURE_RESULT" | jq -r '.field_ids["Status"] // empty')
PRIORITY_FIELD_ID=$(echo "$ENSURE_RESULT" | jq -r '.field_ids["Priority"] // empty')
SIZE_FIELD_ID=$(echo "$ENSURE_RESULT" | jq -r '.field_ids["Size"] // empty')
START_DATE_FIELD_ID=$(echo "$ENSURE_RESULT" | jq -r '.field_ids["Start date"] // empty')
TARGET_DATE_FIELD_ID=$(echo "$ENSURE_RESULT" | jq -r '.field_ids["Target date"] // empty')
ESTIMATE_FIELD_ID=$(echo "$ENSURE_RESULT" | jq -r '.field_ids["Estimate"] // empty')
```

---

## Phase 5: Link Repository to Project

Phase 1, Step 1.1 already queried whether the repo was linked to this project.
If `LINKED_PROJECTS` included this project number, skip the link step:

```bash
ALREADY_LINKED=false
if echo "$LINKED_PROJECTS" | awk -F'\t' '{print $1}' | grep -qx "$PROJECT_NUMBER"; then
  ALREADY_LINKED=true
fi

if [ "$ALREADY_LINKED" = "true" ]; then
  echo "  ✓ Repository already linked to project #$PROJECT_NUMBER"
else
  # Resolve project node ID and repo node ID, then link via GraphQL mutation
  PROJECT_NODE_ID=$(nightgauge forge graphql -f query='query($owner:String!,$number:Int!){organization(login:$owner){projectV2(number:$number){id}}}' -f owner="$PROJECT_OWNER" -F number="$PROJECT_NUMBER" 2>/dev/null | jq -r '.data.organization.projectV2.id // empty')
  if [ -z "$PROJECT_NODE_ID" ]; then
    PROJECT_NODE_ID=$(nightgauge forge graphql -f query='query($owner:String!,$number:Int!){user(login:$owner){projectV2(number:$number){id}}}' -f owner="$PROJECT_OWNER" -F number="$PROJECT_NUMBER" 2>/dev/null | jq -r '.data.user.projectV2.id // empty')
  fi
  REPO_NODE_ID=$(nightgauge forge graphql -f query='query($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}' -f owner="$OWNER" -f name="$REPO_NAME" | jq -r '.data.repository.id')
  nightgauge forge graphql -f query='mutation($projectId:ID!,$repoId:ID!){linkProjectV2ToRepository(input:{projectId:$projectId,repositoryId:$repoId}){repository{id}}}' -f projectId="$PROJECT_NODE_ID" -f repoId="$REPO_NODE_ID"
  echo "  + Linked $REPO to project #$PROJECT_NUMBER"
fi
```

---

## Phase 5.2: Standard Project Board Views

Create the standard set of project board views using the GitHub REST API
(`X-GitHub-Api-Version: 2026-03-10`). Views provide different layouts for
managing issues: board views for status/priority grouping, table views for
detailed lists, and roadmap views for timeline planning.

**Standard views:**

| View Name      | Layout    | Filter         | Purpose                          |
| -------------- | --------- | -------------- | -------------------------------- |
| Backlog        | `board`   | (none)         | Issue triage, grouped by status  |
| Priority board | `board`   | (none)         | Active work, grouped by priority |
| Team items     | `table`   | (none)         | Team capacity planning           |
| Roadmap        | `roadmap` | (none)         | Timeline with start/target dates |
| My items       | `table`   | `assignee:@me` | Personal assignee view           |

**Implementation details:**

- **Tool:** `nightgauge project view-create` (idempotent Go binary wrapper)
- **Idempotency:** The Go binary queries existing views before creating. Skip
  views that already exist by name. The GraphQL pre-check below provides the
  `EXISTING_VIEWS` list for fast "✓ exists" display.
- **Limitation:** Views cannot be updated or deleted via API. If a view needs
  renaming or removal, it must be done manually in the GitHub web UI.

```bash
# Determine --owner-type flag for view commands (Go binary default is "org")
VIEW_OWNER_TYPE_FLAG="org"
if [ "$PROJECT_OWNER_TYPE" = "User" ]; then
  VIEW_OWNER_TYPE_FLAG="user"
fi

# Query existing views via GraphQL (for fast ✓ exists / + created display)
if [ "$PROJECT_OWNER_TYPE" = "Organization" ]; then
  EXISTING_VIEWS=$(nightgauge forge graphql -f query='
  {
    organization(login: "'"$PROJECT_OWNER"'") {
      projectV2(number: '"$PROJECT_NUMBER"') {
        views(first: 20) {
          nodes { name layout }
        }
      }
    }
  }' --jq '.data.organization.projectV2.views.nodes[].name')
else
  EXISTING_VIEWS=$(nightgauge forge graphql -f query='
  {
    user(login: "'"$PROJECT_OWNER"'") {
      projectV2(number: '"$PROJECT_NUMBER"') {
        views(first: 20) {
          nodes { name layout }
        }
      }
    }
  }' --jq '.data.user.projectV2.views.nodes[].name')
fi

# Create each standard view if it doesn't already exist
create_view_if_missing() {
  local name="$1" layout="$2" filter="$3"

  if echo "$EXISTING_VIEWS" | grep -qx "$name"; then
    echo "  ✓ exists:  $name ($layout)"
    return
  fi

  if [ "$DRY_RUN" = "true" ]; then
    echo "  ~ would create: $name ($layout)"
    return
  fi

  local view_args=(
    --project "$PROJECT_NUMBER"
    --owner "$PROJECT_OWNER"
    --owner-type "$VIEW_OWNER_TYPE_FLAG"
    --name "$name"
    --layout "$layout"
  )
  if [ -n "$filter" ]; then
    view_args+=(--filter "$filter")
  fi

  if nightgauge project view-create "${view_args[@]}" 2>&1; then
    echo "  + created: $name ($layout)"
  else
    echo "  ✗ FAILED:  $name ($layout) — check token scopes (project write required)"
  fi
}

create_view_if_missing "Backlog"        "board"   ""
create_view_if_missing "Priority board" "board"   ""
create_view_if_missing "Team items"     "table"   ""
create_view_if_missing "Roadmap"        "roadmap" ""
create_view_if_missing "My items"       "table"   "assignee:@me"
```

**Summary line** for Phase 7 report:

```
── Views ───────────────────────────────────────────────────────────
  ✓ exists:  Backlog (board)
  + created: Priority board (board)
  + created: Team items (table)
  + created: Roadmap (roadmap)
  + created: My items (table)
```
