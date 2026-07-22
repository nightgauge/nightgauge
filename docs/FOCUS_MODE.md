# Focus Mode

> Steer autonomous enhancement, release-watch scoring, and continuous-improvement
> proposals toward a specific quality dimension — without changing a single config
> file by hand.

## Overview

Focus mode lets you tell Nightgauge _what matters most right now_. When a
focus lens is active, the pipeline boosts issues, proposals, and release-feature
scores that align with that lens. Unrelated work is still processed — it just
sorts lower.

The active lens is persisted in `.nightgauge/focus.yaml` and read by every
component that participates in prioritization:

| Component                | How focus is applied                                    |
| ------------------------ | ------------------------------------------------------- |
| Autonomous scheduler     | Issues matching lens keywords sort higher in each cycle |
| Release-watch assessment | Matching Claude Code features receive a score boost     |
| Continuous-improvement   | Matching proposals are promoted; marked `★` in output   |
| CLI / VSCode             | Set, clear, and inspect the active lens                 |

---

## Quick Start

```bash
# Show current focus
nightgauge focus show

# Set focus to security improvements
nightgauge focus set security

# Reset to balanced (no bias)
nightgauge focus clear
```

---

## Built-in Lenses

| Lens            | Description                                                             | Score boosts                                                                    |
| --------------- | ----------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `general`       | Balanced — no specific bias (default)                                   | none                                                                            |
| `quality`       | Code quality, test coverage, linting, type safety                       | `safety_reliability` +10, `pipeline_stage` +5, `developer_experience` +5        |
| `features`      | New capabilities, tools, integrations, product value                    | `pipeline_stage` +10, `automation_potential` +10                                |
| `security`      | Vulnerability remediation, auth hardening, input validation, compliance | `safety_reliability` +15, `cross_repo` +5                                       |
| `performance`   | Speed, token efficiency, cost reduction, resource optimization          | `automation_potential` +10, `implementation_complexity` +5, `pipeline_stage` +5 |
| `documentation` | Docs accuracy, coverage, onboarding, knowledge management               | `developer_experience` +15, `cross_repo` +5                                     |
| `reliability`   | Error handling, recovery, monitoring, health, fault tolerance           | `safety_reliability` +15, `pipeline_stage` +5                                   |
| `ux`            | Developer experience, CLI ergonomics, VSCode UI, onboarding friction    | `developer_experience` +15, `cross_repo` +5                                     |

Score boosts apply to the six assessment dimensions defined in
`skills/nightgauge-release-watch/assessment-engine.md`.

---

## How Focus Works in Each System

### Autonomous Scheduler

The scheduler reads `focus.yaml` at the start of each `prioritize()` cycle.
Issues that match the active lens's keywords receive a boost of up to **+20
points**:

- **+2 per label match** — issue labels that contain a lens keyword
- **+1 per title word match** — title words that appear in the lens keyword list
- Score is **capped at 20** to prevent domination

Prioritization order (highest to lowest weight):

1. Critical path (highest unblock potential)
2. **Focus alignment boost** ← applied here
3. Priority field (P0 > P1 > P2 > P3)
4. Smaller size (XS > S > M > L > XL)
5. Higher unblock count

**Key behaviors:**

- Critical-path items always sort above focus-aligned items regardless of boost
- `general` lens produces zero boost — ordering falls back to pure priority/size
- Missing `focus.yaml` is treated as `general` (fully backward-compatible)

### Release-Watch Assessment Engine

When the release-watch skill evaluates a new Claude Code release, it reads
`focus.yaml` at Phase 5 (scoring). For each feature/change item, it:

1. Maps the item's keywords to assessment dimensions
   (`safety_reliability`, `pipeline_stage`, `automation_potential`,
   `developer_experience`, `cross_repo`)
2. Looks up each matching dimension in the lens's `ScoringBoosts` map
3. Adds the boost to the item's base quick-pass score
4. Caps the final score at 100

Score output notation: `Score: 42 → 57 [+15 security lens focus]`

When no `focus.yaml` exists or the active lens is `general`, no boost is applied
and the output omits the notation.

Keyword-to-dimension mapping used by release-watch:

| Dimension              | Trigger keywords                                                                                   |
| ---------------------- | -------------------------------------------------------------------------------------------------- |
| `safety_reliability`   | auth, permission, security, sandbox, privacy, scope, vulnerability, secret, encrypt, sanitize, CVE |
| `pipeline_stage`       | tool, mcp, agent, command, skill, context, ability, plugin, server                                 |
| `automation_potential` | performance, speed, token, cost, cache, optimize, efficient, reduce                                |
| `developer_experience` | ux, experience, ergonomic, friction, ui, usability, onboard, interface                             |
| `cross_repo`           | cross, multi-repo, workspace, integration, ecosystem                                               |

### Continuous-Improvement Reviews

The continuous-improvement skill loads the active lens at Phase 1.4. In Phase 4,
each generated proposal is checked for focus alignment by matching proposal
keywords against the lens keyword list. Aligned proposals are:

- **Promoted** in priority ordering (sorted above same-tier non-aligned proposals)
- **Marked with `★`** in the markdown report
- **Tagged** with `focus_aligned: true` in the JSON output schema

The JSON output schema (`v2`) includes focus metadata:

```json
{
  "active_focus": {
    "activeLens": "security",
    "keywords": ["security", "vulnerability", "auth", ...],
    "alignmentStats": {
      "total": 12,
      "aligned": 4,
      "alignedPercent": 33
    }
  }
}
```

When the active lens is `general`, the focus metadata section is omitted from
output.

Proposal categories and their associated focus lenses:

| Proposal category              | Boosted by focus             |
| ------------------------------ | ---------------------------- |
| Skill drift / effectiveness    | `quality`, `reliability`     |
| Calibration accuracy           | `quality`, `reliability`     |
| Pipeline health                | `reliability`, `performance` |
| Cost / token efficiency        | `performance`                |
| Reliability / failure patterns | `reliability`, `security`    |
| Safety-critical (always)       | all lenses                   |

### VSCode Extension

The VSCode extension exposes focus management through:

- **Command Palette**: `Nightgauge: Focus: Set`, `Nightgauge: Focus: Show`, `Nightgauge: Focus: Clear`
- **Dashboard sidebar**: Shows active lens name and description
- **IPC protocol**: Focus state changes broadcast to all connected Claude Code sessions

---

## Focus Configuration File

Focus state is stored in `.nightgauge/focus.yaml`:

```yaml
active_lens: security
set_at: 2026-03-15T09:00:00Z
set_by: cli
custom_lenses:
  - name: mobile
    description: Focus on mobile app quality across Flutter and Angular repos
    scoring_boosts:
      cross_repo: 15
      developer_experience: 5
    keywords:
      - flutter
      - ios
      - android
      - mobile
```

**Fields:**

| Field           | Type      | Description                                   |
| --------------- | --------- | --------------------------------------------- |
| `active_lens`   | string    | Name of active lens (default: `general`)      |
| `set_at`        | timestamp | When the focus was last changed (UTC)         |
| `set_by`        | string    | Source of last change: `cli`, `vscode`, `ipc` |
| `custom_lenses` | array     | User-defined lens definitions                 |

The file is created automatically on first `focus set` invocation. If the file
does not exist, all systems default to the `general` (no-boost) lens.

---

## Custom Lenses

Add custom lenses under `custom_lenses` in `focus.yaml` or define them via the
CLI (feature coming — for now, edit the YAML directly):

```yaml
custom_lenses:
  - name: mobile
    description: Mobile app quality — Flutter, iOS, Android
    scoring_boosts:
      cross_repo: 15
      developer_experience: 5
    keywords:
      - flutter
      - ios
      - android
      - mobile
      - widget
```

Custom lenses follow the same scoring rules as built-in lenses. The `scoring_boosts`
keys must match valid assessment dimension names:
`safety_reliability`, `pipeline_stage`, `automation_potential`,
`developer_experience`, `cross_repo`, `implementation_complexity`.

---

## CLI Reference

### `nightgauge focus set <name>`

Activate a named lens. Persists to `.nightgauge/focus.yaml`.

```bash
nightgauge focus set quality
# → Focus lens set to "quality" (set_by: cli)
```

Lens names are **case-insensitive** — `QUALITY`, `Quality`, and `quality` are
equivalent.

**Error** if the lens name is not recognized:

```
unknown lens "fast" — available: general, quality, features, security, performance, documentation, reliability, ux
```

### `nightgauge focus show`

Display the current focus state and resolved lens definition:

```
Active lens:  quality
Set at:       2026-03-15 09:00 UTC
Set by:       cli
Description:  Focus on code quality, test coverage, linting, type safety, and correctness.
Keywords:     test, coverage, lint, quality, type, strict, validate, correctness
Boosts:
  safety_reliability:  +10
  pipeline_stage:       +5
  developer_experience: +5
```

### `nightgauge focus clear`

Reset focus to `general` (no boost):

```bash
nightgauge focus clear
# → Focus lens reset to "general"
```

---

## Workflow Examples

### Prioritize security hardening for a sprint

```bash
# Before starting the sprint
nightgauge focus set security

# Now run autonomous mode — security-related issues sort first
nightgauge autonomous run

# After the sprint
nightgauge focus clear
```

### Assess new Claude Code release through a performance lens

```bash
nightgauge focus set performance
# Run release-watch — performance features score higher
/nightgauge:release-watch
```

### Review self-improvement proposals with quality bias

```bash
nightgauge focus set quality
# Run continuous-improvement — quality proposals are promoted and marked ★
/nightgauge:continuous-improvement
```

---

## Backward Compatibility

Focus mode is fully backward-compatible:

- **No `focus.yaml`**: All systems behave as before — no boost, pure priority ordering
- **`active_lens: general`**: Explicit general lens — identical to no file
- **Unknown lens name in YAML**: Systems fall back to `general` (no error, no crash)
- **New consumers**: Must handle missing `focus.yaml` gracefully (treat as general)

---

## Related Documentation

- [docs/AUTONOMOUS_ORCHESTRATOR.md](AUTONOMOUS_ORCHESTRATOR.md) — Autonomous scheduler architecture and focus integration
- [docs/CONFIGURATION.md](CONFIGURATION.md) — Full `.nightgauge/config.yaml` schema (focus.yaml is separate)
- [docs/SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md) — Pipeline learning system
- [skills/nightgauge-release-watch/assessment-engine.md](../skills/nightgauge-release-watch/assessment-engine.md) — Assessment dimension definitions
- [internal/focus/focus.go](../internal/focus/focus.go) — Go implementation reference
