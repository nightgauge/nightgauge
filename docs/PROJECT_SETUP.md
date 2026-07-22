# GitHub Project Setup

This document describes the GitHub Project configuration for the nightgauge
repository and how it integrates with the Nightgauge pipeline.

## Quick Start: Auto-Generate Configuration

The fastest way to configure Nightgauge for a new repository is to use the
auto-generation script. This queries the GitHub Project API and generates all
required configuration files automatically.

```bash
# Auto-generate configuration (recommended)
./scripts/init-nightgauge-config.sh --project <number>

# Example: For project #10 in nightgauge organization
./scripts/init-nightgauge-config.sh --project 10 --owner nightgauge
```

**What it generates:**

| File                      | Purpose                                                          |
| ------------------------- | ---------------------------------------------------------------- |
| `.nightgauge/config.yaml` | Main configuration with project IDs and field option ID mappings |

**Options:**

| Option      | Description                              |
| ----------- | ---------------------------------------- |
| `--force`   | Overwrite existing configuration         |
| `--merge`   | Merge with existing config (default)     |
| `--dry-run` | Preview changes without writing files    |
| `--json`    | Output JSON summary (for CI integration) |

**Time savings:** ~15 minutes → <2 minutes per repository.

After generation, verify the configuration:

```bash
./scripts/validate-project-config.sh --verify-with-api
```

---

## Overview

| Property    | Value                                                |
| ----------- | ---------------------------------------------------- |
| Project     | Nightgauge (#10)                                     |
| URL         | https://github.com/orgs/nightgauge/projects/10       |
| Purpose     | Track all issues through the development pipeline    |
| Integration | Nightgauge pipeline skills auto-update project board |

## Views

The project uses a minimal set of views optimized for solo or small team
development.

### Board (Primary)

| Property | Value                                            |
| -------- | ------------------------------------------------ |
| Layout   | Board (Kanban)                                   |
| Purpose  | Primary work queue organized by status           |
| Columns  | Backlog → Ready → In progress → In review → Done |
| Usage    | Daily standup, work-in-progress tracking         |

This is the default view for day-to-day development work. Items flow left to
right as they progress through the pipeline.

### Roadmap

| Property | Value                               |
| -------- | ----------------------------------- |
| Layout   | Roadmap (Timeline)                  |
| Purpose  | Timeline visualization by milestone |
| Usage    | Release planning, deadline tracking |

Use this view for understanding the overall project timeline and milestone
progress. Items are positioned based on their Start date and Target date fields.

### My Items

| Property | Value                              |
| -------- | ---------------------------------- |
| Layout   | Table                              |
| Purpose  | Personal dashboard filtered to you |
| Filter   | `assignee:@me`                     |
| Usage    | Quick access to your assigned work |

A filtered view showing only items assigned to the current user. Useful for
focusing on your personal work queue.

### Sprint

| Property | Value                       |
| -------- | --------------------------- |
| Layout   | Board                       |
| Purpose  | Current iteration work only |
| Filter   | `Sprint:@current`           |

This view shows only issues assigned to the current sprint, filtering out
backlog and future work. See [SPRINT_WORKFLOW.md](./SPRINT_WORKFLOW.md) for
sprint planning guidance.

## Fields

### Built-in Fields

| Field               | Type       | Purpose                               |
| ------------------- | ---------- | ------------------------------------- |
| Title               | Title      | Issue/PR title                        |
| Assignees           | Assignees  | Who is working on this                |
| Labels              | Labels     | Categorization (type, priority, etc.) |
| Linked PRs          | Links      | Associated pull requests              |
| Milestone           | Milestone  | Release grouping                      |
| Repository          | Repository | Source repository                     |
| Reviewers           | Reviewers  | PR reviewers                          |
| Parent issue        | Parent     | Epic/parent issue link                |
| Sub-issues progress | Progress   | Completion % of sub-issues            |

### Custom Fields

| Field       | Type          | Options                                      | Purpose                 |
| ----------- | ------------- | -------------------------------------------- | ----------------------- |
| Status      | Single Select | Backlog, Ready, In progress, In review, Done | Workflow state          |
| Priority    | Single Select | P0, P1, P2, P3                               | Urgency ranking         |
| Size        | Single Select | XS, S, M, L, XL                              | Effort estimation       |
| Estimate    | Number        | Story points                                 | Numeric effort estimate |
| Start date  | Date          | -                                            | When work began         |
| Target date | Date          | -                                            | Expected completion     |

### Iteration Field

| Field  | Type      | Purpose                   | Status     |
| ------ | --------- | ------------------------- | ---------- |
| Sprint | Iteration | Sprint/iteration tracking | Configured |

The Sprint (Iteration) field enables the Sprint view and velocity tracking in
Insights. See [SPRINT_WORKFLOW.md](./SPRINT_WORKFLOW.md) for configuration
details.

## Built-in Workflows

GitHub Projects v2 built-in workflows handle common status transitions
automatically, without scripts or GitHub Actions.

### Enabled Workflows (Project #1 — Nightgauge)

| Workflow                       | Trigger                              | Action                    |
| ------------------------------ | ------------------------------------ | ------------------------- |
| Auto-add to project            | New issue opened in repo             | Adds to project board     |
| Auto-add sub-issues to project | Sub-issue linked to tracked epic     | Adds sub-issue to board   |
| Item added to project          | Item first appears on board          | Sets initial Status       |
| Item closed                    | Issue/PR closed                      | Sets Status → Done        |
| Pull request merged            | PR merged                            | Sets Status → Done        |
| Auto-close issue               | Status manually set to Done on board | Closes the GitHub issue   |
| Auto-archive items             | Item in Done for 14 days             | Archives from board       |
| Pull request linked to issue   | PR linked to issue                   | Tracks linked PR on board |

### Limitations

- **No "Item reopened" workflow** — GitHub does not offer a built-in workflow to
  reset status when an issue is reopened. The pipeline handles this via scripts.
- **Status field only** — Built-in workflows can only set the Status field, not
  Priority or Size. Those are set directly as board fields at issue creation.
- **Coexistence** — Built-in workflows and pipeline scripts can coexist safely.
  Scripts that set Status to "Done" are idempotent with the built-in close→Done
  workflow.

### Also Enabled on Project #2 (Nightgauge Platform)

The same set of workflows is enabled on the platform project board.

## Pipeline Integration

The Nightgauge pipeline skills automatically interact with this project:

### /nightgauge:issue-pickup

- Moves issue to "In progress" status when picked up
- Assigns issue to the developer

### /nightgauge:pr-create

- Links PR to issue
- Updates status to "In review" when PR is created

### /nightgauge:pr-merge

- Updates status to "Done" after merge
- Cleans up context files

### Automation

Issues are automatically added to the project via the **Auto-add to project**
built-in workflow (see [Built-in Workflows](#built-in-workflows) above).
Priority and Size fields are set directly via GraphQL at issue creation (by
`create-sub-issue.sh` or the issue-create workflow).

## Priority and Size Fields

Priority and Size are project board fields — not labels. They are set directly
via GraphQL at issue creation by `create-sub-issue.sh` or the issue-create
workflow.

**Labels are for classification** (`type:*`, `component:*`). **Board fields are
for project management** (Priority, Size, Status).

The `add-to-project.sh` script adds issues to the project board but does not map
labels to Priority/Size fields. The `issue-pickup` skill reads Priority/Size
from board fields first, with a label fallback for legacy issues only.

## Insights

The project uses GitHub Insights for velocity and progress tracking.

### Configured Charts

| Chart               | Type    | Purpose                               | Field    |
| ------------------- | ------- | ------------------------------------- | -------- |
| Burn-up by Sprint   | Burn-up | Track progress toward sprint goal     | Estimate |
| Velocity Trend      | Line    | Points completed per sprint over time | Estimate |
| Status Distribution | Pie     | Work breakdown by status              | Status   |

### Configuring Insights Charts

These charts should be configured manually in the GitHub Project UI.

#### Burn-up Chart

1. Go to Project → Insights → New chart
2. Select "Burn up" chart type
3. X-axis: Sprint (iteration field)
4. Y-axis: Sum of Estimate field
5. Group by: Status (for completed vs remaining)
6. Name: "Burn-up by Sprint"
7. Save chart

#### Velocity Chart

1. Go to Project → Insights → New chart
2. Select "Line" chart type
3. X-axis: Sprint (iteration field)
4. Y-axis: Sum of Estimate field
5. Filter: Status = Done
6. Name: "Velocity Trend"
7. Save chart

#### Status Distribution

1. Go to Project → Insights → New chart
2. Select "Pie" chart type
3. Field: Status
4. Name: "Status Distribution"
5. Save chart

### Interpreting Charts

See [ESTIMATION.md](./ESTIMATION.md) for detailed guidance on:

- Reading velocity trends
- Using burn-up charts to detect scope creep
- Capacity planning with velocity data

## Multi-Project Configuration

For organizations that need to sync issues to multiple GitHub Projects (e.g.,
engineering board, QA board, and leadership dashboard), Nightgauge supports
a `projects:` array configuration.

### When to Use Multi-Project Mode

| Scenario                       | Use Multi-Project? |
| ------------------------------ | ------------------ |
| Single team, single board      | No                 |
| Multiple teams, shared issues  | Yes                |
| Different views for leadership | Yes                |
| QA and engineering boards      | Yes                |

### Configuration

Add a `projects:` array to `.nightgauge/config.yaml`:

```yaml
projects:
  # Engineering team board - all feature work
  - name: "Engineering Board"
    number: 10
    sync_filter: "type:feature OR type:bug"
    default: true

  # QA team board - bugs and testing items
  - name: "QA Board"
    number: 15
    sync_filter: "type:bug OR needs-qa"

  # Leadership review - high priority only
  - name: "Leadership Review"
    number: 20
    sync_filter: "priority:critical OR priority:high"
```

### sync_filter Expressions

The `sync_filter` field uses boolean expressions to control which issues sync:

| Expression                                     | Meaning                            |
| ---------------------------------------------- | ---------------------------------- |
| `type:feature OR type:bug`                     | Features and bugs                  |
| `priority:high AND needs-review`               | High priority items needing review |
| `NOT status:done`                              | Exclude completed items            |
| `(type:feature OR type:bug) AND priority:high` | High priority features/bugs        |

### Default Project

One project must be marked `default: true` for reverse sync (project fields →
labels). When you change a field in the default project, it syncs back to issue
labels. Non-default projects receive label changes but don't trigger reverse
sync.

### Dashboard Integration

When multi-project mode is active, the VSCode dashboard shows:

- **Project selector dropdown** - Switch between individual projects
- **Aggregate view** - Combined counts across all projects
- **Project-specific issue lists** - Filtered by the selected project

### Backward Compatibility

Existing single `project:` configurations continue to work unchanged. When both
`project:` and `projects:` are present, the `projects:` array takes precedence.

See [CONFIGURATION.md](./CONFIGURATION.md#projects-multi-project-mode) for the
complete schema reference.

---

## Best Practices

1. **Keep Status Updated**: Move items through columns as work progresses
2. **Set Dates**: Use Start date and Target date for roadmap accuracy
3. **Size Early**: Estimate size during planning, not during development
4. **Use My Items**: Start your day by checking your personal view
5. **Link PRs**: Always link PRs to issues for automatic status updates

## Removed Views

The following views were removed to reduce clutter:

| View           | Reason                                |
| -------------- | ------------------------------------- |
| Priority board | Redundant - use filters on Board view |
| Team items     | Not needed for solo development       |

These can be recreated if team size increases.

---

## Author

nightgauge
