---
name: nightgauge-backlog-groom
description: Perform periodic backlog triage - identify stale issues, detect duplicates,
  validate priorities, and discover dependencies. Use weekly/monthly for backlog
  hygiene.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
context: fork
agent: pipeline-researcher
model: haiku
---

# Nightgauge Backlog Groom

## Description

Automated backlog triage and hygiene for GitHub-backed repositories. Identifies
stale issues, detects potential duplicates, validates priority labels against
issue content, and discovers hidden dependency chains.

**Use Cases:**

- Weekly/monthly backlog reviews
- Post-sprint cleanup
- Pre-release grooming
- Onboarding new team members to codebase issues

**When to Use:**

- Regular cadence (weekly/bi-weekly) to maintain backlog health
- Before planning sessions to ensure accurate priorities
- After major feature completion to retire related issues
- When backlog size exceeds manageable limits

## Invocation

| Tool        | Command                                                   |
| ----------- | --------------------------------------------------------- |
| Claude Code | `/nightgauge:backlog-groom [options]`                     |
| Copilot     | Invoke via Agent Skills extension                         |
| Cursor      | Run via Agent Skills or direct SKILL.md                   |
| Standalone  | `claude --skill skills/nightgauge-backlog-groom/SKILL.md` |

## Arguments

### Core Options

| Argument         | Description                                     | Default |
| ---------------- | ----------------------------------------------- | ------- |
| `--apply`        | Apply recommended changes (add comments/labels) | `false` |
| `--dry-run`      | Generate report without applying changes        | `true`  |
| `--stale-days N` | Mark issues inactive for N days as stale        | `60`    |
| `--focus TYPE`   | Focus analysis on specific area                 | `all`   |

### Focus Areas

| Focus Type     | Analyzes                        |
| -------------- | ------------------------------- |
| `all`          | All triage phases (default)     |
| `stale`        | Only stale issue detection      |
| `duplicates`   | Only duplicate detection        |
| `priorities`   | Only priority validation        |
| `dependencies` | Only dependency chain discovery |

### Examples

```bash
# Dry run with default 60-day stale threshold
/nightgauge:backlog-groom

# Apply changes with 90-day threshold
/nightgauge:backlog-groom --apply --stale-days 90

# Focus on duplicate detection only
/nightgauge:backlog-groom --focus duplicates

# Full triage and apply
/nightgauge:backlog-groom --apply
```

## Philosophy

### Deterministic vs Probabilistic Split

This skill follows the Nightgauge architecture principle of using
deterministic operations where possible, reserving AI for truly interpretive
tasks:

| Operation             | Type          | Rationale                                          |
| --------------------- | ------------- | -------------------------------------------------- |
| Stale detection       | Deterministic | Date math - no interpretation needed               |
| Duplicate pre-filter  | Deterministic | Keyword overlap using exact string matching        |
| Semantic similarity   | Probabilistic | AI understands context beyond keywords             |
| Priority validation   | Probabilistic | Requires reasoning about urgency/impact/complexity |
| Dependency extraction | Deterministic | Regex parsing of issue references                  |
| Dependency graph      | Deterministic | Graph traversal algorithm                          |
| Report generation     | Deterministic | Fixed markdown template                            |

**Cost Efficiency**: Deterministic pre-filtering reduces AI calls. For example,
duplicate detection only sends top 5 keyword-matched candidates to AI for
semantic analysis, rather than comparing all N×N issue pairs.

### Context Isolation

This is a **standalone utility skill**, not part of the main pipeline. It:

- Does NOT read pipeline context files (`.nightgauge/pipeline/*.json`)
- Does NOT write pipeline handoff files
- Generates standalone triage reports in `.nightgauge/triage/`
- Can be run at any time without affecting pipeline state

## Configuration

Configuration is read from `.nightgauge/config.yaml`:

```yaml
backlog_groom:
  # Stale issue detection
  stale_threshold_days: 60
  stale_exclude_labels:
    - "status:blocked"
    - "status:needs-info"

  # Duplicate detection
  duplicate_similarity_threshold: 0.75 # 0.0-1.0
  duplicate_keyword_min_overlap: 3 # Minimum shared keywords

  # Priority validation
  priority_labels:
    - "priority:critical"
    - "priority:high"
    - "priority:medium"
    - "priority:low"

  # Dependency tracking
  dependency_keywords:
    - "depends on"
    - "blocks"
    - "blocked by"
    - "requires"
    - "prerequisite"

  # Report output
  report_dir: ".nightgauge/triage"
  max_duplicate_candidates: 5
```

**Defaults**: If `.nightgauge/config.yaml` is missing or incomplete, the skill uses
built-in defaults matching the schema above.

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 0.5: Run Reflection

Load the previous groom so this run can lead with **what changed** (newly stale,
newly resolved, new duplicates) instead of re-dumping the whole backlog.

```bash
SKILL_NAME="nightgauge-backlog-groom"
RUN_LOG=".nightgauge/triage/runs.jsonl"
```

<!-- include: ../_shared/RUN_REFLECTION.md -->

Set `RUN_COUNTS` (e.g. `{"stale":N,"duplicates":N,"priority_fixes":N}`) and
`RUN_SUMMARY` from the Phase 3 report before the append step.

---

See the CLI wrapper documentation at
[claude-plugins/nightgauge/commands/backlog-groom.md](../../claude-plugins/nightgauge/commands/backlog-groom.md)
for complete workflow details including all phases:

- Phase 0: Setup & Configuration
- Phase 1: Data Collection
- Phase 2: Analysis (Stale, Duplicates, Priorities, Dependencies)
- Phase 3: Report Generation
- Phase 4: Apply Changes (optional)

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

### Common Errors

| Error                   | Cause                         | Solution                               |
| ----------------------- | ----------------------------- | -------------------------------------- |
| `gh: command not found` | GitHub CLI not installed      | Install from https://cli.github.com    |
| `gh auth status` fails  | Not authenticated             | Run `gh auth login`                    |
| `403 rate limit`        | Too many API requests         | Wait 1 hour or use authenticated token |
| `No open issues found`  | Repository has no open issues | Normal - report shows 0 results        |

## Integration

### Standalone Utility

This skill is **NOT** part of the main Nightgauge pipeline. It:

- Does not require pipeline context files
- Can be run at any time
- Does not modify pipeline state
- Does not block or depend on pipeline stages

### Recommended Cadence

| Frequency | Use Case                               |
| --------- | -------------------------------------- |
| Weekly    | Active projects with high issue volume |
| Bi-weekly | Medium-sized teams                     |
| Monthly   | Mature projects with stable backlog    |
| Ad-hoc    | Before planning sessions, releases     |

## Dependencies

- **gh CLI** (GitHub CLI) - Required for issue fetching
- **jq** - JSON parsing and manipulation
- **Python 3.7+** - Keyword analysis, graph traversal
- **Bash 4+** - Shell scripting
