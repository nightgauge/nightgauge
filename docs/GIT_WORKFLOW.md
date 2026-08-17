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

### Single-Maintainer Merge Policy

The sanctioned merge is:

```bash
gh pr merge <n> --squash --delete-branch
```

**No `--admin`.** The `main` ruleset requires **zero** approving reviews while
the project has one maintainer, so a plain squash merge succeeds on its own —
and GitHub enforces the 12 required status checks itself. The gate is machine
-enforced: a merge is _impossible_ while a check is red or pending.

This replaces an earlier policy that used `--admin` to satisfy a
one-approving-review requirement nobody could meet. That worked, but it solved a
review problem with a tool that disables everything:

- **`--admin` bypasses the ENTIRE ruleset.** Rulesets have no per-rule bypass
  granularity, so `--admin` waives required status checks, linear history and
  the rest in a single move. Under the old policy the "all checks green" rule
  was prose enforced by whoever typed the command — an honour system wearing a
  gate's clothing.
- The `OrganizationAdmin` bypass actor is **deliberately retained** as an
  emergency escape hatch. Needing it during ordinary work means something is
  misconfigured; fix the configuration rather than reaching for the hatch.
- **`--auto` remains forbidden.** Auto-merge surrenders the merge moment to
  GitHub, which is what the rest of this document exists to prevent.

Restore `required_approving_review_count: 1` the moment a second maintainer can
review — the requirement is worth keeping once it is satisfiable, and it no
longer costs you the check gate to do so.

### Verify `main` After Every Merge

A green PR check is a **prediction** about a tree that does not exist yet; the
run on the merge commit is the **observation** of the tree that does. They can
disagree, and when they do the disagreement is the finding:

```bash
gh api "repos/<owner>/<repo>/commits/<merge-sha>/check-runs" \
  --jq '[.check_runs[]|select(.conclusion!="success" and .conclusion!="skipped" and .conclusion!="neutral")]|length'
```

Non-zero means `main` is red and it is the merger's to fix immediately.

Three classes only the post-merge run can catch, which is why this is not
redundant with the PR gate:

| Class                      | Why the PR gate misses it                                                                                                                             |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Nondeterministic test**  | A coin-flip test passes the PR and fails `main` on the _identical tree_. This is exactly how #572 was found — a 5023ms test against a 5000ms timeout. |
| **Merge skew**             | Two PRs green apart, broken together. `strict_required_status_checks_policy` closes most of this, but not the window between last check and merge.    |
| **Environment difference** | `main` runs have secrets and permissions that PR runs — especially from forks — do not.                                                               |

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

**Step 0 — run the post-merge hook.** Before cleanup, before anything else:

```bash
nightgauge hook post-merge --issue <N> --owner nightgauge --repo nightgauge \
  --pr <PR> --project <PROJECT>
```

This is the step a hand merger has no way to discover, and skipping it is
invisible until an epic has been sitting open for weeks.

**Why it is not automatic.** Parent-epic auto-close lives in one implementation,
`hooks.EvaluatePostMerge`, deliberately shared so the deterministic CLI route and
the scheduler cannot drift. But it has only two callers:

| Caller                                        | Fires when                     |
| --------------------------------------------- | ------------------------------ |
| `runPipeline`'s post-merge verification block | **the pipeline merged the PR** |
| `nightgauge hook post-merge` (this verb)      | you run it                     |

There is no third. The merge policy in this document mandates **manual** squash
merges as the routine path — so on the routine path the rollup is reached only if
a human invokes it. The machinery is not missing; the invocation is.

Epic #342 is the worked example: 27 PRs hand-merged in a single session, every
sub-issue closed, `checkEpicCompletion` never called once, epic still open on the
board. Read as "we should have used the pipeline", it looks like a discipline
problem. It is not — the pipeline was never supposed to be the merger here.

**Read the output; do not trust the exit code.** The hook is intentionally
non-blocking: `issue_fetch_error` and `auto_close_error` are printed to stderr and
the command still exits `0`, so a failed rollup and a successful one are
indistinguishable by exit status alone. Expected lines:

| Output                                    | Meaning                                  |
| ----------------------------------------- | ---------------------------------------- |
| `Issue #N has no parent epic — skipping…` | normal for a standalone issue            |
| `Epic #N auto-closed (all sub-issues …)`  | the rollup fired                         |
| `Epic #N: <reason>`                       | siblings still open — nothing to do yet  |
| `Warning: post-merge check failed: …`     | **it did not run**; re-run once resolved |

**Both optional flags change what actually happens:**

- **`--project`** — omit it and `boardSyncer` is never wired, so the board-Done
  sync silently no-ops. The epic closes on the issue tracker while its board row
  stays in Ready, which is the confusing half-state this step exists to prevent.
  Resolve the number with `nightgauge project resolve --repo <owner/repo> --json`
  rather than reading it out of the workspace YAML.
- **`--pr`** — makes the hook verify the PR is genuinely `MERGED` before closing
  the issue. Omitting it skips the verification (`0` means "don't check").

`nightgauge project reconcile` is the board-wide backstop sweep for this hook, not
a substitute for it: it repairs drift after the fact across every board item,
where the hook resolves one merge at the moment it happens.

---

**Cleanup is part of the merge, not an optional follow-up.** A merge that leaves
its branch and worktree behind is not finished. This applies to every forge —
GitHub, GitLab, or anything the `forge` abstraction grows next — and to both
sides: the remote ref AND the local one.

```bash
# 1. remote branch — or rely on the forge's delete-on-merge setting
#    (GitHub: "Automatically delete head branches"; GitLab: "Delete source
#    branch" checked by default on the MR)

# 2. local branch
git checkout main
git pull origin main
git branch -d feat/my-feature      # -D if the PR was squash-merged, since
                                   # the squash commit is not the branch tip

# 3. the worktree, if the work used one
git worktree remove .worktrees/my-feature
git worktree prune                 # drops registrations whose dirs are gone
```

**Squash merges need `-D`, not `-d`.** A squash creates a new commit, so the
branch tip is never an ancestor of `main` and `git branch -d` refuses it. Verify
the content landed rather than trusting the ancestry check — with the script,
not by hand:

```bash
scripts/branch-merged-check.sh feat/my-feature   # 0 SAFE-DELETE, 1 KEEP, 2 UNKNOWN
scripts/branch-merged-check.sh --all             # sweep every local branch
NO_PR=1 scripts/branch-merged-check.sh --all     # offline; conservative by design
```

Only exit `0` authorizes a delete. `2` means undecidable, not safe. Every
`SAFE-DELETE` cites its evidence — either identical content or the merged PR
number — so the verdict is auditable rather than trusted.

**Content alone cannot decide this retrospectively.** Comparing base-tip to
branch-tip is exact _at merge time_, when base has not moved. Later it is not: a
branch that was merged reads "differs" once base evolves those files. A branch
merged via squash PR read `6 files changed, 6 insertions(+), 292 deletions(-)`
sixteen days on. Large deletion counts mean base is ahead and the branch is
stale, not that the branch holds work. The script therefore also accepts a
merged PR whose head commit **contains the branch tip** — either the head SHA
equals the tip, or the tip is one of that commit's own parents — proof the
branch is precisely what merged, however far base has moved since. The parent
case matters for a branch that was also `gh pr update-branch`'d: update-branch
creates a merge commit on the PR's REMOTE head whose parents are exactly the
branch's previous tip and base at merge time, and that commit is typically
never fetched into the local branch ref — so a branch that fully landed can
still read as content-differs against a base that evolved the same files after
the branch was cut (#593).

**Do not substitute a hand-written `git diff`.** Both obvious forms report
"nothing unmerged" on branches that carry real work:

- `git diff --stat origin/main..feat/x` (two-dot) also reports every change
  `main` gained after the branch, so it is noisy for old branches — and its
  three-dot cousin restricted to no paths is empty for the wrong reason.
- Restricting to the branch's own files is correct, but
  `files=$(git diff --name-only ...)` then `-- $files` **does not word-split in
  zsh** — an unquoted parameter expansion stays a single word there, unlike an
  unquoted command substitution. The list becomes one pathspec, matches nothing,
  and every branch reads merged.
- A branch whose file list is empty produces the same false "merged".

All three fail toward deleting unmerged work, and all three look like a clean
pass. That is why the check is a script with an explicit `UNKNOWN` verdict.

**Why this is a standing rule.** Skipping it is invisible for one merge and
compounding across a hundred. Accumulated local branches and worktrees make
`git branch` unreadable, leave `node_modules` trees and per-issue docker stacks
squatting disk and host ports, and — the failure that actually cost time —
produce stale checkouts that look like a valid working tree while being months
out of date. Cleanup is cheap at merge time and expensive to reconstruct later,
because after the fact you cannot cheaply tell a squash-merged branch from one
that was never pushed.

> **Pipeline note.** When Nightgauge itself creates the branch and worktree, the
> pipeline is what must clean them up — the operator should never be the garbage
> collector for machine-created state. Inline cleanup alone cannot honor that: a
> run swept mid-flight never reaches its cleanup step. A reconcile sweep
> (`nightgauge worktree sweep`, also folded into the autonomous reconcile cycle)
> reclaims the leftovers using the same content check shown above. See
> [GO_BINARY.md § Worktree Reclamation](GO_BINARY.md#worktree-reclamation-issue-110).

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
# (Homebrew: reinstall a previous GitHub Release directly;
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
