---
name: repo-init
description: Prime a new GitHub repository and project board for the Nightgauge SDLC
  pipeline. Creates standard labels, validates/creates project board fields,
  creates standard project board views (Backlog, Priority board, Team items,
  Roadmap, My items), links the repo to the project, and generates
  .nightgauge/config.yaml with all field IDs. Discovers and reuses existing
  projects (org-preferred) before creating new ones. Validates token scopes
  upfront. Idempotent — safe to re-run at any time. Use once when onboarding a
  fresh repository for the pipeline, or to fill gaps and refresh field IDs.
  User-invoked; the user types `/nightgauge:repo-init`.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.3.3"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Bash AskUserQuestion
disable-model-invocation: true
---

# Nightgauge Repo Init

> Prime a GitHub repository for the Nightgauge SDLC pipeline

## Description

This skill sets up everything a repository needs to work with the
Nightgauge pipeline: standard labels, GitHub Project board fields,
standard project board views, repo-to-project linking, and
`.nightgauge/config.yaml` with all field IDs pre-populated for pipeline
board status sync.

**Run this once when onboarding a new repository. It is idempotent — safe to
re-run to fill gaps or refresh field IDs.**

## Invocation

| Tool           | Command                              |
| -------------- | ------------------------------------ |
| Claude Code    | `/nightgauge:repo-init` (via plugin) |
| OpenAI Codex   | `$nightgauge-repo-init`              |
| GitHub Copilot | Invoke via Agent Skills              |
| Cursor         | Invoke via Agent Skills              |

## Arguments

| Argument             | Description                                                                            | Default                 |
| -------------------- | -------------------------------------------------------------------------------------- | ----------------------- |
| `--dry-run`          | Preview changes without applying                                                       | `false`                 |
| `--project N`        | GitHub Project number to link — resolved against org and user ownership, org preferred | (from config or prompt) |
| `--skip-docs`        | Skip smart-setup AI documentation phase                                                | `false`                 |
| `--seed-from <path>` | Seed complexity model from another repo's model file                                   | (none)                  |
| `--skip-knowledge`   | Skip knowledge directory scaffolding                                                   | `false`                 |

## Philosophy

All operations are **deterministic** (no LLM inference, no interpretation):

| Operation               | Type          | Rationale                                        |
| ----------------------- | ------------- | ------------------------------------------------ |
| Label existence check   | Deterministic | `nightgauge label list --json` comparison        |
| Label creation          | Deterministic | `nightgauge label create` (idempotent Go binary) |
| Project field discovery | Deterministic | GraphQL query, fixed field names                 |
| Project field creation  | Deterministic | GraphQL mutation with known schemas              |
| Project view creation   | Deterministic | `nightgauge project view-create` (idempotent)    |
| Config file generation  | Deterministic | Templated YAML with discovered IDs               |
| User prompts            | Probabilistic | AI formats questions and summaries               |

## Forge Abstraction

All forge operations route through `nightgauge forge` (and `nightgauge` Go-binary verbs) so the skill works against GitHub today and GitLab through the same abstraction tomorrow. The table below summarizes the call surface.

| Phase | Operation              | Command                                                                        |
| ----- | ---------------------- | ------------------------------------------------------------------------------ |
| 0     | Token scope validation | `nightgauge forge auth status` / `nightgauge auth check`                       |
| 1     | Owner type detection   | `nightgauge forge graphql` (`repositoryOwner(login:) { __typename }`)          |
| 1     | Project discovery      | `nightgauge forge graphql -f query='query{user{projectsV2...}}'`               |
| 3     | Label existence check  | `nightgauge label list --json`                                                 |
| 3     | Label creation         | `nightgauge label create`                                                      |
| 4     | Field schema query     | `nightgauge forge graphql -f query='...'`                                      |
| 5     | Repository link        | `nightgauge forge graphql -f query='mutation{linkProjectV2ToRepository(...)}'` |
| 5.2   | View creation          | `nightgauge project view-create`                                               |

## Supporting files (load on demand)

This skill follows ADR-010 progressive disclosure: the skeleton below carries
the overview and per-phase Read directives; the heavy procedural detail (bash,
GraphQL, config templates, tables) lives in `_includes/` and is loaded only when
its phase fires.

- `_includes/prerequisites.md` (same directory as this SKILL.md) — read in Phase 0 (tool checks, account pin, token scopes, argument parsing, repo identity, existing config)
- `_includes/project-selection.md` (same directory as this SKILL.md) — read in Phase 1 (link check, project discovery, `--project` resolution, prompt, confirm)
- `_includes/labels.md` (same directory as this SKILL.md) — read in Phases 2 and 3 (component label selection, label setup)
- `_includes/board-fields-and-link.md` (same directory as this SKILL.md) — read in Phases 4, 5, and 5.2 (field validation, repo link, standard views)
- `_includes/workspace-registration.md` (same directory as this SKILL.md) — read in Phase 5.5 (multi-repo workspace registration)
- `_includes/config-generation.md` (same directory as this SKILL.md) — read in Phases 6 and 6.5 (generate config.yaml, verify field IDs)
- `_includes/knowledge-and-complexity.md` (same directory as this SKILL.md) — read in Phases 6.7 and 6.8 (scaffold knowledge, bootstrap complexity model)
- `_includes/summary-and-migration.md` (same directory as this SKILL.md) — read in Phases 7 and 7.5 (summary report, native sub-issue migration check)

---

## Workflow

### Phase 0: Prerequisites

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

Verify tools, pin the active forge account, validate token scopes, parse
arguments, resolve repo identity, and detect any existing config.

> **Read `_includes/prerequisites.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 1: Project Selection

Discover, resolve, and confirm the GitHub Project to use — org-preferred,
reusing an existing link when present. Five ordered steps gate each other.

> **Read `_includes/project-selection.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 2: Component Label Selection

Ask which component label set to create (the only project-specific label group).

> **Read `_includes/labels.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 3: Label Setup

Create all standard labels plus the selected component labels, skipping any that
already exist (idempotent).

> **Read `_includes/labels.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 4: Project Board Field Validation

Create or ensure all required project board fields exist via the Go binary's
idempotent `project ensure-fields` verb, returning their field IDs.

> **Read `_includes/board-fields-and-link.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 5: Link Repository to Project

Link the repo to the resolved project (skipping if already linked from the
Phase 1 link check).

> **Read `_includes/board-fields-and-link.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 5.2: Standard Project Board Views

Create the standard set of board/table/roadmap views (Backlog, Priority board,
Team items, Roadmap, My items), skipping any that already exist.

> **Read `_includes/board-fields-and-link.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 5.5: Multi-Repo Workspace Registration

If the repo belongs to an existing multi-repo workspace, register it in the
workspace config (handling N:1 shared-project topology). Standalone repos skip.

> **Read `_includes/workspace-registration.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 5.7: Repository Merge Hygiene

Enable `delete_branch_on_merge` so GitHub removes a PR's head branch on merge —
this pairs with the pipeline's post-merge worktree + local-branch teardown
(#3969) so neither remote nor local merged branches accumulate. Deterministic,
idempotent, safe to re-run:

```bash
nightgauge repo enable-delete-branch --owner "$OWNER" --repo "$REPO" --json
```

(GitHub forge only. Skip on GitLab — the forge adapter doesn't expose this REST
setting; the pipeline's local teardown still applies.)

---

### Phase 6: Generate .nightgauge/config.yaml

Create the pipeline directory structure and `.gitignore`, then generate
`config.yaml` (with all field IDs) via the deterministic `config init` verb.

> **Read `_includes/config-generation.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 6.5: Verify field IDs in config.yaml

Verify `project.id` and `project.fields` are present in `config.yaml`. Board
status sync resolves fields from the board and does not depend on them (#1501).

> **Read `_includes/config-generation.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 6.7: Scaffold Knowledge Directory

Create the knowledge base directory structure (idempotent). Skip if
`--skip-knowledge` was passed.

> **Read `_includes/knowledge-and-complexity.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 6.8: Bootstrap Complexity Model

Create this checkout's `complexity-model.yaml` (`nightgauge layout path checkout
complexity-model.yaml`) with baseline calibration (or
seed it from another repo via `--seed-from`) if it does not already exist.

> **Read `_includes/knowledge-and-complexity.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 7: Summary Report

Output a clear summary of everything created/verified (labels, board, views,
config, workspace, knowledge), then the recommended next steps.

> **Read `_includes/summary-and-migration.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 7.5: Native Sub-Issue Migration Check

Flag any existing issues using the legacy "Part of #X" body pattern that lack
native GitHub sub-issue links (the pipeline requires native sub-issues).

> **Read `_includes/summary-and-migration.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

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

## Error Handling

| Error                                          | Cause                                           | Resolution                                                      |
| ---------------------------------------------- | ----------------------------------------------- | --------------------------------------------------------------- |
| `nightgauge: command not found`                | Binary not installed                            | Build with `go build ./cmd/nightgauge` or install via releases  |
| `forge auth status` fails                      | Forge auth not configured                       | Run `nightgauge forge auth login` (or set GITHUB_TOKEN env var) |
| Missing `project` scope                        | Token lacks project scope                       | Run `nightgauge forge auth login --scopes project` then retry   |
| `Could not detect active account`              | Forge auth not configured                       | Run `nightgauge forge auth login`                               |
| `403 Resource not found`                       | Project number wrong, wrong owner, or no access | Verify project number and that active account has org access    |
| Project #N found under both org and user       | Ambiguous --project arg                         | Skill defaults to org project; confirm at Step 1.5              |
| `Field already exists`                         | Duplicate field name                            | Skill detects and skips — not an error                          |
| Missing `jq`                                   | jq not installed                                | `brew install jq` (macOS) / `apt install jq`                    |
| Not in a git repo                              | Not run from a repo directory                   | `cd` to your repository root first                              |
| `nightgauge: command not found` (Phase 0)      | Go binary not installed or not in PATH          | Build with `go build ./cmd/nightgauge` or install via releases  |
| `label list: failed to fetch labels` (Phase 3) | Token missing `repo` scope or network error     | Run `nightgauge forge auth login --scopes repo` then retry      |
| `view-create: project not found` (Phase 5.2)   | Project number or owner mismatch                | Verify `--project` and `--owner` match resolved values          |

---

## Idempotency

This skill is **fully idempotent**:

- **Labels**: Fetches `nightgauge label list --json` once and caches the
  result. Skips creation if name already exists in cache.
- **Project fields**: Checks field names before creating. Skips if present.
- **Repo link**: Checks `repositories` in project before linking. Skips if
  already linked.
- **Config file**: Asks before overwriting. Defaults to "update field IDs only".
- **Field mappings**: Updates `project.fields` in `config.yaml`. Deletes legacy
  `project-field-mappings.json` if found.

Safe to re-run after:

- Adding new repos to the org
- Recreating a project board
- Refreshing config after field IDs change

---

## Integration

### Relationship to Other Skills

| Skill                       | Relationship                                       |
| --------------------------- | -------------------------------------------------- |
| `/nightgauge:smart-setup`   | Run after repo-init to generate AI documentation   |
| `/nightgauge:project-sync`  | Run after repo-init to bulk-sync existing issues   |
| `/nightgauge:issue-pickup`  | Start here once repo-init and smart-setup are done |
| `/nightgauge:backlog-groom` | Useful after initial issue import                  |

### Recommended Onboarding Order

```
1. /nightgauge:repo-init    ← This skill (GitHub infra)
2. /nightgauge:smart-setup                  ← AI documentation
3. /nightgauge:project-sync ← Sync existing issues (if any)
4. /nightgauge:issue-pickup ← Start the pipeline
```

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) —
AI-Augmented SDLC Framework.
