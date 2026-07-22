---
name: smart-setup
description: Make any repository AI-ready with AGENTS.md, CLAUDE.md, and focused
  documentation. Use when setting up a new project or when a repository is
  missing AI configuration files.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "4.7.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---

# Smart Setup

> Make any repository AI-ready

## Description

This skill analyzes your repository and creates **minimal, focused
documentation** optimized for both humans and AI coding agents. It uses a
**tiered approach** to avoid bloating repositories with unnecessary files.

## Invocation

| Tool           | Command                     |
| -------------- | --------------------------- |
| Claude Code    | `/smart-setup` (via plugin) |
| OpenAI Codex   | `$smart-setup`              |
| GitHub Copilot | Invoke via Agent Skills     |
| Cursor         | Invoke via Agent Skills     |

## Philosophy

- **Keep it minimal** — Only create what teams actually need; avoid bloating
  repositories
- **Document what IS** — Current practices, patterns, and conventions
- **Note what SHOULD BE** — Flag deviations from best practices for team review
- **Leave room for WHY** — Mark sections requiring human input (tribal knowledge
  AI can't infer)
- **Don't bloat repositories** — Skip files for tools the team doesn't use
- **Single source of truth** — Create docs/ files first, then have AI configs
  reference them (see
  [docs/ARCHITECTURE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/ARCHITECTURE.md#documentation-architecture-philosophy))

Use this marker for information only humans can provide:

```
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
# Run with default settings
/smart-setup

# Specify configuration level
/smart-setup --tier essential
/smart-setup --tier standard
/smart-setup --tier advanced
```

The `$ARGUMENTS` variable contains everything after the skill name.

## IMPORTANT: Tiered Approach

This setup uses a **tiered approach** to avoid creating unnecessary files:

### Tier 1: Essential (Default)

These are the minimum files every AI-ready repository needs:

**AI Configuration Files (based on tool selection):**

| Tools Selected                        | Create AGENTS.md   | Create CLAUDE.md             |
| ------------------------------------- | ------------------ | ---------------------------- |
| Claude Code only                      | ❌ No (not needed) | ✅ Yes                       |
| GitHub Copilot, Cursor, or Kiro (any) | ✅ Yes             | ❌ No (unless also selected) |
| Claude Code + any other tool          | ✅ Yes             | ✅ Yes                       |

> **Why conditional?** Claude Code uses `CLAUDE.md` exclusively and does not
> read `AGENTS.md`. Creating both when only Claude Code is selected adds
> unnecessary files. Conversely, GitHub Copilot, Cursor, and Kiro all read
> `AGENTS.md`.

**Documentation Files (always created):**

- `docs/README.md` - Documentation index
- `docs/ARCHITECTURE.md` - System architecture
- `docs/CODE_STANDARDS.md` - Coding conventions
- `docs/GIT_WORKFLOW.md` - Git/version control workflow (or equivalent for TFS,
  etc.)
- `docs/SECURITY_AND_ERROR_HANDLING.md` - Security guidelines and error handling

> **Note**: Tier 1 files are created regardless of whether user selects
> Essential, Standard, or Advanced. Higher tiers ADD to Tier 1, not replace it.

### Tier 2: Tool-Specific (Based on User's Tools)

**Only create these if the user uses that specific tool:**

- `.cursor/rules/` - Only if using Cursor IDE
- `.kiro/steering/` - Only if using Kiro IDE

> **Note**: GitHub Copilot now reads `AGENTS.md` directly, so
> `.github/copilot-instructions.md` is no longer required.

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

- `AGENTS.md` — Universal AI config (current standard)
- `CLAUDE.md` — Claude Code memory
- `.github/copilot-instructions.md` — **Legacy** (content should migrate to
  AGENTS.md)
- `.cursor/rules/` — Cursor IDE config
- `.kiro/steering/` — Kiro IDE config

**Documentation:** Check for docs/ directory and its contents.

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
          "description": "Anthropic's CLI and VS Code agent"
        },
        {
          "label": "GitHub Copilot",
          "description": "Uses AGENTS.md directly (no extra config needed)"
        },
        {
          "label": "Cursor IDE",
          "description": "AI-first code editor (needs .cursor/rules/)"
        },
        {
          "label": "Kiro IDE",
          "description": "AWS-backed agent (needs .kiro/steering/)"
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
          "description": "Core files only: AGENTS.md, CLAUDE.md, basic docs"
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

## Project Context Questions

[Record questions asked and user's answers here]

## Files Created/Modified

- [ ] File 1 - status
- [ ] File 2 - status
```

---

### Phase 1: Scan & Report

Scan ONLY files relevant to user's selections. Report findings clearly:

```
Based on your selections (Claude Code, Essential config, Git):

✅ AGENTS.md - exists (127 lines)
❌ CLAUDE.md - missing
✅ docs/ - exists with 5 files
   ├── README.md ✅
   ├── ARCHITECTURE.md ✅
   ├── CODE_STANDARDS.md ✅
   ├── GIT_WORKFLOW.md ✅
   └── SECURITY_AND_ERROR_HANDLING.md ❌ missing

⚠️ Legacy files found (will integrate into modern config):
   └── .github/copilot-instructions.md (67 lines) → content will merge into AGENTS.md

Your repository is mostly AI-ready! Only 2 files need attention.
```

**DO NOT report on tool-specific files the user didn't select.**

**Nightgauge Knowledge Base reporting**: When
`HAS_NIGHTGAUGE_CONFIG=true`:

- If `KNOWLEDGE_DIR_EXISTS=true`: Report
  `✅ Knowledge base active (.nightgauge/knowledge/ — {KNOWLEDGE_ENTRY_COUNT} issue entries)`
  — the Knowledge Base row will be added to the Documentation Map and
  `## Knowledge Base` section to AGENTS.md.
- If `KNOWLEDGE_DIR_EXISTS=false`: Report the following recommendation:

```
ℹ️  Knowledge base not enabled. To activate, add to .nightgauge/config.yaml:
    knowledge:
      enabled: true
      auto_scaffold: true
    This scaffolds PRD.md and decisions.md for each issue as it enters the pipeline.
    See: docs/KNOWLEDGE_BASE.md
```

**Legacy File Handling:** If `.github/copilot-instructions.md` exists, its
valuable content will be integrated into `AGENTS.md` during generation. The
legacy file can then be removed or kept as a reference.

---

### Phase 2: Context Questions About Files

**For existing files**, ask how to handle:

- "CLAUDE.md exists (89 lines). Keep as-is, review and suggest improvements, or
  replace?"

**For missing files**, ask if they want them created:

- "docs/SECURITY_AND_ERROR_HANDLING.md is missing. Create it?"

---

### Phase 3: Analyze Codebase & Ask Project Questions

1. **Analyze codebase thoroughly:**
   - Primary language (check file extensions, config files)
   - Framework (check imports, dependencies)
   - Build tools & package managers
   - Testing frameworks
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

**CRITICAL: Generation Order**

Generate files in this order so later files can reference earlier ones:

1. **First**: `docs/GIT_WORKFLOW.md` (or TFS) — using content from
   [Git Workflow Rules](#git-workflow-rules)
2. **Second**: `docs/SECURITY_AND_ERROR_HANDLING.md` — using content from
   [Security Rules](#security-rules)
3. **Then**: Other docs/ files
4. **Then**: `AGENTS.md` — **only if** GitHub Copilot, Cursor, or Kiro was
   selected (references the docs/ files)
5. **Then**: `CLAUDE.md` — **only if** Claude Code was selected (references
   docs/ files directly if AGENTS.md was skipped)
6. **Finally**: Tool-specific files (Tier 2) — reference docs/ files

> **Note**: If only Claude Code is selected, skip step 4 (AGENTS.md). The
> CLAUDE.md will reference docs/ files directly instead of referencing
> AGENTS.md.

**CRITICAL RULES:**

1. **Use REAL code examples only** — Every example MUST come from actual files
   in this repository
2. **Never overwrite without permission** — Always ask before modifying existing
   files
3. **Mark unknowns clearly** — `[TEAM TO DOCUMENT: Why was PostgreSQL chosen?]`
4. **Respect tier selection** — Don't create files for unselected tools

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

### docs/GIT_WORKFLOW.md Template

```markdown
# Git Workflow

## Critical Rules

**These rules are MANDATORY for ALL contributors, including AI assistants:**

| Rule                              | Description                                                            |
| --------------------------------- | ---------------------------------------------------------------------- |
| **NEVER push directly to `main`** | All changes must go through feature branches                           |
| **ALWAYS use feature branches**   | Create a branch for every change                                       |
| **ALWAYS create pull requests**   | Every merge to main requires a reviewed PR                             |
| **Follow branch naming**          | Use prefixes: `feat/`, `fix/`, `docs/`, `refactor/`, `test/`, `chore/` |

## Branch Naming

- `feat/TICKET-123-description` - New features
- `fix/TICKET-123-description` - Bug fixes
- `docs/TICKET-123-description` - Documentation
- `refactor/TICKET-123-description` - Refactoring
- `test/TICKET-123-description` - Test changes
- `chore/TICKET-123-description` - Maintenance

## Commit Message Format
```

[TYPE][TICKET-ID] Short summary (50 chars or less)

Detailed explanation if necessary.

Refs: TICKET-ID

```

Types: `[FEAT]`, `[FIX]`, `[DOCS]`, `[STYLE]`, `[REFACTOR]`, `[TEST]`, `[CHORE]`

## Pull Request Process

1. Create feature branch from `main`
2. Make changes with meaningful commits
3. Push branch and create PR
4. Request review from team member
5. Address feedback
6. Merge after approval (squash or merge per team preference)
```

---

### docs/SECURITY_AND_ERROR_HANDLING.md Template

```markdown
# Security and Error Handling

## Security Rules (CRITICAL)

| Rule                                 | Description                                    |
| ------------------------------------ | ---------------------------------------------- |
| **NEVER hardcode secrets**           | Use environment variables or secret management |
| **ALWAYS validate input**            | Sanitize at system boundaries                  |
| **NEVER expose sensitive data**      | Keep credentials out of logs/responses         |
| **ALWAYS use parameterized queries** | Prevent SQL injection                          |
| **NEVER commit secrets**             | Use .gitignore, pre-commit hooks               |

## Input Validation

- Validate all user input at API boundaries
- Use allowlists over denylists where possible
- Sanitize data before database operations
- Encode output appropriately (HTML, URL, SQL)

## Error Handling

- Return generic error messages to users
- Log detailed errors server-side with context
- Include request IDs for correlation
- Never expose stack traces in production

## Logging Guidelines

**Log these:**

- Authentication attempts (success and failure)
- Authorization failures
- Input validation failures
- System errors

**Never log:**

- Passwords or tokens
- Full credit card numbers
- Personal identifiable information (PII)
- Session tokens or API keys
```

---

### AGENTS.md Template

```markdown
# [Project Name]

## Overview

[Brief description based on README and code analysis]

## Technology Stack

- **Language**: [detected language and version]
- **Framework**: [detected framework and version]
- **Build Tool**: [actual tool from config files]
- **Testing**: [frameworks found in test files]

## Project Structure
```

[project-root]/ ├── [dir1]/ # [actual purpose based on contents] ├── [dir2]/ #
[actual purpose based on contents] └── [etc.]

````

<!-- Include the following section only when KNOWLEDGE_DIR_EXISTS=true -->

## Knowledge Base

Issue-specific context files (PRDs and decision logs) are stored under
`.nightgauge/knowledge/`. Structure:

- `epics/{N}-{slug}/PRD.md` — Epic product requirements
- `epics/{N}-{slug}/decisions.md` — Epic architectural decisions
- `features/{N}-{slug}/PRD.md` — Feature requirements
- `features/{N}-{slug}/decisions.md` — Feature decisions

See `docs/KNOWLEDGE_BASE.md` for schema and usage.

<!-- End conditional Knowledge Base section -->

## Quick Start
```bash
[Real commands from package.json/Makefile/csproj/etc.]
````

## Development Guidelines

### Coding Conventions

[Document ACTUAL patterns found - include real code snippets with file
references]

**Example from `[actual-file-path]`:**

```[language]
[actual code from repository]
```

### Testing Patterns

[Describe actual testing approach with examples from test files]

## Git Workflow (CRITICAL)

**See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md)** for complete workflow.

Key rules:

1. NEVER push directly to `main` - Always use feature branches
2. ALWAYS create pull requests - Even for small changes
3. Follow branch naming - `feat/`, `fix/`, `docs/`, `refactor/`

## Security (CRITICAL)

**See
[docs/SECURITY_AND_ERROR_HANDLING.md](docs/SECURITY_AND_ERROR_HANDLING.md)**

Key rules:

1. NEVER hardcode secrets - Use environment variables
2. ALWAYS validate input - Sanitize at system boundaries
3. ALWAYS use parameterized queries - Prevent SQL injection

````

---

### CLAUDE.md Template

**CRITICAL: CLAUDE.md must be concise.** A bloated CLAUDE.md causes Claude to ignore important instructions. Follow this principle from official Claude Code best practices:

> "For each line, ask: Would removing this cause Claude to make mistakes? If not, cut it."

**Quality Guidelines:**

| Metric | Good | Warning | Concern |
| ------ | ---- | ------- | ------- |
| Line count | < 100 | 100-200 | > 200 |
| Self-evident instructions | None | Few | Many |
| Discoverable information | None | Some | Lots |

**What to INCLUDE:**

- Bash commands Claude can't guess (non-standard test commands, build steps)
- Code style that deviates from language standards
- Critical safety rules (don't delete production data, don't push to main)
- Non-obvious project conventions
- References to detailed docs (not the content itself)

**What to EXCLUDE:**

- File-by-file directory listings (Claude can explore)
- Self-evident instructions ("write clean code", "use meaningful names")
- Standard language conventions (Claude already knows TypeScript best practices)
- Information Claude can discover by reading files

```markdown
# [Project Name] - Claude Code Configuration

## Quick Reference
- **Test**: `[non-standard test command]`
- **Build**: `[build command if non-obvious]`
- **Lint**: `[lint command]`

## Critical Rules
1. [Rule Claude CANNOT infer from code - e.g., "Never push directly to main"]
2. [Project-specific constraint - e.g., "All API responses must include request_id"]
3. [Safety rule - e.g., "Require confirmation before database migrations"]

## Key Documentation
- **Architecture**: See @docs/ARCHITECTURE.md
- **Git Workflow**: See @docs/GIT_WORKFLOW.md
- **Security**: See @docs/SECURITY_AND_ERROR_HANDLING.md
- **Standards**: See @docs/CODE_STANDARDS.md

## Documentation Map

> This map helps AI agents find relevant documentation based on the task at
> hand. Keywords are matched against issue content to prioritize which docs to
> read.

| Topic        | Primary Docs                   | Keywords                                        |
| ------------ | ------------------------------ | ----------------------------------------------- |
| Architecture | docs/ARCHITECTURE.md           | architecture, design, components, structure     |
| Git          | docs/GIT_WORKFLOW.md           | git, branch, commit, merge, pull, request, pr   |
| Security     | docs/SECURITY_AND_ERROR_HANDLING.md | security, validation, secrets, auth, input |
| Testing      | docs/TESTING.md                | test, coverage, unit, integration, e2e          |
| Standards    | docs/CODE_STANDARDS.md         | naming, style, format, convention, standard     |
<!-- Add the following row only when KNOWLEDGE_DIR_EXISTS=true -->
| Knowledge Base | .nightgauge/knowledge/ | knowledge, prd, decision, adr, wiki, reference, scaffold |

## [Project-Specific Section Only If Needed]
[Only include if there's something non-obvious Claude needs to know]
````

**Anti-patterns to AVOID in CLAUDE.md:**

```markdown
# BAD - Too verbose, self-evident

## Directory Structure

- src/ - Source code
- tests/ - Test files
- docs/ - Documentation

## Coding Standards

- Use meaningful variable names
- Write clean, readable code
- Follow TypeScript best practices
```

---

### Tool-Specific Templates (Tier 2)

**Create ONLY if user selected that tool.**

> **Note**: GitHub Copilot now reads `AGENTS.md` directly — no separate file
> needed.

#### .cursor/rules/project-rules.mdc

```markdown
---
description: Project-specific coding standards
globs: ["**/*"]
alwaysApply: true
---

# [Project Name] Cursor Rules

## Language: [detected] | Framework: [detected]

## Coding Conventions

[Key patterns from CODE_STANDARDS.md]

## Git Workflow

**See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md)**

## Security

**See
[docs/SECURITY_AND_ERROR_HANDLING.md](docs/SECURITY_AND_ERROR_HANDLING.md)**

## References

See `AGENTS.md` for comprehensive guidelines.
```

#### .kiro/steering/project-standards.md

```markdown
# [Project Name] Standards

## Overview

[Brief project description]

## Technology Stack

- **Language**: [detected]
- **Framework**: [detected]

## Key Constraints

[Important rules and patterns]

## Git Workflow

**See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md)**

## Security

**See
[docs/SECURITY_AND_ERROR_HANDLING.md](docs/SECURITY_AND_ERROR_HANDLING.md)**

## References

See `AGENTS.md` for comprehensive guidelines.
```

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

After creating files, verify:

### Tier 1 - AI Config Files (Based on Tool Selection)

- [ ] `AGENTS.md` created **only if** GitHub Copilot, Cursor, or Kiro was
      selected
- [ ] `AGENTS.md` **skipped** if only Claude Code was selected
- [ ] `CLAUDE.md` created if Claude Code was selected (or unless user explicitly
      declined)

### Tier 1 - Documentation Files (Always)

- [ ] `docs/README.md` serves as documentation index
- [ ] `docs/ARCHITECTURE.md` describes system structure
- [ ] `docs/CODE_STANDARDS.md` reflects actual codebase patterns
- [ ] `docs/GIT_WORKFLOW.md` or `docs/TFS_WORKFLOW.md` exists
- [ ] `docs/SECURITY_AND_ERROR_HANDLING.md` exists

### Tier 2 (Only If Selected)

- [ ] `.cursor/rules/` — only if Cursor IDE selected
- [ ] `.kiro/steering/` — only if Kiro IDE selected

> GitHub Copilot uses `AGENTS.md` directly — no separate file needed.

### Quality Checks

- [ ] All examples from THIS repository (no generic/placeholder code)
- [ ] All `[TEAM TO DOCUMENT]` markers are specific questions
- [ ] **CLAUDE.md is concise** (< 100 lines ideal, flag if > 200)
- [ ] **CLAUDE.md contains no self-evident instructions** (no "write clean
      code")
- [ ] **CLAUDE.md contains no discoverable information** (no file-by-file
      listings)
- [ ] **CLAUDE.md references docs/ files** instead of duplicating content
- [ ] No files created for unselected tools
- [ ] User's tier selection was respected
- [ ] No empty/stub files that provide no value
- [ ] Git/Security rules reference docs/ files (not duplicated in full)

---

## Existing Files Policy (NON-DESTRUCTIVE)

When existing documentation is found:

1. **READ existing file first** — Understand what's documented
2. **NEVER overwrite** — Do not replace existing content
3. **IDENTIFY gaps** — Compare against template to find missing sections
4. **OFFER additions** — Suggest specific sections to ADD
5. **ASK permission** — Before ANY modification:
   > "AGENTS.md exists (127 lines). I found these gaps: [list]. Add them?"

### Legacy File Integration

When legacy configuration files are found, **integrate their valuable content**
into modern files:

| Legacy File                       | Integrate Into  | Action                                                           |
| --------------------------------- | --------------- | ---------------------------------------------------------------- |
| `.github/copilot-instructions.md` | `AGENTS.md`     | Extract project-specific rules, coding standards, and guidelines |
| Existing `CLAUDE.md`              | New `CLAUDE.md` | Preserve all existing content, add missing sections              |
| Existing `AGENTS.md`              | New `AGENTS.md` | Preserve all existing content, add missing sections              |

**Integration Process:**

1. **Read the legacy file** — Extract valuable project-specific content
2. **Identify unique content** — Find rules, patterns, or guidelines not in
   templates
3. **Merge intelligently** — Incorporate unique content into the appropriate
   sections of the new file
4. **Preserve team customizations** — Never discard project-specific rules the
   team has defined
5. **Offer cleanup** — After successful integration, ask:
   > "I've integrated content from `.github/copilot-instructions.md` into
   > `AGENTS.md`. Would you like to delete the legacy file or keep it as a
   > reference?"

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
SELECTED_LIST=$(echo "$USER_MULTISELECT" | jq -r '
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
  enabled: true
  warn_only: false
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

1. **Review generated files** for accuracy
2. **Generate TODO file** for `[TEAM TO DOCUMENT]` markers (see below)
3. **Commit documentation** to repository

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

> "Setup complete! I created AI_SETUP_TODO.md with X items that need team input.
> Would you like me to delete AI_SMART_SETUP.md? It's no longer needed."

If answer log was created, also ask about its deletion (remind about sensitive
info).

---

## Source

This skill follows the
[AI_SMART_SETUP.md](https://github.com/nightgauge/nightgauge/blob/main/AI_SMART_SETUP.md)
methodology from Nightgauge.
