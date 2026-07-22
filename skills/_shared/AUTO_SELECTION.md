### Auto-Selection Algorithm

**PURPOSE**: Automatically select the highest priority issue using a 7-tier
algorithm. This is the DEFAULT behavior when no issue number is provided and
`-i` flag is not present.

#### Global Filters (Applied to ALL Tiers)

- REQUIRE: project board Status field = "Ready"
- EXCLUDE: `type:epic` label
- EXCLUDE: issues with open blockers (unless mode is `ignore`)

**FIRST MATCH WINS**: Stop at the first tier that returns an issue.

#### Priority Tiers

| Tier | Criteria                                         | Reason Template                                       |
| ---- | ------------------------------------------------ | ----------------------------------------------------- |
| 1    | `--assignee @me` + `priority:critical` or `high` | "Assigned to you with priority:critical/high (ready)" |
| 2    | Any + `priority:critical` or `high`              | "Has priority:critical/high label (ready)"            |
| 3    | `--assignee @me` + has milestone (sort by dueOn) | "Assigned to you in milestone: [title] (due [date])"  |
| 4    | Any + has milestone (sort by dueOn)              | "In milestone: [title] (soonest deadline, ready)"     |
| 5    | `--assignee @me` (any open)                      | "Assigned to you (ready)"                             |
| 6    | `priority:medium` label                          | "Has priority:medium label (ready)"                   |
| 7    | Oldest open issue (sort by createdAt)            | "Oldest open issue (ready)"                           |

#### Reference Filter Pattern

All tiers use this structure with tier-specific `gh issue list` flags. First
fetch candidates, then filter blocked issues via Go binary `hook check-deps`:

```bash
# Step 1: Get candidate issues (no dependency filtering at CLI level —
# gh CLI does not support blockedBy as a --json field)
CANDIDATES=$(gh project item-list "$PROJECT_NUMBER" --owner "$OWNER" --format json 2>/dev/null | \
  jq '[.items[] | select(.status == "Ready" and .type == "ISSUE" and .content.state == "OPEN")
    | { number: .content.number, title: .content.title, labels: (.content.labels // []) }
    | select(.labels | map(.name) | index("type:epic") | not)
  ]')

# Step 2: Filter out blocked issues using Go binary hook check-deps
# (queries GitHub's native blockedBy/blocking GraphQL API)
ISSUE=$(echo "$CANDIDATES" | jq -r '.[].number' | while read -r n; do
  BINARY=$(command -v nightgauge 2>/dev/null || echo "nightgauge")
  RESULT=$("$BINARY" hook check-deps "$n" --check-only 2>/dev/null || echo '{"has_open_dependencies":false}')
  HAS_DEPS=$(echo "$RESULT" | jq -r '.has_open_dependencies')
  if [ "$HAS_DEPS" = "false" ]; then
    echo "$CANDIDATES" | jq ".[] | select(.number == $n)" | head -1
    break
  fi
done)
```

**NOTE**: The `gh issue list --json` command does NOT support `blockedBy` as a
field. Blocking relationships must be queried via GraphQL (which
`nightgauge hook check-deps` handles). Do NOT use `trackedInIssues` — that is a
different GitHub feature (task list checkboxes) and does not represent blocking.

Tier-specific `[TIER_FLAGS]`:

- Tier 1: `--assignee @me --label "priority:critical"` (then fallback
  `--label "priority:high"`)
- Tier 2: `--label "priority:critical"` (then fallback
  `--label "priority:high"`)
- Tier 3: `--assignee @me` + jq `select(.milestone) | sort_by(.milestone.dueOn)`
- Tier 4: jq `select(.milestone) | sort_by(.milestone.dueOn)`
- Tier 5: `--assignee @me`
- Tier 6: `--label "priority:medium"`
- Tier 7: jq `sort_by(.createdAt)`

Each tier checks critical first, then high (for tiers 1-2). If `$ISSUE` is empty
or `"null"`, proceed to next tier.

#### Display Auto-Selected Issue

Present the auto-selected issue with reasoning:

```
Issue:  #42 - Add user profile photo upload
Type:   enhancement
Reason: Assigned to you with priority: high label
```

#### Confirm Selection

Confirm selection: offer Yes / No, show all issues / Cancel options.

- **Yes**: Proceed with selected issue
- **No, show all issues**: Fall back to Interactive Mode
- **Cancel**: Exit skill

#### Interactive Mode (Fallback or `-i` flag)

When `-i` flag is provided or user rejects auto-selection:

```bash
# List open issues (optionally filtered by --label)
gh issue list --state open --limit 15 --json number,title,labels,assignees \
  --jq '.[] | "#\(.number) - \(.title) [\(.labels | map(.name) | join(", "))]"'
```

Present list to user and let them select an issue.

#### No Issues Available

If no issues with "Ready" status on the project board exist:

```
No issues with Ready status found on the project board.

Options:
1. Create a new issue: /nightgauge-issue-create
2. Check all open issues: gh issue list --state open
3. Set an issue to Ready on the project board: `nightgauge project sync-status <number> ready`
```

#### All Issues Blocked

If all tiers are exhausted but blocked issues exist, find the least-blocked
option (fewest open dependencies):

```bash
# Get all ready issues, then check each for blocking relationships
CANDIDATES=$(gh project item-list "$PROJECT_NUMBER" --owner "$OWNER" --format json 2>/dev/null | \
  jq -r '[.items[] | select(.status == "Ready" and .type == "ISSUE" and .content.state == "OPEN")
    | .content.number] | .[]')

BLOCKED_ISSUES="[]"
for n in $CANDIDATES; do
  BINARY=$(command -v nightgauge 2>/dev/null || echo "nightgauge")
  RESULT=$("$BINARY" hook check-deps "$n" 2>/dev/null || echo '{"has_open_dependencies":false}')
  HAS_DEPS=$(echo "$RESULT" | jq -r '.has_open_dependencies')
  if [ "$HAS_DEPS" = "true" ]; then
    ENTRY=$(echo "$RESULT" | jq '{
      number: .issue_number,
      blockers: .open_dependencies,
      blocker_count: .open_count
    }')
    BLOCKED_ISSUES=$(echo "$BLOCKED_ISSUES" "[$ENTRY]" | jq -s 'add | sort_by(.blocker_count)')
  fi
done

LEAST_BLOCKED=$(echo "$BLOCKED_ISSUES" | jq '.[0]')
```

Display the least-blocked issue with its blockers. Offer options: Pick up
blocker instead (recommended) / Pick up anyway / Show all blocked / Cancel.
