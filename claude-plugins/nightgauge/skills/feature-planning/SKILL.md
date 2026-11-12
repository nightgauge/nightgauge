---
name: feature-planning
description: Documentation-first feature planning. Read docs before code, propose an
  implementation approach, and write a plan file to the clone's plans
  directory for approval. Use after /issue-pickup and before /feature-dev to produce the
  approved PLAN.md.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.19.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-researcher
model: haiku
inputs:
  - pipeline/issue-{N}.json
outputs:
  - pipeline/planning-{N}.json
  - plans/{N}-*.md
disable-model-invocation: true
---

## System Context

**Product**: Nightgauge — AI-powered Issue-to-PR pipeline.

**Architecture**: Three-layer stack: portable Skills (SKILL.md), a TypeScript
SDK, and a VSCode Extension, over a Go deterministic layer. Six pipeline
stages execute as isolated subagents with JSON context handoff files.

**Execution model**: Each pipeline stage runs in a fresh conversation. Context
is passed exclusively through JSON files in the clone's pipeline state
directory (`nightgauge layout path pipeline`) — never through conversation
history. Every stage reads its predecessor's context file
and writes its own.

**Configuration**: 6-tier config system — built-in defaults → global config →
project config → local config → env vars → CLI flags. Reference:
[docs/CONFIGURATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md).

**Standards**: Code standards in `docs/CODE_STANDARDS.md`. Security rules in
`standards/security.md`. Testing patterns in `docs/TESTING.md`. Git workflow in
`docs/GIT_WORKFLOW.md`.

**Critical rules**: Never push to main. Never hardcode secrets. Never downgrade
versions. Pre-submission validation is mandatory.

## Autonomy Contract

This run is autonomous. No human is watching, and nobody can answer a
question mid-stage. `AskUserQuestion` is disabled — and _behaving_ as if it
were available is the same failure.

- **Proceed without asking** for any reversible action that follows from the
  stage's task. If a decision is genuinely undecidable from the issue, the
  context files, and the repo, fail fast with a clear error — never pause to
  ask, and never silently pick between materially different product
  directions.
- **Never end a turn on a promise.** Before ending your turn, check your last
  paragraph: if it is a plan, a question, a list of next steps, or a promise
  about work not yet done ("I'll now…", "Next, I will…"), do that work now
  with tool calls instead of describing it. A turn that ends on stated intent
  with no corresponding tool call is recorded as a `premature_turn_end` stage
  failure, not a success.
- **Do not stop because the session feels long.** End the turn only when the
  stage's output contract is satisfied (its context file and phase markers
  are written) or you are genuinely blocked — and a genuine block is reported
  as an explicit failure, never as an open question.
- **Never end a turn with background work outstanding.** There is no "await
  and resume" — your turn ending IS stage completion, and the pipeline
  advances immediately. If you started something in the background, block on
  it and read its result before you finish. "I'll wait for that to complete
  rather than poll, and pick the pipeline back up once it lands" is not a
  handoff; it is the stage reporting success on work it never saw (#202).
- **Work only inside the worktree you were given.** Never delegate the
  stage's implementation to a subagent running under worktree isolation, and
  never `cd` outside your workspace to edit files. Only your own worktree is
  read by later stages: on #202 a subagent wrote the entire fix into
  `.claude/worktrees/agent-<id>`, so the stage passed, the branch stayed
  empty, and the work was invisible to every stage after it. A gate now fails
  the stage when this happens — but the run is dead either way, so do the work
  where you stand.


# Feature Planning

Design a complete implementation plan with documentation-first context loading.

## Outcomes

- Reads prior pipeline context from `issue-{N}.json` in the clone's pipeline
  state directory (`nightgauge layout path pipeline`)
- Loads project docs before source exploration
- Reads scaffolded PRD.md from `knowledge_path` (when present) for planning context
- Carries out-of-band findings that concern the planned paths into a
  `## Known Findings` plan section (only when any match)
- Produces the plan `{N}-*.md` in the clone's plans directory
  (`nightgauge layout path plans`) with concrete implementation and validation
  steps
- Writes planning context `planning-{N}.json` through
  `nightgauge layout write pipeline planning-{N}.json`
- Enriches PRD.md with requirements and approach rationale after planning
- Populates decisions.md with key design decisions in ADR block format
- Planning context JSON includes `knowledge_path` and `knowledge_entries` for
  feature-dev
- Signals pipeline state start/completion for VS Code integration

## Required Inputs

- Current branch contains issue number (for example `feat/542-...`)
- Context file from issue pickup:
  `"$(nightgauge layout path pipeline issue-{N}.json)"`

If context is missing, fail with a clear message and instruct the pipeline
order:

1. `/nightgauge-issue-pickup {N}`
2. `/nightgauge-feature-planning`

## References

- Config schema: `docs/CONFIGURATION.md`
- Context schema: `docs/CONTEXT_ARCHITECTURE.md`
- Documentation reading strategy: `docs/ADAPTIVE_DOCUMENTATION_READING.md`
- Estimation model: `docs/ESTIMATION.md`
- End-to-end stage behavior: `docs/ISSUE_TO_PR_WORKFLOW.md`

Do not duplicate full schema or template content here; read docs on demand.

## Supporting files (load on demand)

- `_includes/feedback-and-context.md` (same directory as this SKILL.md) — read in Phases 0, 1, 1.5 (feedback/revision detection, context load + stage start, batch detection)
- `_includes/pattern-and-docs.md` (same directory as this SKILL.md) — read in Phases 2.5, 3 (pattern mining, documentation-first analysis)
- `_includes/knowledge-recall.md` (same directory as this SKILL.md) — read in Phases 3.5, 3.7 (knowledge base read, recall prior decisions)
- `_includes/plan-and-enrichment.md` (same directory as this SKILL.md) — read in Phases 4, 5.5 (produce plan file, knowledge base enrichment)

## Spike Issues (`type:spike`)

For `type:spike` issues, the plan describes the **investigation questions** the
spike answers and the expected **shape of the recommendations** the artifact
will carry — NOT production code changes (spike deliverables are research
artifacts at `docs/spikes/<N>-*.md`). Include placeholder ids for each
anticipated recommendation so the author has an outline to fill the YAML block
from in feature-dev. See [docs/SPIKE_CONTRACT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SPIKE_CONTRACT.md).

A spike path is still subject to the publication boundary, and in this
repository `docs/spikes/**` is PRIVATE by default. Run the
[Publication Boundary Check](#publication-boundary-check) on the artifact path
before planning it (#2562).

## Gotchas

- **An existing PR branch is updated by merging its base, never rebased.** When
  the issue has an open PR that conflicts with its base, the plan merges
  `origin/<base>` into the branch and pushes normally. Never prescribe a
  rebase or a force push of any kind: a rebased tip no longer descends from
  origin's, so validate's push is rejected and the run ends `branch_forked`
  (#2699).
- **Read docs and the knowledge base before proposing.** Skipping
  `knowledge_path/PRD.md` + `decisions.md` and the relevant `docs/` produces a
  plan that drifts from accumulated decisions and causes rework downstream.
- **Write the plan file — planning's only durable output is its handoff.** A
  stage that proposes an approach but never writes its `planning-{N}.json`
  (`nightgauge layout write pipeline planning-{N}.json`) leaves feature-dev
  with nothing to implement against.
- **Write each implementation step as a `- [ ] task` checkbox** in the
  Step-by-step implementation plan section. `parsePlanFile`
  (`internal/hooks/context.go`/`stop.go`) counts `- [ ]` / `- [x]` lines to
  report plan completion; a step written as prose or a numbered list is
  invisible to it.
- **Size the task list to the issue: each checkbox runs as its own
  feature-dev session** on a small-context model, and every session pays a
  fixed setup cost. Aim for XS: 1–2 tasks, S: 2–3, M: at most 5. Make each
  task one coherent change with its tests (one module and its test file),
  not one edit: an import, a removed cast or a rename belongs to the task
  that needs it, and "run the tests" or "run the local gate" is never a
  task of its own, since every task already verifies its change.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="feature-planning" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### Phase 0: Feedback Context Check

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


---

```bash
printf '<!-- phase:start name="feedback-context-check" index=0 total=13 stage="feature-planning" -->\n'
```

> **Read `_includes/feedback-and-context.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 0 (feedback/revision detection) and Phase 1. Emit
each phase's marker inline (below) as you reach it.

---

### Phase 1: Load Context and Start Stage

```bash
printf '<!-- phase:start name="load-context" index=1 total=13 stage="feature-planning" -->\n'
```

Load issue context and signal stage start — see
`_includes/feedback-and-context.md` (same directory as this SKILL.md).

### Phase 1.7: Read the AC Reconciliation Report (Deterministic)

```bash
printf '<!-- phase:start name="ac-reconcile" index=2 total=13 stage="feature-planning" -->\n'
```

**PURPOSE**: Read the deterministic AC report and route planning by it. Before
this stage was dispatched, the orchestrator rebuilt `ac-reconcile-{N}.json` in
the clone's pipeline state directory from the issue body as it is now, classifying
each AC as `satisfied | partial | unsatisfied | undetectable` against the
working tree (#3228). The report is never left over from an earlier run.

- `all-satisfied` → produce a plan with `approach: "verify-and-close"` and
  empty `files_to_create` / `files_to_modify` (Issue #708 short-circuit).
- `mostly-satisfied` → continue planning but pass `focus_acs` to narrow
  scope to the unsatisfied / undetectable subset.
- otherwise → continue normal planning.

This phase only reads the report. Do not run `preflight ac-reconcile`, and do
not write or edit `ac-reconcile-{N}.json`: on #3192's re-run the planning model
found the previous run's report, read it, and four criteria added since were
never judged. A missing report is a stage failure, not a cue to produce one.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
AC_RECONCILE_FILE="$(nightgauge layout path pipeline ac-reconcile-${ISSUE_NUMBER}.json)"
if [ ! -f "$AC_RECONCILE_FILE" ]; then
  echo "ERROR: $AC_RECONCILE_FILE is missing — the orchestrator writes it before planning (#3228)." >&2
  exit 1
fi
AC_AGGREGATE=$(jq -r '.aggregate_status' "$AC_RECONCILE_FILE")
AC_APPROACH=$(jq -r '.suggested_route.approach' "$AC_RECONCILE_FILE")
AC_FOCUS=$(jq -r '.suggested_route.focus_acs | @json' "$AC_RECONCILE_FILE")
echo "AC reconcile: aggregate=$AC_AGGREGATE approach=$AC_APPROACH focus=$AC_FOCUS"
```

When `aggregate_status === "all-satisfied"`, Phase 4 (Produce Plan) MUST emit a
plan whose `approach` is `"verify-and-close"`, with empty `files_to_create` and
`files_to_modify`, and a body section that quotes the evidence that proved each
AC. When `mostly-satisfied` **or `undetectable`**, the plan-generation prompt
MUST be passed `focus_acs` so it scopes to exactly that subset — `undetectable`
means none of the criteria could be evaluated, so `focus_acs` carries every
index and the plan must cover all of them.

**Do NOT write the `ac_reconcile` field.** The orchestrator splices the whole
report into `planning-{N}.json` from `ac-reconcile-{N}.json` after this stage
exits, and overwrites whatever is there. Transcribing it by hand is what #1011
fixed: the three scalars echoed above are the only ones a shell phase exposes,
so a hand-written block loses `acceptance_criteria`, `main_sha` and
`evaluated_at` — the exact three the schema requires — and the next stage
receives a plan with no machine-readable acceptance criteria.

### Phase 2: Assess Complexity (Deterministic)

```bash
printf '<!-- phase:start name="assess-complexity" index=3 total=13 stage="feature-planning" -->\n'
```

Select documentation scope via a deterministic decision tree. Extract size and priority from the `labels` array in the issue context JSON (e.g. `"size:M"`, `"priority:P1"`):

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
CONTEXT_FILE="$(nightgauge layout path pipeline issue-${ISSUE_NUMBER}.json)"
SIZE_LABEL=$(jq -r '[.labels[] | select(startswith("size:"))] | first // empty' "$CONTEXT_FILE" 2>/dev/null | sed 's/size://')
PRIORITY_LABEL=$(jq -r '[.labels[] | select(startswith("priority:"))] | first // empty' "$CONTEXT_FILE" 2>/dev/null | sed 's/priority://')
TYPE_LABEL=$(jq -r '.type // empty' "$CONTEXT_FILE" 2>/dev/null)
```

Decision tree:

- If `SIZE_LABEL=XS` and `TYPE_LABEL=bug` -> `minimal`
- If `SIZE_LABEL=S` and `TYPE_LABEL` in (`bug`, `docs`) -> `targeted`
- If `SIZE_LABEL` in (`L`, `XL`) or `PRIORITY_LABEL=critical` -> `extended`
- Else -> `standard`

Fibonacci complexity score from `docs/ESTIMATION.md`: `XS=1`, `S=2`, `M=3`, `L=5`, `XL=8`; default `3`. Read only documentation needed for the assessed complexity.

### Phase 2.5: Pattern Mining

```bash
printf '<!-- phase:start name="pattern-mining" index=4 total=13 stage="feature-planning" -->\n'
```

> **Read `_includes/pattern-and-docs.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 2.5 (pattern mining) and Phase 3 (documentation-first analysis).
Emit each phase's marker inline (below) as you reach it.

### Phase 3: Documentation-First Analysis

```bash
printf '<!-- phase:start name="documentation-analysis" index=5 total=13 stage="feature-planning" -->\n'
```

Greenfield detection, parallel/sequential doc gathering — see the supporting
file (`pattern-and-docs.md`).

**Exploration budget (#2186).** Exploration across Phases 2.5-3.7 is bounded
at **12 reads** (file reads, `grep` or listing calls, doc fetches). Batch
discovery into one `grep`/listing pass instead of many narrow ones. Read each
whole relevant range once, not many small slices of the same file. When the
budget is spent, stop exploring and write the plan with what you have; name
any open question in the plan instead of reading further.

### Phase 3.5: Knowledge Base Read

```bash
printf '<!-- phase:start name="knowledge-base-read" index=6 total=13 stage="feature-planning" -->\n'
```

> **Read `_includes/knowledge-recall.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 3.5 (scaffolded PRD.md + cross-repo/workspace detection) and
Phase 3.7 (recall prior decisions). Emit each marker inline (below) as you reach
it.

### Phase 3.7: Recall Prior Decisions

```bash
printf '<!-- phase:start name="recall-prior-decisions" index=7 total=13 stage="feature-planning" -->\n'
```

Query the knowledge base for semantically-related prior decisions and set
`RECALL_HITS` for Phases 4 and 5 — see
`_includes/knowledge-recall.md` (same directory as this SKILL.md).

---

### Phase 4: Produce Plan File in the Clone's Plans Directory

```bash
printf '<!-- phase:start name="produce-plan" index=8 total=13 stage="feature-planning" -->\n'
```

> **Read `_includes/plan-and-enrichment.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 4 (produce the `{N}-*.md` plan file with
`nightgauge layout write plans`) and
Phase 5.5 (knowledge base enrichment). Phase 5 below is the inline output
contract.

#### Known Findings (before writing the plan)

Operators record findings discovered outside pipeline runs with
`nightgauge finding record` (#102). Before writing the plan, ask the binary
which ones concern the paths you plan to change. The binary decides the match;
do not filter findings yourself.

```bash
PLANNED_PATHS="<comma-separated files_to_create + files_to_modify>"
KNOWN_FINDINGS=$(nightgauge finding list --match-paths "$PLANNED_PATHS" --json 2>/dev/null || echo "[]")
```

When `KNOWN_FINDINGS` is a non-empty array, the plan carries a
`## Known Findings` section before the step-by-step implementation plan, one
entry per finding with its `kind`, `scope`, `summary`, `remedy` and `evidence`,
and the steps account for each remedy. When it is `[]` (no match, or no
findings file), add nothing: the plan is identical to one written without this
step. `planning-{N}.json` gains no field.

#### Publication Boundary Check

Before writing `planning-{N}.json`, classify every path the plan would ADD
(`files_to_create`, plus any `files_to_modify` entry that does not exist yet)
against the repository's publication boundary (#2562):

```bash
nightgauge gate check-publication --paths "docs/spikes/42-x.md,internal/new.go" --json
```

- Exit `0`: every path is allowed, or the repository has no boundary
  (`"applicable": false`). Continue.
- Exit `1`: a path is `deny` or `unclassified`. Never plan that path. Re-plan
  to an allowed location when the issue permits one (for a spike, the
  alternatives in [docs/SPIKE_CONTRACT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SPIKE_CONTRACT.md)). When
  the issue allows no permitted location, the deliverable needs an owner
  publication decision: write no plan, and emit the
  [Not-Pipeline-Actionable Signal](#not-pipeline-actionable-signal) with the
  checker's verdict (path, rule, class) quoted in `evidence`.
- Exit `2`: the checker could not run. Fail the stage; do not plan blind.

`FeaturePlanningGate` re-runs this check on the written context, and fails a
rejected path as `not_pipeline_actionable`.

### Phase 5: Write Planning Context

```bash
printf '<!-- phase:start name="write-planning-context" index=9 total=13 stage="feature-planning" -->\n'
```

Write `planning-{N}.json` through `nightgauge layout write pipeline planning-{N}.json`
(JSON on stdin, or `--from` a temp file; never a direct write under the git
directory) with:

- Issue metadata
- Requirement summary
- Planned file changes
- `files_to_read` — existing files that feature-dev should pre-load for
  implementation context (imports, patterns, types). This enables feature-dev to
  front-load file reads instead of discovering them mid-implementation.
- Selected approach
- Validation strategy
- Timestamp and stage metadata
- `pattern_mining_results` — results from Phase 2.5 pattern mining subagent (optional;
  null when pattern mining was skipped or returned no results). When present, use
  **exactly** these field names (the Zod schema enforces them):

  ```json
  "pattern_mining_results": {
    "patterns_found": [
      {
        "pattern_type": "structural",
        "category": "TypeScript",
        "pattern": "flexEnum for enum coercion",
        "evidence": [
          "src/context/schemas/helpers.ts:35",
          "src/context/schemas/validate.ts:20"
        ],
        "frequency": 3,
        "example_implementations": ["ChangeTypeSchema", "RoutingPathSchema"]
      }
    ],
    "similar_issues": ["#2552", "#2314"],
    "pattern_classifications": {
      "naming_conventions": 2,
      "structural_patterns": 3,
      "interface_patterns": 1,
      "idioms": 2
    },
    "search_queries_used": ["flexEnum", "z.preprocess"],
    "coverage_ratio": 0.75,
    "token_cost_estimate": 5000,
    "recommendations": ["Use flexEnum pattern for all agent-facing enums"]
  }
  ```

  **Required `pattern_classifications` field names** (use exactly these 4 keys):
  `naming_conventions`, `structural_patterns`, `interface_patterns`, `idioms`.
  Do NOT use `naming`, `structural`, `interface`, or `idiomatic` as substitutes.

  `pattern_mining_results` may be `null` when pattern mining was skipped — this
  is valid. Schema reference:
  [docs/CONTEXT_ARCHITECTURE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONTEXT_ARCHITECTURE.md).

- `complexity_assessment` — use **exactly** these field names (values from Phase
  2):

  **`size_label` is ALWAYS populated, and it is YOUR assessment — never `null`.**
  It is the size you judged this issue to be after reading it and the code, in
  the `XS`/`S`/`M`/`L`/`XL` vocabulary. If the issue carries a `size:*` label
  and you agree with it, write that bucket. If you disagree, **write your own
  assessment** — the disagreement is recorded and is the most useful row the
  calibration corpus gets. If the issue carries no size label at all, this
  field is the only place the size exists, and the orchestrator applies it to
  the issue as a `size:` label once planning completes (#1515).

  Writing `null` here costs the pipeline a run's worth of calibration data: the
  run record's `size` is the join key the pre-flight cost estimate matches
  history on, and most issues carry no size label (#112). "There was no label
  to copy" is not a reason to leave it empty — it is exactly when it matters.

  Keep `computed_score` on the Fibonacci scale (`1`/`2`/`3`/`5`/`8`), where
  `XS`=1, `S`=2, `M`=3, `L`=5, `XL`=8, so a reader can recover the bucket from
  the score alone.

**Critical field constraints (schema-enforced — wrong names cause pipeline
failure):**

- `approach`: MUST be a non-empty string. Use `"verify-and-close"` for
  already-resolved issues. Never use `implementation_notes`,
  `implementation_status`, `change_type`, or `route` as a substitute.
- `files_to_create`: MUST be a JSON array of strings (even if empty: `[]`).
  Never omit this field or rename it to `new_files`, `files_created`, or
  `files_modified`.
- `files_to_modify`: MUST be a JSON array of strings (even if empty: `[]`).
- `created_at`: MUST be an ISO 8601 datetime string (e.g.
  `"2026-01-01T00:00:00Z"`). Never use `planned_at`, `timestamp`, or
  `created_date`.
- `plan_file`: MUST be the exact absolute path to the `.md` plan file written
  in Phase 4 — the path `nightgauge layout write plans` printed.

Minimal required skeleton:

```json
{
  "schema_version": "1.9",
  "issue_number": N,
  "plan_file": "<absolute path printed by nightgauge layout write plans>",
  "approach": "...",
  "files_to_create": [],
  "files_to_modify": [],
  "pattern_mining_results": null,
  "recalled_decisions": null,
  "revision_count": 0,
  "revision_reasons": [],
  "knowledge_path": null,
  "knowledge_entries": [],
  "cross_repo_knowledge": [],
  "ac_reconcile": null,
  "knowledge_read": null,
  "created_at": "2026-01-01T00:00:00Z"
}
```

**Additional field constraints**:

- `ac_reconcile`: **Leave as `null`. Never populate it.** The orchestrator
  overwrites this field with the verbatim contents of `ac-reconcile-{N}.json`
  after the stage exits (#1011). It is listed here because the skeleton above
  contains it, not because the stage writes it.
- `knowledge_path`: Copied from `issue-{N}.json` when knowledge scaffolding is
  enabled. Omit (or `null`) when absent. Written by Phase 5.5. Always an
  ABSOLUTE path into the main checkout's `.nightgauge/knowledge` (the stage's
  cwd is a worktree; `$NIGHTGAUGE_KNOWLEDGE_DIR` names the same knowledge
  base), so writes through it survive the worktree's removal.
- `knowledge_entries`: Array of `.md` filenames in the knowledge directory.
  Written by Phase 5.5. `[]` when knowledge is not enabled.
- `cross_repo_knowledge`: Array populated by Phase 3.5.1 when sibling repos with
  knowledge bases are found. Each entry: `{repo, path, entries[]}`. `[]` when no
  workspace config exists or no sibling knowledge directories were found.
- `recalled_decisions`: Array of `RecallHit` objects from Phase 3.7. Set to the
  `RECALL_HITS` variable value (JSON array). `null` when recall was skipped
  (knowledge disabled, no index, error, or 0 results above threshold). Use:
  ```bash
  ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
  : "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
  jq --argjson rd "$RECALL_HITS" '. + {"recalled_decisions": (if ($rd | length) > 0 then $rd else null end)}' \
    "$PLANNING_FILE" > /tmp/planning_tmp.json &&
    nightgauge layout write pipeline "planning-${ISSUE_NUMBER}.json" \
      --from /tmp/planning_tmp.json >/dev/null
  ```

**Revision fields**:

- `revision_count`: Number of prior plan attempts. `0` on the first run.
  Populated from `REVISION_COUNT` (set in Phase 0).
- `revision_reasons`: Array of evidence strings collected from all feedback
  signals. Empty array `[]` on the first run. Populated from `REVISION_REASONS`
  (set in Phase 0).

```json
"complexity_assessment": {
  "size_label": "M",
  "type_label": "feature",
  "priority_label": "high",
  "computed_score": 3,
  "documentation_scope": "standard",
  "rationale": "Medium feature requires full documentation review",
  "estimated_token_savings": 0
}
```

Use the canonical schema from `docs/CONTEXT_ARCHITECTURE.md`.

#### Emit risk-tiering facts for the approval gate (#4135)

The architecture-approval gate (run at feature-dev) risk-tiers the change on two
facts that only the plan knows. Merge a `dependency_analysis` block into
`issue-{N}.json` (the file the gate reads) so the trigger is live end-to-end:

- `major_bumps_count` — the number of dependency **major-version** bumps the plan
  introduces (e.g. a `go.mod` / `package.json` dependency going `2.x → 3.0`). A
  patch/minor bump is **not** a major bump. `0` when the plan changes no
  dependency major versions.
- `production_area` — `true` only when the plan touches production-affecting
  surfaces (deploy/infra: `Dockerfile`, `docker-compose*.yml`, `infra/`,
  deploy workflows, DB migrations, production config). `false` otherwise.

**Emit only what the plan actually establishes** — absence must read as `0` /
`false`, never as "assume high-impact" (over-firing floods false positives).

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
CONTEXT_FILE="$(nightgauge layout path pipeline issue-${ISSUE_NUMBER}.json)"
# Replace the two values from the plan; default to 0 / false when none apply.
DEP_MAJOR_BUMPS=0
PROD_AREA=false
tmp=$(mktemp)
jq --argjson mb "$DEP_MAJOR_BUMPS" --argjson pa "$PROD_AREA" \
  '.dependency_analysis = {major_bumps_count: $mb, production_area: $pa}' \
  "$CONTEXT_FILE" > "$tmp" &&
  nightgauge layout write pipeline "issue-${ISSUE_NUMBER}.json" --from "$tmp" >/dev/null
rm -f "$tmp"
jq . "$CONTEXT_FILE" > /dev/null || { echo "ERROR: issue context JSON invalid after dependency_analysis merge" >&2; exit 1; }
```

### Phase 5.5: Knowledge Base Enrichment

```bash
printf '<!-- phase:start name="knowledge-base-enrichment" index=10 total=13 stage="feature-planning" -->\n'
```

> **Read `_includes/plan-and-enrichment.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers enriching the scaffolded `PRD.md` in place, populating `decisions.md`
with ADR blocks, deferred scaffolding, patching `knowledge_path` /
`knowledge_entries` into the planning context, and the `knowledge.require_decisions`
validation gate. **Skip when knowledge is disabled** (config `knowledge.enabled`
false AND `knowledge_path` null) — silently continue to Phase 6.

---

### Phase 6: Complete Stage

```bash
printf '<!-- phase:start name="complete-stage" index=11 total=13 stage="feature-planning" -->\n'
```

1. Signal completion via Go binary: `"$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true`
2. Provide a short summary of plan decisions and next command:
   `/nightgauge-feature-dev`

### Already-Resolved Issue Signal

When feature-planning detects that the issue is already resolved (all acceptance
criteria met, fixes already merged on main), it MUST output a `verify-and-close`
signal instead of a normal plan:

- Set `approach` to `"verify-and-close"` in `planning-{N}.json`
- Set `files_to_create` to an empty array `[]`
- Set `files_to_modify` to an empty array `[]`
- Write a minimal plan file explaining why the issue is already resolved
- Use it ONLY when the fix is reachable from the default (or epic base) branch.
  Work committed only on the issue's own unmerged branch is NOT resolved: the
  orchestrator refuses to close and resumes validate -> pr-create instead.
- Criteria listed in a `<!-- nightgauge:partial-delivery pr=N -->` comment are
  met only by evidence dated after that comment, such as a PR merged later.
  Code that was already on main when the comment was posted is the partial
  delivery itself, not proof of the remainder. Plan those criteria as
  `focus_acs`; never answer verify-and-close over them. The orchestrator
  refuses such a close and raises an Action Center card (#3089).

The orchestrator detects this signal after feature-planning completes and
short-circuits the remaining stages (feature-dev, feature-validate, pr-create,
pr-merge), running a lightweight close path instead.

@see Issue #708

### Not-Pipeline-Actionable Signal

Some issues are filed on a board the pipeline reads, in a repository the
pipeline works, and are still not pipeline work at all. Their deliverable is
**not code, and not producible by any agent**:

- a review, approval or sign-off reserved to a licensed professional (legal
  counsel, an accountant, a medical or safety reviewer);
- an act only an operator can perform — rotating a credential, accepting terms,
  paying an invoice, registering a domain, provisioning hardware, filing with a
  registrar;
- a physical-world step (photograph a venue, mail a device, attend a meeting);
- a decision that is the owner's to make and that no evidence in the repository
  can settle.

You are the FIRST stage that reads the issue with judgement, so you are the
first that can tell these apart from ordinary work — and the only one that can
say so before feature-dev spends a stage discovering it.

**When the issue is one of these, do NOT write a plan for it.** A plan is a
promise that a later stage can implement something, and there is nothing to
implement; a plan written anyway sends feature-dev to reach the identical
verdict at full price, and — because an empty workspace is indistinguishable
from a stage that promised work and produced none — to be convicted of
`dev_produced_no_changes` for having been right.

Instead, write `planning-{N}.json` with a single blocking feedback signal and
nothing else claimed:

```json
{
  "feedback": [
    {
      "signal_type": "NOT_PIPELINE_ACTIONABLE",
      "emitted_by_stage": "feature-planning",
      "backtrack_target_stage": null,
      "severity": "blocking",
      "rationale": "The deliverable is counsel sign-off on the published privacy policy and terms. No code change satisfies it, and drafting legal text as if it were reviewed would be worse than doing nothing.",
      "evidence": [
        "issue body: 'Items requiring counsel sign-off (per PRIVACY_AND_RETENTION.md open items)'",
        "issue body: 'Gate: Must complete before any real team is onboarded.'"
      ]
    }
  ]
}
```

`backtrack_target_stage` is `null` **by definition** — there is no stage to
return to, because no lap of this pipeline changes the answer. That is what
separates this signal from `PLAN_REVISION_NEEDED` (a different plan would work)
and from an out-of-scope blocker (other work would unblock it): nothing
unblocks this but a human doing the thing.

`rationale` and `evidence` are read by a person, so quote the issue rather than
paraphrasing it. The run then ends **`blocked`, not failed**: the finding is
persisted, a comment goes on the issue, an Action Center card is raised, the
issue is labelled `owner-action` so autonomous dispatch stops picking it up, and
its board row is parked in Backlog. Nothing is halted and no failure is charged
to the issue or to the pipeline.

**Do not reach for this to avoid hard work.** The test is whether an agent with
unlimited time and full repository access could produce the deliverable — not
whether it is large, unclear, or unpleasant. An ambiguous acceptance criterion
is `ACCEPTANCE_CRITERIA_AMBIGUOUS`; an under-estimate is
`COMPLEXITY_UNDERESTIMATED`; work blocked on another issue is an out-of-scope
blocker. Only "no agent can produce this artifact" is this signal.

@see Issue #1241

### Open Prerequisite Dependency Signal

The third case that cannot produce a normal plan: the issue is ordinary
pipeline work, and it cannot start yet because **something it depends on is
still open**. You discover this mid-planning, by reading the issue body —
"Depends on: #1187", "Blocked by #1187", a "## Dependencies" section — or by
finding that the API, table or module the plan would build on does not exist
yet.

**Check `dependencies.blockedBy` in `issue-{N}.json` first.** When it is
populated, the prerequisite was already known and the deterministic gates
should have held the issue before you were dispatched at all; say so in the
rationale, because it is a pipeline bug worth a `pipeline-gap` issue. When it
is **empty and the body still names a prerequisite**, do not conclude the
dependency is imaginary — the declaration may simply never have been turned
into a GitHub `blockedBy` edge. Trust the body.

Emit a blocking feedback signal, **no plan file**, and stop:

```json
{
  "feedback": [
    {
      "signal_type": "PLAN_REVISION_NEEDED",
      "emitted_by_stage": "feature-planning",
      "backtrack_target_stage": null,
      "severity": "blocking",
      "rationale": "The issue body declares 'Depends on: #1187'. #1187 is OPEN and its work is unimplemented — the endpoint this issue's acceptance criteria consume does not exist yet — so no plan written now can be implemented. dependencies.blockedBy in issue-1188.json was empty: the declaration was never captured as a GitHub blockedBy edge.",
      "evidence": [
        "blocked-on: #1187 (OPEN) — declared in the issue body: \"Depends on: #1187\"",
        "issue-1188.json: dependencies.blockedBy == [] — the declaration is prose only",
        "AC 2 consumes POST /v1/sessions, which #1187 introduces; it is absent from the current tree"
      ]
    }
  ]
}
```

Four fields carry the whole shape, and none of them is optional:

| Field                    | Value                                                                    | Why                                                                                                                                                                                                 |
| ------------------------ | ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `signal_type`            | `PLAN_REVISION_NEEDED`                                                   | There is no dedicated type, and inventing one gets the signal dropped by the schema. The **marker** below is what classifies it, not the type.                                                      |
| `evidence[0]` prefix     | `blocked-on:` (or `blocked-by:` / `external-blocker:` / `out-of-scope:`) | The post-validate gate reads the structured marker and **never** searches the free-text `rationale`. A marker overrides the type: a `PLAN_REVISION_NEEDED` carrying one is blocked, not rewindable. |
| `backtrack_target_stage` | `null`                                                                   | No lap of this pipeline makes the prerequisite exist. Naming a stage here asks for a rewind that will reach the same verdict at full price.                                                         |
| plan file                | **not written**                                                          | A plan is a promise a later stage can implement something. Writing one sends feature-dev to rediscover the blocker and be convicted of `dev_produced_no_changes` for having been right.             |

Then move the board row to **Backlog**, not In progress — the issue is held,
not in flight.

**This is a hold, not a verdict, and it does not create a `blockedBy` edge.**
An edge is durable and human-visible, and a wrong one converts a zero-cost
defer into a permanent silent stall, so the operator adds it by hand
(`nightgauge issue add-blocked-by <blocked> <blocker>`; a blocker in another
repository is spelled `owner/repo#N` or `repo#N`) after reading your comment. See
[docs/FEEDBACK_LOOPS.md § Not every blocking signal is a rewind request](https://github.com/nightgauge/nightgauge/blob/main/docs/FEEDBACK_LOOPS.md#not-every-blocking-signal-is-a-rewind-request)
for what the run leaves behind (a blocked-finding file, an issue comment, an
Action Center card).

**Which signal is which.** Use this one when _other work_ would unblock the
issue. Use `NOT_PIPELINE_ACTIONABLE` when nothing unblocks it but a human doing
a thing no agent can do. Use a plain `PLAN_REVISION_NEEDED` with a real
`backtrack_target_stage` when a _different plan_ would work today.

@see Issue #1492 — this case was hit in production, took a subagent and a
sibling repository's docs to resolve, and ended the stage's turn before it
could emit anything.

### Phase 7: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=12 total=13 stage="feature-planning" -->\n'
```

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


---

## Execution Rules

- Documentation-first is mandatory; do not start with broad code search.
- Keep context token-efficient: read referenced docs as needed.
- Prefer deterministic scripts for state transitions over manual logic.
- Do not implement code in this stage.
- Do not skip writing the plan `{N}-*.md` and `planning-{N}.json` (both
  through `nightgauge layout write`).

## Failure Conditions

Fail fast with actionable messages when:

- Branch has no parseable issue number
- Required issue context file is missing or invalid
- Required docs are missing for critical decisions
- Pipeline state hook exists but returns an error

## Completion Checklist

- [ ] The plan `{N}-*.md` exists in `nightgauge layout path plans` and is
      complete — OR the issue was declared `NOT_PIPELINE_ACTIONABLE` and no
      plan was written
- [ ] `planning-{N}.json` written (`nightgauge layout write pipeline`)
- [ ] Stage start/completion signaled
- [ ] Next stage clearly indicated (`/nightgauge-feature-dev`)
