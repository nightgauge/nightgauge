# Documentation Publication Boundary

This repository is intended for public use. Documentation must help a user,
contributor, security reviewer, or integrator understand the open-source
product.

## Content that belongs here

- Installation, configuration, operation, troubleshooting, and security
- Public architecture and extension contracts
- Contributor workflows and governance
- Stable design decisions about the open-source implementation
- Synthetic examples and reusable templates
- Release notes and migration instructions for shipped behavior

## Content that does not belong here

- Company positioning, competitive analysis, or commercial plans
- Pricing models, margins, sales plans, or launch sequencing
- Private service architecture, deployment topology, or operational runbooks
- Cross-repository plans involving private repositories
- Unreleased product roadmaps and internal prioritization
- Real customer, credential, telemetry, or incident data
- Raw company research, spike artifacts, and epic execution summaries

Those materials belong in the private `nightgauge-internal` repository. Public
documents may state an integration contract or current user-visible behavior,
but must not explain a private implementation behind that contract.

## Generated workflow artifacts

Nightgauge supports repository-local artifacts such as `docs/spikes/` and
`docs/epics/`. Their contents are owned by the repository in which a user runs
the pipeline. In this project, generated artifacts require an explicit
publication review before being committed.

## Enforcement

`.github/publication-boundary.yaml` is fail-closed: every tracked path must be
classified. It also assigns stricter content rules to paths likely to contain
planning or private implementation details — path allowlisting, a regex
`forbidden_content` denylist, a hashed `forbidden_tokens` denylist, and an
`issue_references` ceiling all live in this one config. Passing the automated
check is necessary but does not replace human publication review.

`issue_references` rejects a **newly added** line citing an issue number above
the repository's high-water mark plus `slack` — a number this repository has
not issued cannot resolve at the moment it is written, and once the
repository's own numbering climbs past it, it stops 404-ing and starts
resolving to unrelated live work. The rule anchors on the mark rather than on
digit count, so it does not need revisiting when the numbering passes four
digits.

**The mark is derived, not recorded** (#1078). GitHub appends `(#N)` to the
subject when it squash-merges a pull request, so the guard reads the trailing
marker off the first-parent history — offline, with no network call, and with
nothing to bump. Only the trailing marker counts: a pull request titled
`feat: thing (#99999)` merges to `feat: thing (#99999) (#1080)` and reads 1080,
so a crafted title cannot raise the ceiling. `slack` remains because the
derived mark can only see what has already MERGED, and numbers are issued while
a pull request is open.

This replaced a hand-maintained integer that cost 21 chore commits and failed
**closed** every time it fell behind — rejecting legitimate references and
turning `main` red rather than degrading quietly. A shallow clone is detected
and refused for the same reason: the derived mark would be far too low, and the
guard will not check against a mark it had to invent.

The scope is the diff, not the tree: the rule is "nothing newly introduced",
and the tree's inherited predecessor references are a separate mechanical
sweep tracked by `tree_baseline`.

The guard's own fail-closed regression suite
(`scripts/test-publication-boundary.sh`) runs both in CI
(`.github/workflows/publication-boundary.yml`) and locally via
`scripts/ci-local.sh`, so a weakened rule (e.g. a deleted denylist entry)
surfaces before a push, not only after CI rejects it. That suite plants
deliberately-forbidden fixtures, so a companion check
(`scripts/test-publication-boundary-hermeticity.sh`) asserts it cannot dirty
the repository it tests: a run killed with `SIGKILL` leaves
`git status --porcelain` byte-identical, and the sandbox worktree such a run
leaks is reclaimed by the next run.

Note this scan covers the **files on disk** — tracked paths plus untracked,
non-`.gitignore`d ones — but GitHub issue and epic bodies are not files, so
nothing mechanical catches private content written there; see `AGENTS.md`
§ Public Core Boundary & Content Hygiene.

When uncertain, document the public contract here and keep the business or
private implementation rationale internal.
