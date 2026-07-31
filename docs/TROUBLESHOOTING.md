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

### Reloading the extension mid-run marks healthy stages failed

Stale-slot recovery on activation can SIGTERM any stage alive longer than the
threshold (elapsed, not idle) and record it as failed with an empty
`terminal_kind`. **Do not Reload Window while a stage is mid-run.** A "stage
failed with no error text" right after a reload is almost always this.

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
green. (Per [no flaky dismissal](../CLAUDE.md#agent-operating-rules): confirm
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
