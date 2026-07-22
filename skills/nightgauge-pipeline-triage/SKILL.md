---
name: nightgauge-pipeline-triage
description: Triage recent pipeline failures end-to-end — distinguish FALSE alarms (work already merged) from real failures, classify the root cause against a known-failure taxonomy, and either apply the safe operational fix (re-queue, clear false pause/cooldown) or report a precise diagnosis. Use whenever the pipeline reports a failure or autonomous mode is paused.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
---

# Nightgauge Pipeline Triage

## Description

One command to triage (and where safe, fix) pipeline failures. It replaces the
manual loop of "paste an error → ask Claude → dig through logs." It is
**reconciliation-first**: many reported failures are false alarms where the work
actually completed (PR merged, issue closed) but a stage exited non-zero on a
secondary error. This skill checks that FIRST, so you never chase ghosts.

**Use when:**

- A pipeline failure notification arrives ("Issue #N failed at <stage> — $X").
- Autonomous mode is paused after a slot failure.
- You want a health sweep of recent failures before resuming autonomous.

## Arguments

| Arg                 | Meaning                                                                                                                                                       | Default                   |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------- |
| `--issue N`         | Triage a specific issue only                                                                                                                                  | all recent failures       |
| `--since DATE`      | Only failures on/after `YYYY-MM-DD`                                                                                                                           | last 2 days               |
| `--limit N`         | Max failures to triage                                                                                                                                        | 15                        |
| `--fix`             | Apply safe operational fixes (re-queue recovered/stuck issues, clear false pause/cooldown). Without it, triage is read-only and only reports recommendations. | off (report-only)         |
| `--repo OWNER/REPO` | Restrict to one repo                                                                                                                                          | all repos in exit-records |

## Output contract

For each failure, emit one block:

```
#<issue> <stage>  —  <VERDICT>
  cost: $X.XX   when: <ts>   repo: <owner/repo>
  → <one-line root cause>
  → action: <what was done / what to do>
```

`VERDICT` is exactly one of: `FALSE-ALARM (already merged)`,
`FALSE-ALARM (already closed)`, `TRANSIENT (retryable)`,
`REAL — operational (fixed)`, `REAL — operational (action needed)`,
`REAL — code bug (issue filed)`, `UNKNOWN (needs human)`.

End with a SUMMARY line: counts per verdict, and the recommended next step
(resume autonomous / fix X / nothing — all recovered).

---

## Workflow

> Deterministic Bash/Python for all data extraction and GitHub reconciliation.
> The model is used ONLY to classify the session-log signature and choose the
> action. Never invent a failure — every verdict must be backed by a log line or
> a GitHub state check quoted in the output.

### Phase 1 — Locate failures (deterministic)

Read failure records from exit-records, newest first:

```bash
python3 - "$ARGUMENTS" <<'PY'
import json, glob, sys, os, datetime
args = " ".join(sys.argv[1:])
def flag(name, default=None):
    parts = args.split()
    if name in parts:
        i = parts.index(name)
        return parts[i+1] if i+1 < len(parts) else True
    return default
issue   = flag("--issue")
since   = flag("--since")
limit   = int(flag("--limit", 15))
repo    = flag("--repo")
if not since:
    since = (datetime.date.today() - datetime.timedelta(days=2)).isoformat()
rows=[]
for f in sorted(glob.glob('.nightgauge/pipeline/exit-records/*.jsonl')):
    day = os.path.basename(f).replace('.jsonl','')
    if day < since: continue
    for line in open(f):
        line=line.strip()
        if not line: continue
        try: o=json.loads(line)
        except: continue
        if o.get('success') is not False: continue
        n = str(o.get('issue') or o.get('issueNumber') or '')
        if issue and n != str(issue): continue
        if repo and o.get('repo') != repo: continue
        rows.append({"issue":n,"stage":o.get('stage','?'),"ts":o.get('ts',''),
                     "repo":o.get('repo',''),"run":o.get('run_id','')})
rows = rows[-limit:]
print(json.dumps(rows, indent=1))
PY
```

If zero rows: report "No pipeline failures in window" and exit.

### Phase 2 — Reconcile against GitHub FIRST (deterministic, the key step)

For each failed issue, before reading any log, check whether the work actually
landed. This catches the dominant false-alarm class (#3835).

Use the forge abstraction (ADR-008) — never raw `gh` (the GitLab CI slot and the
`no-direct-gh` lint both depend on it). The pipeline closes the issue when its PR
merges, so issue state is the strongest single signal.

```bash
# 1) Issue state — CLOSED almost always means the PR merged and the work landed.
nightgauge forge issue view N --repo OWNER/REPO --json \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('state',''))"

# 2) PR evidence — the pr-create stage records the PR it opened in context.
python3 -c "import json;d=json.load(open('.nightgauge/pipeline/pr-N.json'));print(d.get('pr_number'),d.get('pr_url'))" 2>/dev/null

# 3) Confirm the merge via the feature branch (branch is in the exit-record / dev context).
nightgauge forge pr list --head <branch> --state all --repo OWNER/REPO --json \
  | python3 -c "import json,sys;[print(p.get('number'),p.get('state')) for p in json.load(sys.stdin)]"
```

- Issue `CLOSED`, or a PR for the branch in state `merged` → **VERDICT:
  FALSE-ALARM**. The stage exited non-zero after the work landed. No code action.
  If `--fix`: ensure the issue is out of the queue and (if this was the pause
  trigger) the pause can be cleared. Quote the PR number / merge state as evidence.

Only issues that are still `OPEN` with no merged PR proceed to Phase 3.

### Phase 3 — Classify genuine failures (model, log-backed)

Read the session log and match against the known-failure taxonomy. Read in this
order; stop at the first match:

```bash
LOG=$(ls -t .nightgauge/logs/*_<N>_session.log 2>/dev/null | head -1)
grep -niE 'api_error_status|cannot be modified|rate.?limit|429|Force push blocked|Destructive git|"result":"|I asked but|push.failed|Cannot find module|node_modules|stale_sdk_dist' "$LOG" | tail -25
```

**Known-failure taxonomy** (cross-references in `.claude` memory):

| Signature in log                                                                            | Class                            | Verdict & action                                                                                                                                                                                                                                                                                                                 |
| ------------------------------------------------------------------------------------------- | -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `thinking`/`redacted_thinking` blocks `cannot be modified` (400)                            | stale claude CLI (#3801)         | STALE CLI. The forced `CLAUDE_CODE_DISABLE_THINKING=1` workaround was retired after the bug stopped reproducing on CLI 2.1.186 (#73, spike §8.2). Fix: upgrade the claude CLI. Stopgap: `export CLAUDE_CODE_DISABLE_THINKING=1` (spawns inherit the environment).                                                                |
| `api_error_status":429` / `rate limit` with `status:"allowed"`                              | TRANSIENT quota / false cooldown | TRANSIENT. Not a code failure. If a global cooldown was set off a `status=allowed` event, it's the false-positive class — recommend "Autonomous: Clear Quota Cooldown". Re-queue.                                                                                                                                                |
| `socket connection was closed` / `socket hang up` in a result envelope                      | network blip (Anthropic)         | TRANSIENT (#4002, kind `api_connection_lost`). Local network/DNS blip killed the stream. Auto-retries with ~5m backoff, no pause. If the queue paused anyway, the extension predates the fix — resume.                                                                                                                           |
| `github-auth-failed` with `error connecting to api.github.com` in the same window           | network blip (GitHub preflight)  | TRANSIENT (#4002, kind `github_network_outage`). NOT an auth failure — the preflight auth re-check passes on its own once connectivity is back. Auto-defers with a ~2m global cooldown, no pause. Never recommend re-authenticating for this signature.                                                                          |
| `Force push blocked` / `Destructive git` / push `rejected` / `I asked but didn't get a sel` | pr-create git mechanics          | REAL — operational. The branch likely already pushed (feature-dev). Check `nightgauge forge pr list --head <branch> --state all`; if a PR exists or the remote branch has the work, the deterministic idempotency (#3828) should now handle it — re-queue. If a PR can be opened from the existing branch, recommend opening it. |
| `Cannot find module @anthropic-ai/...` / `stale_sdk_dist` / empty `node_modules`            | worktree env                     | REAL — operational. The `.worktrees/issue-N` shipped without node_modules. Recommend symlinking canonical node_modules (root + per-pkg). Re-queue.                                                                                                                                                                               |
| `stage gate failed:` with the work present on GitHub                                        | gate false-negative              | Re-run Phase 2 — likely a FALSE-ALARM the gate missed.                                                                                                                                                                                                                                                                           |
| none of the above                                                                           | UNKNOWN                          | Quote the last 10 non-empty log lines. Verdict UNKNOWN — needs human. Do NOT guess a fix.                                                                                                                                                                                                                                        |

Record the matched signature and the exact log line as evidence.

### Phase 4 — Act (only with `--fix`, and only safe operations)

Safe, reversible operational actions this skill MAY take with `--fix`:

- **Re-queue** a recovered or transient issue: set its board status back to Ready
  (`nightgauge project sync-status <N> ready`).
- **Clear a false pause / quota cooldown**: surface the exact command for the
  user to run ("Autonomous: Clear Quota Cooldown"); do not edit `state.json` by
  hand (see [[quota_cooldown_false_positive]]).
- **Open a PR** from an already-pushed branch when Phase 3 shows the work is
  complete but no PR exists.

NEVER, even with `--fix`:

- Force-push, reset, or any destructive git op (the safety hooks block these for
  a reason).
- Merge a PR (let the pr-merge stage handle merges).
- Edit pipeline state files directly.
- "Fix" a code-level root cause by improvising — instead file a precise issue
  (`nightgauge forge issue create`) with the log evidence, and sync it to
  the board (`nightgauge project add`).

For code bugs: file ONE issue per distinct root cause, sync to project board,
and reference it in the output. Do not file duplicates — search first.

### Phase 5 — Summary

Print the SUMMARY line and the single recommended next step. If every failure was
a FALSE-ALARM or TRANSIENT, say so explicitly and recommend resuming autonomous.

---

## Notes / gotchas

- macOS has no `timeout`; do not rely on it in Bash blocks.
- Session logs **truncate long lines** — grep for substrings, do not `json.loads`
  whole lines.
- Always quote evidence (a log line or a GitHub state) for every verdict — an
  unbacked verdict is a bug in this skill.
- Cost figures come from the exit-record / notification, not recomputed.

## Credits

Part of [Nightgauge](https://github.com/nightgauge/nightgauge) — Issue-to-PR pipeline. Built from the failure taxonomy in epic #3835.
