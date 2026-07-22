# Nightgauge - AI-Augmented SDLC Framework

An enforceable software development lifecycle (SDLC) framework that combines
agentic workflows with deterministic quality gates. Nightgauge takes you
from GitHub issue to merged PR with automated quality enforcement at every step.

## What Makes Nightgauge Different

Traditional AI coding assistants rely on the LLM to _choose_ to follow best
practices. Nightgauge uses **Claude Code hooks** to _enforce_ them:

| Traditional AI           | Nightgauge                              |
| ------------------------ | --------------------------------------- |
| LLM might format code    | Hooks **always** format code            |
| LLM might check versions | Hooks **block** commits on mismatch     |
| LLM might follow plan    | Hooks **verify** plan completion        |
| Context lost on resume   | Hooks **restore** context automatically |

## Using Nightgauge in Any Repository

Once installed, Nightgauge works in **any Git repository**. The plugin is
installed globally to your Claude Code environment, so you can use it wherever
you run Claude Code.

```bash
# Navigate to any repository
cd ~/projects/my-app

# Start the pipeline
claude
> /nightgauge:issue-pickup 42
```

**That's it.** The hooks automatically:

- Detect your current branch and linked issue
- Restore context when you resume sessions
- Enforce quality gates on every operation
- Format code after edits

No per-repository configuration needed.

## Installation

### Add the Marketplace

```bash
# Add the Nightgauge plugins marketplace
claude plugin marketplace add https://github.com/nightgauge/nightgauge.git
```

### Install the Plugin

```bash
# Install Nightgauge Framework
claude plugin install nightgauge@nightgauge-plugins
```

### Manual Configuration

Or add to your `~/.claude/settings.json`:

```json
{
  "enabledPlugins": {
    "nightgauge@nightgauge-plugins": true
  },
  "extraKnownMarketplaces": {
    "nightgauge-plugins": {
      "source": {
        "source": "git",
        "url": "https://github.com/nightgauge/nightgauge.git"
      }
    }
  }
}
```

### Verify Installation

After installing, verify the plugin and hooks are working:

```bash
# 1. Check plugin is installed
claude plugin list | grep nightgauge

# 2. Verify hooks directory exists
ls ~/.claude/plugins/nightgauge@nightgauge-plugins/hooks/

# 3. Test a command (should show help)
claude -p "Show me the /nightgauge:issue-pickup help"
```

> **Note**: Hooks are automatically loaded when the plugin is installed. No
> separate hook installation is required.

## Pipeline Commands

| Command                        | Description                                              |
| ------------------------------ | -------------------------------------------------------- |
| `/nightgauge:issue-create`     | Create a well-structured GitHub issue from a description |
| `/nightgauge:issue-pickup`     | Claim a GitHub issue and create feature branch           |
| `/nightgauge:feature-planning` | Design implementation with documentation-first approach  |
| `/nightgauge:feature-dev`      | Implement features following approved plan               |
| `/nightgauge:test-gen`         | Generate comprehensive tests with coverage analysis      |
| `/nightgauge:pr-create`        | Create pull request with proper format                   |

## The Pipeline

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     NIGHTGAUGE AI-AUGMENTED SDLC PIPELINE                       │
└─────────────────────────────────────────────────────────────────────────────┘

  ┌──────────────┐    ┌──────────────┐    ┌───────────────────┐
  │ issue-create │ → │ issue-pickup │ → │ feature-planning  │ → ...
  └──────────────┘    └──────────────┘    └───────────────────┘
        │                   │                     │
        ▼                   ▼                     ▼
   • Parse request      • Fetch issue         • Read docs/
   • Structure issue    • Extract reqs        • Map patterns
   • Add labels         • Create branch       • Create PLAN.md
   • Create on GitHub   • Set up env          • Get approval

                  ... → ┌─────────────┐    ┌──────────┐    ┌───────────┐
                        │ feature-dev │ → │ test-gen │ → │ pr-create │
                        └─────────────┘    └──────────┘    └───────────┘
                              │                  │               │
                              ▼                  ▼               ▼
                         • Load plan        • Analyze code   • Run tests
                         • Apply standards  • Gen tests      • Generate desc
                         • Write code       • Coverage gaps  • Link issues
                         • Quality review   • Edge cases     • Request review

┌─────────────────────────────────────────────────────────────────────────────┐
│  HOOKS: Enforceable quality gates run automatically at each step            │
│  ────────────────────────────────────────────────────────────────────────── │
│  ✓ Auto-format    ✓ Version check    ✓ Workflow gates    ✓ Context restore │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Hooks: Enforceable Quality Gates

Nightgauge includes Claude Code hooks that enforce quality standards
automatically:

### Hook Categories

| Hook                  | Type           | Purpose                                    |
| --------------------- | -------------- | ------------------------------------------ |
| **Notifications**     | `Notification` | Desktop alerts when Claude needs attention |
| **Auto-Format**       | `PostToolUse`  | Format code after every Edit/Write         |
| **Version Check**     | `PostToolUse`  | Warn on plugin.json/SKILL.md mismatch      |
| **Workflow Gates**    | `PreToolUse`   | Block dangerous operations                 |
| **Completion Verify** | `Stop`         | Ensure tasks complete before stopping      |
| **Context Restore**   | `SessionStart` | Restore plan/issue context on resume       |

### Workflow Gates (PreToolUse)

The following operations are automatically blocked:

| Blocked Operation             | Reason                          |
| ----------------------------- | ------------------------------- |
| `git push origin main`        | Direct push to protected branch |
| `git push --force`            | Force push can destroy history  |
| `git reset --hard`            | Destructive operation           |
| Edit `.env`, `*.key`, `*.pem` | Sensitive file protection       |
| `cat .env` via Bash           | Secrets exposure prevention     |

**Example: What a blocked operation looks like**

```
> Running: git push origin main

⚠️ Hook blocked this action:
   Direct push to main/master blocked. Use the PR workflow with /nightgauge:pr-create.

The operation was not executed. Please use the recommended workflow instead.
```

### Customizing Hooks

Hooks can be disabled or configured via environment variables:

```bash
# Disable specific hooks
export NIGHTGAUGE_SKIP_FORMAT=1      # Skip auto-formatting
export NIGHTGAUGE_SKIP_NOTIFY=1      # Skip notifications
export NIGHTGAUGE_SKIP_GATES=1       # Skip workflow gates
export NIGHTGAUGE_SKIP_CONTEXT=1     # Skip context injection
export NIGHTGAUGE_SKIP_VERSION_CHECK=1  # Skip version checks

# Enable debug logging
export NIGHTGAUGE_HOOKS_DEBUG=1      # Verbose output for troubleshooting
```

### Hooks File Structure

```
hooks/
├── hooks.json              # Hook configuration
├── validate-hooks.sh       # Validate hook setup
├── check-dependencies.sh   # Check required/optional dependencies
├── lib/                    # Pipeline scripts (empty — moved to Go binary)
├── notify.sh               # Desktop notifications
├── format-on-save.sh       # Auto-formatting
├── version-check.sh        # Version consistency
├── workflow-gate.sh        # PreToolUse gates
├── inject-context.sh       # Context persistence
└── tests/
    ├── run-all-tests.sh          # Run complete test suite
    ├── test-common.sh            # Unit tests for common.sh
    └── test-hooks-integration.sh # Integration tests
```

**Validate your setup:**

```bash
./hooks/validate-hooks.sh
```

This checks syntax, permissions, dependencies, and platform compatibility.

**Run tests:**

```bash
./hooks/tests/run-all-tests.sh
```

Runs 69 tests covering unit tests for utility functions and integration tests
for hook behavior.

## Complete Workflow Example

This example shows the actual workflow used to add CHANGELOG.md files to the
nightgauge skills - a real feature developed entirely through this
pipeline.

### Step 1: Pick Up the Issue

```
> /nightgauge:issue-pickup 13

┌─────────────────────────────────────────────────────────────────┐
│  ISSUE PICKUP COMPLETE                                          │
└─────────────────────────────────────────────────────────────────┘

Issue:    #13 - Add CHANGELOG.md files to nightgauge skills
Type:     documentation
Branch:   docs/13-nightgauge-changelog
Status:   Ready for development

## Requirements Summary

Add dedicated CHANGELOG.md files to each nightgauge skill directory
following the Keep a Changelog format.

### Acceptance Criteria
- [ ] Create CHANGELOG.md in each nightgauge skill directory
- [ ] Follow Keep a Changelog format
- [ ] Document initial 1.0.0 release
- [ ] Update skills/README.md
- [ ] Update CONTRIBUTING.md

## Next Steps
1. Run `/nightgauge:feature-planning` to design the implementation
```

### Step 2: Plan the Implementation

```
> /nightgauge:feature-planning

Reading documentation...
✓ docs/ARCHITECTURE.md
✓ CONTRIBUTING.md
✓ skills/README.md

## Documentation Readiness: 100%

Mapping requirements to documented patterns...

┌─────────────────────────────────────────────────────────────────┐
│  FEATURE PLAN CREATED                                           │
└─────────────────────────────────────────────────────────────────┘

Feature:  Add CHANGELOG.md to Nightgauge Skills
Approach: Create per-skill changelogs following Keep a Changelog
Scope:    5 files to create, 2 files to modify

## Files to Create
- skills/nightgauge-issue-pickup/CHANGELOG.md
- skills/nightgauge-feature-planning/CHANGELOG.md
- skills/nightgauge-feature-dev/CHANGELOG.md
- skills/nightgauge-pr-create/CHANGELOG.md
- skills/nightgauge-issue-create/CHANGELOG.md

## Files to Modify
- skills/README.md - Add changelog section
- CONTRIBUTING.md - Add changelog update instructions

Full plan saved to: .nightgauge/plans/13-nightgauge-changelog.md

? Ready to proceed with this plan?
> Approve
```

### Step 3: Implement the Feature

```
> /nightgauge:feature-dev

Reading plan: .nightgauge/plans/13-nightgauge-changelog.md
Loading standards from docs/...

Progress: [1/7] files complete
✓ skills/nightgauge-issue-create/CHANGELOG.md - Created
✓ skills/nightgauge-issue-pickup/CHANGELOG.md - Created
✓ skills/nightgauge-feature-planning/CHANGELOG.md - Created
✓ skills/nightgauge-feature-dev/CHANGELOG.md - Created
✓ skills/nightgauge-pr-create/CHANGELOG.md - Created
✓ skills/README.md - Added changelog section
✓ CONTRIBUTING.md - Added changelog guidelines

## Quality Checks
✓ Code standards: Passed
✓ Version consistency: All 1.0.0
✓ Markdown syntax: Valid

┌─────────────────────────────────────────────────────────────────┐
│  IMPLEMENTATION COMPLETE                                        │
└─────────────────────────────────────────────────────────────────┘

Branch:  docs/13-nightgauge-changelog
Commit:  [DOCS][#13] Add CHANGELOG.md files to nightgauge skills

## Next Steps
1. Push changes: git push
2. Create PR: /nightgauge:pr-create
```

### Step 4: Create the Pull Request

```
> /nightgauge:pr-create

## Pre-Flight Checks
✓ On feature branch: docs/13-nightgauge-changelog
✓ No uncommitted changes
✓ Branch pushed to remote

Gathering context...
✓ 2 commits on branch
✓ 10 files changed
✓ Plan found: .nightgauge/plans/13-nightgauge-changelog.md
✓ Issue #13 details loaded

Detecting base branch...
✓ Base: feat/nightgauge-pipeline (from .nightgauge/plans/.branch-context)

┌─────────────────────────────────────────────────────────────────┐
│  PULL REQUEST CREATED                                           │
└─────────────────────────────────────────────────────────────────┘

PR:       #15
Title:    [DOCS][#13] Add CHANGELOG.md files to nightgauge skills
URL:      #15
Status:   Ready for review

## Linked
- Issue: #13 (Closes)
- Branch: docs/13-nightgauge-changelog → feat/nightgauge-pipeline

## Quick Commands
- View PR: gh pr view 15
- Open in browser: gh pr view 15 --web
- Merge when ready: gh pr merge 15
```

## Key Features

### Stateless Design

Each skill can run in a **new conversation** without losing context. Skills
infer the current issue from the branch name:

```bash
# Branch: docs/13-nightgauge-changelog
# Skills automatically detect: Issue #13
```

### Context Persistence (via Hooks)

When you resume a session or context compacts, the `inject-context.sh` hook
automatically restores:

- Current branch and issue number
- PLAN.md location and progress
- Last commit message
- Uncommitted changes count

### Base Branch Detection

When creating branches, the pipeline detects if you're working on an epic branch
and asks which base to use:

```
? Which branch should be the base for your new feature branch?
> main (Recommended)
  feat/nightgauge-pipeline
  Current branch
```

### Multi-Issue Support

A single PR can close multiple issues:

```markdown
Closes #13 Closes #14
```

### Documentation-First Planning

The planning phase reads your `docs/` folder **before** exploring code, saving
80-90% of tokens:

```
Traditional: Explore 50,000+ tokens of code
Nightgauge:     Read 3,000 tokens of docs, targeted exploration only
```

## Philosophy

- **Enforceable, not advisory** — Hooks ensure standards are followed
- **Issue-driven development** — Every change starts with an issue
- **Documentation-first** — Read docs before exploring code
- **Plan-driven implementation** — Follow approved plans exactly
- **Quality gates** — Automated review before committing
- **Traceability** — Branch names encode issue numbers

## Related Skills

The nightgauge pipeline uses these universal skills (work with any AI
tool):

- [nightgauge-issue-create](../../skills/nightgauge-issue-create/SKILL.md)
- [nightgauge-issue-pickup](../../skills/nightgauge-issue-pickup/SKILL.md)
- [nightgauge-feature-planning](../../skills/nightgauge-feature-planning/SKILL.md)
- [nightgauge-feature-dev](../../skills/nightgauge-feature-dev/SKILL.md)
- [nightgauge-test-gen](../../skills/nightgauge-test-gen/SKILL.md)
- [nightgauge-pr-create](../../skills/nightgauge-pr-create/SKILL.md)

## Versioning

**Plugin vs. Skill Versions**

The Nightgauge plugin (`plugin.json`) and individual skills (`SKILL.md`
files) maintain **independent versions**:

| Component                       | Version | Purpose                                  |
| ------------------------------- | ------- | ---------------------------------------- |
| `nightgauge` plugin             | 1.4.0   | Claude Code wrapper with hooks           |
| `nightgauge-issue-pickup` skill | 1.x.x   | Universal skill (works with any AI tool) |
| `nightgauge-feature-dev` skill  | 1.x.x   | Universal skill                          |
| ...                             | ...     | ...                                      |

The plugin wraps the universal skills for Claude Code-specific features (like
hooks). There is no `skills/nightgauge/SKILL.md` because the plugin itself
is the integration layer.

Version checks in hooks validate that **each skill's `SKILL.md` matches its
corresponding `plugin.json`** (for skills that have both).

## Troubleshooting

### Hooks Not Running

1. Verify plugin is installed: `claude plugin list`
2. Check hooks.json syntax: `jq . hooks/hooks.json`
3. Make hook scripts executable: `chmod +x hooks/*.sh`

### Notifications Not Appearing

- **macOS** (fully tested): Ensure Terminal/IDE has notification permissions in
  System Settings > Notifications
- **Linux** (tested): Install `notify-send` (`sudo apt install libnotify-bin` or
  `dnf install libnotify`)
- **Windows** (best effort): Uses PowerShell notifications - not extensively
  tested. May require enabling script execution. Disable with
  `NIGHTGAUGE_SKIP_NOTIFY=1` if issues occur.

### Format Hook Failing

- Ensure formatters are installed (`npx prettier --version`, `black --version`)
- Disable with `export NIGHTGAUGE_SKIP_FORMAT=1`

### Debug Mode

Enable verbose logging to troubleshoot hook behavior:

```bash
export NIGHTGAUGE_HOOKS_DEBUG=1
```

This outputs detailed information about hook execution to stderr.

### Check Dependencies

Run the dependency checker to verify your environment:

```bash
./hooks/check-dependencies.sh
```

## Known Limitations

| Limitation                                       | Workaround                                                                                                                |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------- |
| **Windows support is best-effort**               | Tested on macOS/Linux. Set `NIGHTGAUGE_SKIP_NOTIFY=1` on Windows if notifications cause issues.                           |
| **jq required for full functionality**           | Hooks use fallback mode without jq, but some features (like version checking) won't work. Install jq for best experience. |
| **No interactive git commands**                  | Hooks block `git rebase -i`, `git add -i`, etc. Use these commands manually outside Claude Code if needed.                |
| **Sensitive file detection is pattern-based**    | Files matching `.env`, `*.key`, `*.pem`, `*secret*` are blocked. Custom sensitive files need manual protection.           |
| **Format-on-save requires formatters installed** | Missing formatters are silently skipped. Run `./hooks/check-dependencies.sh` to see which are missing.                    |

## Support

- **Issues**:
  [GitHub Issues](https://github.com/nightgauge/nightgauge/issues)
- **Documentation**:
  [AI-Nightgauge Docs](https://github.com/nightgauge/nightgauge/tree/main/docs)

---

**Author:** nightgauge **Version:** 1.4.0 **License:** Apache-2.0
