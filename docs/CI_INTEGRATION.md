# CI Integration Guide

This guide explains how to use the Nightgauge SDK CLI for automated
pipeline execution in CI/CD environments.

## Overview

The Nightgauge SDK provides a CLI that enables headless, non-interactive
pipeline execution for CI/CD environments. This allows you to:

- Automatically process issues through the Issue-to-PR pipeline
- Run pipelines on schedule or via triggers
- Integrate with GitHub Actions, GitLab CI, Jenkins, etc.
- Monitor progress with JSON output for parsing

## Installation

```bash
# The SDK is not yet published to npm — build it from a clone of this repo
git clone https://github.com/nightgauge/nightgauge.git
cd nightgauge && npm install
npm run -w @nightgauge/sdk build

# Then run the CLI from inside the workspace
npx nightgauge-sdk run <issue-number>
```

## CLI Commands

### Run Full Pipeline

Execute the complete Issue-to-PR pipeline for an issue:

```bash
npx nightgauge-sdk run <issue-number> [options]
```

**Options:**

| Option                 | Description                         | Default    |
| ---------------------- | ----------------------------------- | ---------- |
| `--stages <list>`      | Comma-separated stages to run       | All stages |
| `--model <model>`      | Model: sonnet, opus, haiku          | sonnet     |
| `--auto-approve`       | Skip approval prompts               | false      |
| `--timeout <ms>`       | Global timeout in milliseconds      | 3600000    |
| `--stage-timeout <ms>` | Per-stage timeout in milliseconds   | 900000     |
| `--format <format>`    | Output format: text, json           | text       |
| `--log-level <level>`  | Log level: debug, info, warn, error | info       |

**Examples:**

```bash
# Run full pipeline
npx nightgauge-sdk run 42

# Run with auto-approve for CI
npx nightgauge-sdk run 42 --auto-approve

# Run specific stages only
npx nightgauge-sdk run 42 --stages issue-pickup,feature-planning

# Run with JSON output
npx nightgauge-sdk run 42 --format json

# Run with custom timeout
npx nightgauge-sdk run 42 --timeout 1800000 --stage-timeout 600000
```

### Run Single Stage

Execute a single pipeline stage:

```bash
npx nightgauge-sdk stage <stage-name> <issue-number> [options]
```

**Stage Names:**

- `issue-pickup` - Extract requirements from GitHub issue
- `feature-planning` - Generate implementation plan
- `feature-dev` - Implement the feature
- `pr-create` - Create pull request
- `pr-merge` - Wait for reviews and merge

**Examples:**

```bash
# Run just the planning stage
npx nightgauge-sdk stage feature-planning 42

# Retry a failed stage
npx nightgauge-sdk stage feature-dev 42 --format json
```

### Check Pipeline Status

Check the current pipeline status for an issue:

```bash
npx nightgauge-sdk status <issue-number> [options]
```

**Examples:**

```bash
# Check status
npx nightgauge-sdk status 42

# Check status with JSON output
npx nightgauge-sdk status 42 --format json
```

## Environment Variables

All configuration can be provided via environment variables:

| Variable                   | Description                                 | Default     |
| -------------------------- | ------------------------------------------- | ----------- |
| `NIGHTGAUGE_ADAPTER`       | Adapter: claude-sdk, claude-headless, codex | auto-detect |
| `ANTHROPIC_API_KEY`        | Required only for `claude-sdk` adapter      | -           |
| `NIGHTGAUGE_AUTO_APPROVE`  | Skip approval prompts                       | false       |
| `NIGHTGAUGE_OUTPUT_FORMAT` | Output format: text, json                   | text        |
| `NIGHTGAUGE_LOG_LEVEL`     | Log level: debug, info, warn, error         | info        |
| `NIGHTGAUGE_TIMEOUT`       | Global timeout in ms                        | 3600000     |
| `NIGHTGAUGE_STAGE_TIMEOUT` | Per-stage timeout in ms                     | 900000      |
| `NIGHTGAUGE_MODEL`         | Model: sonnet, opus, haiku                  | sonnet      |

**Example:**

```bash
export NIGHTGAUGE_ADAPTER=claude-headless
# Required only for claude-sdk adapter:
# export ANTHROPIC_API_KEY=sk-ant-...
export NIGHTGAUGE_AUTO_APPROVE=true
export NIGHTGAUGE_OUTPUT_FORMAT=json

npx nightgauge-sdk run 42
```

## Exit Codes

The CLI uses specific exit codes for different outcomes:

| Code | Meaning                 | Action                       |
| ---- | ----------------------- | ---------------------------- |
| 0    | Pipeline completed      | Success                      |
| 1    | Pipeline stage failed   | Check logs, retry stage      |
| 2    | Configuration error     | Fix config, check API key    |
| 3    | Timeout exceeded        | Increase timeout or simplify |
| 130  | User interrupt (SIGINT) | Pipeline was cancelled       |

## JSON Output

When using `--format json`, the CLI outputs structured JSON for parsing:

### Pipeline Result

```json
{
  "success": true,
  "issueNumber": 42,
  "branch": "feat/42-add-feature",
  "stagesCompleted": ["issue-pickup", "feature-planning", "feature-dev", "pr-create"],
  "stagesFailed": [],
  "totalDurationMs": 245000,
  "usage": {
    "inputTokens": 45000,
    "outputTokens": 12000,
    "cacheReadTokens": 8000,
    "costUsd": 0.0825
  },
  "prUrl": "https://github.com/org/repo/pull/123"
}
```

### Stage Result

```json
{
  "success": true,
  "stage": "feature-planning",
  "issueNumber": 42,
  "durationMs": 45000
}
```

### Status

```json
{
  "isRunning": false,
  "currentStage": "feature-dev",
  "issueNumber": 42,
  "contextFiles": [".nightgauge/pipeline/issue-42.json", ".nightgauge/pipeline/planning-42.json"],
  "planFile": ".nightgauge/plans/42-add-feature.md"
}
```

## GitHub Actions Integration

### Basic Workflow

```yaml
name: Nightgauge Pipeline

on:
  workflow_dispatch:
    inputs:
      issue_number:
        description: "Issue number"
        required: true

jobs:
  run-pipeline:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-node@v4
        with:
          node-version: "20"

      - name: Install dependencies
        run: npm ci

      - name: Run Nightgauge Pipeline
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          NIGHTGAUGE_AUTO_APPROVE: "true"
          NIGHTGAUGE_OUTPUT_FORMAT: "json"
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: npx nightgauge-sdk run ${{ inputs.issue_number }}
```

### Triggered by Issue Label

```yaml
name: Nightgauge Auto-Pipeline

on:
  issues:
    types: [labeled]

jobs:
  run-pipeline:
    if: github.event.label.name == 'nightgauge:run'
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-node@v4
        with:
          node-version: "20"

      - run: npm ci

      - name: Remove trigger label
        uses: actions/github-script@v7
        with:
          script: |
            await github.rest.issues.removeLabel({
              owner: context.repo.owner,
              repo: context.repo.repo,
              issue_number: context.issue.number,
              name: 'nightgauge:run'
            });

      - name: Run Pipeline
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          NIGHTGAUGE_AUTO_APPROVE: "true"
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: npx nightgauge-sdk run ${{ github.event.issue.number }}
```

The workflow above is a complete GitHub Actions example.

## GitLab CI Integration

```yaml
nightgauge-pipeline:
  image: node:20
  variables:
    NIGHTGAUGE_AUTO_APPROVE: "true"
    NIGHTGAUGE_OUTPUT_FORMAT: "json"
  script:
    - npm ci
    - npx nightgauge-sdk run $ISSUE_NUMBER
  rules:
    - if: $CI_PIPELINE_SOURCE == "trigger"
```

## Jenkins Integration

```groovy
pipeline {
    agent any

    environment {
        ANTHROPIC_API_KEY = credentials('anthropic-api-key')
        NIGHTGAUGE_AUTO_APPROVE = 'true'
    }

    parameters {
        string(name: 'ISSUE_NUMBER', description: 'Issue number to process')
    }

    stages {
        stage('Setup') {
            steps {
                sh 'npm ci'
            }
        }

        stage('Run Pipeline') {
            steps {
                sh "npx nightgauge-sdk run ${params.ISSUE_NUMBER}"
            }
        }
    }
}
```

## Timeout Configuration

Timeouts prevent runaway pipelines and control costs:

- **Global timeout** (`NIGHTGAUGE_TIMEOUT`): Maximum total pipeline
  duration
- **Stage timeout** (`NIGHTGAUGE_STAGE_TIMEOUT`): Maximum per-stage
  duration

**Recommended values:**

| Pipeline Type  | Global Timeout | Stage Timeout |
| -------------- | -------------- | ------------- |
| Simple bug fix | 15 min         | 5 min         |
| Small feature  | 30 min         | 10 min        |
| Medium feature | 60 min         | 15 min        |
| Large feature  | 120 min        | 30 min        |

## Error Handling

### Retrying Failed Stages

If a stage fails, you can retry just that stage:

```bash
# Check which stage failed
npx nightgauge-sdk status 42 --format json

# Retry the failed stage
npx nightgauge-sdk stage feature-dev 42
```

### Common Errors

| Error                                | Cause                            | Solution                     |
| ------------------------------------ | -------------------------------- | ---------------------------- |
| API key not set                      | Missing ANTHROPIC_API_KEY        | Set environment variable     |
| Timeout exceeded                     | Stage took too long              | Increase timeout or simplify |
| Stage failed                         | Error during execution           | Check logs, retry            |
| Context file not found               | Previous stage didn't complete   | Run previous stages first    |
| `base branch policy prohibits merge` | Branch ruleset blocks `pr-merge` | See Ruleset Interactions     |

## Ruleset Interactions

GitHub repository rulesets can block a merge in ways that the pipeline cannot
work around silently. The `pr-merge` skill includes a Step 6.0 "Ruleset
Pre-Check" (introduced in #2780) that queries
`/repos/{owner}/{repo}/rules/branches/{base_ref}` before attempting the merge
and attempts to satisfy known blockers.

### Supported Blockers

| Ruleset type                        | Pre-check behaviour                                                  | Failure behaviour if still unresolved                                                            |
| ----------------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `copilot_code_review`               | Requests Copilot as a reviewer and waits up to 5 minutes for review. | Merge fails with `CatRulesetBlocked`. Not retryable — needs an admin to disable or fix the rule. |
| `pull_request` (required approvers) | Logs the required reviewer count. Cannot auto-approve.               | Merge fails with `CatRulesetBlocked`. Resolve by obtaining the required approvals.               |
| Required status checks              | Already covered by Phase 5's CI gate.                                | Merge fails with a non-ruleset failure classification.                                           |

### Failure Classification

A merge that fails with `base branch policy prohibits the merge` is classified
as `ruleset-blocked` by `internal/intelligence/failure/taxonomy.go`. This
category is **not retryable** and **escalates** — the autonomous scheduler
skips retry and surfaces the outcome in the next `retro` run.

### Token Scope

The ruleset pre-check requires a token with `administration:read` on the
repository. If the token lacks this scope, Step 6.0 prints a NOTE and skips
the check — the merge still runs, and any ruleset failure is classified via
the stderr pattern match.

### Relaxing a Ruleset

Rulesets are managed in the repository's Settings → Rules → Rulesets. If a
ruleset is blocking pipeline merges and you want the pipeline to own the
merge lifecycle, either:

1. Remove the specific rule (e.g., `copilot_code_review`) from the ruleset.
2. Bypass the ruleset for the pipeline's service account via
   **Bypass list** → add the GitHub App / user.

Never add an `--admin` flag to the pipeline's merge command as a workaround.
See `docs/GIT_WORKFLOW.md` §Auto-Merge and Pipeline Control for rationale.

## Security Considerations

- **API keys**: Always use environment variables or secrets managers
- **JSON output**: Does not include sensitive data (only URLs, not tokens)
- **Timeouts**: Prevents runaway processes and cost overruns
- **Least privilege**: Use scoped tokens where possible

## Monitoring

### Token Usage

Monitor token usage via JSON output:

```bash
npx nightgauge-sdk run 42 --format json | jq '.usage'
```

### Cost Tracking

Track costs across runs:

```bash
npx nightgauge-sdk run 42 --format json | jq '.usage.costUsd'
```

### Logging

Adjust log level for debugging:

```bash
# Detailed debug output
NIGHTGAUGE_LOG_LEVEL=debug npx nightgauge-sdk run 42

# Errors only
NIGHTGAUGE_LOG_LEVEL=error npx nightgauge-sdk run 42
```

## Troubleshooting

### CI Jobs Not Starting

If GitHub Actions workflows are not being picked up or queued:

1. **Check GitHub Status first**: <https://www.githubstatus.com> — GitHub
   Actions outages cause jobs to queue indefinitely. This should be the first
   thing you check before investigating runner configuration.

2. **Check self-hosted runner**: Verify the runner service is online and not
   stuck on a previous job. Runners are shared across `nightgauge` and
   `acme-platform` repos.

3. **Re-trigger**: Push an empty commit
   (`git commit --allow-empty -m "chore: trigger CI"`) to re-queue the workflow.

---

**Author:** nightgauge **License:** Apache-2.0
