# Workflow Automations

This document describes how to configure and use Nightgauge workflow
automations to execute actions when issues transition between statuses.

## Overview

Workflow automations allow you to define actions that automatically execute when
issues change status. This enables:

- **Slack notifications** when issues enter review
- **Automatic reviewer assignment** when PRs are ready
- **Label management** for blocked issues
- **User notifications** via GitHub comments
- **Custom script execution** for integration with external systems

### Key Benefits

| Benefit             | Description                                  |
| ------------------- | -------------------------------------------- |
| **Zero LLM tokens** | Automations are deterministic shell scripts  |
| **Fast execution**  | Shell scripts execute in milliseconds        |
| **Audit trail**     | All executions logged to JSON Lines file     |
| **Dry-run mode**    | Test automations without side effects        |
| **Secure secrets**  | Webhook URLs stored in environment variables |

### How It Works

```
┌─────────────────────────────────────────────────────────────────┐
│                    AUTOMATION FLOW                               │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Project Status field changes (via sync-project-status.sh)   │
│                              │                                   │
│                              ▼                                   │
│  2. automation-trigger.sh detects matching triggers             │
│     - Parses automations from config.yaml                      │
│     - Matches trigger conditions to new status                  │
│     - Expands template variables                                │
│                              │                                   │
│                              ▼                                   │
│  3. automation-dispatch.sh executes actions                     │
│     - Routes to action handlers (post_slack.sh, etc.)           │
│     - Logs execution to audit file                              │
│                              │                                   │
│                              ▼                                   │
│  4. VSCode extension shows notification (optional)              │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Quick Start

### 1. Add Configuration

Edit `.nightgauge/config.yaml`:

```yaml
automations:
  enabled: true
  dry_run: false
  log_file: ".nightgauge/logs/automation.log"

  triggers:
    - name: "notify-on-review"
      trigger: "in-review"
      actions:
        - type: "post_slack"
          webhook_env: "SLACK_WEBHOOK_CODE_REVIEWS"
          message: "{{issue.title}} (#{{issue.number}}) is ready for review"
```

### 2. Set Environment Variable

```bash
export SLACK_WEBHOOK_CODE_REVIEWS="https://hooks.slack.com/services/..."
```

### 3. Test with Dry-Run

```bash
export NIGHTGAUGE_AUTOMATION_DRY_RUN=true
# Change an issue to in-review status
# Check .nightgauge/logs/automation.log for [DRY-RUN] entries
```

### 4. Enable for Real

```bash
unset NIGHTGAUGE_AUTOMATION_DRY_RUN
# Automations now execute for real
```

---

## Configuration Reference

### Global Settings

| Option     | Type    | Default                             | Description                    |
| ---------- | ------- | ----------------------------------- | ------------------------------ |
| `enabled`  | boolean | `true`                              | Enable/disable all automations |
| `dry_run`  | boolean | `false`                             | Log without executing          |
| `log_file` | string  | `".nightgauge/logs/automation.log"` | Audit log location             |

### Trigger Definition

Each trigger in the `triggers` array:

| Field     | Type   | Required | Description                                      |
| --------- | ------ | -------- | ------------------------------------------------ |
| `name`    | string | No       | Human-readable name for the trigger              |
| `trigger` | string | Yes      | Status value that activates this trigger         |
| `from`    | string | No       | Only trigger when transitioning from this status |
| `actions` | array  | Yes      | Array of actions to execute                      |

**Example with transition filter:**

```yaml
triggers:
  - name: "review-to-done"
    trigger: "done"
    from: "in-review" # Only when coming FROM in-review
    actions:
      - type: "post_slack"
        webhook_env: "SLACK_WEBHOOK_MERGED"
        message: "PR merged for #{{issue.number}}"
```

---

## Action Types

### post_slack

Posts a message to a Slack webhook.

| Field         | Type   | Required | Description                                 |
| ------------- | ------ | -------- | ------------------------------------------- |
| `type`        | string | Yes      | `"post_slack"`                              |
| `webhook_env` | string | Yes      | Environment variable containing webhook URL |
| `channel`     | string | No       | Channel override (if webhook allows)        |
| `message`     | string | Yes      | Message with template variables             |

**Example:**

```yaml
- type: "post_slack"
  webhook_env: "SLACK_WEBHOOK_CODE_REVIEWS"
  channel: "#reviews"
  message: "{{issue.title}} (#{{issue.number}}) needs review"
```

**Security Note:** Never put webhook URLs directly in the config file. Store
them in environment variables:

```bash
export SLACK_WEBHOOK_CODE_REVIEWS="https://hooks.slack.com/services/T.../B.../..."
```

### assign_reviewers

Requests reviewers on the pull request for the issue.

| Field       | Type   | Required | Description                                  |
| ----------- | ------ | -------- | -------------------------------------------- |
| `type`      | string | Yes      | `"assign_reviewers"`                         |
| `reviewers` | array  | Yes      | List of usernames or `@team/team-name` teams |

**Example:**

```yaml
- type: "assign_reviewers"
  reviewers:
    - "@team/platform-reviewers"
    - "senior-dev"
    - "security-reviewer"
```

**Notes:**

- Team reviewers use `@team/` prefix
- Requires an open PR for the issue
- PR is found by matching branch pattern (e.g., `feat/42-...`)

### add_label

Adds a label to the issue.

| Field   | Type   | Required | Description       |
| ------- | ------ | -------- | ----------------- |
| `type`  | string | Yes      | `"add_label"`     |
| `label` | string | Yes      | Label name to add |

**Example:**

```yaml
- type: "add_label"
  label: "needs-help"
```

### remove_label

Removes a label from the issue.

| Field   | Type   | Required | Description          |
| ------- | ------ | -------- | -------------------- |
| `type`  | string | Yes      | `"remove_label"`     |
| `label` | string | Yes      | Label name to remove |

**Example:**

```yaml
- type: "remove_label"
  label: "needs-triage"
```

### notify

Posts a GitHub comment mentioning users.

| Field     | Type   | Required | Description                     |
| --------- | ------ | -------- | ------------------------------- |
| `type`    | string | Yes      | `"notify"`                      |
| `users`   | array  | Yes      | List of usernames to mention    |
| `message` | string | Yes      | Message with template variables |

**Example:**

```yaml
- type: "notify"
  users:
    - "@tech-lead"
    - "@product-owner"
  message: "Issue #{{issue.number}} is blocked and needs attention"
```

### run_script

Executes a custom script in the repository.

| Field    | Type   | Required | Description                              |
| -------- | ------ | -------- | ---------------------------------------- |
| `type`   | string | Yes      | `"run_script"`                           |
| `script` | string | Yes      | Path to script (relative to repo root)   |
| `args`   | array  | No       | Arguments with template variable support |

**Example:**

```yaml
- type: "run_script"
  script: ".nightgauge/scripts/on-done.sh"
  args:
    - "{{issue.number}}"
    - "{{issue.title}}"
```

**Security:**

- Script must be within the repository
- Absolute paths are not allowed
- Path traversal (`..`) is not allowed
- Script must be executable (`chmod +x`)

---

## Template Variables

Use `{{variable}}` syntax in messages and script arguments.

| Variable              | Description            | Example Value                            |
| --------------------- | ---------------------- | ---------------------------------------- |
| `{{issue.number}}`    | GitHub issue number    | `137`                                    |
| `{{issue.title}}`     | Issue title            | `Add workflow automation`                |
| `{{issue.url}}`       | Full GitHub issue URL  | `https://github.com/org/repo/issues/137` |
| `{{issue.labels}}`    | Comma-separated labels | `type:feature,priority:high`             |
| `{{issue.assignee}}`  | Current assignee       | `username`                               |
| `{{status.old}}`      | Previous status value  | `In progress`                            |
| `{{status.previous}}` | Alias for status.old   | `In progress`                            |
| `{{status.new}}`      | New status value       | `In review`                              |
| `{{status.current}}`  | Alias for status.new   | `In review`                              |
| `{{repo.owner}}`      | Repository owner       | `nightgauge`                             |
| `{{repo.name}}`       | Repository name        | `nightgauge`                             |
| `{{timestamp}}`       | ISO 8601 timestamp     | `2026-02-07T20:00:00Z`                   |

---

## Audit Log

All automation executions are logged to the configured `log_file` in JSON Lines
(JSONL) format. Each line is a valid JSON object:

```jsonl
{"timestamp":"2026-02-07T20:00:00Z","trigger":"in-review","action":"post_slack","status":"success","issue":137,"message":"Posted to Slack","dry_run":false}
{"timestamp":"2026-02-07T20:01:00Z","trigger":"blocked","action":"add_label","status":"success","issue":138,"message":"Added label 'needs-help'","dry_run":false}
{"timestamp":"2026-02-07T20:02:00Z","trigger":"done","action":"run_script","status":"error","issue":139,"message":"Script not found","dry_run":false}
```

### Log Entry Fields

| Field         | Description                        |
| ------------- | ---------------------------------- |
| `timestamp`   | ISO 8601 execution timestamp       |
| `trigger`     | Trigger name or status value       |
| `action`      | Action type executed               |
| `status`      | `"success"` or `"error"`           |
| `issue`       | Issue number                       |
| `message`     | Output or error message            |
| `dry_run`     | Whether this was a dry-run         |
| `duration_ms` | Execution duration in milliseconds |

### Viewing Logs

**In VSCode:** Use the Command Palette: `Nightgauge: Show Automation Log`

**In terminal:**

```bash
# View last 10 entries
tail -10 .nightgauge/logs/automation.log | jq

# Filter by issue
jq 'select(.issue == 137)' .nightgauge/logs/automation.log

# Filter by status
jq 'select(.status == "error")' .nightgauge/logs/automation.log

# Filter by action type
jq 'select(.action == "post_slack")' .nightgauge/logs/automation.log
```

---

## Environment Variables

| Variable                          | Description                         |
| --------------------------------- | ----------------------------------- |
| `NIGHTGAUGE_AUTOMATIONS_ENABLED`  | Set to `"false"` to disable         |
| `NIGHTGAUGE_AUTOMATIONS_DRY_RUN`  | Set to `"true"` for dry-run mode    |
| `NIGHTGAUGE_AUTOMATIONS_LOG_FILE` | Override the log file path          |
| `SLACK_WEBHOOK_*`                 | Webhook URLs for Slack integrations |

---

## How Triggers Work

The automation system is invoked by `automation-trigger.sh` when a project board
status change occurs (called by `sync-project-status.sh`). It is **not** driven
by GitHub webhook events directly.

### Invocation

```bash
# Called by sync-project-status.sh when status changes:
scripts/automation-trigger.sh <issue-number> <new-status> [previous-status]
```

### Trigger Matching

The engine evaluates configured triggers against the new status:

```
Status change: issue #137 moved to "in-review" (from "in-progress")

Automation rules evaluation:
  ✓ "notify-on-review" trigger: "in-review" — MATCH
    → Execute actions: post_slack to SLACK_WEBHOOK_CODE_REVIEWS
  ✗ "notify-on-done"   trigger: "done"      — no match
  ✗ "notify-blocked"   trigger: "backlog"    — no match
```

### Template Variable Sources

Template variables are resolved at runtime from `gh issue view` and config:

| Variable             | Source                           | Example                         |
| -------------------- | -------------------------------- | ------------------------------- |
| `{{issue.number}}`   | CLI argument                     | `137`                           |
| `{{issue.title}}`    | `gh issue view --json title`     | `Add retry logic to API client` |
| `{{issue.url}}`      | `gh issue view --json url`       | `https://github.com/...`        |
| `{{issue.labels}}`   | `gh issue view --json labels`    | `type:feature,priority:high`    |
| `{{issue.assignee}}` | `gh issue view --json assignees` | `octocat`                       |
| `{{status.new}}`     | CLI argument                     | `in-review`                     |
| `{{status.old}}`     | CLI argument (optional)          | `in-progress`                   |
| `{{repo.owner}}`     | `.nightgauge/config.yaml`        | `nightgauge`                    |
| `{{repo.name}}`      | `.nightgauge/config.yaml`        | `nightgauge`                    |
| `{{timestamp}}`      | System clock (UTC)               | `2026-02-07T20:00:00Z`          |

---

## Examples

### Notify Slack on Review

```yaml
automations:
  triggers:
    - name: "notify-on-review"
      trigger: "in-review"
      actions:
        - type: "post_slack"
          webhook_env: "SLACK_WEBHOOK_CODE_REVIEWS"
          message: ":mag: *{{issue.title}}* (#{{issue.number}}) is ready for
            review\n<{{issue.url}}|View Issue>"
```

### Auto-Assign Reviewers

```yaml
automations:
  triggers:
    - name: "auto-assign-reviewers"
      trigger: "in-review"
      actions:
        - type: "assign_reviewers"
          reviewers:
            - "@team/platform-reviewers"
```

### Track Blocked Issues

```yaml
automations:
  triggers:
    - name: "track-blocked"
      trigger: "blocked"
      actions:
        - type: "add_label"
          label: "needs-help"
        - type: "notify"
          users:
            - "@tech-lead"
          message: ":warning: Issue #{{issue.number}} is blocked: {{issue.title}}"
        - type: "post_slack"
          webhook_env: "SLACK_WEBHOOK_ALERTS"
          message: ":warning: Issue blocked: {{issue.title}}"
```

### Run Custom Integration

```yaml
automations:
  triggers:
    - name: "sync-to-jira"
      trigger: "done"
      actions:
        - type: "run_script"
          script: ".nightgauge/scripts/sync-jira.sh"
          args:
            - "{{issue.number}}"
            - "{{issue.title}}"
            - "{{repo.owner}}/{{repo.name}}"
```

### Multiple Triggers

```yaml
automations:
  triggers:
    # Trigger 1: When entering review
    - name: "notify-on-review"
      trigger: "in-review"
      actions:
        - type: "post_slack"
          webhook_env: "SLACK_WEBHOOK_CODE_REVIEWS"
          message: "Ready for review: {{issue.title}}"

    # Trigger 2: When done
    - name: "notify-on-done"
      trigger: "done"
      actions:
        - type: "post_slack"
          webhook_env: "SLACK_WEBHOOK_ANNOUNCEMENTS"
          message: ":white_check_mark: Merged: {{issue.title}}"

    # Trigger 3: When blocked
    - name: "notify-blocked"
      trigger: "blocked"
      actions:
        - type: "add_label"
          label: "needs-help"
```

---

## Troubleshooting

### Automations Not Triggering

1. **Check if enabled:**

   ```bash
   grep -A2 "^automations:" .nightgauge/config.yaml
   ```

2. **Check for syntax errors:**

   ```bash
   python3 -c "import yaml; yaml.safe_load(open('.nightgauge/config.yaml'))"
   ```

3. **Enable debug logging:**
   ```bash
   NIGHTGAUGE_HOOKS_DEBUG=1 sync-project-status.sh 137 "In review"
   ```

### Slack Webhook Not Working

1. **Verify environment variable is set:**

   ```bash
   echo $SLACK_WEBHOOK_CODE_REVIEWS
   ```

2. **Test webhook directly:**

   ```bash
   curl -X POST -H "Content-Type: application/json" \
     -d '{"text":"Test message"}' \
     "$SLACK_WEBHOOK_CODE_REVIEWS"
   ```

3. **Check audit log for errors:**
   ```bash
   jq 'select(.action == "post_slack" and .status == "error")' \
     .nightgauge/logs/automation.log
   ```

### Script Not Executing

1. **Check script is executable:**

   ```bash
   ls -la .nightgauge/scripts/on-done.sh
   chmod +x .nightgauge/scripts/on-done.sh
   ```

2. **Test script manually:**

   ```bash
   .nightgauge/scripts/on-done.sh 137 "Test issue"
   ```

3. **Check audit log:**
   ```bash
   jq 'select(.action == "run_script")' .nightgauge/logs/automation.log
   ```

### Dry-Run Mode Stuck

Check if environment variable is set:

```bash
echo $NIGHTGAUGE_AUTOMATION_DRY_RUN
unset NIGHTGAUGE_AUTOMATION_DRY_RUN
```

---

## VSCode Integration

The VSCode extension provides additional automation features:

### View Automation Log

Command Palette: `Nightgauge: Show Automation Log`

Opens a webview showing:

- Recent automation executions
- Filtering by issue, action type, status
- Error highlighting
- Clear log option

### Notifications

When automations execute:

- Success: Status bar message (5 seconds)
- Error: Warning notification with "View Log" action

### Configuration

VSCode settings:

- `nightgauge.automation.showNotifications` - Enable/disable notifications

---

## Best Practices

### Security

1. **Never commit webhook URLs** - Use environment variables
2. **Validate custom scripts** - Review before adding
3. **Use dry-run first** - Test before enabling

### Performance

1. **Keep scripts fast** - Avoid long-running operations
2. **Use filters** - Only trigger when needed (use `from:`)
3. **Batch notifications** - Consider combining actions

### Maintainability

1. **Name your triggers** - Makes audit log readable
2. **Document custom scripts** - Include purpose in script header
3. **Review logs regularly** - Check for unexpected errors

---

## Discord Pipeline Notifications (DiscordService)

**File**: `packages/nightgauge-vscode/src/services/DiscordService.ts`

DiscordService posts a single Discord embed per pipeline run and edits it
in-place as stages progress, so the channel shows live status without flooding
with individual messages. Each embed displays the six pipeline stages with
status icons, elapsed time, and cumulative cost.

### How It Works

1. On `issue-pickup`, the service POSTs a new embed to the configured webhook.
2. As stages progress, it PATCHes the same message (debounced at 1.5 s to
   respect Discord rate limits).
3. When the pipeline completes, a final PATCH sets the outcome color and label
   (green for productive/verify-and-close, yellow for budget-ceiling, grey for
   cancelled, red for failure). Final PATCHes retry up to 2 times with
   exponential backoff if they fail.

### Configuration

```yaml
# .nightgauge/config.yaml
notifications:
  discord:
    enabled: true
    webhook_env: DISCORD_WEBHOOK_URL # name of the env var (not the URL itself)
```

The webhook URL is resolved in priority order:

1. **VSCode SecretStorage** (OS keychain) — set via the
   `Nightgauge: Configure Discord Notifications` command.
2. **Environment variable** named by `webhook_env` — for CI/headless use.

### Concurrent Pipeline Support

In concurrent worktree mode, DiscordService subscribes to each slot's
`PipelineStateService` via `subscribeToSlot()`, creating independent embeds per
concurrent pipeline run.

---

## Post-Epic-Close Version Bump (nightgauge-version-bump)

**Skill**: `skills/nightgauge-version-bump/SKILL.md`

The pipeline automates the **build number** (store-anchored, applied at deploy by
`deploy.sh next_build_number`) but never the **semantic version name** (`2.x.y`)
or the changelog — so a whole feature epic could ship under the prior version and
`CHANGELOG.md` would go stale. This skill is the missing version step, and the
upstream half of the release-prep pair (it runs **before**
`nightgauge-release-notes`).

### Trigger

Epic closure — the same `epic.completed` event (`OnEpicComplete` in
`internal/ipc/server.go`) that drives the release-notes skill. Invocation-driven:
run `/nightgauge:version-bump <epic> --repo <owner/repo>` (then
`/nightgauge:release-notes`), or have a consumer of `epic.completed` invoke
both. No auto-deploy wiring lives in `server.go`.

### What it derives and writes

- **Bump** from the merged sub-issues' types (SemVer + Conventional Commits):
  `feat` → minor, `fix`/`chore`/`docs`/`refactor`/`test` → patch, a breaking
  marker (`!` or `BREAKING CHANGE:`) → major — taking the **highest** across the
  release. Classified from both the `type:` label and the title prefix.
- **`pubspec.yaml`** `version:` **name only** — the store-anchored `+build`
  suffix is preserved verbatim (`deploy.sh` still owns the build number).
- **`CHANGELOG.md`** — a Keep-a-Changelog entry (Added/Fixed/Changed) prepended
  newest-on-top, synthesized from the sub-issue titles.

### One source of truth

`pubspec.yaml` `version:` is the single source: version-bump writes it, and
`nightgauge-release-notes` reads the same field for its "What's new in
vX.Y.Z" header. So `pubspec.yaml`, `CHANGELOG.md`, and the fastlane store notes
can never drift apart.

### Idempotency and override

Idempotent via a per-epic `<!-- nightgauge:version-bump epic #N -->` marker
in `CHANGELOG.md` — re-running for an already-bumped epic is a clean no-op (the
bump is relative to the current version, so the marker, not the pubspec value,
anchors idempotency). A human can override the computed bump with
`--bump major|minor|patch`, or suppress the feat→minor escalation with
`--policy always-patch` (a breaking `major` still escalates).

### No auto-deploy boundary

The skill stops at a reviewable working-tree change. It never commits, pushes, or
dispatches a deploy — the bump lands via the normal PR flow so it reaches `main`
for the next deploy.

---

## Post-Epic-Close Release Notes (nightgauge-release-notes)

**Skill**: `skills/nightgauge-release-notes/SKILL.md`

When an epic fully closes, this skill drafts user-facing "what's new" release
notes for a Flutter/fastlane store repo from the epic's sub-issue titles and
bodies. The output is a **reviewable draft, not an auto-deploy**.

### Trigger

Epic closure. The pipeline already surfaces this as the `epic.completed` event
emitted from `OnEpicComplete` in `internal/ipc/server.go`; its payload carries
`repo` + `epicNumber` — exactly the skill's inputs. The skill is
invocation-driven: run it manually with `/nightgauge:release-notes <epic>
--repo <owner/repo>`, or have a consumer of `epic.completed` invoke it. No
auto-trigger wiring lives in `server.go` (a human runs the deploy after review).

### What it writes

Two store-metadata files in the target repo:

- `fastlane/metadata/en-US/release_notes.txt` — iOS **and** macOS (Universal
  Purchase share one file), ≤4000 chars.
- `fastlane/metadata/android/en-US/changelogs/default.txt` — Android, **≤500
  chars (hard limit)**.

The "What's new in vX.Y.Z" header is read from the target repo's `pubspec.yaml`
`version:` field (overridable with `--version`). The skill detects the fastlane
layout generically and no-ops with a clear message on repos that ship no store
metadata.

### Freshness-gate contract

The draft is shaped to satisfy the downstream store-deploy freshness gate (e.g.
acme-tracker `scripts/deploy.sh` `check_release_notes()`):

1. **BOTH files modified** since the last "Bump build number" commit — the skill
   writes fresh content to both, so `git diff` reports both changed.
2. **Android ≤500 chars** — the skill byte-counts (`wc -c`) the Android file and
   re-condenses until it is ≤500 before finishing. Above 500 the gate aborts the
   whole deploy.

Once both conditions hold, `./scripts/deploy.sh` runs without
`--skip-release-notes`.

### No auto-deploy boundary

The skill stops at a written draft for human review. It never commits, pushes,
or dispatches the store-deploy workflow — store submission stays a deliberate
human action after the copy is reviewed.

---

## Post-Epic Ready-to-Ship Notification (#4076)

The pipeline stops at "merged"; the owner expected "shipped". Store submission is
(correctly) a deliberate, human-dispatched action — but there was no bridge from
"epic closed" to "ready to ship". This notification is that bridge.

When an epic **fully closes** (all sub-issues merged and the epic auto-closed by
the post-merge hook), the pipeline emits a Discord message that includes the
**exact deploy dispatch command** so a human can review and run it. It **never
auto-submits to stores** — it only notifies.

### Example notification

```
🚀 Ready to ship: epic nightgauge/nightgauge#4067 closed

All sub-issues are merged and the epic is closed. Review, then dispatch the
deploy below — nothing is submitted to stores automatically.

Deploy command
  gh workflow run deploy-stores.yml -f platforms=all
```

### Configuration

```yaml
# .nightgauge/config.yaml
ready_to_ship:
  enabled: true # default: true
  discord_webhook_env: NIGHTGAUGE_SHIP_NOTIFY_WEBHOOK # env var holding the URL
  deploy_command: "gh workflow run deploy-stores.yml -f platforms=all" # displayed, never run
```

Set the webhook URL via the environment (the secret never lives in
`config.yaml`):

```bash
export NIGHTGAUGE_SHIP_NOTIFY_WEBHOOK="https://discord.com/api/webhooks/..."
```

With no webhook configured the epic still closes; the notification is simply
skipped (it is an optional bridge). Delivery is best-effort — a failed POST is
logged, never fatal to the pipeline.

### No auto-deploy boundary

This notification only **displays** `deploy_command`; it never executes it,
commits, pushes, or dispatches any workflow. Store submission remains a
deliberate human action.

---

## Related Documentation

- [CONFIGURATION.md](./CONFIGURATION.md) - Full configuration reference
- [ARCHITECTURE.md](./ARCHITECTURE.md#deterministic-vs-probabilistic-architecture) -
  Deterministic script architecture
- [standards/security.md](../standards/security.md) - Security requirements

---

## Author

nightgauge
