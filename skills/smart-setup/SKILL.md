---
name: smart-setup
description: Make any repository AI-ready with a tool-neutral AGENTS.md, a thin
  CLAUDE.md adapter, focused docs and a CI conformance check, migrating
  repositories that use the older CLAUDE.md-first layout. Use when setting up a
  new project, when a repository is missing or has outdated AI configuration
  files, or to verify conformance with `/smart-setup verify`.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "5.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---

# Smart Setup

> Make any repository AI-ready

## Description

This skill analyzes your repository and creates **minimal, focused
documentation** optimized for both humans and AI coding agents. It uses a
**tiered approach** to avoid bloating repositories with unnecessary files.

It has two modes:

- **Setup** (default) — detect which instruction model the repository uses and
  either generate the files, fill gaps in files that already follow the
  architecture, or produce a **migration plan and proposed diff** for a
  repository in the older model. It installs a deterministic check and its CI
  job so the result stays conformant.
- **Verify** (`/smart-setup verify`) — read-only conformance report against the
  DC-01…DC-22 checklist.

## Invocation

| Tool           | Command                                          |
| -------------- | ------------------------------------------------ |
| Claude Code    | `/smart-setup` or `/smart-setup verify` (plugin) |
| OpenAI Codex   | `$smart-setup` or `$smart-setup verify`          |
| GitHub Copilot | Invoke via Agent Skills                          |
| Cursor         | Invoke via Agent Skills                          |

## Supporting files (load on demand)

Each file sits in the same directory as this SKILL.md; pipeline runtimes export
that directory as `NIGHTGAUGE_SKILL_DIR`. Call it `SKILL_DIR` below.

- `_includes/migration.md` — read in Step 0.3 (classify the instruction model)
  and Phase 4M (migration plan and diff for the OLD model)
- `_includes/templates.md` — read in Phase 4 (every file template)
- `_includes/enforcement.md` — read in Phase 4, step 7 (install the check and
  its `agent guidance` CI job)
- `_includes/verify.md` — read in Verify mode (DC-01…DC-22)
- `scripts/check-agent-guidance.sh` — the deterministic check, byte-identical
  to the one Nightgauge runs on itself; installed into the target repository

## Philosophy

- **Keep it minimal** — Only create what teams actually need; avoid bloating
  repositories
- **Document what IS** — Current practices, patterns, and conventions
- **Note what SHOULD BE** — Flag deviations from best practices for team review
- **Leave room for WHY** — Mark sections requiring human input (tribal knowledge
  AI can't infer)
- **Don't bloat repositories** — Skip files for tools the team doesn't use
- **One home per fact** — Explanations live in `docs/`; `AGENTS.md` holds the
  rules every session needs and points to them
- **Never lose a rule** — Migration moves every rule to its new home and lists
  where it went; it never deletes one
- **Enforce, don't just instruct** — Structure is checked in CI, not trusted to
  a model

Use this marker for information only humans can provide:

```text
[TEAM TO DOCUMENT: <specific question>]
```

**Example - Before (AI guessing):**

```markdown
## Database Choice

We use PostgreSQL because it's reliable and has excellent JSON support.
```

**Example - After (marking for human input):**

```markdown
## Database Choice

We use PostgreSQL. [TEAM TO DOCUMENT: Why was PostgreSQL chosen over
MySQL/MongoDB?]
```

This prevents AI hallucination and preserves tribal knowledge.

## Arguments

This skill supports inline arguments via `$ARGUMENTS`:

```bash
# Setup with default settings
/smart-setup

# Specify configuration level
/smart-setup --tier essential
/smart-setup --tier standard
/smart-setup --tier advanced

# Read-only conformance report (changes nothing)
/smart-setup verify
```

The `$ARGUMENTS` variable contains everything after the skill name. When it
starts with `verify`, skip to [Verify Mode](#verify-mode).

## Instruction Architecture

Smart Setup produces the architecture defined in Nightgauge's
[agent guidance reference](https://github.com/nightgauge/nightgauge/blob/main/docs/AGENT_GUIDANCE.md).
Tools load instruction files differently, so the layout is designed for the
tool that reads the least:

| Tool           | Root `AGENTS.md`                            | Nested `AGENTS.md`                                       | `CLAUDE.md`                    |
| -------------- | ------------------------------------------- | -------------------------------------------------------- | ------------------------------ |
| Claude Code    | Only through `@AGENTS.md` in `CLAUDE.md`    | Not read (nested `CLAUDE.md` is)                         | Yes                            |
| Codex          | Yes                                         | Root down to the working directory, nothing deeper       | Not read                       |
| GitHub Copilot | Cloud agent, CLI, code review, VS Code chat | Cloud agent; VS Code only behind an experimental setting | CLI, cloud agent, VS Code chat |
| Cursor         | Yes                                         | Yes                                                      | Yes                            |
| Kiro           | Yes                                         | Yes                                                      | Not documented                 |

Therefore:

1. **Root `AGENTS.md` is the operating contract** and must be complete on its
   own: a session started at the root in Claude Code or Codex sees nothing
   else. Its first heading names the project, never a tool; it never defers to
   `CLAUDE.md`; it names the complete local gate; it stays under 200 lines.
2. **Root `AGENTS.md` indexes every nested `AGENTS.md`.** A nested file only
   adds rules for its subtree, and each has a sibling `CLAUDE.md` whose line 1
   is `@AGENTS.md` so Claude Code loads it there. The root-to-deepest chain
   stays under 32 KiB (Codex's default budget).
3. **`CLAUDE.md` line 1 is exactly `@AGENTS.md`.** Cursor, Copilot and VS Code
   read `CLAUDE.md` too, so below the import it holds only Claude Code content,
   under a heading that names Claude Code — no commands, rules or routing.
4. **Routing lives in `docs/AGENT_GUIDANCE.md`** under the heading
   `## Documentation routing`, linked from the docs index (`docs/README.md`).
5. **Instruction files are regular files** — no symlinks, no imports from
   outside the repository.
6. **Enforcement is deterministic** — `scripts/check-agent-guidance.sh` runs in
   a CI job named `agent guidance`.

## IMPORTANT: Tiered Approach

This setup uses a **tiered approach** to avoid creating unnecessary files:

### Tier 1: Essential (Default)

These are the minimum files every AI-ready repository needs:

**AI configuration files:**

| Tools Selected                        | Create AGENTS.md | Create CLAUDE.md             |
| ------------------------------------- | ---------------- | ---------------------------- |
| Claude Code only                      | ✅ Yes           | ✅ Yes                       |
| GitHub Copilot, Cursor, or Kiro (any) | ✅ Yes           | ❌ No (unless also selected) |
| Claude Code + any other tool          | ✅ Yes           | ✅ Yes                       |

> `AGENTS.md` is always the tool-neutral contract. Claude Code does not read it
> directly, so Claude projects also receive a thin `CLAUDE.md` whose line 1
> imports `@AGENTS.md`. This avoids duplicate instructions while leaving the
> repository ready for other compatible agents.

**Documentation files (always created):**

- `docs/README.md` - Documentation index (links `docs/AGENT_GUIDANCE.md`)
- `docs/AGENT_GUIDANCE.md` - Agent architecture and `## Documentation routing`
- `docs/ARCHITECTURE.md` - System architecture
- `docs/CODE_STANDARDS.md` - Coding conventions
- `docs/GIT_WORKFLOW.md` - Git/version control workflow (or equivalent for TFS,
  etc.)
- `docs/SECURITY_AND_ERROR_HANDLING.md` - Security guidelines and error handling

**Enforcement (always installed):**

- `scripts/check-agent-guidance.sh` - byte-identical copy of the bundled check
- `.github/workflows/agent-guidance.yml` - CI job named `agent guidance`

> **Note**: Tier 1 files are created regardless of whether user selects
> Essential, Standard, or Advanced. Higher tiers ADD to Tier 1, not replace it.

### Tier 2: Tool-Specific (Based on User's Tools)

**Only create these if the user uses that specific tool:**

- `.github/copilot-instructions.md` — **only when GitHub Copilot is selected.**
  `AGENTS.md` is read by the Copilot cloud agent, Copilot CLI, code review and
  VS Code chat; github.com Chat, Visual Studio, JetBrains, Eclipse and Xcode
  chat and IDE code review read only this file
  ([support matrix](https://docs.github.com/en/copilot/reference/custom-instructions-support)).
  It is a pointer to `AGENTS.md` of at most 30 lines, never a second ruleset.
- `.cursor/rules/` — Cursor already applies `AGENTS.md` and `CLAUDE.md`. Create
  a rule only for something genuinely Cursor-specific (a glob-scoped rule).
- `.kiro/steering/` — Kiro already includes `AGENTS.md`. Create a steering file
  only for something genuinely Kiro-specific.

Never create `.cursorrules`, `.windsurfrules` or `GEMINI.md` copies of
`AGENTS.md`.

### Tier 3: Advanced (Opt-in Only)

**Only create if user explicitly requests advanced configuration:**

- `.claude/skills/` - Custom automation workflows
- `.claude/subagents/` - Multi-agent orchestration
- `.claude/workflows/` - Complex pipelines
- `.claude/templates/` - Code templates
- `.claude/instructions/` - Task-specific instructions
- `.github/prompts/` - Reusable prompts

**DO NOT create Tier 3 content unless the user specifically asks for it.**

---

## Workflow

### Phase 0: Auto-Detect + Ask (2 Questions Max)

#### Step 0.1: Silent Auto-Detection (No User Interaction)

Before asking questions, detect:

**Version Control:**

- `.git/` → Git
- `.svn/` → SVN
- `$tf/` or `.tfvc/` → TFS
- None → Ask user

**Existing AI Config Files:**

- `AGENTS.md` (root and nested) — the tool-neutral contract
- `CLAUDE.md` (root and nested) — Claude Code adapter; line 1 should be
  `@AGENTS.md`
- `.github/copilot-instructions.md` — Copilot pointer adapter
- `.cursor/rules/`, `.cursorrules` — Cursor config
- `.kiro/steering/` — Kiro config
- `.windsurfrules`, `GEMINI.md` — other tool files
- `scripts/check-agent-guidance.sh`, `.github/workflows/*` running it —
  enforcement

**Documentation:** Check for docs/ directory and its contents, and for an
existing documentation index.

**Nightgauge Knowledge Base:**

- `.nightgauge/knowledge/` → knowledge base present; note subdirectory
  count and types
- `.nightgauge/` exists but no `knowledge/` sub-dir → flag for
  recommendation (only when `.nightgauge/config.yaml` also present)
- Neither exists → skip (non-Nightgauge repo)

Store results: `KNOWLEDGE_DIR_EXISTS`, `KNOWLEDGE_ENTRY_COUNT` (total issue
subdirs), `HAS_NIGHTGAUGE_CONFIG` (from existing config.yaml check).

```bash
KNOWLEDGE_DIR=".nightgauge/knowledge"
KNOWLEDGE_DIR_EXISTS=false
KNOWLEDGE_ENTRY_COUNT=0
HAS_NIGHTGAUGE_CONFIG=false
[ -f ".nightgauge/config.yaml" ] && HAS_NIGHTGAUGE_CONFIG=true
if [ -d "$KNOWLEDGE_DIR" ]; then
  KNOWLEDGE_DIR_EXISTS=true
  KNOWLEDGE_ENTRY_COUNT=$(find "$KNOWLEDGE_DIR" -mindepth 2 -maxdepth 2 -type d 2>/dev/null | wc -l | tr -d ' ')
fi
```

Store results internally for Phase 1 reporting.

#### Step 0.2: Ask Essential Questions

> **Claude Code**: Use `AskUserQuestion` tool for better UX with clickable
> buttons.

```json
{
  "questions": [
    {
      "question": "Which AI coding tools does your team use?",
      "header": "AI Tools",
      "multiSelect": true,
      "options": [
        {
          "label": "Claude Code",
          "description": "Reads CLAUDE.md, which imports AGENTS.md"
        },
        {
          "label": "GitHub Copilot",
          "description": "Agent, CLI, review and VS Code chat read AGENTS.md; other chat surfaces need a short pointer file"
        },
        {
          "label": "Cursor IDE",
          "description": "Reads AGENTS.md and CLAUDE.md natively; no extra file needed"
        },
        {
          "label": "Kiro IDE",
          "description": "Reads AGENTS.md natively; no extra file needed"
        }
      ]
    },
    {
      "question": "What level of AI configuration do you want?",
      "header": "Config Level",
      "multiSelect": false,
      "options": [
        {
          "label": "Essential (Recommended)",
          "description": "AGENTS.md, CLAUDE.md if selected, core docs, CI check"
        },
        {
          "label": "Standard",
          "description": "Essential + tool-specific configs for selected AI tools"
        },
        {
          "label": "Advanced",
          "description": "Full framework with skills, workflows, subagents"
        }
      ]
    }
  ]
}
```

Codex and other `AGENTS.md`-compatible agents need no extra file; a user may
name them under "Other".

**WAIT for user response before proceeding to Phase 1.**

**Optional Answer Log**: After questions, offer to create
`AI_SMART_SETUP_ANSWER_LOG.md`:

> "Would you like me to create an answer log to track your choices? Note: May
> contain sensitive project info. Consider adding to .gitignore."

If accepted, create with this format:

```markdown
# AI Smart Setup - Answer Log

Generated: [YYYY-MM-DD]

## Configuration Choices

- **AI Tools**: [user's selection]
- **Config Level**: [Essential/Standard/Advanced]
- **Version Control**: [Git/TFS/etc.]
- **CLAUDE.md**: [Yes/No]
- **Instruction model detected**: [NONE/OLD/NEW]

## Project Context Questions

[Record questions asked and user's answers here]

## Files Created/Modified

- [ ] File 1 - status
- [ ] File 2 - status
```

#### Step 0.3: Classify the Instruction Model

**Read `_includes/migration.md` (same directory as this SKILL.md) now and run
its Phase 0.3 detection before continuing.**

It classifies the repository as `NONE` (no instruction files), `OLD` (any of
signals M1–M8: `CLAUDE.md` without `@AGENTS.md` on line 1; routing, commands
or portable rules in `CLAUDE.md`; `AGENTS.md` deferring to `CLAUDE.md`;
`AGENTS.md` whose first heading names a tool; a committed managed-steering
block; symlinked instruction files; over-budget files; rules only in a tool
file) or `NEW`. The model decides the Phase 4 path.

---

### Phase 1: Scan & Report

Scan ONLY files relevant to user's selections. Report findings clearly:

```text
Based on your selections (Claude Code + Copilot, Essential config, Git):

Instruction model: OLD — a migration plan will be proposed, not an additive merge
  [M1] CLAUDE.md line 1 is not @AGENTS.md
  [M3] AGENTS.md:3 "Read CLAUDE.md first"
  [M5] AGENTS.md:88-140 managed steering block committed

✅ AGENTS.md - exists (127 lines)
✅ CLAUDE.md - exists (214 lines, holds the documentation map and commands)
✅ docs/ - exists with 5 files
   ├── README.md ✅
   ├── ARCHITECTURE.md ✅
   ├── CODE_STANDARDS.md ✅
   ├── GIT_WORKFLOW.md ✅
   └── SECURITY_AND_ERROR_HANDLING.md ❌ missing
❌ docs/AGENT_GUIDANCE.md - missing (routing currently in CLAUDE.md)
❌ scripts/check-agent-guidance.sh - not installed
⚠️ .github/copilot-instructions.md (67 lines) - rules will move to AGENTS.md;
   the file becomes a pointer (Copilot selected)
```

**DO NOT report on tool-specific files the user didn't select**, except that
an unselected tool's file holding project rules is reported as a migration
source (its rules must move before it can be removed).

**Nightgauge Knowledge Base reporting**: When
`HAS_NIGHTGAUGE_CONFIG=true`:

- If `KNOWLEDGE_DIR_EXISTS=true`: Report
  `✅ Knowledge base active (.nightgauge/knowledge/ — {KNOWLEDGE_ENTRY_COUNT} issue entries)`
  — the Knowledge row will be added to the `## Documentation routing` table in
  `docs/AGENT_GUIDANCE.md`, and a `## Knowledge base` section to AGENTS.md.
- If `KNOWLEDGE_DIR_EXISTS=false`: Report the following recommendation:

```text
ℹ️  Knowledge base directory not present yet. It is enabled by default and is
    scaffolded on the next issue pickup — no config needed. To opt out (repo
    footprint or per-run token cost), add to .nightgauge/config.yaml:
    knowledge:
      enabled: false
    See: docs/KNOWLEDGE_BASE.md
```

---

### Phase 2: Context Questions About Files

**For a `NEW`-model repository**, ask about each gap the check or the
templates reveal:

- "AGENTS.md follows the architecture (127 lines). It does not name the local
  gate. Add a Commands section taken from your CI workflow?"

**For an `OLD`-model repository**, ask once:

- "This repository uses the older layout (signals M1, M3, M5). I'll prepare a
  migration plan and a proposed diff on a new branch for your review; nothing
  is committed until you approve. Proceed?"

**For missing files**, ask if they want them created:

- "docs/SECURITY_AND_ERROR_HANDLING.md is missing. Create it?"

---

### Phase 3: Analyze Codebase & Ask Project Questions

1. **Analyze codebase thoroughly:**
   - Primary language (check file extensions, config files)
   - Framework (check imports, dependencies)
   - Build tools & package managers
   - Testing frameworks
   - CI workflows and the commands they run (the local gate must match them)
   - Sub-packages with their own toolchain (nested-file candidates)
   - IDE indicators (.idea/, .vscode/, .vs/)

2. **Ask questions AI cannot infer:**
   - Why was this architecture chosen?
   - What's the team's testing philosophy?
   - Any known issues or technical debt?
   - What's the deployment/approval workflow?
   - What tribal knowledge do new team members need?

3. **User can answer or say "skip"** — use `[TEAM TO DOCUMENT: <question>]`
   markers for skipped questions.

---

### Phase 4: Generate Documentation

**Read `_includes/templates.md` (same directory as this SKILL.md) now; every
file below uses its template.**

**Path by model:**

- `NONE` — generate from the templates in the order below.
- `NEW` — additive: add only the parts the user approved in Phase 2; never
  rewrite content that already conforms.
- `OLD` — **HARD GATE: run Phase 4M from `_includes/migration.md` instead of
  the additive merge.** It produces a migration plan and proposed diff; the
  steps below are applied inside it.

**Generation order** (later files reference earlier ones):

1. `docs/GIT_WORKFLOW.md` (or TFS) — using [Git Workflow Rules](#git-workflow-rules)
2. `docs/SECURITY_AND_ERROR_HANDLING.md` — using [Security Rules](#security-rules)
3. Other docs/ files, then `docs/AGENT_GUIDANCE.md` (`## Documentation
routing`; every path must resolve) and `docs/README.md` linking it
4. Root `AGENTS.md` — always; tool-neutral first heading, the provenance marker
   `<!-- nightgauge:agent-guidance v1 -->` below it, the complete local gate,
   and the nested-file index
5. Nested `AGENTS.md` + sibling `CLAUDE.md` — only for approved sub-packages
6. `CLAUDE.md` — only if Claude Code was selected; line 1 `@AGENTS.md`, then
   only Claude Code content
7. **Enforcement** — **Read `_includes/enforcement.md` (same directory as this
   SKILL.md) now** and install `scripts/check-agent-guidance.sh` plus the
   `agent guidance` CI job with `--workspace-block forbidden`
8. Tool-specific files (Tier 2) — only for selected tools; pointers, not copies
9. Run the installed check; it must exit `0` before you present the result

**CRITICAL RULES:**

1. **Use REAL code examples only** — Every example MUST come from actual files
   in this repository
2. **Never overwrite without permission** — Always ask before modifying existing
   files; in the `OLD` model, the migration diff is the request
3. **Never delete a rule** — every rule in a file you change ends up somewhere,
   and the plan says where
4. **Never copy rules from another repository** — including Nightgauge's own
   rules and its workspace-rules block
5. **Mark unknowns clearly** — `[TEAM TO DOCUMENT: Why was PostgreSQL chosen?]`
6. **Respect tier selection** — Don't create files for unselected tools

---

## Canonical Rules (Referenced by Templates)

### Git Workflow Rules

These rules MUST be included in `docs/GIT_WORKFLOW.md` and referenced (not
duplicated) by other files:

1. **NEVER push directly to `main`** - Always use feature branches
2. **ALWAYS create pull requests** - Even for small changes
3. **Follow branch naming conventions**:
   - `feat/` - New features
   - `fix/` - Bug fixes
   - `docs/` - Documentation changes
   - `refactor/` - Code refactoring
   - `test/` - Test additions/changes
   - `chore/` - Maintenance tasks
4. **Write meaningful commit messages** - Follow conventional commit format
5. **Request code review** - All PRs require at least one approval

**For TFS users**, create `docs/TFS_WORKFLOW.md` with equivalent rules for
shelvesets, check-in policies, and branch folders.

### Security Rules

These rules MUST be included in `docs/SECURITY_AND_ERROR_HANDLING.md` and
referenced (not duplicated) by other files:

1. **NEVER hardcode secrets** - Use environment variables or secure secret
   management
2. **ALWAYS validate input** - Sanitize all user input at system boundaries
3. **NEVER expose sensitive data** - Keep credentials, API keys, and PII out of
   logs and responses
4. **ALWAYS use parameterized queries** - Prevent SQL injection attacks
5. **NEVER commit secrets to git** - Use .gitignore and pre-commit hooks
6. **Handle errors gracefully** - Don't expose internal details in error
   messages
7. **Log security events** - Authentication attempts, authorization failures,
   input validation failures

---

## File Templates

All templates live in `_includes/templates.md` (same directory as this
SKILL.md): `docs/GIT_WORKFLOW.md`, `docs/SECURITY_AND_ERROR_HANDLING.md`,
`docs/README.md`, `docs/AGENT_GUIDANCE.md`, root `AGENTS.md`, `CLAUDE.md`,
nested `AGENTS.md` with its sibling `CLAUDE.md`, and
`.github/copilot-instructions.md`. The enforcement workflow template is in
`_includes/enforcement.md`.

**CLAUDE.md must stay small.** For each line, ask: would removing it make
Claude Code make a mistake? If not, cut it. No directory listings, no
self-evident advice, no standard language conventions, nothing discoverable by
reading the code — and, because other tools read it too, no commands, rules or
routing.

---

## Stack-Specific Additions

Include relevant sections based on detected stack:

**For .NET/C#:** File-scoped namespaces, primary constructors (if C# 10+),
records for DTOs, nullable reference types, logging patterns
(NLog/Serilog/ILogger<T>), xUnit/NUnit patterns.

**For Python:** Type hints usage, package manager (pip/poetry/uv/conda), pytest
patterns, code style (ruff/black/isort/flake8).

**For Node.js/TypeScript:** tsconfig.json key settings, package manager
(npm/yarn/pnpm), Jest/Vitest/Mocha patterns, ESLint/Prettier configuration.

**For Java:** Maven or Gradle commands, Spring Boot/Quarkus patterns,
JUnit/TestNG patterns, Checkstyle/SpotBugs configuration.

---

## docs/ Directory Files

| File                                  | Create When         | Content                                                     |
| ------------------------------------- | ------------------- | ----------------------------------------------------------- |
| `docs/README.md`                      | Always              | Documentation index with quick links                        |
| `docs/ARCHITECTURE.md`                | Always              | System overview, directory structure, key components        |
| `docs/CODE_STANDARDS.md`              | Always              | Naming, file organization, code examples from repo          |
| `docs/GIT_WORKFLOW.md`                | Git detected        | Use template from [Git Workflow Rules](#git-workflow-rules) |
| `docs/TFS_WORKFLOW.md`                | TFS detected        | Workspace setup, check-in process, branching                |
| `docs/SECURITY_AND_ERROR_HANDLING.md` | Always              | Use template from [Security Rules](#security-rules)         |
| `docs/GETTING_STARTED.md`             | Recommended         | Prerequisites, installation, running locally                |
| `docs/TESTING.md`                     | Only if tests exist | Test structure, running tests, coverage                     |
| `docs/DEPLOYMENT.md`                  | Only if CI/CD found | Pipeline, environments, manual steps                        |

**SKIP files that would be empty or contain only `[TEAM TO DOCUMENT]` markers.**

---

## Validation Checklist

After creating or migrating files, run [Verify Mode](#verify-mode) on the
result. Every `FAIL` must be fixed before the change is proposed; report each
`WARN` to the user. In addition, confirm the judgement items the checklist
cannot see:

- [ ] All examples come from THIS repository (no generic/placeholder code)
- [ ] All `[TEAM TO DOCUMENT]` markers are specific questions
- [ ] `CLAUDE.md` holds no self-evident or discoverable information
- [ ] `AGENTS.md` references `docs/` files instead of duplicating procedures
- [ ] No files created for unselected tools; the tier selection was respected
- [ ] No empty/stub files that provide no value
- [ ] In a migration, every inventoried rule appears in the plan with a
      destination

---

## Existing Files Policy

The policy depends on the instruction model detected in Step 0.3.

### `NEW` model — additive, non-destructive

For files that already follow the architecture:

1. **READ the existing file first** — understand what is documented
2. **NEVER overwrite** — do not replace existing content
3. **IDENTIFY gaps** — compare against the templates and the check's findings
4. **OFFER additions** — suggest specific sections to ADD
5. **ASK permission** — before ANY modification:
   > "AGENTS.md exists (127 lines). I found these gaps: [list]. Add them?"

### `OLD` model — migration plan, never an additive merge

Adding sections to an old-model file leaves the old model in place. Instead,
Phase 4M in `_includes/migration.md`:

| Existing file                                                                     | Becomes                                                                  |
| --------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `CLAUDE.md` holding rules, commands or routing                                    | Thin adapter; each rule moved to `AGENTS.md`, `docs/` or a nested file   |
| `AGENTS.md` deferring to `CLAUDE.md` / tool-named                                 | Tool-neutral contract holding the moved rules                            |
| Documentation map anywhere but the routing doc                                    | `docs/AGENT_GUIDANCE.md` § `## Documentation routing`, linked from index |
| `.github/copilot-instructions.md` with rules                                      | Rules moved; file kept as a pointer only if Copilot is selected          |
| `.cursorrules`, `.windsurfrules`, `GEMINI.md`, `.kiro/steering/*` restating rules | Unique rules moved; file proposed for removal                            |
| Symlinked instruction file                                                        | Regular file                                                             |
| Committed managed-steering block in `AGENTS.md`                                   | Removed (generated content, not a rule)                                  |

The plan lists every moved rule with its old and new location; the diff is
presented on a branch for review before anything is committed.

---

## Phase 4.5: Tooling Config Scaffold (Greenfield Only)

**Only execute when ALL of the following are true:**

1. No `tsconfig.json` exists in the project root (greenfield indicator)
2. `package.json` exists and indicates TypeScript usage
3. User has not passed `--skip-tooling`

```bash
# Only generate if tsconfig doesn't exist and TypeScript is in use
GENERATE_TOOLING=false
if [ ! -f "tsconfig.json" ] && grep -q '"typescript"' package.json 2>/dev/null; then
  GENERATE_TOOLING=true
fi
```

**If `GENERATE_TOOLING=true`**, ask the user before generating:

```json
{
  "questions": [
    {
      "question": "Detected TypeScript project without tooling configs. Generate starter configs?",
      "header": "Tooling Scaffold",
      "multiSelect": true,
      "options": [
        {
          "label": "tsconfig.json",
          "description": "TypeScript strict mode config with ESM output"
        },
        {
          "label": "vitest.config.ts",
          "description": "Only if vitest in devDependencies"
        },
        {
          "label": "eslint.config.js",
          "description": "Only if eslint in devDependencies"
        },
        {
          "label": ".prettierrc",
          "description": "Only if prettier in devDependencies"
        },
        {
          "label": ".github/workflows/ci.yml",
          "description": "Basic Node.js CI workflow (build + test)"
        },
        {
          "label": "Skip tooling scaffold",
          "description": "Skip all tooling config generation"
        }
      ]
    }
  ]
}
```

**If user confirms**, dispatch the selection to the deterministic
`nightgauge setup scaffold-tooling` Go verb. The verb owns brownfield-
safety (`[ ! -f ]` skip), Node-version detection from `package.json`
`engines.node`, devDep probing for vitest/eslint/prettier, and byte-for-
byte template emission — see
[docs/GO_BINARY.md → Setup Operations](../../docs/GO_BINARY.md#setup-operations)
for the schema, exit codes, and template provenance.

```bash
# Parse user selections from AskUserQuestion multiselect into the comma list
# the binary expects. Map each label to its --select key:
#   "tsconfig.json"               → tsconfig
#   "vitest.config.ts"            → vitest
#   "eslint.config.js"            → eslint
#   ".prettierrc"                 → prettier
#   ".github/workflows/ci.yml"    → ci
# "Skip tooling scaffold" or an empty selection → no run.
SELECTED_LIST=$(printf '%s\n' "$USER_MULTISELECT" | jq -r '
  [.[] | select(. != "Skip tooling scaffold")
    | sub("^tsconfig\\.json$"; "tsconfig")
    | sub("^vitest\\.config\\.ts$"; "vitest")
    | sub("^eslint\\.config\\.js$"; "eslint")
    | sub("^\\.prettierrc$"; "prettier")
    | sub("^\\.github/workflows/ci\\.yml$"; "ci")
  ] | join(",")')

if [ -z "$SELECTED_LIST" ]; then
  echo "Tooling scaffold skipped."
else
  nightgauge setup scaffold-tooling --select "$SELECTED_LIST" --json \
    | jq -r '
        "nightgauge setup scaffold-tooling — schema v\(.v)",
        "detected: package.json=\(.detected.package_json_found) node=\(.detected.node_version) ts=\(.detected.has_typescript) vitest=\(.detected.has_vitest) eslint=\(.detected.has_eslint) prettier=\(.detected.has_prettier)",
        (.outcomes[] |
          if .outcome == "created" then
            "  + created: \(.path)"
          elif .outcome == "skipped_existing" then
            "  ✓ \(.path) already exists — skipping"
          elif .outcome == "skipped_missing_dep" then
            "  ⚠ \(.path) skipped — \(.reason)"
          else
            "  ✗ \(.path) — \(.outcome)\(if .reason != "" then ": " + .reason else "" end)"
          end),
        (.warnings[]? | "  ! \(.)")
      '

  echo ""
  echo "=== Tooling Scaffold Complete ==="
  echo ""
  echo "Next steps:"
  echo "  1. Review each generated config and customize for your project"
  echo "  2. Commit the new files: git add . && git commit -m 'chore: add tooling scaffold'"
  echo "  3. Push and verify CI passes: git push origin HEAD"
fi
```

**Brownfield safety is enforced inside the binary** — every emit function
stats the target path (and, for ESLint/Prettier, the legacy filenames
`.eslintrc.js`, `.eslintrc.json`, `.prettierrc.json`,
`prettier.config.js`) before writing. The verb returns
`outcome: "skipped_existing"` for any pre-existing file rather than
overwriting.

---

## Phase 5: Project Validation (Optional)

This phase validates GitHub Project board integration for the repository. It
ensures the target project has required fields and optionally creates
`.nightgauge/config.yaml` with project configuration.

> **Skip Condition**: If user declines project integration in Step 5.2, skip to
> Phase 6.

### Step 5.1: Check for Existing Configuration

```bash
# Check if .nightgauge/config.yaml exists
CONFIG_FILE=".nightgauge/config.yaml"
if [ -f "$CONFIG_FILE" ]; then
  PROJECT_NUMBER=$(grep -E "^project:" -A5 "$CONFIG_FILE" | grep "number:" | awk '{print $2}')
  if [ -n "$PROJECT_NUMBER" ]; then
    echo "Found existing project configuration: Project #$PROJECT_NUMBER"
  fi
else
  echo "No nightgauge configuration found"
fi
```

### Step 5.2: Ask About Project Integration

```json
{
  "questions": [
    {
      "question": "Do you want to enable GitHub Project board integration?",
      "header": "Project",
      "multiSelect": false,
      "options": [
        {
          "label": "Yes (Recommended)",
          "description": "Validate project fields and create .nightgauge/config.yaml for roadmap tracking"
        },
        {
          "label": "No",
          "description": "Skip project integration for now (can add later)"
        }
      ]
    }
  ]
}
```

**If "No"**: Skip to Phase 6.

**If "Yes"**: Continue to Step 5.3.

### Step 5.3: Get Project Number

If no existing `.nightgauge/config.yaml` with project config:

```json
{
  "questions": [
    {
      "question": "What is your GitHub Project number? (Find it in the URL: /orgs/OWNER/projects/NUMBER)",
      "header": "Project #",
      "multiSelect": false,
      "options": [
        {
          "label": "Enter number",
          "description": "The numeric project ID from your project board URL"
        }
      ]
    }
  ]
}
```

### Step 5.4: Discover Project Fields

Use the `nightgauge forge` CLI to discover existing project fields
(deterministic, not AI interpretation):

```bash
# Get repository owner
OWNER=$(nightgauge forge repo view --repo "$REPO" --json owner -q '.owner.login')

# List all project fields (forge GraphQL passthrough)
nightgauge forge graphql -f query='
  query($org: String!, $number: Int!) {
    organization(login: $org) {
      projectV2(number: $number) {
        fields(first: 50) {
          nodes {
            ... on ProjectV2Field { id name dataType }
            ... on ProjectV2SingleSelectField { id name dataType options { id name } }
            ... on ProjectV2IterationField { id name dataType }
          }
        }
      }
    }
  }' -F org="$OWNER" -F number="$PROJECT_NUMBER"
```

Parse the JSON response to build a field inventory.

### Step 5.5: Validate Required Fields

Check for these required fields (used by Nightgauge pipeline):

| Field       | Type          | Required Options (for SINGLE_SELECT)         |
| ----------- | ------------- | -------------------------------------------- |
| Status      | SINGLE_SELECT | Backlog, Ready, In progress, In review, Done |
| Priority    | SINGLE_SELECT | P0, P1, P2, P3                               |
| Size        | SINGLE_SELECT | XS, S, M, L, XL                              |
| Start date  | DATE          | N/A                                          |
| Target date | DATE          | N/A                                          |
| Estimate    | NUMBER        | N/A                                          |
| Sprint      | ITERATION     | N/A (optional)                               |

For each field, check:

1. Field exists with correct name
2. Field has correct type
3. For SINGLE_SELECT fields, required options exist

### Step 5.6: Report Missing Fields

For each missing field, provide actionable fix commands:

```
┌─────────────────────────────────────────────────────────────────┐
│  PROJECT FIELD VALIDATION                                        │
└─────────────────────────────────────────────────────────────────┘

Project: #10 (nightgauge)

✅ Status - SINGLE_SELECT with required options
✅ Priority - SINGLE_SELECT with P0, P1, P2
❌ Size - MISSING

To create the Size field:

  nightgauge forge graphql -f query='
    mutation {
      createProjectV2Field(input: {
        projectId: "PROJECT_ID"
        dataType: SINGLE_SELECT
        name: "Size"
      }) { projectV2Field { ... on ProjectV2Field { id } } }
    }'

Then add options:

  nightgauge forge graphql -f query='
    mutation {
      updateProjectV2Field(input: {
        projectId: "PROJECT_ID"
        fieldId: "FIELD_ID"
        singleSelectOptions: [
          {name: "XS", color: GRAY}
          {name: "S", color: BLUE}
          {name: "M", color: GREEN}
          {name: "L", color: YELLOW}
          {name: "XL", color: RED}
        ]
      }) { field { id } }
    }'

✅ Start date - DATE
✅ Target date - DATE
⚠️ Sprint - ITERATION (optional, not configured)

Summary: 5/6 required fields present. Fix Size field to enable full pipeline.
```

**If all required fields present:**

```
✅ Project #10 is roadmap-ready! All required fields configured.
```

### Step 5.7: Offer to Add Repository to Project

If repository is not already linked to the project:

```json
{
  "questions": [
    {
      "question": "Add this repository to Project #10?",
      "header": "Link Repo",
      "multiSelect": false,
      "options": [
        {
          "label": "Yes (Recommended)",
          "description": "Link repo so issues can be added to project board"
        },
        {
          "label": "No",
          "description": "Skip for now (can link manually later)"
        }
      ]
    }
  ]
}
```

If "Yes":

```bash
REPO_ID=$(nightgauge forge repo view --repo "$REPO" --json id -q '.id')
PROJECT_ID=$(nightgauge forge graphql -f query='
  query($org: String!, $number: Int!) {
    organization(login: $org) { projectV2(number: $number) { id } }
  }' -F org="$OWNER" -F number="$PROJECT_NUMBER" --jq '.data.organization.projectV2.id')

nightgauge forge graphql -f query='
  mutation($p: ID!, $r: ID!) {
    linkProjectV2ToRepository(input: { projectId: $p, repositoryId: $r }) {
      repository { id }
    }
  }' -F p="$PROJECT_ID" -F r="$REPO_ID"
```

### Step 5.8: Create or Update .nightgauge/config.yaml

If `.nightgauge/config.yaml` doesn't exist or lacks project config, offer
to create it:

```json
{
  "questions": [
    {
      "question": "Create .nightgauge/config.yaml with project configuration?",
      "header": "Config",
      "multiSelect": false,
      "options": [
        {
          "label": "Yes (Recommended)",
          "description": "Enables Nightgauge pipeline commands (/issue-pickup, /pr-create, etc.)"
        },
        {
          "label": "No",
          "description": "Skip config file creation"
        }
      ]
    }
  ]
}
```

**Template for .nightgauge/config.yaml:**

```yaml
# Nightgauge Configuration
# See: https://github.com/nightgauge/nightgauge/docs/CONFIGURATION.md

project:
  # GitHub Project number (from URL: /orgs/OWNER/projects/NUMBER)
  number: <PROJECT_NUMBER>

  # GraphQL IDs (discovered during validation)
  # These enable deterministic field updates without API lookups
  id: "<PROJECT_GLOBAL_ID>"
  status_field_id: "<STATUS_FIELD_ID>"
  priority_field_id: "<PRIORITY_FIELD_ID>"
  size_field_id: "<SIZE_FIELD_ID>"

  # Optional: Sprint/Iteration support
  sprint:
    enabled: false
    field_name: "Sprint"
    auto_assign: false

# Pipeline configuration
pipeline:
  auto_fix: true # Auto-fix linting issues during feature development

# Sanitization (prompt injection protection)
sanitization:
  mode: warn # warn (default: log + allow), block, disabled
```

**Gitignore Entry for Local Config:**

When creating or updating `.gitignore`, add the local config override file:

```gitignore
# Nightgauge - local developer config (personal overrides)
.nightgauge/config.local.yaml
```

> **Note**: `.nightgauge/config.local.yaml` allows individual developers to
> override project settings without affecting the shared config. See
> [docs/CONFIGURATION.md](https://github.com/nightgauge/nightgauge/docs/CONFIGURATION.md#local-config-override)
> for details.

**Field ID Discovery**: Use the field data from Step 5.4 to populate the IDs:

```bash
# Extract field IDs from the field-list response (forge GraphQL passthrough)
nightgauge forge graphql -f query='
  query($org: String!, $number: Int!) {
    organization(login: $org) {
      projectV2(number: $number) {
        fields(first: 50) {
          nodes {
            ... on ProjectV2Field { id name }
            ... on ProjectV2SingleSelectField { id name }
            ... on ProjectV2IterationField { id name }
          }
        }
      }
    }
  }' -F org="$OWNER" -F number="$PROJECT_NUMBER" | \
  jq '{
    status_field_id: (.data.organization.projectV2.fields.nodes[] | select(.name == "Status") | .id),
    priority_field_id: (.data.organization.projectV2.fields.nodes[] | select(.name == "Priority") | .id),
    size_field_id: (.data.organization.projectV2.fields.nodes[] | select(.name == "Size") | .id)
  }'
```

---

## Phase 6: Completion

1. **Review generated files** for accuracy, then run [Verify Mode](#verify-mode)
2. **Generate TODO file** for `[TEAM TO DOCUMENT]` markers (see below)
3. **Commit through the repository's workflow** — a feature branch and a pull
   request, never a direct push to the default branch
4. **Tell the user to make `agent guidance` a required status check** once the
   pull request that adds it has merged; until then a red check can be merged

### Documentation TODO File

After generating all files, scan for `[TEAM TO DOCUMENT]` markers and create
`AI_SETUP_TODO.md`:

**Why this file exists**: AI assistants can document _what_ your code does, but
only your team knows _why_ certain decisions were made. These markers flag
sections requiring tribal knowledge that AI cannot infer from code alone.

```markdown
# AI Setup - Documentation TODO

> **Purpose**: These sections were flagged during AI setup because they require
> team knowledge that cannot be inferred from code. Delete this file when
> complete.

## How to Use

1. Work through each item with team members who have context
2. Check off items as you document them
3. Delete this file when all items are complete

---

## docs/ARCHITECTURE.md

- [ ] Line 45: `[TEAM TO DOCUMENT: Why was this architecture chosen?]`
- [ ] Line 78: `[TEAM TO DOCUMENT: Key integration points]`

## AGENTS.md

- [ ] Line 23: `[TEAM TO DOCUMENT: Team-specific conventions]`

---

_Generated by AI Smart Setup on [DATE]_
```

**Generation Instructions**:

1. After all files are created, grep for `[TEAM TO DOCUMENT` across generated
   files
2. Group findings by file with line numbers
3. Create the TODO file only if markers exist
4. Include the marker text so teams know what's needed

**Cleanup Offers**: After completion, ask:

> "Setup complete! I created AI_SETUP_TODO.md with X items that need team
> input."

If answer log was created, also ask about its deletion (remind about sensitive
info).

---

## Verify Mode

`/smart-setup verify` — read-only conformance report. It changes no file.

**Read `_includes/verify.md` (same directory as this SKILL.md) now and follow
it.** It runs the installed `scripts/check-agent-guidance.sh` with the flags
the repository's `agent guidance` CI job uses (the items marked "script"), then
performs the remaining checklist items with explicit commands:

| ID    | Requirement                                                                      | Decided by     |
| ----- | -------------------------------------------------------------------------------- | -------------- |
| DC-01 | Root `AGENTS.md` is a regular file                                               | script         |
| DC-02 | `AGENTS.md` ≤ 200 lines; `AGENTS.md` chain ≤ 32 KiB                              | script         |
| DC-03 | First heading of `AGENTS.md` names no tool                                       | script         |
| DC-04 | `AGENTS.md` does not defer to `CLAUDE.md`                                        | script         |
| DC-05 | `CLAUDE.md` regular, line 1 `@AGENTS.md`, ≤ 100 lines                            | script         |
| DC-06 | No routing or commands in `CLAUDE.md`                                            | script         |
| DC-07 | Routing doc exists, linked from index, pointed to by `AGENTS.md`                 | script + skill |
| DC-08 | Routing-map paths resolve                                                        | script         |
| DC-09 | Nested `AGENTS.md` files indexed from root                                       | script         |
| DC-10 | Nested `AGENTS.md` files have a sibling `CLAUDE.md`                              | script         |
| DC-11 | No symlinked instruction files                                                   | script         |
| DC-12 | No out-of-repository imports                                                     | script         |
| DC-13 | Copilot adapter only if Copilot selected, ≤ 30 lines, references `AGENTS.md`     | script + skill |
| DC-14 | No redundant `.cursorrules`/`GEMINI.md`/`.kiro/steering`/`.windsurfrules` (warn) | skill          |
| DC-15 | `AGENTS.md` names the complete local gate                                        | skill          |
| DC-16 | Every gate command appears in a PR CI workflow (warn)                            | skill          |
| DC-17 | Check installed and run by a PR workflow job named `agent guidance`              | skill          |
| DC-18 | Provenance marker present (warn)                                                 | skill          |
| DC-19 | No Nightgauge workspace block (`--workspace-block forbidden`)                    | script         |
| DC-20 | No committed managed-steering block                                              | skill          |
| DC-21 | No `AGENTS.md`/`CLAUDE.md` at a multi-repository workspace root (warn)           | skill          |
| DC-22 | No doc claims to be canonical for a topic routed elsewhere (warn)                | skill          |

The report ends with the fix for each `FAIL`: an `OLD`-model finding points to
`/smart-setup` (which proposes a migration), a `NEW`-model gap to the additive
step that closes it.

---

## Source

This skill implements the agent-instruction architecture defined in
Nightgauge's
[docs/AGENT_GUIDANCE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/AGENT_GUIDANCE.md).
