---
name: config-show
description: Display the effective Nightgauge configuration with source annotations,
  showing where each value comes from (default, global, project, or environment).
  Use when debugging config precedence or confirming which tier supplies a value.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Bash
disable-model-invocation: true
---

# Config Show

> Display effective configuration with source annotations

## Description

This skill displays the effective Nightgauge configuration, showing:

1. The merged configuration from all sources
2. Source annotations indicating where each value came from
3. Configuration file locations
4. Any validation warnings

## Invocation

| Tool           | Command                                |
| -------------- | -------------------------------------- |
| Claude Code    | `/nightgauge:config-show` (via plugin) |
| OpenAI Codex   | `$nightgauge-config-show`              |
| GitHub Copilot | Invoke via Agent Skills                |
| Cursor         | Invoke via Agent Skills                |

## Arguments

```bash
# Show full effective config
/nightgauge-config-show

# Show specific section
/nightgauge-config-show --section pr

# Show only values from a specific source
/nightgauge-config-show --source global

# Show config paths only
/nightgauge-config-show --paths

# JSON output (for scripting)
/nightgauge-config-show --json
```

## Configuration Precedence

Values are resolved in this order (highest priority first):

```
┌─────────────────────────────────────────────────────────────────┐
│                    CONFIG PRECEDENCE                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Environment Variables (NIGHTGAUGE_*)     ← Highest priority    │
│         ↓ if not set                                             │
│  2. Project Config (.nightgauge/config.yaml)                       │
│         ↓ if not set                                             │
│  3. Global Config (~/.nightgauge/config.yaml)                      │
│         ↓ if not set                                             │
│  4. Built-in Defaults                      ← Lowest priority    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

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

### Phase 1: Locate Configuration Files

#### Step 1.1: Find Global Config

```bash
# Check for global config in platform-specific location
# macOS: ~/.nightgauge/config.yaml
# Linux: ~/.config/nightgauge/config.yaml or XDG_CONFIG_HOME/nightgauge/config.yaml
# Windows: %APPDATA%/nightgauge/config.yaml

# Priority: NIGHTGAUGE_CONFIG_HOME > XDG_CONFIG_HOME > platform default
if [[ -n "${NIGHTGAUGE_CONFIG_HOME:-}" ]]; then
  GLOBAL_CONFIG_DIR="$NIGHTGAUGE_CONFIG_HOME"
elif [[ -n "${XDG_CONFIG_HOME:-}" ]]; then
  GLOBAL_CONFIG_DIR="$XDG_CONFIG_HOME/nightgauge"
else
  case "$(uname -s)" in
    Darwin*) GLOBAL_CONFIG_DIR="$HOME/.nightgauge" ;;
    Linux*)  GLOBAL_CONFIG_DIR="$HOME/.config/nightgauge" ;;
    *)       GLOBAL_CONFIG_DIR="$HOME/.nightgauge" ;;
  esac
fi

GLOBAL_CONFIG="$GLOBAL_CONFIG_DIR/config.yaml"

if [[ -f "$GLOBAL_CONFIG" ]]; then
  echo "✓ Global config: $GLOBAL_CONFIG"
else
  echo "○ Global config: $GLOBAL_CONFIG (not found)"
fi
```

#### Step 1.2: Find Project Config

```bash
# Check for project config
GIT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null)
PROJECT_CONFIG="$GIT_ROOT/.nightgauge/config.yaml"
LEGACY_CONFIG="$GIT_ROOT/.nightgauge/nightgauge.yaml"

if [[ -f "$PROJECT_CONFIG" ]]; then
  echo "✓ Project config: $PROJECT_CONFIG"
elif [[ -f "$LEGACY_CONFIG" ]]; then
  echo "⚠ Project config: $LEGACY_CONFIG (legacy, please migrate)"
else
  echo "○ Project config: $PROJECT_CONFIG (not found)"
fi
```

---

### Phase 2: Load and Merge Configuration

#### Step 2.1: Parse YAML Files

Read global and project config files using the YAML parsing utilities.

#### Step 2.2: Merge with Source Tracking

Track the source of each value during the merge:

```
Merge Order:
1. Start with DEFAULT_CONFIG
2. Overlay global config values
3. Overlay project config values
4. Apply environment variable overrides

Track source for each leaf value.
```

---

### Phase 3: Display Configuration

#### Step 3.1: Output Header

```
┌─────────────────────────────────────────────────────────────────┐
│  NIGHTGAUGE CONFIGURATION                                           │
└─────────────────────────────────────────────────────────────────┘

Config Files:
  Global:  ~/.nightgauge/config.yaml (exists)
  Project: .nightgauge/config.yaml (exists)
```

#### Step 3.2: Output Effective Config

Display each section with source annotations:

```
┌─────────────────────────────────────────────────────────────────┐
│  project                                                         │
├─────────────────────────────────────────────────────────────────┤
│  number: 10                              [project]              │
│  auto_dates: true                        [default]              │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: squash                  [global]               │
│  delete_branch: true                     [default]              │
│  reviewers: []                           [default]              │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  branch                                                          │
├─────────────────────────────────────────────────────────────────┤
│  base: main                              [default]              │
│  protected: [main, master]               [default]              │
│  prefixes:                                                      │
│    feature: feat/                        [default]              │
│    bugfix: fix/                          [default]              │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  pipeline (budget)                                               │
├─────────────────────────────────────────────────────────────────┤
│  budget_preset: standard                 [project]              │
│  budget_mode: hard                       [default]              │
│  budget_grace_percent: 50                [default]              │
│  stage_budgets: (none)                   [default]              │
└─────────────────────────────────────────────────────────────────┘
```

#### Step 3.3: Source Legend

```
Source Legend:
  [default] = Built-in default value
  [global]  = From ~/.nightgauge/config.yaml
  [project] = From .nightgauge/config.yaml
  [env]     = From NIGHTGAUGE_* environment variable
```

---

### Phase 4: Validation Warnings (if any)

```
┌─────────────────────────────────────────────────────────────────┐
│  ⚠ VALIDATION WARNINGS                                          │
├─────────────────────────────────────────────────────────────────┤
│  global: pr.merge_strategy - Invalid value "invalid"            │
│  project: project.number - Must be a positive integer           │
└─────────────────────────────────────────────────────────────────┘
```

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

## Output Formats

### Default (Human-Readable)

Formatted tables with source annotations as shown above.

### JSON (--json flag)

```json
{
  "config": {
    "project": {
      "number": 10,
      "auto_dates": true
    },
    "pr": {
      "merge_strategy": "squash"
    }
  },
  "sources": {
    "project.number": "project",
    "project.auto_dates": "default",
    "pr.merge_strategy": "global"
  },
  "files": {
    "global": {
      "path": "/Users/user/.nightgauge/config.yaml",
      "exists": true
    },
    "project": {
      "path": "/path/to/repo/.nightgauge/config.yaml",
      "exists": true,
      "isLegacy": false
    }
  },
  "warnings": []
}
```

---

## Error Handling

### Global Config Parse Error

```
┌─────────────────────────────────────────────────────────────────┐
│  ❌ ERROR: Global Config Parse Error                             │
├─────────────────────────────────────────────────────────────────┤
│  File: ~/.nightgauge/config.yaml                                    │
│  Error: YAML syntax error at line 5: unexpected character       │
│                                                                  │
│  Global config will be skipped. Fix the syntax error or         │
│  remove the file to use project config only.                    │
└─────────────────────────────────────────────────────────────────┘
```

### Project Config Parse Error

```
┌─────────────────────────────────────────────────────────────────┐
│  ❌ ERROR: Project Config Parse Error                            │
├─────────────────────────────────────────────────────────────────┤
│  File: .nightgauge/config.yaml                                      │
│  Error: YAML syntax error at line 12: duplicate key             │
└─────────────────────────────────────────────────────────────────┘
```

---

## Examples

### Example 1: No Config Files

```
$ /nightgauge-config-show

┌─────────────────────────────────────────────────────────────────┐
│  NIGHTGAUGE CONFIGURATION                                           │
└─────────────────────────────────────────────────────────────────┘

Config Files:
  Global:  ~/.nightgauge/config.yaml (not found)
  Project: .nightgauge/config.yaml (not found)

Note: Using default configuration values.
Run 'nightgauge init' to create a project config file.
```

### Example 2: Global Config Only

```
$ /nightgauge-config-show --section pr

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: rebase                  [global]               │
│  delete_branch: true                     [global]               │
│  reviewers: ["alice"]                    [global]               │
│  auto_merge: false                       [default]              │
└─────────────────────────────────────────────────────────────────┘
```

### Example 3: Global + Project Config

```
$ /nightgauge-config-show --section pr

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: squash                  [project]              │
│  delete_branch: true                     [global]               │
│  reviewers: ["alice", "bob"]             [project]              │
│  auto_merge: false                       [default]              │
└─────────────────────────────────────────────────────────────────┘
```

### Example 4: Environment Override

```
$ NIGHTGAUGE_PR_MERGE_STRATEGY=merge /nightgauge-config-show --section pr

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: merge                   [env]                  │
│  delete_branch: true                     [global]               │
│  reviewers: ["alice", "bob"]             [project]              │
│  auto_merge: false                       [default]              │
└─────────────────────────────────────────────────────────────────┘
```

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) -
Issue-to-PR Pipeline.
