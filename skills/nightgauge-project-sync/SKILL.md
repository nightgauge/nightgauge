---
name: nightgauge-project-sync
description: Bulk-sync existing repository issues to GitHub Project boards with proper
  field mappings. Use during onboarding or catch-up to sync dates from
  milestones and the Status field.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Bash Task AskUserQuestion
---

# Nightgauge Project Sync

## Description

Bulk-synchronize existing repository issues to GitHub Project boards with proper
field mappings. Designed for onboarding scenarios where a repository already has
many issues that need to be synced to the Nightgauge project board.

**Key Features:**

- Bulk date sync from milestones (milestone dates → project Start/Target Date
  fields) via `nightgauge project set-field --start-date/--target-date`
- Status field sync (for all issues at once)
- Dry-run report mode (preview changes without applying)
- Idempotent (safe to run multiple times)
- Clear output showing changes made

**Use Cases:**

- Onboarding existing repositories to Nightgauge
- Catch-up after manual issue creation
- Post-migration field synchronization
- Periodic maintenance to fix field drift

**When to Use:**

- After setting up `.nightgauge/config.yaml` for a new repository
- When bulk-creating issues outside the pipeline
- After milestone changes that affect multiple issues
- When the Status field gets out of sync with issue states

## Invocation

| Tool        | Command                                                  |
| ----------- | -------------------------------------------------------- |
| Claude Code | `/nightgauge:project-sync [options]`                     |
| Copilot     | Invoke via Agent Skills extension                        |
| Cursor      | Run via Agent Skills or direct SKILL.md                  |
| Standalone  | `claude --skill skills/nightgauge-project-sync/SKILL.md` |

## Arguments

### Core Options

| Argument           | Description                                                | Default |
| ------------------ | ---------------------------------------------------------- | ------- |
| `--mode MODE`      | Sync mode: full/dates-only/status-only/report              | `full`  |
| `--dry-run`        | Preview changes without applying (alias for --mode=report) | `false` |
| `--milestone NAME` | Filter by milestone name                                   | (all)   |
| `--label PATTERN`  | Filter by label pattern (can repeat)                       | (all)   |

### Sync Modes

| Mode          | Description                                        |
| ------------- | -------------------------------------------------- |
| `full`        | Sync both dates and status fields (default)        |
| `dates-only`  | Sync only Start/Target Date fields from milestones |
| `status-only` | Sync only Status field                             |
| `report`      | Dry-run mode - preview changes without applying    |

### Examples

```bash
# Dry run to preview all changes
/nightgauge:project-sync --dry-run

# Full sync of all open issues
/nightgauge:project-sync --mode full

# Sync only dates from milestones
/nightgauge:project-sync --mode dates-only

# Sync only status labels
/nightgauge:project-sync --mode status-only

# Filter by milestone
/nightgauge:project-sync --milestone "Sprint 23"

# Filter by label pattern
/nightgauge:project-sync --label "priority:high"

# Combine filters
/nightgauge:project-sync --milestone "v2.0" --label "type:feature"
```

## Philosophy

### Deterministic vs Probabilistic Split

This skill follows the Nightgauge architecture principle of using
deterministic operations where possible:

| Operation                 | Type          | Rationale                               |
| ------------------------- | ------------- | --------------------------------------- |
| Label→field mapping       | Deterministic | Fixed mapping, no interpretation needed |
| Milestone date extraction | Deterministic | Direct field access from GitHub API     |
| Issue fetching/pagination | Deterministic | GraphQL query with fixed parameters     |
| Field comparison          | Deterministic | Simple equality check                   |
| Progress display          | Probabilistic | AI formats output for readability       |
| Error messages            | Probabilistic | AI provides context-aware explanations  |

**Cost Efficiency**: All bulk operations handled by the Go binary
(`nightgauge project add`), minimizing LLM token consumption. SKILL.md only handles
user interaction and output formatting.

### Context Isolation

This is a **standalone utility skill**, not part of the main pipeline. It:

- Does NOT read pipeline context files (`.nightgauge/pipeline/*.json`)
- Does NOT write pipeline handoff files
- Does NOT affect pipeline state
- Can be run at any time without affecting pipeline execution

## Configuration

Configuration is read from `.nightgauge/config.yaml`:

```yaml
# Project board configuration
project:
  number: 1 # GitHub Project number
  id: "PVT_kwHOABC123" # Project global ID
  status_field_id: "PVTSSF_lAHOABC123" # Status field ID
  priority_field_id: "PVTSSF_lAHODEF456" # Priority field ID
  size_field_id: "PVTSSF_lAHOGHI789" # Size field ID
  start_date_field_id: "PVTF_lAHOJKL012" # Start Date field ID
  target_date_field_id: "PVTF_lAHOMNO345" # Target Date field ID

# Sync behavior (optional)
project_sync:
  # Issues without milestones will be skipped (not an error)
  skip_no_milestone: true

  # Idempotency - only update if value differs
  compare_before_update: true

  # Pagination
  page_size: 100
```

**Field ID Discovery**:

To get your project field IDs, run:

```bash
nightgauge forge graphql -f query='query($owner:String!,$number:Int!){organization(login:$owner){projectV2(number:$number){fields(first:50){nodes{... on ProjectV2FieldCommon{id,name,dataType}}}}}}' -f owner=<org> -F number=<number>
```

**Defaults**: If `.nightgauge/config.yaml` is missing or incomplete, the
skill will gracefully skip operations and report what's needed.

## Prerequisites

- **nightgauge binary** - Required for forge API access (`nightgauge forge`)
- **jq** - JSON parsing in shell scripts
- **Bash 4+** - Shell scripting
- **Project board configured** - `.nightgauge/config.yaml` must have
  `project.number` and field IDs
- **Milestone dates** - Issues must have milestones with due dates for date sync

## Workflow

### Phase 0: Configuration Check

<!-- include: ../_shared/PREFLIGHT.md -->

---

#### Step 0.1: Verify Prerequisites

```bash
# Check nightgauge binary installed and forge auth configured
if ! command -v nightgauge &> /dev/null; then
  echo "ERROR: nightgauge binary not installed"
  echo "See: docs/GO_BINARY.md"
  exit 1
fi

if ! nightgauge forge auth status &> /dev/null; then
  echo "ERROR: forge auth not configured"
  echo "Run: nightgauge forge auth login (or set GITHUB_TOKEN env var)"
  exit 1
fi

# Check jq installed
if ! command -v jq &> /dev/null; then
  echo "ERROR: jq not installed"
  echo "Install: brew install jq (macOS) or apt-get install jq (Linux)"
  exit 1
fi
```

#### Step 0.2: Load Configuration

```bash
# Check for .nightgauge/config.yaml
if [ ! -f .nightgauge/config.yaml ]; then
  echo "ERROR: No .nightgauge/config.yaml found"
  echo "Run /nightgauge:init to set up Nightgauge configuration"
  exit 1
fi

# Verify project configuration
PROJECT_NUMBER=$(yq eval '.project.number' .nightgauge/config.yaml 2>/dev/null)
if [ -z "$PROJECT_NUMBER" ]; then
  echo "ERROR: No project.number configured in .nightgauge/config.yaml"
  echo "Add: project.number: <your-project-number>"
  exit 1
fi

echo "✓ Configuration loaded"
echo "  Project: #$PROJECT_NUMBER"
```

#### Step 0.3: Parse Arguments

Parse command-line arguments into variables:

- `MODE` - full/dates-only/status-only/report
- `MILESTONE_FILTER` - Milestone name filter (optional)
- `LABEL_FILTERS` - Array of label patterns (optional)
- `DRY_RUN` - Boolean flag

---

### Phase 1: Mode Selection (If Not Provided)

If no `--mode` argument provided, use AskUserQuestion to select mode:

```json
{
  "questions": [
    {
      "question": "What would you like to sync?",
      "header": "Sync Mode",
      "multiSelect": false,
      "options": [
        {
          "label": "Preview changes (dry-run)",
          "description": "Show what would be synced without applying changes"
        },
        {
          "label": "Full sync (dates + status)",
          "description": "Sync both milestone dates and status labels"
        },
        {
          "label": "Dates only",
          "description": "Sync only Start/Target Date fields from milestones"
        },
        {
          "label": "Status only",
          "description": "Sync only Status field from status labels"
        }
      ]
    }
  ]
}
```

Map user selection to MODE variable.

---

### Phase 2: Filter Selection (Optional)

If user wants to filter issues, use AskUserQuestion:

```json
{
  "questions": [
    {
      "question": "Do you want to filter which issues to sync?",
      "header": "Filter",
      "multiSelect": false,
      "options": [
        {
          "label": "All open issues",
          "description": "Sync all open issues in the repository"
        },
        {
          "label": "By milestone",
          "description": "Sync only issues in a specific milestone"
        },
        {
          "label": "By labels",
          "description": "Sync only issues with specific labels"
        },
        {
          "label": "By milestone and labels",
          "description": "Combine both filters"
        }
      ]
    }
  ]
}
```

If milestone filter selected, fetch available milestones and ask user to choose:

```bash
# Get open milestones
nightgauge forge api repos/:owner/:repo/milestones --jq '.[].title'
```

If label filter selected, ask user for label pattern (can use wildcards or exact
match).

---

### Phase 3: Execute Sync

#### Step 3.1: Sync Issues via Go Binary

Sync each open issue to the project board using the Go binary in a loop. For
full sync, fetch all open issues and call `project sync-status` per issue:

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"

if [ "$DRY_RUN" = "true" ]; then
  echo "  [dry-run] Would bulk-add issues (milestone: ${MILESTONE_FILTER:-any}, labels: ${LABEL_FILTERS[*]:-any})"
  RESULT='{ "total_issues": 0, "synced": 0, "skipped": 0, "errors": 0, "dry_run": true }'
else
  # Build bulk add args
  BULK_ARGS=(--bulk --json)
  [ -n "$MILESTONE_FILTER" ] && BULK_ARGS+=(--milestone "$MILESTONE_FILTER")
  for label in "${LABEL_FILTERS[@]:-}"; do
    [ -n "$label" ] && BULK_ARGS+=(--label "$label")
  done

  BULK_OUTPUT=$("$BINARY" project add "${BULK_ARGS[@]}" 2>/dev/null)
  BULK_EXIT=$?

  SYNCED=$(echo "$BULK_OUTPUT" | jq -r '.added // 0')
  SKIPPED=$(echo "$BULK_OUTPUT" | jq -r '.skipped // 0')
  ERRORS=$(echo "$BULK_OUTPUT" | jq -r '.failed // 0')
  TOTAL=$(echo "$BULK_OUTPUT" | jq -r '.total // 0')

  RESULT=$(jq -n \
    --argjson total "$TOTAL" \
    --argjson synced "$SYNCED" \
    --argjson skipped "$SKIPPED" \
    --argjson errors "$ERRORS" \
    --argjson dry_run false \
    '{ total_issues: $total, synced: $synced, skipped: $skipped, errors: $errors, dry_run: $dry_run }')
fi
```

#### Step 3.2: Parse Hook Output

The hook script outputs JSON:

```json
{
  "total_issues": 42,
  "synced": 35,
  "skipped": 7,
  "errors": 0,
  "dry_run": false,
  "details": [
    {
      "issue": 123,
      "title": "Add user authentication",
      "changes": {
        "start_date": {
          "old": null,
          "new": "2026-02-01"
        },
        "target_date": {
          "old": null,
          "new": "2026-02-15"
        },
        "status": {
          "old": "Backlog",
          "new": "Ready"
        }
      },
      "skipped_reason": null
    },
    {
      "issue": 124,
      "title": "Fix login bug",
      "changes": {},
      "skipped_reason": "No milestone"
    }
  ]
}
```

Parse JSON using jq:

```bash
TOTAL=$(echo "$RESULT" | jq -r '.total_issues')
SYNCED=$(echo "$RESULT" | jq -r '.synced')
SKIPPED=$(echo "$RESULT" | jq -r '.skipped')
ERRORS=$(echo "$RESULT" | jq -r '.errors')
DRY_RUN_MODE=$(echo "$RESULT" | jq -r '.dry_run')
```

#### Step 3.3: Sync Date Fields from Milestones

For issues with milestone due dates, set the `Start date` and `Target date`
project board fields using the Go binary:

```bash
if [ "$MODE" = "full" ] || [ "$MODE" = "dates-only" ]; then
  # Fetch issues with milestones
  ISSUES_WITH_MILESTONES=$(nightgauge forge issue list --state open --json number,milestone \
    --limit 500 2>/dev/null | jq -r '.[] | select(.milestone != null) | "\(.number) \(.milestone.dueOn // empty)"')

  while IFS=' ' read -r issue_number due_date; do
    [ -z "$issue_number" ] || [ -z "$due_date" ] && continue
    # Use milestone due date as target date; start date is optional
    if [ "$DRY_RUN" = "true" ]; then
      echo "  [dry-run] Would set target-date=$due_date on #$issue_number"
    else
      "$BINARY" project set-field "$issue_number" --target-date "$due_date" 2>/dev/null || true
    fi
  done <<< "$ISSUES_WITH_MILESTONES"
fi
```

---

### Phase 4: Display Results

#### Step 4.1: Summary Output

Format summary based on mode:

```
┌─────────────────────────────────────────────────────────────────┐
│  PROJECT SYNC COMPLETE                                          │
└─────────────────────────────────────────────────────────────────┘

Mode: Full Sync (Dates + Status)
Dry Run: No

## Summary
✓ Synced: 35 issues
⊘ Skipped: 7 issues
✗ Errors: 0 issues
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Total: 42 issues processed
```

If dry-run mode:

```
┌─────────────────────────────────────────────────────────────────┐
│  PROJECT SYNC PREVIEW (DRY RUN)                                 │
└─────────────────────────────────────────────────────────────────┘

The following changes would be applied:

✓ 35 issues would be synced
⊘ 7 issues would be skipped

Run without --dry-run to apply these changes.
```

#### Step 4.2: Detailed Changes (Top 10)

Show details for up to 10 synced issues:

```markdown
## Recent Changes

### Issue #123: Add user authentication

- Start Date: null → 2026-02-01
- Target Date: null → 2026-02-15
- Status: Backlog → Ready

### Issue #125: Implement photo upload

- Status: Backlog → In progress

### Issue #127: Fix payment processing

- Target Date: 2026-02-10 → 2026-02-20
```

#### Step 4.3: Skipped Issues Summary

Show why issues were skipped:

```markdown
## Skipped Issues

7 issues were skipped:

- 4 issues: No milestone
- 2 issues: Not in project board
- 1 issue: Already up-to-date
```

#### Step 4.4: Error Summary (If Any)

If errors occurred:

```markdown
## Errors

2 issues failed to sync:

- Issue #130: API rate limit exceeded
- Issue #132: Invalid date format in milestone
```

---

### Phase 5: Recommendations

#### Step 5.1: Suggest Next Actions

Based on results, suggest next actions:

```
## Next Steps

✓ Project board is now synchronized

Recommendations:
- Run /nightgauge:issue-pickup to start working on ready issues
- Review skipped issues and add milestones if needed
- Set up automation to keep fields in sync:
  - Add project board sync hooks to .nightgauge/hooks/
```

If errors occurred:

```
## Action Required

Some issues failed to sync. Review errors above and:
1. Check API rate limits: nightgauge forge api rate_limit
2. Verify milestone dates are valid ISO format
3. Re-run sync after resolving issues
```

If dry-run mode:

```
## Next Steps

Review the changes above. If they look correct:
1. Run the same command without --dry-run
2. Or run: /nightgauge:project-sync --mode full
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

### Common Errors

| Error                     | Cause                             | Solution                                   |
| ------------------------- | --------------------------------- | ------------------------------------------ |
| `gh: command not found`   | GitHub CLI not installed          | Install from https://cli.github.com        |
| `forge auth status` fails | Forge auth not configured         | Run `nightgauge forge auth login`          |
| `403 rate limit`          | Too many API requests             | Wait 1 hour or use authenticated token     |
| `No project configured`   | Missing `.nightgauge/config.yaml` | Run `/nightgauge:init` first               |
| `Missing field IDs`       | Incomplete project config         | Add field IDs to `.nightgauge/config.yaml` |
| `No open issues found`    | Repository has no open issues     | Normal - no sync needed                    |

### Graceful Degradation

The skill handles missing configuration gracefully:

- **No project configured**: Skips sync, reports what's needed
- **No milestones**: Skips date sync, continues with status sync
- **Issue not in project**: Skips that issue, continues with others
- **Missing field IDs**: Skips affected fields, syncs what's possible

---

## Integration

### Standalone Utility

This skill is **NOT** part of the main Nightgauge pipeline. It:

- Does not require pipeline context files
- Can be run at any time
- Does not modify pipeline state
- Does not block or depend on pipeline stages

### Recommended Usage

| Scenario            | When to Run                                |
| ------------------- | ------------------------------------------ |
| Initial onboarding  | After setting up `.nightgauge/config.yaml` |
| Bulk issue creation | After importing issues from another system |
| Milestone changes   | After updating milestone due dates         |
| Label drift         | Weekly/monthly maintenance                 |
| Pre-sprint planning | Before sprint planning to ensure accuracy  |

### Integration with Other Skills

| Skill                       | Relationship                              |
| --------------------------- | ----------------------------------------- |
| `/nightgauge:init`          | Run before project-sync to set up config  |
| `/nightgauge:issue-pickup`  | Run after project-sync to start work      |
| `/nightgauge:backlog-groom` | Complementary - groom then sync           |
| Pipeline skills             | Independent - project-sync doesn't affect |

---

## Performance

### Expected Performance

| Repository Size | Sync Time     | Notes                              |
| --------------- | ------------- | ---------------------------------- |
| 10 issues       | < 10 seconds  | Single page, minimal API calls     |
| 50 issues       | 20-30 seconds | Single page                        |
| 100 issues      | 40-60 seconds | Multiple pages, idempotency checks |
| 500+ issues     | 3-5 minutes   | Heavy pagination, consider filters |

### Optimization Tips

1. **Use filters** - Narrow scope with `--milestone` or `--label`
2. **Incremental sync** - Run periodically instead of full sync
3. **Dates-only or status-only** - Use specific mode if only one field changed
4. **Off-peak hours** - Run during low API usage times if rate-limited

### API Rate Limits

GitHub API rate limits:

- **Authenticated**: 5000 requests/hour
- **GraphQL**: 5000 points/hour (query complexity-based)

This skill uses GraphQL pagination (100 issues/query) for efficiency. A
500-issue sync uses ~10 requests.

---

## Dependencies

- **nightgauge binary** - Required for forge issue fetching
- **jq** - JSON parsing and manipulation
- **yq** (optional) - YAML parsing for config
- **Bash 4+** - Shell scripting

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) -
AI-Augmented SDLC Framework.
