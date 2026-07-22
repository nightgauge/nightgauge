# Workflow Playbooks

Step-by-step narrative guides for the most common Nightgauge development scenarios. Each playbook shows which skills fire in what order and what to expect at each step.

**Cross-reference:** See [docs/SKILLS_USAGE_GUIDE.md](SKILLS_USAGE_GUIDE.md) for detailed skill documentation and [docs/PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md) for execution modes (manual CLI vs automated VSCode extension).

---

## Playbook 1: Fix a Bug (Small Single-Repo Fix)

**When to use this playbook:** You have a bug report issue and want to fix it with a small, focused change. No major planning needed. Single repository. Estimated time: 30-60 minutes.

**Prerequisites:**

- Bug report issue already exists on GitHub (or create one)
- You're in the repository root with git access and GitHub CLI authenticated (`gh auth status`)
- No other work in progress on this branch

**Token budget:** Light (200-400 tokens total)

### Flowchart

```
GitHub Issue (Bug)
       ↓
issue-pickup  → Extract requirements from bug report
       ↓
feature-dev   → Small fix, test it locally
       ↓
pr-create     → Create PR, link to issue
       ↓
pr-merge      → Wait for CI, merge
       ↓
Issue Closed (Done)
```

### Steps

**Step 1: Claim the issue and set up your branch**

```bash
/nightgauge-issue-pickup 42
```

What happens:

- Validates GitHub CLI authentication
- Fetches issue #42 from GitHub
- Extracts title, description, labels
- Creates feature branch: `fix/42-brief-description`
- Pushes branch to remote
- Outputs summary: issue number, branch name, requirements extracted

Expected output:

```
┌─────────────────────────────────────┐
│  ISSUE PICKUP COMPLETE              │
├─────────────────────────────────────┤
│ Issue:    #42 - Login button broken │
│ Type:     bug                       │
│ Branch:   fix/42-login-button       │
│ Status:   Ready for development     │
└─────────────────────────────────────┘
```

**What happens next:** You're on a new branch with issue context saved to `.nightgauge/pipeline/issue-42.json`. For small bugs, you can skip feature-planning and go directly to feature-dev.

---

**Step 2: Implement the fix (no planning needed for small bugs)**

```bash
/nightgauge-feature-dev
```

What happens:

- Reads issue context from step 1
- Loads code standards from `docs/CODE_STANDARDS.md`
- Examines codebase to understand bug
- Implements fix (small, targeted code change)
- Writes tests to verify fix
- Runs tests locally
- Reviews code against standards
- Commits with proper message format

Expected output:

```
┌─────────────────────────────────────┐
│  IMPLEMENTATION COMPLETE            │
├─────────────────────────────────────┤
│ Branch:  fix/42-login-button        │
│ Commit:  [FIX][#42] Login button    │
│          responsive on mobile       │
│                                     │
│ Files Changed:                      │
│ • src/components/LoginButton.tsx    │
│ • tests/components/button.test.ts   │
│                                     │
│ Quality Checks:                     │
│ ✓ Code standards: Passed            │
│ ✓ Tests: 3 passed, 0 failed         │
└─────────────────────────────────────┘
```

**Common failure points:**

- Tests fail → Fix test expectations or code
- Linter errors → Run `npm run lint --fix`
- Build errors → Address TypeScript or compilation errors before proceeding

**Recovery:** If you need to fix something, just run `/nightgauge-feature-dev` again — it will overwrite the previous attempt.

---

**Step 3: Create a pull request**

```bash
/nightgauge-pr-create
```

What happens:

- Validates you're not on main branch
- Checks for uncommitted changes (should be none)
- Confirms branch is pushed to remote
- Generates PR title from issue and commit message
- Generates PR description with issue link (`Closes #42`)
- Lists files changed
- Creates PR using `gh pr create`
- Assigns to yourself
- Outputs PR number and URL

Expected output:

```
┌─────────────────────────────────────┐
│  PULL REQUEST CREATED               │
├─────────────────────────────────────┤
│ PR:       #143                      │
│ Title:    [FIX][#42] Login button   │
│           responsive on mobile      │
│ URL:      https://github.com/...    │
│ Status:   Ready for review          │
│                                     │
│ Quick Commands:                     │
│ • View: gh pr view 143              │
│ • Merge: gh pr merge 143 --squash   │
└─────────────────────────────────────┘
```

**What happens next:** PR is created and posted. CI checks run automatically. Once CI passes and you have approval, proceed to step 4.

---

**Step 4: Merge the PR**

```bash
/nightgauge-pr-merge
```

What happens:

- Validates PR is open and ready
- Waits for CI checks to pass (polls every 30 seconds, 5-minute timeout)
- Shows progress: "Waiting for CI... (3/5 checks complete)"
- Parses automated review comments
- Auto-fixes minor issues (formatting, typos)
- Squash merges PR
- Closes issue #42 (GitHub sets status to "Done" automatically)
- Deletes feature branch locally and on remote
- Switches to main and pulls latest changes

Expected output:

```
┌─────────────────────────────────────┐
│  PR MERGE COMPLETE                  │
├─────────────────────────────────────┤
│ PR:       #143 - [FIX][#42]         │
│ Status:   Merged (squash)           │
│ Issue:    #42 (closed)              │
│                                     │
│ Review Summary:                     │
│ • CI: All checks passed ✓           │
│ • Human: 1 approval ✓               │
│                                     │
│ Cleanup:                            │
│ ✓ Issue #42 → Done                  │
│ ✓ Branch deleted                    │
│ ✓ On main (latest pulled)           │
└─────────────────────────────────────┘
```

**Common failure points:**

- CI checks fail → The pr-merge skill will tell you which check failed. Go back to your branch, fix it, and push. Then run pr-merge again.
- Merge conflict → Manually resolve or ask for help
- Branch protection rule → If you don't have merge permissions, contact admin

**What happens next:** Bug is fixed, PR is merged, issue is closed. Done! Move to the next issue.

---

## Playbook 2: Build a Feature (Medium Feature with Planning)

**When to use this playbook:** Feature request is more complex (3-5 acceptance criteria, might touch multiple files). Needs architecture planning. Single repository. Estimated time: 2-4 hours.

**Prerequisites:**

- Feature request issue already exists on GitHub
- You have time to review and approve a PLAN.md before implementation
- Clean working directory, on main branch

**Token budget:** Moderate (1,000-2,000 tokens total)

### Flowchart

```
GitHub Issue (Feature Request)
           ↓
   issue-pickup  → Extract requirements, user story
           ↓
feature-planning → Read docs, propose approach, create PLAN.md
           ↓
   (YOU APPROVE)
           ↓
  feature-dev    → Implement following approved PLAN.md
           ↓
   test-gen      → Generate comprehensive tests (optional but recommended)
           ↓
   pr-create     → Create PR
           ↓
   pr-merge      → Merge to main
           ↓
Issue Closed (Done)
```

### Steps

**Step 1: Issue pickup**

```bash
/nightgauge-issue-pickup 78
```

Same as Playbook 1, Step 1. Creates feature branch from issue.

---

**Step 2: Plan the feature (crucial approval gate)**

```bash
/nightgauge-feature-planning
```

What happens:

- Reads issue context from step 1
- Reads your documentation: `docs/ARCHITECTURE.md`, `docs/CODE_STANDARDS.md`, `docs/SECURITY_AND_ERROR_HANDLING.md`
- Proposes 2-3 implementation approaches (minimal changes vs clean architecture vs pragmatic balance)
- Creates a detailed PLAN.md file with:
  - Requirements summary
  - Documented patterns applied (from your docs/)
  - Implementation approach (with rationale)
  - Files to modify and create
  - Test strategy
- Saves plan to `.nightgauge/plans/78-feature-name.md`
- Outputs summary and asks for approval

Expected output:

```
┌─────────────────────────────────────────────────────┐
│  FEATURE PLANNING COMPLETE                          │
├─────────────────────────────────────────────────────┤
│ Issue:    #78 - Add user profile photo upload       │
│ Status:   Awaiting Your Approval                    │
│                                                     │
│ Plan saved to:                                      │
│ .nightgauge/plans/78-photo-upload.md           │
│                                                     │
│ Proposed approach: Pragmatic Balance                │
│                                                     │
│ Files to modify:                                    │
│ • src/routes/users.ts (add endpoint)                │
│ • src/middleware/auth.ts (add permission check)     │
│                                                     │
│ Files to create:                                    │
│ • src/services/PhotoService.ts (new)                │
│ • tests/services/photo.test.ts (new)                │
│                                                     │
│ NEXT STEP:                                          │
│ 1. Read the PLAN.md (review approach)               │
│ 2. Approve or request changes (discuss with agent)  │
│ 3. Once approved, run /nightgauge-feature-dev  │
└─────────────────────────────────────────────────────┘
```

**Critical approval gate:** Before proceeding to step 3, you MUST read the PLAN.md and approve it. This prevents wasted effort on the wrong approach.

```bash
# Review the plan
cat .nightgauge/plans/78-photo-upload.md

# If you like it, proceed to step 3
# If you want changes, ask the agent to revise and re-run feature-planning
```

**Common issues:**

- Plan is too ambitious → Ask agent to simplify approach
- Plan doesn't match your architecture → Ask agent to re-read docs and revise
- Missing a file → Ask agent to add it to the plan

Once you approve, you're ready for implementation.

---

**Step 3: Implement the feature**

```bash
/nightgauge-feature-dev
```

Same as Playbook 1, Step 2, but with:

- More files to create/modify (because plan is more detailed)
- Longer implementation time
- More comprehensive tests

Expected output includes list of 5-10 files changed instead of 2-3 for a small bug fix.

---

**Step 4: Generate comprehensive tests (optional but recommended)**

```bash
/nightgauge-test-gen
```

What happens:

- Analyzes files changed in step 3
- Detects test framework (Jest, Vitest, Pytest, dotnet test, Gradle)
- Spawns three parallel subagents:
  - Unit test generator (for PhotoService)
  - Integration test generator (for API endpoint)
  - E2E test generator (for full user flow)
- Generates edge case tests (empty inputs, null, unicode, timeouts, etc.)
- Runs all tests
- Fixes failing tests
- Reports coverage improvement

Expected output:

```
┌─────────────────────────────────────────────────────┐
│  TEST GENERATION COMPLETE                           │
├─────────────────────────────────────────────────────┤
│ Issue:   #78 - Add user profile photo upload        │
│ Branch:  feat/78-photo-upload                       │
│                                                     │
│ Tests Generated:                                    │
│ • Unit tests: 28 tests (new)                        │
│ • Integration tests: 8 tests (new)                  │
│ • E2E tests: 5 tests (new)                          │
│ • Total: 41 new tests                               │
│                                                     │
│ Coverage:                                           │
│ Before: 55%  →  After: 82%  ✓ Target met           │
│                                                     │
│ All tests passing ✓                                 │
└─────────────────────────────────────────────────────┘
```

This step adds safety and confidence before creating a PR.

---

**Step 5-6: PR creation and merge**

```bash
/nightgauge-pr-create
/nightgauge-pr-merge
```

Same as Playbook 1, Steps 3-4.

---

## Playbook 3: Onboard a New Repository (Pipeline Ready Setup)

**When to use this playbook:** You have a new empty repository (or existing repo with no Nightgauge setup) and want to make it pipeline-ready. One-time setup. Estimated time: 15-30 minutes.

**Prerequisites:**

- Repository exists on GitHub
- You have admin access to configure labels and project board
- You have pushed at least one commit (so main branch exists)

**Token budget:** Light (300-500 tokens)

### Flowchart

```
Empty Repository
        ↓
repo-init     → Create labels, board fields, config
        ↓
smart-setup   → Create docs/, AGENTS.md, CLAUDE.md
        ↓
backlog-preflight → Validate setup complete
        ↓
project-sync  → (Optional) Sync existing issues to board
        ↓
Pipeline Ready ✓
```

### Steps

**Step 1: Initialize repository for Nightgauge**

```bash
/nightgauge-repo-init
```

What happens:

- Creates all required GitHub labels:
  - `bug`, `enhancement`, `documentation`, `refactor`, `chore`
  - `type:epic`, `type:task`, `type:story`
  - `status:backlog`, `status:ready`, `status:in-progress`, `status:done`
  - `size:small`, `size:medium`, `size:large`
- Creates or validates GitHub Project board with fields:
  - Status (Ready, In Progress, In Review, Done)
  - Priority (Critical, High, Medium, Low)
  - Size (Small, Medium, Large)
- Generates `.nightgauge/config.yaml` with:
  - Organization/repo names
  - Project board ID
  - Label mappings
  - Pipeline settings
- Pushes config to remote

Expected output:

```
┌─────────────────────────────────────────────┐
│  REPOSITORY INITIALIZATION COMPLETE         │
├─────────────────────────────────────────────┤
│ Repository: org/repo                        │
│ Status:     Pipeline-Ready                  │
│                                             │
│ Created:                                    │
│ ✓ GitHub Labels (14 total)                  │
│ ✓ Project Board fields                      │
│ ✓ .nightgauge/config.yaml              │
│                                             │
│ Configuration saved:                        │
│ • Project ID: PVT_kwABC123                  │
│ • Labels mapped correctly                   │
│ • Pipeline enabled ✓                        │
└─────────────────────────────────────────────┘
```

---

**Step 2: Make repository AI-ready (docs, config, standards)**

```bash
/smart-setup
```

What happens:

- Creates `docs/` directory with template files:
  - `docs/README.md` — documentation index
  - `docs/ARCHITECTURE.md` — system architecture
  - `docs/CODE_STANDARDS.md` — coding standards
  - `docs/SECURITY_AND_ERROR_HANDLING.md` — security guidelines
  - `docs/TESTING.md` — test patterns
- Creates `AGENTS.md` — universal AI agent configuration
- Creates `CLAUDE.md` — Claude Code specific configuration
- Creates `.gitignore` entries for build artifacts and secrets
- Asks clarifying questions (or use `--skip-questions` for automated)

Expected output:

```
┌─────────────────────────────────────────────┐
│  SMART SETUP COMPLETE                       │
├─────────────────────────────────────────────┤
│ Repository: org/repo                        │
│                                             │
│ Created:                                    │
│ ✓ docs/README.md                            │
│ ✓ docs/ARCHITECTURE.md                      │
│ ✓ docs/CODE_STANDARDS.md                    │
│ ✓ docs/SECURITY_AND_ERROR_HANDLING.md       │
│ ✓ docs/TESTING.md                           │
│ ✓ AGENTS.md                                 │
│ ✓ CLAUDE.md                                 │
│                                             │
│ Ready for:                                  │
│ • Feature development with /skills          │
│ • Automated issue-to-PR pipeline            │
│ • Multi-tool AI assistance                  │
│                                             │
│ Next: Review docs/, customize, push to repo │
└─────────────────────────────────────────────┘
```

**What to do next:** Review the created files and customize them for your project's specific architecture and standards. Commit and push:

```bash
git add docs/ AGENTS.md CLAUDE.md .nightgauge/ .gitignore
git commit -m "chore: Initialize repository for Nightgauge pipeline"
git push
```

---

**Step 3: Validate backlog is pipeline-ready (if you have existing issues)**

```bash
/nightgauge-backlog-preflight
```

What happens:

- Scans all issues with `status:ready` label
- Validates each issue has:
  - Proper title (clear and specific)
  - Description with structured sections (summary, acceptance criteria)
  - At least one label indicating type (bug, enhancement, etc.)
  - Acceptance criteria clearly defined
- Reports which issues are pipeline-ready
- Flags issues needing fixes

Expected output:

```
┌─────────────────────────────────────────────┐
│  BACKLOG PREFLIGHT COMPLETE                 │
├─────────────────────────────────────────────┤
│ Repository: org/repo                        │
│                                             │
│ Issues Scanned: 12                          │
│ Ready: 10                                   │
│ Needs Fixes: 2                              │
│                                             │
│ Issues Needing Work:                        │
│ • #3: Missing acceptance criteria           │
│ • #7: No type label (bug/feature/etc)       │
│                                             │
│ Ready issues: #1, #2, #4, #5, #6, #8...    │
│                                             │
│ Next: Fix flagged issues, then you can run  │
│ the pipeline on ready issues                │
└─────────────────────────────────────────────┘
```

---

**Step 4: Sync existing issues to project board (optional)**

If you have existing issues that need to be added to the project board:

```bash
/nightgauge-project-sync
```

What happens:

- Fetches all open issues
- Adds each to the project board
- Sets Status field based on labels (Ready, In Progress, Done)
- Sets Priority based on milestone or labels
- Sets Size if detected from labels

---

**What happens next:** Your repository is now pipeline-ready. Users can:

- Create issues with `/nightgauge-issue-create`
- Pick up issues with `/nightgauge-issue-pickup`
- Run the full pipeline
- All skills will work seamlessly

---

## Playbook 4: Audit Codebase Health (Comprehensive Quality Assessment)

**When to use this playbook:** You inherit a codebase or want a comprehensive health check. Understand current state across multiple dimensions. Estimated time: 45-90 minutes.

**Prerequisites:**

- You're in a repository root
- Codebase is buildable and testable
- You have time to read and understand a detailed report

**Token budget:** Heavy (2,000-3,500 tokens)

### Flowchart

```
Repository Root
        ↓
health-check       → 6-dimension health score
        ↓
security-audit     → 7-dimension security posture
        ↓
test-scaffold      → Test coverage and critical untested paths
        ↓
product-audit      → 8-dimension cross-repo quality (if multi-repo)
        ↓
integration-audit  → Cross-repo API alignment (if multi-repo)
        ↓
Health Report with Recommendations
```

### Steps

**Step 1: Run comprehensive health check**

```bash
/nightgauge-health-check
```

What happens:

- Analyzes 6 dimensions:
  1. **Code Quality** — Complexity, readability, duplication, test coverage
  2. **Architecture** — Layering, separation of concerns, documentation
  3. **Dependencies** — Version freshness, vulnerability scanning, unused imports
  4. **Testing** — Coverage %, test type breakdown (unit/integration/e2e), critical paths
  5. **Documentation** — API docs, README completeness, architecture docs
  6. **Performance** — Load time, bundle size, query performance (if applicable)
- Produces quantitative scores (0-100 for each dimension)
- Generates detailed findings with examples

Expected output:

```
┌──────────────────────────────────────────┐
│  HEALTH CHECK COMPLETE                   │
├──────────────────────────────────────────┤
│ Repository: org/repo                     │
│ Analyzed:   247 files, 42k LOC          │
│                                          │
│ OVERALL SCORE: 72/100                    │
│                                          │
│ By Dimension:                            │
│ • Code Quality:      85/100 ✓            │
│ • Architecture:      78/100 ✓            │
│ • Dependencies:      52/100 ⚠ (outdated)│
│ • Testing:          65/100 ⚠ (70% needed)│
│ • Documentation:    80/100 ✓            │
│ • Performance:      75/100 ✓            │
│                                          │
│ Top Findings:                            │
│ 1. 8 dependencies outdated (critical)    │
│ 2. 15% untested code paths (risky)       │
│ 3. Missing API docs in 3 modules         │
│                                          │
│ Report saved: .nightgauge/logs/...  │
└──────────────────────────────────────────┘
```

**What to do with this:** Note which dimensions are weak (score < 70). Those are candidates for improvement.

---

**Step 2: Run security audit**

```bash
/nightgauge-security-audit
```

What happens:

- Analyzes 7 dimensions:
  1. **Vulnerabilities** — Known CVEs in dependencies
  2. **Secrets** — Hardcoded API keys, credentials, tokens
  3. **OWASP Top 10** — SQL injection, XSS, CSRF, auth flaws, etc.
  4. **Cryptography** — Weak crypto algorithms, insecure randomness
  5. **Input Validation** — User input sanitization, parameter validation
  6. **Authentication & Authorization** — Access control, permission checks
  7. **Misconfiguration** — Unsafe defaults, exposed endpoints, debug flags
- Produces scores and risk categorization (Critical, High, Medium, Low)
- Generates remediation steps for each finding

Expected output:

```
┌──────────────────────────────────────────┐
│  SECURITY AUDIT COMPLETE                 │
├──────────────────────────────────────────┤
│ Repository: org/repo                     │
│                                          │
│ OVERALL SECURITY SCORE: 81/100           │
│                                          │
│ Critical Issues: 0                       │
│ High Issues:    2                        │
│ Medium Issues:  4                        │
│ Low Issues:     3                        │
│                                          │
│ High Issues:                             │
│ 1. Hardcoded API key in .env.example     │
│    → Remove from repo, use secrets mgmt  │
│ 2. Missing CSRF protection on POST forms │
│    → Add CSRF token validation           │
│                                          │
│ Report: .nightgauge/logs/...        │
└──────────────────────────────────────────┘
```

**Critical vs High vs Medium:** Fix Critical and High issues before merging code. Medium/Low can be tracked as tech debt.

---

**Step 3: Generate characterization tests (safety net)**

```bash
/nightgauge-test-scaffold
```

What happens:

- Identifies test coverage gaps
- Finds critical untested code paths (security-sensitive, high-use)
- Generates "characterization tests" — tests that capture current behavior
- Creates safety net before refactoring

Expected output:

```
┌──────────────────────────────────────────┐
│  TEST SCAFFOLD COMPLETE                  │
├──────────────────────────────────────────┤
│ Current Coverage: 68%                    │
│                                          │
│ Critical Untested Paths Identified: 12   │
│ • Authentication flow (high risk)        │
│ • Error handling in database layer       │
│ • Permission checks in API endpoints     │
│                                          │
│ Characterization Tests Generated: 47     │
│ • auth.test.ts (new): 15 tests           │
│ • database.test.ts (new): 18 tests       │
│ • api-auth.test.ts (new): 14 tests       │
│                                          │
│ Coverage after tests: 78%                │
│                                          │
│ ✓ Safety net in place for refactoring    │
└──────────────────────────────────────────┘
```

---

**Step 4: Cross-repo product audit (if multi-repo)**

If your product spans multiple repositories (e.g., VSCode extension + SDK + platform):

```bash
/nightgauge-product-audit
```

What happens:

- Audits all repositories in the product
- Validates 8 dimensions:
  1. **API Alignment** — Client/server APIs match
  2. **Epic Lifecycle** — Issues properly linked across repos
  3. **Documentation** — Docs are consistent across repos
  4. **Feature Parity** — Feature availability matches across repos
  5. **Test Coverage** — All repos meet coverage targets
  6. **Security** — Security practices consistent
  7. **Dependencies** — Versions and licenses aligned
  8. **CI/CD** — Pipeline health across repos
- Produces cross-repo scorecard

---

**Step 5: Cross-repo integration audit (if multi-repo)**

```bash
/nightgauge-integration-audit
```

What happens:

- Validates client → server API calls match actual endpoints
- Checks auth flows are consistent across repos
- Verifies documentation is current
- Tracks cross-repo dependencies
- Detects mismatches and drift

Expected output:

```
┌──────────────────────────────────────────┐
│  INTEGRATION AUDIT COMPLETE              │
├──────────────────────────────────────────┤
│ Client Repo: nightgauge-vscode      │
│ Server Repo: acme-platform    │
│                                          │
│ HEALTH SCORE: 88/100                     │
│                                          │
│ Findings:                                │
│ ✓ All API calls match endpoints          │
│ ✓ Auth flows aligned                     │
│ ⚠ 1 deprecated endpoint still used       │
│   → Update client to use new endpoint    │
│ ✓ Docs current                           │
│                                          │
│ Deprecated Endpoints:                    │
│ • POST /auth/login (deprecated 2026-03)  │
│   Use: POST /auth/v2/login instead       │
└──────────────────────────────────────────┘
```

---

**Next steps after audits:** Create a modernization plan (Playbook 5) to address findings.

---

## Playbook 5: Plan a Major Refactor (Tech Debt → Roadmap)

**When to use this playbook:** You have significant tech debt and want a structured plan to address it. Creates a phased, prioritized roadmap. Estimated time: 1-2 hours.

**Prerequisites:**

- You've run health-check and security-audit (or understand your tech debt)
- You have time for detailed analysis
- You're ready to commit to a multi-phase improvement plan

**Token budget:** Heavy (2,000-3,000 tokens)

### Flowchart

```
Tech Debt Identified
        ↓
health-check         → Quantify problems (current state)
        ↓
refactor-rewrite     → Should we refactor or rewrite each area?
        ↓
modernize-plan       → Create phased, prioritized roadmap
        ↓
issue-create (epic)  → Create epic with sub-issues for each phase
        ↓
Structured Roadmap ✓
```

### Steps

**Step 1: Gather current state (if not done)**

```bash
/nightgauge-health-check
/nightgauge-security-audit
```

From Playbook 4. You need baseline scores to measure improvement.

---

**Step 2: Decide refactor vs rewrite for each component**

```bash
/nightgauge-refactor-rewrite
```

What happens:

- Analyzes your codebase across 8 dimensions:
  1. Code quality and maintainability
  2. Test coverage and confidence in changes
  3. Performance characteristics
  4. Security vulnerabilities
  5. Dependency health
  6. Documentation completeness
  7. Technical debt weight
  8. Business risk of extensive changes
- For each major component, recommends:
  - **Refactor** (fix in place) — Low risk, good ROI
  - **Rewrite** (rebuild) — Higher risk, sometimes necessary
  - **Leave as-is** — Not worth addressing now
- Produces confidence level (high/medium/low) for each recommendation
- Creates risk/benefit matrix

Expected output:

```
┌──────────────────────────────────────────┐
│  REFACTOR VS REWRITE ANALYSIS            │
├──────────────────────────────────────────┤
│ Repository: org/repo                     │
│                                          │
│ Component Analysis:                      │
│                                          │
│ 1. Auth Module                           │
│    Recommendation: REFACTOR              │
│    Confidence: HIGH                      │
│    Effort: 1 sprint                      │
│    Benefit: Fixes security gaps          │
│    Risk: Low (good test coverage)        │
│                                          │
│ 2. Database Layer                        │
│    Recommendation: REWRITE               │
│    Confidence: MEDIUM                    │
│    Effort: 3 sprints                     │
│    Benefit: 40% performance gain         │
│    Risk: Medium (need extensive testing) │
│                                          │
│ 3. API Routes                            │
│    Recommendation: LEAVE AS-IS           │
│    Confidence: HIGH                      │
│    Reason: Working well, low debt        │
│                                          │
│ Overall Recommendation:                  │
│ • Phase 1: Refactor auth (1 sprint)      │
│ • Phase 2: Rewrite database (3 sprints)  │
│ • Estimated total: 4 sprints             │
└──────────────────────────────────────────┘
```

---

**Step 3: Create modernization roadmap**

```bash
/nightgauge-modernize-plan
```

What happens:

- Consumes outputs from health-check, security-audit, and refactor-rewrite
- Creates a phased roadmap:
  - **Phase 1:** Quick wins (high impact, low effort)
  - **Phase 2:** Core improvements (medium impact/effort)
  - **Phase 3:** Strategic improvements (lower priority)
- For each phase:
  - Lists specific tasks
  - Estimates effort
  - Predicts improvements in health/security scores
  - Identifies dependencies (must do Phase 1 before Phase 2)
- Generates an epic structure ready for pipeline

Expected output:

```
┌──────────────────────────────────────────┐
│  MODERNIZATION ROADMAP GENERATED         │
├──────────────────────────────────────────┤
│ Repository: org/repo                     │
│ Current Health: 72/100                   │
│ Target Health: 88/100                    │
│ Estimated Timeline: 3 months (12 weeks)  │
│                                          │
│ PHASE 1: Quick Wins (2 weeks)            │
│ • Update 8 outdated dependencies         │
│ • Add missing API documentation          │
│ • Fix 3 security misconfigurations       │
│ → Projected score: 78/100                │
│                                          │
│ PHASE 2: Core Improvements (5 weeks)     │
│ • Refactor auth module                   │
│ • Increase test coverage to 85%          │
│ • Improve error handling                 │
│ → Projected score: 84/100                │
│                                          │
│ PHASE 3: Strategic Improvements (5 weeks)│
│ • Rewrite database layer                 │
│ • Performance optimization               │
│ • Architecture documentation             │
│ → Projected score: 88/100                │
│                                          │
│ Roadmap saved to:                        │
│ .nightgauge/logs/roadmap-{date}.md  │
└──────────────────────────────────────────┘
```

---

**Step 4: Create epic for the roadmap**

```bash
/nightgauge-issue-create --type epic
```

What happens:

- Creates a GitHub issue with `type:epic` label
- Includes full roadmap in description
- Links to health/security reports
- Structured for you to create sub-issues for each phase

Example epic:

```markdown
## Summary

Multi-phase modernization roadmap to improve codebase health from 72/100 to 88/100.

## Phases

### Phase 1: Quick Wins (2 weeks)

- [ ] Update 8 outdated dependencies
- [ ] Add missing API documentation
- [ ] Fix 3 security misconfigurations

### Phase 2: Core Improvements (5 weeks)

- [ ] Refactor auth module
- [ ] Increase test coverage to 85%
- [ ] Improve error handling

### Phase 3: Strategic (5 weeks)

- [ ] Rewrite database layer
- [ ] Performance optimization
- [ ] Architecture documentation

## Linked Reports

- Health Check: [link]
- Security Audit: [link]
- Refactor/Rewrite Analysis: [link]
```

---

**Next steps:**

- Create sub-issues for each phase
- Use `nightgauge-queue` to manage phased execution
- Run phases sequentially through the pipeline
- Track progress against health/security scores

---

## Playbook 6: Run Pipeline on an Epic (Batch Execution with Waves)

**When to use this playbook:** You have an epic with multiple sub-issues and want to process them all through the pipeline efficiently. Includes parallel/sequential optimization. Estimated time: 2-8 hours (depending on epic size).

**Prerequisites:**

- Epic issue exists with `type:epic` label
- All sub-issues are linked via GitHub's issue linking (not task lists)
- Sub-issues have proper acceptance criteria
- You've labeled issues with size estimates (small/medium/large)

**Token budget:** Heavy (3,000-6,000+ tokens depending on epic size)

### Flowchart

```
Epic Issue (#100)
        ↓
assess-epic      → Analyze sub-issues for batch vs sequential
        ↓
        ├─→ Sequential path (complex dependencies)
        │        ↓
        │    queue (add all issues)
        │        ↓
        │    issue-pickup (each sub-issue)
        │        ↓
        │    feature-planning (each)
        │        ↓
        │    feature-dev (each)
        │        ↓
        │    pr-create (each)
        │        ↓
        │    pr-merge (each)
        │
        └─→ Parallel/Wave path (independent issues)
                 ↓
             queue (add all issues)
                 ↓
             Parallel Wave 1: feautres-dev 3x
                 ↓
             Parallel Wave 2: pr-create 3x
                 ↓
             Parallel Wave 3: pr-merge 3x
                 ↓
retro            → Analyze results
        ↓
Epic Complete ✓
```

### Steps

**Step 1: Assess epic for processing strategy**

```bash
/nightgauge-assess-epic 100
```

What happens:

- Fetches epic #100 and all linked sub-issues
- Analyzes:
  - File overlap (do issues touch same files?)
  - Size variance (1 tiny, 1 huge = sequential)
  - Dependency signals (blockedBy relationships)
  - Estimated effort
- Recommends:
  - **Batch (parallel waves)** if issues are independent
  - **Sequential** if issues have dependencies or file conflicts
  - **Hybrid** if some waves can be parallel

Expected output:

```
┌──────────────────────────────────────────┐
│  EPIC ASSESSMENT COMPLETE                │
├──────────────────────────────────────────┤
│ Epic: #100 - Major Feature Release       │
│ Sub-Issues: 9 total                      │
│                                          │
│ Size Breakdown:                          │
│ • Small:  4 issues (2-4 hours each)      │
│ • Medium: 3 issues (4-8 hours each)      │
│ • Large:  2 issues (8+ hours each)       │
│                                          │
│ File Overlap: Moderate                   │
│ • Issues 1,2,3 share src/services/       │
│ • Issues 4-6 independent                 │
│ • Issues 7-9 share tests/                │
│                                          │
│ Dependencies: Detected                   │
│ • Issue 2 blocks Issue 3 (blockedBy)     │
│ • Issue 5 blocks Issues 6,7              │
│                                          │
│ RECOMMENDATION: HYBRID SEQUENTIAL        │
│ • Wave 1: Issues 1 (sequential)          │
│ • Issue 2 completes                      │
│ • Wave 2: Issues 3,4 (parallel)          │
│ • Issue 5 completes                      │
│ • Wave 3: Issues 6,7,8,9 (parallel)      │
│                                          │
│ Estimated Total Time: 24-32 hours        │
│ (vs 72 hours if all sequential)          │
└──────────────────────────────────────────┘
```

---

**Step 2: Add issues to queue**

```bash
/nightgauge-queue add 101 102 103 104 105 106 107 108 109
```

Or use interactive mode:

```bash
/nightgauge-queue add
# Shows list of sub-issues, you select which to queue
```

What happens:

- Adds all sub-issues to the processing queue
- Stores queue state in `.nightgauge/pipeline/queue.json`
- Shows queue order and upcoming waves

Expected output:

```
┌──────────────────────────────────────────┐
│  QUEUE UPDATED                           │
├──────────────────────────────────────────┤
│ Queued: 9 issues (epic #100)             │
│                                          │
│ Queue Order:                             │
│ 1. Issue #101 (small, independent)       │
│ 2. Issue #102 (small, depends on #101)   │
│ 3. Issue #103 (medium, parallel OK)      │
│ 4. Issue #104 (medium, parallel OK)      │
│ 5. Issue #105 (large, blocker)           │
│ ... (9 total)                            │
│                                          │
│ Estimated waves: 5 (with parallelization)│
│                                          │
│ Next: Run /nightgauge:issue-pickup  │
│ or use orchestrator for batch execution  │
└──────────────────────────────────────────┘
```

---

**Step 3: Process queue through pipeline**

**Option A: Manual sequential (full control, slower)**

```bash
# Process each issue manually
/nightgauge-issue-pickup 101
/nightgauge-feature-planning
/nightgauge-feature-dev
/nightgauge-feature-validate
/nightgauge-pr-create
/nightgauge-pr-merge

# Then next issue
/nightgauge-issue-pickup 102
# ... repeat for all 9 issues
```

**Option B: Automated orchestration (faster, VSCode Extension)**

Open VSCode and run:

```
Nightgauge: Run Epic Pipeline (Batch Mode)
# Select epic #100
# Extension processes all 9 issues through all stages
# Shows progress bar for each issue
# Handles waves/parallelization automatically
```

Expected output from automated mode:

```
┌────────────────────────────────────────────────┐
│  EPIC PIPELINE EXECUTION IN PROGRESS           │
├────────────────────────────────────────────────┤
│ Epic: #100 - Major Feature Release             │
│ Processing: 9 sub-issues                       │
│                                                │
│ WAVE 1 (Sequential):                           │
│ ✓ #101: issue-pickup complete (30s)            │
│ ✓ #101: feature-planning complete (3m 45s)     │
│ ✓ #101: feature-dev complete (12m)             │
│ ✓ #101: feature-validate complete (5m)         │
│ ✓ #101: pr-create complete (2m)                │
│ ✓ #101: pr-merge complete (4m)                 │
│ ↓ Issue #102 now unblocked                     │
│                                                │
│ WAVE 2 (Parallel - Issues 103,104):            │
│ ✓ #103: feature-dev complete (8m)              │
│ ✓ #104: feature-dev complete (6m)              │
│ → Running feature-validate on both in parallel │
│                                                │
│ WAVE 3 (Sequential - Issue 105):               │
│ → Waiting (blocked until #102 completes)      │
│                                                │
│ Total Time So Far: 45m                         │
│ Estimated Total: 3-4 hours                     │
│                                                │
│ Dashboard: View real-time progress in sidebar  │
└────────────────────────────────────────────────┘
```

---

**Step 4: Analyze results with retrospective**

After all issues are merged:

```bash
/nightgauge-retro
```

What happens:

- Analyzes execution of all 9 issues
- Identifies patterns:
  - Which stages took longest
  - Which issues had failures
  - Common blockers
  - Root causes of any failures
- Produces lessons learned

Expected output:

```
┌────────────────────────────────────────────┐
│  EPIC RETROSPECTIVE                        │
├────────────────────────────────────────────┤
│ Epic: #100 - Major Feature Release         │
│ Issues Processed: 9                        │
│ Success Rate: 8/9 (89%)                    │
│ Total Time: 3h 47m                         │
│                                            │
│ Stage Performance:                         │
│ • issue-pickup: 15m total (2m avg)         │
│ • feature-planning: 42m total (5m avg)     │
│ • feature-dev: 1h 58m total (13m avg)      │
│ • feature-validate: 28m total (3m avg)     │
│ • pr-create: 12m total (1.3m avg)          │
│ • pr-merge: 15m total (1.5m avg)           │
│                                            │
│ Failures (1 total):                        │
│ • Issue #105: feature-validate failed      │
│   Root cause: Test timeout on large file   │
│   Fix: Increased timeout in dev stage      │
│   Time to recovery: 8 minutes              │
│                                            │
│ Lessons Learned:                           │
│ • Large issues (>2k LOC) need extra time   │
│ • Test validation on API changes is slow   │
│ • Parallel wave 3 saved ~30 minutes        │
│                                            │
│ Recommendations:                           │
│ • Split issues >2k LOC into smaller ones   │
│ • Add performance regression tests         │
│ • Increase feature-validate timeout to 10m │
└────────────────────────────────────────────┘
```

---

**What happens next:** Epic is complete, all sub-issues merged, lessons learned documented for next epic.

---

## Playbook 7: Monitor and Improve Pipeline (Self-Improvement Cycle)

**When to use this playbook:** You want to continuously improve your pipeline execution. Periodic cycle (weekly/monthly). Estimated time: 2-3 hours per cycle.

**Prerequisites:**

- Pipeline has been in use for at least a few weeks
- You have execution history and data
- You want to optimize performance/cost

**Token budget:** Moderate-Heavy (1,500-2,500 tokens)

### Flowchart

```
Pipeline Has Been Running
        ↓
pipeline-audit       → Quick snapshot: token usage, cost, stage perf
        ↓
pipeline-health      → Comprehensive analysis (7 dimensions)
        ↓
retro (if failures)  → Root cause on any failures
        ↓
continuous-improvement → Unified improvement review
        ↓
Recommendations → Implement changes
        ↓
Next Cycle ↻
```

### Steps

**Step 1: Quick audit snapshot (weekly)**

```bash
/nightgauge-pipeline-audit
```

What happens:

- Analyzes last 10-20 pipeline executions
- Produces quick snapshot:
  - Token usage per stage
  - Cost per stage
  - Success rate
  - Average time per stage
  - Top issues

Expected output:

```
┌────────────────────────────────────────────┐
│  PIPELINE AUDIT SNAPSHOT                   │
├────────────────────────────────────────────┤
│ Last 10 Runs Analyzed                      │
│                                            │
│ Success Rate: 90% (9 successes, 1 failure)│
│ Total Tokens: 48,500                       │
│ Average Tokens/Run: 4,850                  │
│ Cost (Claude 3.5): $0.73/run (avg)         │
│                                            │
│ Stage Performance (average times):         │
│ • issue-pickup:       2m 15s               │
│ • feature-planning:   4m 30s               │
│ • feature-dev:        8m 45s (longest)    │
│ • feature-validate:   3m 20s               │
│ • pr-create:          1m 50s               │
│ • pr-merge:           2m 10s               │
│                                            │
│ Token Usage by Stage:                      │
│ • feature-dev:    2,400 tokens (49%)      │
│ • feature-planning: 1,200 tokens (25%)    │
│ • others:         1,150 tokens (26%)      │
│                                            │
│ Top Failure:                               │
│ • Build timeout in feature-validate (1/10)│
│                                            │
│ Quick Recommendations:                     │
│ • Increase timeout for large features     │
│ • Optimize feature-dev token usage        │
│ • Add build caching                       │
└────────────────────────────────────────────┘
```

---

**Step 2: Comprehensive health analysis (monthly)**

```bash
/nightgauge-pipeline-health
```

What happens:

- Comprehensive analysis across 7 dimensions:
  1. **Token Economics** — Cost per stage, trending, budget allocation
  2. **Cost Efficiency** — ROI per stage, cost optimization opportunities
  3. **Stage Effectiveness** — Success rate per stage, quality gates
  4. **Model Routing** — Which models used per stage, performance
  5. **Reliability** — Failure patterns, recovery times, MTTR
  6. **Self-Improvement Loop Health** — Are we getting faster/better?
  7. **Velocity** — Issues completed per week, trend

Expected output:

```
┌────────────────────────────────────────────┐
│  PIPELINE HEALTH COMPREHENSIVE             │
├────────────────────────────────────────────┤
│ Last 30 Days of Execution Data             │
│                                            │
│ OVERALL HEALTH SCORE: 82/100               │
│                                            │
│ 1. Token Economics: 78/100                 │
│    • Cost/run trending up (+8% vs month ago)│
│    • feature-dev is 49% of budget          │
│    • Recommendation: Optimize prompts      │
│                                            │
│ 2. Cost Efficiency: 85/100                 │
│    • Average cost/merged PR: $0.73         │
│    • ROI per stage is positive             │
│    • No significant waste detected         │
│                                            │
│ 3. Stage Effectiveness: 88/100             │
│    • Stage success rates: 90-100%          │
│    • Build gate catches 2-3 issues/month   │
│    • Quality gates are effective           │
│                                            │
│ 4. Model Routing: 75/100                   │
│    • Using Claude 3.5 for all stages       │
│    • Consider Claude 3 for lighter stages  │
│    • Potential 15-20% cost savings         │
│                                            │
│ 5. Reliability: 92/100                     │
│    • MTTR: 8 minutes average               │
│    • No cascading failures                 │
│    • Recovery is smooth                    │
│                                            │
│ 6. Self-Improvement Loop: 71/100           │
│    • Not actively measuring or improving   │
│    • No documented lessons learned         │
│    • Retros are informal                   │
│                                            │
│ 7. Velocity: 88/100                        │
│    • 12 PRs merged last month              │
│    • 18 PRs merged previous month          │
│    • Trend: Slightly down (-33%)           │
│    • Likely due to larger features         │
│                                            │
│ Detailed Report: .nightgauge/logs/... │
└────────────────────────────────────────────┘
```

---

**Step 3: Root cause analysis on failures (if any)**

```bash
/nightgauge-retro
```

What happens (if there were failures):

- Analyzes all failed runs from the period
- Classifies failures into 7 categories:
  1. User error
  2. Environment/tools issue
  3. Code quality issue
  4. Timeout/resource limit
  5. External service issue
  6. Pipeline bug
  7. Unclear
- Produces root causes and recommendations

---

**Step 4: Continuous improvement review**

```bash
/nightgauge-continuous-improvement
```

What happens:

- Unified review that orchestrates insights from:
  - Pipeline audit
  - Pipeline health
  - Retro (if applicable)
  - Security/quality audits (if running)
- Produces prioritized improvement list
- Tracks whether previous month's improvements were implemented

Expected output:

```
┌────────────────────────────────────────────┐
│  CONTINUOUS IMPROVEMENT REVIEW             │
├────────────────────────────────────────────┤
│ Month: March 2026                          │
│                                            │
│ PREVIOUS MONTH'S IMPROVEMENTS:             │
│ ✓ Increased feature-validate timeout      │
│   Result: Reduced failures from 3→1/month  │
│ ✓ Added build caching                     │
│   Result: 15% faster feature-dev stage    │
│ ⚠ Optimize feature-dev prompts             │
│   Status: In progress, not yet implemented │
│                                            │
│ THIS MONTH'S RECOMMENDATIONS (prioritized):│
│                                            │
│ 1. Switch lighter stages to Claude 3      │
│    Effort: Low (config change)             │
│    Benefit: 15-20% cost savings            │
│    Impact: High                            │
│                                            │
│ 2. Implement formal lesson documentation  │
│    Effort: Medium (process + tooling)      │
│    Benefit: Faster learning, better sharing│
│    Impact: High                            │
│                                            │
│ 3. Reduce feature-dev context size        │
│    Effort: High (refactor prompts)         │
│    Benefit: Faster execution, cost savings │
│    Impact: High                            │
│                                            │
│ 4. Add pre-flight validation to pr-merge  │
│    Effort: Medium (new checks)             │
│    Benefit: Catch issues before merge      │
│    Impact: Medium                          │
│                                            │
│ Next Month Goals:                          │
│ • Implement #1 (cost savings)              │
│ • Implement #2 (learning loop)             │
│ • Maintain 90%+ success rate               │
│ • Reduce cost per run by 10%               │
└────────────────────────────────────────────┘
```

---

**Step 5: Implement improvements**

Based on recommendations, prioritize and implement:

- **Config changes** (quick):
  - Switch models per stage
  - Adjust timeouts
  - Update prompts

- **Process changes** (medium):
  - Add lesson documentation templates
  - Create retro schedule
  - Set improvement goals

- **Code changes** (longer):
  - Refactor pipeline prompts
  - Add new validation checks
  - Optimize context handling

---

**What happens next:** Run this cycle monthly. Track improvement over time. Celebrate wins (like the timeout increase that reduced failures).

---

## Quick Reference: When to Use Each Playbook

| Playbook               | Scenario                                  | Time      | Token Budget                 |
| ---------------------- | ----------------------------------------- | --------- | ---------------------------- |
| **Fix a Bug**          | Small bug, quick fix, no planning         | 30-60 min | Light (200-400)              |
| **Build a Feature**    | Feature request with planning and testing | 2-4 hours | Moderate (1,000-2,000)       |
| **Onboard Repository** | New repo, first-time setup                | 15-30 min | Light (300-500)              |
| **Audit Codebase**     | Comprehensive health assessment           | 45-90 min | Heavy (2,000-3,500)          |
| **Plan Refactor**      | Tech debt strategy, roadmap               | 1-2 hours | Heavy (2,000-3,000)          |
| **Run Epic Pipeline**  | Batch process multiple issues             | 2-8 hours | Heavy (3,000-6,000+)         |
| **Monitor Pipeline**   | Self-improvement, optimize execution      | 2-3 hours | Moderate-Heavy (1,500-2,500) |

---

## Execution Modes for Playbooks

Each playbook can be run in two ways:

### Manual Mode (CLI)

Run each skill individually using Claude Code:

```bash
/nightgauge-issue-pickup 42
/nightgauge-feature-planning
# Review plan
/nightgauge-feature-dev
# etc
```

**Pros:** Full control, easy to debug, works anywhere Claude Code runs
**Cons:** Must remember stage order, requires explicit approval gates

### Automated Mode (VSCode Extension)

Use the VSCode extension UI:

```
Nightgauge: Run Pipeline
→ Select issue
→ Extension handles all stages with progress tracking
→ Shows token usage and cost in real-time dashboard
```

**Pros:** Fast, visual progress, token tracking, crash recovery
**Cons:** Only works in VSCode, less flexibility

---

## Common Decision Points

### Should I use feature-planning for this issue?

**Use it if:**

- Issue has 3+ acceptance criteria
- Affects multiple files
- Requires architectural decisions
- You want to review the approach before implementing

**Skip it if:**

- Small bug fix (typo, one-liner)
- Trivial feature (add a label, tweak config)
- You're certain of the approach

### Should I use test-gen for this PR?

**Use it if:**

- Code is complex or security-sensitive
- Coverage is below 80%
- You want comprehensive test suite
- Time permits (adds 15-30 minutes)

**Skip it if:**

- Feature dev already added tests
- Code is simple/well-covered
- You're in a hurry

### Should I run the epic pipeline or manual issues?

**Use epic pipeline if:**

- 3+ sub-issues to process
- Issues are independent (or have documented dependencies)
- You want parallel wave optimization
- You want automated retrospective

**Use manual issues if:**

- 1-2 issues total
- Issues have complex dependencies
- You need manual review between each
- You want maximum control

---

## Related Documentation

- [SKILLS_USAGE_GUIDE.md](SKILLS_USAGE_GUIDE.md) — Detailed reference for all skills
- [PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md) — Manual vs automated execution modes
- [ISSUE_TO_PR_WORKFLOW.md](ISSUE_TO_PR_WORKFLOW.md) — Complete pipeline reference
- [CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md) — Context file schemas
- [skills/README.md](../skills/README.md) — Skill catalog and lifecycle

---

## Author

nightgauge
