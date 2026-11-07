---
name: issue-create
description: Create well-structured GitHub issues with SDLC metadata, project board sync,
  and optional parent/child linking. Use when filing a new issue or epic that the
  pipeline will pick up, so it lands board-ready with correct labels and links.
  User-invoked; the user types `/nightgauge:issue-create`.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.26.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
---

# Issue Create

Create GitHub issues that are immediately usable by the Nightgauge
pipeline.

## Outcomes

- Creates issue with complete SDLC metadata
- Ensures a consistent type label; priority, size and status as board fields
- Assigns milestone when required by repo workflow
- Optionally links to parent issue/epic
- Adds created issue to the GitHub Project board via deterministic hook
- Optionally scaffolds a knowledge directory at creation time
  (`--with-knowledge`)
- Separates execution-ready implementation work from decision-oriented spikes

## Required Metadata

Each created issue must include:

- Exactly one `type:*` label
- Priority and Size set as project board fields (not labels) — add to board
  with `nightgauge project add`, then set fields via GraphQL mutations
  (see Phase 4)
- Milestone when required by team process
- A clear issue intent: implementation or spike

Use the target repository's own label conventions when it documents them. If label docs are missing, infer from existing repo labels using
`nightgauge label list --json`.

## References

Core Nightgauge docs (not files in the target repository):

- Configuration: [CONFIGURATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md)
- Project board behavior: [PROJECT_SETUP.md](https://github.com/nightgauge/nightgauge/blob/main/docs/PROJECT_SETUP.md)
- Issue quality expectations: [ISSUE_TO_PR_WORKFLOW.md](https://github.com/nightgauge/nightgauge/blob/main/docs/ISSUE_TO_PR_WORKFLOW.md) and
  [CONTRIBUTING.md](https://github.com/nightgauge/nightgauge/blob/main/CONTRIBUTING.md)
- Context and pipeline expectations: [CONTEXT_ARCHITECTURE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONTEXT_ARCHITECTURE.md)

Do not inline large label taxonomies or template text in this skill.

## Supporting files (load on demand)

- `_includes/environment-and-content.md` (same directory as this SKILL.md) — read in Phases 1, 2
- `_includes/epic-routing.md` (same directory as this SKILL.md) — read in Phases 2.4, 2.5, 2.6
- `_includes/spike-routing.md` (same directory as this SKILL.md) — read in Phases 2.7, 2.X
- `_includes/scope-gates.md` (same directory as this SKILL.md) — read in Phases 2.8, 2.85, 2.9
- `_includes/create-and-dependencies.md` (same directory as this SKILL.md) — read in Phases 3, 3.5
- `_includes/board-and-audit.md` (same directory as this SKILL.md) — read in Phases 4, 4.5, 4.6, 4.7, 4.8, 4.9, 5, 6

## Gotchas

- **Persist the routing manifest BEFORE any forge mutation (#3232).** Epics with
  sub-issues silently misroute to the wrong repo/project without it. The manifest
  must exist and validate before a single issue is created.
- **Oversized scope must be gated BEFORE creation (#3811).** One oversized issue
  burned $112.77 in feature-dev. Decompose into an epic or attach an explicit
  oversized-scope marker first.
- **Board sync needs BOTH `--repo` and `--project` — no defaults.** Relying on
  defaults caused the #3232 silent-misroute. Every created issue is added with
  both flags explicit.
- **Never block a sub-issue on its own epic.** That creates a dependency cycle;
  use `addBlockedBy` only between siblings/prerequisites.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

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

Verify the Go binary, `GITHUB_TOKEN`, and a GitHub-backed remote are present before any work; stop with an exact remediation command if a prerequisite fails.

> **Read `_includes/environment-and-content.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2: Build Issue Content

Classify the issue (implementation vs spike), draft a concise, actionable title and body with acceptance criteria **and a `## Verification` section a reviewer could run**, state any security hazard in the proposed design as an acceptance criterion, choose the parent epic from the open-epic list, determine labels, and size the issue from the complexity model and file-based heuristics.

> **Read `_includes/environment-and-content.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.4: Multi-Repo Sub-Issue Routing (Mandatory for Epics)

**Gate**: Runs UNCONDITIONALLY for any epic with sub-issues; skipped for standalone issues. Build, hard-gate, and persist a routing manifest mapping each sub-issue to its target repo/project BEFORE any GitHub mutation — the #3232 footgun defense.

> **Read `_includes/epic-routing.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.5: Parallel Decomposition (Agent Teams)

**Gate**: Check `agent_teams.enabled`; if disabled, skip to Phase 2.6. Detect dependencies, compute execution waves, assign per-issue complexity and model suggestions, and embed dependency metadata in each sub-issue body.

> **Read `_includes/epic-routing.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.6: Dependency Analysis (Always for Epics)

**Gate**: Runs for epics with 2+ sub-issues when Phase 2.5 was skipped. Use the Go binary (`epic plan-waves`) for deterministic wave assignment, with a prose-based fallback when sub-issues are not yet created.

> **Read `_includes/epic-routing.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.7: Spike + Dependent Implementation Routing

**Gate**: Runs for epics with a spike + dependents, or cross-repo dependents sharing an architectural decision. Route each dependent group through exactly one of Path A (recommendations), Path B (concurrent siblings — triggers the single-point-of-failure guard), or Path C (spike-with-implementation, the cross-repo default).

> **Read `_includes/spike-routing.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.8: Cross-Repo Reality Check (Recommended for API Issues)

**Gate**: Runs when the issue references platform API endpoints, cross-repo dependencies, or companion-repo integration; skip for purely internal issues. Verify endpoint/companion-repo assumptions against reality and record a `## Cross-Repo Dependencies` section in the body.

> **Read `_includes/scope-gates.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.85: Oversized-Scope Hard-Gate

**Gate**: Runs UNCONDITIONALLY for **every issue type**. **Blocking** — an issue bundling many independent targets/refactors MUST be decomposed into an epic, or carry an explicit `<!-- nightgauge:oversized-scope-accepted -->` marker, before any GitHub mutation (the #3811 $112-runaway defense). An issue whose predicted size exceeds the target model's capacity (`nightgauge size-gate capacity`) MUST be decomposed, one level deep.

> **Read `_includes/scope-gates.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.9: Epic Decomposition Hard-Gate

**Gate**: Runs UNCONDITIONALLY when `TYPE_LABEL=epic`; skipped for non-epics. **Blocking** — every epic creation MUST fall into one of three explicit shapes (Path A sub-issues, Path B placeholder chore, Path C standalone declaration) before any GitHub mutation.

> **Read `_includes/scope-gates.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2.X: Spike Artifact Path Selection

**Gate**: Only runs for `type:spike` issues. Compute, validate, and scaffold the artifact path (`docs/spikes/<N>-<slug>.md` and the `yaml recommendations` block) so the issue is spike-contract-conformant by default and `feature-validate` has a concrete path to check.

> **Read `_includes/spike-routing.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 3: Create Issue Deterministically

Create the issue with `nightgauge issue create`/`create-sub`, capturing the issue number. For epics, the Phase 2.4 routing manifest is the mandatory source of truth for every sub-issue's target repo — never re-derive routing. Includes the `type:spike` pre-creation validation hard-gate.

> **Read `_includes/create-and-dependencies.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 3.5: Set Dependency Relationships (Epic Sub-Issues)

**Gate**: Always runs for epics with 2+ sub-issues. Set GitHub's native `blockedBy` relationships (cross-epic in Step 1, intra-epic in Step 2) so the pipeline respects execution ordering. Native `blockedBy` is the relationship to create: labels are invisible to the pipeline and wave planning reads only native edges, while the pickup gate and the dispatcher also honour `Depends on:` / `Blocked by` lines in a body, so keep the two in agreement.

> **Read `_includes/create-and-dependencies.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 4: Sync Project Board and Set Fields (Mandatory)

All created issues MUST be added to the project board with Status set, passing BOTH `--repo` and `--project` from the Phase 2.4 manifest (#3232) — defaults are forbidden. Priority and Size fields are set from labels by `project add`.

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 4.6: Promote to Ready (After All Relationships Are Configured)

**Epics only.** After board sync, fields, `blockedBy`, and `addSubIssue` are all complete, promote the epic and its sub-issues from Backlog to Ready — closing the race window where the scheduler dispatches before relationships exist. A sub-issue whose declared `file_ownership` fails `nightgauge issue check-ownership` stays in Backlog (#561).

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 4.7: Verification (Mandatory)

Verify board fields were actually set (via binary exit codes; assert `.status == "Ready"` for standalone issues). Do NOT report success if any field is empty — empty fields make issues invisible in the tree views.

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 4.8: Cross-Repo Project Membership Audit (Mandatory for Epics)

**Gate**: Runs UNCONDITIONALLY for any epic that produced sub-issues. Audit each sub-issue's actual project membership against the Phase 2.4 manifest; a mismatch is fatal. The manifest is deleted only after this audit passes.

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 4.5: Knowledge Scaffolding (--with-knowledge)

**Gate**: Only runs when `--with-knowledge` is passed. Scaffold a knowledge directory for the new issue (and append a knowledge reference to each sub-issue body for epics).

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 4.9: Write Creation Manifest

Write the strict-mode creation manifest (`issue-create-manifest-<ts>.json`, through `nightgauge layout write pipeline`) consumed by the Phase 6 terminal audit, and validate it parses as JSON.

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 5: Return Structured Result

Return the issue number/URL, final metadata, parent-link status, knowledge path, manifest path, and the suggested next command (`/nightgauge-issue-pickup <issue-number>`).

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 6: Terminal Audit Pass (Mandatory)

**Gate**: Runs UNCONDITIONALLY after Phase 5 unless `--no-audit`. Invoke the model-invocable `nightgauge:issue-audit` skill (Skill tool, arguments `--manifest <path>`); its result is authoritative (0 READY, 1 NEEDS FIXES, 2 failure) and propagates.

> **Read `_includes/board-and-audit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

## Decision Rules

- Prefer deterministic repository state over assumptions.
- If type/priority is ambiguous, ask the minimum needed question.
- If milestone policy is unclear, inspect recent issues for precedent.
- Never skip project sync hook after issue creation.

### Self-Assessment Epilogue

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

## Failure Conditions

Fail with clear remediation when:

- Go binary is unavailable or `GITHUB_TOKEN` is not set
- Required labels do not exist and cannot be inferred
- Milestone is required but none available
- Issue creation succeeds but project sync fails

## Completion Checklist

- [ ] Issue created successfully
- [ ] Every body carries `## Verification` naming a test/command, input and
      expected observable output per acceptance criterion (no "add tests")
- [ ] A design touching auth, secrets, endpoints, retries, spawning, file
      paths, shell, or model-authored text carries its mitigation as an AC
- [ ] Parent epic chosen from the open-epic list and linked via `addSubIssue`
      (or the alternative named in `## Related work`)
- [ ] Cross-repo reality check completed (API endpoints verified or skip noted)
- [ ] Required metadata applied (type label, priority, size)
- [ ] Added to project board via `nightgauge project add`
- [ ] Status field set to "Ready" via `nightgauge project add --status Ready` (standalone) or `nightgauge project sync-status` (epic Phase 4.6)
- [ ] Parent link handled if requested (via `addSubIssue`, not body text alone)
- [ ] **Epic only**: Decomposition gate passed (Phase 2.9) — epic has sub-issues
      planned (Path A), OR placeholder chore created (Path B), OR standalone
      declaration present (Path C)
- [ ] **Epic only**: All sub-issues added to project board with Status "Ready"
- [ ] **Epic only**: Intra-epic blocking relationships set via `addBlockedBy`
- [ ] **Epic only**: Cross-epic blocking set (when epic depends on another epic)
- [ ] **Epic only**: Wave and dependency annotations in sub-issue bodies
- [ ] Knowledge directory scaffolded (when `--with-knowledge` was used)
- [ ] **Phase 4.9**: Creation manifest written to
      `issue-create-manifest-<ts>.json` in the clone's pipeline state
      directory (`nightgauge layout path pipeline`) and validates as JSON
- [ ] **Phase 6**: Terminal audit pass invoked unless `--no-audit`; verdict
      is READY (exit 0)
- [ ] Number and URL returned with next step
