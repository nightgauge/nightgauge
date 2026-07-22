# Estimation Guide

This document describes the estimation philosophy and practices for the
nightgauge repository.

## Overview

Nightgauge uses story points for velocity tracking without requiring manual
time logging. This approach measures relative complexity rather than time,
leading to more sustainable and predictable development.

### Why Story Points?

| Approach      | Pros                             | Cons                        |
| ------------- | -------------------------------- | --------------------------- |
| Story Points  | Measures complexity, no pressure | Requires calibration        |
| Hours         | Familiar, precise                | Encourages micro-management |
| T-shirt Sizes | Simple, quick                    | Can't sum for velocity      |

Story points are the best balance of simplicity and utility for tracking
velocity over time.

## Size-to-Points Mapping

Issues use T-shirt size labels that map to numeric story points using the
Fibonacci sequence:

| Size Label | Story Points | Typical Scope                  |
| ---------- | ------------ | ------------------------------ |
| XS         | 1            | Trivial change, < 1 hour       |
| S          | 2            | Small task, 1-4 hours          |
| M          | 3            | Medium task, 4-8 hours (1 day) |
| L          | 5            | Large task, 1-2 days           |
| XL         | 8            | Very large task, 2-5 days      |

### Why Fibonacci?

The Fibonacci sequence (1, 2, 3, 5, 8) is industry standard for story points:

- **Increasing gaps** reflect uncertainty in larger items
- **No false precision** - you can't estimate large work to the hour
- **Familiar to teams** - widely used in agile methodologies

### Using Size Labels vs. Estimate Field

| Field    | Type          | When to Use                        |
| -------- | ------------- | ---------------------------------- |
| Size     | Single Select | Quick T-shirt sizing during triage |
| Estimate | Number        | Precise story points for velocity  |

The Size label is set on issues for quick categorization. The Estimate field
should be set on project items for accurate velocity tracking.

## Estimation Best Practices

### Do

- **Estimate relative complexity**, not time
- **Use reference stories** for calibration (see examples below)
- **Re-estimate if scope changes** significantly
- **Estimate during planning**, not during development
- **Include all work** - coding, testing, documentation, review

### Don't

- Don't estimate in hours or days
- Don't pad estimates "just in case"
- Don't compare velocity across different teams
- Don't use velocity for individual performance measurement
- Don't estimate spikes (`type:spike`) in story points — time-box them instead

### Reference Stories

Use these as calibration points when estimating:

| Size | Reference Example                                  |
| ---- | -------------------------------------------------- |
| XS   | Fix a typo in documentation                        |
| S    | Add a new validation rule to existing code         |
| M    | Implement a new slash command with tests           |
| L    | Add a new pipeline skill with documentation        |
| XL   | Implement a new feature across multiple components |

## Using the Estimate Field

### When to Set Estimates

1. **During sprint planning** - All items entering a sprint should be estimated
2. **Before pickup** - Estimate before `/nightgauge-issue-pickup` if not already
   set
3. **After scope change** - Re-estimate if requirements change significantly

### How Estimates Flow to Charts

```
Issue created
    │
    ▼
Size label set (XS/S/M/L/XL)
    │
    ▼
Issue added to project
    │
    ▼
Estimate field set (story points)
    │
    ▼
Insights charts calculate velocity
```

The Estimate field is the authoritative source for velocity calculations in
GitHub Insights charts.

## Velocity Tracking

Velocity measures how many story points the team completes per sprint.

### Why Track Velocity?

- **Capacity planning** - Know how much work fits in a sprint
- **Predictability** - Forecast completion dates
- **Trend analysis** - Identify improvement or slowdown

### Reading the Velocity Chart

| Pattern        | Meaning                | Action                     |
| -------------- | ---------------------- | -------------------------- |
| Steady line    | Consistent velocity    | Continue current pace      |
| Upward trend   | Improving efficiency   | Team is maturing           |
| Downward trend | Slowdown               | Investigate causes         |
| High variance  | Unpredictable velocity | Improve estimation quality |

### Using Velocity for Planning

1. **Average the last 3 sprints** for capacity planning
2. **Don't commit to more than average velocity**
3. **Leave 20% buffer** for unexpected work and interruptions

### Sustainable Velocity

Velocity should remain consistent over time. If velocity increases temporarily
and then drops, the team may be accumulating technical debt. Aim for sustainable
throughput, not maximum throughput.

## Burn-up vs. Burndown Charts

Nightgauge uses burn-up charts because they reveal scope changes:

| Chart    | Shows                          | Best For               |
| -------- | ------------------------------ | ---------------------- |
| Burndown | Work remaining                 | Simple sprint progress |
| Burn-up  | Work completed AND total scope | Detecting scope creep  |

### Reading a Burn-up Chart

```
Points ▲
       │                    ┌─── Total Scope Line
       │              ╱─────┘    (rises when scope added)
       │            ╱
       │          ╱─────────────── Completed Line
       │        ╱                  (rises as work done)
       │      ╱
       │    ╱
       │  ╱
       │╱
       └─────────────────────────► Time
```

- **Gap between lines** = remaining work
- **Scope line rising** = scope creep detected
- **Lines converging** = on track for completion

## Cross-References

- [PROJECT_SETUP.md](./PROJECT_SETUP.md) - Field configuration and Insights
  setup
- [SPRINT_WORKFLOW.md](./SPRINT_WORKFLOW.md) - Sprint planning and execution

---

## Author

nightgauge
