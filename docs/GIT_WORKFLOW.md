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

### Working one repository from two sessions at once

Running two interactive sessions against the same checkout is a supported way to
get more done, and it has exactly one rule that matters:

**Split by disjoint FILE SETS, not by issue count.** `AGENTS.md` already
requires concurrent issues to be conflict-free by construction; with two
sessions in one repository the check is mechanical — list the files each side
will plausibly touch and confirm the intersection is empty (`comm -12` over two
sorted lists) _before_ either one pushes. Separate worktrees isolate the
checkout, not the merge: both sides see a clean tree and green CI, and the
collision only appears when the second one rebases.

**Parallel sessions do not multiply throughput the way they look like they
should**, so pick the split deliberately rather than opportunistically:

- **Merging is serial.** Every merge puts every other open PR `BEHIND` and costs
  it a fresh CI run.
- **One machine runs one `ci-local.sh` at a time.** N branches cost N sequential
  gates however fast they were written.

Two sessions on disjoint files is a real gain. Two sessions on the same area is
strictly worse than one.

**Before starting, check whether you are alone**, because the tree does not
say so:

```bash
git worktree list                                     # someone else's .wt/*
gh pr list --repo <owner>/<repo> --state open         # someone else's branch
```

An unexpected `.wt/<n>` with uncommitted work is not an anomaly to clean up —
it is somebody's in-flight work. Leave it alone.

**One shared-state hazard is worth naming**, because it presents as a flaky
test rather than as a conflict: the publication-boundary regression suite builds
each case in a throwaway git worktree and calls `git worktree prune`, which is
**repo-global**. A second session creating a worktree during that run can strand
a sandbox mid-case, and every case then fails with
`FileNotFoundError: '.github/publication-boundary.yaml'`. Re-run it in isolation
before hunting a bug in your own diff — and note this is a hazard of two
sessions sharing one checkout, not a defect in the suite. CI never shares a
checkout.

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

### The PR body closes issues too, and it cannot read a negation

GitHub scans **both** the commit messages and the **PR body** for closing
keywords when a PR merges. A correct commit message does not protect you from a
PR body that happens to contain one.

The parser matches a keyword next to an issue reference. It has no notion of
"not", so every one of these closes the issue:

```text
why this does not close #849          ← closed it
this PR should not fix #123           ← closed it
do not resolve #456 in this pass      ← closed it
```

This is not theoretical. #930 was deliberately scoped `Part of #849`, said so in
its commit message, and deliberately skipped the post-merge hook — and merging
it closed #849 anyway, from a heading that read _"why this does not close
#849"_. The board followed: the project's built-in **closed → Done** workflow
moved the row to Done, so a partially-complete issue was both closed and marked
finished, by prose explaining that it was neither.

**When a PR only partly addresses an issue, never put a closing keyword next to
its number anywhere in the body.** Write around it:

```text
✓ Part of #NNN — the remaining work is <…>
✓ #NNN stays open; see its worklist
✗ this does not clos<span>e</span> #NNN
```

Writing _about_ the keyword is indistinguishable from using it, so quoting the
pattern in prose needs the adjacency broken — an HTML entity or a hyphen.

The failure is silent and asymmetric: nothing warns you, the merge output says
nothing, and you only find it by re-measuring the backlog afterwards — which is
the argument for re-measuring open-issue counts after a merge instead of
assuming your intent carried.

**Scan both channels before merging**, because a clean commit message does not
imply a clean body and vice versa:

```bash
gh pr view <n> --json body --jq .body | grep -inE "(clos(e|es|ed)|fix(es|ed)?|resolv(e|es|ed))[[:space:]]*#[0-9]+"
git log origin/main..HEAD --format='%B'  | grep -inE "(clos(e|es|ed)|fix(es|ed)?|resolv(e|es|ed))[[:space:]]*#[0-9]+"
```

Note that a fix to a **pushed** commit message needs an amend, and force-push is
blocked in this environment — so the repair is a new branch and a new PR. Scan
before you push, not after.

#### The link is sticky, which is the part that actually bites

Editing the offending text afterwards does **not** unlink the issue, and neither
does reopening it. The association GitHub created from the original body
persists and can close the issue again on a **later, unrelated merge**.

Measured on #849, which was closed twice:

```bash
gh api graphql -f query='{ repository(owner:"…", name:"…") {
  issue(number: NNN) { closedByPullRequestsReferences(first:10, includeClosedPrs:true) {
    nodes { number state } } } } }'
```

That returned #930, #931 **and** #932 — including #931, whose body had been
cleaned and which was **never merged**. The second close fired on #932's merge,
whose body and every commit message scanned clean.

**So reopening is not a repair; it is a reprieve.** Check the linked-PR list
above before deciding what to do:

```bash
gh issue reopen <n> --repo <owner>/<repo> --comment "<why it closed by accident>"
nightgauge project sync-status <n> --repo <owner>/<repo> --project <p> <status>   # undo closed→Done
gh issue view <parent-epic> --json state    # confirm no rollup fired on the way through
```

If the issue carries sticky links it cannot shed, the durable fix is to stop
relying on that number: **re-file the remaining work as a fresh issue** and let
the old one stay closed. Re-read its acceptance criteria first — an issue that
keeps getting closed by accident is sometimes an issue whose criteria are
actually met, with follow-on work that deserves its own number anyway. That was
the outcome on #849: all three criteria were satisfied and the remainder became
#933.

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
non-blocking and always exits `0`, so a failed rollup and a successful one are
indistinguishable by exit status alone. Expected lines:

| Output                                       | Meaning                                  |
| -------------------------------------------- | ---------------------------------------- |
| `Issue #N has no parent epic — skipping…`    | normal for a standalone issue            |
| `Epic #N auto-closed (all sub-issues …)`     | the rollup fired                         |
| `Epic #N skipped: has_open`                  | siblings still open — nothing to do yet  |
| `Warning: post-merge FAILED for issue #N: …` | **it did not run**; re-run once resolved |

**`--json` reports failures too, since #1025.** It did not before: the reporting
switch sat _below_ the `--json` early return, so the machine-readable mode — the
one the extension uses — printed nothing at all on failure while the human mode
printed a warning. The JSON now carries a `failed` boolean and an `epicReason`
discriminator, and the warning goes to stderr in **both** modes, leaving stdout
pure JSON.

Two shapes of the same bug were fixed together, and both were silent:

- The result's `reason` was set from the epic service's raw status word
  `"error"`, which was **not in the vocabulary any caller branched on** — so a
  failed rollup fell through a `default:` case that printed nothing, while the
  one reason value callers _did_ recognise (`auto_close_error`) was unreachable,
  because `AutoCloseSingle` swallows every error and returns `nil`.
- `Epic #N skipped: %s` re-printed `reason` — the word "skipped" itself — so the
  line read `Epic #206 skipped: skipped`. The discriminator that says _why_
  (`has_open`, `check_failed`) was computed on every call and never copied out.

A failure now also raises an Action Center card, because the operator who most
needs this signal is the one who ran a hand merge and moved on. Epic #206 is the
worked example: it did not roll up, and nothing said so.

The board half of the hook reports itself separately, because exit `0` says
nothing about it either (#691):

| Output                                               | Meaning                                                                 |
| ---------------------------------------------------- | ----------------------------------------------------------------------- |
| `synced issue #N board status to Done`               | the row existed and is now Done                                         |
| `was not on project board … — added it and set …`    | the row was **missing** and was repaired; the issue was filed off-board |
| `Warning: issue #N is closed but its board status …` | the board is out of date and needs a look — this is the one to act on   |

**Both optional flags change what actually happens:**

- **`--project`** — omit it and `boardSyncer` is never wired, so the board-Done
  sync silently no-ops. Passing it also enables the missing-row repair (#691):
  an issue filed ad-hoc with `gh issue create` has no board row at all, and the
  hook now adds one rather than warning and exiting 0. The epic closes on the
  issue tracker while its board row
  stays in Ready, which is the confusing half-state this step exists to prevent.
  Resolve the number with `nightgauge project resolve --repo <owner/repo> --json`
  rather than reading it out of the workspace YAML.
- **`--pr`** — makes the hook verify the PR is genuinely `MERGED` before closing
  the issue. Omitting it skips the verification (`0` means "don't check").

**`--issue` is the authority — the PR body does not control closure.** The hook
closes the issue it is **told** to close. It never reads the PR body, so
GitHub's `Closes #N` keyword and `--issue N` are two independent mechanisms, and
on the hand-merge train this document mandates, the hook is the one that decides:
a body with no `Closes` keyword leaves GitHub with nothing to do, and the hook
then closes whatever number it was handed.

This bites on partial fixes. A PR that resolves 2 of an issue's 5 acceptance
criteria, whose body was deliberately edited to read _"Partially addresses #N"_
with no `Closes` keyword, still closed #N — because the merge was finished with
`nightgauge hook post-merge --issue N --pr <PR> --project <PROJECT>`. The
remaining three criteria left the board silently.

To land a partial fix, do one of:

- pass `--issue` a **different** number — a follow-up issue that this PR really
  does complete; or
- **skip the hook for that PR** and sync the board by hand
  (`nightgauge project …`), accepting that no epic rollup is evaluated.

Either way, re-check afterwards that the partially-fixed issue is still open:

```bash
nightgauge forge issue view <N> --repo <owner/repo> --json state,title
```

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

### The gate is not the loop

`ci-local.sh` is a **pre-push gate**, not an iteration loop. Run it once, when
you believe you are done. Running it to find out what you broke is the single
most expensive way to ask that question: it rebuilds Go, runs `go test ./...`
plus `-race` on the orchestrator, regenerates the plugin skills mirror, and
makes network calls for the markdown link check — on the order of fifteen
minutes, most of it re-verifying code you did not touch.

Climb the cheapest rung that can answer the question in front of you:

| Rung         | Command                                      | Use it when                             |
| ------------ | -------------------------------------------- | --------------------------------------- |
| **Targeted** | `npx -w nightgauge-vscode vitest run <file>` | Fixing a failure you can already name   |
|              | `go test ./internal/<pkg>/`                  | Same, on the Go side                    |
| **Package**  | `npm run test -w nightgauge-vscode`          | Discovering what a batch of edits broke |
|              | `go test ./internal/...`                     | Same, when the edits were in Go         |
| **Gate**     | `bash scripts/ci-local.sh`                   | Once, immediately before `git push`     |

**Discovery needs breadth, but rarely the whole repo.** Some checks are
repo-wide by design and will not fire from the test file you edited —
`configRegressionGuard` scans every source file for config-system bypasses,
snapshot suites cover rendered HTML far from the component you changed, and the
publication-boundary and plugin-mirror checks read the whole tree. That is a
real argument for a sweep after a substantive change; it is not an argument for
the _full_ gate. A change confined to one package is almost always caught by
that package's own suite, which costs minutes rather than a quarter of an hour.

The failure mode this replaces: fix a test, re-run the whole gate, discover one
new failure, fix it, re-run the whole gate. Three passes of that spends most of
an hour and leaves the tree no greener than a single gate run at the end would
have. **Re-running the gate does not make the tree more correct — it only tells
you again what a narrower run already told you.**

Two things this does not license:

- **Skipping the gate.** It still runs before every push, in full, and it still
  has to pass. Narrow runs during development are how you _arrive_ at a green
  gate, never a substitute for it — a package suite cannot see the cross-cutting
  checks above, which is exactly why the gate exists.
- **Trusting a narrow run's silence.** A targeted run proves the file you named
  passes; it proves nothing about the file you did not think of. When a change
  touches shared types, generated files, config schemas, or anything under
  `src/manifest/`, go straight to the package rung — those edits break things at
  a distance by their nature.

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
