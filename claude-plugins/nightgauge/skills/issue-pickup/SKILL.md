---
name: issue-pickup
description: Claim a GitHub issue, extract requirements, and set up the development
  environment. Use at the start of any issue-based development work.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.21.3"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-researcher
model: haiku
inputs: []
outputs:
  - pipeline/issue-{N}.json
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


# Issue Pickup

> Claim a GitHub issue, extract requirements, and set up development

## Description

This skill starts the development pipeline by:

1. Fetching and analyzing a GitHub issue
2. Extracting structured requirements
3. Creating a properly-named feature branch
4. Setting up the development environment

## Invocation

| Tool           | Command                                                |
| -------------- | ------------------------------------------------------ |
| Claude Code    | `/nightgauge-issue-pickup [issue-number]` (via plugin) |
| OpenAI Codex   | `$nightgauge-issue-pickup [issue-number]`              |
| GitHub Copilot | Invoke via Agent Skills                                |
| Cursor         | Invoke via Agent Skills                                |

## Arguments

```bash
# Pick up a specific issue
/nightgauge-issue-pickup 42

# Auto-select highest priority issue (DEFAULT behavior)
/nightgauge-issue-pickup

# Force interactive mode (list and choose)
/nightgauge-issue-pickup -i
/nightgauge-issue-pickup --interactive

# Filter by label
/nightgauge-issue-pickup --label "ready-for-dev"
```

The skill arguments are everything typed after the skill name.

### Flags

| Flag                  | Description                                              |
| --------------------- | -------------------------------------------------------- |
| `-i`, `--interactive` | Force interactive mode (list issues and choose manually) |
| `--label <label>`     | Filter issues by label before selection                  |

## Prerequisites

- **Nightgauge binary**: Must have `nightgauge` binary installed and configured
- **Git repository**: Must be in a git repository with GitHub remote
- **docs/GIT_WORKFLOW.md**: Should exist for branch naming conventions (or will
  use defaults)

## Philosophy

- **Issue-driven development** — Every change starts with an issue
- **Traceability** — Branch names include issue numbers
- **Structured requirements** — Extract clear requirements before coding
- **Documentation-first** — Read workflow docs before creating branches

---

### Configuration Reference

Pipeline skills read configuration from `.nightgauge/config.yaml`.

**Full reference**: See [docs/CONFIGURATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md)
for the complete configuration guide including the 6-tier precedence chain, all
config options, and examples. `nightgauge config show` prints the resolved
configuration for the current repository.

**Key config sections by stage**:

| Stage            | Key Options                                           |
| ---------------- | ----------------------------------------------------- |
| issue-pickup     | `branch.base`, `branch.prefixes.*`, `project.*`       |
| feature-planning | `project.number`, `project.auto_dates`                |
| feature-dev      | `pipeline.auto_fix`, `commands.*`                     |
| feature-validate | `commands.test`, `pipeline.skip.tests`                |
| pr-create        | `pr.draft_by_default`, `pr.reviewers`, `validation.*` |
| pr-merge         | `pr.merge_strategy`, `pr.delete_branch`               |

#### Issue-Pickup Configuration

| Config Key                   | Default     | Description                       |
| ---------------------------- | ----------- | --------------------------------- |
| `branch.base`                | `main`      | Default base branch for PRs       |
| `branch.protected`           | `[main]`    | Branches that cannot be pushed to |
| `branch.prefixes.*`          | (see below) | Custom branch prefix mappings     |
| `project.number`             | -           | GitHub Project number             |
| `project.auto_dates`         | `false`     | Auto-populate Start/Target dates  |
| `project.sprint.auto_assign` | `false`     | Assign current sprint on pickup   |

**Branch Prefixes (defaults):**

| Type       | Default     |
| ---------- | ----------- |
| `feature`  | `feat/`     |
| `bugfix`   | `fix/`      |
| `docs`     | `docs/`     |
| `refactor` | `refactor/` |
| `chore`    | `chore/`    |
| `test`     | `test/`     |
| `hotfix`   | `hotfix/`   |

**Environment overrides:**

```bash
export NIGHTGAUGE_BRANCH_BASE=develop
export NIGHTGAUGE_BRANCH_PROTECTED=main,develop
export NIGHTGAUGE_PROJECT_AUTO_DATES=true
```

#### PR-Merge Configuration

| Config Key                 | Default  | Description                                             |
| -------------------------- | -------- | ------------------------------------------------------- |
| `pr.merge_strategy`        | `squash` | Merge strategy for sub-issue PRs: squash, merge, rebase |
| `pr.epic_merge_strategy`   | `merge`  | Merge strategy for epic→main PRs: merge, squash, rebase |
| `pr.delete_branch`         | `true`   | Delete feature branch after merge                       |
| `pr.auto_fix_ci`           | `true`   | Auto-fix CI failures before merge                       |
| `pr.auto_fix_max_attempts` | `3`      | Maximum auto-fix retry attempts                         |
| `pr.ci_check_timeout`      | `10`     | Timeout for CI checks in minutes                        |
| `project.number`           | -        | GitHub Project number for status sync                   |

**Environment overrides:**

```bash
export NIGHTGAUGE_PR_ADMIN_MERGE=true
export NIGHTGAUGE_PR_MERGE_STRATEGY=rebase
export NIGHTGAUGE_PR_DELETE_BRANCH=false
export NIGHTGAUGE_PR_AUTO_FIX_CI=true
export NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS=3
export NIGHTGAUGE_PR_CI_CHECK_TIMEOUT=10  # minutes (not seconds)
```

**Important notes:**

- There is no admin merge bypass — nothing skips branch protection, and CI
  checks are always waited for unless `--skip-ci-gate` is explicitly used
  (#186).
- If no CI checks are configured on the repository, the skill proceeds normally
  without waiting.

### Config Helper Functions

When skills need to read configuration values, use these inline patterns
rather than undefined helper functions:

#### Single value with default

```bash
VALUE=$(yq -r '.path.to.key // "default"' .nightgauge/config.yaml 2>/dev/null || echo "default")
```

#### Boolean with default

```bash
ENABLED=$(yq -r '.path.to.key // "true"' .nightgauge/config.yaml 2>/dev/null || echo "true")
```

#### With env var override (6-tier precedence)

```bash
# Env var takes priority, then config file, then default
VALUE="${ENV_VAR_OVERRIDE:-$(yq -r '.path.to.key // "default"' .nightgauge/config.yaml 2>/dev/null || echo "default")}"
```

**NOTE**: Do not use `get_config_bool` or `get_config_value` — these functions
are not defined. Use the inline `yq` patterns above.


---

## Supporting files (load on demand)

- `_includes/issue-selection-and-gates.md` (same directory as this SKILL.md) — read in Phases 2.5, 2.7, 2.8 (signal stage start, size gate, baseline-CI gate)
- `_includes/issue-analysis.md` (same directory as this SKILL.md) — read in Phase 3 (fetch, parse, route, requirements summary)
- `_includes/branch-and-env.md` (same directory as this SKILL.md) — read in Phase 5 (deterministic branch creation)
- `_includes/context-and-knowledge.md` (same directory as this SKILL.md) — read in Phases 8, 9 (write context file, knowledge scaffolding)

---

## Gotchas

- **Validate arguments before any phase runs.** A missing/invalid issue number
  must fail fast — never part-execute (claim, branch) on bad input.
- **Re-run invariant.** On a rerun, re-check claim/branch state before acting —
  don't re-claim the issue or create a duplicate branch that already exists.
- **Context isolation — stop after handoff.** Issue-pickup must not continue into
  feature-planning in the same session. Write the context file and exit.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="issue-pickup" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### CRITICAL: Argument Check (Before Any Other Phase)

**This check MUST happen FIRST, before any other workflow logic.**

Parse the skill arguments immediately:

```bash
# The arguments are everything after the skill name
# Example: "/nightgauge:issue-pickup 42" → arguments "42"
# Example: "/nightgauge:issue-pickup -i" → arguments "-i"
# Example: "/nightgauge:issue-pickup" → no arguments
```

**Decision tree:**

1. **If the arguments contain a number** (e.g., "42", "123"):
   - This is the issue number provided by the user
   - **SKIP Phase 2 entirely** - do NOT ask for issue selection
   - **Proceed directly to Phase 3** with this issue number

2. **If the arguments contain `-i` or `--interactive`**:
   - Use Interactive Mode (Phase 2, Step 2.5)

3. **If the arguments are empty or contain only `--label`**:
   - Use auto-selection (Phase 2)

| Command                                    | Arguments           | Action                          |
| ------------------------------------------ | ------------------- | ------------------------------- |
| `/nightgauge:issue-pickup 42`              | `"42"`              | Skip to Phase 3 with issue #42  |
| `/nightgauge:issue-pickup 123 --label bug` | `"123 --label bug"` | Skip to Phase 3 with issue #123 |
| `/nightgauge:issue-pickup -i`              | `"-i"`              | Use interactive mode            |
| `/nightgauge:issue-pickup`                 | `""`                | Use auto-selection              |

---

### Phase 0: Environment Preflight

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

### Phase 1: Validate Environment

```bash
printf '<!-- phase:start name="validate-environment" index=0 total=14 stage="issue-pickup" -->\n'
```

#### Step 1.1: Check Forge Authentication

```bash
nightgauge forge auth status
```

If not authenticated: "Please run `nightgauge forge auth login` to authenticate."

#### Step 1.2: Verify Git Repository

```bash
git remote -v | grep -E "(github\.com|github\.)"
```

#### Step 1.3: Get Repository Info

```bash
REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug)}"
: "${REPO:?set NIGHTGAUGE_REPO or run inside a clone with an origin remote}"
nightgauge forge repo view --repo "$REPO" --json | jq -r .nameWithOwner
```

#### Step 1.4: Verify Repo Identity

### Repo Identity Assertion (HARD GATE — non-recoverable)

If the `NIGHTGAUGE_TARGET_REPO` environment variable is set, the orchestrator
has pinned the exact repo this stage must run in. Verify the current repository
matches **before doing any work**:

```bash
if [ -n "$NIGHTGAUGE_TARGET_REPO" ]; then
  # Derive this checkout's identity from its own git remote. The question
  # is "which repository is this working directory?", and the remote answers
  # it exactly — locally, with no API call. This used to shell out to the
  # GitHub CLI for the same string, spending a request per stage on every
  # repo in the workspace, invisible to `nightgauge api-usage`.
  ACTUAL_REPO=$(git remote get-url origin 2>/dev/null \
    | sed -E 's#^git@[^:]+:#-#; s#^https?://[^/]+/#-#; s#^-##; s#\.git$##')
  if [ -n "$ACTUAL_REPO" ] && [ "$ACTUAL_REPO" != "$NIGHTGAUGE_TARGET_REPO" ]; then
    echo "[repo-mismatch] FATAL: repository identity assertion failed"
    echo "  Expected: $NIGHTGAUGE_TARGET_REPO"
    echo "  Actual:   $ACTUAL_REPO"
    echo "  CWD:      $(pwd)"
    echo ""
    echo "The orchestrator expected this stage to run in $NIGHTGAUGE_TARGET_REPO"
    echo "but the working directory belongs to $ACTUAL_REPO."
    exit 1
  fi
fi
```

**This is a terminal, non-recoverable stop condition. If the check prints
`[repo-mismatch]` and exits 1, you MUST immediately end the stage with a failure.**

Specifically, on a mismatch you must NOT:

- `cd` into a different directory, switch worktrees, or `gh repo set-default` to
  "make it match" — the orchestrator owns CWD; second-guessing it corrupts state.
- Continue to later phases, create or push a branch, or open a PR — every
  downstream artifact would land in the wrong repo.
- Ask the user a question (`AskUserQuestion`) — autonomous runs have no human at
  the prompt; the question is auto-dismissed and you would proceed on a guess.
- Write a context/assessment file that claims success.

The correct and ONLY action is to stop now and let the stage exit non-zero. This
is a configuration fault in repo routing (the orchestrator set the wrong
`NIGHTGAUGE_TARGET_REPO` for this worktree); it is fixed upstream in the
orchestrator, never worked around inside the stage. The `[repo-mismatch]` marker
is how the pipeline classifies and surfaces it.

This check is a no-op — and the stage proceeds normally — when
`NIGHTGAUGE_TARGET_REPO` is unset (e.g. manual single-stage CLI
invocations) or already matches the current repo.


---

### Phase 2: Issue Selection

```bash
printf '<!-- phase:start name="issue-selection" index=1 total=14 stage="issue-pickup" -->\n'
```

#### Step 2.1: Parse Arguments

Check the skill arguments for issue number, `-i`/`--interactive` flag, or `--label`.

**DEFAULT BEHAVIOR**: When no issue number and no `-i` flag is provided, the
skill MUST use auto-selection mode. Interactive mode is ONLY used when
explicitly requested with `-i` or `--interactive`.

### Auto-Selection Algorithm

**PURPOSE**: Automatically select the highest priority issue using a 7-tier
algorithm. This is the DEFAULT behavior when no issue number is provided and
`-i` flag is not present.

#### Global Filters (Applied to ALL Tiers)

- REQUIRE: project board Status field = "Ready"
- EXCLUDE: `type:epic` label
- EXCLUDE: issues with open blockers (unless mode is `ignore`)

**FIRST MATCH WINS**: Stop at the first tier that returns an issue.

#### Priority Tiers

| Tier | Criteria                                         | Reason Template                                       |
| ---- | ------------------------------------------------ | ----------------------------------------------------- |
| 1    | `--assignee @me` + `priority:critical` or `high` | "Assigned to you with priority:critical/high (ready)" |
| 2    | Any + `priority:critical` or `high`              | "Has priority:critical/high label (ready)"            |
| 3    | `--assignee @me` + has milestone (sort by dueOn) | "Assigned to you in milestone: [title] (due [date])"  |
| 4    | Any + has milestone (sort by dueOn)              | "In milestone: [title] (soonest deadline, ready)"     |
| 5    | `--assignee @me` (any open)                      | "Assigned to you (ready)"                             |
| 6    | `priority:medium` label                          | "Has priority:medium label (ready)"                   |
| 7    | Oldest open issue (sort by createdAt)            | "Oldest open issue (ready)"                           |

#### Reference Filter Pattern

**Pull the board ONCE, then run every tier against that one payload.**

The board read is the expensive call in this skill: a ProjectV2 page costs
17 GraphQL points against an hourly budget of 5000, and the previous version
of this section put a raw GitHub-CLI `project item-list` inside the tiers
with no `--limit`. Seven tiers, two of them retrying critical-then-high, is
up to fourteen whole-board pulls for one pickup — and a raw `gh` call is
invisible to `nightgauge api-usage`, so that spend never appeared in any
report. Read it once into a file; the tiers are `jq` filters over that file
and cost nothing.

```bash
BINARY=$(command -v nightgauge 2>/dev/null || echo "nightgauge")

# Step 1: one board read, cached and ledgered, for the whole selection.
BOARD=$(mktemp -t ng-board.XXXXXX.json)
"$BINARY" board list --status Ready --json > "$BOARD" || exit 1

# Every tier filters THIS file. Never re-read the board inside the loop.
CANDIDATES=$(jq '[.[] | select(.type == "ISSUE" and .state == "OPEN")
  | { number, title, labels, assignees, milestone, createdAt }
  | select(.labels | index("type:epic") | not)
]' "$BOARD")
```

Apply the tier's own filter to `$CANDIDATES` with `jq` — see the tier table
above for what each one selects — and stop at the first tier that yields a
row. Then, and only then, check blockers on the survivors:

```bash
# Step 2: blocker check, on the selected tier's candidates only.
# (blockedBy is not a --json field on any CLI; check-deps reads the native
# relationship and the dependencies the issue body declares. It is one call
# per issue, so it runs on the tier that matched, never on all seven.)
# Only a check that ran and answered `false` admits a candidate. A check that
# failed printed no verdict, and an unknown is never "no open dependencies"
# (#1700): that candidate is skipped, and the next one is tried.
ISSUE=$(printf '%s\n' "$TIER_CANDIDATES" | jq -r '.[].number' | while read -r n; do
  CHECK_EXIT=0
  RESULT=$("$BINARY" hook check-deps "$n" --check-only 2>/dev/null) || CHECK_EXIT=$?
  OPEN=$(printf '%s\n' "$RESULT" | jq -r '.has_open_dependencies' 2>/dev/null)
  if [ "$CHECK_EXIT" -eq 0 ] && [ "$OPEN" = "false" ]; then
    printf '%s\n' "$TIER_CANDIDATES" | jq -c ".[] | select(.number == $n)" | head -1
    break
  fi
  case "$OPEN" in
    true) ;;
    *) echo "#$n skipped: its dependency check could not run (exit $CHECK_EXIT)" >&2 ;;
  esac
done)

rm -f "$BOARD"
```

**NOTE**: No CLI `issue list --json` surface supports `blockedBy` as a
field. Ask `nightgauge hook check-deps`, which reads the native relationships
through GraphQL and also the dependencies the issue body declares. Do NOT use
`trackedInIssues` — that is a different GitHub feature (task list checkboxes)
and does not represent blocking.

Tier-specific `[TIER_FLAGS]`:

- Tier 1: `--assignee @me --label "priority:critical"` (then fallback
  `--label "priority:high"`)
- Tier 2: `--label "priority:critical"` (then fallback
  `--label "priority:high"`)
- Tier 3: `--assignee @me` + jq `select(.milestone) | sort_by(.milestone.dueOn)`
- Tier 4: jq `select(.milestone) | sort_by(.milestone.dueOn)`
- Tier 5: `--assignee @me`
- Tier 6: `--label "priority:medium"`
- Tier 7: jq `sort_by(.createdAt)`

Each tier checks critical first, then high (for tiers 1-2). If `$ISSUE` is empty
or `"null"`, proceed to next tier.

#### Display Auto-Selected Issue

Present the auto-selected issue with reasoning:

```
Issue:  #42 - Add user profile photo upload
Type:   enhancement
Reason: Assigned to you with priority: high label
```

#### Confirm Selection

Confirm selection: offer Yes / No, show all issues / Cancel options.

- **Yes**: Proceed with selected issue
- **No, show all issues**: Fall back to Interactive Mode
- **Cancel**: Exit skill

#### Interactive Mode (Fallback or `-i` flag)

When `-i` flag is provided or user rejects auto-selection:

```bash
# List open issues (`issue list` returns open issues only), first 15
REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug)}"
: "${REPO:?set NIGHTGAUGE_REPO or run inside a clone with an origin remote}"
nightgauge issue list --repo "$REPO" --json \
  | jq -r '(. // [])[:15][] | "#\(.number) - \(.title) [\(.labels // [] | join(", "))]"'
```

Present list to user and let them select an issue.

#### No Issues Available

If no issues with "Ready" status on the project board exist:

```
No issues with Ready status found on the project board.

Options:
1. Create a new issue: /nightgauge-issue-create
2. Check all open issues: nightgauge issue list --repo <owner/name>
3. Set an issue to Ready on the project board: `nightgauge project sync-status <number> ready`
```

#### All Issues Blocked

If all tiers are exhausted but blocked issues exist, find the least-blocked
option (fewest open dependencies):

```bash
# Reuse the ONE board payload already read in Step 1 — do not read it again.
CANDIDATES=$(printf '%s\n' "$CANDIDATES" | jq -r '.[].number')

BLOCKED_ISSUES="[]"
for n in $CANDIDATES; do
  BINARY=$(command -v nightgauge 2>/dev/null || echo "nightgauge")
  # A failed check is an unknown, not an unblocked issue (#1700): leave it out.
  if ! RESULT=$("$BINARY" hook check-deps "$n" 2>/dev/null); then
    echo "#$n left out: its dependency check could not run" >&2
    continue
  fi
  HAS_DEPS=$(printf '%s\n' "$RESULT" | jq -r '.has_open_dependencies' 2>/dev/null)
  if [ "$HAS_DEPS" = "true" ]; then
    ENTRY=$(printf '%s\n' "$RESULT" | jq '{
      number: .issue_number,
      blockers: .open_dependencies,
      blocker_count: .open_count
    }')
    BLOCKED_ISSUES=$(printf '%s\n' "$BLOCKED_ISSUES" "[$ENTRY]" | jq -s 'add | sort_by(.blocker_count)')
  fi
done

LEAST_BLOCKED=$(printf '%s\n' "$BLOCKED_ISSUES" | jq '.[0]')
```

Display the least-blocked issue with its blockers. Offer options: Pick up
blocker instead (recommended) / Pick up anyway / Show all blocked / Cancel.


---

### Phase 2.5: Signal Stage Start

```bash
printf '<!-- phase:start name="signal-stage-start" index=2 total=14 stage="issue-pickup" -->\n'
```

Signal the stage start by moving the issue's board status to `in-progress`
(runs after issue selection so `$ISSUE_NUMBER` is set).

> **Read `_includes/issue-selection-and-gates.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 2.7: Size Gate Preflight

```bash
printf '<!-- phase:start name="size-gate-preflight" index=3 total=14 stage="issue-pickup" -->\n'
```

Reject or soft-route issues that exceed pipeline size thresholds before
committing resources to branch creation and analysis.

> **Read `_includes/issue-selection-and-gates.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 2.8: Baseline-CI Dependency Gate

```bash
printf '<!-- phase:start name="baseline-ci-gate" index=4 total=14 stage="issue-pickup" -->\n'
```

Defer dispatch of issues whose acceptance criteria require promoting a CI check
on `main` when `main`'s recent runs of that check are failing.

> **Read `_includes/issue-selection-and-gates.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 2.9: Native blockedBy Dependency Gate

```bash
printf '<!-- phase:start name="blocked-dependency-gate" index=5 total=14 stage="issue-pickup" -->\n'
```

Defer pickup of issues that have an OPEN native `blockedBy` dependency (the
blocker's PR is not merged). A controlled hold, not a failure — the item is
paused and automatically re-queued when its blockers close.

> **Read `_includes/issue-selection-and-gates.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 3: Issue Analysis

```bash
printf '<!-- phase:start name="issue-analysis" index=6 total=14 stage="issue-pickup" -->\n'
```

Fetch the full issue, parse its content, derive the change-detection/routing
decision, and produce the requirements summary.

> **Read `_includes/issue-analysis.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

The following two sub-steps stay in this body (they expand shared `_shared`
includes) — apply them after Step 3.1.4 and before Step 3.2 as the reference
file directs:

#### Step 3.1.5: Check for Epic Type

### Epic Detection and Handling

## Contents

- [Epic Branch Lifecycle](#epic-branch-lifecycle)
- [Epic Setup Requirements](#epic-setup-requirements)
- [Cross-Epic Blocking](#cross-epic-blocking-mandatory)
- [Detect Epic Type](#detect-epic-type)
- [If Epic Detected](#if-epic-detected)
- [Auto-Selection Exclusion](#auto-selection-exclusion)
- [Epic Completion Check](#epic-completion-check-post-merge)

**PURPOSE**: Detect epic issues and handle them appropriately. Epics are
tracking issues with the `type:epic` label that organize sub-issues — they are
not directly actionable work items.

#### Epic Branch Lifecycle

Every sub-issue of an epic uses an epic branch as its merge target:

1. **First sub-issue pickup**: Creates `epic/{PARENT}-{slug}` from default
   branch (lazy creation in issue-pickup Step 5.4)
2. **Subsequent sub-issues**: Branch from existing epic branch
3. **Sub-issue PRs**: Target epic branch (not main). Use `Part of #EPIC` instead
   of `Closes #ISSUE` to avoid premature issue closure
4. **Last sub-issue merges**: pr-merge detects epic branch target, runs
   completion check, creates epic→main PR (Step 2.5)
5. **Epic PR merges**: Epic branch deleted by GitHub PR cleanup

The epic branch belongs to the epic's own repository: `epic/{PARENT}-*` names
issue `#{PARENT}` of the repository it is pushed to. A sub-issue whose epic
lives in **another** repository has no epic branch. It branches from, and its
PR targets, its own repository's default branch, and `git branch-create`
reports `parent_issue` and `epic_branch` as `null` for it.

This workflow is **STATELESS** — relies on git remote state (`git ls-remote`)
and GitHub API (GraphQL parent detection, issue state). Works regardless of
queuing method (batch, individual, mixed).

#### Epic Setup Requirements

When creating epics with sub-issues, ALL of the following are required:

1. **Link sub-issues** via `addSubIssue` GraphQL mutation (not body text)
2. **Add to project board** — epic AND all sub-issues via `addProjectV2ItemById`
3. **Set Status field** — epic and **all sub-issues to "Ready"**. The pipeline
   uses `blockedBy` to enforce ordering, NOT board status. Only use "Backlog"
   for issues genuinely not ready for work.
4. **Set intra-epic blocking** (for sequential phases) via `addBlockedBy`
5. **Set cross-epic blocking** when this epic depends on another epic

Without steps 2-3, issues are invisible in the extension's board views. Without
steps 4-5, the pipeline may execute issues out of order.

#### Cross-Epic Blocking (MANDATORY)

When an epic depends on work from another epic, you MUST set blocking
relationships at **both** levels:

1. **Epic-to-epic**: The dependent epic is blocked by the prerequisite epic.
   This lets the scheduler skip the entire epic's sub-issues with a single
   parent check.
2. **Sub-issue-to-sub-issue**: The dependent epic's root sub-issues (those with
   no intra-epic blockers) are blocked by the prerequisite epic's leaf
   sub-issues (those that no other sub-issue depends on).

**Example**: Epic B (Pipeline Integration) depends on Epic A (Schema
Foundation):

```
Epic A (#1672) ──blocks──→ Epic B (#1678)

Epic A leaf sub-issues:
  #1675 (KnowledgeService)  ──blocks──→  #1679 (first root of Epic B)
  #1676 (wiki-link resolver) ──blocks──→  #1679
```

The scheduler checks both direct `blockedBy` on each issue AND whether the
issue's parent epic has open `blockedBy` entries. Without cross-epic blocking,
the pipeline will execute dependent work before its prerequisites exist.

**Detection**: When creating an epic, scan its body for references to other
epics ("builds on #NNN", "depends on", "requires", "after epic #NNN"). See
`issue-create` SKILL.md Phase 3.5 Step 1 for the full procedure.

Each sub-issue appears in exactly one board tab matching its project board
status. Blocked sub-issues display with 🔒 lock icons in the Ready tab.

#### Detect Epic Type

```bash
LABELS=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.labels[].name' 2>/dev/null)
if printf '%s\n' "$LABELS" | grep -q "^type:epic$"; then
  IS_EPIC=true
fi
```

#### If Epic Detected

1. **Check if all sub-issues are complete** using Go binary `epic check-completion`:

   ```bash
   ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
   : "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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
   [ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
   if [ -z "$BINARY" ]; then
     echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
     exit 1
   fi
   EPIC_STATUS=$("$BINARY" epic check-completion "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"complete":false}')
   EPICS_CLOSED=$(printf '%s\n' "$EPIC_STATUS" | jq 'if .complete == true then 1 else 0 end')
   if [ "$EPICS_CLOSED" -gt 0 ]; then
     echo "Epic #$ISSUE_NUMBER had all sub-issues completed. Auto-closed and synced to Done."
     exit 1
   fi
   ```

2. **Fetch sub-issues** with the binary, which reads the whole list, page by
   page, or exits non-zero; it never returns part of it as the whole (#1701).
   A fixed `subIssues(first: N)` read stops at its first page with no sign
   that more exist.

   ```bash
   ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
   : "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
   # stdout only: a warning on stderr must not land in the JSON.
   if ! RESULT=$(nightgauge issue list --epic "$ISSUE_NUMBER" --json); then
     echo "ERROR: could not read the sub-issues of epic #$ISSUE_NUMBER" >&2
     exit 1
   fi
   # Each entry carries its own repo; the binary writes subIssues as null
   # when there are none.
   printf '%s\n' "$RESULT" | jq -r '.subIssues // [] | .[] |
     "#\(.number) - \(.title) [\(.state)] (\(.repo))"'
   ```

3. **Display Epic message**: Show that the issue is an Epic and list its
   sub-issues with their labels/status.

4. **Offer sub-issue selection**: Let user pick a ready sub-issue instead.

5. **If user selects a sub-issue**: Proceed with that issue number.

6. **If user cancels**: Exit with non-zero code to halt pipeline (`exit 1`).

   **CRITICAL**: The `exit 1` ensures the orchestrator receives a non-zero exit
   code, which triggers `success: false` in the skill result.

#### Auto-Selection Exclusion

Epics are **automatically excluded** from all tiers of the auto-selection
algorithm via the jq filter:

```bash
select(.labels | map(.name) | index("type:epic") | not)
```

#### Epic Completion Check (Post-Merge)

After merging a PR, check if the completed issue's parent epic is ready to
close. This check triggers in two scenarios:

1. **Epic branch merge**: pr-merge detects `MERGED_INTO` starts with `epic/`,
   extracts the epic number from the branch name, and runs the check directly
2. **Non-epic-branch merge**: The standard check below runs using the issue
   number to find parent epics

Both paths converge at Step 2.5 (epic branch PR creation) when all sub-issues
are complete.

**Step 1: Check completion status (check-only mode)**

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  exit 1
fi
EPIC_RESULT=$("$BINARY" epic check-completion "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"complete":false}')
# Determine if epic is ready to close
EPICS_READY=$(printf '%s\n' "$EPIC_RESULT" | jq 'if .complete == true then 1 else 0 end')
EPIC_ACTION=$( [ "$EPICS_READY" -gt 0 ] && echo "ready-to-close" || echo "not-ready" )
```

**Step 2: Generate summary if epic is ready to close**

If `action == "ready-to-close"`:

```bash
EPIC_NUMBER=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.epicNumber')
EPIC_TITLE=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.title')
# a+b. Classify the tier and generate the summary in one deterministic call
#      (no AI tokens). Tiers: minimal, standard, detailed.
SUMMARY_JSON=$(nightgauge epic summary "$EPIC_NUMBER" --json 2>/dev/null || echo '{}')
TIER=$(printf '%s\n' "$SUMMARY_JSON" | jq -r '.tier // "minimal"')
SUMMARY=$(printf '%s\n' "$SUMMARY_JSON" | jq -r '.summary // empty')

# c. Post summary as issue comment (if generated)
if [ -n "$SUMMARY" ]; then
  gh issue comment "$EPIC_NUMBER" --body "$SUMMARY" 2>/dev/null || true
fi

# d. For the detailed tier: commit summary to docs/epics/{N}-{slug}.md
if [ "$TIER" = "detailed" ] && [ -n "$SUMMARY" ]; then
  SLUG=$(echo "$EPIC_TITLE" | tr '[:upper:]' '[:lower:]' | \
    sed 's/[^a-z0-9 -]//g' | tr ' ' '-' | sed 's/--*/-/g' | sed 's/^-//;s/-$//' | \
    cut -c1-40 | sed 's/-$//')
  SUMMARY_FILE="docs/epics/${EPIC_NUMBER}-${SLUG}.md"
  mkdir -p docs/epics
  echo "$SUMMARY" > "$SUMMARY_FILE"
  git add "$SUMMARY_FILE"
  git commit -m "docs(#${EPIC_NUMBER}): add epic completion summary" || true
  git push origin HEAD 2>/dev/null || true
fi
```

**Step 2.5: Create Epic Branch PR — DETERMINISTIC SCRIPT**

**CRITICAL**: Use the Go binary `pr create` command to create the epic branch PR.

```bash
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
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  exit 1
fi
# Find epic branch
EPIC_BRANCH=$(git branch -r --list "origin/epic/${EPIC_NUMBER}-*" 2>/dev/null | head -1 | sed 's|origin/||; s/^ *//')
if [ -z "$EPIC_BRANCH" ]; then
  echo "WARNING: No epic branch found for epic #$EPIC_NUMBER — skipping PR creation" >&2
else
  PR_RESULT=$("$BINARY" pr create     --title "feat(#${EPIC_NUMBER}): ${EPIC_TITLE}"     --head "$EPIC_BRANCH"     --base main     --body "Epic #${EPIC_NUMBER} completion: all sub-issues are closed."     --json 2>/dev/null) || true
  PR_ACTION=$(printf '%s\n' "$PR_RESULT" | jq -r '.action // "created"')
  PR_URL=$(printf '%s\n' "$PR_RESULT" | jq -r '.prUrl // ""')

  if [ -n "$PR_URL" ] || [ "$PR_ACTION" = "created" ] || [ "$PR_ACTION" = "already-exists" ]; then
    echo "Epic PR ready: $PR_URL"
    EPIC_BRANCH_PR_CREATED=true
  else
    echo "WARNING: Go binary pr create returned action=$PR_ACTION"
  fi
fi
```

**Step 3: Close the epic** (only when no epic branch workflow)

Skipped if `EPIC_BRANCH_PR_CREATED=true` (Step 2.5 handled it). When the epic
branch PR merges, GitHub auto-closes the epic via `Closes #N` in the PR body.

```bash
if [ "${EPIC_BRANCH_PR_CREATED:-false}" != "true" ]; then
  CLOSE_COMMENT="All sub-issues have been completed. Closing epic."
  if [ -n "$SUMMARY" ]; then
    CLOSE_COMMENT="Epic summary has been posted above. Closing epic."
  fi

  gh issue close "$EPIC_NUMBER" --comment "$CLOSE_COMMENT" 2>/dev/null || true
  # GitHub Projects built-in workflow handles Status → Done on close.
fi
```

**Error handling**: Summary generation is wrapped in fallback logic. If any step
fails, execution falls through to closing the epic with a standard comment.
Summary failure NEVER blocks epic closure.

**No epics ready**: If `action != "ready-to-close"` or no epics found, this is a
no-op.


#### Step 3.1.6: Check Dependencies

### Dependency Checking (Enforcement)

## Contents

- [Check Dependency Configuration](#check-dependency-configuration)
- [Fetch Dependencies](#fetch-dependencies)
- [Handle Open Dependencies](#handle-open-dependencies)

**PURPOSE**: Check if an issue has open dependencies (blockers) before
proceeding. This prevents wasted effort on issues that can't be completed until
prerequisite work lands.

#### Check Dependency Configuration

```bash
# Check if dependency enforcement is enabled (default: true)
DEP_ENABLED=$(yq -r '.enforcement.dependencies.enabled // "true"' .nightgauge/config.yaml 2>/dev/null || echo "true")

# Get enforcement mode: warn | block | ignore (default: warn)
DEP_MODE=$(yq -r '.enforcement.dependencies.mode // "warn"' .nightgauge/config.yaml 2>/dev/null || echo "warn")

# Check if transitive checking is enabled (default: false)
DEP_TRANSITIVE=$(yq -r '.enforcement.dependencies.check_transitive // "false"' .nightgauge/config.yaml 2>/dev/null || echo "false")
```

#### Fetch Dependencies

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"

if [ "$DEP_ENABLED" = "true" ] && [ "$DEP_MODE" != "ignore" ]; then
  if [ -z "$BINARY" ]; then
    echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
    echo "This binary is required for dependency enforcement." >&2
    exit 1
  fi
  # A check that could not run is not "no open dependencies" (#1700). Stop,
  # rather than pick up an issue whose blockers may be open.
  DEP_EXIT=0
  DEP_RESULT=$("$BINARY" hook check-deps "$ISSUE_NUMBER") || DEP_EXIT=$?
  HAS_OPEN_DEPS=$(printf '%s\n' "$DEP_RESULT" | jq -r '.has_open_dependencies' 2>/dev/null)
  if [ "$DEP_EXIT" -ne 0 ] || { [ "$HAS_OPEN_DEPS" != "true" ] && [ "$HAS_OPEN_DEPS" != "false" ]; }; then
    echo "ERROR: could not check #$ISSUE_NUMBER's dependencies (exit $DEP_EXIT); not proceeding." >&2
    echo "Re-run once 'nightgauge hook check-deps $ISSUE_NUMBER' succeeds." >&2
    exit 1
  fi
  SHOULD_BLOCK=$(printf '%s\n' "$DEP_RESULT" | jq -r '.should_block')
  OPEN_DEPS=$(printf '%s\n' "$DEP_RESULT" | jq -r '.open_dependencies')
  OPEN_COUNT=$(printf '%s\n' "$DEP_RESULT" | jq -r '.open_count')
fi
```

#### Handle Open Dependencies

If the issue has open dependencies, display the blocking issues and their count.

##### Block Mode → Deferral (Issue #231)

If `enforcement.dependencies.mode: block`, **defer** the issue rather than
reporting a pipeline failure. An open native `blockedBy` dependency is a
controlled hold, not an organic failure — mirror the baseline-CI gate
(Phase 2.8): pause the queue item, post a deferral comment naming the blockers,
print `signal=deferred`, and exit **0** so the orchestrator records a deferral
(not a failure) and auto-requeues when the blockers close.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# $BINARY was resolved in the "Fetch Dependencies" block above.
GATE_OUTPUT=$("$BINARY" deps-gate check --issue "$ISSUE_NUMBER" --json 2>&1)
GATE_EXIT=$?

if [ $GATE_EXIT -eq 1 ]; then
  # Deferred: the gate paused the queue item (kind=blocked_dependency) and
  # emitted the open blockers as JSON. Post a deferral comment naming each.
  BLOCKERS=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.open_dependencies[]? | "- #\(.number) \(.title)"' 2>/dev/null)
  REASON=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.reason // "blocked by open dependency"' 2>/dev/null)

  COMMENT_BODY="## Dependency Gate — Deferred

This issue has an open \`blockedBy\` dependency (the blocker's PR is not merged),
so pickup is deferred ([\`blocked-dependency\`](../../docs/FAILURE_TAXONOMY.md#infrastructure)):

$BLOCKERS

The pipeline has paused this item and will automatically resume dispatch when the
blockers close (\`deps-gate promote\` sweep, or the autonomous cascade). No
operator action required."

  # `forge issue comment` takes the issue NODE id, not its number.
  REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug)}"
  ISSUE_NODE_ID=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo "$REPO" --json 2>/dev/null \
    | jq -r '.nodeId // empty')
  nightgauge forge issue comment --subject-id "$ISSUE_NODE_ID" --body "$COMMENT_BODY" 2>/dev/null || \
    echo "warning: failed to post deferral comment to #$ISSUE_NUMBER"

  "$BINARY" outcome record \
    --issue "$ISSUE_NUMBER" \
    --stage "issue-pickup" \
    --outcome "deferred" \
    --reason "[blocked-dependency] $REASON" 2>/dev/null || true

  echo "Dependency gate: DEFERRED"
  echo "signal=deferred"
  exit 0
fi

if [ $GATE_EXIT -ne 0 ]; then
  # Exit 2: the gate could not evaluate the blockers (a read failed). Nothing
  # was paused, so post no deferral comment and record no deferral. Stop the
  # pickup; a retry evaluates again (#1700).
  echo "Dependency gate: could not evaluate #$ISSUE_NUMBER's blockers (exit $GATE_EXIT); not picking it up." >&2
  printf '%s\n' "$GATE_OUTPUT" >&2
  exit 1
fi
```

Exit the skill without creating a branch. On a deferral (exit 1) do **not**
emit a failure report: the run must exit 0. Exit 2 means the gate could not
run, which is neither a pass nor a deferral: the stage stops, and a retry
checks again.

##### Warn Mode (Default)

If `enforcement.dependencies.mode: warn`, display open blockers and offer
options: Pick up blocker #X instead (recommended) / Continue anyway / View
dependencies / Cancel.

If multiple blockers exist, recommend the least-blocked one (fewest open
dependencies).

- **Pick up blocker #X instead** (recommended): Restart with the blocker's issue
  number. If multiple blockers exist, present the list sorted by blocker count
  (least-blocked first) and let the user choose.
- **Continue anyway**: Record override in context file and proceed
- **View dependencies**: Display full details of each blocker
- **Cancel**: Exit skill

##### Record Override Decision

If user chooses "Continue anyway":

```bash
DEPENDENCY_OVERRIDE=true
DEPENDENCY_OVERRIDE_REASON="User acknowledged open dependencies"
```

This will be stored in the context file for downstream skills.

##### Handle Blocker Redirect

If user chooses "Pick up blocker #X instead":

1. Set `ISSUE_NUMBER` to the selected blocker's issue number
2. Restart issue analysis with the new issue number (re-fetch issue details,
   re-run epic check, re-run dependency check)
3. The original issue is NOT recorded in the context — the skill now operates
   entirely on the blocker issue

If the selected blocker also has open dependencies, the recursive restart will
naturally present the same options for the blocker's dependencies.


---

### Phase 4: Read Git Workflow

```bash
printf '<!-- phase:start name="read-git-workflow" index=7 total=14 stage="issue-pickup" -->\n'
```

#### Step 4.1: Check for Workflow Documentation

```bash
ls docs/GIT_WORKFLOW.md 2>/dev/null || ls docs/TFS_WORKFLOW.md 2>/dev/null
```

#### Step 4.2: If docs/GIT_WORKFLOW.md Exists

Read it to extract branch naming conventions, commit message format, and any
special requirements.

#### Step 4.3: If No Workflow Docs

Use default conventions: `feat/<issue>-<desc>`, `fix/<issue>-<desc>`,
`docs/<issue>-<desc>`, `refactor/<issue>-<desc>`.

---

### Phase 5: Branch Creation

```bash
printf '<!-- phase:start name="branch-creation" index=8 total=14 stage="issue-pickup" -->\n'
```

Verify a clean working tree, then create the feature branch deterministically
via the Go binary (prefix/slug derivation, parent-epic detection, lazy
epic-branch creation, idempotent re-runs).

> **Read `_includes/branch-and-env.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 6: Environment Setup

```bash
printf '<!-- phase:start name="environment-setup" index=9 total=14 stage="issue-pickup" -->\n'
```

#### Step 6.1: Push Branch to Remote

```bash
git push -u origin <branch-name>
```

#### Step 6.2: Assign Issue (Optional)

Offer self-assignment only when the configured forge exposes an assignment
command. The provider-neutral `forge issue edit` command edits by node ID and
does not support `--add-assignee`; do not pass GitHub CLI-only flags to it.

---

### Date Field Automation (Optional)

**PURPOSE**: Automatically set GitHub Project date fields to keep the Roadmap
view current. Call date automation hooks: set Start date to today, set Target
date from milestone if unset.

| Config Key           | Default | Description                      |
| -------------------- | ------- | -------------------------------- |
| `project.auto_dates` | `false` | Enable date automation           |
| `project.number`     | -       | GitHub Project number (required) |

**Steps** (skip entire phase if `auto_dates` is not `true`):

1. Get project number and owner from config
2. Find the issue's project item ID via `gh project item-list`
3. Get date field IDs dynamically via `gh project field-list`
4. Set Start date to today:
   `gh project item-edit --field-id "$START_DATE_FIELD" --date "$TODAY"`
5. If Target date is unset, derive from milestone `dueOn`:
   `gh project item-edit --field-id "$TARGET_DATE_FIELD" --date "$MILESTONE_DUE"`


---

### Phase 7: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=10 total=14 stage="issue-pickup" -->\n'
```

Present final summary:

```
Issue:    #<number> - <title>
Type:     <bug|feature|docs|refactor>
Branch:   <branch-name>
Status:   Ready for development

## Next Steps
1. Run `/feature-planning` to design the implementation
2. Or start coding if the approach is clear
```

---

### Phase 8: Write Context File

```bash
printf '<!-- phase:start name="write-context" index=11 total=14 stage="issue-pickup" -->\n'
```

**PURPOSE**: Write structured context file for downstream pipeline skills.
All data is available in shell variables at this point — write the context
inline using `jq -n` for safe JSON construction.

The file is still yours to write. If the stage exits 0 and the file is missing,
is not valid JSON, does not match the schema, or has no `branch`, the
orchestrator repairs it from the worktree's HEAD and the issue before the gate
reads it. It does this only when HEAD is on this issue's branch (#1919). A
recovered file is a recorded fault, not a success path.

> **Read `_includes/context-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Run Steps 8.1 and 8.2 there, then emit the `knowledge-scaffolding` marker below and continue through Steps 8.3–8.7.

#### Step 8.3: Knowledge Scaffolding (MANDATORY)

```bash
printf '<!-- phase:start name="knowledge-scaffolding" index=12 total=14 stage="issue-pickup" -->\n'
```

Scaffold the per-issue (and optionally workspace-level) knowledge base, populate
the routing field, verify the context file, and signal stage completion — all
detailed in the reference file already loaded for this phase (Steps 8.3–8.7).

---

---

## Output Contract

This skill outputs `issue-{N}.json` in the clone's pipeline state directory
(`nightgauge layout path pipeline`) for use by downstream skills.

**Schema**: See
[docs/CONTEXT_ARCHITECTURE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONTEXT_ARCHITECTURE.md) for full
schema documentation.

**Read by**: `/nightgauge-feature-planning`

---

### Phase 10: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="issue-pickup" -->\n'
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

## Error Handling

| Condition             | Action                                                                                                                                                         |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Issue not found       | Display error with issue number, suggest verifying access. Hint: `nightgauge forge issue view <number> --repo $REPO --web`                                     |
| Branch already exists | Switch to existing branch (`git checkout <branch-name>`). Do NOT prompt — this is a re-run. Continue through all phases to ensure `issue-{N}.json` is written. |
| Authentication failed | Display: "Forge auth not configured. Run: `nightgauge forge auth login`"                                                                                       |
