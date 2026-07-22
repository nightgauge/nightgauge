# Git Workflow

This document outlines the Git workflow for contributing to the nightgauge
repository.

## Branch Strategy

### Main Branch

- `main` - Production-ready configurations and plugins
- All changes go through pull requests — never push directly to `main`
- Server-enforced branch protection (required PR approval + status checks) is
  not live yet; it lands with the governance epic's flip-day ruleset
  ([#137](https://github.com/nightgauge/nightgauge/issues/137)). Until then this
  is a project convention, not a rule GitHub enforces.

### Feature Branches

Create feature branches for all changes:

```bash
# Format: type/description
git checkout -b feat/add-cursor-config
git checkout -b fix/smart-setup-path
git checkout -b docs/update-getting-started
```

### Branch Naming Conventions

| Type        | Usage                   | Example                              |
| ----------- | ----------------------- | ------------------------------------ |
| `feat/`     | New features or plugins | `feat/add-kiro-support`              |
| `fix/`      | Bug fixes               | `fix/json-validation-error`          |
| `docs/`     | Documentation changes   | `docs/update-readme`                 |
| `refactor/` | Code restructuring      | `refactor/simplify-plugin-structure` |
| `chore/`    | Maintenance tasks       | `chore/update-dependencies`          |

## Commit Messages

### Format

```text
type(scope): brief description

Longer description if needed.

Refs: #issue-number (optional)
```

### Types

| Type       | Description                |
| ---------- | -------------------------- |
| `feat`     | New feature                |
| `fix`      | Bug fix                    |
| `docs`     | Documentation              |
| `style`    | Formatting, no code change |
| `refactor` | Code restructuring         |
| `test`     | Adding tests               |
| `chore`    | Maintenance                |

### Examples

```bash
# Good commit messages
git commit -m "feat(smart-setup): add --audit-only flag"
git commit -m "fix(plugin): correct JSON schema reference"
git commit -m "docs: add installation guide for Cursor"

# Bad commit messages
git commit -m "fixed stuff"
git commit -m "updates"
git commit -m "WIP"
```

## Auto-Merge and Pipeline Control

### Why Auto-Merge Must Be Disabled

The Nightgauge pipeline requires **exclusive control over PR merging** via
the `pr-merge` stage. If GitHub's repository-level `allow_auto_merge` setting is
enabled:

- PRs merge automatically once CI passes, without pipeline intervention
- The pipeline's watch/resolve loop cannot respond to CI failures
- Self-healing logic (failure detection, corrections) is bypassed
- UI state becomes stale and out-of-sync with actual PR status

**Result:** Failed builds go unnoticed, recovery mechanisms don't trigger, and
users see incorrect pipeline status in the extension.

### Disabling Auto-Merge

Via the Go CLI:

```bash
nightgauge repo disable-auto-merge --owner <org> --repo <repo>
```

Via VSCode extension: run the command palette entry
**"Nightgauge: Disable Repository Auto-Merge"**, or click the warning
notification that appears on workspace load when auto-merge is detected.

### Verification

After disabling, confirm:

```bash
gh api repos/<owner>/<repo> --jq '.allow_auto_merge'
# Should print: false

# Or via the Go binary:
nightgauge repo settings --owner <org> --repo <repo>
```

### PR Creation Guard

The `pr-create` pipeline stage includes a guard (Phase 0.5) that checks
`allow_auto_merge` before creating a PR. If auto-merge is detected, PR creation
fails with an actionable error message rather than creating a PR that bypasses
pipeline control.

No pipeline-created PR ever uses the `--auto` flag — the `pr-merge` stage owns
the entire merge lifecycle.

### Identity Preflight

Before dispatching **any** stage for a target repo, the scheduler asserts the
resolved GitHub identity can actually mutate that repo (#4068, epic #4067). It
resolves the `github_user` configured for the repo's owner (e.g.
`Acme-Community → acmebot`), confirms the **effective** login
matches it, and confirms that identity has **push** access — failing fast with a
surfaced, specific reason (recorded as a `pipeline-start` stage error) rather
than producing an un-mergeable PR as a read-only or wrong user.

- The check is **skippable**: repos that configure no `github_user` (and CLI
  mode) are unaffected.
- Run it manually with `nightgauge forge auth assert --repo <owner>/<repo>`
  (`--admin` to also require admin / ruleset-bypass). Exit 0 = ready; non-zero
  prints the blocker and a one-line remediation.
- The configured per-repo identity is **authoritative over the ambient
  `GH_TOKEN`/`GITHUB_TOKEN`** — see
  [CONFIGURATION.md § Token Resolution Priority](CONFIGURATION.md#token-resolution-priority).
  Git `push` for these repos already uses the SSH host alias; this preflight
  closes the `gh` API / HTTPS gap.

---

## Pull Request Process

### Creating a PR

1. **Create feature branch:**

   ```bash
   git checkout main
   git pull origin main
   git checkout -b feat/my-feature
   ```

2. **Make changes and commit:**

   ```bash
   git add .
   git commit -m "feat(scope): description"
   ```

3. **Push and create PR:**

   ```bash
   git push -u origin feat/my-feature
   ```

4. **Open PR on GitHub** with:
   - Clear title describing the change
   - Description of what changed and why
   - Testing steps
   - Screenshots (if applicable)

### PR Template

```markdown
## Summary

Brief description of what this PR does.

## Changes

- Change 1
- Change 2
- Change 3

## Testing

Steps to test:

1. Step 1
2. Step 2
3. Expected result

## Checklist

- [ ] JSON files validated
- [ ] Markdown linting passes
- [ ] Tested with Claude Code
- [ ] Documentation updated
```

### Review Process

1. At least one approval required
2. All CI checks must pass
3. Address review comments
4. Squash merge to main

### Epic Merge Strategy

Epics use a two-tier merge strategy to preserve sub-issue granularity on main:

1. **Sub-issue PRs → epic branch**: Squash merge (configured via
   `pr.merge_strategy`, default: `squash`). Each sub-issue becomes one clean
   commit on the epic branch.
2. **Epic branch → main**: Regular merge commit (configured via
   `pr.epic_merge_strategy`, default: `merge`). This preserves all individual
   sub-issue commits on main, keeping them independently revertable and
   bisectable.

Configure in `.nightgauge/config.yaml`:

```yaml
pr:
  merge_strategy: squash # Sub-issue PRs (default: squash)
  epic_merge_strategy: merge # Epic → main (default: merge)
```

**Why not squash the epic?** Squashing the epic→main merge would collapse all
sub-issue work into a single commit, losing the ability to revert or bisect
individual sub-issues. The whole point of breaking work into sub-issues is
granularity — squashing the epic throws that away.

### After Merge

- Delete feature branch
- Pull latest main locally

```bash
git checkout main
git pull origin main
git branch -d feat/my-feature
```

## Versioning

### Unified Version — One Product, One Version

All packages (VSCode extension, SDK, Go binary) share a **single version**
derived from git tags at release time. The version is encoded in the tag itself.

**Why unified versioning?** The SDK is bundled into the VSCode extension at
build time (esbuild `--bundle`). It is never published independently. The Go
binary is also packaged into the `.vsix`. All three ship as one artifact — so
they share one version.

| Component | Build version source            | Example |
| --------- | ------------------------------- | ------- |
| Extension | Tag → `npm version`             | `0.2.0` |
| SDK       | Tag → `npm version`             | `0.2.0` |
| Go binary | Tag → `make build-cli VERSION=` | `0.2.0` |

### Version Rules

- **NEVER set different versions** across `nightgauge-vscode` and
  `nightgauge-sdk` — they must match
- Version is always derived from the git tag — never hardcode release versions
  in package.json (the base version `0.1.0` is a placeholder)

## Deployment Strategy — Tags & Environments

### Environment Tiers

| Tier           | Purpose                        | Trigger                        | GitHub Environment |
| -------------- | ------------------------------ | ------------------------------ | ------------------ |
| **dev**        | Local development, feature PRs | `git push` / PR                | —                  |
| **staging**    | Integration testing, QA, demo  | Tag: `v*.*.*-rc.*`             | `staging`          |
| **production** | Live release                   | Tag: `v*.*.*` (no pre-release) | `production`       |

### Git Tag Format

All repositories use the same tag conventions:

| Tag Pattern             | Example         | Triggers      | Publishes? |
| ----------------------- | --------------- | ------------- | ---------- |
| `v<M>.<m>.<p>-rc.<N>`   | `v0.2.0-rc.1`   | `staging.yml` | No         |
| `v<M>.<m>.<p>-beta.<N>` | `v0.2.0-beta.1` | —             | No         |
| `v<M>.<m>.<p>`          | `v0.2.0`        | `release.yml` | Yes        |

### Tag Rules

- Tags are **only** created from the `main` branch
- Tags are **annotated** (`git tag -a`) with a changelog summary
- Tags are **never deleted or moved** — immutable release history
- RC tags can be created freely; production tags should follow a validated RC

### Promotion Flow

```
main ──●──●──●──●──●──
                │     │
          v0.2.0-rc.1  v0.2.0
          (staging)    (production)
```

1. Merge feature PRs to `main` as usual (CI validates on PR)
2. When ready to validate a release: `git tag -a v0.2.0-rc.1 -m "RC1 for 0.2.0"`
3. Push the tag: `git push origin v0.2.0-rc.1`
4. `staging.yml` runs → builds artifacts, uploads them, records in GitHub
   Environments — but does NOT publish or create a GitHub Release
5. Validate the RC artifacts (install VSIX, test Docker image, etc.)
6. When satisfied: `git tag -a v0.2.0 -m "Release 0.2.0"` on the same commit
7. Push the tag: `git push origin v0.2.0`
8. `release.yml` runs → builds, creates GitHub Release, publishes artifacts
   (gated by `production` environment)

### Per-Repository Workflows

| Repository        | Staging (`v*-rc.*`)               | Production (`v*.*.*`)                   |
| ----------------- | --------------------------------- | --------------------------------------- |
| **nightgauge**    | Build VSIX + Go binary → artifact | Build → GitHub Release with VSIX        |
| **acme-platform** | Build Docker → push GHCR staging  | Build Docker → push GHCR prod + Release |
| **acme-mobile**   | Build release APK → artifact      | Build APK → GitHub Release              |

### Cutting a Release — Step by Step

```bash
# 1. Ensure main is up to date
git checkout main && git pull origin main

# 2. Create an RC tag (annotated)
git tag -a v0.2.0-rc.1 -m "Release candidate 1 for 0.2.0

- feat: new dashboard layout
- fix: pipeline timeout handling"

# 3. Push the RC tag
git push origin v0.2.0-rc.1

# 4. Watch staging build
gh run list --workflow=staging.yml --limit 1

# 5. Validate (download artifact, test locally, etc.)

# 6. If RC passes, create production tag on same commit
git tag -a v0.2.0 -m "Release 0.2.0

- feat: new dashboard layout
- fix: pipeline timeout handling"

# 7. Push production tag
git push origin v0.2.0

# 8. Watch production release
gh run list --workflow=release.yml --limit 1
```

### Rollback

```bash
# Option 1: Point consumers back at the previous known-good release tag
# (Homebrew: brew install nightgauge/tap/nightgauge@<prev>;
#  binaries/VSIX: download the previous GitHub Release assets)

# Option 2: Create a hotfix
git checkout -b hotfix/critical-issue v0.2.0
# ... fix ...
git checkout main && git merge hotfix/critical-issue
git tag -a v0.2.1 -m "Hotfix: critical issue"
git push origin main v0.2.1
```

### GitHub Environments

Environments are configured in each repository's Settings → Environments:

- **`staging`** — No protection rules (auto-approve). Scoped for staging
  secrets when needed.
- **`production`** — Add required reviewers and wait timers when the GitHub plan
  supports it. Scoped for production secrets.

Benefits of environments even without upper-tier infrastructure:

- Deployment history visible in GitHub UI
- Secrets scoped per environment (staging DB ≠ production DB)
- Audit trail of deployments
- Protection rules enforced by GitHub (when plan supports it)

### Changelog

Major changes should be documented in:

- Git tag annotation message (primary source of truth)
- GitHub Release notes (auto-generated from tag)

## Pre-Submission Validation (CRITICAL)

**Run these checks WHILE developing, not after.** Getting this right during
development prevents CI failures and review delays.

### For All Contributions

1. **Validate JSON files** (run from repo root):

   ```bash
   find . -name "*.json" -not -path "./.git/*" -exec python3 -m json.tool {} \; > /dev/null && echo "✓ JSON valid"
   ```

2. **Validate YAML files**:

   ```bash
   find . \( -name "*.yaml" -o -name "*.yml" \) -not -path "./.git/*" \
     -exec python3 -c "import yaml; yaml.safe_load(open('{}'))" \; && echo "✓ YAML valid"
   ```

3. **Check for broken links** in markdown:

   ```bash
   # Manually verify relative links point to existing files
   grep -rh '\[.*\](\\./' --include="*.md" . | head -10
   ```

4. **Check for sensitive data**:

   ```bash
   # Look for potential secrets (review any matches carefully)
   grep -rniE "(api[_-]?key|secret|password|token)\\s*[:=]\\s*['\"][^'\"]+['\"]" \
     --include="*.json" --include="*.yaml" --include="*.md" . 2>/dev/null | \
     grep -v "example\\|placeholder\\|YOUR_\\|xxx" || echo "✓ No obvious secrets"
   ```

### For Plugin Changes (MANDATORY)

When modifying plugins in `claude-plugins/`, you MUST ensure version
consistency:

1. **Check plugin.json version**:

   ```bash
   cat claude-plugins/YOUR-PLUGIN/.claude-plugin/plugin.json | grep version
   ```

2. **Check corresponding SKILL.md version** (if it exists):

   ```bash
   grep -m1 'version:' skills/YOUR-PLUGIN/SKILL.md
   ```

3. **Versions MUST match.** If they don't:
   - Update the SKILL.md to match plugin.json
   - Or update both if you're bumping the version

4. **Never downgrade versions.** Check main branch first:

   ```bash
   git show main:claude-plugins/YOUR-PLUGIN/.claude-plugin/plugin.json | grep version
   ```

### For Skill Changes

1. **Verify SKILL.md frontmatter** has required fields:
   - `name:` (matches directory name)
   - `description:` (1-2 sentences)
   - `metadata.version:` (semantic version in quotes)

2. **Check version not downgraded**:

   ```bash
   git show main:skills/YOUR-SKILL/SKILL.md | grep version
   ```

3. **Update skills/README.md** with the new skill

### Mandatory Local CI Validation (NEVER skip)

**NEVER push to GitHub without passing all local checks first.** CI is for
catching environment differences, not for running tests you skipped locally.
Every push that fails CI wastes time and pollutes the PR with fix-up commits.

> **Pre-commit hook**: The repository's pre-commit hook automatically validates
> generated files on every `git commit`. If you see a hook error, run the
> suggested command, re-stage the file, and commit again. The hook is installed
> automatically via `npm install` (husky). If it is not running, execute
> `npm run setup-hooks`.

Run these commands **before every `git push`**:

```bash
# 1. Go build + tests
go build ./...
go test ./...

# 2. Generated files must be in sync (pre-commit hook checks this; CI is the backstop)
make generate-ipc-client
git diff --exit-code packages/nightgauge-vscode/src/services/IpcClient.generated.ts

# 3. TypeScript build (catches type errors)
npm run -w nightgauge-vscode build

# 4. TypeScript tests (use `vitest run`, NOT bare `vitest` which hangs in watch mode)
npx -w nightgauge-vscode vitest run

# 5. SDK tests (if SDK changes were made)
npx -w @nightgauge/sdk vitest run

# 6. Prettier formatting (CI's build-and-test job enforces this — runs
#    `npm run format:check`. Run format first to auto-fix drift.)
npm run format
npm run format:check

# 7. ESLint (CI enforces this)
npm run lint
```

Or run all CI-parity checks in order with one command:

```bash
bash scripts/ci-local.sh
```

If any step fails, fix the issue and re-run before pushing. The order matters:
generated files must be regenerated before the TypeScript build, and the build
must succeed before tests can run. `format:check` is the #1 cause of avoidable
CI failures — always run `npm run format` before committing.

### Quick Validation Script

Run `/pr-preflight` locally if you have the skill installed, or run these checks
manually:

```bash
#!/bin/bash
# Quick validation before PR
echo "🔍 Pre-PR Validation"

# JSON check
echo -n "JSON: "
find . -name "*.json" -not -path "./.git/*" -exec python3 -m json.tool {} \\; > /dev/null 2>&1 && echo "✓" || echo "❌"

# YAML check
echo -n "YAML: "
find . \\( -name "*.yaml" -o -name "*.yml" \\) -not -path "./.git/*" \\
  -exec python3 -c "import yaml; yaml.safe_load(open('{}'))" \\; 2>/dev/null && echo "✓" || echo "❌"

# Version consistency check
echo "Version Consistency:"
for plugin_dir in claude-plugins/*/; do
  [ ! -d "$plugin_dir" ] && continue
  name=$(basename "$plugin_dir")
  pj="${plugin_dir}.claude-plugin/plugin.json"
  skill="skills/${name}/SKILL.md"
  [ ! -f "$pj" ] && continue
  pj_ver=$(python3 -c "import json; print(json.load(open('$pj')).get('version','?'))" 2>/dev/null)
  if [ -f "$skill" ]; then
    skill_ver=$(grep -m1 'version:' "$skill" | sed 's/.*version: *"\\{0,1\\}\\([^"]*\\)"\\{0,1\\}/\\1/' | tr -d ' ')
    if [ "$pj_ver" = "$skill_ver" ]; then
      echo "  ✓ $name: $pj_ver"
    else
      echo "  ❌ $name: plugin.json=$pj_ver, SKILL.md=$skill_ver"
    fi
  fi
done

echo "Done. Fix any ❌ before submitting PR."
```

---

## Best Practices

### Do

- Keep commits atomic (one logical change per commit)
- Write descriptive commit messages
- Test changes before pushing
- Keep PRs focused and reasonable in size
- Update documentation with code changes

### Don't

- Commit directly to main
- Force push to shared branches
- Include unrelated changes in a PR
- Leave WIP commits in PR history
- Ignore CI failures

## Pre-Push Merge Validation Gate

The pre-push merge validation gate validates changes against the target branch
**before** pushing to the remote. It catches merge conflicts, build failures,
test regressions, and security issues in the merged state (feature + target
combined) — eliminating wasted CI cycles and fix-up commits.

### What the Gate Checks

| Phase           | Check                                                    | Blocking?      |
| --------------- | -------------------------------------------------------- | -------------- |
| 1. Merged-state | Fetch target, merge locally, verify no conflicts         | Yes            |
| 2. Build        | `go build ./...` or `npm run build` against merged state | Yes            |
| 3. Test         | `go test ./...` or `npm test` against merged state       | Yes            |
| 4. Vet          | `go vet ./...` against merged state                      | Yes            |
| 5. Security     | gitleaks + grep patterns for secrets in diff             | Yes (critical) |
| 6. Static       | IPC client sync, JSON/YAML validation, large files       | Yes            |

### When It Runs

- **Pipeline**: `feature-validate` Phase 2.7 runs the gate automatically before
  committing and pushing. If the gate fails, the commit/push phase is skipped.
- **Git hook**: Install with `nightgauge pre-push install` to run the gate
  on every `git push` from a pipeline branch.
- **Manual**: Run `nightgauge pre-push validate <issue-number>` at any time.

### Installing the Git Hook

```bash
nightgauge pre-push install
```

This creates `.git/hooks/pre-push` which calls the validation gate before each
push. The hook only activates for pipeline branches (branches with issue
numbers). Non-pipeline branches pass through.

### Reading the Context File

The gate writes `.nightgauge/pipeline/pre-push-{N}.json` with validation
results. Downstream stages read this file to skip redundant checks:

- **pr-create Phase 2.5**: Skips security re-scan if gate security passed
- **pr-merge Phase 1.5**: Skips `go vet` if gate vet passed

### Graceful Degradation

If the `nightgauge` binary is not available:

- **feature-validate**: Falls back to shell-based checks (JSON validation,
  secret grep patterns)
- **Git hook**: Skips gracefully (exit 0)
- **Claude Code hook**: Allows the push (no context file to block on)

### Commands

```bash
# Run validation manually
nightgauge pre-push validate <issue-number> [--target main] [--timeout 180] [--json]

# Install git hook
nightgauge pre-push install
```

---

## Emergency Fixes

For critical fixes that need immediate deployment:

1. Create branch from main: `git checkout -b hotfix/critical-issue`
2. Make minimal fix
3. Create PR with `[HOTFIX]` prefix
4. Request expedited review
5. Merge after approval

## Author

nightgauge
