---
name: update-docs
description: Verify and update documentation to match current codebase - detect drift,
  deprecated references, and inconsistencies. Use when documentation may be
  stale or after significant codebase changes.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.7.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
---

# Update Docs

> Verify and update documentation to match current codebase

## Description

This skill proactively verifies that documentation accurately reflects the
current codebase architecture and implementation. It detects documentation
drift, deprecated references, and inconsistencies between docs and code.

## Invocation

| Tool           | Command                     |
| -------------- | --------------------------- |
| Claude Code    | `/update-docs` (via plugin) |
| OpenAI Codex   | `$update-docs`              |
| GitHub Copilot | Invoke via Agent Skills     |
| Cursor         | Invoke via Agent Skills     |

## Options

| Option               | Description                                                                                             |
| -------------------- | ------------------------------------------------------------------------------------------------------- |
| `--audit-only`       | Only report discrepancies, don't make any changes                                                       |
| `--fix-all`          | Automatically fix all detected issues (broken links, stale dates, version mismatches) without prompting |
| `--scope <path>`     | Limit the audit to a specific directory                                                                 |
| `--check-deprecated` | Focus on detecting deprecated terms and patterns                                                        |

## Arguments

This skill supports inline arguments via `$ARGUMENTS`:

```bash
# Audit a specific path
/update-docs docs/

# Audit with options
/update-docs --audit-only

# Combined
/update-docs docs/ --audit-only
```

The `$ARGUMENTS` variable contains everything after the skill name.

## What It Does

### Phase 0: Repository Selection

When multiple repositories are detected in the workspace, prompt the user to
select which repository to analyze.

1. **Detect Available Repositories** - Scan workspace for git repositories
2. **Present Selection** - Show list of repositories with their paths
3. **Confirm Selection** - Display which repository will be analyzed before
   proceeding
4. **Output Clarity** - All subsequent output clearly indicates the target
   repository

**Why This Matters:**

- Multi-repo workspaces are common (monorepos, related projects, framework +
  plugins)
- Users should always know which repository is being modified
- Prevents accidental changes to the wrong codebase

**Selection Prompt Example:**

```text
AI: I detected multiple repositories in your workspace:

1. acme/nightgauge (/Users/name/repos/acme/nightgauge)
2. nightgauge (/Users/name/repos/nightgauge)
3. my-project (/Users/name/repos/my-project)

Which repository would you like me to analyze?
```

**Single Repository Behavior:** If only one repository is detected, proceed
directly to Phase 1 but still clearly state which repository is being analyzed.

**Multi-Repository Command Execution (CRITICAL):**

When analyzing multiple repositories, bash commands can silently run in the
wrong directory. Always use explicit directory context:

```bash
# WRONG - assumes current directory, may be wrong after previous commands
grep -rn "pattern" --include="*.md" .

# CORRECT - explicit absolute path
grep -rn "pattern" --include="*.md" /path/to/repo

# CORRECT - cd with verification in subshell (doesn't pollute state)
(cd /path/to/repo && pwd && grep -rn "pattern" --include="*.md" .)

# CORRECT - cd with && chain (fails fast if cd fails)
cd /path/to/repo && grep -rn "pattern" --include="*.md" .
```

**Best Practice:** After switching repositories, always run `pwd` to verify
you're in the expected directory before executing commands.

### Phase 1: Establish Ground Truth from Code

Before checking any documentation, determine the actual state of the codebase.

1. **Project Structure Discovery** - Find package managers, project files,
   monorepo patterns
2. **Service Architecture Discovery** - Identify Docker services, Kubernetes
   deployments, CI/CD workflows
3. **API and Configuration Discovery** - Find URLs, endpoints, environment
   configs
4. **Technology Stack Discovery** - Determine actual technologies in use from
   dependencies

### Phase 2: Detect Deprecated References

Search for terms that may indicate stale documentation.

1. **Check for Deprecated Terms File** - Look for `.deprecated-terms.yaml`
2. **Auto-Detect Deprecated Patterns** - Find archived directories, git history
   renames, deprecation TODOs
3. **Auto-Detect Code/Doc Mismatches** - Compare documentation claims against
   actual code

### Phase 3: Verify Documentation Against Code

For each documentation file, verify claims against actual code.

1. **Documentation Inventory** - Find all markdown files
2. **Cross-Reference Verification** - Verify repository counts, service lists,
   tech stack, API endpoints
3. **Automated Verification** - Check directory structure claims, tech stack
   claims, submodule claims

### Phase 4: Cross-Reference Validation

Check that different docs agree with each other.

1. **URL Consistency** - Compare all URLs mentioned across docs
2. **Naming Consistency** - Check service/component naming consistency
3. **Version and Date Consistency** - Find stale versions or dates

### Phase 4.6: Version Consistency Validation

**DETERMINISTIC**: Call `nightgauge docs version-consistency` to validate
version numbers across all related files.

```bash
# Validate version consistency
nightgauge docs version-consistency --root . --json | jq '.'

# List mismatches only
nightgauge docs version-consistency --root . --json | \
  jq '.mismatches[] | "\(.file):\(.line) expected \(.expected_version) got \(.found_version)"'
```

If mismatches are detected (exit code 1), update the documentation to match the
source-of-truth version. The command auto-detects project type (Node.js, Python,
Go, Rust, .NET, AI Agent Skills) from marker files in the root directory.

### Phase 4.8: Updated Date Staleness Detection

**DETERMINISTIC**: Call `nightgauge docs check-freshness` to detect files
with stale "Updated" metadata.

```bash
# Check for stale Updated: dates
nightgauge docs check-freshness --root . --json | jq '.'

# List stale files only
nightgauge docs check-freshness --root . --json | \
  jq '.stale_findings[] | "\(.file):\(.line) (\(.days_stale) days stale)"'
```

If stale dates are found (exit code 1), update the "Updated: YYYY-MM-DD"
metadata in those files to the current date. The command detects the patterns
`Updated: YYYY-MM-DD`, `**Updated**: YYYY-MM-DD`, and `| Updated | YYYY-MM-DD |`
(case-insensitive) across all markdown files, skipping code fences.

### Phase 4.5: Link Validation (EXHAUSTIVE)

**CRITICAL:** Link validation MUST be exhaustive and systematic. Ad-hoc checking
misses links and produces inconsistent results across runs.

**The Exhaustive Approach:**

1. **Extract ALL Links First** - Build complete inventory before any validation
2. **Validate EACH Link** - No sampling, no shortcuts, check every single link
3. **Track Progress** - Know exactly how many checked vs total
4. **Verify Completion** - Re-run extraction after fixes to confirm zero issues

**Link Extraction (Use Simple, Reliable Patterns):**

```bash
# RELIABLE pattern - use this, not complex escaping
grep -rn '\[[^]]*\]([^)]*)' --include="*.md" . | grep -v 'https\?://'
```

**Context-Aware Resolution:**

- Resolve paths relative to the file containing the link (not repository root)
- A link in `guides/setup/INSTALL.md` to `../README.md` resolves to
  `guides/README.md`

**Link Validation Rules:**

| Link Context                                     | Action                     |
| ------------------------------------------------ | -------------------------- |
| Outside code blocks                              | Validate file exists       |
| Inside ` ```markdown ` blocks                    | Skip - these are templates |
| Inside ` ```text ` blocks showing file structure | Skip - these are diagrams  |
| In skill/command files showing output examples   | Skip - target repo context |

**Common Issues to Detect:**

| Issue                     | Example                               | Solution                         |
| ------------------------- | ------------------------------------- | -------------------------------- |
| Wrong relative path depth | `./file.md` should be `../../file.md` | Count directory levels carefully |
| Directory renamed         | `plugins/` is now `claude-plugins/`   | Update all references            |
| File moved                | `GUIDE.md` moved to `guides/GUIDE.md` | Update path in all linking files |

**Relative Path Depth Validation:**

A common error is using insufficient `../` prefixes. For example, a file at
`guides/agent-setup/FEATURES.md` linking to `.github/workflows/file.yml` needs
`../../.github/workflows/file.yml`.

```bash
# Validate relative path depth for each link
validate_link_depth() {
  SOURCE_FILE="$1"
  LINK="$2"

  # Count directory depth of source file (number of / minus 1 for filename)
  SOURCE_DEPTH=$(echo "$SOURCE_FILE" | tr -cd '/' | wc -c)

  # Count ../ prefixes in link
  LINK_UP_COUNT=$(echo "$LINK" | grep -o '\.\.\/' | wc -l)

  # If link references root-level file but doesn't have enough ../
  if [[ "$LINK" == *".github/"* ]] || [[ "$LINK" == *"docs/"* ]]; then
    NEEDED=$((SOURCE_DEPTH - 1))  # -1 because source includes filename
    if [ "$LINK_UP_COUNT" -lt "$NEEDED" ] && [[ "$LINK" != /* ]]; then
      echo "WARNING: $SOURCE_FILE:$LINK may need $NEEDED '../' prefixes (has $LINK_UP_COUNT)"
    fi
  fi
}
```

**Quick depth check heuristic:**

- File at `./README.md` → links need `./` prefix (depth 0)
- File at `docs/FILE.md` → links to root need `../` (depth 1)
- File at `guides/setup/FILE.md` → links to root need `../../` (depth 2)
- File at `a/b/c/FILE.md` → links to root need `../../../` (depth 3)

**Common False Positives to Avoid:**

- Links in template files (skills, commands) that reference files the template
  _will create_ in target repos
- Directory structure diagrams showing planned/example layouts
- Code examples demonstrating documentation patterns

**Code Block Detection (MANDATORY for Avoiding False Positives):**

**CRITICAL:** Simple grep CANNOT distinguish links inside vs outside code
blocks. You MUST use code-aware filtering to avoid false positives.

**Required Approach - AWK-based Code Block Filtering:**

````bash
# Extract links ONLY from outside fenced code blocks
# This awk script tracks code block state and filters correctly
find . -name "*.md" -not -path "*/node_modules/*" | while read -r file; do
  awk '
    /^```/ { in_code = !in_code; next }
    !in_code && /\[[^\]]*\]\([^)]+\)/ { print FILENAME":"NR":"$0 }
  ' "$file"
done
````

**Why This Is Mandatory:**

Without code block awareness, you WILL get false positives from:

- **Template files** - Skills showing what will be created in target repos
  (e.g., smart-setup SKILL.md listing `docs/SECURITY_AND_ERROR_HANDLING.md`)
- **Educational examples** - Documentation demonstrating correct vs incorrect
  patterns
- **Code samples** - Tutorials showing link syntax

**Alternative Approach (Python):**

If you prefer Python for validation scripts:

````python
import re
# Remove fenced code blocks before extracting links
content_no_code = re.sub(r'```[\s\S]*?```', '', file_content)
# Then extract and validate links from content_no_code
````

**DO NOT use simple grep without filtering:**

```bash
# ❌ WRONG - Will produce false positives
grep -rn '\[[^]]*\]([^)]*)' --include="*.md" . | grep -v 'https\?://'
```

**Additional Filtering for Skill/Command Files:**

Even with code block filtering, you may want to exclude skill and command files
entirely from link validation, as they contain template content:

```bash
# Exclude skill and command files from validation
find . -name "*.md" \
  -not -path "*/skills/*/SKILL.md" \
  -not -path "*/claude-plugins/*/commands/*" \
  -not -path "*/node_modules/*" | while read -r file; do
  # ... validation logic
done
```

### Phase 4.7: Duplication Detection

Detect when content is duplicated across multiple files instead of using
references to a single source of truth.

**Why This Matters:**

- **Single Source of Truth Principle**: Information should be defined once and
  referenced elsewhere
- **Maintenance Burden**: Duplicated content requires updating multiple files
  when changes occur
- **Drift Risk**: Duplicates inevitably diverge over time, creating
  inconsistencies
- **AI Confusion**: AI assistants may receive conflicting guidance from
  duplicate sources

**What to Detect:**

1. **Rule/Guideline Duplication** - Same rules defined in multiple files (e.g.,
   git workflow rules in both CLAUDE.md and docs/GIT_WORKFLOW.md)
2. **Code Standards Duplication** - Coding conventions duplicated instead of
   referenced
3. **Process Documentation Duplication** - Same process documented in multiple
   places
4. **Configuration Duplication** - Same settings/configs defined in multiple
   files

**Detection Strategy:**

```bash
# Find potential duplicates by looking for similar section headers
grep -rn "## Git Workflow\|## Security\|## Code Standards" --include="*.md" . | \
  grep -v node_modules | sort

# Check for files that should reference docs/ but define their own content
# Look for AI config files (CLAUDE.md, AGENTS.md) with substantial content
# that duplicates docs/ files
```

**Duplication Patterns to Flag:**

| Pattern                    | Example                              | Recommended Fix                           |
| -------------------------- | ------------------------------------ | ----------------------------------------- |
| Rules listed in AI config  | CLAUDE.md lists 5 git rules          | Reference `@docs/GIT_WORKFLOW.md` instead |
| Checklists duplicated      | Security checklist in multiple files | Define once in docs/, reference elsewhere |
| Commands/examples repeated | Same bash examples in multiple docs  | Create single reference, link to it       |
| Version info duplicated    | Version listed in multiple files     | Single source of truth for version        |

**What Belongs Where:**

| Content Type         | Belongs In                            | Other Files Should            |
| -------------------- | ------------------------------------- | ----------------------------- |
| Team-wide workflows  | `docs/` folder                        | Reference with links          |
| Coding standards     | `docs/CODE_STANDARDS.md`              | Reference, not duplicate      |
| Security guidelines  | `docs/SECURITY_AND_ERROR_HANDLING.md` | Reference, not duplicate      |
| AI-specific behavior | `CLAUDE.md`, `AGENTS.md`              | Define only AI-specific rules |

**Output Format:**

```text
### Duplication Issues (MEDIUM)
1. `CLAUDE.md:45-67` duplicates content from `docs/GIT_WORKFLOW.md:12-34`
   - CLAUDE.md defines 5 git rules that exist in GIT_WORKFLOW.md
   - Recommendation: Replace with reference: "See @docs/GIT_WORKFLOW.md"

2. `AGENTS.md:89-112` duplicates `docs/SECURITY_AND_ERROR_HANDLING.md:15-45`
   - Security checklist repeated instead of referenced
   - Recommendation: Reference the authoritative source
```

### Phase 4.8: CLAUDE.md Quality Audit

**CRITICAL:** A bloated CLAUDE.md causes Claude to ignore important
instructions. This phase detects CLAUDE.md files that violate official best
practices.

**The Core Principle (from Claude Code Best Practices):**

> "For each line, ask: Would removing this cause Claude to make mistakes? If
> not, cut it."

**Quality Metrics:**

| Metric                    | Good  | Warning | Flag      |
| ------------------------- | ----- | ------- | --------- |
| Line count                | < 100 | 100-200 | > 200     |
| File-by-file descriptions | None  | Some    | Extensive |
| Self-evident instructions | None  | Few     | Many      |
| Discoverable information  | None  | Some    | Lots      |

**What to Flag:**

1. **File-by-File Directory Listings** - Exhaustive lists like:

   ```markdown
   # BAD - Claude can discover this by reading code

   .claude/ ├── instructions/ │ ├── code-review.md │ ├── debugging.md │ └──
   testing.md
   ```

2. **Self-Evident Instructions** - Things Claude would do anyway:
   - "Write clean, readable code"
   - "Review code before merging"
   - "Follow best practices"
   - "Document your code"

3. **Standard Language Conventions** - Unless project deviates:
   - "Use PascalCase for classes" (standard in most languages)
   - "Use camelCase for functions" (standard convention)
   - "Add comments to complex code" (universal practice)

4. **Information Claude Can Discover** - By reading code:
   - Tech stack (visible in package.json, requirements.txt)
   - Project structure (visible from file system)
   - Dependencies (visible in manifest files)

**What SHOULD Be in CLAUDE.md:**

| Keep                                | Why                                               |
| ----------------------------------- | ------------------------------------------------- |
| Bash commands Claude can't guess    | `npm run test:integration` vs `npm test`          |
| Code style deviating from standards | "We use snake_case in JavaScript"                 |
| Repository-specific workflow        | "Always run /update-docs before commits"          |
| Critical safety rules               | "NEVER push to main directly"                     |
| Non-obvious conventions             | "API responses use camelCase, DB uses snake_case" |

**Detection Commands:**

```bash
# Count lines in CLAUDE.md
wc -l CLAUDE.md

# Find file-by-file listings (directory tree patterns)
grep -n "├──\|└──\|│" CLAUDE.md | wc -l

# Find self-evident phrases
grep -in "clean code\|best practice\|readable\|document.*code\|review.*before" CLAUDE.md

# Find exhaustive directory descriptions
grep -n "^- \*\*\|^  - " CLAUDE.md | wc -l
```

**Output Format:**

```text
### CLAUDE.md Quality Issues (MEDIUM)

**File:** CLAUDE.md (267 lines - exceeds 200 line threshold)

**Issues Found:**
1. **Lines 45-89**: File-by-file directory listing
   - Contains 44 lines describing .claude/ directory contents
   - Recommendation: Remove - Claude can discover this by reading files

2. **Lines 112-118**: Self-evident instructions
   - "Follow best practices for code quality"
   - "Document your code appropriately"
   - Recommendation: Remove - Claude does this by default

3. **Lines 156-198**: Standard language conventions
   - Lists naming conventions that match TypeScript standards
   - Recommendation: Remove unless your project deviates

**Recommendations:**
- Target: Reduce to < 100 lines
- Keep: Git workflow rules, security rules, repo-specific commands
- Remove: Discoverable info, self-evident practices, standard conventions
```

**Why This Matters:**

- **Context window is precious**: Every unnecessary line in CLAUDE.md takes
  space from actual code
- **Instruction dilution**: Important rules get lost in noise
- **False confidence**: Long CLAUDE.md files feel thorough but reduce AI
  effectiveness
- **Official guidance**: Claude Code docs explicitly warn against bloated
  CLAUDE.md

### Phase 5: Generate Discrepancy Report

Produce a structured report with:

- Summary table of issues by category and severity
- Deprecated references found
- Code/doc mismatches
- **Broken links (with severity classification)**
- **Duplication issues (content repeated instead of referenced)**
- Missing documentation
- Inconsistencies between docs
- **Stale Dates (MANDATORY — report even if count is zero)**
- **Version Inconsistencies (MANDATORY — report even if count is zero)**
- Prioritized recommendations

#### Stale Dates Section (MANDATORY)

This section MUST always appear in the report, even when no stale dates are
detected. Use the counts from Phase 4.8.

**When issues are found:**

```text
### Stale Dates (HIGH)

| File | Line | Documented Date | Git Last Modified |
|------|------|-----------------|-------------------|
| docs/ARCHITECTURE.md | 3 | 2026-01-15 | 2026-02-10 |

**2 stale dates detected.**
```

**When no issues are found:**

```text
### Stale Dates

**0 stale dates detected.**
```

#### Version Inconsistencies Section (MANDATORY)

This section MUST always appear in the report, even when no version
inconsistencies are detected. Use the counts from Phase 4.6.

**When issues are found:**

```text
### Version Inconsistencies (HIGH)

| File | Line | Found Version | Expected (package.json) |
|------|------|---------------|-------------------------|
| docs/API.md | 12 | v1.5.0 | v1.7.0 |

**1 version inconsistency detected.**
```

**When no issues are found:**

```text
### Version Inconsistencies

**0 version inconsistencies detected.**
```

### Phase 6: Fix and Verify (MANDATORY VERIFICATION LOOP)

After generating the report, apply fixes AND verify they are complete.

1. **Apply Fixes** - Edit documentation with correct information
2. **Skip Historical Files** - Don't modify deprecated, archive, or changelog
   files
3. **MANDATORY Verification** - Re-run the SAME extraction and validation used
   in Phase 4.5, Phase 4.6, and Phase 4.8
4. **Loop Until Clean** - If verification finds issues, fix them and verify
   again
5. **Commit Workflow** - Only commit after verification passes with zero issues

**Re-validation MUST confirm zero issues in all three categories:**

- **Zero broken links** — Re-run Phase 4.5 link extraction and verify no broken
  links remain
- **Zero stale dates** — Re-run Phase 4.8 date staleness check and verify all
  "Updated" dates are current
- **Zero version inconsistencies** — Re-run Phase 4.6 version check and verify
  all version references match the source of truth

**Why the Verification Loop is Critical:**

- Fixes may introduce new broken links (e.g., fixing one path breaks another
  reference)
- Ad-hoc fixing without verification leaves issues behind
- Users expect "update-docs" to catch ALL issues, not require multiple runs
- The same systematic extraction ensures consistent results

## Files to Skip

Don't modify these types of files:

- Files with `DEPRECATED` in the name
- Files in `archive/` or `historical/` directories
- Audit reports from previous runs
- Planning documents from past releases
- Changelog entries (history should not be rewritten)

## Creating a .deprecated-terms.yaml

To use this skill effectively, create a `.deprecated-terms.yaml` file:

```yaml
# .deprecated-terms.yaml
deprecated:
  - term: "old-service-name"
    replacement: "new-service-name"
    context: "Renamed in v2.0 release"

  - term: "api.old-domain.com"
    replacement: "api.new-domain.com"
    context: "Domain migrated"

exceptions:
  - pattern: "CHANGELOG.md"
  - pattern: "migration-*.md"
  - pattern: "**/archive/**"

canonical_urls:
  production_api: "https://api.example.com"
  documentation: "https://docs.example.com"
```

## Example Output

### Audit Only

```text
User: /update-docs --audit-only

AI: **Documentation Audit Report**

**Repository:** my-project
**Analyzed:** 47 markdown files
**Links Validated:** 89 (skipped 23 template examples)

## Summary

| Category                | Issues | Severity |
|-------------------------|--------|----------|
| Broken Links            | 4      | High     |
| Deprecated References   | 3      | High     |
| Stale Dates             | 2      | High     |
| Version Inconsistencies | 1      | High     |
| Code/Doc Mismatches     | 5      | Medium   |
| Structure Drift         | 2      | Medium   |
| Missing Documentation   | 2      | Low      |

## Issues Found

### Broken Links (HIGH)
1. `README.md:45` - `./guides/setup.md` → file is at `./docs/guides/setup.md`
2. `docs/API.md:12` - `../shared/types.md` → should be `../types/index.md`
3. `CLAUDE.md:67` - `docs/SECURITY.md` → file doesn't exist
4. `.github/copilot-instructions.md:23` - `../plugins/` → directory is `../claude-plugins/`

### Deprecated References (HIGH)
1. `README.md:15` - References "old-service" but service was renamed
2. `docs/API.md:42` - URL "api.old-domain.com" should be updated
3. `CLAUDE.md:28` - Mentions removed component "legacy-auth"

### Stale Dates (HIGH)

| File | Line | Documented Date | Git Last Modified |
|------|------|-----------------|-------------------|
| docs/ARCHITECTURE.md | 3 | 2026-01-15 | 2026-02-10 |
| docs/API.md | 1 | 2026-01-20 | 2026-02-18 |

**2 stale dates detected.**

### Version Inconsistencies (HIGH)

| File | Line | Found Version | Expected (package.json) |
|------|------|---------------|-------------------------|
| docs/API.md | 12 | v1.5.0 | v1.7.0 |

**1 version inconsistency detected.**

### Structure Drift (MEDIUM)
1. `.github/copilot-instructions.md:17-33` - Directory diagram shows `plugins/` but actual is `claude-plugins/`
2. `docs/ARCHITECTURE.md:45` - Shows `shared/standards/` but actual is `standards/`

**Note:** 23 links inside code block templates were skipped (template examples for target repos).

Run `/update-docs` to fix these issues.
```

### Full Update

```text
User: /update-docs

AI: I'll analyze your documentation and fix any discrepancies.

**Phase 1: Analyzing codebase...**
- Detected project type and dependencies
- Found 3 services in docker-compose.yml

**Phase 2: Checking for deprecated terms...**
- Found .deprecated-terms.yaml with 5 terms defined
- 3 deprecated references detected

**Phase 3-4: Verifying documentation...**
- 8 discrepancies found

**Phase 5: Report generated**

**Phase 6: Applying fixes...**
- Updated README.md (2 changes)
- Updated docs/ARCHITECTURE.md (3 changes)
- Updated CLAUDE.md (2 changes)

**Verification passed** — Zero broken links, zero stale dates, zero version inconsistencies.

Would you like me to commit these changes?
```

## Philosophy

- **Code is truth** — Documentation must reflect what the code actually does
- **Detect drift early** — Regular audits prevent documentation rot
- **Preserve history** — Don't modify historical documents or changelogs
- **Project-specific rules** — Use `.deprecated-terms.yaml` for customization

## Lessons Learned and Known Patterns

This section documents patterns discovered from real-world usage that improve
reliability.

### Grep Patterns That Work

**DO use simple patterns:**

```bash
# Reliable link extraction
grep -rn '\[[^]]*\]([^)]*)' --include="*.md" . | grep -v 'https\?://'
```

**DON'T use overly complex escaping:**

```bash
# This often fails silently - avoid
grep -noE '\[[^\]]*\]\([^)]+\)' file.md  # Unreliable
```

### Common Failure Modes

| Failure Mode                      | Root Cause                                          | Prevention                                                         |
| --------------------------------- | --------------------------------------------------- | ------------------------------------------------------------------ |
| Missed broken links               | Ad-hoc checking instead of exhaustive extraction    | Always extract ALL links first, then validate ALL                  |
| Inconsistent results between runs | Sampling or stopping early                          | Same extraction + validation = same results                        |
| Fixes introduce new issues        | No verification after fixes                         | MANDATORY re-run validation after every fix                        |
| Wrong relative path resolution    | Resolving from repo root instead of source file     | Always resolve relative to the file containing the link            |
| False positives from templates    | Links in code blocks flagged as broken              | Use AWK/Python to filter out code block content                    |
| Version drift in README tables    | SKILL.md updated but skills/README.md not           | Add explicit SKILL.md→README.md version check                      |
| Wrong directory context           | Multi-repo bash commands run in wrong dir           | Always use absolute paths or explicit `cd` with `pwd` verification |
| Insufficient `../` prefixes       | Link uses `./` from nested directory                | Validate link depth matches source file depth                      |
| Stale "Updated" date metadata     | File modified but "Updated: YYYY-MM-DD" not changed | Compare documented date against git history                        |

### Patterns Discovered (2026-01)

#### Universal Patterns (Apply to ALL repositories)

**Pattern: Relative Path Depth Mismatch**

Files in nested directories often have links with insufficient `../` prefixes:

```markdown
<!-- File: guides/agent-setup/FEATURES.md -->
<!-- WRONG: -->

See [workflow](.github/workflows/review.yml)

<!-- CORRECT: -->

See [workflow](../../.github/workflows/review.yml)
```

**Solution:** Count source file depth and verify link prefix depth matches.

**Pattern: Code Block False Positives**

Links inside fenced code blocks (templates, examples) are flagged as broken when
they're actually intentional examples of what will be generated elsewhere.

**Solution:** Use AWK or Python to filter out links inside code blocks before
validation.

**Pattern: Stale "Updated" Date Metadata**

Files with "Updated: YYYY-MM-DD" metadata become stale when the file is modified
but the date isn't updated:

```markdown
<!-- AI_SMART_SETUP.md -->

> **Version**: 4.2.0 | **Updated**: 2026-01-22 ← File was modified on
> 2026-01-30!
```

**Solution:** Compare documented "Updated" date against git history. If file was
modified after the documented date, flag it and update the date to the current
date.

#### Project-Type-Specific Patterns

**Pattern: SKILL.md to skills/README.md Version Sync** _(Plugin repositories
only)_

In plugin repositories with a `skills/` directory, SKILL.md files contain the
authoritative version in metadata, but skills/README.md often has a version
table that drifts:

```yaml
# SKILL.md (source of truth)
metadata:
  version: "4.2.0"
```

```markdown
<!-- skills/README.md (often stale) -->

| **Version** | 4.1.0 |
```

**Solution:** Only check if `skills/` directory exists. See Phase 4.6 for
detection command.

**Pattern: package.json to README Badge Mismatch** _(Node.js projects only)_

README badges showing npm version can become stale if manually specified rather
than dynamically generated.

**Solution:** Check if README has hardcoded version badges that don't match
package.json.

## Lessons Learned and Known Patterns

This section documents patterns discovered from real-world usage that improve
reliability. Contributions welcome via PR.

### Known False Positive Patterns

#### Pattern 1: Template Content in Skill/Command Files

**Problem:** Skills that CREATE files in target repositories (like smart-setup)
contain template content showing what files WILL BE created. Link validation
incorrectly flags these as broken links.

**Example:**

```markdown
<!-- In smart-setup SKILL.md -->

## Tier 1: Core Documentation

- `docs/SECURITY_AND_ERROR_HANDLING.md` - Security guidelines ← Template output,
  not a link!
```

**Detection:** If the file path is `skills/*/SKILL.md` or
`claude-plugins/*/commands/*.md`, and the link appears in a template/example
section (not in actual skill documentation), it's likely template content.

**Solution:** Exclude skill and command files from exhaustive link validation,
OR only validate links that are clearly documentation references (e.g., links to
external resources, other files in the skill's directory).

#### Pattern 2: Example Code Showing Correct vs Incorrect Patterns

**Problem:** Educational examples demonstrating correct relative path resolution
contain intentionally "wrong" examples. Validators flag the wrong examples as
broken.

**Example:**

```markdown
<!-- WRONG: -->

See [workflow](.github/workflows/review.yml) ← Used to teach - not a real link

<!-- CORRECT: -->

See [workflow](../../.github/workflows/review.yml)
```

**Detection:** Links near `<!-- WRONG -->` or `<!-- CORRECT -->` comments, or
within sections titled "Example", "Common Mistakes", "Good vs Bad".

**Solution:** Skip validation of links in sections containing teaching examples.
Look for markers like `<!-- WRONG -->`, `## Examples`, `### Common Mistakes`.

#### Pattern 3: Subdirectory Paths vs Top-Level Paths

**Problem:** Detection logic flags all references to `commands/` as wrong when
top-level directory is `skills/`, but many projects correctly have
`claude-plugins/*/commands/` subdirectories.

**Example:**

```text
claude-plugins/smart-setup/
├── commands/ ← This is CORRECT
└── skills/
```

**Detection:** Context matters - `commands/` is wrong at repo root level but
correct within `claude-plugins/*/`.

**Solution:** When checking for directory renames:

1. First verify if top-level directory exists
2. If not, check if it's a COMMON SUBDIRECTORY pattern (like
   `claude-plugins/*/commands/`)
3. Only flag if references are to TOP-LEVEL directory that doesn't exist

**Improved Detection Command:**

```bash
# Don't just count all "commands/" references
# Check if references are to TOP-LEVEL commands/ or subdirectory commands/

# Check for incorrect top-level references only
grep -r "^├── commands/\|^\`commands/\|/commands/" --include="*.md" . | \
  grep -v "claude-plugins/.*commands/" | \ # Exclude valid subdirectory refs
  grep -v "node_modules"
```

### Known Reliable Patterns

#### Grep Pattern for Link Extraction

**DO use simple patterns:**

```bash
# RELIABLE - extracts markdown links consistently
grep -rn '\[[^]]*\]([^)]*)' --include="*.md" . | grep -v 'https\?://'
```

**DON'T use overly complex escaping:**

```bash
# UNRELIABLE - often fails silently, returns 0 results
grep -noE '\[[^\]]*\]\([^)]+\)' file.md
```

The complex pattern with escaped brackets and extended regex often fails
silently depending on shell and grep version. The simpler pattern is more
portable and reliable.

#### AWK for Code Block Filtering

**Mandatory approach for accurate link validation:**

````bash
# Extract links ONLY from outside fenced code blocks
find . -name "*.md" -not -path "*/node_modules/*" | while read -r file; do
  awk '
    /^```/ { in_code = !in_code; next }
    !in_code && /\[[^\]]*\]\([^)]+\)/ { print FILENAME":"NR":"$0 }
  ' "$file"
done
````

This awk script tracks code block state and ensures template/example content is
excluded.

### Common Validation Failure Modes

| Failure Mode                         | Root Cause                                  | Prevention                                                     |
| ------------------------------------ | ------------------------------------------- | -------------------------------------------------------------- |
| Missed broken links                  | Ad-hoc checking instead of exhaustive       | Always extract ALL links first, then validate ALL              |
| Inconsistent results between runs    | Sampling or stopping early                  | Same extraction + validation = same results                    |
| Fixes introduce new issues           | No verification after fixes                 | MANDATORY re-run validation after every fix                    |
| Wrong relative path resolution       | Resolving from repo root vs source file     | Always resolve relative to file containing link                |
| Links in deep directories missed     | Using wrong relative path depth             | File in `guides/setup/` linking to root needs TWO `../`        |
| Template content flagged as broken   | No code block filtering                     | Use AWK-based filtering to exclude fenced code blocks          |
| Subdirectory paths flagged as errors | Checking for directory name without context | Verify TOP-LEVEL directory existence, allow subdirectory usage |

### Contributing Improvements

If you discover patterns that improve this skill's reliability, please:

1. Document the specific issue you encountered
2. Describe the solution that worked
3. Submit a PR to add the pattern to this "Lessons Learned" section

Focus on **generic improvements** that apply to any repository, not
project-specific fixes.

## Source

This skill implements documentation hygiene best practices from the
[Nightgauge](https://github.com/nightgauge/nightgauge) framework.
