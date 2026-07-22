# Auto-Issue Creation Workflow

Reference document for Phase 8 (Issue Creation) of the `nightgauge-release-watch` skill. Defines the complete auto-issue creation workflow triggered when `--create-issues` flag is passed to release-watch.

**Version:** 1.0.0
**Author:** nightgauge
**License:** Apache-2.0

---

## Overview

When release-watch identifies changes with high pipeline relevance (score ≥ 70 after full Feature Assessment Engine evaluation, or override rules apply), it automatically creates well-formed GitHub issues to track integration work. This workflow balances automation with safety: dry-run mode allows review before creation, deduplication prevents duplicates, and creation logs track all decisions for auditability.

**Design principle:** Auto-create only for clear high-value features; medium-priority features go to backlog; low-priority features are logged for reference.

---

## Trigger Criteria

Auto-issue creation occurs **only** when all of these conditions are met:

1. **`--create-issues` flag is passed** to `/nightgauge:release-watch --create-issues`
2. **Score ≥ 70 (High priority)** after full Feature Assessment Engine evaluation
   - OR: Override rule applies (breaking change min 60, deprecation min 70, security min 50, model change min 60)
3. **Not a dry-run** (unless `--dry-run` is also passed, which shows preview without creating)
4. **No existing issue** found for this feature (deduplication check passes)

**Decision tree:**

```
Release note change with score ≥ 50 on quick-pass
    ↓
Full Feature Assessment Engine evaluation (see assessment-engine.md)
    ↓
    ├─ Score ≥ 70 → Auto-create issue (High priority)
    ├─ Score 40-69 → Log to backlog file (Medium priority)
    └─ Score < 40 → Log for reference only (Low priority)

Override rules:
    ├─ Breaking change (min 60) → Auto-create if not already (requires migration plan)
    ├─ Deprecation (min 70) → Auto-create if not already (requires migration timeline)
    ├─ Security change (min 50) → Auto-create if not already (requires security review)
    └─ Model availability change (min 60) → Auto-create if not already (requires tuning)
```

---

## Issue Template by Feature Type

All auto-created issues include:

1. **Source tracking** — `source:auto-discovery` label + assessment file link
2. **Version tracking** — Which Claude Code version triggered creation
3. **Score summary** — Composite score and classification
4. **Action items** — Clear next steps (review, assess, implement, document)

### Template 1: Single-Stage Feature (1–2 stages impacted)

Use when a feature primarily impacts 1–2 pipeline stages. Creates a single issue.

```markdown
Title: feat: Integrate Claude Code [feature-name] into [primary-stage] stage

Labels: type:feature, priority:high, size:M, source:auto-discovery, claude-code-release

Body:

## Context

Claude Code [version] introduced [feature-name]. Assessment score: [X]/100 (High).

See [assessment file link] for full analysis.

## Summary

[2-3 sentences from assessment's implementation approach, explaining why this feature matters]

## Feature Description

[1-2 paragraphs from Claude Code release notes explaining what this feature does]

## Acceptance Criteria

- [ ] Feature [feature-name] integrated into [primary-stage] stage workflow
- [ ] Configuration added to `.nightgauge/config.yaml` (if needed)
- [ ] Related SKILL.md files updated (if behavior changed)
- [ ] Documentation updated in `docs/` (if new capability)
- [ ] Tests cover new integration (unit + integration tests)
- [ ] Assessment file updated with implementation status and learnings
- [ ] Feature verified in manual testing before CI submission

## Implementation Notes

[From assessment's implementation approach section]

## References

- Assessment: [skills/nightgauge-release-watch/assessments/[feature].md](./assessments/[feature].md)
- Claude Code release: https://github.com/anthropics/claude-code/releases/tag/v[version]
- Assessment framework: [skills/nightgauge-release-watch/assessment-engine.md](./assessment-engine.md)
- Feature Assessment Template: [skills/nightgauge-release-watch/assessments/TEMPLATE.md](./assessments/TEMPLATE.md)
```

### Template 2: Multi-Stage Feature (3+ stages impacted)

Use when a feature impacts 3+ pipeline stages. Creates an **epic** with sub-issues, one per major stage.

```markdown
Title: feat: Integrate Claude Code [feature-name] across pipeline

Type: Epic
Labels: type:epic, priority:high, source:auto-discovery, claude-code-release

Body:

## Context

Claude Code [version] introduced [feature-name]. Assessment score: [X]/100 (High).

This feature impacts 3+ stages of the pipeline. Creating epic with phase-based sub-issues.

See [assessment file link] for full analysis.

## Overview

[2-3 sentences from assessment]

## Feature Description

[1-2 paragraphs from Claude Code release notes]

## Implementation Phases

### Phase 1: [Stage Name] Integration

**Sub-issue:** feat: Integrate [feature-name] into [stage] stage

**Acceptance Criteria:**

- [ ] [Feature] integrated into [stage] workflow
- [ ] Tests cover [stage]-specific integration
- [ ] [Stage] SKILL.md updated if behavior changed

### Phase 2: [Stage Name] Integration

**Sub-issue:** feat: Integrate [feature-name] into [stage] stage

[Repeat for each affected stage]

### Phase 3: Cross-Stage Validation

**Sub-issue:** test: Cross-stage validation of [feature-name]

**Acceptance Criteria:**

- [ ] End-to-end pipeline execution verifies [feature] works across all stages
- [ ] Regression tests added for feature interaction points
- [ ] Documentation updated with cross-stage guidance

### Phase 4: Release and Monitoring

**Sub-issue:** chore: Release [feature-name] integration and monitor

**Acceptance Criteria:**

- [ ] All phases merged to main
- [ ] Feature released in next version
- [ ] Monitoring in place for common issues

## Epic Completion Criteria

- [ ] All sub-issues completed and merged
- [ ] End-to-end integration verified
- [ ] Documentation complete
- [ ] Assessment file updated with implementation learnings

## References

- Assessment: [skills/nightgauge-release-watch/assessments/[feature].md](./assessments/[feature].md)
- Claude Code release: https://github.com/anthropics/claude-code/releases/tag/v[version]
- Assessment framework: [skills/nightgauge-release-watch/assessment-engine.md](./assessment-engine.md)
```

### Template 3: Breaking Change

Use when a change breaks existing functionality. Requires immediate attention.

```markdown
Title: fix: Address Claude Code [version] breaking change — [description]

Labels: type:bug, priority:critical, size:M, source:auto-discovery, claude-code-release

Body:

## Context

Claude Code [version] introduced a breaking change. Migration required before adoption.

Assessment score: [X]/100 (High, due to override rule for breaking changes).

See [assessment file link] for full analysis.

## Breaking Change Description

[What changed and why]

## Impact Assessment

**Affected components:**

- [Component 1]: [Impact]
- [Component 2]: [Impact]

**User-facing impact:** [Will this break user workflows?]

**Pipeline impact:** [Will this break the pipeline?]

## Migration Path

[Concrete steps to migrate to new behavior]

## Acceptance Criteria

- [ ] All affected components identified and scoped
- [ ] Migration plan documented and approved
- [ ] Code changes implemented for all affected areas
- [ ] Tests updated to reflect new behavior
- [ ] Regression tests added
- [ ] Documentation updated with migration guidance
- [ ] Breaking change communicated to users/team

## Timeline & Deprecation

**Claude Code version:** v[version]
**Expected sunset date:** [When will old behavior stop working?]
**Migration deadline:** [When must we complete migration?]

## References

- Assessment: [skills/nightgauge-release-watch/assessments/[feature].md](./assessments/[feature].md)
- Claude Code release notes: https://github.com/anthropics/claude-code/releases/tag/v[version]
- Assessment framework: [skills/nightgauge-release-watch/assessment-engine.md](./assessment-engine.md)
```

### Template 4: Deprecation

Use when an existing feature is being deprecated. Requires timeline-based migration planning.

```markdown
Title: chore: Migrate from deprecated [feature] before Claude Code [version]

Labels: type:chore, priority:high, size:M, source:auto-discovery, claude-code-release

Body:

## Context

Claude Code will deprecate [feature] in v[version]. We must migrate before sunset date.

Assessment score: [X]/100 (High, due to override rule for deprecations).

See [assessment file link] for full analysis.

## What's Being Deprecated

[What feature is deprecated and why]

## Current Usage in Pipeline

**Affected components:**

- [Component 1]: Uses [deprecated feature] for [purpose]
- [Component 2]: Uses [deprecated feature] for [purpose]

## Migration Strategy

[Recommended replacement and migration path]

## Acceptance Criteria

- [ ] All current usages of [deprecated feature] identified
- [ ] Replacement [feature] evaluated and tested
- [ ] Code changes implemented for all affected areas
- [ ] Tests updated
- [ ] Regression tests added
- [ ] Documentation updated with migration guidance
- [ ] Deprecation cleanup completed before sunset date

## Timeline

**Deprecation announced:** Claude Code v[version]
**Expected sunset date:** [When will feature stop working?]
**Our migration deadline:** [When must we complete?]

## References

- Assessment: [skills/nightgauge-release-watch/assessments/[feature].md](./assessments/[feature].md)
- Claude Code release notes: https://github.com/anthropics/claude-code/releases/tag/v[version]
- Assessment framework: [skills/nightgauge-release-watch/assessment-engine.md](./assessment-engine.md)
```

---

## Deduplication Logic

Before creating an issue, **always check for existing issues** to prevent duplicates.

### Step 1: Search for Existing Issues

```bash
# Search for issues mentioning the feature or version
SEARCH_TERMS="\"[feature-name]\" OR \"v[version]\""
EXISTING=$(gh issue list \
  --search "$SEARCH_TERMS" \
  --label "claude-code-release" \
  --state "open,closed" \
  --json "number,title,state" \
  --limit 10)
```

### Step 2: Calculate Title Similarity

For each found issue:

1. Extract issue title
2. Compare with proposed issue title using string similarity (80%+ match = duplicate)
3. Use fuzzy matching: if 4+ words (length ≥5 chars) appear in both titles, treat as duplicate

Example:

- Proposed: `"feat: Integrate Computer Use into feature-validate stage"`
- Existing: `"feat: Computer Use integration into feature-validate"`
- Match score: 8/9 major words match → **88% match** → **DUPLICATE**

### Step 3: Decision Logic

```
If existing issue found with >80% title similarity:
    ├─ If closed → Create new issue (old one is resolved)
    ├─ If open → SKIP creation, add to creation log as "deduped"
    └─ Log: "Skipped creation for [feature] — similar issue exists (#NNN)"

If existing issue found with lower similarity but overlaps:
    ├─ Different scope → Create new issue, reference existing in body
    └─ Log: "Created issue for [feature], referenced existing #NNN for context"

If no similar existing issue:
    ├─ Create new issue
    └─ Log: "Created issue for [feature] as #NNN"
```

---

## Label Selection Strategy

All auto-created issues include these labels:

1. **`type:*`** — Exactly one, based on feature type:
   - `type:feature` — New capability (default for features)
   - `type:bug` — Breaking change requiring fix
   - `type:chore` — Deprecation migration or maintenance
   - `type:spike` — Requires assessment before implementation (rare)

2. **`priority:*`** — Exactly one, based on score:
   - `priority:critical` — Breaking changes (MUST handle)
   - `priority:high` — Score ≥70 or override rules (fast-track)
   - `priority:medium` — Score 40-69 (backlog, next planning cycle)
   - **Handled by board field after creation, NOT as label**

3. **`size:*`** — Estimated implementation scope:
   - `size:S` — < 2 hours (small bug fix, simple integration)
   - `size:M` — 2–8 hours (typical feature integration)
   - `size:L` — 8+ hours (major feature, multi-stage impact)
   - **Handled by board field after creation, NOT as label**

4. **`source:auto-discovery`** — Auto-created by release-watch
   - Create label if it doesn't exist: `gh label create "source:auto-discovery" --description "Auto-created by release-watch assessment" --color "0e8a16" 2>/dev/null || true`
   - Never manually remove this label (used for tracking and auditing)

5. **`claude-code-release`** — Groups all Claude Code release issues together
   - Create label if it doesn't exist: `gh label create "claude-code-release" --description "Tracks Claude Code release impact and integration" --color "1f6feb" 2>/dev/null || true`

6. **Optional component labels** — If applicable:
   - `component:security` — If security-related change
   - `component:performance` — If performance impact
   - `component:vscode-extension` — If VSCode extension affected
   - Only add if explicitly mentioned in assessment

### Priority & Size Fields on Project Board

After issue creation, **do NOT set Priority and Size as labels**. Instead:

1. Add issue to project board: `nightgauge project add <issue-number>`
2. Set Priority field (High/Medium/Low) via GraphQL
3. Set Size field (S/M/L) via GraphQL

See **Phase 4: Set Board Fields** in the release-watch skill workflow for mutation examples.

---

## Board Sync Process

After creating an issue:

```bash
# Step 1: Add issue to project board
ISSUE_NUMBER=<created-issue-number>
nightgauge project add "$ISSUE_NUMBER"

# Step 2: Set Priority field
# High = score >= 70 or override rule applies
# Medium = score 40-69 (only in creation log, not created as issue)
nightgauge project set-field "$ISSUE_NUMBER" Priority "High"

# Step 3: Set Size field
# S = <2 hours, M = 2-8 hours, L = 8+ hours
nightgauge project set-field "$ISSUE_NUMBER" Size "M"

# Step 4: Set Status to Ready (for backlog item)
nightgauge project sync-status "$ISSUE_NUMBER" ready

# Log success
echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) | Created issue #$ISSUE_NUMBER on board" >> creation-log.json
```

---

## Source Tracking & Assessment Files

Every auto-created issue includes a link to its assessment file for traceability.

### Assessment File Naming Convention

Store assessments at: `.nightgauge/release-watch/assessments/[feature-name].md`

Example paths:

- `.nightgauge/release-watch/assessments/computer-use.md`
- `.nightgauge/release-watch/assessments/scheduled-tasks.md`
- `.nightgauge/release-watch/assessments/agent-teams.md`

### Assessment File Content

Use the template from `skills/nightgauge-release-watch/assessments/TEMPLATE.md`. Must include:

1. Feature description
2. All six dimension scores
3. Composite score and classification
4. Stage-by-stage impact breakdown
5. Implementation approach with concrete phases
6. Risks & concerns
7. Decision rationale

### Linking from Issue to Assessment

In issue body, include:

```markdown
## Assessment

See [Assessment Document](../../skills/nightgauge-release-watch/assessments/[feature-name].md) for complete analysis.

**Score:** [X]/100 ([High/Medium/Low])
**Dimensions:**

- Pipeline Stage Impact: [score]/30
- Automation Potential: [score]/20
- Safety & Reliability: [score]/15
- Developer Experience: [score]/15
- Implementation Complexity: [score]/10
- Cross-Repo Applicability: [score]/10
```

---

## Dry-Run Mode

When `--create-issues --dry-run` are both passed:

1. **Identify what WOULD be created** (same deduplication checks, same templates)
2. **Show preview** of each issue (title, labels, first 500 chars of body)
3. **Don't create anything** — no issues, no board syncs, no log updates
4. **Output structured preview** for review:

```markdown
# Dry-Run Preview: Issues That Would Be Created

## Release: Claude Code v[version]

### 1. feat: Integrate [feature-name] into [stage]

**Would create as:** type:feature, priority:high, size:M, source:auto-discovery
**Would add to board:** Yes (Priority=High, Size=M, Status=Ready)
**Assessment score:** 75/100 (High)

Preview:

> Context: Claude Code v[version] introduced [feature-name]...
> [First 500 chars of body]
> ...

### 2. [Additional issues...]

---

## Summary

- **Total issues to create:** 2
- **Deduped (existing issues found):** 1
- **Dry-run mode:** Yes (no changes made)

To actually create these issues, run: `/nightgauge:release-watch --create-issues`
```

---

## Safety Rails

Prevent spam and unintended behavior:

### Rail 1: Maximum Issues Per Release

**Limit: 3 issues per release**

If a release has many high-scoring changes:

1. Rank by score (highest first)
2. Create issues for top 3 changes
3. Log remaining high-scoring changes to backlog file for manual review

Example log:

```json
{
  "release": "v2.1.82",
  "issues_created": 3,
  "capped_at": 3,
  "remaining_high_priority": [
    {
      "feature": "feature-4",
      "score": 72,
      "reason": "capped at 3 issues per release"
    }
  ]
}
```

### Rail 2: Never Create Duplicate Issues

Deduplication check (see earlier section) prevents creating same issue twice.

### Rail 3: Always Show Dry-Run First

Require review before creation:

- If `--create-issues` passed without `--dry-run`, automatically run dry-run first
- Show preview and ask: "Create these X issues? (y/n)"
- Only proceed if user confirms (or if running in non-interactive CI mode with explicit `--no-confirm`)

### Rail 4: Creation Log for Auditability

Track every creation decision in `.nightgauge/release-watch/creation-log.json`:

```json
{
  "entries": [
    {
      "date": "2026-03-24T15:30:45Z",
      "release_version": "2.1.82",
      "feature": "computer-use-enhancements",
      "assessment_score": 75,
      "action": "created",
      "issue_number": 2410,
      "issue_url": "#2410",
      "dry_run": false
    },
    {
      "date": "2026-03-24T15:30:45Z",
      "release_version": "2.1.82",
      "feature": "scheduled-tasks",
      "assessment_score": 48,
      "action": "backlog",
      "reason": "score_in_medium_range",
      "dry_run": false
    },
    {
      "date": "2026-03-24T15:30:45Z",
      "release_version": "2.1.82",
      "feature": "permission-relay",
      "assessment_score": 52,
      "action": "deduped",
      "existing_issue": 2387,
      "reason": "similar_to_existing",
      "dry_run": false
    }
  ]
}
```

**Log fields:**

- `date` — When decision was made (ISO 8601 UTC)
- `release_version` — Claude Code version
- `feature` — Feature name
- `assessment_score` — Final composite score (0-100)
- `action` — One of: `created`, `backlog`, `deduped`, `skipped`, `error`
- `issue_number` — If created, the GitHub issue number
- `issue_url` — If created, full GitHub issue URL
- `reason` — Why action was taken (e.g., "score_too_low", "similar_to_existing", "breaking_change")
- `existing_issue` — If deduped, the number of similar existing issue
- `dry_run` — Whether this was a dry-run (no actual creation)

### Rail 5: Error Recovery

If issue creation fails (network error, auth failure, etc.):

1. Log the error to creation-log.json with `"action": "error"`
2. Print clear error message with remediation
3. **Do NOT continue** creating remaining issues (fail fast)

Example error log:

```json
{
  "date": "2026-03-24T15:30:45Z",
  "release_version": "2.1.82",
  "feature": "computer-use",
  "assessment_score": 75,
  "action": "error",
  "error_message": "GitHub API rate limit exceeded (60/60 requests). Try again in 1 hour.",
  "remediation": "Run: gh auth refresh"
}
```

---

## Example Workflow: End-to-End

Scenario: Release-watch detects Claude Code v2.1.82 with 2 high-scoring changes.

```bash
/nightgauge:release-watch --create-issues --dry-run
```

Output:

```
# Release Watch: Dry-Run Preview

## Claude Code v2.1.82 (2026-03-24)

### 1. feat: Integrate Computer Use into feature-validate stage
Type: type:feature, priority:high, size:M
Assessment Score: 75/100 (High)
Labels: type:feature, source:auto-discovery, claude-code-release
Board: Priority=High, Size=M, Status=Ready
Dedup Check: ✓ No existing issues found

Preview:
> Context: Claude Code v2.1.82 introduced Computer Use.
> Assessment score: 75/100 (High).
> [... rest of body ...]

### 2. chore: Migrate from deprecated --bare flag before v2.2.0
Type: type:chore, priority:high, size:S
Assessment Score: 72/100 (High, deprecation override)
Labels: type:chore, source:auto-discovery, claude-code-release
Board: Priority=High, Size=S, Status=Ready
Dedup Check: ⚠ Similar issue found: #2401 (issue-pickup refactor)
   [Would reference #2401 in body, not create duplicate]

Summary:
--------
Total to create: 1 new issue
Total to reference: 1 (deduped)
Release safety rail: ✓ Within limit (2 ≤ 3)

Dry-run mode: YES (no changes made)

Create these issues? [y/n] y
```

User confirms with `y`:

```bash
/nightgauge:release-watch --create-issues
```

Output:

```
Creating issues from release-watch assessment...

✓ Created #2410: feat: Integrate Computer Use into feature-validate stage
  - Added to project board (Priority=High, Size=M, Status=Ready)
  - Labels: type:feature, source:auto-discovery, claude-code-release

✓ Created #2411: chore: Migrate from deprecated --bare flag before v2.2.0
  - Added to project board (Priority=High, Size=S, Status=Ready)
  - Referenced existing issue #2401 for context
  - Labels: type:chore, source:auto-discovery, claude-code-release

✓ Updated creation log

Done! 2 issues created, 0 deduped, 0 backlogged.
Latest release: v2.1.82
Last-seen: v2.1.82 (2026-03-24T15:35:20Z)

Next steps:
1. Review created issues in GitHub
2. Create assessment files for high-priority features
3. Assign issues to team members
4. Update relevant SKILL.md files once implementation is underway
```

Creation log updated:

```json
{
  "entries": [
    {
      "date": "2026-03-24T15:35:20Z",
      "release_version": "2.1.82",
      "feature": "computer-use-enhancements",
      "assessment_score": 75,
      "action": "created",
      "issue_number": 2410,
      "issue_url": "#2410",
      "dry_run": false
    },
    {
      "date": "2026-03-24T15:35:20Z",
      "release_version": "2.1.82",
      "feature": "bare-flag-deprecation",
      "assessment_score": 72,
      "action": "created",
      "issue_number": 2411,
      "issue_url": "#2411",
      "referenced_issue": 2401,
      "dry_run": false
    }
  ]
}
```

---

## Backlog File Format

For medium-priority changes (score 40-69) that are NOT auto-created as issues, log to backlog file:

**File:** `.nightgauge/release-watch/backlog.json`

```json
{
  "backlog_entries": [
    {
      "date": "2026-03-24T15:35:20Z",
      "release_version": "2.1.82",
      "feature": "scheduled-tasks",
      "assessment_score": 48,
      "classification": "Medium",
      "short_description": "New --scheduled-run flag for automated task execution",
      "stage_impact": "issue-pickup (0), feature-planning (0), feature-dev (0), feature-validate (0), pr-create (0), pr-merge (0)",
      "automation_potential": 18,
      "notes": "Strong automation potential but low stage-specific impact. Consider for next planning cycle.",
      "assessment_file": "skills/nightgauge-release-watch/assessments/scheduled-tasks.md",
      "next_review_date": "2026-04-24"
    }
  ]
}
```

Review backlog monthly during planning cycles to identify features ready for next iteration.

---

## Integration with Release-Watch SKILL.md

This document (`auto-issue-creation.md`) is referenced from Phase 8 of the release-watch skill. The skill's Phase 8 code should:

1. Load JSON report from Phase 6 (scored releases)
2. For each release and each change:
   - Check if score ≥ 70 or override rule applies
   - Run deduplication check
   - Build issue from appropriate template
   - If `--create-issues` (not `--dry-run`): create issue + sync to board + log
   - If `--dry-run`: show preview + log (no creation)
   - If score 40-69: log to backlog + skip creation
   - If score < 40: skip and log reference

3. Show summary output with counts created/deduped/backlogged/skipped
4. Update creation log with all decisions

**See:** `skills/nightgauge-release-watch/SKILL.md` Phase 8 for implementation code.

---

## Error Handling & Troubleshooting

### Error: "GitHub API rate limit exceeded"

**Cause:** Too many API calls to GitHub (gh CLI hits rate limit)

**Fix:**

```bash
# Refresh auth token
gh auth refresh

# Wait for rate limit to reset (typically 1 hour)
gh api rate_limit | jq '.resources.core'

# Retry creation
/nightgauge:release-watch --create-issues
```

### Error: "gh CLI not authenticated"

**Cause:** `gh` CLI cannot authenticate to GitHub

**Fix:**

```bash
gh auth login

# Select GitHub.com
# Select SSH key or HTTPS
# Authorize device code
```

### Error: "Cannot add issue to board — project not found"

**Cause:** Issue board not configured for this repo

**Fix:**

```bash
# Sync project board first
/nightgauge:project-sync

# Then retry issue creation
/nightgauge:release-watch --create-issues
```

### Issue Created But Board Not Updated

**Cause:** `nightgauge project add` or field mutations failed

**Symptom:** Issue exists but not visible in project board views

**Fix:**

```bash
# Manually re-sync issue to board
nightgauge project add <issue-number>

# Re-set fields
nightgauge project set-field <issue-number> Priority "High"
nightgauge project set-field <issue-number> Size "M"
nightgauge project sync-status <issue-number> ready

# Verify
gh issue view <issue-number>
```

---

## Implementation Notes for Release-Watch Skill

When implementing Phase 8 in the release-watch SKILL.md, follow these patterns:

1. **Template selection:** Use switch/case on feature type and score to select template
2. **Variable substitution:** Replace `[feature-name]`, `[version]`, `[stage]`, etc. with actual values
3. **Label creation:** Create labels if they don't exist before assigning:
   ```bash
   gh label create "source:auto-discovery" --description "..." --color "0e8a16" 2>/dev/null || true
   ```
4. **Dry-run preview:** Build and display preview before actual creation
5. **Interactive confirmation:** Ask user to confirm before creating (unless `--no-confirm` in CI)
6. **Error handling:** Log all errors and continue with next change (don't fail entire operation)
7. **Logging:** Write creation-log.json atomically (update in place, don't delete/recreate)

---

## Testing the Auto-Issue Creation

### Manual Testing

```bash
# 1. Create test environment
cd /path/to/nightgauge
mkdir -p .nightgauge/release-watch/assessments

# 2. Run dry-run with recent releases
/nightgauge:release-watch --create-issues --dry-run

# 3. Review preview output
# 4. If correct, run without dry-run
/nightgauge:release-watch --create-issues

# 5. Verify issues created
gh issue list --label claude-code-release --limit 5

# 6. Check creation log
cat .nightgauge/release-watch/creation-log.json | jq '.entries[-1]'
```

### Deduplication Testing

```bash
# 1. Create test issue
gh issue create --title "feat: Integrate test-feature into stage" \
  --body "Test issue" \
  --label "claude-code-release"

# 2. Run release-watch again (should dedupe)
/nightgauge:release-watch --create-issues --dry-run

# Should show: "Similar issue found: #NNN"
```

### Error Handling Testing

```bash
# 1. Revoke gh auth temporarily
gh auth logout

# 2. Run release-watch
/nightgauge:release-watch --create-issues

# Should error: "gh CLI not authenticated"

# 3. Re-authenticate
gh auth login

# 4. Retry
/nightgauge:release-watch --create-issues
```

---

## References

- **Release-Watch SKILL.md:** [skills/nightgauge-release-watch/SKILL.md](./SKILL.md)
- **Feature Assessment Engine:** [skills/nightgauge-release-watch/assessment-engine.md](./assessment-engine.md)
- **Assessment Template:** [skills/nightgauge-release-watch/assessments/TEMPLATE.md](./assessments/TEMPLATE.md)
- **Issue-Create SKILL.md:** [skills/nightgauge-issue-create/SKILL.md](../nightgauge-issue-create/SKILL.md)
- **Configuration Reference:** [docs/CONFIGURATION.md](../../docs/CONFIGURATION.md)
- **Project Board Sync:** [docs/PROJECT_SETUP.md](../../docs/PROJECT_SETUP.md)

---

**Author:** nightgauge
**Version:** 1.0.0
**License:** Apache-2.0
**Last Updated:** 2026-07-21
