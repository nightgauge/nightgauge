---
name: pattern-mining
description: Deterministic codebase pattern mining subagent. Discovers existing
  implementation patterns, naming conventions, and structural organization
  before planning. Invoked by feature-planning as a Task subagent — not
  user-facing.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Glob Grep
disable-model-invocation: true
---

# Codebase Pattern Mining

> Utility subagent launched by `/nightgauge-feature-planning` during Phase
> 3.0.5. Not invoked directly by users.

## Purpose

Discover existing codebase patterns before plan production so that:

1. Feature plans reference real conventions instead of inventing new ones
2. Feature-dev receives example implementations to follow
3. Documentation reading is shortlisted based on pattern evidence
4. Similar past issues are surfaced to avoid duplicate work

## Constraints

- **No model invocation** — all logic is deterministic keyword matching
- **Tools**: Glob, Grep, Read only (no Bash for security)
- **Token budget**: < 5,000 tokens total
- **Credibility threshold**: n >= 2 evidence files per pattern
- **Result limit**: First 50 matches per keyword; stop after 3 keywords with no
  results

## Input

Received from feature-planning as Task subagent arguments:

| Field                 | Type   | Description                            |
| --------------------- | ------ | -------------------------------------- |
| `issue_number`        | number | GitHub issue number                    |
| `issue_title`         | string | Issue title (primary keyword source)   |
| `requirements`        | string | Issue requirements/acceptance criteria |
| `documentation_scope` | string | minimal/targeted/standard/extended     |
| `excluded_paths`      | array  | Paths to exclude (from config)         |

## Output

Returns JSON matching `PatternMiningResultSchema`.

See [docs/PATTERN_MINING.md](https://github.com/nightgauge/nightgauge/blob/main/docs/PATTERN_MINING.md) for full schema
documentation.

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

### Phase 1: Keyword Extraction

Extract semantic keywords from the issue title and requirements. Map each
keyword to the keyword hierarchy.

**Keyword Hierarchy**:

| Domain   | Keywords                                      | File Patterns                                   |
| -------- | --------------------------------------------- | ----------------------------------------------- |
| API      | endpoint, route, handler, REST, HTTP          | `routes/`, `*Handler.ts`, `*Controller.ts`      |
| Service  | service, provider, manager, repository        | `*Service.ts`, `*Provider.ts`, `*Repository.ts` |
| Auth     | auth, login, session, token, permission       | `auth/`, `*Auth*.ts`, `*Session*.ts`            |
| Database | database, schema, migration, model, ORM       | `schema/`, `*Model.ts`, `migrations/`           |
| UI       | component, view, page, widget, render         | `components/`, `views/`, `*.tsx`, `*.vue`       |
| Testing  | test, spec, mock, fixture, assert             | `*.test.ts`, `*.spec.ts`, `__tests__/`          |
| Config   | config, setting, option, environment          | `config/`, `*.config.ts`, `*.yaml`              |
| Pipeline | pipeline, stage, context, handoff, orchestrat | `pipeline/`, `*Orchestrator.ts`, `*Stage.ts`    |
| Skill    | skill, command, plugin, slash                 | `skills/`, `SKILL.md`, `commands/`              |
| Schema   | schema, type, interface, validation, zod      | `schemas/`, `*.schema.ts`, `types/`             |

**Algorithm**:

1. Tokenize the issue title and requirements into words
2. Match each word against the hierarchy keywords (case-insensitive)
3. Collect the corresponding file patterns and content patterns
4. If fewer than 2 keywords match, use the issue title words as literal Grep
   patterns

### Phase 2: Search Execution

For each matched keyword group, execute searches:

#### File Pattern Search (Glob)

```
For each file_pattern from matched keywords:
  Glob: pattern = file_pattern
  Collect matching file paths (max 50 per pattern)
  Exclude paths matching excluded_paths
```

#### Content Search (Grep)

```
For each content_pattern from matched keywords:
  Grep: pattern = content_pattern, output_mode = "files_with_matches"
  Collect matching file paths (max 50 per pattern)
  Exclude paths matching excluded_paths
```

**Early termination**: If 3 consecutive keyword groups produce 0 results, stop
searching. The issue likely uses terminology not present in the codebase.

### Phase 3: Pattern Extraction

For each set of matching files, analyze patterns:

#### Naming Convention Extraction

1. Group matched files by directory
2. Extract common suffixes/prefixes (e.g., `*Service.ts`, `*Controller.ts`)
3. Count frequency of each naming pattern
4. Filter: require n >= 2 files with same pattern

#### Structural Pattern Extraction

1. Analyze directory structure of matched files
2. Identify organizational patterns (e.g., `src/services/`, `src/routes/`)
3. Map file placement conventions
4. Filter: require n >= 2 directories with same structure

#### Implementation Interface Extraction

1. Read matched files (first 50 lines each)
2. Search for export patterns: `export (async )?function`, `export class`,
   `export interface`
3. Extract method signatures and return types
4. Group by signature similarity
5. Filter: require n >= 2 files with similar signatures

#### Idiom Extraction

1. Search for common code patterns in matched files:
   - Builder pattern: `return this` in method chains
   - Factory pattern: `create*`, `build*` functions
   - Error wrapping: `try { ... } catch`
   - Result pattern: `Result<T>`, `Either<L, R>`
2. Count occurrences
3. Filter: require n >= 2 files with same idiom

### Phase 4: Similar Issues Detection

1. List existing plan files: `ls "$(nightgauge layout path plans)"/*.md`
2. For each plan file, extract the issue number and title from the filename
3. For each plan, check if the issue's keywords overlap with the plan's filename
   keywords
4. Rank by number of overlapping pattern categories
5. Return top 5 similar issues with relevance scores

### Phase 5: Output Formatting

Assemble the JSON output matching `PatternMiningResultSchema` (fields in
[docs/PATTERN_MINING.md](https://github.com/nightgauge/nightgauge/blob/main/docs/PATTERN_MINING.md)).

#### pattern_classifications Field

The `pattern_classifications` object MUST contain **exactly** these four fields
with **exactly** these names (matching `PatternClassificationsSchema`):

| Field                 | Type         | Description                                                                                   |
| --------------------- | ------------ | --------------------------------------------------------------------------------------------- |
| `naming_conventions`  | integer >= 0 | Count of naming convention patterns discovered (e.g., `*Service.ts`, `camelCase` vars)        |
| `structural_patterns` | integer >= 0 | Count of directory/file structure patterns (e.g., `src/routes/`, `src/services/`)             |
| `interface_patterns`  | integer >= 0 | Count of implementation interface/signature patterns (e.g., method signatures, class exports) |
| `idioms`              | integer >= 0 | Count of code idiom patterns (builder, factory, error wrapping, result pattern)               |

> **Critical**: Do NOT use alternate field names such as `naming_conventions_count`,
> `pattern_naming`, `structure_patterns`, or `interface_count`. The Zod schema
> enforces exact field names — any deviation will fail validation in feature-planning.

```json
{
  "patterns_found": [...],
  "similar_issues": [...],
  "pattern_classifications": {
    "naming_conventions": <integer>,
    "structural_patterns": <integer>,
    "interface_patterns": <integer>,
    "idioms": <integer>
  },
  "search_queries_used": [...],
  "coverage_ratio": <matched_files / total_searched>,
  "token_cost_estimate": <estimated_tokens>,
  "recommendations": [...]
}
```

Generate 1-5 recommendations based on the strongest patterns found:

- If a naming pattern has frequency >= 5: "Follow [pattern] convention"
- If a structural pattern exists: "Place new files in [directory] following
  [pattern]"
- If an implementation interface exists: "Implement using [signature pattern]
  from [example file]"
- If a similar issue exists with score >= 0.7: "Review plan from issue #N for
  related approach"

---

## Error Handling

| Condition                  | Action                                  |
| -------------------------- | --------------------------------------- |
| No keywords match          | Return empty patterns_found, 0 coverage |
| Glob/Grep timeout          | Skip remaining queries, return partial  |
| All searches return 0 hits | Return empty results with note          |
| File read fails            | Skip file, continue with remaining      |
| Too many results (> 200)   | Truncate to top 50 per pattern type     |

---

## References

- **Documentation**: `docs/PATTERN_MINING.md`
- **Config**: `.nightgauge/pattern-mining-config.yaml`
- **Consumer**: `skills/nightgauge-feature-planning/SKILL.md` (Phase 3.0.5)

---

**Author**: nightgauge **License**: Apache-2.0
