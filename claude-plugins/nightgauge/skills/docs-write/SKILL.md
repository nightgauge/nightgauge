---
name: docs-write
description: >
  Write narrative architecture documentation sections by reading source files
  and synthesizing accurate, validated content. Use when picking up a
  documentation issue that requires reading code and writing a structured doc
  section — without a PLAN.md approval cycle.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
---

# Write Docs

> Write narrative documentation sections from source files with accuracy
> validation

## Description

This skill synthesizes accurate, validated documentation sections by:

1. Parsing the target file, section name, and source file glob from arguments
2. Reading all relevant source files
3. Synthesizing narrative content for the requested section
4. Writing or replacing the named section in the target doc file
5. Self-checking that every class, function, or type name referenced in the
   written section actually exists in the source files
6. Verifying all relative links in the written section resolve correctly

Use this for documentation issues that follow the pattern:

> Read N source files → synthesize a structured narrative section → write it to
> a target doc file → validate accuracy

This skill does NOT require a PLAN.md approval cycle. It is explicitly
self-contained and does not write pipeline context files.

## Invocation

| Tool           | Command                               |
| -------------- | ------------------------------------- |
| Claude Code    | `/nightgauge:docs-write` (via plugin) |
| OpenAI Codex   | `$nightgauge-docs-write`              |
| GitHub Copilot | Invoke via Agent Skills               |
| Cursor         | Invoke via Agent Skills               |

## Arguments

```bash
# Write a section to a target doc file (source inferred from issue)
/nightgauge:docs-write --target docs/ARCHITECTURE.md --section "Pipeline Lifecycle"

# Specify source files explicitly
/nightgauge:docs-write --target docs/ARCHITECTURE.md --section "Pipeline Lifecycle" \
  --source "src/pipeline/**/*.ts"

# Preview generated content without writing
/nightgauge:docs-write --target docs/ARCHITECTURE.md --section "Pipeline Lifecycle" --dry-run

# Generate architecture knowledge entries (ADRs + overview) from source code analysis
/nightgauge:docs-write --knowledge --source "packages/**/*.ts"

# Generate knowledge entries AND write a doc section in the same run
/nightgauge:docs-write --target docs/ARCHITECTURE.md \
  --section "Event System" --source "packages/**/*.ts" --knowledge
```

## Arguments Table

| Argument           | Required | Description                                                                                                                                                                                            |
| ------------------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `--target <file>`  | No\*     | Target documentation file to write to (e.g., `docs/ARCHITECTURE.md`). Required unless `--knowledge` is provided.                                                                                       |
| `--section <name>` | No\*     | Section heading to write or update (exact heading text, without `#` prefix). Required when `--target` is provided.                                                                                     |
| `--source <glob>`  | No       | Glob pattern for source files to read (default: inferred from issue AC or branch)                                                                                                                      |
| `--dry-run`        | No       | Preview the generated content without writing to the target file                                                                                                                                       |
| `--knowledge`      | No       | Generate architecture knowledge entries (ADRs + architecture notes) from source code analysis. Output goes to `.nightgauge/knowledge/architecture/`. Can be used alone or with `--target`/`--section`. |

---

## Workflow

<!-- phase-registry: standalone-skill -->

This skill is standalone (not a pipeline execution stage), so its
`stage="docs-write"` emits intentionally do not appear in `PHASE_REGISTRY`. The
annotation above opts the skill out of
`scripts/validate-phase-markers.ts`.

### Phase 0: Parse Arguments and Context

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
printf '<!-- phase:start name="parse-arguments" index=0 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Extract task parameters from arguments and optional pipeline
context.

#### Step 0.1: Parse Provided Arguments

Extract `--target`, `--section`, `--source`, `--dry-run`, and `--knowledge` from
the skill arguments (the text after the skill name).

```bash
KNOWLEDGE_MODE=false
if printf '%s\n' "$ARGUMENTS" | grep -q -- '--knowledge'; then
  KNOWLEDGE_MODE=true
fi
```

**Validation rules:**

- When `--knowledge` is false and `--target` is missing → exit 1 with usage
  error (existing behavior)
- When `--knowledge` is true and `--target` is not provided → proceed in
  knowledge-only mode (no `--section` required)
- When `--knowledge` is true and `--target` is provided → both target doc
  writing AND knowledge entry generation run (requires `--section` as before)
- When neither `--knowledge` nor `--target` is provided → exit 1 with usage
  error

If the validation fails, exit 1 with a clear usage error:

```
ERROR: --target and --section are required (or use --knowledge for knowledge-only mode).

Usage:
  /nightgauge:docs-write --target docs/ARCHITECTURE.md --section "Pipeline Lifecycle"
  /nightgauge:docs-write --knowledge --source "packages/**/*.ts"
  /nightgauge:docs-write --target docs/ARCHITECTURE.md --section "Event System" \
    --source "packages/**/*.ts" --knowledge

Arguments:
  --target <file>   Target documentation file to write to
  --section <name>  Section heading to write or update (without # prefix)
  --source <glob>   Source files to read (optional, inferred if omitted)
  --dry-run         Preview content without writing
  --knowledge       Generate ADRs and architecture notes to .nightgauge/knowledge/architecture/
```

#### Step 0.2: Load Optional Pipeline Context

Check for issue context from the current branch:

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(printf '%s\n' "$BRANCH" | grep -oE '[0-9]+' | head -1)
CONTEXT_FILE="$(nightgauge layout path pipeline issue-${ISSUE_NUMBER}.json)"

if [ -f "$CONTEXT_FILE" ]; then
  # Load acceptance criteria to help infer source files and content requirements
  AC=$(jq -r '.acceptance_criteria // ""' "$CONTEXT_FILE" 2>/dev/null)
fi
```

Use the issue's acceptance criteria to guide which content to cover if no
`--source` glob is provided.

---

### Phase 1: Identify Source Files

```bash
printf '<!-- phase:start name="identify-source-files" index=1 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Build the list of source files to read before synthesizing content.

#### Step 1.1: Use Provided --source Glob

If `--source` was provided, expand the glob:

```bash
ls $SOURCE_GLOB 2>/dev/null
```

#### Step 1.2: Infer Source Files from Context

If `--source` was not provided, infer source files from:

1. The target section name — search for files related to the topic
2. The issue acceptance criteria — extract mentioned files, classes, or modules
3. The target doc file — read existing section context for clues

```bash
# Example: section "Pipeline Lifecycle" → look for pipeline-related source files
SECTION_KEYWORDS=$(echo "$SECTION_NAME" | tr ' ' '\n' | tr '[:upper:]' '[:lower:]')
```

Search relevant directories:

```bash
# Search source for files related to section keywords
grep -rl "$SECTION_KEYWORD" packages/ src/ --include="*.ts" --include="*.js" \
  --include="*.py" 2>/dev/null | head -20
```

#### Step 1.3: Report Source Files Found

List the files that will be read:

```
Source files to read:
  src/pipeline/PipelineOrchestrator.ts
  src/pipeline/ContextManager.ts
  src/pipeline/stages/FeatureDevStage.ts
  ...
```

If no source files are found, warn and prompt for explicit `--source` argument.

---

### Phase 1.5: Architecture Pattern Detection

```bash
printf '<!-- phase:start name="architecture-pattern-detection" index=2 total=10 stage="docs-write" -->\n'
```

> **This phase only runs when `KNOWLEDGE_MODE=true`.** When `--knowledge` is not
> set, skip directly to Phase 2.

**PURPOSE**: Classify source files by architectural pattern type before reading
them. The detected pattern categories guide which ADRs to generate in Phase 5.5.

#### Step 1.5.1: Search Source Files for Known Patterns

Use the `nightgauge docs detect-patterns` Go binary to search the source
files identified in Phase 1 for the 7 architectural pattern slugs. This
replaces the former inline bash grep loop (audit row B35).

```bash
# FILES_GLOB should be set to a glob covering the source files from Phase 1.
# Adjust the glob to match the files identified in Phase 1 (e.g. "packages/**/*.ts").
PATTERN_JSON=$(nightgauge docs detect-patterns --files "$FILES_GLOB" --json)

PATTERNS_FOUND=()
# Extract matched slugs and their files from the JSON result.
while IFS= read -r slug; do
  PATTERNS_FOUND+=("$slug")
done < <(printf '%s\n' "$PATTERN_JSON" | jq -r '.patterns[].slug')

declare -A PATTERN_FILES
while IFS=$'\t' read -r slug files; do
  PATTERN_FILES[$slug]="$files"
done < <(printf '%s\n' "$PATTERN_JSON" | jq -r '.patterns[] | [.slug, (.files | join(","))] | @tsv')

# Log any warnings from the binary (unreadable files, etc.)
printf '%s\n' "$PATTERN_JSON" | jq -r '.warnings[]?' | while IFS= read -r w; do
  echo "  ! $w"
done
```

#### Step 1.5.2: Report Detected Patterns

List the detected patterns and representative files:

```
Architecture patterns detected:
  ✓ event-system    — src/pipeline/PipelineOrchestrator.ts, ...
  ✓ service-pattern — src/services/KnowledgeService.ts, ...
  ✓ config-system   — src/config/schema.ts, ...
  ✗ auth-security   — not detected
```

If no patterns are detected at all, warn and continue. An `arch-notes.md`
overview will still be generated with an empty patterns table.

---

### Phase 2: Read Source Files

```bash
printf '<!-- phase:start name="read-source-files" index=3 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Load all source file content for synthesis.

For each source file identified in Phase 1:

1. Read the full file content
2. Note key exports, classes, methods, types, and constants
3. Build a mental model of the component's responsibilities and interactions

If a listed file does not exist, log a warning and continue. Report missing
files in the final summary.

---

### Phase 3: Read Target Documentation File

```bash
printf '<!-- phase:start name="read-target-file" index=4 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Understand the target file's structure and the section to write.

#### Step 3.1: Read Target File

Read the full content of `--target` if it exists:

```bash
cat "$TARGET_FILE"
```

If the file does not exist, note that it will be created.

#### Step 3.2: Detect Existing Section

Search for the `--section` heading within the target file:

- Headings at any level (`##`, `###`, `####`, etc.)
- Match the exact section name (case-insensitive)

If the section already exists:

- Note its current content
- Plan to **replace** the section while preserving surrounding content

If the section does not exist:

- Plan to **insert** the section at a logical location in the file
- Place it at the end of the file if no logical location can be determined

#### Step 3.3: Understand Document Context

Read the surrounding sections to understand:

- The document's overall narrative flow
- The heading level appropriate for the new section
- Any cross-references the new section should maintain

---

### Phase 4: Synthesize Content

```bash
printf '<!-- phase:start name="synthesize-content" index=5 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Generate the documentation section content from source files.

#### Step 4.1: Determine Content Requirements

Based on:

- The section name and the document's existing content
- The acceptance criteria from the issue (if available)
- The source files read in Phase 2

Determine what content the section should include:

- What problem or concept does this section explain?
- What key classes, methods, or patterns should be highlighted?
- Should the section include code examples, diagrams, or tables?

#### Step 4.2: Write the Section Content

Generate narrative content that:

- **Accurately reflects the source code** — only describe what the source
  actually does
- **Uses correct names** — class names, method names, and type names must match
  the source exactly
- **Is appropriately detailed** — neither too shallow nor too exhaustive
- **Follows the document's style** — match the tone, heading depth, and
  formatting of surrounding sections
- **Uses Markdown** — tables, code blocks, and bullet lists where they add
  clarity

**Content quality guidelines:**

- Prefer concrete examples over abstract descriptions
- Describe the "why" as well as the "what"
- Link to related sections or files using relative markdown links
- Do not speculate about behavior not visible in the source

#### Step 4.3: Structure the Section

Format the section with the appropriate heading level:

````markdown
## Section Name

Introductory paragraph explaining what this section covers...

### Sub-heading (if needed)

Content...

```language
Code example if helpful
```
````

````

---

### Phase 5: Write to Target File

```bash
printf '<!-- phase:start name="write-to-target" index=6 total=10 stage="docs-write" -->\n'
````

**PURPOSE**: Write or replace the section in the target documentation file.

#### Step 5.1: Dry-Run Mode

If `--dry-run` is set, print the generated content to stdout and stop:

```

[DRY RUN] Would write the following to docs/ARCHITECTURE.md:

## Pipeline Lifecycle

... generated content ...

[DRY RUN] No files were modified.

```

#### Step 5.2: Write or Update Target File

If the section already exists in the target file:

- Replace the existing section content (from the heading line to the next
  heading of equal or higher level)
- Preserve all content outside the replaced section

If the section does not exist:

- Append the section to the end of the target file
- Or insert at a logical location if the document structure clearly calls for it

Use the Edit or Write tool to apply the change.

#### Step 5.3: Confirm Write

Report what was written:

```

✓ Wrote section "Pipeline Lifecycle" to docs/ARCHITECTURE.md Action: inserted
(section was not previously present) Lines added: ~45

```

---

### Phase 5.5: Knowledge Entry Generation

```bash
printf '<!-- phase:start name="knowledge-entry-generation" index=7 total=10 stage="docs-write" -->\n'
```

> **This phase only runs when `KNOWLEDGE_MODE=true`.** When `--knowledge` is not
> set, skip directly to Phase 6.

**PURPOSE**: Generate and write ADR files and an architecture overview note for
each architectural pattern detected in Phase 1.5.

#### Step 5.5.1: Ensure Architecture Directory

```bash
ARCH_DIR=".nightgauge/knowledge/architecture"
mkdir -p "$ARCH_DIR"
```

#### Step 5.5.2: Generate ADR for Each Detected Pattern

For each slug in `PATTERNS_FOUND`, generate an ADR file:

**File naming**: `adr-{pattern-slug}.md`
**Examples**: `adr-event-system.md`, `adr-config-system.md`

Use the following template, populating all fields from source file analysis.
Sections where historical context is required get `[TEAM TO DOCUMENT]`
placeholders:

```markdown
# ADR: {Pattern Title}

**Status**: Detected
**Date**: {YYYY-MM-DD}
**Source files**: {comma-separated list of files that exhibit this pattern}

## Context

{1-2 sentence summary of what this pattern does in the codebase, derived from
source file analysis}

## Decision

{Description of the architectural decision — how this pattern is implemented and
why. Describe what is observable in the code.}

**Why this approach was chosen**: [TEAM TO DOCUMENT]

## Consequences

**Positive**: {List of benefits observable in the code}
**Negative/Trade-offs**: [TEAM TO DOCUMENT]

## Related Files

{List of files exhibiting this pattern, as relative paths with wiki-links}

- [[{relative_path}]] — {one-line description of the file's role}

## Related Issues

{List of GitHub issue wiki-links if discernible from code comments or commit
history; otherwise omit this section entirely}
```

Write each ADR using the Write or Edit tool to
`.nightgauge/knowledge/architecture/adr-{pattern-slug}.md`.

**Track generated files** in `ADR_FILES_GENERATED` for the done report.

#### Step 5.5.3: Generate Architecture Overview Note

Write `.nightgauge/knowledge/architecture/arch-notes.md` — a single
overview document listing all detected patterns with brief descriptions and
links to their ADR files.

```markdown
# Architecture Notes

**Generated**: {YYYY-MM-DD}
**Source**: docs-write --knowledge analysis

This document provides an auto-generated overview of key architectural patterns
detected in the codebase. Each section links to a detailed ADR.

> **Note**: Sections marked `[TEAM TO DOCUMENT]` require human input for
> historical context, design rationale, or trade-off analysis.

## Detected Patterns

| Pattern        | ADR File          | Key Files                    |
| -------------- | ----------------- | ---------------------------- |
| {Pattern Name} | [[adr-{slug}.md]] | {key files, comma-separated} |

...

## How to Use This Knowledge Base

- Review each ADR and fill in `[TEAM TO DOCUMENT]` placeholders
- Update **Status** from `Detected` to `Accepted` after team review
- Add new ADRs for patterns not auto-detected using the ADR template above
```

#### Step 5.5.4: Report Generated Files

```
Knowledge entries generated:
  .nightgauge/knowledge/architecture/adr-event-system.md
  .nightgauge/knowledge/architecture/adr-config-system.md
  .nightgauge/knowledge/architecture/arch-notes.md

[TEAM TO DOCUMENT] placeholders: 6 (require human input)
```

---

### Phase 6: Validate Accuracy

```bash
printf '<!-- phase:start name="validate-accuracy" index=8 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Self-check that all code references in the written section actually
exist in the source files.

#### Step 6.1: Extract Referenced Code Names

From the written section, extract all backtick-quoted names that look like:

- Class names (`PipelineOrchestrator`, `ContextManager`)
- Method names (`runPipeline`, `loadContext`)
- Type names (`PipelineStage`, `StageResult`)
- File paths (`src/...`)

#### Step 6.2: Verify Each Reference

For each extracted name, search the source files:

```bash
# Check class/function name exists
grep -rn "class PipelineOrchestrator\|function runPipeline\|type StageResult" \
  packages/ src/ --include="*.ts" --include="*.js" --include="*.py" 2>/dev/null
```

Report any references that could NOT be found:

```
Accuracy Check:
  ✓ PipelineOrchestrator — found in src/pipeline/PipelineOrchestrator.ts
  ✓ runPipeline — found in src/pipeline/PipelineOrchestrator.ts
  ⚠ StageRunner — NOT FOUND in source files
```

#### Step 6.3: Fix Accuracy Issues

For each unresolved reference:

1. Re-check the source files for the closest matching name
2. If a near match exists (e.g., `StageRunner` vs `StageExecutor`), update the
   written content to use the correct name
3. If no match exists, remove or soften the claim in the written content
4. Re-apply the corrected content to the target file

---

### Phase 7: Validate Links

```bash
printf '<!-- phase:start name="validate-links" index=9 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Verify all relative markdown links in the written section resolve.

#### Step 7.1: Run the Deterministic Validator

Validation is delegated to the Go binary's `docs check-links` verb (audit
row B6). The verb walks the target file, extracts every Markdown link
outside of fenced code blocks, and verifies that each relative target
resolves to a real file. External links (`http://`, `https://`, `mailto:`,
`tel:`, in-page `#anchor`) and code-fence content are skipped to match the
behavior of the previous bash implementation.

```bash
# Validate links inside the section that was just written. --section is
# case-insensitive and scopes validation to the heading subtree, matching
# how the previous bash flow only inspected $WRITTEN_CONTENT.
SECTION_NAME="${SECTION_NAME:-}"
if [ -n "$SECTION_NAME" ]; then
  LINK_RESULT=$(nightgauge docs check-links \
    --target "$TARGET_FILE" --section "$SECTION_NAME" --json)
else
  LINK_RESULT=$(nightgauge docs check-links --target "$TARGET_FILE" --json)
fi
LINK_EXIT=$?

# Schema v1 — fields are stable. Skills parse via fixed jq paths.
LINKS_TOTAL=$(printf '%s\n' "$LINK_RESULT" | jq -r '.links_total')
LINKS_BROKEN=$(printf '%s\n' "$LINK_RESULT" | jq -r '.links_broken')
```

`LINK_EXIT` is `0` when all links resolve, `1` when at least one is broken,
`2` on hard error (e.g. unreadable target). Treat `2` as a failure of this
phase; treat `1` as the input to Step 7.2.

#### Step 7.2: Fix Broken Links

When `LINKS_BROKEN > 0`, iterate over `findings[]` and repair each one. The
verb reports the file, line, raw link text, attempted resolved path, the
optional anchor, and a closed-enum `reason`
(`file_not_found`, `outside_root`, `unreadable`).

```bash
printf '%s\n' "$LINK_RESULT" | jq -r '.findings[] | "\(.file):\(.line)  \(.link)  → \(.resolved)  [\(.reason)]"'
```

For each finding:

1. Search for the actual file location
2. Update the link in the written section with the correct relative path
3. Re-apply the corrected content to the target file
4. Re-run the verb against the same `--target` (and `--section` when set);
   stop iterating when `links_broken == 0`

#### Step 7.3: Report Results

```
Link Check (nightgauge docs check-links — schema v1):
  Total: 3, Broken: 1

  ⚠ docs/ARCHITECTURE.md:142  ../docs/ARCHITECTURE.md  →  /…/docs/docs/ARCHITECTURE.md  [file_not_found]
  → Fixed: updated link to ./ARCHITECTURE.md
```

---

### Phase 8: Done Report

```bash
printf '<!-- phase:start name="done-report" index=10 total=10 stage="docs-write" -->\n'
```

**PURPOSE**: Summarize what was written, validated, and any remaining issues.

```
┌──────────────────────────────────────────────────────────────────┐
│  DOCS-WRITE COMPLETE                                             │
└──────────────────────────────────────────────────────────────────┘

Target file:   docs/ARCHITECTURE.md
Section:       Pipeline Lifecycle
Action:        inserted (was not present)
Dry-run:       no

Source files read:     4
Classes referenced:    6 / 6 verified ✓
Links checked:         3 / 3 resolved ✓

Files modified:
  ✓ docs/ARCHITECTURE.md

Next steps:
  - Review the written section for narrative quality
  - Run /update-docs to check for broader doc drift
  - Commit and run /nightgauge:pr-create when ready
```

When `--knowledge` was used, append a knowledge generation summary:

```
Knowledge entries generated:  3
  Architecture directory: .nightgauge/knowledge/architecture/
  ADRs written:           2 (adr-event-system.md, adr-config-system.md)
  Overview note written:  1 (arch-notes.md)
  [TEAM TO DOCUMENT] placeholders: 6 (require human input)
```

If there are unresolved references or broken links that could not be auto-fixed,
list them clearly:

```
⚠ Unresolved references (manual review needed):
  - `StageRunner` not found in source — removed from written content
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

| Condition                     | Action                                                                                |
| ----------------------------- | ------------------------------------------------------------------------------------- |
| Missing --target or --section | Exit 1 with usage message                                                             |
| Target file not found         | Create the file with the section as its content                                       |
| No source files found         | Warn, prompt user to provide `--source <glob>`, or continue with available context    |
| Source file missing           | Warn and continue with remaining source files                                         |
| Accuracy check fails          | Auto-correct if near-match found; soften/remove claim if no match; report all changes |
| Link validation fails         | Search for correct path, auto-fix if found; report if unfixable                       |
| --dry-run set                 | Print generated content, do not modify any files                                      |

---

## Philosophy

- **Accuracy over completeness** — Never state something that can't be verified
  in source
- **Validate before shipping** — Self-check all code references and links
- **Minimal footprint** — Only writes the requested section; never touches other
  sections
- **No approval cycle needed** — Self-contained for well-scoped doc tasks

## Pipeline Position

```
UTILITIES (standalone, not part of the main pipeline)

/nightgauge:docs-write
       ↑
  Use when picking up a documentation issue
```

This skill is a standalone utility. It does NOT write pipeline context files and
does NOT require `/nightgauge-feature-planning` first.

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) -
Issue-to-PR Pipeline.
