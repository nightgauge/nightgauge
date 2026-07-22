# Scheduled Discovery — Autonomous Self-Improvement Loop

Nightgauge automatically detects Claude Code releases, analyzes their
relevance to the pipeline, and creates actionable GitHub issues — daily and
weekly, without manual intervention.

## Overview

Two scheduled workflows power the autonomous self-improvement loop:

| Workflow                     | Schedule                  | Trigger                 | Skill                                |
| ---------------------------- | ------------------------- | ----------------------- | ------------------------------------ |
| `release-watchdog.yml`       | Daily at 9 AM UTC         | New Claude Code release | `/nightgauge:release-watch`          |
| `continuous-improvement.yml` | Weekly on Monday 8 AM UTC | Scheduled               | `/nightgauge:continuous-improvement` |

Both workflows write structured JSON logs that are surfaced in the VSCode
dashboard **Discovery Activity** tab.

---

## How It Works

### Release-Watch (Daily)

1. **Detect**: `release-watchdog.yml` calls the GitHub API to check for new Claude Code releases
2. **Compare**: Compares against `.nightgauge/release-watch/last-seen-claude-code.json`
3. **Assess**: If a new release is found, runs `/nightgauge:release-watch --since <version> --create-issues`
4. **Create**: Auto-creates GitHub issues for changes with relevance score >= `score_threshold` (default: 70)
5. **Backlog**: Stores lower-scored changes in `.nightgauge/release-watch/backlog.json`
6. **Log**: Writes run results to `.nightgauge/release-watch/creation-log.json`

### Continuous Improvement (Weekly)

1. **Review**: Runs `/nightgauge:continuous-improvement --mode dogfood --create-issues`
2. **Analyze**: Reviews pipeline effectiveness using recent execution history
3. **Focus**: Applies active focus lens from `.nightgauge/focus.yaml` to rank proposals
4. **Create**: Auto-creates GitHub issues for high-value improvement proposals
5. **Log**: Writes run results to `.nightgauge/improvement-runs/latest.json`

---

## Focus Lens Integration

Both skills read `.nightgauge/focus.yaml` to apply dimension boosts during scoring:

- **Release-Watch**: Focus lens boosts relevance scores for feature categories matching the active lens (e.g., `security` lens elevates security-related Claude Code changes)
- **Continuous Improvement**: Focus lens steers proposal ranking toward the active focus area (e.g., `performance` lens surfaces performance-related improvement proposals first)

When no `focus.yaml` exists, both skills use baseline scoring without dimension boosts.

**Example:** With `focus.yaml` set to `performance`:

```yaml
# .nightgauge/focus.yaml
active_lens: performance
```

A Claude Code change affecting streaming performance might score 62 (below threshold)
without the lens, but 81 with the performance boost — triggering automatic issue creation.

---

## Configuration

Autonomous discovery is configured in `.nightgauge/config.yaml`:

```yaml
autonomous_discovery:
  enabled: true # Master switch for all scheduled runs
  kill_switch: false # Pause issue creation (detection continues)
  score_threshold: 70 # Min relevance score (0-100) to auto-create an issue
  auto_created_label: "type:chore,area:release-watch"

discovery_budget:
  release_watch_max_tokens: null # No limit by default
  continuous_improvement_max_tokens: null

scheduled_tasks:
  release_watch:
    enabled: true
    schedule: "0 9 * * *" # Daily at 9 AM UTC
  continuous_improvement:
    enabled: true
    schedule: "0 8 * * 1" # Weekly on Monday 8 AM UTC
```

See [docs/CONFIGURATION.md](CONFIGURATION.md) for the full configuration reference.

---

## Kill Switch

To pause issue creation without removing discovery infrastructure:

```yaml
# .nightgauge/config.yaml
autonomous_discovery:
  kill_switch: true # Monitoring continues — no issues created
```

With the kill switch active:

- Release detection still runs daily
- `last-seen.json` is still updated
- Skill runs but `--create-issues` is not passed
- Dashboard shows all run results normally

To disable all scheduled runs entirely:

```yaml
autonomous_discovery:
  enabled: false # All scheduled runs are skipped
```

---

## Dashboard Visibility

The VSCode dashboard **Discovery Activity** tab shows:

- **Summary cards**: Issues created (7 days), proposals created (7 days), pending backlog count
- **Last run timestamps**: When release-watch and CI review last ran
- **Release-Watch section**: Version detected, issues auto-created (with relevance scores), backlogged count, deduplication count
- **Continuous Improvement section**: Mode, proposals created, backlogged proposals
- **Backlog table**: Pending changes sorted by score (top 20)
- **Configuration reference**: Kill-switch instructions

The data is loaded lazily when you click the **Discovery** tab.

---

## Manual Triggering

Both workflows support `workflow_dispatch` for on-demand runs:

```bash
# Trigger release-watch check
gh workflow run release-watchdog.yml

# Trigger continuous improvement review (dry run)
gh workflow run continuous-improvement.yml -f dry_run=true

# Trigger CI review in customer mode
gh workflow run continuous-improvement.yml -f mode=customer
```

---

## State Files

| File                                                   | Purpose                              | Written by                   |
| ------------------------------------------------------ | ------------------------------------ | ---------------------------- |
| `.nightgauge/release-watch/last-seen-claude-code.json` | Last detected Claude Code version    | `release-watchdog.yml`       |
| `.nightgauge/release-watch/creation-log.json`          | Last release-watch run results       | `release-watchdog.yml`       |
| `.nightgauge/release-watch/backlog.json`               | Sub-threshold changes pending review | Release-watch skill          |
| `.nightgauge/improvement-runs/latest.json`             | Last CI review run results           | `continuous-improvement.yml` |

---

## Required Secrets

The autonomous discovery step requires `ANTHROPIC_API_KEY` to be set in the
repository secrets. Without it, the workflow runs in **detection-only mode**:
the release is detected and logged, but no skill assessment or issue creation occurs.

```bash
# Set via GitHub CLI
gh secret set ANTHROPIC_API_KEY --body "<your-api-key>"
```

---

## Troubleshooting

| Symptom                                 | Likely Cause                                       | Fix                                                 |
| --------------------------------------- | -------------------------------------------------- | --------------------------------------------------- |
| No issues created after detection       | `kill_switch: true` or `ANTHROPIC_API_KEY` not set | Check config or secrets                             |
| Same issue created multiple times       | Deduplication not working                          | Check issue title pattern in `release-watchdog.yml` |
| Dashboard shows "No discovery runs yet" | No state files exist                               | Trigger `workflow_dispatch` manually                |
| Skill fails in GitHub Actions           | `claude` CLI not in PATH                           | Ensure `claude` CLI is installed on runner          |
| Backlog grows but no issues created     | `score_threshold` too high                         | Lower `score_threshold` in config                   |

---

## Related Documentation

- [docs/CONFIGURATION.md](CONFIGURATION.md) — Full config schema reference
- [docs/FOCUS_MODE.md](FOCUS_MODE.md) — Focus lens system
- [skills/nightgauge-release-watch/SKILL.md](../skills/nightgauge-release-watch/SKILL.md)
- [skills/nightgauge-continuous-improvement/SKILL.md](../skills/nightgauge-continuous-improvement/SKILL.md)
- [docs/SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md)
