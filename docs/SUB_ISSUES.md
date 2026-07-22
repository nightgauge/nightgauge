# GitHub Sub-Issues with Nightgauge

## Overview

GitHub's native sub-issues feature allows you to create parent-child
relationships between issues. Nightgauge integrates with this to provide:

- Epic tracking through parent issues
- Progress visualization in the Roadmap view
- Automatic parent linking in PR descriptions
- Epic auto-completion when all sub-issues close

Sub-issues appear in GitHub's issue sidebar and are tracked separately from the
older `type:epic` label approach.

## Creating Sub-Issues

### Using the VSCode Extension

1. Open the Command Palette (`Cmd+Shift+P` / `Ctrl+Shift+P`)
2. Run `Nightgauge: Create Issue`
3. Fill in the issue details
4. In the **Parent Issue** field, enter the parent issue number (e.g., `295`)
5. Submit to create the sub-issue

The extension automatically:

- Creates the GitHub sub-issue relationship
- Adds the issue to your project board
- Sets appropriate labels based on configuration

### Using the CLI

```bash
/nightgauge:issue-create --parent 295
```

This will:

1. Prompt for issue title, description, and labels
2. Create the issue with parent linkage via GitHub API
3. Add the issue to the project board with proper field mappings

### Manual Creation

You can also create sub-issues directly in GitHub:

1. Open the parent issue
2. Click "Add sub-issue" in the right sidebar
3. Create or link an existing issue

Then sync to the project board:

```bash
claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>
```

## Viewing Epic Progress

### Roadmap View (VSCode Extension)

The Roadmap view groups issues by epic and shows completion status:

```
📋 Epic: Authentication System (#295)
  Progress: 60% (3/5 complete)
  ├── ✅ Add JWT middleware (#301)
  ├── ✅ Create login endpoint (#302)
  ├── ✅ Add password hashing (#303)
  ├── 🔄 Implement session storage (#304)
  └── ⏸️  Add OAuth provider support (#305)
```

**Key Features**:

- Visual progress bars showing completion percentage
- Status icons (✅ Done, 🔄 In Progress, ⏸️ Ready, 🚫 Blocked)
- Automatic updates as sub-issues change status
- Click any issue to view details or start work

### Dashboard View

The Dashboard shows epic-level metrics:

- Total epics in progress
- Average completion rate
- Estimated time remaining (based on sub-issue sizes)

## Parent-Child Relationships in PRs

When creating a PR for a sub-issue, Nightgauge automatically includes
parent context in the PR description.

### Example PR Description

```markdown
## Summary

Implements JWT middleware for authentication.

**Part of**: #295 — Authentication System

## Changes

- Added JWT verification middleware
- Created token refresh endpoint
- Updated route guards

## Testing

- [x] Unit tests pass
- [x] Integration tests with auth flow
- [x] Manual testing with Postman
```

The `**Part of**: #295` line links to the parent epic, providing reviewers with
context about the broader feature scope.

### How It Works

The `pr-create` skill:

1. Reads the issue metadata from the context file
2. Checks for parent relationships via GitHub API
3. Includes the parent reference in the PR template
4. Links the PR back to both the sub-issue and parent epic

## Epic Merge Strategy

Epics use a two-tier merge strategy:

1. **Sub-issue PRs → epic branch**: Squash merge (`pr.merge_strategy`, default:
   `squash`). Each sub-issue becomes one clean commit on the epic branch.
2. **Epic branch → main**: Regular merge commit (`pr.epic_merge_strategy`,
   default: `merge`). Preserves all sub-issue commits on main.

This means each sub-issue remains independently revertable and bisectable on
main, while keeping the epic branch history clean during development.

See [GIT_WORKFLOW.md](GIT_WORKFLOW.md#epic-merge-strategy) for configuration
details.

## Best Practices for Epic Breakdown

### When to Use Parent-Child vs Labels

| Approach       | Use For                          | Example                 |
| -------------- | -------------------------------- | ----------------------- |
| **Sub-issues** | Related work with shared context | Authentication system   |
| **Labels**     | Cross-cutting concerns           | `tech-debt`, `security` |
| **Milestones** | Time-boxed releases              | `v2.0`, `Q1-2026`       |

### Structuring Epics

**Good Epic Structure**:

```
Epic #295: Authentication System
├── #301: Add JWT middleware (size:S, priority:high)
├── #302: Create login endpoint (size:S, priority:high)
├── #303: Add password hashing (size:S, priority:high)
├── #304: Implement session storage (size:M, priority:medium)
└── #305: Add OAuth provider support (size:L, priority:low)
```

**Best Practices**:

- Keep epics focused (3-8 sub-issues ideal)
- Size sub-issues consistently (prefer S/M over XL)
- Prioritize sub-issues independently
- Make sub-issues independently testable
- Avoid deep nesting (stick to one level: parent → child)

### Epic Size Guidelines

| Epic Size  | Sub-Issues | Total Story Points | Timeline   |
| ---------- | ---------- | ------------------ | ---------- |
| **Small**  | 2-4        | 5-13               | 1-2 weeks  |
| **Medium** | 5-8        | 13-21              | 2-4 weeks  |
| **Large**  | 9-15       | 21-40              | 1-2 months |

Larger epics should be broken into multiple medium-sized epics.

## Epic Display in Board Views

When epic grouping is enabled, the project board tree views (Ready, Backlog, In
Progress, etc.) group sub-issues under their parent epic header. Each sub-issue
appears in **exactly one tab** matching its actual project board status — no
duplication across tabs. For sequential epic phases, set **all sub-issues to
"Ready"** — the pipeline uses `blockedBy` relationships to enforce ordering, not
board status. Only use "Backlog" for issues genuinely not ready for work.

**Ready tab** (all sub-issues set to "Ready", blocking enforced by `blockedBy`):

```
📋 Epic: Test Quality Audit (#1819)
  ├── #1821 Phase 1 — Contract tests
  ├── 🔒 #1822 Phase 2 — Config parsing tests (blocked)
  ├── 🔒 #1823 Phase 3 — IPC protocol tests (blocked)
  ├── 🔒 #1824 Phase 4 — GitHub API schema tests (blocked)
  ├── 🔒 #1825 Phase 5 — E2E smoke tests (blocked)
  └── 🔒 #1826 Phase 6 — Audit low-value tests (blocked)
```

The lock (🔒) icons indicate issues blocked by open dependencies (GitHub's
native `blockedBy` relationships). The pipeline uses `blockedBy` to enforce
sequential ordering — NOT board status.

**Implementation**: `groupIssuesByEpic()` in `EpicGroupTreeItem.ts` groups the
status-filtered issues by `epicRef`. The `allItems` parameter is only used to
resolve epic metadata (title, URL).

## Native Sub-Issues Requirement

**The Nightgauge pipeline requires GitHub's native sub-issue feature for
epic grouping.** The project board tree view queries the GraphQL
`subIssues(first: 50)` field on each issue to detect epics and build
parent-child groupings. Issues with sub-issues are marked `isEpic: true`. The
parent relationship is derived from the epic's sub-issue list — there is no
`parentIssue` field in the GitHub GraphQL API.

### What counts as a native sub-issue

A native sub-issue is created via one of:

- **`create-sub-issue.sh`** — creates a new issue and links it as a sub-issue in
  one step (uses the `addSubIssue` GraphQL mutation)
- **`link-sub-issue.sh`** — links an existing issue as a sub-issue of a parent
  (uses the same `addSubIssue` mutation)
- **GitHub UI** — clicking "Add sub-issue" in the parent issue's sidebar
- **Direct GraphQL** — calling the `addSubIssue` mutation

### What does NOT count

Adding "Part of #X" text to an issue body does **not** create a native sub-issue
link. While `create-sub-issue.sh` adds this text for human readability, it is
the `addSubIssue` mutation that creates the actual relationship.

### Migrating legacy repos

Repositories that used the body-based "Part of #X" pattern before native
sub-issues were adopted can be migrated using the migration script:

```bash
# Preview what will be linked
./scripts/migrate-body-to-native-sub-issues.sh --repo <owner/repo> --dry-run

# Run the migration
./scripts/migrate-body-to-native-sub-issues.sh --repo <owner/repo>

# Include closed issues too
./scripts/migrate-body-to-native-sub-issues.sh --repo <owner/repo> --include-closed
```

The script parses "Part of #X" from issue bodies and creates native sub-issue
links via the `addSubIssue` mutation. It is idempotent — already-linked issues
are skipped.

## Troubleshooting

### Sub-Issue Not Showing in Roadmap

**Symptoms**: Created a sub-issue but it doesn't appear under the parent in the
Roadmap view.

**Causes**:

1. **Not added to project board**: Run sync script manually:

   ```bash
   claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>
   ```

2. **Parent issue lacks `type:epic` label**: Add the label:

   ```bash
   gh issue edit <parent-number> --add-label "type:epic"
   ```

3. **VSCode cache outdated**: Refresh the view:
   - Click the refresh icon in the Roadmap view header
   - Or restart VSCode

### Epic Not Auto-Closing

**Symptoms**: All sub-issues are closed but the epic remains open.

**Causes**:

1. **Epic body doesn't reference sub-issues**: Add references:

   ```markdown
   ## Sub-Issues

   - #301
   - #302
   - #303
   ```

2. **`check-epic-completion.sh` not running**: Verify the `pr-merge` skill
   includes Step 7.2.5 (Epic completion check). If using an older version,
   update to the latest skill.

3. **Manual intervention needed**: Close the epic manually:

   ```bash
   gh issue close <epic-number> --comment "All sub-issues complete"
   ```

### Parent Link Missing in PR

**Symptoms**: PR description doesn't show "Part of: #295".

**Causes**:

1. **Context file missing parent metadata**: Re-run `issue-pickup`:

   ```bash
   /nightgauge:issue-pickup <issue-number>
   ```

2. **Using older `pr-create` version**: Update to the latest skill version that
   supports parent references.

3. **Parent relationship not in GitHub API**: Verify in GitHub UI that the
   sub-issue relationship exists. If not, manually add it and re-create the PR.

## Related Documentation

- [docs/ARCHITECTURE.md](ARCHITECTURE.md#epic-handling) — Epic handling
  architecture
- [skills/nightgauge-issue-create/SKILL.md](../skills/nightgauge-issue-create/SKILL.md)
  — Issue creation with `--parent` flag
- [skills/nightgauge-pr-create/SKILL.md](../skills/nightgauge-pr-create/SKILL.md)
  — PR creation with parent linking

## Author

nightgauge
