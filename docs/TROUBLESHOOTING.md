# Troubleshooting

This guide helps resolve common issues when using nightgauge.

## Plugin Installation Issues

### Claude Code doesn't recognize the plugin

**Symptoms:**

- `/smart-setup` command not found
- Plugin not appearing in installed list

**Solutions:**

1. **Verify settings.json syntax:**

   ```bash
   cat ~/.claude/settings.json | jq .
   ```

   If this fails, you have invalid JSON.

2. **Check plugin path:**

   ```json
   {
     "plugins": ["https://github.com/nightgauge/nightgauge/tree/main/claude-plugins/nightgauge"]
   }
   ```

3. **Restart Claude Code:** Close and reopen your terminal/IDE.

4. **Check network access:** Ensure you can access the GitHub repository.

### Marketplace not appearing

**Symptoms:**

- Can't browse Nightgauge plugins
- Marketplace shows empty

**Solutions:**

1. **Verify marketplace URL:**

   ```json
   {
     "extraKnownMarketplaces": ["https://github.com/nightgauge/nightgauge"]
   }
   ```

2. **Check marketplace.json exists:**

   ```bash
   curl https://raw.githubusercontent.com/nightgauge/nightgauge/main/.claude-plugin/marketplace.json
   ```

## Command Execution Issues

### /smart-setup doesn't generate files

**Symptoms:**

- Command runs but no files created
- Audit shows nothing

**Solutions:**

1. **Check you're in a Git repository:**

   ```bash
   git status
   ```

2. **Verify write permissions:**

   ```bash
   touch test-file.txt && rm test-file.txt
   ```

3. **Check for existing files:** If AGENTS.md exists, the command asks
   permission (NON-DESTRUCTIVE policy).

### /update-docs reports false positives

**Symptoms:**

- Reports stale docs that are actually current
- Flags valid references as deprecated

**Solutions:**

1. **Create `.deprecated-terms.yaml`:**

   ```yaml
   deprecated_terms:
     - term: "old-api-name"
       replacement: "new-api-name"
       reason: "API renamed in v2.0"
   ```

2. **Check discovery patterns:** The command uses heuristics that may need
   tuning for your codebase.

### Command times out

**Symptoms:**

- Command hangs or takes very long
- No output for extended period

**Solutions:**

1. **Large repository:** Use `--scope` to limit analysis:

   ```bash
   /update-docs --scope=docs
   ```

2. **Network issues:** Check connectivity if referencing external resources.

## Configuration Issues

### AGENTS.md not being read

**Symptoms:**

- AI assistant ignores your guidelines
- Generic responses instead of project-specific

**Solutions:**

1. **Verify file location:** AGENTS.md must be in repository root.

2. **Check file syntax:** Ensure valid Markdown formatting.

3. **Verify AI tool support:**

   | Tool           | Reads AGENTS.md        |
   | -------------- | ---------------------- |
   | GitHub Copilot | ✅ Yes                 |
   | OpenAI Codex   | ✅ Yes                 |
   | Cursor         | ✅ Yes                 |
   | Claude Code    | Uses CLAUDE.md instead |

### Copilot instructions not applying

**Symptoms:**

- Copilot ignores `.github/copilot-instructions.md`
- Suggestions don't follow guidelines

**Solutions:**

1. **Verify file path:**

   ```bash
   ls .github/copilot-instructions.md
   ```

2. **Check Copilot version:** Custom instructions require recent Copilot
   versions.

3. **Workspace vs. user settings:** Repository-level instructions may be
   overridden by user settings.

## Validation Errors

### JSON validation fails

**Symptoms:**

- `jq` reports parse error
- CI validation fails

**Solutions:**

1. **Find the error:**

   ```bash
   jq . file.json
   ```

   Output shows line number and error.

2. **Common issues:**
   - Trailing commas
   - Missing quotes
   - Unescaped special characters

3. **Use a JSON validator:**
   - VS Code JSON extension
   - Online validators

### Markdown linting errors

**Symptoms:**

- markdownlint reports issues
- CI markdown check fails

**Solutions:**

1. **Run locally:**

   ```bash
   markdownlint "**/*.md" --ignore node_modules
   ```

2. **Common issues:**
   - MD022: Headings should be surrounded by blank lines
   - MD032: Lists should be surrounded by blank lines
   - MD041: First line should be a heading

3. **Fix or disable:** Some rules can be disabled in `.markdownlint.json` if
   needed.

## Cross-Tool Compatibility

### Configuration works in one tool but not another

**Symptoms:**

- Config works in Copilot but not Cursor
- Different behavior across tools

**Solutions:**

1. **Check tool-specific requirements:** Each tool has different configuration
   formats.

2. **Use universal AGENTS.md:** AGENTS.md is the most widely supported format.

3. **Create tool-specific configs:** Use `configs/<tool>/` for tool-specific
   customizations.

## Interactive Mode Issues

### Token tracking shows "N/A"

**Symptoms:**

- Token usage displays "N/A" in dashboard/sidebar
- Cost tracking unavailable

**Explanation:**

Token tracking is not available in interactive mode. This is by design:

- Interactive mode uses raw text output (not stream-json)
- stream-json requires `-p` flag which closes stdin
- Keeping stdin open for user messages prevents token parsing

**Solution:**

Use headless mode for stages where token tracking is important:

1. Run `Nightgauge: Run Stage`
2. Select "Headless (Recommended)"
3. Token tracking will work normally

### Cannot use interactive mode with batch processing

**Symptoms:**

- Interactive mode option not available during batch
- Batch always runs in headless mode

**Explanation:**

Interactive mode is fundamentally incompatible with batch processing:

- Batch processes multiple issues sequentially without human intervention
- Interactive mode requires human presence to send messages
- Mixing modes would break the automation workflow

**Solution:**

For exploratory work during batch development:

1. Stop the batch (`Nightgauge: Stop Batch`)
2. Run a single stage interactively to debug
3. Resume batch processing when ready

### Interactive session times out unexpectedly

**Symptoms:**

- Session ends after period of inactivity
- "Session terminated due to inactivity" message

**Solutions:**

1. **Check timeout configuration:**

   ```yaml
   # .nightgauge/config.yaml
   execution:
     interactive:
       timeout_minutes: 60 # Increase from default 30
   ```

2. **Send keepalive messages:** Type any message to the agent to reset the
   inactivity timer.

3. **Use headless mode for long-running stages:** If you don't need
   mid-execution interaction, headless mode has no timeout.

---

## Multi-Backend Issues

### Bedrock: Access Denied errors

**Symptoms:**

- "AccessDeniedException" when running pipeline
- "You don't have access to the model" error

**Solutions:**

1. **Enable model access in your region:**
   - Go to AWS Console > Amazon Bedrock > Model access
   - Request access to Claude models
   - Wait for approval (usually instant)

2. **Check IAM permissions:**
   - Verify `bedrock:InvokeModel` permission
   - See
     [MULTI_BACKEND_SETUP.md](./MULTI_BACKEND_SETUP.md#step-2-create-iam-policy)
     for minimum policy

3. **Verify region:**
   - Check `AWS_REGION` environment variable
   - Ensure region supports Bedrock Claude models

### Bedrock: Region not supported

**Symptoms:**

- "Bedrock is not available in this region"
- "Model not found" errors

**Solutions:**

1. **Switch to supported region:**

   ```bash
   export AWS_REGION=us-east-1
   ```

2. **Check model availability:** Not all Claude models are available in all
   Bedrock regions. Supported regions include:
   - `us-east-1` (N. Virginia)
   - `us-west-2` (Oregon)
   - `eu-west-1` (Ireland)
   - `ap-northeast-1` (Tokyo)

### Bedrock: Invalid credentials

**Symptoms:**

- "UnrecognizedClientException"
- "The security token included in the request is invalid"

**Solutions:**

1. **Check environment variables:**

   ```bash
   echo $AWS_ACCESS_KEY_ID
   echo $AWS_REGION
   ```

2. **Verify credentials are valid:**

   ```bash
   aws sts get-caller-identity
   ```

3. **Check credential source priority:** AWS SDK checks credentials in this
   order: environment variables > `~/.aws/credentials` > IAM role
   (EC2/ECS/Lambda)

### Vertex: Authentication errors

**Symptoms:**

- "Could not load the default credentials"
- "Permission denied" errors

**Solutions:**

1. **Set up application default credentials:**

   ```bash
   gcloud auth application-default login
   ```

2. **Or use service account:**

   ```bash
   export GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json
   ```

3. **Verify authentication:**

   ```bash
   gcloud auth application-default print-access-token
   ```

### Vertex: Model not available

**Symptoms:**

- "Model not found in Model Garden"
- "Permission denied to access model"

**Solutions:**

1. **Enable Claude in Model Garden:**
   - Open GCP Console > Vertex AI > Model Garden
   - Search for "Claude"
   - Enable the model for your project

2. **Check IAM permissions:**
   - Service account needs `roles/aiplatform.user` role
   - See
     [MULTI_BACKEND_SETUP.md](./MULTI_BACKEND_SETUP.md#step-3-create-service-account)

3. **Verify region supports model:**
   - `us-central1`
   - `europe-west1`
   - `asia-northeast1`

### Backend switching not working

**Symptoms:**

- Pipeline still uses default backend after config change
- `auth_provider` setting appears to be ignored

**Solutions:**

1. **Verify config file location:**

   ```bash
   cat .nightgauge/config.yaml | grep auth_provider
   ```

2. **Check environment variable override:**

   ```bash
   echo $NIGHTGAUGE_UI_CORE_AUTH_PROVIDER
   ```

   Environment variables take precedence over config files.

3. **Restart VSCode extension:** The extension caches configuration on startup.
   Reload the window after config changes.

---

## Pipeline Runtime — Known False-Alarms & Operational Gotchas

Recurring patterns where the pipeline _looks_ broken but isn't, plus
environment traps that waste triage time. Check these before deep-diving a
reported "failure."

### A reported stage "failure" is often a false alarm

Many paged failures are work that actually succeeded — the PR merged or the
issue closed while the stage exited non-zero. **Always check real state first:**
`gh pr list --head <branch>` and the issue status before re-running anything.
`pr-create` is the genuine fail hotspot (the agent improvising git). See
[FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md) and [AUTO_TRIAGE.md](AUTO_TRIAGE.md).

### Autonomous failure? Suspect the TS-vs-Go orchestrator split first

Extension autonomous slots run per-issue stages through the **TypeScript**
`HeadlessOrchestrator`/`SkillRunner`, NOT the Go scheduler. Deterministic
features wired only into the Go layer silently no-op in autonomous runs. Before
deep-diving any autonomous pipeline failure, confirm which orchestrator path
actually executes the behavior you're debugging. See
[docs/ARCHITECTURE.md](ARCHITECTURE.md) and `.claude/rules/vscode-extension.md`.

### Autonomous "in cooldown" with no real rate limit

A healthy `status=allowed` rate-limit event combined with an idle stall can be
misclassified as quota exhaustion, triggering a global cooldown. Read
`<stage>-stalled.log` `kill_reason:` — `status=allowed` means the cooldown is
bogus. Clear it via the "Autonomous: Clear Quota Cooldown" command (do not hand-
edit `state.json`).

### Reloading the extension mid-run leaves a run with no terminal event

Closing the window or reloading kills the extension host and orphans its stage
children, so the run's `pipeline_done` never fires from the host. The Go orphan
ladder reconciles it at the next activation
(`internal/ipc/pipeline_orphan_reconcile.go`, ADR-017 §7.2–7.4): a 120s startup
grace (`startupGrace`), after which arm 3 probes the stage child's recorded pid.
No TypeScript scanner is involved and nothing is SIGTERM'd on activation — the
#1643 `StaleSlotRecoveryService` was deleted by #427, and its kill had already
been removed by #3840. **Reloading mid-run is still discouraged**: the run
records no terminal outcome until the ladder closes it.

### Dashboard shows phantom "in flight" runs the workspace doesn't have

A run interrupted mid-flight (window closed, extension-host crash, machine
sleep) may not send its terminal `pipeline_done`, so an optional remote monitor
can temporarily show a phantom in-flight run. Two layers can reconcile this:

- **Extension activation reconcile (#44)** — the IPC server persists each
  run's `runtime-{N}.json` snapshot (carrying the platform run UUID) on every
  stage transition and, at next activation, emits the missing `pipeline_done`
  for any non-paused leftover, then deletes the snapshot. Immediate cleanup
  when the workspace reopens.
- **Service-side expiration** — a remote integration should expire runs that
  remain inactive beyond its documented retention window. This is part of the
  service's public behavior, not the local pipeline state model.

Nothing needs re-running: the underlying issue and pull-request state are
unaffected, so check them directly. If a phantom persists beyond the remote
integration's documented expiration window, contact that service's operator;
no private database access is required to repair the local workspace.

### `ci wait` returns TIMEOUT / 0 checks on a healthy PR

`nightgauge ci wait <PR> --json` sometimes burns its timeout and returns
`state=TIMEOUT, total=0` while the PR is all-green. Cross-check
`gh pr checks` / `statusCheckRollup` before recording a CI-monitoring result;
populate from the rollup when `total==0` or `TIMEOUT`.

### `pr ruleset-precheck --auto-satisfy` false `copilot_code_review` blocker

The precheck exits 1 (`requestReviewsInput isn't a defined input type`) on every
PR into `main` and then reports a `copilot_code_review` blocker that does not
exist. Trust `gh pr view --json mergeStateStatus` — `CLEAN` means merge works.
Do not improvise review-request workarounds.

### Worktree has no `node_modules` → build fails

`.worktrees/issue-N` often ships empty → `Cannot find module @anthropic-ai/sdk`;
`npm install` 404s on the private `@nightgauge/shared-types` without
`NODE_AUTH_TOKEN`. Fix: symlink the canonical `node_modules` (root + per-pkg)
and add the symlink paths to `git rev-parse --git-path info/exclude` so
`git add -A` skips them.

### Binary staleness — check `dist/bin/` only

The extension loads `<ext>/dist/bin/nightgauge` (what `dev-install.sh`
copies). Other `bin/` copies (repo-root, `<ext>/bin`) are dead weight. Verify a
suspected stale binary by grepping for a YAML struct tag (survives `-s -w`), not
a Go function symbol.

### Autonomous "0 candidates from N nodes" — stale project board index

Despite Ready+Open issues, GitHub's `projectV2.items` index can go stale. Fix:
delete and re-add each stuck item to the board (preserving Status/Priority/Size).

### `[PRE-FLIGHT]` says `Historical p75: UNCALIBRATED` (#112)

The pre-flight line is projecting from the **static** cost estimate alone, which
carries almost no predictive signal on its own (one `$2.70` estimate bucket
produced actuals from `$1.66` to `$107.02`). Expect the number to run low.

Calibration needs at least three history records with a non-zero cost. It
prefers records whose `size` matches the issue's size bucket and falls back to
the cross-size cohort when too few match, so this line means the run history is
genuinely thin — not that the size join failed.

### `[PRE-FLIGHT]` reads `Historical p75 (all sizes)` and the number is absurd

If the cohort label says **`all sizes`** with no arrow, the projection is a raw
cross-size p75 — the cost of a corpus, not of this issue. On a mixed corpus that
is wrong in whichever direction the issue differs from the corpus: an S issue in
a dogfood workspace was projected at **$29.23** against a **$3.42** actual,
because the p75 landed on a `$63.86` L run.

A healthy fallback reads **`all sizes → S`** instead. That is the rescaled form
(#1229): each historical run is converted into this issue's size by the static
table's own size weighting (`cost × static(target) ÷ static(runSize)`) _before_
the p75 is taken, so a large expensive run stops out-ranking the small ones. The
un-arrowed form now appears only when no run in the cohort carries a size at
all, leaving nothing to anchor the rescale — the fix for that is `size:*` labels,
per the root cause below.

Neither form is a substitute for the per-`(stage, model)` table. Once
`.nightgauge/pipeline/stage-model-calibration.json` has ≥5 samples in a cell the
source becomes `stage-model` and the cross-size path stops being consulted; if
that never happens after many runs, check that the table is being written to
**this** repo and not a sibling's (#1229 — see
[SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md)).

Root cause of the original outage: `size` is the **join key** for cost
calibration, and the IPC history-write path left it `null` on every
extension-driven run (it looked like a cosmetic `omitempty` display field), so
the historical override was silently disabled and estimates ran a median 3.9x
under actual across 112 runs. New records hydrate `labels`/`size`/`type` from
the run's `issue-{N}.json`. An issue with **no `size:*` label** still records a
null size on purpose — a guessed bucket would poison calibration for every
future run of that size — and the Go log says so per run:
`#N has no size:* label — its run record cannot calibrate the pre-flight cost estimate`.

Budget _enforcement_ is unaffected by this and was never implicated: the
warning, wind-down, stage-terminate, escalation, and ceiling-stop guardrails all
measure real spend, not the forecast.

### Opus 4.8 fatal 400 "thinking blocks cannot be modified"

**Historical (#3801, retired 2026-07-13).** On claude CLI 2.1.154, multi-turn
stages on Opus 4.8 failed with this 400 unless `CLAUDE_CODE_DISABLE_THINKING=1`
was forced on every `claude` spawn. The bug no longer reproduces on CLI
2.1.186 — three multi-turn runs with thinking re-enabled (up to 26 turns / 9
replayed blocks across supported reasoning routes) all completed with no 400 —
so the forced flag was **removed** and reasoning models now run with thinking
on (issue #73).

If you hit this 400 today, you are on an old claude CLI. Fix: upgrade the CLI
(≥ 2.1.186). Stopgap: every spawn inherits your environment, so
`export CLAUDE_CODE_DISABLE_THINKING=1` restores the old workaround without a
rebuild — but note `--effort` is moot while it is set.

See [PIPELINE_EXECUTION.md § Spawn Environment Inheritance (Issue
#91)](PIPELINE_EXECUTION.md#spawn-environment-inheritance-issue-91) for why the
stopgap works with no rebuild.

### Frontier run recorded/billed as Opus — CLI refusal fallback (#91)

**Not a bug in your config.** When Fable 5's safety classifier refuses a turn
(e.g. `api_refusal_category: reasoning_extraction`), the claude CLI silently
retries it on Opus 4.8 and the stage still exits 0 — the session `init` event
keeps claiming Fable while every later assistant message reports Opus. Since
#91 both stream parsers track this: the swap logs one
`model_refusal_fallback` line (Go stderr / `[skillRunner]` output), the
per-stage history `ModelSelection` records the served model with source
`cli-refusal-fallback`, and cost/telemetry attribute the model that actually
served. If telemetry shows Opus on a frontier run, grep the session log for
`model_refusal_fallback` — that's the CLI's own safety behavior, not a
routing defect. Do not "fix" it by retrying; attribution is the designed
response.

See [FAILURE_TAXONOMY.md § Model Refusal Fallback (Issue
#91)](FAILURE_TAXONOMY.md#model-refusal-fallback-issue-91) for the full event
shape and the distinct `fableFallbacks` orchestrator-retry mechanism.

### PTC stage fails with "Model refused the request (stop_reason: refusal)" (#75)

A PTC-backed step (context gathering, validation) hit a model safety refusal:
the API ended the turn with `stop_reason: refusal` instead of `end_turn`.
Since #75 the `PTCExecutor` reports this as `success: false` with
`refusal: true` and keeps the refusal text out of `output` (it must never
become downstream context) — before #75 the turn was silently misreported as
success with the refusal prose as the stage's "result". This is the API-level
sibling of the CLI-level fallback above: the raw Messages API has no Opus
retry, so the run fails instead of swapping models. Refusals are almost
always prompt-shape triggered — check what the stage prompt asked for before
retrying; a bare re-run usually refuses again.

### "scheduler not configured" on drag-to-pipeline (multi-repo root)

Go `serve` only attaches a scheduler when the **workspace-root** config has
`owner` + `project.number`. Multi-repo roots have a
`.vscode/nightgauge-workspace.yaml` manifest but no root
`.nightgauge/config.yaml`, so `config.Load` returns defaults (empty owner,
no error) and no scheduler is created. Fix: add a root
`.nightgauge/config.yaml` with owner + project number, then Reload Window.

### `validate-config` red on `internal/ipc` is a contention flake

`TestContract_*` hangs on the shared self-hosted Mac runner under CPU load
(especially when local `go test` runs during CI). Stop local tests and
`gh run rerun <id> --failed`; the GitHub-hosted `build-and-test` job stays
green. (Per [no flaky dismissal](../AGENTS.md#agent-operating-rules): confirm
it's contention, don't assume.)

### Dashboard shows "0 runs" while the pipeline is healthy

**Symptom:** `dashboard.nightgauge.dev` shows `0 runs completed / 0 in
progress / 0 failed in the last 24h` even though the autonomous pipeline is
actively dispatching and completing issues (the `go-backend.log` shows
`autonomous: completed …`).

**Data flow (how a run reaches the dashboard):**

```
Go history producer (internal/state/history.go)
  └─ writes V2 JSONL → .nightgauge/pipeline/history/YYYY-MM-DD.jsonl
       └─ TelemetryUploaderService (extension, every 15 min + on completion)
            └─ maps V2→V4 (pipelineRunV4Mapper.ts) → POST /v1/telemetry/pipeline-run
                 └─ platform TelemetryIngestService.ingest()
                      ├─ usage_events / cost_events / pipeline_outcomes  (Analytics view)
                      └─ pipeline_runs                                   (run list + /stats)
                           └─ dashboard GET /v1/pipeline-runs + /stats
```

Note the autonomous path does **not** use the Go scheduler's direct
`ingestRun` push or the live `/v1/pipelines/events` emitter — both are bypassed
when execution is delegated to the TypeScript `ConcurrentPipelineManager`. The
**telemetry upload is the single path that populates the dashboard.**

**Root-cause class — silent schema/status-vocab skew.** The endpoint returns
`202 {accepted, rejected}` even when it accepts _zero_ records, so a producer/
consumer mismatch (the original incident: snake_case V2 records sent to the
strict camelCase V4 endpoint) drops 100% of records while looking like success.
Diagnose:

1. **Is anything reaching the platform?** On the platform host, grep the API logs for
   `POST /v1/telemetry/pipeline-run` — 202s mean uploads arrive.
2. **Are rows landing?** `select count(*) from pipeline_runs;` (and
   `pipeline_outcomes`). All-zero with 202s = records are being _rejected_, not
   persisted. The platform runbook (`docs/runbooks/operations.md` in the closed
   platform repo, section "Diagnosing telemetry not reaching the dashboard")
   has the exact psql + canary commands.
3. **Check the extension logs** for `TelemetryUploaderService: platform REJECTED
pipeline-run records` (now logged at **error** with sample reasons) or
   `skipping unmappable run record` (a record missing `repo` — only pre-fix
   history lines should hit this).

**Guards now in place:** the uploader no longer advances its watermark past
server-rejected records (so loss is loud and retried, not silent), and the
platform's post-deploy smoke test has a telemetry round-trip canary that fails
the deploy on a `202 accepted:0`. Keep `pipelineRunV4Mapper.ts` aligned with the
platform's `ExecutionHistoryRunRecordV4Schema` — the deploy canary + the
mapper/integration contract tests enforce this.

> **Not yet covered:** true real-time _in-progress_ runs (a run shows once it
> completes, within seconds, via the on-completion upload trigger — not mid-flight).
> Real-time in-progress requires wiring lifecycle events into
> `ConcurrentPipelineManager`; tracked as a follow-up.

### Nothing is running anywhere — check GitHub Status before anything else

**Symptom:** Workflow runs sit `queued` across repositories and nothing moves to
`in_progress`. Self-hosted runners look idle. Jobs may complete with
`startup_failure` and zero steps, or a workflow may never create a run at all.

**Check this first, before touching a runner:**

<https://www.githubstatus.com>

```bash
curl -s https://www.githubstatus.com/api/v2/components.json \
  | jq -r '.components[] | select(.name=="Actions") | "Actions: \(.status)"'
curl -s https://www.githubstatus.com/api/v2/incidents/unresolved.json \
  | jq -r '.incidents[] | "[\(.impact)] \(.name) — \(.status)"'
```

[CI_INTEGRATION.md](CI_INTEGRATION.md#troubleshooting) already states this as
rule 1. It is repeated here because that is not where you look when the pipeline
appears broken — you look here, at the false-alarm list.

**The discriminator, in one line:** an Actions outage stalls **every** repo. A
runner problem cannot stall a repo that uses no self-hosted runner, and a
billing problem cannot stall a **public** repo, where Actions minutes are free
and unmetered.

```bash
# If this repo is also stalled, it is neither your runner nor your billing.
gh api "repos/<owner>/<public-repo>/actions/runs?status=queued&per_page=1" --jq .total_count
gh api "repos/<owner>/<public-repo>/actions/runs?status=in_progress&per_page=1" --jq .total_count
```

**A healthy idle runner is the expected picture during an outage**, not evidence
of a runner fault. `Runner.Listener` alive, log ending in `Listening for Jobs`,
no `Runner.Worker` process — that is a runner correctly waiting for work it is
never offered. Restarting it changes nothing.

**Do not diagnose from the runner log.** `_diag/Runner_*.log` routinely contains
transient `SocketException`s with `Back off N seconds before next retry. N
attempts left`. Those are self-healing retries, and a subsequent
`Listening for Jobs` line is the proof they healed. On 2026-08-26 one such line
was mistaken for a root cause during a `major_outage`, costing an unnecessary
runner restart — while the public repo, which shares neither the runner nor the
billing account, sat stalled in exactly the same way and would have settled it in
one query.

**Ordering that would have caught it:**

1. GitHub Status — is `Actions` operational?
2. Is a **public** repo also stalled? → not runner, not billing.
3. Are **`ubuntu-latest`** jobs also stalled? → not the self-hosted runner.
4. Only then: runner health, labels, runner-group scoping.

### Every PR is stuck and nothing says why (repo-wide blocker)

**Symptom:** Multiple PRs sit unmergeable at once. Individually each looks like
its own problem — a failing check here, a merge that will not go through there —
and each run rediscovers it independently at its merge stage. Nothing anywhere
says "this is one condition affecting everything."

**Root cause class — a repo-wide gate, reported per-run.** A required check
failing on the **default branch** blocks every open PR simultaneously. Before
the repo-scoped sweep existed, the only producer that could notice was
`branch-protection`, which fires when a run reaches `pr-merge` and punts. So the
fact was reported N times as N run-scoped problems, and never once as the single
repo-scoped fact it is.

**The 2026-07-25 incident** is the canonical shape. A red required check on
`main` went unobserved long enough that two actors independently authored the
same fix; the duplicate surfaced only after both were complete. The same day, a
PR passed all thirteen checks and then sat on `REVIEW_REQUIRED` with no signal
on any surface. `.nightgauge/attention/` did not exist in the repository at all
— not empty, **absent** — because every producer shipped to that point fired
from inside a run, and no run had ever hit these conditions in a way that raised
one.

**Diagnose:**

```bash
# Ask the question directly — no run required
nightgauge attention sweep --repo <owner/name> --json

# Then read the inbox
nightgauge attention list
```

A `default-branch-health` card names the specific failing check — which matters,
because the next action differs entirely between a flaky integration test and a
dependency gate that started failing with no code change. A `human-gate` card
names a PR that is green and waiting on a person.

**Notes:**

- The sweep raises nothing for a check that is merely **pending**, and nothing
  for a failure inside its grace window (default 10 minutes, measured from the
  check's own completion time) — so a failure that is re-run green never
  surfaces. If you expect a card and do not see one, check both.
- No required checks configured on the branch means **nothing is required to
  merge**, so `default-branch-health` stays silent by design even if the branch
  looks red.
- If the sweep reports `skipped`, it could not trust its own view — an auth,
  permission, rate-limit, or deadline failure. Existing cards are deliberately
  left untouched rather than retracted. Fix the credential and re-run.
- Muting a card silences it **until the condition changes**, not until a timer
  expires: `nightgauge attention mute <id>`. If a second check starts failing,
  the fingerprint moves, the mute drops, and it alerts again.

See [ATTENTION_PRODUCERS.md](ATTENTION_PRODUCERS.md) and
[ADR 015](decisions/015-decision-requests.md) (Decisions L and M).

### A stage was killed and its implementation is "gone" (#128)

**Symptom:** a stage is terminated by a guard —
`[runaway-progress-exceeded]`, `[stall-killed]`, `exceeded stage_hard_cap`,
`[rate-limit-quota-exhausted]` — and the implementation it had already written
cannot be found. `feature-validate` reports that there is no implementation
work, or the retry starts from an empty branch.

**Root cause:** `feature-dev` never commits (#1608). Between its first edit and
`feature-validate` Phase 5, the entire deliverable exists only as uncommitted
worktree changes. Re-dispatching an issue force-removes the worktree, runs
`git branch -D <branch>`, and re-creates the branch from `origin/<base>`
(`WorktreeManager.create`) — so anything uncommitted, and anything committed to
that branch alone, is gone.

**Where the work is now.** Since #128 every guard kill commits the dirty tree
before the process is reaped, and anchors the commit outside `refs/heads/`:

```bash
# In the repo the issue was worked in (not the worktree — the worktree is gone):
git for-each-ref --sort=-creatordate --format='%(refname) %(creatordate:short)' \
  refs/nightgauge/wip
git show --stat <ref>          # what was preserved
git cherry-pick <ref>          # or: git checkout <ref> -- <paths>
```

Look for `[wip-preserved]` in the stage log to confirm the commit was written,
or `[wip-preserve-skipped]` with the reason it was not (clean tree, protected
branch, detached HEAD, or a git error such as a stale `index.lock`).

**Note (#134):** as of #134, `feature-validate` itself no longer reports a
false "no implementation work" in the mid-kill case where `feature-dev` wrote
its changes but was killed before writing `dev-{N}.json`. Its own Phase 0
consults `nightgauge gate verify feature-dev` and proceeds against the
git-visible diff instead of exiting when git finds evidence of work. This is
unrelated to the worktree-destruction failure mode above (re-dispatch still
force-removes the worktree) — it only fixes the case where the _current_
worktree is intact but the handoff file is missing.

**If the kill predates #128** the changes may still be sitting in the worktree
under `.nightgauge/worktrees/issue-<N>/` — provided the issue has not been
re-dispatched since. Copy them out before re-queuing the issue.

### A stage wrote into another repo's checkout (#129)

**Symptom:** the stage fails with

```
[stage:worktree-containment] Stage feature-dev wrote outside its worktree into 1 repository/repositories it does not own.
  acme-platform (/Users/you/repos/acme-platform) — 7 path(s):
    src/api/handlers.ts
    ...
    preserved: /Users/you/repos/acme/.nightgauge/containment/feature-dev-129-2026-07-26T.../acme-platform.patch
```

**Root cause:** the issue's work does not live in the repo the issue was filed
in. The agent reasons correctly about where the code is and writes there — into
that repo's live working checkout, uncommitted, on whatever branch happens to be
out. Two shapes cause it:

1. **A misfiled issue** — filed in a coordination repo that contains none of the
   relevant code. Transfer the issue to the repo that owns the code and re-run.
2. **Genuinely cross-repo acceptance criteria** — e.g. a dashboard issue whose
   criteria require an endpoint in the platform repo. File a second issue in the
   other repo and link them; one pipeline run works one repo's worktree.

Before #129 neither shape failed here. The stage exited 0, `feature-validate`
two stages later reported **"no implementation work detected"** — true of the
branch and completely misleading — and the run was billed in full and recorded
as a failure with substantial work stranded uncommitted in a repo the operator
was actively using, where a `git checkout .`, a branch switch or a `git pull`
would destroy it silently.

**Recovering the work.** Nothing in the other repo was modified, staged,
committed or reverted — the files are still on disk exactly as the stage left
them, and a patch of them is captured under the stage repo's canonical root
(which outlives the worktree a re-dispatch removes):

```bash
ls .nightgauge/containment/                      # one dir per detection
cat .nightgauge/containment/<dir>/manifest.json  # repos, paths, reason
git -C <repoPath> apply .nightgauge/containment/<dir>/<repoName>.patch
```

**`[containment-ambiguous]` is a warning, not a failure.** It means a path that
was ALREADY dirty before the stage started changed while it ran. An operator
saving their own in-progress file is indistinguishable from a stage write and is
the likelier explanation, so those paths are never attributed to the stage,
never captured, and never fail the run — they are only named so you can check
them. Only paths that went **clean → dirty** during the stage are attributed.

**`[containment-check-failed]`** means git could not be consulted (missing repo,
timeout). The check fails open: out-of-worktree writes were not verified for
that stage, but nothing else changed.

---

### Tool-call shape blindness

**Symptom.** A feature keyed on tool calls does nothing at runtime, throws
nothing, and has passing tests. Telemetry that depends on it is silently empty
or, worse, reports a clean value: `promptDetected` stuck at `false` marked
stages that had begged for interactive input as clean passes for months.

**Root cause.** The CLI delivers a tool call in one of two shapes:

| Shape                           | Parser fields                     | Who emits it                                                             |
| ------------------------------- | --------------------------------- | ------------------------------------------------------------------------ |
| complete `assistant` message    | plural `toolUses[]`               | the Claude CLI — effectively **all** live traffic                        |
| streaming `content_block_start` | singular `toolName` / `toolInput` | other adapters; needs `--include-partial-messages`, which we do not pass |

Code written against the singular shape alone is not a bug that throws — it is
a feature that never runs. That is the whole failure mode, and it is invisible
in review because the branch reads perfectly.

**Why it kept coming back.** The same dead branch was repaired three times
(#151, #154/#155, #161/#295), each fix adding a plural-shape equivalent for the
_one_ consumer that had been noticed. Nobody asked what else read it. #169
found four more, including one (`resumeSessionWithResponse`) that no issue had
ever named.

**The rule.** Never read `parsed.toolName` / `parsed.toolInput` / `parsed.toolUses`
directly. Call `collectToolCalls(parsed)` (`tokenParser.ts`), which flattens both
shapes into one list of `{name, input, id}`. In `runStageSkillHeadless` every
consumer lives inside the single `observeToolCall` body — add to that body, never
a new shape-specific branch beside it.

**Deduping is mandatory, not defensive.** A call can arrive in both shapes, so
observation dedupes on `tool_use.id` (as the bash ring has since #156). Without
it the AskUserQuestion counter advances twice per attempt and aborts at half its
threshold — a stage killed for a loop it never entered.

**Writing tests for this.** Build fixtures as **complete `assistant` messages**.
Fixtures built from `content_block_start` are exactly what let this survive three
fixes: they exercise a branch production never reaches, so the suite stays green
while the feature is dead. Verify a new assertion **fails against the unfixed
code** before trusting it — and note that a dedupe assertion can pass _vacuously_
when the feature is dead (nothing observed is trivially "observed once"), so
confirm it fails with dedupe disabled too. Give distinct calls distinct ids;
`tool_${Date.now()}` collides within a millisecond, which the CLI never does.

### `bash scripts/ci-local.sh` exits 1 with every vscode test passing (#173)

**Symptom:** `npx vitest run` (or `ci-local.sh`) intermittently exits non-zero
in `packages/nightgauge-vscode` even though the summary shows every test
passing (e.g. `11136 passed | 9 skipped`, 0 assertion failures). The real
failure is buried above the summary:

```
(node:PID) TimeoutNaNWarning: NaN is not a number.
Timeout duration was set to 1.
...
EnvironmentTeardownError: [vitest-worker]: Closing rpc while
"onUserConsoleLog" was pending
```

**Root cause:** `IpcClientBase.getTimeoutMs()`
(`src/services/IpcClientBase.ts`) read
`vscode.workspace.getConfiguration("nightgauge.backend").get<number>("timeoutSeconds", 30)`
and multiplied the result by `1000` with no validation. The lightweight
`vscode` module mock most unit tests load (`tests/setup.ts`) stubs
`getConfiguration` as `() => ({ get: vi.fn() })` — a bare `vi.fn()` with no
implementation returns `undefined` regardless of the default argument a real
`WorkspaceConfiguration.get` would honor. `undefined * 1000` is `NaN`, and
Node's `setTimeout`/`setInterval` treat a non-finite duration as `1` — the
timer (an IPC request timeout, e.g. for `config.getHealthThresholds`) fires
almost immediately. When that fires after the owning test's `afterEach` has
already run and torn down mocks/timers, its `console.log`/reject path forwards
an `onUserConsoleLog` RPC that races the vitest worker's environment teardown,
producing the `EnvironmentTeardownError` — nondeterministically, since it
depends on exact timing.

**These are two independent bugs**, and only fixing both makes the suite green.

**Fix 1 — the NaN timer.** `getTimeoutMs()` now validates with
`Number.isFinite(...)` before multiplying, falling back to the documented 30s
default — matching the pattern already used by
`AttentionSweepService.readSweepConfig()` and
`TelemetryConsentService.getUploadIntervalMinutes()`. If this recurs with a
_different_ NaN-producing timer, the fix is the same shape: validate the
duration with `Number.isFinite()` before it reaches `setTimeout`/`setInterval`,
right at the config/env read site.

**Fix 2 — the teardown race.** Removing the NaN timer alone left the suite
failing **3 runs in 10** (measured over 10 consecutive full-suite runs). The
`onUserConsoleLog` RPC is high-volume across ~11,000 tests and any line emitted
near a worker's teardown can still be in flight when the environment closes —
the NaN timer was one way to reach that state, not the only one.

Widening `teardownTimeout` does **not** fix it. That option bounds how long
teardown _hooks_ may run; it does not give an in-flight RPC longer to drain.
Measured with `teardownTimeout: 10_000` and no other change: still 3/10.

The fix is to stop creating the RPC: `disableConsoleIntercept: true` in
`vitest.config.ts`. Console output then goes straight to the terminal instead
of being forwarded to the main process for per-test attribution. Measured
after: **10/10 clean, 0 teardown errors**. The trade-off is that console lines
are no longer labelled with the test file that emitted them — they are still
printed, both stdout and stderr.

The general lesson: when a symptom is "an RPC was pending at teardown", widening
the teardown window treats the timing, not the cause. Remove the traffic.

---

### `worktree sweep` reports "No reclaimable worktrees" over obvious leaks (#332)

**Symptom.** Worktrees are visibly piling up, and every one is skipped:

```text
$ nightgauge worktree sweep --dry-run
No reclaimable worktrees (scanned 7)
  skipped  .worktrees/issue-1181  (uncommitted-changes)
  skipped  .worktrees/issue-1252  (uncommitted-changes)
```

`git branch -D` also refuses their branches ("checked out at …"), so cleanup is
blocked at both ends. `nightgauge doctor` reports nothing.

**Root cause.** The worktree's only change is a file _the pipeline itself
wrote_ — usually `.nightgauge/knowledge/README.md`, scaffolded at issue pickup:

```text
$ git -C .worktrees/issue-1181 status --porcelain
?? .nightgauge/knowledge/README.md
```

It is untracked because that repo's `.nightgauge/.gitignore` has no
`/knowledge/` rule. Pre-#326 the file was hand-written per repo; #326 made
`ensureGitignore.ts` the single source but only the **primary** repo ever
received the generated copy, so every sibling kept a stale one. Nothing removes
the scaffold, so the skip was permanent.

**Fixed in #332** on three levels — the sweep classifies untracked bookkeeping
as exhaust rather than as a blocker, the extension propagates the generated
`.gitignore` to every repo in the workspace manifest, and `doctor` now reports
stale worktrees with the paths that blocked them.

**If you see it on an older binary:** delete the scaffold and re-run the sweep,
but guard on `git ls-files --error-unmatch <path>` first — `knowledge/README.md`
is untracked exhaust in most worktrees and **tracked content** in some. Deleting
a tracked one produces a staged deletion, and the removal then refuses for a
genuinely different reason.

**Do not "just exclude `.nightgauge`".** A staged change to a tracked file under
`.nightgauge/` can be the whole deliverable — #701's was 209 staged deletions
under `.nightgauge/pipeline/assessments/`. The tracked/untracked distinction is
what separates the two, and it is why `sweep` still refuses that worktree.

### Stashes accumulating in sibling repos (#330)

**Symptom.** `git stash list` in a repo the pipeline has worked shows entries
nobody created by hand, sometimes months old.

**Root cause.** A stage stashes to run a test suite against a clean tree and
pops afterwards. A killed stage never reaches the pop — no `trap` or `defer`
survives a SIGKILL — and before #330 the stash carried no identifying message,
so no tool could tell it from the operator's own and none would touch it.

**Now:** pipeline stashes carry `nightgauge:<purpose>:<issue>:<stage>`.

```bash
git stash list | grep nightgauge:     # find them
nightgauge stash sweep --dry-run      # classify
nightgauge stash sweep                # restore (pop) them
```

`nightgauge doctor` reports them per repo with age, and a killed stage's
stage-exit record names what it stranded in `unreclaimed_stashes`.

**A stash with no marker is never touched by any of this** — that includes
pre-#330 pipeline stashes. Ownership cannot be proven from a free-form message,
so they are reported as `unowned` and left for you to decide on.

---

### VSCode host tier dies at "Resolving version…" (#770)

Symptom, from the host smoke job:

```
VSCode host smoke tier: workspace /tmp/nightgauge-host-XXXXXX
- Resolving version...
ERROR: VSCode failed to launch. Underlying error:
AggregateError [ETIMEDOUT]:
    at internalConnectMultiple (node:net:1339:18)
    at Timeout.internalConnectMultipleTimeout (node:net:1969:5)
ERROR: the in-host test module never wrote its transcript.
```

**Root cause**: the runner could not open a TCP connection to
`update.code.visualstudio.com`, which `@vscode/test-electron` contacts to
resolve the VSCode version before downloading it. Roughly 300ms elapses between
the two lines — that is the Happy Eyeballs attempt timeout expiring with every
candidate address unreachable, not a slow or truncated download. Nothing in the
repository is at fault and the change under test is irrelevant.

**Mitigation already in place**: `tests/launcher/acquireVSCode.ts` retries the
acquisition three times (2s then 6s backoff) and logs each failed attempt.
`runTests()` is handed the resolved executable and performs no network I/O, so
it is **not** retried — a retry there could turn a failing assertion into a
green run, which is the failure mode this tier exists to prevent.

**Therefore**: a single `WARN: acquiring VSCode failed` line followed by a pass
is this working as designed and needs no action. Three of them followed by
`ERROR: could not acquire VSCode after 3 attempts` is a real outage — check
whether the update service is reachable before re-running, and do not treat it
as a flaky test.

**Deliberately not done**: caching or pinning the VSCode build. Neither helps.
The version is resolved over the network on every run before the cache is
consulted, so a cache does not remove the failing call; pinning would remove it
but would also stop the tier from answering the question it exists to answer —
does the extension come up in the VSCode people actually have.

### A tree view collapses every expanded node on refresh (#1277)

**Symptom**: expand a repository, its `Ready: N issues` bucket and an epic
under it; switch editor tabs, press refresh, or let a count change — every
node below the row snaps shut.

**Cause**: VS Code preserves expansion across `onDidChangeTreeData` only for
nodes whose `TreeItem.id` is set and unchanged. Without an id it keys identity
on `parentHandle/index:label`, so a label that carries live data ("Ready: 12
issues", "(blocked)", "(empty)") renames the node on the very refresh that
changes it, VS Code treats it as new, and the constructor's hard-coded
`Collapsed` wins. The Repositories tree set no ids at all until #1277.

**Rule**: every item class rendered by a tree sets `this.id` from what the node
IS (repo name, status bucket, epic number, issue number) and never from what it
holds — no counts, suffixes, timestamps or array indexes. Ids must be unique
tree-wide, so a shared item class takes a `parentId` option and derives
`<parentId>/epic:N` / `<parentId>/issue:M`: the same epic legitimately appears
under both the Ready and Backlog buckets of one repository. Repaint a cached
row in place (`applyHaltState`, `applyActiveState`, `applyConcurrency`) and fire
with that element; reserve `fire(undefined)` for structural changes. Pinned by
`tests/views/items/stableTreeIds.test.ts` and the `stable ids across refresh`
block in `tests/views/RepositoriesTreeProvider.test.ts`.

**Related quota fact**: the same tree's per-row `board.counts` read was cached
per `ProjectBoardService` instance while item lists were shared per board, so N
repositories on one board cost N counts queries per cold refresh. Counts now
live in `BoardSnapshotStore` under `COUNTS_SCOPE`; if the API ledger ever shows
more than one `board.counts` per board per refresh, something is bypassing the
store. Daemon-side, `board.counts` is no longer a query at all: it is derived
from the cached open-item snapshot (`docs/GO_BINARY.md` § _The Board Snapshot
Cache_), so a ledger showing five-alias `totalCount` documents means an old
binary.

## Local Validation Traps

Failures that read as success, and successes that read as failure. Every one
below has cost a session at least once.

### Reading `ci-local.sh`'s result — its own exit code, not a wrapper's

**The script's exit code is authoritative**: `scripts/ci-local.sh` ends in an
explicit `exit 0` when `FAIL_COUNT` is zero and `exit 1` otherwise. An earlier
revision of this page claimed the exit code "does not reflect whether the
checks passed"; that has not been true for some time, and believing it costs
you the single most reliable signal the run produces.

What actually goes wrong is **reading someone else's exit code.** Backgrounding
the gate as a compound command reports the status of the LAST command in the
chain, not the script's:

```bash
# WRONG — reports the echo's status (always 0), whatever the gate did
bash scripts/ci-local.sh > gate.log 2>&1; echo "EXIT=$?"

# RIGHT — the script is the last command; $? is its own
bash scripts/ci-local.sh > gate.log 2>&1
echo "CI_LOCAL_EXIT=$?"
```

This is not hypothetical: a session read the first shape, reported a gate that
had failed 109 tests as passing, and pushed on it.

Confirm against the log's own verdict line, which is unambiguous either way:

```bash
grep -qE "^✓ All CI-parity checks passed" gate.log && echo PASS || echo FAIL
```

**Do not judge by tailing the log.** On failure the script prints remediation
advice last ("Fix the failures before pushing. Most format/lint failures are
auto-fixable…"), so a tail of a FAILED run ends in friendly text that reads
like success. The failure summary — `✗ N check(s) failed:` with each step and
its log path — sits above that.

A full run on this tree takes **10–15 minutes**, not the three this page used
to claim: `go test ./...` plus `-race`, both TypeScript workspaces (~12,500
extension tests), the plugin-skills mirror regeneration and several network
calls. That far exceeds the foreground tool-call timeout in most agent
harnesses. **Background it and poll.** A foreground timeout SIGKILLs the job,
and a SIGKILLed run leaks a worktree registration that `git worktree prune`
structurally cannot clear — prune only removes entries whose directory is gone,
and on SIGKILL the directory survives.

**Background it in the right directory.** A backgrounded command's `cd` does
not persist to later calls in some harnesses, so a gate launched with a bare
`bash scripts/ci-local.sh` can run against the DEFAULT worktree while you
believe it is testing your branch — a green result for code you did not
change. Put the `cd` in the same command and have it print `pwd`, or verify
with `lsof -a -d cwd -p "$(pgrep -f '[c]i-local.sh')"`.

### `vitest` in a worktree fails until the SDK is built

`packages/nightgauge-vscode/tests/setup.ts` imports `@nightgauge/sdk` from
`dist`, not from source. A fresh worktree has no `dist`:

```bash
npm run build --workspaces --if-present
```

The same applies after any `cp`-restore of SDK `src` during mutation testing —
that makes `dist` stale, and the pretest freshness hook will correctly block the
run until you rebuild with `npm run build -w @nightgauge/sdk`.

### File mtimes tell you nothing about who last edited a file

Pull and rebase run with `--autostash`, which rewrites the mtime of every file
in the tree on every pull. An mtime-based "who touched this last" hypothesis is
worthless here; use `git log` instead.

### A diff against a branch also reports what the branch is BEHIND

`git diff origin/main..HEAD` answers "how do these two trees differ", which is
not the question "what did my work change" — it also reports everything
`origin/main` gained while you were working. On a busy repository that is
routinely another session's files, and it reads exactly like _you_ swept up
someone else's work.

A session hit this after a concurrent PR merged: the diff listed 18 files
including a `board_services.go` it had never opened. Nothing was wrong;
`origin/main` had simply moved.

**Compare against the commit's own parent before concluding anything:**

```bash
git show --stat HEAD          # what this commit changed, vs its own parent
git diff --stat @{u}...HEAD   # three dots: your side of the fork only
```

`git show` cannot drift, because a commit's parent never moves. Reach for it
first whenever a diff surprises you — the cheap explanation is usually that
your reference point moved, not that your tree is wrong.

### `gh` sometimes 503s _after_ succeeding

A failed `gh pr create` may still have created the PR. **Check before
retrying** — a blind retry produces a duplicate.

Relatedly, `gh issue list/view --json` is GraphQL-backed and shares the hourly
5,000-point GraphQL budget; two agents can exhaust it in about ten minutes, and
an exhausted budget returns mid-stream HTML that breaks `jq` rather than a clean
error. `gh api /repos/{owner}/{repo}/issues` is REST, on a separate and much
larger budget. Never `--paginate` a whole project-items connection.

### Never `git fetch --prune` before `git branch -d`

Pruning drops the remote-tracking ref that lets `-d` recognize a squash merge.
After a prune, `-d` refuses, and since `git branch -D` is blocked by the
destructive-operation guard, prune-first turns routine teardown into an operator
ask. Delete the branch first, prune second.

See [GIT_WORKFLOW.md § After Merge](GIT_WORKFLOW.md#after-merge) and
`scripts/branch-merged-check.sh` for deciding whether a branch is safe to delete
at all.

### Mojibake is a shipped defect, and nothing else in the toolchain catches it

`internal/preflight/source_encoding_test.go` guards this. Text that was UTF-8,
read back as Latin-1, and re-encoded (`—` = `e2 80 94` → `c3 a2 c2 80 c2 94`)
produces **valid UTF-8**, so `gofmt`, `go vet`, ESLint, Prettier and the entire
test suite pass. It is invisible until a human reads the output. Sixteen such
characters once survived 25 commits in `internal/orchestrator/`, two of them in
operator-facing runtime strings — so every attention card down that path shipped
a corrupted character to a person.

- `TestSourceFilesAreCleanUTF8` keys on a **C1 control (U+0080–U+009F)**, which
  is what the Latin-1 round trip always produces and which real source never
  contains. That makes it proof, not a heuristic. Its failure message reverses
  the round trip so it tells you what the text was meant to say.
- `TestGoCommentsHaveNoLiteralUnicodeEscapes` catches the ASCII-only variant (a
  literal `\u2014` in comment prose), scoped to comments via `go/parser`
  because the same escape inside a string literal is ordinary Go.

**If one fails, repair the bytes** (utf-8 decode → latin-1 encode → utf-8
decode). Do not retype the line by hand, and do not exempt the file.

### A fresh worktree fails three packaging suites until `build:assets` runs

`claudeAgentSdkPackaging`, `modelRegistryPackaging` and `failureTaxonomyPackaging`
assert that files exist under `packages/nightgauge-vscode/dist/`. In a worktree
where you have run `npm install` and `npm run build --workspace=@nightgauge/sdk`
— the two steps the handoff and `AGENTS.md` tell you to run — those files are
still absent, and five tests fail:

```
× dist/model-registry.json is present after build
× dist/failure-taxonomy.yaml is present after build
× dist copy deep-equals the SDK source — a stale build:assets output is a red test (#436)
× dist copy parses under the packaged strict schema — the load-time-crash class as a test (#436)
× does not claim the optional Agent SDK as redistributed software
```

They are **environmental, not your diff**. `ci-local.sh` runs the extension build
(and with it `build:assets`), so they pass in the gate and in CI.

**Confirm before attributing, and confirm the cheap way**: `git stash push -u`,
re-run the same suites against the clean tree, `git stash pop`. If they fail
identically with your change stashed, they are pre-existing. This costs about
thirty seconds and is the difference between "my change broke packaging" and
"this worktree has never been built" — a distinction that reads identically in
the failure output.

### Go silently excludes a file whose name ends in a GOARCH suffix

A new file called `cadence_arm.go` compiled **nowhere**. Go reads `_arm` as the
GOARCH build constraint for 32-bit ARM, so the file was excluded from its own
package on every other platform — and `go build ./...` exited 0, because a file
that is not in the package cannot fail to compile.

The symptom is `undefined: <yourNewFunction>` from a _caller_ while the function
is plainly there in the file you just wrote. Confirm with:

```bash
go list -f '{{.IgnoredGoFiles}}' ./internal/yourpkg/
```

An excluded file appears there. The reserved suffixes are the GOOS and GOARCH
values — `_arm`, `_386`, `_amd64`, `_windows`, `_linux`, `_darwin`, `_js` and the
rest — so avoid ending a filename with `_` plus any platform word. Rename rather
than adding a build tag: the name is the bug.

This is worth knowing because the failure mode is **dead code that every gate
accepts**. Build, vet, and test all pass; the code simply is not there.

### The publication-boundary regression suite sandboxes at `HEAD`

`scripts/test-publication-boundary.sh` checks out a sandbox at `HEAD`, which
deliberately **excludes uncommitted work**. Running it mid-change therefore
measures the PRE-change tree while reading your working-tree manifest, and the
two disagree.

The visible symptom is a false ratchet violation: `UNRESOLVABLE REFERENCE COUNT
ROSE: 5983 > baseline 5977`, where 5977 is the number you correctly measured
against your own tree seconds earlier. Nothing is wrong; the suite is comparing
your new baseline against the old content.

**Commit first, then run the suite.** `python3 scripts/publication-boundary-check.py`
alone does read the working tree, so use that while iterating and the suite as
the gate.

### `timeout` does not exist on macOS

`timeout 900 gh pr checks --watch` exits **127** immediately with
`command not found`, and a backgrounded wrapper reports that as its own exit
status — so a watch that never ran can look like a watch that completed. GNU
coreutils ships it as `gtimeout` if installed. Prefer a bounded poll loop, and
always read the log body rather than the exit code alone.

### A poll loop must distinguish "false" from "I could not measure it"

The natural form of a wait-for-CI loop exits on the wrong condition:

```bash
# WRONG — exits when `gh` ERRORS, not when the checks settle
until ! gh pr checks "$N" --jq 'any(.[]; .bucket=="pending")' | grep -q true; do
  sleep 30
done
echo SETTLED
```

A failed command produces no output, so `grep` finds no `true`, so the loop
exits and reports `SETTLED`. During a GitHub API outage this printed `SETTLED`
while the PR still had two pending checks — the loop reported a state it had
never observed.

Capture the output, bail to a `sleep` on a non-zero exit, and compare against
the literal `false` rather than testing for the absence of `true`:

```bash
out=$(gh pr checks "$N" --json bucket --jq 'any(.[]; .bucket=="pending")' 2>/dev/null) \
  || { sleep 30; continue; }
[ "$out" = "false" ] && break
```

Bound the loop with an iteration count as well, so an endpoint that stays
unreachable fails loudly instead of spinning.

This is the same family as `ci-local.sh`'s exit code and `timeout`'s 127 above:
**the wrapper's exit status is not the measurement.** Any check whose "pass"
branch is reachable by the command failing is not a check.

## VSCode Extension Diagnostics

### Where to look when something fails

**Look in the "Nightgauge" output channel first — it is the one destination
every extension-side diagnostic now folds into (#749).** Before this, the
extension split diagnostics across six separately-named channels, and
knowing which one carried a given failure was implicit tribal knowledge: a
failed platform dashboard tab logged to `Nightgauge`, IPC transport errors
logged to `Nightgauge Go Backend`, and `Nightgauge Pipeline` — the name an
operator reaches for first — carried neither. That gap is exactly how a
structural auth failure (#742, the Go daemon never receiving the signed-in
session token) survived to a release candidate: the maintainer hit a failed
tab, checked the output window, found nothing there, and reasonably
concluded the retry button was dead.

**Command Palette → `Nightgauge: Show Diagnostics`** reveals the channel and
prints a one-shot snapshot:

- platform connectivity (connected / degraded / offline / disabled)
- which **kind** of credential the Go daemon is currently using — `session
(signed in)`, `license key`, or `none configured` — **never the value**
- the resolved Go binary's path and version
- the last transport error recorded for each platform-backed dashboard
  surface (Health, Trends, Cost, Runs, Compliance, Quota/Usage)

Every line printed to the channel — including the snapshot — passes through
`redactSecrets()` (see `packages/nightgauge-vscode/src/utils/redaction.ts`)
before it reaches the output window, so a token or license key cannot appear
there even if it leaked into an upstream error string.

### "credential insufficient" on Health / Trends / Cost / Compliance

**Symptom.** A signed-in user sees the analytics tabs fail, and the
`Nightgauge` channel carries an `[ipc]` line like:

```text
[WARN] [ipc] platform.getAnalyticsHealth: IPC error -32603: ...
  credential insufficient for operation:
  holding a license_key, operation requires a user-scoped session token
```

Signing out and back in fixes it — until the next window, when it returns.

**What the message actually proves.** These four routes are user-scoped and
reject an account-scoped license key. The Go client picks its bearer in the
order sessionToken → staticAPIKey → licenseKey, so this exact wording means the
daemon's in-memory `sessionToken` is the **empty string**. An _expired_ JWT
would still classify as a user token and fail differently. So this is not a
refresh-expiry problem; something pushed an empty credential.

**Root cause (fixed, #797).** Platform credentials are stored per host, under `nightgauge.platform.{hostKey}.{field}`. `resolvePlatformHostKey()`
returned the preset _name_ when `platform.environment` was set explicitly, but
fell through to the endpoint _hostname_ otherwise — so one endpoint had two
keys, `production` and `api.nightgauge.dev`. `TokenStorage` is constructed
during bootstrap **before** `ConfigBridge` has loaded any config, so a read in
that window resolved to the other bucket, returned `null`, and
`PlatformCredentialBridge` pushed `""` to the daemon. Nothing re-pushed
afterwards — the bridge only listened for token _writes_ — so the daemon stayed
on its license key until a manual sign-out/sign-in wrote a token again.

The same divergence fired `onPlatformHostChanged` on every activation, which
`SessionManager` turns into an `unauthenticated` transition — the spurious
sign-outs users saw alongside the failing tabs.

The fix makes the key a pure function of the resolved endpoint (any config
resolving to a preset URL yields that preset's name), has `TokenStorage`
announce a re-key when the host genuinely moves, and has the credential bridge
re-read storage on that signal. Tokens stranded in a hostname-keyed bucket are
migrated on activation, so the fix does not itself force a re-login.

**If you see this on a build that has the fix**, check
`Nightgauge: Show Diagnostics` — the credential line reports `license key`
rather than `session (signed in)` whenever the daemon lacks a session token,
which distinguishes a credential-plumbing failure from a genuine 401.

### HTTP 422 on a platform tab ("may be transient — retry")

**Symptom.** A signed-in user sees, most visibly on the Cost tab:

```text
The platform returned an error (HTTP 422) for platform.getCostAnalytics.
This may be transient — retry.
```

Retrying never works.

**The message was wrong, and that matters.** A 422 is _Validation error_ — the
request we sent is malformed. It is a Nightgauge bug, not an outage, and no
number of retries can fix it. Every non-401/403 status used to be bucketed as
`server_error`, whose copy invites a retry; 4xx now classifies as `bad_request`,
renders no retry button, and names the endpoint and status so it can be
reported.

**Root cause (fixed, #800).** `/v1/analytics/cost` declares `startDate` and
`endDate` as `format: date-time`. The Cost tab built them with
`.toISOString().slice(0, 10)`, sending a bare calendar date (`2026-07-23`),
which fails that validation. The Go client now normalises both bounds to
RFC 3339, widening to whole days so the requested window stays inclusive at
both ends.

**Why nothing caught it.** `GetCostAnalytics` had no unit test, and
`scripts/staging-platform-smoke.sh` called `/v1/analytics/cost` with **no query
string at all** — so the live canary exercised a request the extension never
makes, and stayed green while every real Cost tab load failed.

> **An endpoint exercised only without its parameters is not covered.** When a
> tab fails against the real platform but every test tier is green, compare what
> the client puts on the wire against the platform's published contract at
> `/docs/openapi.json` — not against our stubs, which encode what we _believe_
> the API does. That belief being wrong is the recurring root cause here: it was
> #742 (credential class), then the Health tab response shape, then this.

### What happened to the six channels

| Former channel            | Now                                                                                                                                                                                                                                                                                                                    |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Nightgauge`              | Unchanged — this **is** the shared main channel everything else now folds into.                                                                                                                                                                                                                                        |
| `Nightgauge Autonomous`   | Folded in, tagged `[autonomous]`.                                                                                                                                                                                                                                                                                      |
| `Nightgauge Codex Setup`  | Folded in, tagged `[codex-setup]`.                                                                                                                                                                                                                                                                                     |
| `Nightgauge Pipeline`     | Folded in, tagged `[pipeline]` (project board sync / autonomous debug dumps).                                                                                                                                                                                                                                          |
| `Nightgauge Plugin Setup` | Folded in, tagged `[plugin-setup]`.                                                                                                                                                                                                                                                                                    |
| `Nightgauge Go Backend`   | **Kept separate** — the raw, unfiltered Go IPC transport log. Any error from a `call()` that a user-visible surface can hit is _also_ mirrored into `Nightgauge`, tagged `[ipc]`, with the failing endpoint and (when parseable) HTTP status — so you don't need to already know to open the transport log to find it. |

If a fix needs the raw, unfiltered process I/O — a hung spawn, a malformed
JSON-RPC frame, restart backoff timing — that detail is still only in
`Nightgauge Go Backend`. Everything else — dashboard fetch failures, setup
flows, autonomous mode, project board sync — is in `Nightgauge`.

## Getting Help

If you can't resolve an issue:

1. **Check GitHub Issues:** Search for existing issues or solutions.

2. **Create a new issue:** Include:
   - What you tried
   - Expected vs. actual behavior
   - Relevant configuration files
   - Error messages

3. **Reference documentation:**
   - [nightgauge/nightgauge](https://github.com/nightgauge/nightgauge)
   - [AGENTS.md standard](https://agents.md/)

## Author

nightgauge
