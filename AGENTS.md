# AGENTS.md - AI Agent Configuration

This file is the **canonical, tool-neutral ruleset** for AI agents working in
this repository. Claude Code loads it through the `@AGENTS.md` import at the
top of [CLAUDE.md](CLAUDE.md); other tools (Codex, Copilot, Cursor, Kiro) read
it natively per the [AGENTS.md standard](https://agents.md/).
Claude-Code-specific material (Documentation Map, memory policy,
`.claude/rules/` pointers, compaction rules) lives in `CLAUDE.md` — each rule
appears in exactly one of the two files.

## Project Overview

**nightgauge** is an AI-powered Issue-to-PR pipeline: a VSCode extension and a
Go binary that pick up GitHub issues, plan, implement, validate, and merge PRs
using AI coding agents, with a deterministic compiled layer for everything
that must not depend on a model. Skills follow the
[Agent Skills specification](https://agentskills.io/specification) and are
portable across Claude Code, OpenAI Codex, GitHub Copilot, Cursor, Kiro, and
other AI coding assistants. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
for product layers and design.

## Key Terminology

| Term              | Definition                                                     | Location          |
| ----------------- | -------------------------------------------------------------- | ----------------- |
| **Skill**         | Universal capability (SKILL.md) that works across all AI tools | `skills/`         |
| **Claude Plugin** | Claude Code-specific wrapper with `/slash` commands            | `claude-plugins/` |
| **Config**        | Tool-specific configuration files                              | `configs/`        |
| **Standard**      | Shared best practices and guidelines                           | `standards/`      |

## Where to Start a Session

**One repo per session: start the agent in the repo that owns the issue.**
Working an issue in this repository means starting Claude Code (or any agent)
**in this repository's directory**.

Agent configuration loads from the working directory and its parents. A session
started somewhere else does not get a merged ruleset — it silently gets a
different repo's rules, or none at all. Cross-repo work is **two sessions, not
one**: reading a sibling repository's file is fine, but the session's rules,
validation gate and merge policy must be the ones belonging to the repo being
changed.

This file is the **canonical** source for that rule and for the workspace-wide
rules in [Critical Rules](#critical-rules) below — `gh` multi-account handling,
merge policy, the post-merge `main` check, board rollup, branch and worktree
cleanup, concurrency, background-process reaping, and context economy. Sibling
repositories in the same working checkout restate the relevant subset in their
own `AGENTS.md` under a _Workspace-Wide Rules_ heading, because each repository
is cloned independently and a relative cross-repo import resolves on one machine
and breaks in CI. **Change this file first, then propagate.**

Contributors working only on this repository need nothing further. For a
maintainer with the wider working checkout, the map of sibling repositories and
their entry points is tracked privately (`AGENTS.md` § _Public Core Boundary_),
alongside the session handoffs described in the _Knowledge & Memory_ section of
[CLAUDE.md](CLAUDE.md).

## Auto-Refine Pipeline

Issues can be automatically refined and processed without manual intervention.
Add the `auto-process` label to any issue (or check "Immediately actionable" in
the issue template) and the autonomous refinement scan will rewrite it with
structured acceptance criteria, then route it through the full pipeline. See
[docs/AUTONOMOUS_ORCHESTRATOR.md](docs/AUTONOMOUS_ORCHESTRATOR.md) for details.

## Critical Rules

### Versioning

**Unified version**: all packages share `0.1.0` as the base version in
`package.json`. The release version is derived from the git tag (`vX.Y.Z`)
at release time and applied uniformly to the Go binary and the extension —
not from a commit count. **NEVER set different versions** across
`nightgauge-vscode` and `nightgauge-sdk`. See
[docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#versioning) for full rules.

### Git Workflow (CRITICAL)

**NEVER push directly to main.** Use feature branches (`feat/`, `fix/`,
`docs/`) and conventional commit messages, and always open a pull request —
even for small changes. See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md) for
the full workflow.

```bash
# Create feature branch
git checkout -b feat/description-of-change

# After changes and passing local validation, push and open a PR
git push -u origin feat/description-of-change
```

### Agent Operating Rules

- **No backwards compatibility (pre-customer).** Delete old paths; never add
  deprecation shims, migration fallbacks, or compat knobs. Consolidate N
  overlapping options to one, delete the rest from schema/config/docs, and
  surface the single resolved value. Consistency over compatibility.
- **Ship the best solution; don't offer menus.** If you spot a real gap or a
  better approach, file the issue and execute it. Propose the recommended fix
  and do it — avoid "quick fix vs proper fix" choices and "want me to…?"
  friction. (This does not override genuine product-direction decisions, which
  are still the user's call.)
- **Manual PR merges only, and `--admin` is NOT the routine path.** Auto-merge
  is disabled on all workspace repos. Watch CI (`gh pr checks`), fix/rerun real
  failures (never dismiss a failing test as "flaky" without root-causing it),
  then **`gh pr merge --squash`** — never `--auto`, and **never `--admin` as a
  matter of course**.

  **Green checks are the go signal, not a prompt to ask for one.** An agent that
  finishes the work, watches CI go green and then stops to ask permission to
  merge has not finished the work — completing work includes landing it. The
  approval it waits for adds nothing, because the ruleset below already makes a
  merge impossible while any check is red or pending: the gate is enforced by
  the forge, not by the operator being awake to answer. Merge, then run the
  post-merge verification in the next bullet. A real failure is still surfaced
  rather than merged around — this removes a redundant confirmation, not the
  judgement.

  The `main` ruleset requires **zero** approving reviews (single maintainer), so
  the plain squash merge succeeds on its own and GitHub itself enforces the 12
  required status checks. That enforcement is the point: a merge must be
  impossible while a check is red or pending, not merely discouraged.

  **`--admin` bypasses the ENTIRE ruleset, not just one rule.** GitHub rulesets
  have no per-rule bypass granularity — passing `--admin` waives required status
  checks, linear history and everything else in one move. It exists as an
  emergency escape hatch (the `OrganizationAdmin` bypass actor is deliberately
  retained); using it routinely turns a machine-enforced gate back into an
  honour system. If a merge needs `--admin`, that is a signal something is
  misconfigured — fix the configuration instead.

- **A green PR check is a prediction; `main`'s own run is the observation.**
  After every merge, verify the merge commit's checks actually went green:

  ```bash
  gh api "repos/<owner>/<repo>/commits/<merge-sha>/check-runs" \
    --jq '[.check_runs[]|select(.conclusion!="success" and .conclusion!="skipped" and .conclusion!="neutral")]|length'
  ```

  Non-zero means `main` is red and it is yours to fix immediately. PR checks run
  against a _predicted_ merge; three classes only the post-merge run can catch —
  **nondeterministic tests** (a coin-flip test passes the PR and fails `main` on
  the identical tree — this is exactly how #572 was found), **merge skew** (two
  PRs green apart, broken together), and **environment differences** (`main` has
  secrets and permissions PR runs do not). Post-merge CI is not redundant with
  the PR gate; it is the detector for what the PR gate structurally cannot see.
  See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md).

- **Run the post-merge hook after every hand merge — the board does not roll up
  on its own.**

  ```bash
  nightgauge hook post-merge --issue <N> --owner nightgauge --repo nightgauge \
    --pr <PR> --project <PROJECT>
  ```

  Parent-epic auto-close is implemented once, in `hooks.EvaluatePostMerge`, and
  reached by two callers: the scheduler's `runPipeline` post-merge path, and this
  CLI verb. **The scheduler path only fires when the pipeline merged the PR** — so
  on the manual squash-merge train this file mandates as the routine path, nothing
  ever evaluates the rollup. An epic whose sub-issues are all closed then stays
  open indefinitely, and the board keeps showing work that is finished.

  This is not hypothetical and not a discipline failure: epic #342 sat open with
  every child closed after a 27-PR hand-merged session, because
  `checkEpicCompletion` never ran once. No amount of "use the pipeline instead"
  fixes it — the merge policy above _requires_ hand merges.

  Two flags decide whether the hook does anything useful. **`--project` is
  optional and silently no-ops the board-Done sync when omitted**, so an epic can
  auto-close on the issue tracker while its board row stays in Ready. `--pr` makes
  the hook verify the PR really is `MERGED` before closing the issue; omitting it
  skips that check. The hook is deliberately non-blocking — it logs failures to
  stderr and still exits 0 — so **read its output**; a silent exit code is not
  evidence that the rollup happened. See
  [docs/GIT_WORKFLOW.md § After Merge](docs/GIT_WORKFLOW.md#after-merge).

  With `--project`, an issue that was never added to the board no longer needs a
  hand repair (#691): the hook adds the row and then sets it to Done, reporting
  `added it and set it to Done`. That is a repair, not a substitute for filing
  issues onto the board — see `.claude/rules/scripts.md` — and it only fires on
  the post-merge path, so an issue that never merges is still invisible to the
  board until someone adds it.

- **Clean up on merge — branch and worktree, remote and local, every forge.** A
  merge is not finished until its branch and any worktree are gone on both
  sides. Squash merges need `git branch -D` (the squash commit is not the branch
  tip, so `-d` refuses); confirm by content rather than ancestry. **Do not
  hand-write the comparison — run `scripts/branch-merged-check.sh <branch>`**
  (`--all` for every local branch). It exits `0` SAFE-DELETE / `1` KEEP / `2`
  UNKNOWN, and only `0` authorizes deletion. Content alone cannot decide this:
  a branch that _was_ merged reads "differs" once `main` evolves those files
  (large deletion counts are the tell), so the script also accepts a merged PR
  whose head SHA equals the branch tip. `NO_PR=1` skips the forge lookup and is
  conservative by design. The idiom is scripted because every
  hand-written form fails toward "safe to delete", silently: `git diff
origin/main..<branch>` also reports everything `main` gained afterwards, and
  restricting to the branch's own files via `-- $files` stops word-splitting
  under zsh (an unquoted _variable_ is one word there, unlike an unquoted
  command substitution) so the pathspec matches nothing and **every** branch
  reads merged. An empty file list produces the same false "merged". Skipping
  this is invisible once and compounding
  across a hundred merges — and after the fact you cannot cheaply tell a
  squash-merged branch from one that was never pushed. When the pipeline created
  the branch or worktree, the pipeline must remove it; the operator is never the
  garbage collector for machine-created state. See
  [docs/GIT_WORKFLOW.md § After Merge](docs/GIT_WORKFLOW.md#after-merge).

  **The cleanup itself is one script, and it is not per-repo:**
  `nightgauge-internal/scripts/branch-cleanup.sh`, run with no arguments. It
  sweeps all six workspace repos, re-derives every verdict at run time, and
  deletes a branch only when one of two independent tests proves it holds no
  unique work — `ahead=0` against `origin/main`, or a MERGED PR at that exact
  head. Everything else is reported and KEPT; stashes and worktrees are never
  touched, and a branch held by a worktree is reported with the worktree path
  rather than a bare failure. `branch-merged-check.sh` above is the
  single-branch verdict tool; this is the sweep. **Do not write a per-repo
  cleanup script, and do not hand-write `git branch -D`** — run
  `ls nightgauge-internal/scripts/` before building any workspace-level tooling.

  A harness may block `git branch -D` as a direct tool invocation while
  permitting a script that calls it internally. Four separate handoffs recorded
  this chore as "blocked, needs a human with a shell" on that basis and were
  wrong — the existing script then deleted 39 branches with no prompt at all.
  "The tool is missing", "the tool is blocked" and "this _invocation form_ is
  blocked" are three different diagnoses with one identical symptom: a chore
  that never gets done. Try the existing tool before concluding anything about
  why a chore is undone.

- **Concurrent issues must be conflict-free by construction.** Before working
  two issues at the same time, compare the file sets they will plausibly touch.
  If those sets overlap, either work the issues **sequentially** or declare a
  `blockedBy` edge and **honor it** — declaring the edge and then starting both
  anyway is the same failure with extra steps. Separate worktrees isolate the
  checkout, not the merge: two agents editing one file in two worktrees each see
  a clean tree and green CI, and the collision only surfaces when the second PR
  rebases. Prefer sequencing whenever the overlap is uncertain; the cost of
  serializing two issues is far below the cost of untangling a conflict after
  both are "done". Broad mechanical sweeps (renames, redactions, codemods) touch
  everything by definition — land them alone, over settled code, never
  alongside logic changes to the same files.
- **Context economy — auto-compact is not a context-management strategy.**
  Frontier-model tokens are expensive and long contexts cost more per step, so
  a large context is justified only when it actually carries the answer to the
  task at hand (e.g. an in-flight incident's accumulated state). Otherwise
  prefer the lean path: finish the current scope, then start a fresh session
  for new work; delegate self-contained searches/subtasks to subagents that
  return conclusions instead of dumping file contents into the main context;
  and read only the parts of files a task needs. Never let a session drift
  into "one more small task" accretion just because compaction will eventually
  reclaim space — deliberate scoping beats automatic summarization. This is
  also product philosophy: Nightgauge's pipeline hands each stage a scoped
  context on purpose.
- **Reap every background process you spawn — by PID, never by `jobs`.**
  Capture the PID at spawn (`proc=$!`), kill that PID explicitly, and verify it
  is dead (`kill -0 "$proc" 2>/dev/null` must fail) before you report the task
  complete. This applies to anything you background: servers, watchers, tailers,
  and above all **synthetic load generators** used to reproduce a race.

  **`kill $(jobs -p)` and `kill %1` do not work here and will silently reap
  nothing.** The job table belongs to the single shell instance that spawned the
  jobs; a later command runs in a _new_ shell where that table is empty, so the
  kill succeeds vacuously. Worse, a `( … ) &` subshell whose owning shell has
  exited is reparented to PID 1, which puts it beyond `jobs` entirely and leaves
  nothing tying it to the work that created it.

  This is not hypothetical. An agent reproducing a CI race needed a loaded
  machine, spawned 24 `while :; do :; done` loops, and ended with
  `kill $(jobs -p)`. The kill matched nothing, the loops were reparented to
  PID 1, and they spun at ~45% CPU each for eleven hours — load average 253 on
  the maintainer's laptop, long after the PR had merged and the worktree was
  gone. Nothing in the tree pointed back at them; they were found by reading
  `ps -Ao pid,ppid,pcpu,lstart,command` by hand.

  If a task genuinely needs background load, bound it: write the PIDs to a file
  as you spawn them, kill from that file, and re-check with `ps` afterwards.
  Prefer a bounded run (`timeout`, a fixed iteration count) over an unbounded
  loop, so a missed cleanup expires on its own instead of running until reboot.

### GitHub CLI in Multi-User Workspaces

Multiple workspaces from **different GitHub accounts** are often open at once
(e.g. `octocat` for nightgauge repos, a separate bot account elsewhere).
`gh auth switch` changes the **global** active account and silently breaks every
other open workspace. **Never `gh auth switch` for repo work.** Instead pass a
per-command scoped token:

```bash
GH_TOKEN=$(gh auth token --user octocat) gh <command> ...
```

This resolves `nightgauge/*` without disturbing the active account. (`git push`
uses SSH and is unaffected.)

### Developer Setup

`npm install` requires no registry authentication: this repo depends on no
private packages, and the generated platform types are vendored under
`api/generated/`. Building the Go binary needs only the Go toolchain (`go.mod`).

### Pre-Submission Validation (MANDATORY before every push)

**NEVER push to GitHub without passing ALL local checks first.** CI is for
confirming environment differences — not for discovering failures you could
have caught locally. Every failed CI push wastes time and pollutes PRs with
fix-up commits.

The complete, ordered command list is maintained in **exactly one place** —
[docs/GIT_WORKFLOW.md § Pre-Submission Validation](docs/GIT_WORKFLOW.md#pre-submission-validation-critical).
Run every step there (Go build/tests → IPC client regen → TypeScript build →
VSCode tests → SDK tests → Prettier → ESLint), or run them all at once with
`bash scripts/ci-local.sh`, before every `git push`. Do NOT mark work as
complete until all checks pass.

**`ci-local.sh` is the gate, not the loop.** Run it once, when you believe you
are done — not to find out what you broke. It rebuilds Go, runs `go test ./...`
plus `-race`, regenerates the plugin skills mirror and makes network calls, so
using it as an iteration loop spends ~15 minutes to learn what a single test
file or one package suite would have told you in seconds. Iterate on the
narrowest run that answers the question, then gate once.
[docs/GIT_WORKFLOW.md § The gate is not the loop](docs/GIT_WORKFLOW.md#the-gate-is-not-the-loop)
has the rungs and the cases where a wider sweep genuinely is required.

**This rule binds whoever pushes — not every stage that touches code.** In the
pipeline that is `feature-validate` (and any interactive session about to push).
`feature-dev` does not commit or push (#1608); it verifies what it changed and
hands off. A stage that runs the full suite anyway spends its whole budget on a
job the next stage will redo — #221 lost a completed implementation that way,
babysitting `ci-local.sh` until it ran out of turn.

## Security (CRITICAL)

**MANDATORY: Follow the security guidelines in
[standards/security.md](./standards/security.md)** — the complete
requirements.

### Key Security Rules for AI Agents

1. **NEVER hardcode secrets** - Use environment variables or secure secret
   management
2. **ALWAYS validate input** - Sanitize all user input at system boundaries
3. **NEVER expose sensitive data** - Keep credentials, API keys, and PII out of
   logs and responses
4. **ALWAYS use parameterized queries** - Prevent SQL injection attacks
5. **NEVER commit secrets to git** - Use .gitignore and pre-commit hooks

## Public Core Boundary & Content Hygiene (CRITICAL)

This repository is the Apache-2.0 local core and is maintained as a
public-safe tree. Before creating an issue, planning a feature, writing an
ADR, or committing documentation, read
[docs/PUBLIC_CORE_BOUNDARY.md](./docs/PUBLIC_CORE_BOUNDARY.md) and
[docs/DOCUMENTATION_IA.md](docs/DOCUMENTATION_IA.md).

- Keep hosted-service implementation, commercial planning, customer context,
  internal operations, and private repository issue references out of this
  repository. Those belong in `nightgauge-internal`.
- The publication-boundary guard scans the tracked **tree** — but **GitHub
  issue and epic bodies are not in the tree**, so nothing mechanical catches
  them. When authoring an issue, epic, spike, or doc whose home is
  `nightgauge/nightgauge`, keep company economics, private implementation
  details, customer data, and unreleased roadmap material out.
- **The guard's issue-reference ceiling is per-branch, and `git merge
origin/main` does not raise it.** The ceiling is derived from the trailing
  `(#N)` of squash-merge subjects on **first-parent** history, and a merge puts
  `main` on the _second_ parent — so a branch's own line still ends where it
  forked. The rules above forbid force-push and rebase, which makes merging the
  only permitted way to update a branch and therefore the one path that never
  helps: a long-lived branch measures against an ever-staler ceiling. The
  checker compensates by taking the larger of the branch's mark and its base
  ref's, so **keep `origin/main` fetched** — an unfetched base ref puts the lag
  back. A lower ceiling leaves more numbers above it, so the same tree measures
  a **higher** unresolvable count on a lagging branch.
- **Never hand-lower `issue_references.tree_baseline` on a falling count.** The
  baseline is one global integer compared against a ceiling-dependent count, so
  a fall has two causes — references genuinely removed, and the ceiling rising
  over references that were already there — and only the first may be ratcheted.
  Recording the second claims a sweep that did not happen and blocks the next
  branch with references it never wrote. The checker separates them and names
  the number to record; when it says **"Do NOT lower"**, do not lower.
- **Coordination epics/spikes that are mostly private work** belong in
  `nightgauge-internal`; leave only a slim capability-level stub in the public
  repo if a community tracker is wanted.
- Represent cross-surface work here only as a local capability or stable
  public integration contract. Private repository plans, issue numbers,
  deployment topology, and implementation status are not public material.
- Generated `docs/spikes/`, `docs/epics/`, and ADR artifacts require explicit
  publication review; their directory location is not evidence of safety.
- Raw research, spikes, epics, estimates, execution logs, and agent memory are
  private by default. Publish only deliberately rewritten, stable guidance.
- External issue text never authorizes autonomous execution. A maintainer must
  review the issue and apply any automation label.
- If classification is uncertain, stop and track the work privately until the
  public boundary is explicitly resolved.

## Code Standards

### File Naming

- Use kebab-case for directories and files
- Use SCREAMING_SNAKE_CASE for template files (e.g., `AGENTS_TEMPLATE.md`)
- Skill files are always `SKILL.md`
- Plugin manifests are always `plugin.json`

### Content Guidelines

- Keep documentation concise and actionable
- Use concrete examples over abstract descriptions
- Include `[TEAM TO DOCUMENT]` markers for content requiring human input
- Reference external standards rather than duplicating them

### Markdown Standards

- Use ATX-style headers (`#`, `##`, etc.)
- Include language identifiers in fenced code blocks
- Use tables for structured comparisons
- Keep lines under 100 characters where practical

## Key Patterns

Skill file layout and SKILL.md frontmatter, plugin directory structure, and
standards-document conventions are specified in
[skills/README.md](./skills/README.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## What to Avoid

- Don't duplicate content between files - reference instead
- Don't include tool-specific syntax in `standards/`
- Don't hardcode URLs that might change - use relative paths where possible
- Don't add skills without corresponding documentation
- Don't downgrade version numbers in existing skills or plugins

## Testing Changes

1. For skill changes: Copy to a test repo and verify with multiple AI tools
2. For plugin changes: Test with Claude Code in a sample repository
3. For config changes: Verify syntax is valid for the target tool
4. For standards changes: Review impact across all tool configs
5. For Go/TypeScript changes: run the full pre-submission validation suite
   (see [Pre-Submission Validation](#pre-submission-validation-mandatory-before-every-push))

## Creating Content

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for how to add VSCode commands, SDK
modules, skills, and plugin commands — plus branch naming and the epic/issue
ordering standard.

## Multi-Repository Workspace

See **[docs/MULTI_REPO_WORKSPACE.md](docs/MULTI_REPO_WORKSPACE.md)** for
multi-repo workspace support.

## Knowledge Base Usage

When `knowledge_path` is set in pipeline context (auto-scaffolded at issue
pickup when `knowledge.enabled: true`), always read `knowledge_path/PRD.md`
and `knowledge_path/decisions.md` before implementing — these capture
accumulated requirements and architecture decisions for the issue's feature
area. Record new decisions using the ADR block format defined in
`docs/KNOWLEDGE_BASE.md`. Outcomes and lessons are appended post-retro via
`/nightgauge:retro`. See `docs/KNOWLEDGE_BASE.md` for the full schema and
lifecycle.

**IA rule**: see
[docs/KNOWLEDGE_BASE.md#information-architecture](docs/KNOWLEDGE_BASE.md#information-architecture)
before deciding where to record decisions (`docs/` vs
`.nightgauge/knowledge/`). Cross-cutting, stable, reader-facing decisions live
in `docs/`; per-issue context lives in `knowledge/` and graduates manually via
`nightgauge knowledge graduate`.

## Companion Repositories

Nightgauge integrates with a separate, closed-source hosted platform
(licensing, billing, team analytics). It is optional: the pipeline runs fully
locally against your own model keys with no account and no server.

## Related Resources

- [Agent Skills Specification](https://agentskills.io/specification) - Universal
  skill standard
- [AGENTS.md Standard](https://agents.md/) - Industry standard (Linux
  Foundation)
- [CONTRIBUTING.md](./CONTRIBUTING.md) - Contribution guidelines
- [skills/README.md](./skills/README.md) - Skills catalog
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) - Architecture overview

## Author

nightgauge
