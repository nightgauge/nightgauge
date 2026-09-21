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

**Pull the board ONCE, then run every tier against that one payload.**

The board read is the expensive call in this skill: a ProjectV2 page costs
17 GraphQL points against an hourly budget of 5000, and the previous version
of this section put a raw GitHub-CLI `project item-list` inside the tiers
with no `--limit`. Seven tiers, two of them retrying critical-then-high, is
up to fourteen whole-board pulls for one pickup — and a raw `gh` call is
invisible to `nightgauge api-usage`, so that spend never appeared in any
report. Read it once into a file; the tiers are `jq` filters over that file
and cost nothing.

```bash
BINARY=$(command -v nightgauge 2>/dev/null || echo "nightgauge")

# Step 1: one board read, cached and ledgered, for the whole selection.
BOARD=$(mktemp -t ng-board.XXXXXX.json)
"$BINARY" board list --status Ready --json > "$BOARD" || exit 1

# Every tier filters THIS file. Never re-read the board inside the loop.
CANDIDATES=$(jq '[.[] | select(.type == "ISSUE" and .state == "OPEN")
  | { number, title, labels, assignees, milestone, createdAt }
  | select(.labels | index("type:epic") | not)
]' "$BOARD")
```

Apply the tier's own filter to `$CANDIDATES` with `jq` — see the tier table
above for what each one selects — and stop at the first tier that yields a
row. Then, and only then, check blockers on the survivors:

```bash
# Step 2: blocker check, on the selected tier's candidates only.
# (blockedBy is not a --json field on any CLI; check-deps reads the native
# GraphQL relationship. It is one call per issue, so it runs on the tier
# that matched, never on all seven.)
ISSUE=$(printf '%s\n' "$TIER_CANDIDATES" | jq -r '.[].number' | while read -r n; do
  RESULT=$("$BINARY" hook check-deps "$n" --check-only 2>/dev/null || echo '{"has_open_dependencies":false}')
  if [ "$(printf '%s\n' "$RESULT" | jq -r '.has_open_dependencies')" = "false" ]; then
    printf '%s\n' "$TIER_CANDIDATES" | jq ".[] | select(.number == $n)" | head -1
    break
  fi
done)

rm -f "$BOARD"
```

**NOTE**: No CLI `issue list --json` surface supports `blockedBy` as a
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
nightgauge issue list --state open --limit 15 --json \
  --jq '.[] | "#\(.number) - \(.title) [\(.labels | map(.name) | join(", "))]"'
```

Present list to user and let them select an issue.

#### No Issues Available

If no issues with "Ready" status on the project board exist:

```
No issues with Ready status found on the project board.

Options:
1. Create a new issue: /nightgauge-issue-create
2. Check all open issues: nightgauge issue list --state open
3. Set an issue to Ready on the project board: `nightgauge project sync-status <number> ready`
```

#### All Issues Blocked

If all tiers are exhausted but blocked issues exist, find the least-blocked
option (fewest open dependencies):

```bash
# Reuse the ONE board payload already read in Step 1 — do not read it again.
CANDIDATES=$(printf '%s\n' "$CANDIDATES" | jq -r '.[].number')

BLOCKED_ISSUES="[]"
for n in $CANDIDATES; do
  BINARY=$(command -v nightgauge 2>/dev/null || echo "nightgauge")
  RESULT=$("$BINARY" hook check-deps "$n" 2>/dev/null || echo '{"has_open_dependencies":false}')
  HAS_DEPS=$(printf '%s\n' "$RESULT" | jq -r '.has_open_dependencies')
  if [ "$HAS_DEPS" = "true" ]; then
    ENTRY=$(printf '%s\n' "$RESULT" | jq '{
      number: .issue_number,
      blockers: .open_dependencies,
      blocker_count: .open_count
    }')
    BLOCKED_ISSUES=$(printf '%s\n' "$BLOCKED_ISSUES" "[$ENTRY]" | jq -s 'add | sort_by(.blocker_count)')
  fi
done

LEAST_BLOCKED=$(printf '%s\n' "$BLOCKED_ISSUES" | jq '.[0]')
```

Display the least-blocked issue with its blockers. Offer options: Pick up
blocker instead (recommended) / Pick up anyway / Show all blocked / Cancel.
