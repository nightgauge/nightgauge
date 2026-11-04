---
name: workspace-init
description: Scaffold a multi-repo workspace manifest
  (.vscode/nightgauge-workspace.yaml) for the N:1 shared-project topology,
  where several member repositories share a single GitHub Project. Detects member
  repos under the parent folder, derives the shared project, generates the
  manifest with repositories/routing/epic blocks, and verifies the result via
  `workspace sync-payload`. Idempotent — re-running refreshes the manifest
  without duplicating entries. Use once when onboarding a parent folder that
  groups multiple pipeline repos, or to repair an empty Repositories view.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Bash AskUserQuestion
disable-model-invocation: true
---

# Nightgauge Workspace Init

> Scaffold a multi-repo workspace manifest for the Nightgauge SDLC pipeline

## Description

`repo-init` primes a single repository. **`workspace-init` primes the layer
above it**: the parent folder that groups several member repos which share one
GitHub Project (the N:1 topology). Without this manifest, opening the parent
folder renders an empty Repositories/board panel in the VSCode extension.

This skill:

1. Detects member repositories under the parent folder (each directory with a
   `.nightgauge/config.yaml`).
2. Derives the shared GitHub Project (from each member's `project.number`, or via
   `nightgauge workspace repos-from-project`).
3. Generates `.vscode/nightgauge-workspace.yaml` with `workspace`,
   `repositories`, `routing`, and `epic` blocks.
4. Verifies success by running `workspace sync-payload` and asserting a non-empty
   `repos` array.

**Run this once when onboarding a multi-repo parent folder. It is idempotent —
safe to re-run to add newly-onboarded members or refresh routing.**

## Invocation

| Tool           | Command                                   |
| -------------- | ----------------------------------------- |
| Claude Code    | `/nightgauge:workspace-init` (via plugin) |
| OpenAI Codex   | `$nightgauge-workspace-init`              |
| GitHub Copilot | Invoke via Agent Skills                   |
| Cursor         | Invoke via Agent Skills                   |

## Arguments

| Argument        | Description                                | Default                  |
| --------------- | ------------------------------------------ | ------------------------ |
| `--dry-run`     | Preview the manifest without writing it    | `false`                  |
| `--name <name>` | Workspace display name                     | (derived or prompted)    |
| `--project <N>` | Shared GitHub Project number               | (from members or prompt) |
| `--root <path>` | Parent folder to scan (the workspace root) | current directory        |

## Prerequisites

- **Parent folder** containing 2+ member repos, each already onboarded with
  `repo-init` (so each has a `.nightgauge/config.yaml`).
- **Go binary** — `nightgauge` on PATH (verified in Phase 0).

> **CRITICAL**: When invoked headless (no TTY), do NOT use AskUserQuestion.
> Derive values from member configs or fail with a clear error.

## Philosophy

All manifest assembly is **deterministic** — member detection reads on-disk
configs, project derivation reuses the `repos-from-project` Go verb, and
verification reuses `workspace sync-payload`. The skill prefers Go-binary verbs
over reimplementing logic, consistent with `repo-init`.

| Operation                | Type          | Source                                          |
| ------------------------ | ------------- | ----------------------------------------------- |
| Member detection         | Deterministic | Filesystem scan for `.nightgauge/config.yaml`   |
| Project derivation       | Deterministic | `workspace repos-from-project` / member configs |
| Manifest generation      | Deterministic | Templated YAML from detected members            |
| Verification             | Deterministic | `workspace sync-payload` (non-empty `repos`)    |
| Workspace name / prompts | Probabilistic | AI formats questions and summaries              |

## Supporting files (load on demand)

This skill follows ADR-010 progressive disclosure: the skeleton below carries
the overview and per-phase Read directives; the procedural detail (bash, YAML
templates, verification logic) lives in `_includes/` and is loaded only when its
phase fires.

- `_includes/member-detection.md` (same directory as this SKILL.md) — read in Phases 1 and 2 (scan members, derive shared project)
- `_includes/manifest-generation.md` (same directory as this SKILL.md) — read in Phases 3 and 4 (existing-manifest handling, generate/merge the YAML)
- `_includes/verification.md` (same directory as this SKILL.md) — read in Phases 5 and 6 (sync-payload verification, summary report)

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

Verify the `nightgauge` binary is available, resolve the workspace root
(`--root` or CWD), and parse arguments.

> **Read `_includes/member-detection.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 1: Detect Member Repositories

Scan the workspace root for member repos — each immediate subdirectory that
contains `.nightgauge/config.yaml`. Read each member's `owner`, `repo`, and
`project.number`. Fail with a clear error if fewer than 2 members are found
(a single repo does not need a workspace manifest — use `repo-init` instead).

> **Read `_includes/member-detection.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 2: Derive the Shared Project

Determine the shared GitHub Project number. Prefer an explicit `--project`; else
read it from the members' configs (they should agree); else query
`nightgauge workspace repos-from-project`. Pick a `default_repository`
(the member whose role is primary, or the first detected).

> **Read `_includes/member-detection.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 3: Existing Manifest Handling

If `.vscode/nightgauge-workspace.yaml` already exists, read it and MERGE
(idempotent) rather than overwrite — preserve existing routing patterns and the
workspace description, add only newly-detected members. In `--dry-run`, print
the would-be manifest and stop.

> **Read `_includes/manifest-generation.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 4: Generate the Manifest

Write `.vscode/nightgauge-workspace.yaml` with `workspace` (name +
description), `repositories[]` (name/path/role/project_number per member),
`routing` (default_repository + any patterns), and an `epic` block. Match the
verified reference shape documented in the include.

> **Read `_includes/manifest-generation.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 5: Verify

Run `nightgauge workspace sync-payload` from the workspace root and assert
the `repos` array is non-empty and contains every detected member. A non-empty
payload proves the extension will render the shared board. Run `workspace
doctor` and surface any fatal validation errors.

> **Read `_includes/verification.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 6: Summary Report

Output a clear summary: workspace name, members registered, shared project,
manifest path, and `sync-payload` verification result. Then the recommended next
step (open the parent folder in VSCode).

> **Read `_includes/verification.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

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

| Error                                   | Cause                                 | Resolution                                                                |
| --------------------------------------- | ------------------------------------- | ------------------------------------------------------------------------- |
| `nightgauge: command not found`         | Binary not installed                  | Build with `go build ./cmd/nightgauge` or install via releases            |
| Fewer than 2 members detected           | Parent folder has 0–1 onboarded repos | Run `repo-init` in each member first; a single repo needs no manifest     |
| Members disagree on project number      | Configs point at different projects   | Pass `--project <N>` explicitly, or align the members' `project.number`   |
| `sync-payload` returns empty `repos`    | Member config missing owner/repo      | Re-run `repo-init` in the offending member, or check its `config.yaml`    |
| `workspace doctor` reports fatal errors | Forge misconfiguration                | Fix the reported `forges:`/`autonomous.repositories` entries, then re-run |

---

## Idempotency

This skill is **fully idempotent**:

- **Manifest**: re-running MERGES — existing routing patterns and description are
  preserved; only newly-detected members are appended. No duplicate
  `repositories[]` entries.
- **Verification**: `sync-payload` is read-only.

Safe to re-run after onboarding a new member repo into the parent folder.

---

## Integration

### Relationship to Other Skills

| Skill                      | Relationship                                                 |
| -------------------------- | ------------------------------------------------------------ |
| `/nightgauge:repo-init`    | Run per-member FIRST — each member needs its own config.yaml |
| `/nightgauge:project-sync` | Bulk-sync existing issues after the workspace is registered  |
| `/nightgauge:issue-pickup` | Start the pipeline once the workspace renders                |

### Recommended Onboarding Order

```
1. /nightgauge:repo-init       ← per member repo (run N times)
2. /nightgauge:workspace-init  ← This skill (parent folder manifest)
3. Open the parent folder in VSCode ← shared board renders
```

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) —
AI-Augmented SDLC Framework.
