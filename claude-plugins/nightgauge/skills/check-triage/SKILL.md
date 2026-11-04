---
name: check-triage
description: Triage a failing CI check that no issue exists for — reproduce it, observe the running system, then fix it, in that order. Refuses to propose a fix it could not reproduce, files a type:spike instead, and emits a triage record naming the hypotheses it ruled out and the observation that ruled each one out. Use when a named check, workflow run or workflow file is red and nothing is tracking it.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
disable-model-invocation: true
---

# Nightgauge Check Triage

## Description

Every other skill in this repository is issue-driven. An unowned red CI job has
no issue behind it, so no pipeline stage can act on it — which is how a nightly
E2E sweep stayed red on **every run for five weeks** while merge after merge
went green, the sweep being a non-required check.

This is the entry point for "this check is failing — find out why and fix it."

**It is disciplined about the order** because the two earlier attempts at that
same investigation were not. Both produced confident diagnoses derived from
reading source rather than observing the running system, and both shipped code
encoding those guesses:

- a **magic-link redelivery retry** — deliver the link, and if nothing happens
  in 25 seconds, deliver it again. Documented as diagnostic rather than a cure.
  It would have re-hidden the real bug the moment that bug was fixed, converting
  a genuine regression of "one tap signs you in" back into a green run.
- a **cold-restart probe** that reported `no session was persisted, so
verification genuinely never completed`. Verification had succeeded and the
  session _had_ persisted. The probe reported the opposite of the truth and sent
  the following session down the wrong path for its entire duration.

Neither was caught in review, because both were framed as instrumentation. The
order below — reproduce, observe, then fix — is what separates them from a real
diagnosis, and the triage record is what makes the difference checkable instead
of a matter of tone.

**Use when:**

- A named check is red and no issue tracks it.
- A scheduled workflow (nightly sweep, cron job) has been failing and nobody has
  looked.
- `nightgauge attention` or a default-branch health card surfaces a red check
  and someone has to go find out why.

**Do not use for** a pipeline stage failure with an issue behind it — that is
`/nightgauge-pipeline-triage`, which is reconciliation-first because most such
reports are false alarms. This skill assumes the failure is real, because a
check that has been red for weeks is not a false alarm.

## Arguments

| Arg                 | Meaning                                                                    | Default        |
| ------------------- | -------------------------------------------------------------------------- | -------------- |
| `--check NAME`      | The failing check's display name, e.g. `E2E Sweep`                         | —              |
| `--run URL`         | A workflow run URL, when you have one to start from                        | —              |
| `--workflow FILE`   | Workflow file or path, e.g. `e2e.yml`                                      | —              |
| `--repo OWNER/REPO` | Repository the check belongs to (**required**)                             | —              |
| `--branch NAME`     | Branch to examine history on                                               | default branch |
| `--no-fix`          | Stop after the observe phase; produce the diagnosis and the record, no fix | off            |

One of `--check`, `--run` or `--workflow` is required. `--repo` always is: a
check name means nothing without the repository it belongs to.

## Output contract

The run ends with exactly one of:

```
DIAGNOSED — <one-line cause>
  reproduced: local | ci
  ruled out:  <hypothesis> — <the observation that killed it>
  cause:      product_code | skill_instruction | agent_config
  fix:        <branch> / <PR>   test: <name> (red without the fix: yes|no — <why not>)
  scenario:   evals/scenarios/<skill>/<file> (red pre-fix: yes)   [skill causes only]
  record:     <checkout>/triage/checks/<id>.json
  tracked:    <issue URL>
```

```
NOT REPRODUCED — no fix proposed
  tried:   <n> approaches (see the record)
  spike:   <issue URL>
  record:  <checkout>/triage/checks/<id>.json
```

There is no third outcome. "Probably X, here is a change that might help" is the
failure mode this skill exists to prevent.

## Gotchas

- **A check that has never passed is not a regression.** Looking for "what
  changed" is then a search with no target. Phase 1 answers this before anything
  else, mechanically.
- **Never push to a default branch.** Work lands on a branch and through the
  normal PR path — the merge policy in `AGENTS.md` applies unchanged.
- **UNOBSERVED-MECHANISM RULE (#1263)** — do not land a retry, a fallback, a
  widened timeout, or added tolerance for a failure whose mechanism you have not
  directly observed, and do not exempt it by calling it instrumentation. A
  diagnostic you add to a harness must be exercised against a known-good and a
  known-bad case before you trust a word it says; if that is impractical, do not
  ship it. Deliberate mitigation carries
  `NIGHTGAUGE-MITIGATION: issue=<owner/repo#N> mechanism=unobserved` beside the
  code, never prose in a doc comment. Read
  [`_shared/UNOBSERVED_MECHANISM.md`](../_shared/UNOBSERVED_MECHANISM.md) — the
  redelivery retry and the misreporting probe are the worked example, including
  why each was persuasive at the time.
- See also [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

---

## Workflow

**Phase markers**: at the start of each phase, emit
`<!-- phase:start name="{phase-name}" index={N} total=6 stage="check-triage" -->`
as an HTML comment on its own line, BEFORE any other output.

### Phase 0: Establish the Target

### Phase 0: Environment Preflight

**PURPOSE**: Verify the nightgauge binary is available and the environment
is healthy before executing any pipeline stage. Skills halt immediately with
actionable instructions when this check fails.

Three-layer check:

1. **Binary discovery** — shell-level, runs before invoking the binary
2. **Doctor check** — binary-level, full environment health via `doctor --json`
3. **Project environment** — the checks the project declares under
   `preflight.environment` (#101); opt-in, silent when none are declared

The binary discovery cascade below is **provider-neutral** (issue 4029): it works
identically under every adapter (Claude, Codex, Gemini, …) and references no
VSCode-extension-specific path. The host that spawns the skill (the VSCode
extension's `skillRunner`, or the Go `nightgauge` binary in auto/CLI mode)
resolves the binary authoritatively and exports `NIGHTGAUGE_BIN`, which
this cascade honors first. The remaining steps (PATH, the development-only repo
`bin/` and worktree canonical-repo `bin/`, `~/go/bin`) are fallbacks for direct
or terminal invocation.

This intentionally **diverges** from
`claude-plugins/nightgauge/hooks/lib/guard.sh` (issue 3262 → issue 4029): guard.sh is
Claude-Code-only and retains a trailing `~/.vscode/extensions/...` glob to serve
the standalone-terminal-Claude case (where no host exports `NIGHTGAUGE_BIN`).
Skills must stay portable Markdown and cannot reference that VSCode-only path —
so the shared resolution order (`NIGHTGAUGE_BIN` → PATH → gated repo → gated
canonical → `~/go/bin`) stays in sync, and only guard.sh carries the extra Claude-only
fallback. The `nightgauge preflight skill-portability` gate enforces that
no skill reintroduces a `.vscode/extensions` path, and that every cascade keeps
the `NIGHTGAUGE_DEV_REPO_BIN` gate (#2758): a foreign repository's
`bin/nightgauge` must never run.

After the cascade resolves `$BINARY`, the preflight prepends
`dirname($BINARY)` to `PATH` so subsequent bare `nightgauge ...`
invocations later in the same skill body resolve through PATH rather than
failing with "command not found" (issue 3262).

```bash
# Layer 1: provider-neutral binary discovery (NIGHTGAUGE_BIN → PATH → gated repo
# bin → gated canonical-repo bin → ~/go/bin). No VSCode-extension path — see issue 4029.
# Repo bins need NIGHTGAUGE_DEV_REPO_BIN=1 and a verified core checkout (#2758).
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  echo "" >&2
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
  echo "Or download from: https://github.com/nightgauge/nightgauge/releases" >&2
  echo "Run \`nightgauge doctor\` after installing to verify your environment." >&2
  exit 1
fi
# Make the resolved binary callable via bare `nightgauge ...` later in this skill body (issue 3262).
export PATH="$(dirname "$BINARY"):$PATH"

# Per-repo GitHub token: export GH_TOKEN so every `gh` call in this skill
# authenticates as the *current repo's* configured user instead of the machine-
# global `gh auth` active account. `forge auth token` resolves per-repo (config
# github_user/token → GITHUB_TOKEN → `gh auth token --user <github_user>`), so
# concurrent sessions/workspaces owned by different GitHub users each use their
# own token. GH_TOKEN outranks GITHUB_TOKEN and the gh keyring. Only resolve when
# unset (an upstream env value — e.g. the VSCode extension's terminal env —
# wins); never fail preflight over it. Mirrors hooks/lib/guard.sh (sync, issue 3262).
if [ -z "${GH_TOKEN:-}" ]; then
  IB_REPO_TOKEN="$("$BINARY" forge auth token 2>/dev/null || true)"
  if [ -n "$IB_REPO_TOKEN" ]; then
    export GH_TOKEN="$IB_REPO_TOKEN"
    export GITHUB_TOKEN="${GITHUB_TOKEN:-$IB_REPO_TOKEN}"
  fi
  unset IB_REPO_TOKEN
fi

# Layer 2: full environment health check via doctor --json
DOCTOR_RESULT=$("$BINARY" doctor --json 2>/dev/null)
DOCTOR_EXIT=$?
if [ "$DOCTOR_EXIT" -eq 2 ]; then
  echo "ERROR: Environment check failed — nightgauge doctor reports broken environment." >&2
  FAILED_CHECKS=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.failed_checks[]? // empty' 2>/dev/null)
  if [ -n "$FAILED_CHECKS" ]; then
    echo "Failing check(s): $(echo "$FAILED_CHECKS" | tr '\n' ',' | sed 's/,$//')" >&2
  fi
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.errors[]' >&2 2>/dev/null || true
  INSTALL_MSG=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.install_instructions // empty' 2>/dev/null)
  [ -n "$INSTALL_MSG" ] && echo "$INSTALL_MSG" >&2
  exit 1
fi
if [ "$DOCTOR_EXIT" -eq 1 ]; then
  echo "WARNING: Environment has non-critical issues:" >&2
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.warnings[]' >&2 2>/dev/null || true
  # Continue — warnings do not block skill execution
fi

# Layer 3: project-declared environment checks (preflight.environment, #101).
# Opt-in: with none declared this prints nothing and continues. The verb
# writes one `[environment-halt]` line per failed check, with its remedy.
if ! "$BINARY" preflight environment >/dev/null; then
  echo "[environment-halt] Project environment check failed — fix the environment, not the code." >&2
  exit 1
fi
```

**Exit codes from `nightgauge doctor`**:

| Code | Meaning                         | Skill behavior                          |
| ---- | ------------------------------- | --------------------------------------- |
| 0    | Healthy                         | Continue                                |
| 1    | Degraded (warnings only)        | Continue with warning printed to stderr |
| 2    | Broken (required checks failed) | Halt immediately with error details     |

**Layer 3** halts the stage when any check declared under
`preflight.environment` in the workspace's `.nightgauge/config.yaml` fails
(exit 1) or the declaration is invalid (exit 2). The failure is in the
operator's environment, not the code: do not edit code to work around it, do
not run the remedy yourself, and end the stage. The pipeline records it as
`environment_halt` and holds the issue for an operator.

Do not theorize about the cause beyond what is printed here — the failing
check name (`failed_checks`) and its `.errors[]` message are the complete
diagnosis (#277).


Resolve the argument into a concrete workflow file and repository. From a run
URL, read the workflow from the run; from a check name, match it against the
repo's workflow files by `name:`. If it resolves to nothing, say so and stop —
guessing which workflow was meant produces an investigation of the wrong thing,
which looks exactly like an investigation of the right thing until the end.

Start the triage record now, as a JSON document you will extend through every
phase. Nothing is written until Phase 5; assembling it as you go is what keeps
the record a log of what happened rather than a summary composed afterwards.

### Phase 1: Has It Ever Been Green?

```bash
nightgauge ci history --repo "$REPO" --workflow "$WORKFLOW" --branch "$BRANCH" --limit 100 --json
```

This is first, and it is mechanical, because the answer changes what the rest of
the investigation is looking for:

- **`ever_passed: false`** — there is no regression and no "what changed". The
  check has never worked. State this explicitly in the report; do not describe
  it as broken, and do not bisect.
- **`ever_passed: true`** — the summary names the last success and the count of
  consecutive failures since. That window is the range worth diffing.

Record the verdict verbatim into `history.detail`.

### Phase 2: Reproduce — and Stop If You Cannot

Establish a reproduction, and say plainly which kind it is:

- **local** — you made the failure happen on this machine. Strongest.
- **ci** — you made it happen in CI (a re-run, an instrumented branch). Weaker,
  and still a reproduction.

Read the failing run's logs (`nightgauge ci logs <run-id> --json`) and the
workflow's own commands (`nightgauge ci discover-commands`) to build the local
invocation. Run it. Confirm it fails, and confirm it fails **for the reason the
CI log shows** — a local failure with a different error is a different bug, and
chasing it is how a session finishes triumphant and unhelpful.

**If nothing reproduces it, this phase is terminal.** Write down every approach
you tried, file a `type:spike` carrying them, emit the record, and stop. Do
**not** propose a change.

That refusal is the point of the phase. An investigation that cannot make the
failure happen has no way to tell a fix from a coincidence, and a change shipped
in that state is worse than nothing: it looks like the case is handled. The
redelivery retry above was exactly this — a plausible mitigation for an
unobserved mechanism, which would have masked the real bug the moment anyone
fixed it.

### Phase 3: Observe — Evidence Before a Cause

Capture direct evidence from the **running system** before naming a cause.
Reading the source produces hypotheses; it does not produce evidence. Print
state, dump the tree, log the value at the moment it is wrong, attach a debugger
— whatever makes the mechanism visible.

Then write down, for each candidate explanation, what became of it:

- `falsified` — and **the observation that ruled it out**.
- `supported` — and the observation that supports it.
- `untested` — considered, not investigated. Honest, and satisfies nothing.

**A report that names only its winning hypothesis does not pass this skill.**
Several explanations always fit the symptom; the one that survives contact with
the system is the one whose rivals were killed by something observed. The record
schema enforces this — a record with no falsified hypothesis is rejected by
`nightgauge triage record`.

**If you add a diagnostic to the harness to get this evidence**, the
UNOBSERVED-MECHANISM RULE applies to it. Exercise it first against a case whose
answer you already know — one known-good and one known-bad. A probe you have not
validated is a hypothesis wearing the costume of a measurement, and when it is
wrong it does not merely fail to help; it authoritatively misdirects whoever
reads it next. If validating it is impractical, do not ship it.

### Phase 4: Fix

**Fix the mechanism you observed in Phase 3, and nothing else.** A retry, a
fallback, a widened timeout or added tolerance for anything you did NOT observe
does not land here — see the UNOBSERVED-MECHANISM RULE above. If the honest
answer is that the mechanism is still unknown, this skill's outcome is
`NOT REPRODUCED` or a diagnosis without a fix, not a change that makes the
symptom go away.

Produce the change **plus a test that fails without it**. Prove that: revert the
fix on a copy and watch the test go red. A test that passes either way is
decoration, and decoration that ships as coverage is worse than no test at all,
because it tells the next person the case is guarded.

If you could not write such a test, say so plainly in `fix.no_test_reason` — "an
emulator and a live stack are needed; nothing reproduces this in unit scope" is
a legitimate answer. Implying coverage you do not have is not.

Land it on a branch and open a PR. Never push to a default branch.

**When the cause is a skill instruction or agent configuration**, not product
code, set `cause.kind` to `skill_instruction` or `agent_config` and the guard is
an eval scenario rather than a unit test (#1287). Add a `skill_regression` block
to the record — the decision the skill faced, the enumerated options, the
corrected decision, and the pre-fix decision with a verbatim transcript excerpt
as `pre_fix_evidence` (schema: `docs/SKILL_EVALUATION.md` § Generated
scenarios) — and run the generator, which writes the scenario only if it passes
on the corrected decision and fails on both the non-answer sentinel and the
pre-fix decision:

```bash
npx tsx scripts/generate-eval-scenario.ts --from "$RECORD_JSON"
```

Exit 0 with `"status": "written"` → put the reported `scenario_path` in
`fix.test` and set `test_fails_without_fix: true`. Exit 1 (rejected) → the
reason names the failed gate; fix the diagnosis, or record
`fix.no_test_reason` with that reason. Never set `test_fails_without_fix` on a
scenario the generator did not write. Product-code causes set
`cause.kind: product_code` and the generator skips them.

### Phase 5: Record and Track

Write the record and let the binary check it. The binary writes it to
`triage/checks/<id>.json` in this checkout's directory (`<checkout>` above,
`nightgauge layout path checkout`):

```bash
nightgauge triage record --file "$RECORD_JSON"
```

Exit 1 means the investigation does not meet the contract, and the message names
which part. The record is written either way — a failing investigation is still
the most useful thing the next session could read, and deleting it to keep the
gate green would be the same mistake at a different level. Fix the substance,
not the JSON.

Then make sure the work is tracked:

- **Fix landed** → file or link an issue and put its URL in `tracking_issue`.
  Work that exists only in a session transcript is work the next person redoes.
- **Not reproduced** → the `type:spike` from Phase 2 goes in `spike_issue`.

Once `nightgauge finding record` exists (#102), the out-of-band findings this
investigation turns up graduate there rather than gaining a second capture path
here.

### Phase 6: Report

Emit the output contract block from above. One outcome, no hedging.

### Self-Assessment Epilogue

**PURPOSE**: Evaluate whether this skill's instructions matched reality during
this execution. This phase is **non-blocking** — skip entirely if any main phase
failed. A perfectly working skill produces **no output** from this phase.

> See [docs/SKILL_SELF_ASSESSMENT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SKILL_SELF_ASSESSMENT.md) for
> the full strategy, synthesis algorithm, and integration architecture.

#### When to Skip

- Any prior phase exited with an error before completing
- The skill was cancelled or timed out
- Running in `--dry-run` mode

#### Step 1: Evaluate Execution Friction

Review the execution that just completed. Answer these questions honestly:

1. **Command failures**: Did any command, script, or binary call in the skill
   instructions fail? (e.g., script not found, wrong flags, missing binary)
2. **Workarounds**: Did you have to deviate from the skill instructions to
   accomplish the goal? (e.g., used `gh` directly because a referenced script
   didn't exist, skipped a step that referenced a nonexistent file)
3. **Stale references**: Did any file path, function name, API endpoint, or tool
   referenced in the instructions not exist in the current codebase?
4. **Unclear instructions**: Were any instructions ambiguous enough that you had
   to guess at the intended behavior?
5. **Missing instructions**: Was there a significant step you had to figure out
   on your own that should have been documented in this skill?

**If ALL answers are "no" — write nothing and complete the skill normally.** The
goal is silence when everything works.

#### Step 2: Write Assessment Record

Only if friction was detected in Step 1. Write a single JSON file,
`assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json` in the clone's pipeline state
directory, through the binary (JSON on stdin; never a direct file write):

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge layout write pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json" <<'JSON'
{ ... the record below ... }
JSON
```

The assessment record MUST follow this schema:

```json
{
  "schema_version": "1",
  "skill": "{STAGE_NAME}",
  "skill_file": "skills/nightgauge-{STAGE_NAME}/SKILL.md",
  "issue_number": 42,
  "timestamp": "2026-03-10T14:30:00Z",
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found — script was deleted",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

**Friction types**: `command_failure`, `workaround`, `stale_reference`,
`unclear_instruction`, `missing_instruction`

**Severity levels**:

- `high` — instruction is **broken**. Required manual workaround to complete.
- `medium` — instruction is **misleading**. Agent adapted without user help.
- `low` — instruction is **suboptimal**. No functional impact.

**Rules**:

- **One record per execution** — multiple friction items go in the `friction`
  array, not separate files.
- **Be specific** — quote the exact instruction text that was wrong. Not "some
  commands didn't work" but "Step 5.2 calls `hooks/lib/add-to-project.sh` but
  this script was deleted in commit 65915701."
- **Suggest the fix** — every finding MUST include `suggested_fix` with the
  concrete SKILL.md change needed. Not "update the docs" but "replace
  `hooks/lib/add-to-project.sh <N>` with `nightgauge project add <N>`."
- **Don't invent friction** — only report issues you actually encountered during
  this execution. Do not speculate about potential problems.

#### Step 3: Validate and Complete

```bash
# Validate JSON if written
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
ASSESSMENT_FILE="$(nightgauge layout path pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json")"
if [ -f "$ASSESSMENT_FILE" ]; then
  python3 -m json.tool "$ASSESSMENT_FILE" > /dev/null 2>&1 || \
    echo "WARNING: Assessment record is not valid JSON" >&2
fi
```

