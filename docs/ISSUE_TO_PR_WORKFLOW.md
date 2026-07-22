# Issue-to-PR Development Workflow

> A complete guide to using the Nightgauge pipeline skills for GitHub-based
> development

## Overview

This document describes the end-to-end workflow for developing features using
GitHub Issues and the Nightgauge pipeline skills. The workflow emphasizes:

- **Issue-driven development** — Every change starts with an issue
- **Documentation-first planning** — Read docs/ before exploring code
- **Standards compliance** — Code follows documented patterns
- **Quality gates** — Review before committing and merging

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        ISSUE-TO-PR DEVELOPMENT WORKFLOW                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────┐    ┌────────────────────┐    ┌─────────────────────────┐ │
│  │ PREREQUISITES │    │  ISSUE MANAGEMENT  │    │    DEVELOPMENT CYCLE    │ │
│  └──────────────┘    └────────────────────┘    └─────────────────────────┘ │
│         │                     │                           │                 │
│         ▼                     ▼                           ▼                 │
│  ┌─────────────┐      ┌───────────────┐          ┌───────────────┐         │
│  │ /smart-setup│      │/nightgauge-issue-│          │/nightgauge-issue-│         │
│  │ (if needed) │      │    create     │─────────▶│    pickup     │         │
│  └─────────────┘      └───────────────┘          └───────┬───────┘         │
│                                                          │                  │
│                                                          ▼                  │
│                                                  ┌───────────────┐         │
│                                                  │/nightgauge-      │         │
│                                                  │feature-planning│         │
│                                                  └───────┬───────┘         │
│                                                          │                  │
│                                                          ▼                  │
│                                                  ┌───────────────┐         │
│                                                  │/nightgauge-      │         │
│                                                  │  feature-dev  │         │
│                                                  └───────┬───────┘         │
│                                                          │                  │
│                                                          ▼                  │
│                                                  ┌───────────────┐         │
│                                                  │/nightgauge-      │         │
│                                                  │   test-gen    │         │
│                                                  └───────┬───────┘         │
│                                                          │                  │
│                                                          ▼                  │
│                                                  ┌───────────────┐         │
│                                                  │/nightgauge-      │         │
│                                                  │   pr-create   │         │
│                                                  └───────┬───────┘         │
│                                                          │                  │
│                                                          ▼                  │
│                                                  ┌───────────────┐         │
│                                                  │/nightgauge-      │         │
│                                                  │   pr-merge    │         │
│                                                  └───────────────┘         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Execution Modes

The pipeline supports two execution modes:

| Mode          | Description                          | Documentation                                  |
| ------------- | ------------------------------------ | ---------------------------------------------- |
| **Manual**    | Run stages individually via CLI      | This document (default)                        |
| **Automated** | VSCode extension orchestrates stages | [PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md) |

This document describes the **manual mode**. For automated execution with
progress tracking, token analytics, and crash recovery, see
[PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md).

---

## Phase 0: Prerequisites

Before using the pipeline, ensure your environment and repository are properly
set up.

### 0.1 Required Tools

| Tool                | Purpose             | Installation                            |
| ------------------- | ------------------- | --------------------------------------- |
| **GitHub CLI (gh)** | Issue/PR management | `brew install gh` then `gh auth login`  |
| **Git**             | Version control     | Usually pre-installed                   |
| **Claude Code**     | AI assistant        | [Install guide](https://claude.ai/code) |

**Verify installation:**

```bash
# Check GitHub CLI
gh auth status

# Check Git
git --version

# Check Claude Code
claude --version
```

### 0.2 Repository Setup

The pipeline works best with repositories following the Nightgauge pattern:

```
your-repo/
├── docs/                              # Documentation (REQUIRED)
│   ├── README.md                      # Documentation index
│   ├── ARCHITECTURE.md                # System architecture
│   ├── CODE_STANDARDS.md              # Coding conventions
│   ├── GIT_WORKFLOW.md                # Git workflow rules
│   ├── SECURITY_AND_ERROR_HANDLING.md # Security guidelines
│   └── TESTING.md                     # Test patterns (optional)
├── CLAUDE.md                          # Claude Code configuration
├── AGENTS.md                          # Universal AI configuration
└── [your code]
```

**If docs/ is missing, run `/smart-setup` first:**

```bash
# In Claude Code
/smart-setup
```

This creates the documentation structure the pipeline skills depend on.

### 0.3 Install Pipeline Skills

```bash
# Add Nightgauge plugins marketplace
claude plugin marketplace add https://github.com/nightgauge/nightgauge.git

# Install the pipeline skills
claude plugin install nightgauge-issue-create@nightgauge-plugins
claude plugin install nightgauge-issue-pickup@nightgauge-plugins
claude plugin install nightgauge-feature-planning@nightgauge-plugins
claude plugin install nightgauge-feature-dev@nightgauge-plugins
claude plugin install nightgauge-pr-create@nightgauge-plugins
```

---

## Phase 1: Issue Management

### 1.1 Creating Issues with /nightgauge-issue-create

The `/nightgauge-issue-create` skill ensures issues have all the
information needed for the development pipeline.

```bash
# In Claude Code, in your repository directory

# Interactive mode - AI guides you through issue creation
/nightgauge-issue-create

# Quick mode with description
/nightgauge-issue-create "Add user profile photo upload feature"

# With type hint
/nightgauge-issue-create --type feature "Add dark mode toggle"
/nightgauge-issue-create --type bug "Login button unresponsive on mobile"
```

**What the skill does:**

1. **Gathers information** - Asks for summary, user story, acceptance criteria,
   technical notes
2. **Structures content** - Formats with standard sections that downstream
   skills can parse
3. **Suggests labels** - Recommends appropriate labels based on content
4. **Creates issue** - Uses `gh issue create` with proper formatting

### 1.2 Issue Structure (Automatic)

Issues created by `/nightgauge-issue-create` include these parseable
sections:

```markdown
## Summary

Brief description of what needs to be done.

## User Story

> As a [user type], I want [goal] so that [benefit].

## Acceptance Criteria

- [ ] Criterion 1
- [ ] Criterion 2
- [ ] Criterion 3

## Technical Notes

- Files likely affected: `src/services/...`
- Related components: UserService, AuthMiddleware
- Dependencies: None

---

_Created with `/nightgauge-issue-create` - ready for
`/nightgauge-issue-pickup`_
```

### 1.3 Manual Issue Creation (Alternative)

You can also create issues directly on GitHub using the repository's issue
templates:

1. Navigate to **Issues** → **New Issue**
2. Select a template: **Feature Request**, **Bug Report**, **Documentation**, or
   **Chore**
3. Fill in the sections - they match the `/nightgauge-issue-create` format
4. Submit the issue

The templates include the `_Ready for /nightgauge-issue-pickup_` footer for
seamless pipeline integration.

**Or use GitHub CLI:**

```bash
gh issue create \
  --title "Add user profile photo upload" \
  --body "$(cat <<'EOF'
## Summary
Allow users to upload a profile photo.

## Acceptance Criteria
- [ ] Users can upload JPG/PNG images
- [ ] Images are resized to 200x200
- [ ] Images are stored in S3
- [ ] Fallback to default avatar if no photo

## Technical Notes
- Integrate with existing FileService
- Follow security guidelines for file uploads
EOF
)" \
  --label "enhancement"
```

### 1.4 Issue Labels

Use labels to categorize issues (affects branch naming):

| Label           | Branch Prefix | Description             |
| --------------- | ------------- | ----------------------- |
| `bug`           | `fix/`        | Something isn't working |
| `enhancement`   | `feat/`       | New functionality       |
| `documentation` | `docs/`       | Documentation changes   |
| `refactor`      | `refactor/`   | Code restructuring      |
| `chore`         | `chore/`      | Maintenance tasks       |

> **Note**: `/nightgauge-issue-create` suggests appropriate labels based on
> your issue description.

### 1.5 Assigning Issues

Issues can be assigned before or during pickup:

```bash
# Assign to yourself
gh issue edit 42 --add-assignee @me

# Or let /nightgauge-issue-pickup handle it
```

---

## Phase 2: Issue Pickup

### 2.1 Start the Pipeline

```bash
# In Claude Code, in your repository directory

# Option A: Pick up a specific issue
/nightgauge-issue-pickup 42

# Option B: See available issues and choose
/nightgauge-issue-pickup

# Option C: See issues assigned to you
/nightgauge-issue-pickup --mine
```

### 2.2 What Happens

1. **Environment Validation**
   - Checks `gh` CLI is authenticated
   - Verifies you're in a git repository with GitHub remote

2. **Issue Retrieval**
   - Fetches issue details from GitHub API
   - Extracts title, body, labels, milestone, comments

3. **Requirements Extraction**
   - Parses user story (if present)
   - Extracts acceptance criteria
   - Identifies technical notes and file references

4. **Branch Creation**
   - Determines prefix from labels (`feat/`, `fix/`, `docs/`, etc.)
   - Creates branch: `<prefix><issue>-<brief-description>`
   - Pushes branch to remote

5. **Optional: Assignment**
   - Offers to assign issue to yourself
   - Offers to add "in-progress" label

### 2.3 Output

```
┌─────────────────────────────────────────────────────────────────┐
│  ISSUE PICKUP COMPLETE                                          │
└─────────────────────────────────────────────────────────────────┘

Issue:    #42 - Add user profile photo upload
Type:     feature
Branch:   feat/42-user-photo-upload
Status:   Ready for development

## Requirements Summary
[Extracted requirements]

## Next Steps
Run `/nightgauge-feature-planning` to design the implementation
```

---

## Phase 3: Feature Planning

### 3.1 Start Planning

```bash
# After /nightgauge-issue-pickup (uses context automatically)
/nightgauge-feature-planning

# Or with explicit description
/nightgauge-feature-planning Add user profile photo upload with S3 storage
```

### 3.2 Documentation-First Approach

**This is the key innovation of the pipeline.**

Instead of exploring the entire codebase, the skill:

1. **Reads documentation first** (saves 80-90% of tokens):
   - `docs/README.md` - What's documented
   - `docs/ARCHITECTURE.md` - System patterns
   - `docs/CODE_STANDARDS.md` - Coding conventions
   - `docs/SECURITY_AND_ERROR_HANDLING.md` - Security rules
   - `docs/TESTING.md` - Test patterns
   - `CLAUDE.md` / `AGENTS.md` - AI instructions

2. **Maps requirements to documented patterns**:
   - Which architectural components apply
   - Which code standards are relevant
   - Security considerations

3. **Only explores code for undocumented areas**:
   - Uses subagents to keep context clean
   - Finds existing implementations of patterns
   - Locates integration points

### 3.3 Design Phase

The skill generates 2-3 implementation approaches:

1. **Minimal Changes** - Extend existing components, low risk
2. **Clean Architecture** - New components, well-structured
3. **Pragmatic Balance** (Recommended) - Best of both

You choose the approach before proceeding.

### 3.4 PLAN.md Creation

A detailed implementation plan is created:

```markdown
# Feature Plan: Add user profile photo upload

**Issue**: #42 **Branch**: feat/42-user-photo-upload **Date**: 2026-01-30
**Status**: Awaiting Approval

## Requirements Summary

[From issue]

## Documented Patterns Applied

- Architecture: Service pattern from docs/ARCHITECTURE.md
- Standards: Naming per docs/CODE_STANDARDS.md
- Security: File upload validation per docs/SECURITY.md

## Implementation Approach

[Chosen approach with rationale]

## Files to Modify

- [ ] `src/routes/users.ts` - Add photo upload endpoint

## Files to Create

- [ ] `src/services/PhotoService.ts` - Photo handling
- [ ] `tests/services/photo.test.ts` - Unit tests

## Test Strategy

[Based on docs/TESTING.md]
```

### 3.5 Approval Gate

**You must approve the plan before implementation begins.**

This prevents wasted effort on the wrong approach.

---

## Phase 4: Feature Development

### 4.1 Start Implementation

```bash
# Uses PLAN.md from previous phase
/nightgauge-feature-dev

# Or specify a plan file
/nightgauge-feature-dev --plan .nightgauge/plans/42-photo-upload.md
```

### 4.2 Implementation Process

1. **Plan Verification**
   - Reads PLAN.md
   - Confirms you're on the correct branch
   - Asks for confirmation to proceed

2. **Standards Loading**
   - Loads `docs/CODE_STANDARDS.md`
   - Loads `docs/SECURITY_AND_ERROR_HANDLING.md`
   - Loads `docs/TESTING.md`

3. **Code Implementation**
   - Follows plan step-by-step
   - Applies documented patterns
   - Includes error handling per security docs
   - Shows progress: `[3/5] files complete`

4. **Test Writing**
   - Writes tests alongside implementation
   - Follows patterns from docs/TESTING.md
   - Runs test suite

5. **Quality Review**
   - Code standards check
   - Security review
   - Test coverage review
   - Documentation review

6. **Self-Correction**
   - Fixes any issues found
   - Re-runs tests
   - Verifies all checks pass

7. **Commit**
   - Stages specific files
   - Creates commit with proper message format

### 4.3 Output

```
┌─────────────────────────────────────────────────────────────────┐
│  IMPLEMENTATION COMPLETE                                        │
└─────────────────────────────────────────────────────────────────┘

Branch:  feat/42-user-photo-upload
Commit:  [FEAT][#42] Add user photo upload

## Files Changed
- src/services/PhotoService.ts (created)
- src/routes/users.ts (modified)
- tests/services/photo.test.ts (created)

## Quality Checks
✓ Code standards: Passed
✓ Security review: Passed
✓ Tests: 12 passed, 0 failed

## Next Steps
Run `/nightgauge-test-gen` to generate comprehensive tests (optional but recommended)
```

---

## Phase 4.5: Test Generation (Optional)

This phase generates comprehensive test suites with coverage analysis. It's
optional but recommended before creating a PR.

### 4.5.1 Generate Tests

```bash
# Generate tests for changed files
/nightgauge-test-gen

# Target specific files
/nightgauge-test-gen --files "src/services/*.ts"

# Set coverage target
/nightgauge-test-gen --target-coverage 90

# Skip E2E tests (faster)
/nightgauge-test-gen --skip-e2e

# Preview without writing
/nightgauge-test-gen --dry-run
```

### 4.5.2 What Happens

1. **Source Analysis**
   - Identifies files needing tests
   - Detects test framework (Jest, Pytest, dotnet test, Gradle)
   - Analyzes existing coverage

2. **Parallel Test Generation**
   - Spawns unit test subagent
   - Spawns integration test subagent
   - Spawns E2E test subagent (unless `--skip-e2e`)

3. **Edge Case Generation**
   - String inputs: empty, null, unicode, special chars
   - Numeric inputs: zero, negative, max/min, NaN
   - Collections: empty, single item, large arrays
   - Async: timeout, network failure scenarios

4. **Non-Destructive Mode**
   - Asks before overwriting existing tests
   - Options: add only, replace, create separate files, review first

5. **Test Execution**
   - Runs generated tests
   - Fixes failing tests (test issues, not implementation)
   - Reports coverage improvement

### 4.5.3 Output

```
┌─────────────────────────────────────────────────────────────────┐
│  TEST GENERATION COMPLETE                                       │
└─────────────────────────────────────────────────────────────────┘

Branch:  feat/42-user-photo-upload
Issue:   #42

## Tests Generated

| Type        | Files | Tests | Coverage Added |
|-------------|-------|-------|----------------|
| Unit        | 3     | 35    | +25%           |
| Integration | 1     | 5     | +5%            |
| E2E         | 1     | 3     | N/A            |
| **Total**   | **5** | **43**| **+30%**       |

## Coverage

Before: 50%  →  After: 80%  ✓ Target met

## Next Steps
Run `/nightgauge-pr-create` to create a pull request
```

---

## Phase 5: PR Creation

### 5.1 Create the PR

```bash
# Standard PR
/nightgauge-pr-create

# Draft PR (for early feedback)
/nightgauge-pr-create --draft

# Specify reviewers
/nightgauge-pr-create --reviewer @teammate
```

### 5.2 What Happens

1. **Pre-Flight Checks**
   - Verifies not on main branch
   - Checks for uncommitted changes
   - Ensures branch is pushed
   - Runs tests (warns if failing)

2. **Context Gathering**
   - Gets commit history
   - Reads PLAN.md
   - Fetches linked issue details

3. **Description Generation**
   - Summary of changes
   - Links to issue (`Closes #42`)
   - List of files changed
   - Test plan
   - Documentation status

4. **PR Creation**
   - Uses `gh pr create`
   - Applies proper title format
   - Assigns to self
   - Requests reviewers

### 5.3 Output

```
┌─────────────────────────────────────────────────────────────────┐
│  PULL REQUEST CREATED                                           │
└─────────────────────────────────────────────────────────────────┘

PR:       #87
Title:    [FEAT][#42] Add user photo upload
URL:      https://github.com/org/repo/pull/87
Status:   Ready for review

## Linked
- Issue: #42
- Branch: feat/42-user-photo-upload

## Reviewers
- @teammate (requested)

## Quick Commands
- View: gh pr view 87
- Browser: gh pr view 87 --web
- Merge: gh pr merge 87
```

---

## Phase 6: Review & Merge with /nightgauge-pr-merge

### 6.1 Start the Merge Process

```bash
# Merge current branch's PR (waits for CI, handles feedback)
/nightgauge-pr-merge

# Merge specific PR
/nightgauge-pr-merge --pr 87

# Custom CI timeout
/nightgauge-pr-merge --timeout 10
```

### 6.2 What Happens

1. **Validates PR State**
   - Confirms open PR exists for current branch
   - Checks PR hasn't already been merged
   - Verifies branch is up to date with base

2. **Waits for CI Checks**
   - Polls check status every 30 seconds
   - Default timeout: 5 minutes (configurable with `--timeout`)
   - Shows progress: "Waiting for CI... (2/5 checks complete)"

3. **Parses Review Feedback**
   - Fetches automated review comments (e.g., Claude Code Review)
   - Fetches human review comments
   - Extracts actionable items

4. **Categorizes Issues**

   | Category     | Keywords                          | Action                   |
   | ------------ | --------------------------------- | ------------------------ |
   | **Critical** | security, vulnerability, breaking | Block merge, require fix |
   | **Major**    | bug, incorrect, wrong, must       | Ask user for decision    |
   | **Minor**    | nit, style, typo, consider        | Auto-fix if enabled      |

5. **Addresses Feedback**
   - Auto-fixes minor issues (unless `--no-auto-fix`)
   - Presents major issues for user decision
   - Blocks on critical issues until resolved

6. **Merges PR**
   - Squash merge by default
   - Use `--merge` or `--rebase` for alternatives
   - No admin bypass exists — a merge blocked by branch protection is
     terminal and must be escalated (#186)

7. **Post-Merge Cleanup**
   - Closes the issue (GitHub built-in workflow sets Status to "Done")
   - Deletes feature branch (local and remote)
   - Switches to main and pulls latest

### 6.3 Manual Review (Optional)

If you prefer manual review before using `/nightgauge-pr-merge`:

```bash
# View PR details
gh pr view 87

# Check out the branch locally
gh pr checkout 87

# View diff
gh pr diff 87

# Add review comments
gh pr review 87 --comment --body "Looks good overall, minor suggestion..."

# Approve
gh pr review 87 --approve
```

### 6.4 Output

```
┌─────────────────────────────────────────────────────────────────┐
│  PR MERGE COMPLETE                                              │
└─────────────────────────────────────────────────────────────────┘

PR:       #87 - [FEAT][#42] Add user photo upload
Status:   Merged via squash
Issue:    #42 (closed)

## Review Summary
- Automated: 9/10 "Approved with Minor Changes"
- Human: 1 approval

## Issues Addressed
- Minor: Fixed typo in error message (auto-fixed)
- Minor: Added missing type annotation (auto-fixed)

## Cleanup
✓ Issue #42 status: done
✓ Project board: Done
✓ Branch deleted: feat/42-user-photo-upload
✓ Switched to main (pulled latest)
```

---

## Complete Example Walkthrough

Here's a full example from issue creation to merge:

```bash
# 1. Open Claude Code in your repo
claude

# 2. Create an issue using the skill
> /nightgauge-issue-create "Add dark mode toggle"
# Interactive prompts for acceptance criteria, technical notes, etc.
# Creates issue #42 with proper structure

# 3. Pick up the issue
> /nightgauge-issue-pickup 42
# Creates branch: feat/42-dark-mode-toggle

# 4. Plan the implementation
> /nightgauge-feature-planning
# Reads docs/, designs approach, creates PLAN.md
# Review and approve the plan

# 5. Implement the feature
> /nightgauge-feature-dev
# Implements, tests, reviews, commits

# 6. Create the PR
> /nightgauge-pr-create --reviewer @teammate
# Creates PR #87, links to issue #42

# 7. After approval, merge
gh pr merge 87 --squash
# Issue #42 is automatically closed
```

---

## Troubleshooting

### Common Issues

| Problem                | Solution                                                  |
| ---------------------- | --------------------------------------------------------- |
| `gh` not authenticated | Run `gh auth login`                                       |
| No docs/ folder        | Run `/smart-setup` first                                  |
| Tests failing          | Fix tests before `/nightgauge-pr-create` or use `--draft` |
| Branch already exists  | Delete or rename: `git branch -D <branch>`                |
| Uncommitted changes    | Commit, stash, or discard before switching branches       |

### Getting Help

```bash
# View skill documentation
# Ask Claude to show the skill file:
> Show me the /nightgauge-issue-pickup skill documentation

# Check GitHub CLI help
gh issue --help
gh pr --help
```

---

## Workflow Gaps & Future Enhancements

### Currently Manual Steps

| Step                    | Current      | Future Enhancement            |
| ----------------------- | ------------ | ----------------------------- |
| Code review (authoring) | Manual       | `/nightgauge-review-pr` skill |
| Issue templates         | Manual setup | `/smart-setup` enhancement    |

### Recently Automated (v1.0.0+)

| Step               | Skill                  | Description                                      |
| ------------------ | ---------------------- | ------------------------------------------------ |
| Wait for CI        | `/nightgauge-pr-merge` | Polls check status with configurable timeout     |
| Parse feedback     | `/nightgauge-pr-merge` | Extracts issues from automated and human reviews |
| Address feedback   | `/nightgauge-pr-merge` | Auto-fixes minor issues, asks about major        |
| Merge PR           | `/nightgauge-pr-merge` | Squash/merge/rebase with admin bypass option     |
| Post-merge cleanup | `/nightgauge-pr-merge` | Labels, project board, branch deletion           |

### Planned Improvements

1. **PR Review Skill** (`/nightgauge-review-pr`)
   - Review PRs against docs/ standards
   - Check for security issues
   - Suggest improvements

2. **Pipeline Orchestration**
   - Run entire pipeline with one command
   - Progress tracking across phases
   - Rollback capabilities

3. **Issue Templates Integration**
   - `/smart-setup` creates issue templates aligned with
     `/nightgauge-issue-create`
   - Templates for features, bugs, documentation, chores

---

## Best Practices

### Do

- ✅ Always start with an issue
- ✅ Run `/smart-setup` on new repositories
- ✅ Review and approve PLAN.md before implementation
- ✅ Run `/nightgauge-test-gen` for comprehensive test coverage
- ✅ Write meaningful commit messages
- ✅ Link PRs to issues with "Closes #X"
- ✅ Request code review before merging

### Don't

- ❌ Push directly to main
- ❌ Skip the planning phase for complex features
- ❌ Merge without code review
- ❌ Create PRs with failing tests (use draft instead)
- ❌ Ignore documented patterns in docs/

---

## Quick Reference Card

```
┌─────────────────────────────────────────────────────────────────┐
│                    NIGHTGAUGE PIPELINE QUICK REFERENCE             │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  SETUP                                                          │
│  ──────                                                         │
│  gh auth login          # Authenticate GitHub CLI               │
│  /smart-setup           # Create docs/ if missing               │
│                                                                 │
│  FULL PIPELINE                                                  │
│  ─────────────                                                  │
│  /nightgauge-issue-create "desc"  # Create structured issue        │
│  /nightgauge-issue-pickup 42      # Claim issue, create branch     │
│  /nightgauge-feature-planning     # Design approach                │
│  /nightgauge-feature-dev          # Implement code                 │
│  /nightgauge-test-gen             # Generate comprehensive tests   │
│  /nightgauge-pr-create            # Create PR                      │
│  /nightgauge-pr-merge             # Wait for reviews, merge        │
│                                                                 │
│  GITHUB CLI                                                     │
│  ──────────                                                     │
│  gh issue list          # List issues                           │
│  gh issue view 42       # View issue details                    │
│  gh pr view 87          # View PR                               │
│  gh pr merge 87         # Merge PR                              │
│                                                                 │
│  BRANCH PREFIXES                                                │
│  ───────────────                                                │
│  feat/    enhancement   fix/     bug                            │
│  docs/    documentation refactor/ refactoring                   │
│  chore/   maintenance                                           │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## Author

nightgauge
