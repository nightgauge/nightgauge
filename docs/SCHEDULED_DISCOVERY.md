# Scheduled Discovery — Autonomous Self-Improvement Loop

Nightgauge can detect Claude Code releases, judge their relevance to the
pipeline, and file actionable issues — daily and weekly, without manual
intervention. Two GitHub Actions workflows drive it, and the VSCode dashboard's
**Discovery** tab shows what they found.

> **History.** For most of this feature's life, this page described the two
> workflows in the present tense and neither existed. The dashboard tab, the
> service that reads their output, and its tests all shipped; the producer did
> not (#753). The tab was correct code reading files nothing wrote. Everything
> below now describes what is in the tree — the workflow files, the scripts they
> call, and the state branch that carries their output to your checkout.

## Overview

| Workflow                     | Schedule                  | Produces                                                 |
| ---------------------------- | ------------------------- | -------------------------------------------------------- |
| `release-watchdog.yml`       | Daily at 9 AM UTC         | `.nightgauge/release-watch/creation-log-<provider>.json` |
| `continuous-improvement.yml` | Weekly on Monday 8 AM UTC | `.nightgauge/improvement-runs/latest.json`               |

Both are **off by default**, in this repository and in any workspace that has
not opted in — see [Configuration](#configuration). Both also support
`workflow_dispatch` for an on-demand run.

---

## How a Run Works

Each workflow is two jobs: a `gate` that reads the config, and the run itself.
The gate is separate so a switched-off loop costs one five-minute job and says
why in the run summary, instead of silently doing nothing inside a long job.

### Release-Watch (daily)

1. **Gate** — `scripts/discovery-config-gate.py --task release_watch` resolves
   `autonomous_discovery.enabled`, `scheduled_tasks.release_watch.enabled`,
   `kill_switch` and `score_threshold`. It **fails closed**: a missing,
   unreadable or unparseable config resolves to "disabled", never to defaults.
2. **Carry state forward** — `scripts/discovery-state-sync.sh` fetches the last
   run's `last-seen-<provider>.json` from the `discovery-state` branch. Without
   this the runner would start from `0.0.0` every morning.
3. **Detect** — `nightgauge release fetch --source <repo> --since <last-seen>`.
   This is deterministic Go (`internal/cmd/release`), model-free, and drops
   drafts and prereleases.
4. **Open the run record** — `scripts/discovery-run-record.py open` writes a
   `status: running` record _before_ any model work.
5. **Assess** — when a release was found, the kill switch is off, and
   `ANTHROPIC_API_KEY` is configured, the release-watch skill runs headlessly
   and files issues for changes scoring at or above `score_threshold`. Changes
   below it go to `.nightgauge/release-watch/backlog.json`.
6. **Advance last-seen** — including in detection-only mode, so the same release
   is not re-detected forever.
7. **Close the run record and publish** — `discovery-run-record.py close` stamps
   the terminal status, and `scripts/discovery-state-publish.sh` pushes the
   result to the `discovery-state` branch.

### Continuous Improvement (weekly)

The same shape, with the review skill in place of release-watch: gate → carry
state forward → open record → run the review (`--mode dogfood` by default) →
close record → publish. The review is the only model-driven step.

### Why the record is opened before the work

A producer that writes only on success is indistinguishable from a producer
that never ran — which is precisely the state this feature was in. Opening a
`running` record first means a cancelled or crashed run still leaves evidence
in the tab, and `close` stamps `failed` with the reason when the skill errors.

### Detection-only mode

Everything except assessment and issue authoring is deterministic, so a
repository with no `ANTHROPIC_API_KEY` still gets a complete, honest run
record — detection ran, last-seen advanced, zero issues created. The workflow
does not fail; it records what happened.

---

## How State Reaches Your Checkout

The Discovery tab reads the **local filesystem**. The workflows write on a
**GitHub-hosted runner** whose disk is discarded when the job ends. Something
has to carry the bytes, and this is the part the original design left implicit.

**The decision: a dedicated `discovery-state` branch, fetched on demand.**

```bash
# In your checkout, whenever you want fresh discovery data:
scripts/discovery-state-sync.sh
```

That fetches the tip of `discovery-state` and writes
`.nightgauge/release-watch/*.json` and `.nightgauge/improvement-runs/*.json`
into your working tree without touching your index or your branch. Those paths
are gitignored (`.nightgauge/.gitignore`), so a sync leaves `git status` clean:
they are local copies of state owned elsewhere, the same class as
`.nightgauge/health/` and `.nightgauge/attention/`.

Three alternatives were rejected, each for a concrete reason:

| Alternative          | Why not                                                                                                                                                                           |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Commit to `main`     | Impossible. The `main` ruleset carries a `pull_request` rule, so no workflow token can push to it; the only route is a ruleset bypass, which also waives every required check.    |
| A daily pull request | Auto-merge is disabled on every workspace repo, so each run would wait on a human — and burn a full CI matrix on a changed timestamp. A loop needing a daily click is not a loop. |
| Workflow artifacts   | They expire, are not addressable as "the latest one" without a second API call, and need an authenticated download per file.                                                      |

The branch costs one extra ref. It is not built by CI — the CI, CodeQL and
publication-boundary workflows trigger on `main` and on pull requests only — so
it consumes no minutes and gates nothing. `git log discovery-state` reads as a
record of when the loop ran and what it found.

---

## Configuration

Autonomous discovery is configured in `.nightgauge/config.yaml`:

```yaml
autonomous_discovery:
  enabled: false # Master switch for all scheduled discovery
  kill_switch: true # Pause issue creation (detection continues)
  score_threshold: 70 # Min relevance score (0-100) to auto-create an issue
  auto_created_label: "type:chore,area:release-watch"

scheduled_tasks:
  release_watch:
    enabled: false
  continuous_improvement:
    enabled: false
```

Resolution, implemented once in `scripts/discovery-config-gate.py`:

- **The run happens** when `autonomous_discovery.enabled` **and** the per-task
  `scheduled_tasks.<task>.enabled` are both true. Either being false skips the
  whole run.
- **Issues are created** when the run happens **and** `kill_switch` is false.
- Every switch defaults to **off** when absent, and `kill_switch` defaults to
  **on**. A workspace does not acquire an autonomous loop by upgrading
  Nightgauge.

The schedules themselves live in the workflow files' `cron` expressions, not in
`scheduled_tasks`. GitHub resolves `on: schedule` before any repository file is
read, so a cron in config could never take effect; the config switch decides
whether a triggered run does anything.

See [docs/CONFIGURATION.md](CONFIGURATION.md) for the full configuration
reference.

### Kill Switch

```yaml
autonomous_discovery:
  kill_switch: true # Monitoring continues — no issues created
```

With the kill switch active, release detection still runs daily, `last-seen` is
still advanced, the run record is still written and published, and the tab still
shows the run. Only issue creation is suppressed.

To stop the runs entirely, set `autonomous_discovery.enabled: false` (or the
per-task switch).

---

## Focus Lens Integration

Both skills read `.nightgauge/focus.yaml` to apply dimension boosts during
scoring:

- **Release-Watch** — the lens boosts relevance scores for change categories
  matching the active lens (a `security` lens elevates security-related Claude
  Code changes).
- **Continuous Improvement** — the lens steers proposal ranking toward the
  active focus area.

With no `focus.yaml`, both use baseline scoring. Example: with
`active_lens: performance`, a Claude Code change affecting streaming
performance might score 62 (below threshold) without the lens and 81 with it,
crossing into automatic issue creation.

---

## Manual Triggering

```bash
# Daily release check, now
gh workflow run release-watchdog.yml

# Detect and report without creating issues or advancing last-seen
gh workflow run release-watchdog.yml -f dry_run=true

# Re-check from a specific version
gh workflow run release-watchdog.yml -f since=2.1.74

# Weekly review in customer mode
gh workflow run continuous-improvement.yml -f mode=customer
```

A dispatched run is still subject to the config gate: `workflow_dispatch` is a
trigger, not an override.

---

## State Files

| File                                                     | Purpose                                | Written by                                    |
| -------------------------------------------------------- | -------------------------------------- | --------------------------------------------- |
| `.nightgauge/release-watch/last-seen-<provider>.json`    | Last detected version, per provider    | `release-watchdog.yml`                        |
| `.nightgauge/release-watch/creation-log-<provider>.json` | Last release-watch run result          | `scripts/discovery-run-record.py` + the skill |
| `.nightgauge/release-watch/backlog.json`                 | Sub-threshold changes pending review   | release-watch skill                           |
| `.nightgauge/improvement-runs/latest.json`               | Last continuous-improvement run result | `scripts/discovery-run-record.py` + the skill |

The run-record schema is spelled out in exactly one place —
`scripts/discovery-run-record.py` — and read in exactly one place,
`packages/nightgauge-vscode/src/services/DiscoveryActivityService.ts`. The
arrival test at `packages/nightgauge-vscode/tests/arrival/dashboardDiscoveryTab.test.ts`
runs the real script, publishes the result through the real state branch, syncs
it into a second checkout and asserts the values reach the rendered tab, so a
field renamed on either side turns red rather than silently emptying the tab.

Release-watch state is per-provider: the service globs `creation-log*.json` and
aggregates every match, so each provider writes its own file.

---

## Dashboard Visibility

The **Discovery** tab shows:

- **Summary cards** — issues created (7 days), proposals created (7 days),
  pending backlog count, and when each loop last ran
- **Release-Watch section** — version detected, issues auto-created with their
  relevance scores, backlogged count, deduplication count
- **Continuous Improvement section** — mode, proposals created, backlogged
  proposals
- **Backlog table** — pending changes sorted by score (top 20)
- **Configuration reference** — kill-switch instructions

Data is loaded when the tab is first activated, and re-read on the tab's refresh
button. It reads whatever is on disk — so run `scripts/discovery-state-sync.sh`
first, or the tab shows the last state you synced.

---

## Required Secrets

Assessment and issue authoring need `ANTHROPIC_API_KEY` in the repository
secrets. Without it the workflows run in **detection-only mode**: the release is
detected, last-seen advances, and the run is recorded, but no relevance
assessment or issue creation occurs.

```bash
gh secret set ANTHROPIC_API_KEY --body "<your-api-key>"
```

`GITHUB_TOKEN` is supplied by Actions; the jobs request `contents: write` (to
push the state branch) and `issues: write` (for the skills).

---

## Troubleshooting

| Symptom                                          | Likely cause                                         | Fix                                                                       |
| ------------------------------------------------ | ---------------------------------------------------- | ------------------------------------------------------------------------- |
| Workflow run is one green `gate` job and no more | The config gate resolved "disabled"                  | Read the run summary — it names the switch that stopped it                |
| Dashboard shows "No discovery activity yet"      | No state has been synced into this checkout          | Run `scripts/discovery-state-sync.sh`                                     |
| Sync says the branch does not exist              | No run has published yet                             | Dispatch a run, or check whether the gate is skipping every scheduled run |
| Run recorded, zero issues created                | `kill_switch: true`, or no `ANTHROPIC_API_KEY`       | Check the run summary's "issue creation" line                             |
| Same release detected every day                  | `last-seen` never advanced — the publish step failed | Check the "Publish state" step; it needs `contents: write`                |
| Backlog grows but no issues created              | `score_threshold` too high                           | Lower `autonomous_discovery.score_threshold`                              |
| Tab shows a run stuck in "running"               | The run was cancelled between `open` and `close`     | Expected, and deliberate — the next run replaces the record               |

---

## Related Documentation

- [docs/CONFIGURATION.md](CONFIGURATION.md) — Full config schema reference
- [docs/FOCUS_MODE.md](FOCUS_MODE.md) — Focus lens system
- [docs/SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md)
- [skills/nightgauge-release-watch/SKILL.md](../skills/nightgauge-release-watch/SKILL.md)
- [skills/nightgauge-continuous-improvement/SKILL.md](../skills/nightgauge-continuous-improvement/SKILL.md)
