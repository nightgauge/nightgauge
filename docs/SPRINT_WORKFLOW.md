# Sprint Workflow

This document describes how to set up and use sprint/iteration support with
Nightgauge.

## Overview

Sprint support integrates GitHub Project's native iteration field with the
Nightgauge pipeline. When enabled, issues are automatically assigned to the current
sprint iteration during `/nightgauge-issue-pickup`.

## Quick Start

### 1. Create an Iteration Field in GitHub Project

1. Go to your GitHub Project (e.g., `/orgs/nightgauge/projects/10`)
2. Click **+** to add a new field
3. Select **Iteration** as the field type
4. Name the field "Sprint" (or your preferred name)
5. Configure iteration duration (recommended: 2 weeks)

### 2. Enable Sprint Support in .nightgauge/config.yaml

```yaml
project:
  number: 10
  sprint:
    enabled: true
    auto_assign: true
    field_name: "Sprint" # Must match the field name from step 1
```

### 3. Use the Pipeline

When you run `/nightgauge-issue-pickup`, the issue will automatically be assigned
to the current sprint iteration.

## GitHub Project Setup

### Creating an Iteration Field

Iteration fields support:

- **Configurable duration** - Days or weeks per iteration
- **Flexible start dates** - Iterations can start on any day
- **Breaks** - Add gaps between iterations (holidays, planning)
- **@current/@next** - Special references for automation

**Example iteration setup:**

| Sprint   | Start Date | Duration | Notes    |
| -------- | ---------- | -------- | -------- |
| Sprint 1 | 2026-01-13 | 2 weeks  |          |
| Sprint 2 | 2026-01-27 | 2 weeks  |          |
| Sprint 3 | 2026-02-10 | 2 weeks  |          |
| (break)  | 2026-02-24 | 1 week   | Planning |
| Sprint 4 | 2026-03-03 | 2 weeks  |          |

### Setting Up Views

Create a "Sprint" view to focus on current work:

1. Click **New view** → **Board** or **Table**
2. Add filter: `Sprint @current`
3. Group by **Status** for board view
4. Save the view

This shows only issues assigned to the current sprint, filtering out backlog and
future work.

### Recommended Views

| View Name       | Filter                   | Purpose                      |
| --------------- | ------------------------ | ---------------------------- |
| Sprint Board    | `Sprint @current`        | Current sprint work          |
| Sprint Backlog  | `Sprint @next`           | Next sprint planning         |
| Unplanned       | `no:Sprint Status:Ready` | Issues ready but not planned |
| All Active Work | `Sprint @current @next`  | Current and next sprint      |

## Configuration Reference

### .nightgauge/config.yaml Options

```yaml
project:
  number: 10 # Required: GitHub Project number
  sprint:
    enabled: true # Enable iteration field integration
    auto_assign: true # Auto-assign @current iteration on issue-pickup
    field_name: "Sprint" # Name of iteration field (default: "Sprint")
```

### Environment Variable Overrides

| Config Key                   | Environment Variable                    | Default    |
| ---------------------------- | --------------------------------------- | ---------- |
| `project.sprint.enabled`     | `NIGHTGAUGE_PROJECT_SPRINT_ENABLED`     | `false`    |
| `project.sprint.auto_assign` | `NIGHTGAUGE_PROJECT_SPRINT_AUTO_ASSIGN` | `false`    |
| `project.sprint.field_name`  | `NIGHTGAUGE_PROJECT_SPRINT_FIELD_NAME`  | `"Sprint"` |

## Nightgauge Integration

### How It Works

When `/nightgauge-issue-pickup` runs with sprint auto-assign enabled:

1. **Check Configuration** - Verify `project.sprint.auto_assign: true`
2. **Find Iteration Field** - Query project for field matching `field_name`
3. **Get Current Iteration** - Find the iteration containing today's date
4. **Assign Iteration** - Set the iteration field on the project item

### Deterministic Hook Script

Sprint assignment uses `sync-project-iteration.sh`, a deterministic shell
script:

```bash
# Assign current iteration (default)
./claude-plugins/nightgauge/hooks/lib/sync-project-iteration.sh 90

# Assign next iteration
./claude-plugins/nightgauge/hooks/lib/sync-project-iteration.sh 90 @next

# Clear iteration
./claude-plugins/nightgauge/hooks/lib/sync-project-iteration.sh 90 none
```

**Why Deterministic?**

- Fixed input→output mapping: `@current` → current iteration ID
- No interpretation needed
- Predictable, testable behavior
- Zero LLM tokens consumed

See
[docs/ARCHITECTURE.md](./ARCHITECTURE.md#deterministic-vs-probabilistic-architecture)
for more on the deterministic vs probabilistic principle.

### Manual Iteration Assignment

You can manually assign iterations using the `gh` CLI:

```bash
# Find the iteration field ID
gh project field-list 10 --owner nightgauge --format json | jq '.fields[] | select(.type == "ITERATION")'

# List iterations
gh api graphql -f query='
  query($projectId: ID!) {
    node(id: $projectId) {
      ... on ProjectV2 {
        field(name: "Sprint") {
          ... on ProjectV2IterationField {
            configuration {
              iterations { id title startDate duration }
            }
          }
        }
      }
    }
  }
' -f projectId="PROJECT_GLOBAL_ID"

# Set iteration on an item
gh project item-edit --project-id PROJECT_ID --id ITEM_ID --field-id FIELD_ID --iteration-id ITERATION_ID
```

## Sprint Planning Workflow

### Recommended Process

1. **Sprint Planning** (before sprint starts)
   - Review unplanned items: `no:Sprint Status:Ready`
   - Assign items to @next iteration
   - Prioritize the sprint backlog

2. **Sprint Execution**
   - Use `/nightgauge-issue-pickup` to claim issues
   - Issues auto-assigned to @current sprint
   - Track progress in Sprint Board view

3. **Sprint Review** (end of sprint)
   - Review completed work in Done column
   - Move incomplete items to next sprint or backlog

4. **Iteration Rollover**
   - GitHub automatically advances @current/@next
   - No manual intervention needed

### Best Practices

- **Don't over-commit** - Leave buffer for unexpected work
- **Size appropriately** - Use story points or T-shirt sizes
- **Track velocity** - Compare planned vs completed each sprint
- **Plan one sprint ahead** - Keep @next populated

## Velocity Tracking

Velocity measures how many story points the team completes per sprint. Tracking
velocity helps with capacity planning and predictability.

### Reading the Velocity Chart

The velocity chart (configured in Insights) shows points completed per iteration
over time.

| Pattern        | Meaning                | Action                         |
| -------------- | ---------------------- | ------------------------------ |
| Steady line    | Consistent velocity    | Continue current pace          |
| Upward trend   | Improving efficiency   | Team is maturing               |
| Downward trend | Slowdown               | Investigate causes             |
| High variance  | Unpredictable velocity | Improve estimation consistency |

### Using Velocity for Planning

1. **Average the last 3 sprints** for capacity planning
2. **Don't commit to more than average velocity**
3. **Leave 20% buffer** for unexpected work and interruptions

### Reading the Burn-up Chart

The burn-up chart shows both completed work and total scope over time:

| Signal            | Meaning                 | Action                   |
| ----------------- | ----------------------- | ------------------------ |
| Lines converging  | On track for completion | Continue as planned      |
| Gap increasing    | Falling behind          | Review scope or velocity |
| Scope line rising | Scope creep detected    | Address with stakeholder |
| Scope line flat   | Scope stable            | Good discipline          |

For detailed estimation guidance, see [ESTIMATION.md](./ESTIMATION.md).

## Troubleshooting

### "Iteration field not found"

**Problem:** Script reports no iteration field found.

**Solution:**

1. Verify the field name matches exactly (case-sensitive)
2. Check the field type is "Iteration" (not "Single select")
3. Verify `.nightgauge/config.yaml` has correct `field_name`

```bash
# List all fields in project
gh project field-list 10 --owner nightgauge --format json | jq '.fields[] | {name, type}'
```

### "No current iteration found"

**Problem:** Script cannot find @current iteration.

**Solution:**

1. Ensure at least one iteration covers today's date
2. Check iteration start date and duration
3. Create or extend iterations to include today

```bash
# View iteration dates
gh api graphql -f query='...' | jq '.data.node.field.configuration.iterations'
```

### "Issue not in project"

**Problem:** Script skips because issue isn't in project board.

**Solution:**

1. Add the issue to the project manually, or
2. Run `/nightgauge-issue-pickup` which adds issues automatically via
   `add-to-project.sh`

### Sprint auto-assign not working

**Checklist:**

- [ ] `project.sprint.enabled: true` in `.nightgauge/config.yaml`
- [ ] `project.sprint.auto_assign: true` in `.nightgauge/config.yaml`
- [ ] Iteration field exists with correct name
- [ ] At least one iteration covers today's date
- [ ] Issue is in the GitHub Project board
- [ ] Not skipped via `NIGHTGAUGE_SKIP_PROJECT=1`

Enable debug logging:

```bash
NIGHTGAUGE_HOOKS_DEBUG=1 ./claude-plugins/nightgauge/hooks/lib/sync-project-iteration.sh 90
```

## Edge Cases

| Scenario                  | Behavior                                   |
| ------------------------- | ------------------------------------------ |
| No iteration field exists | Skipped gracefully, issue pickup continues |
| No current iteration      | Skipped gracefully, logs reason            |
| Issue not in project      | Skipped gracefully, issue pickup continues |
| Multiple iteration fields | Uses first field matching `field_name`     |
| Sprint feature disabled   | Skipped, no API calls made                 |
| Already has iteration set | Overwrites with current iteration          |

## Security Considerations

Per [standards/security.md](../standards/security.md):

- No secrets stored in configuration
- All API calls use existing `gh` authentication
- Input validation on issue numbers
- Graceful failure if iteration field doesn't exist
- No sensitive data logged

## Author

nightgauge
