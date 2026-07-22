# Scheduled Tasks & Watchdogs

This document describes how to configure automated scheduled execution of Nightgauge skills and tasks.

## Overview

Nightgauge supports multiple scheduling approaches depending on your needs:

1. **Lightweight detection** (GitHub Actions) — Deterministic API checks with no AI inference
2. **Full assessment** (Claude Code Desktop) — Full skill execution with relevance scoring
3. **Remote scheduling** (Claude Code Cloud) — Future: cloud-based scheduled execution

---

## Release Discovery Watchdog

The **Release Watchdog** monitors Claude Code GitHub releases and detects new versions relevant to the pipeline.

### Architecture

Two-tier approach:

**Tier 1: Lightweight Detection (GitHub Actions)**

- Runs daily at 9 AM UTC via `.github/workflows/release-watchdog.yml`
- Fetches latest Claude Code release from GitHub API
- Compares against cached last-seen version
- Creates notification issue on first detection of new release
- Cost: ~2 cents per run (GitHub Actions VM)
- Time: < 1 minute

**Tier 2: Full Assessment (Claude Code Desktop)**

- Manually triggered or scheduled in Claude Code Desktop
- Runs the `nightgauge-release-watch` skill with full AI analysis
- Scores changes by pipeline relevance (features, breaking changes, deprecations, etc.)
- Auto-creates GitHub issues for high-relevance findings
- Cost: ~\$0.30 per run (Claude 3.5 Sonnet API)
- Time: 2-5 minutes

### Configuration

#### Automatic Detection (No Setup Required)

The GitHub Actions watchdog is always enabled. It runs automatically on the schedule defined in `.github/workflows/release-watchdog.yml`:

```yaml
on:
  schedule:
    - cron: "0 9 * * *" # Daily at 9 AM UTC
  workflow_dispatch: {} # Manual trigger option
```

**View runs:** [Actions → Release Watchdog](https://github.com/nightgauge/nightgauge/actions/workflows/release-watchdog.yml)

#### Full Assessment (Manual or Scheduled)

To run the full release-watch skill with detailed analysis:

**Option 1: Manual trigger in Claude Code Desktop**

1. Open Claude Code
2. Run the skill command:
   ```
   /nightgauge:release-watch --create-issues
   ```
3. The skill will:
   - Fetch releases since the last-seen version
   - Classify each change by type (feature, fix, breaking, deprecation, improvement)
   - Score pipeline relevance on a 0-100 scale
   - Create GitHub issues for scores >= 70 (configurable)

**Option 2: Scheduled in Claude Code Desktop**

1. Open Claude Code → Settings → Scheduled Tasks (experimental)
2. Create new scheduled task:
   - **Name:** "Claude Code Release Watch"
   - **Command:** `/nightgauge:release-watch --create-issues`
   - **Schedule:** Daily or weekly (recommended: weekly)
   - **Timeout:** 10 minutes

**Option 3: Schedule via `/schedule` command (Future)**

```bash
/schedule --name "release-watch" \
  --command "/nightgauge:release-watch --create-issues" \
  --interval daily \
  --cloud-enabled
```

### State Management

#### Last-Seen State File

Location: `.nightgauge/release-watch/last-seen-claude-code.json`

```json
{
  "version": "2.1.81",
  "detected_at": "2026-03-24T09:15:00Z",
  "detection_source": "github-actions-watchdog",
  "full_tag": "v2.1.81"
}
```

**Important:** This file is **tracked in git** so all team members see the same state.

#### Reports Directory

Location: `.nightgauge/release-watch/reports/`

Transient files created during full assessment (not committed):

- `assessment-TIMESTAMP.md` — Detailed markdown report
- `assessment-TIMESTAMP.json` — Structured assessment data
- `.gitignore` entry: `release-watch/reports/`

### Workflow: New Release Detection

```
┌─────────────────────────────────────────────────────────────┐
│                  SCHEDULED WATCHDOG RUN                      │
│                   Daily 9 AM UTC                            │
└─────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────┐
│    Step 1: Fetch Latest Claude Code Release (API call)      │
│    - Query: GET /repos/anthropics/claude-code/releases      │
│    - Extract: tag_name, release notes                       │
└─────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────┐
│    Step 2: Compare with Last-Seen Version                   │
│    - Read: .nightgauge/release-watch/last-seen-claude-code.json    │
│    - Match: latest_version == last_seen_version?            │
└─────────────────────────────────────────────────────────────┘
                            │
                    ┌───────┴───────┐
                    │               │
            No New Release    New Release Found
                    │               │
                 [Skip]            ▼
                                   │
        ┌───────────────────────────────────────────┐
        │ Step 3: Create Notification Issue         │
        │ - Title: "chore: New Claude Code release" │
        │ - Label: type:chore,priority:low          │
        │ - Body: Instructions for full assessment  │
        └───────────────────────────────────────────┘
                    │
                    ▼
        ┌───────────────────────────────────────────┐
        │ Step 4: Update last-seen.json             │
        │ - Store detected version                  │
        │ - Store detection timestamp               │
        │ - Commit to git                           │
        └───────────────────────────────────────────┘
```

### Health Monitoring

#### How to Check Watchdog Health

1. **Last detection time:**

   ```bash
   jq '.detected_at' .nightgauge/release-watch/last-seen-claude-code.json
   ```

2. **View GitHub Actions runs:**

   ```bash
   gh run list --workflow release-watchdog.yml --limit 10
   ```

3. **Check for failures:**
   ```bash
   gh run list --workflow release-watchdog.yml --status failure --limit 5
   ```

#### Stale Detection Alerts

If the watchdog hasn't run in **48+ hours** without explicit disablement, check:

1. **GitHub Actions status:** https://www.githubstatus.com
2. **Workflow failures:**
   ```bash
   gh run list --workflow release-watchdog.yml -s failure
   ```
3. **Manual trigger to recover:**
   ```bash
   gh workflow run release-watchdog.yml
   ```

#### Verification Script

Create `.nightgauge/scripts/check-watchdog-health.sh`:

```bash
#!/bin/bash
set -e

echo "=== Release Watchdog Health Check ==="
echo ""

# Check if state file exists
if [ ! -f ".nightgauge/release-watch/last-seen-claude-code.json" ]; then
  echo "ERROR: State file not found — watchdog may not have run yet"
  exit 1
fi

# Get last detection time
LAST_DETECTED=$(jq -r '.detected_at' .nightgauge/release-watch/last-seen-claude-code.json)
LAST_VERSION=$(jq -r '.version' .nightgauge/release-watch/last-seen-claude-code.json)

echo "Last detection: $LAST_DETECTED"
echo "Last version: $LAST_VERSION"

# Calculate hours since last detection
LAST_EPOCH=$(date -d "$LAST_DETECTED" +%s 2>/dev/null || date -j -f "%Y-%m-%dT%H:%M:%SZ" "$LAST_DETECTED" +%s)
NOW_EPOCH=$(date +%s)
HOURS_AGO=$(( ($NOW_EPOCH - $LAST_EPOCH) / 3600 ))

echo "Hours since last detection: $HOURS_AGO"

if [ "$HOURS_AGO" -gt 48 ]; then
  echo "WARNING: Watchdog appears stale (>48 hours)"
  echo "Run: gh workflow run release-watchdog.yml --ref main"
  exit 1
fi

echo "Status: Healthy"
exit 0
```

Run with:

```bash
chmod +x .nightgauge/scripts/check-watchdog-health.sh
./.nightgauge/scripts/check-watchdog-health.sh
```

---

## Configuration

### Environment Variables

| Variable                                         | Description                    | Default |
| ------------------------------------------------ | ------------------------------ | ------- |
| `NIGHTGAUGE_RELEASE_WATCH_ENABLED`               | Enable watchdog                | `true`  |
| `NIGHTGAUGE_RELEASE_WATCH_AUTO_CREATE_ISSUES`    | Create issues for new releases | `false` |
| `NIGHTGAUGE_RELEASE_WATCH_RELEVANCE_THRESHOLD`   | Min relevance score (0-100)    | `70`    |
| `NIGHTGAUGE_RELEASE_WATCH_STALE_THRESHOLD_HOURS` | Max hours before stale alert   | `48`    |

### Config File

Add to `.nightgauge/config.yaml`:

```yaml
release_watch:
  enabled: true
  schedule: daily
  timezone: UTC
  cron: "0 9 * * *" # 9 AM UTC
  auto_create_issues: false # Start conservative
  relevance_threshold: 70
  notification_channel: github_issue # or: slack, email (future)
  stale_threshold_hours: 48
  state_file: .nightgauge/release-watch/last-seen-claude-code.json
  reports_dir: .nightgauge/release-watch/reports
```

---

## Examples

### Example 1: Manual Full Assessment

Run the release-watch skill with all options:

```bash
/nightgauge:release-watch \
  --since 2.1.75 \
  --create-issues \
  --format json
```

This will:

1. Fetch releases from v2.1.75 to current
2. Score each change for pipeline relevance
3. Create GitHub issues for scores >= 70
4. Output detailed JSON report

### Example 2: Dry-Run (Preview Issues)

Preview what issues would be created without actually creating them:

```bash
/nightgauge:release-watch \
  --create-issues \
  --dry-run \
  --format markdown
```

### Example 3: Monitor Specific Category

Score only changes in a specific area:

```bash
/nightgauge:release-watch \
  --filter tools \
  --create-issues
```

---

## Troubleshooting

### Watchdog Not Running

1. **Check GitHub Actions status:** https://www.githubstatus.com

2. **View recent runs:**

   ```bash
   gh run list --workflow release-watchdog.yml --limit 5
   ```

3. **Check for recent failures:**

   ```bash
   gh run list --workflow release-watchdog.yml -s failure
   ```

4. **Manual trigger:**
   ```bash
   gh workflow run release-watchdog.yml --ref main
   ```

### Duplicate Issues Created

The watchdog checks for existing issues before creating new ones. If duplicates occur:

1. Close the duplicates manually
2. Check the workflow logs to diagnose why the check failed
3. Report issue to the team

### State File Out of Sync

If `.nightgauge/release-watch/last-seen-claude-code.json` is out of sync with actual releases:

```bash
# Reset to current latest release
gh api repos/anthropics/claude-code/releases/latest --jq '.tag_name' | \
  xargs -I {} bash -c 'echo "{\"version\":\"'{}'\",\"detected_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"detection_source\":\"manual-reset\",\"full_tag\":\"{}\"}" > .nightgauge/release-watch/last-seen-claude-code.json'

git add .nightgauge/release-watch/last-seen-claude-code.json
git commit -m "chore: reset release-watch state"
git push
```

### Full Assessment Not Running

If the release-watch skill isn't available:

1. **Check skill exists:**

   ```bash
   ls -la skills/nightgauge-release-watch/
   ```

2. **Test skill directly:**

   ```bash
   /nightgauge:release-watch --dry-run
   ```

3. **Check for errors:**
   ```bash
   /nightgauge:release-watch --format json | jq '.errors'
   ```

---

## Focus-Aware Scheduling

When a focus lens is active in `.nightgauge/focus.yaml`, scheduled tasks
apply focus-based weighting to their output:

### Release Watch + Focus

The release-watch assessment engine reads the active focus lens and boosts
matching Claude Code features. For example, with `security` focus active:

- Features related to auth, permissions, or vulnerability fixes receive score
  boosts (`safety_reliability` +15, `cross_repo` +5)
- Boosted scores appear as: `Score: 42 → 57 [+15 security lens focus]`
- High-relevance threshold is applied to the _boosted_ score

This makes the release watchdog more likely to open GitHub issues for changes
that matter most to your current sprint focus.

**Setting focus before a scheduled run:**

```bash
nightgauge focus set security
# Now run or wait for the next scheduled release-watch execution
nightgauge focus clear   # reset after the sprint
```

### Continuous-Improvement + Focus

When a scheduled continuous-improvement review runs, focus-aligned proposals
are promoted above non-aligned proposals of the same type and marked with `★`
in the output report.

For complete focus mode documentation, see [docs/FOCUS_MODE.md](FOCUS_MODE.md).

---

## Related Documentation

- **[Release Watch Skill](../skills/nightgauge-release-watch/SKILL.md)** — Full skill documentation
- **[Focus Mode](./FOCUS_MODE.md)** — Focus lens configuration and integration
- **[CI Integration](./CI_INTEGRATION.md)** — GitHub Actions and CI/CD patterns
- **[Automations](./AUTOMATIONS.md)** — Event-triggered automations on status changes
- **[CONFIGURATION.md](./CONFIGURATION.md)** — Full configuration reference

---

## Best Practices

### Scheduling

1. **Avoid peak hours:** Schedule during low-traffic windows (early morning UTC)
2. **Stagger multiple tasks:** If scheduling multiple watchdogs, space them 15+ minutes apart
3. **Use GitHub Actions for detection:** Lightweight API checks should use Actions
4. **Use Claude Code Desktop for assessment:** Full skill execution is more reliable locally

### State Management

1. **Commit last-seen.json to git:** Keep team in sync
2. **Review state periodically:** Check for stale detections
3. **Don't manually edit timestamps:** Let the system manage state
4. **Archive old reports:** Move `.nightgauge/release-watch/reports/` periodically

### Notifications

1. **Use GitHub issues for blocking changes:** High-relevance findings
2. **Use Slack for FYI notifications:** Low-relevance findings (future)
3. **Set appropriate label filters:** Use `priority:low` to avoid blocking important work
4. **Tag relevant team members:** Add `@team/` mentions in issue body for visibility

---

## Author

nightgauge
