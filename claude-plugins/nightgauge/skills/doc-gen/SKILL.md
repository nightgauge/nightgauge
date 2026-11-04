---
name: doc-gen
description: Auto-generate and update documentation for public APIs. Detects undocumented
  functions, generates JSDoc/docstrings, identifies signature changes, and
  suggests README updates. Use after /feature-dev or standalone on any codebase.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
---

# Auto-Documentation Generation

> Detect undocumented APIs, generate documentation stubs, and keep docs in sync
> with code

## Description

This skill automatically generates and updates documentation by:

1. Detecting public APIs that lack documentation
2. Generating appropriate documentation stubs (JSDoc, docstrings, GoDoc, etc.)
3. Identifying signature changes in already-documented APIs
4. Suggesting README updates for new features

## Invocation

| Tool           | Command                            |
| -------------- | ---------------------------------- |
| Claude Code    | `/nightgauge:doc-gen` (via plugin) |
| OpenAI Codex   | `$nightgauge-doc-gen`              |
| GitHub Copilot | Invoke via Agent Skills            |
| Cursor         | Invoke via Agent Skills            |

## Arguments

```bash
# After feature-dev (uses dev context for changed files)
/nightgauge:doc-gen

# Scan specific files
/nightgauge:doc-gen --files "src/services/*.ts"

# Report only, no changes
/nightgauge:doc-gen --report-only

# Skip README suggestions
/nightgauge:doc-gen --skip-readme

# Generate for all public APIs (not just undocumented)
/nightgauge:doc-gen --all
```

## Prerequisites

- **Source code to document**: Either specify with `--files` or have changes in
  `dev-{N}.json` context
- **Language detection**: Auto-detects based on file extensions

## Philosophy

- **Non-invasive** — Never overwrites existing documentation without asking
- **Context-aware** — Uses function names, parameters, and return types to
  generate meaningful descriptions
- **Standards-compliant** — Follows project documentation style from
  `docs/CODE_STANDARDS.md`
- **Flexible** — Works standalone or as part of the pipeline

---

## Workflow

### Phase 0: Context Loading

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

#### Step 0.1: Extract Issue from Branch

**CRITICAL**: This skill is stateless. Extract issue context from branch name:

```bash
# Get current branch
BRANCH=$(git branch --show-current)

# Extract issue number from branch (e.g., feat/42-description -> 42)
ISSUE_NUMBER=$(printf '%s\n' "$BRANCH" | grep -oE '[0-9]+' | head -1)

echo "Branch: $BRANCH"
echo "Issue: #$ISSUE_NUMBER"
```

#### Step 0.2: Check for Dev Context

Look for dev context file from `/feature-dev`:

```bash
# Check for dev context
CONTEXT_FILE="$(nightgauge layout path pipeline dev-${ISSUE_NUMBER}.json)"

if [ -f "$CONTEXT_FILE" ]; then
  echo "Found dev context: $CONTEXT_FILE"
  # Extract files_changed from JSON
else
  echo "No dev context found, will scan project"
fi
```

#### Step 0.3: Parse Arguments

Check for provided arguments:

- `--files` - Specific file glob pattern
- `--report-only` - Only report, don't make changes
- `--skip-readme` - Skip README update suggestions
- `--all` - Document all public APIs, not just undocumented ones

---

### Phase 1: Project Analysis

#### Step 1.1: Detect Languages and Frameworks

Scan project for language indicators:

```bash
# Check for TypeScript/JavaScript
ls *.ts *.tsx *.js *.jsx package.json tsconfig.json 2>/dev/null

# Check for Python
ls *.py pyproject.toml setup.py requirements.txt 2>/dev/null

# Check for Go
ls *.go go.mod go.sum 2>/dev/null

# Check for Rust
ls *.rs Cargo.toml 2>/dev/null

# Check for Java
ls *.java pom.xml build.gradle 2>/dev/null

# Check for C#
ls *.cs *.csproj 2>/dev/null
```

#### Step 1.2: Load Documentation Style

If `docs/CODE_STANDARDS.md` exists, extract documentation conventions:

- Comment style preferences
- Required documentation elements (@param, @returns, @throws)
- Example formatting

#### Step 1.3: Report Detection Results

```
Project Analysis
================
Languages detected: TypeScript, JavaScript
Framework: Node.js/Express
Documentation style: JSDoc (/** */)
Code standards: docs/CODE_STANDARDS.md found

Target files:
- From dev context: 5 files changed
- Or: Scanning src/**/*.ts
```

---

### Phase 2: API Detection

#### Step 2.1: Identify Target Files

If `--files` argument provided:

```bash
FILES=$(ls $FILES_ARG 2>/dev/null)
```

If dev context exists:

```bash
# Extract files_changed from dev-{N}.json
FILES=$(jq -r '.files_changed[]' "$CONTEXT_FILE" 2>/dev/null)
```

Otherwise, scan project using parallel Glob calls (faster than `find`):

```
# Find all source files — run these Glob calls in parallel
Glob("**/*.ts")   Glob("**/*.js")   Glob("**/*.py")   Glob("**/*.go")

Exclude: node_modules/, .git/, vendor/, dist/, build/
```

From the combined results, filter out files matching test patterns
(`*.test.*`, `*.spec.*`). Remaining files are `FILES`.

#### Step 2.2: Detect Public APIs per Language

**TypeScript/JavaScript:**

```typescript
// Detect: export function, export class, export const, public methods
export function publicApi() {} // ✓ Needs docs
export class Service {} // ✓ Needs docs
export const handler = () => {}; // ✓ Needs docs
function privateHelper() {} // ✗ Skip (not exported)
```

Pattern to match:

```regex
^export\s+(function|class|const|let|var|interface|type|enum)\s+\w+
```

**Python:**

```python
# Detect: module-level functions (no leading _), classes, __all__ members
def public_function():  # ✓ Needs docs
    pass

def _private_helper():  # ✗ Skip (leading underscore)
    pass

class PublicClass:      # ✓ Needs docs
    pass
```

Pattern to match:

```regex
^(def|class)\s+[^_]\w+
```

**Go:**

```go
// Detect: Capitalized names (exported)
func PublicFunction() {}  // ✓ Needs docs
func privateFunction() {} // ✗ Skip (lowercase)

type PublicStruct struct {} // ✓ Needs docs
```

Pattern to match:

```regex
^func\s+[A-Z]\w+|^type\s+[A-Z]\w+
```

**Rust:**

```rust
// Detect: pub functions, structs, enums, traits
pub fn public_function() {} // ✓ Needs docs
fn private_function() {}    // ✗ Skip (no pub)
```

**Java:**

```java
// Detect: public methods, classes
public void publicMethod() {} // ✓ Needs docs
private void helper() {}      // ✗ Skip (private)
```

**C#:**

```csharp
// Detect: public methods, classes
public void PublicMethod() {} // ✓ Needs docs
private void Helper() {}      // ✗ Skip (private)
```

#### Step 2.3: Check for Existing Documentation

For each detected public API, check if documentation exists:

**JSDoc (TypeScript/JavaScript):**

```regex
/\*\*[\s\S]*?\*/\s*export
```

**Docstring (Python):**

```regex
def \w+\([^)]*\):\s*"""
```

**GoDoc (Go):**

```regex
//\s+\w+\s+.*\nfunc\s+[A-Z]
```

#### Step 2.4: Build API Report

```
API Detection Report
====================

File: src/services/UserService.ts
---------------------------------
✓ Documented:  createUser (line 15)
✗ Undocumented: updateUser (line 45)
✗ Undocumented: deleteUser (line 72)
⚠ Outdated:    getUser (signature changed, line 30)

File: src/utils/validation.ts
-----------------------------
✗ Undocumented: validateEmail (line 8)
✗ Undocumented: validatePhone (line 22)
✓ Documented:  validatePassword (line 35)

Summary:
- Total public APIs: 12
- Documented: 4 (33%)
- Undocumented: 6
- Potentially outdated: 2
```

---

### Phase 3: Signature Change Detection

#### Step 3.1: Parse Existing Documentation

For documented APIs, extract:

- Parameter names and types from documentation
- Return type from documentation
- Documented exceptions/errors

#### Step 3.2: Parse Current Signatures

Extract from code:

- Actual parameter names and types
- Actual return type
- Thrown exceptions

#### Step 3.3: Compare and Flag Mismatches

```
Signature Changes Detected
==========================

UserService.getUser:
  Documentation says:
    @param id: string - User ID
    @returns User

  Code signature:
    @param id: string
    @param options?: GetUserOptions  ← NEW PARAMETER
    @returns Promise<User | null>    ← CHANGED RETURN TYPE

Recommendation: Update documentation to match new signature
```

#### Step 3.4: Handle Partial Documentation

If a function has some documentation but is incomplete (e.g., missing @param
tags):

```
Incomplete Documentation
========================

validateEmail (src/utils/validation.ts:8):
  Has: Description
  Missing: @param email, @returns, @throws

Recommendation: Add missing parameter and return documentation
```

---

### Phase 4: Documentation Generation

#### Step 4.1: Confirm with User (unless --report-only)

```json
{
  "questions": [
    {
      "question": "Found 6 undocumented APIs. Generate documentation stubs?",
      "header": "Generate",
      "multiSelect": false,
      "options": [
        {
          "label": "Generate all",
          "description": "Create documentation for all 6 undocumented APIs"
        },
        {
          "label": "Review each",
          "description": "Show proposed documentation one by one for approval"
        },
        {
          "label": "Skip",
          "description": "Don't generate any documentation"
        }
      ]
    }
  ]
}
```

#### Step 4.2: Generate Documentation Stubs

Based on detected language, generate appropriate format:

**JSDoc (TypeScript/JavaScript):**

```typescript
/**
 * Updates user information in the database.
 *
 * @param id - The unique identifier of the user to update
 * @param updates - Object containing the fields to update
 * @returns The updated user object
 * @throws {NotFoundError} If user with given ID doesn't exist
 */
export async function updateUser(
  id: string,
  updates: UserUpdateInput
): Promise<User> {
```

**Docstring (Python):**

```python
def validate_email(email: str) -> bool:
    """Validate an email address format.

    Args:
        email: The email address to validate.

    Returns:
        True if the email format is valid, False otherwise.

    Raises:
        ValueError: If email is None or empty.
    """
```

**GoDoc (Go):**

```go
// CreateUser creates a new user with the given information.
// It returns the created user and an error if the operation fails.
func CreateUser(name string, email string) (*User, error) {
```

#### Step 4.3: Apply Documentation

Use Edit tool to insert documentation above each function:

```
Applying Documentation
======================
✓ src/services/UserService.ts:45 - updateUser
✓ src/services/UserService.ts:72 - deleteUser
✓ src/utils/validation.ts:8 - validateEmail
✓ src/utils/validation.ts:22 - validatePhone
⚠ src/services/UserService.ts:30 - getUser (updated existing)

5 functions documented
```

#### Step 4.4: Verify Syntax

After applying changes, verify files still parse correctly:

```bash
# TypeScript
npx tsc --noEmit

# Python
python -m py_compile file.py

# Go
go build ./...
```

---

### Phase 5: README Suggestions

#### Step 5.1: Detect New Features

Compare current exports against baseline (if `--skip-readme` not set):

- New exported functions/classes
- New API endpoints
- New CLI commands

#### Step 5.2: Analyze for README Relevance

Not all exports need README documentation. Check for:

- Public-facing APIs
- User-visible features
- Configuration changes
- Breaking changes

#### Step 5.3: Generate Suggestions

````
README Update Suggestions
=========================

New exports detected in src/services/PhotoService.ts:
- uploadPhoto
- resizeImage
- generateThumbnail

These appear to be a new Photo Upload feature. Suggested README section:

## Photo Upload

Upload and manage user photos with automatic resizing.

### Functions

- `uploadPhoto(file, options)` - Upload a photo for the current user
- `generateThumbnail(photoId, size)` - Generate a thumbnail for a photo

### Example

```typescript
const photo = await uploadPhoto(file, { maxSize: 5_000_000 });
const thumb = await generateThumbnail(photo.id, { width: 100, height: 100 });
````

---

Would you like to add this section to README.md?

````

#### Step 5.4: Confirm and Apply

```json
{
  "questions": [
    {
      "question": "Add suggested section to README.md?",
      "header": "README",
      "multiSelect": false,
      "options": [
        { "label": "Add as-is", "description": "Add the suggested section" },
        {
          "label": "Edit first",
          "description": "Let me modify the section before adding"
        },
        { "label": "Skip", "description": "Don't update README" }
      ]
    }
  ]
}
````

---

### Phase 6: Output Context

#### Step 6.1: Generate Context File

Write documentation context for pipeline continuity:

```json
{
  "schema_version": "1.0",
  "issue_number": 28,
  "documentation_generated": {
    "files_modified": ["src/services/UserService.ts", "src/utils/validation.ts"],
    "apis_documented": 5,
    "apis_skipped": 0,
    "apis_already_documented": 4
  },
  "signature_updates": {
    "detected": 2,
    "applied": 2
  },
  "readme_suggestions": {
    "offered": true,
    "accepted": true
  },
  "created_at": "2026-02-02T12:00:00Z"
}
```

Save it with `nightgauge layout write pipeline docgen-{N}.json` (content on
stdin, or `--from FILE` after writing a temp file outside the git directory).

#### Step 6.2: Final Report

```
┌─────────────────────────────────────────────────────────────────┐
│  DOCUMENTATION GENERATION COMPLETE                              │
└─────────────────────────────────────────────────────────────────┘

Branch:  feat/28-auto-documentation-generation
Issue:   #28

## Summary

| Metric              | Count |
|---------------------|-------|
| Files scanned       | 8     |
| Public APIs found   | 12    |
| Already documented  | 4     |
| Newly documented    | 6     |
| Signatures updated  | 2     |
| README updated      | Yes   |

## Files Modified

- src/services/UserService.ts (+3 doc blocks)
- src/utils/validation.ts (+2 doc blocks)
- README.md (+1 section)

## Next Steps

1. Review generated documentation for accuracy
2. Run `/nightgauge:pr-create` to create pull request
3. Or commit manually: git add . && git commit -m "docs: add JSDoc for public APIs"
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

## Error Handling

### No Source Files Found

```
Warning: No source files found to document.

Possible causes:
1. No files match the --files pattern
2. Project has no recognized source files
3. All source files are in excluded directories

Try:
- /nightgauge:doc-gen --files "src/**/*.ts"
- Check that source files exist and are not in node_modules/
```

### Language Not Supported

```
Warning: Found files with unsupported extensions: .swift, .kt

Currently supported languages:
- TypeScript/JavaScript (.ts, .tsx, .js, .jsx)
- Python (.py)
- Go (.go)
- Rust (.rs)
- Java (.java)
- C# (.cs)

Contribution welcome for additional language support!
```

### Syntax Error After Documentation

```
Warning: Syntax error after applying documentation to src/services/UserService.ts

Error: Unexpected token at line 47

Rolling back changes to this file...

The generated documentation may have formatting issues.
Please review and fix manually, or try again with --review-each flag.
```

---

## Security Considerations

Per `standards/security.md`:

- **No secrets in documentation** — Don't include example credentials
- **No internal details** — Generated docs should describe WHAT, not internal
  HOW
- **Input validation** — Validate `--files` argument to prevent path traversal

---

## Integration with Pipeline

This skill integrates with the Nightgauge pipeline:

```
/issue-pickup → /feature-planning → /feature-dev → /nightgauge:doc-gen → /pr-create
                                                          ↑
                                                     YOU ARE HERE
```

**Input from /feature-dev**: Files changed in implementation (`dev-{N}.json`)
**Output to /pr-create**: Documentation added, ready for review

Can also be used standalone on any codebase.

---

## Source

Part of the
[Nightgauge](https://github.com/nightgauge/nightgauge) -
Issue-to-PR Pipeline.
