# Component Label Selection & Label Setup (Phases 2 and 3)

Procedural detail for Phase 2 (Component Label Selection) and Phase 3 (Label
Setup).

## Contents

- [Phase 2: Component Label Selection](#phase-2-component-label-selection)
- [Phase 3: Label Setup](#phase-3-label-setup)

---

## Phase 2: Component Label Selection

Ask which component label set to create. The component labels are the only
project-specific group — all others (type, priority, size, status) are fixed.

```json
{
  "questions": [
    {
      "question": "Which component labels should be created for this repository?",
      "header": "Components",
      "multiSelect": false,
      "options": [
        {
          "label": "Web/API service (Recommended)",
          "description": "component:api, component:auth, component:ui, component:db, component:infra"
        },
        {
          "label": "CLI/Library",
          "description": "component:core, component:cli, component:sdk, component:docs, component:infra"
        },
        {
          "label": "Platform/SaaS",
          "description": "component:api, component:billing, component:auth, component:analytics, component:infra, component:sdk"
        },
        {
          "label": "VS Code Extension",
          "description": "component:extension, component:sdk, component:skills, component:docs, component:infra"
        },
        {
          "label": "Flutter/Mobile",
          "description": "component:app, component:api, component:auth, component:notifications, component:infra"
        }
      ]
    }
  ]
}
```

User may select "Other" to type a custom comma-separated list:
`component:foo,component:bar,component:baz`

Store the selected component definitions (name + description + color) for
Phase 3.

**Standard component colors** (apply to all presets unless custom):

| Component           | Color   | Description                         |
| ------------------- | ------- | ----------------------------------- |
| component:api       | #1d76db | API / tRPC / REST layer             |
| component:auth      | #5319e7 | Authentication and authorization    |
| component:ui        | #e99695 | Frontend / UI components            |
| component:db        | #006b75 | Database schema and migrations      |
| component:infra     | #cfd3d7 | Infrastructure and deployment       |
| component:core      | #0075ca | Core library functionality          |
| component:cli       | #fbca04 | CLI interface and commands          |
| component:sdk       | #a2eeef | SDK / programmatic interface        |
| component:docs      | #0075ca | Documentation only                  |
| component:billing   | #e4e669 | Billing and subscription management |
| component:analytics | #fbca04 | Analytics and telemetry             |
| component:extension | #7057ff | VS Code extension                   |
| component:skills    | #8957e5 | Pipeline skills and automation      |

For custom labels provided via "Other", use color `#c2e0c6` (default neutral).

---

## Phase 3: Label Setup

Create all standard labels plus the selected component labels. Skip any that
already exist.

### Standard Labels (Fixed — Same for Every Nightgauge Repo)

```bash
# Determine --owner-type flag for label commands (Go binary default is "org")
LABEL_OWNER_TYPE_FLAG="org"
if [ "$OWNER_TYPE" = "User" ]; then
  LABEL_OWNER_TYPE_FLAG="user"
fi

# Fetch all labels once and cache — avoids one API call per label
CACHED_LABELS_JSON=$(nightgauge label list \
  --owner "$OWNER" --repo "$REPO_NAME" \
  --owner-type "$LABEL_OWNER_TYPE_FLAG" --json 2>/dev/null || echo "[]")

create_label() {
  local name="$1" color="$2" description="$3"
  # Check cached list — avoids repeated API calls
  if echo "$CACHED_LABELS_JSON" | jq -r '.[].name' | grep -qx "$name"; then
    echo "  ✓ exists: $name"
  elif [ "$DRY_RUN" = "true" ]; then
    echo "  [DRY RUN] would create: $name"
  else
    if nightgauge label create \
      --name "$name" \
      --color "$color" \
      --description "$description" \
      --owner "$OWNER" --repo "$REPO_NAME" \
      --owner-type "$LABEL_OWNER_TYPE_FLAG" 2>&1; then
      echo "  + created: $name"
    else
      echo "  ✗ FAILED:  $name — check token scopes (repo write required)"
    fi
  fi
}

# ── Type labels ──────────────────────────────────────────────────────────────
create_label "type:bug"      "d73a4a" "Something broken"
create_label "type:docs"     "0075ca" "Documentation only"
create_label "type:chore"    "cfd3d7" "Maintenance task"
create_label "type:feature"  "1d76db" "New functionality"
create_label "type:refactor" "fbca04" "Code improvement"
create_label "type:epic"     "8957e5" "Parent issue with sub-issues"
create_label "type:spike"    "c2e0c6" "Research/investigation task"

# ── Pipeline labels ───────────────────────────────────────────────────────────
create_label "pipeline:refined" "0969da" "Issue has been refined and is ready for development"
create_label "auto-process"     "8957e5" "Issue is queued for automatic pipeline processing"

# NOTE: Priority and Size are NOT created as labels.
# They are set directly as project board fields (single-select) via GraphQL
# at issue creation by the Go binary (nightgauge issue create-sub) or the issue-create workflow.
# Labels are for classification (type:*, component:*) only.

```

### Component Labels (From Phase 2 Selection)

Apply the same `create_label` function to the selected component set.

### Dry-Run Behavior

If `--dry-run` is set, the `create_label` function prints what would be
created/skipped without calling `nightgauge label create`. Output format:

```
[DRY RUN] would create: type:bug
  ✓ exists: documentation
```
