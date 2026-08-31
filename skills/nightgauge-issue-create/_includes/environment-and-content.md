# Phases 1–2: Validate Environment & Build Issue Content — Procedural Detail

Detail bodies for Phase 1 (Validate Environment) and Phase 2 (Build Issue Content) of the `nightgauge-issue-create` skill. Read this when executing those phases.

## Contents

- [Phase 1: Validate Environment](#phase-1-validate-environment)
- [Phase 2: Build Issue Content](#phase-2-build-issue-content)

## Phase 1: Validate Environment

1. Verify Go binary is available — hard failure if not:

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found."
  echo "Install from: https://github.com/nightgauge/nightgauge/releases"
  echo "Or build locally: go build -o bin/nightgauge ./cmd/nightgauge"
  exit 1
fi
```

2. Verify GitHub token is available:

```bash
if [ -z "$GITHUB_TOKEN" ]; then
  echo "ERROR: GITHUB_TOKEN is not set. Set it before running this skill."
  exit 1
fi
```

3. Verify repository remote is GitHub-backed
4. Confirm at least one active milestone if milestone is required
5. Owner/repo is auto-detected by the binary from git remote config.
   Use `--owner` and `--repo` flags to override when needed.

If prerequisites fail, stop with exact remediation command.

## Phase 2: Build Issue Content

1. Parse user-provided description (or ask focused questions if missing).
2. Classify the issue before drafting:
   - **Implementation**: this issue is expected to ship code, config, docs, or
     tests
   - **Spike**: this issue is expected to end in a recommendation or decision,
     not broad implementation
   - If a request mixes uncertain capability discovery with concrete delivery,
     split it into a spike and a follow-up implementation issue instead of
     blending both into one ticket
3. Produce the issue title and body. **The body's `##` headings are a
   contract, not a style preference** — Phase 6 runs
   `/nightgauge:issue-audit` as this skill's terminal gate, and its Phase 5
   checks these exact headings. The canonical per-type table lives in
   [`nightgauge-issue-audit`'s SKILL.md, Phase 5](../../nightgauge-issue-audit/SKILL.md#phase-5-body-section-completeness);
   it is reproduced here so the author does not have to open it, and
   `scripts/check-issue-body-contract.py` fails CI if the two ever drift apart.

   | `type:` label             | Required `##` headings, in this order                       |
   | ------------------------- | ----------------------------------------------------------- |
   | feature / docs / refactor | Summary, Acceptance Criteria, Verification                  |
   | bug                       | Summary, Steps to Reproduce, Expected, Actual, Verification |
   | spike                     | Summary, Acceptance Criteria, Recommendations, Verification |
   | chore                     | Summary, Verification                                       |
   | epic                      | Summary, Sub-Issues, Acceptance Criteria, Verification      |

   **Casing is significant.** The audit matches
   `^##[[:space:]]+<heading>\s*$` with `grep -E` and no `-i`, so
   `## Acceptance criteria` does NOT satisfy `Acceptance Criteria`. Copy the
   headings from the table verbatim.

   Required headings are a floor, not a ceiling — the audit ignores headings
   it does not require. Add these wherever they carry information:

   - `## Business/user value` — why this is worth doing (strongly encouraged
     on every `feature` and `epic`)
   - `## Technical notes` — change targets, constraints, prior art
   - `## Related work` — sequencing against other issues

   Section content, by type:

   - **Summary** (every type): what is wrong or missing and what changes.
     Two to six sentences, no bullet-only bodies.
   - **Acceptance Criteria**: `- [ ]` checkboxes describing shipped or
     observable behavior (see item 4).
   - **Steps to Reproduce / Expected / Actual** (`bug`): the reproduction
     path, the behavior the contract promises, and the behavior observed. A
     bug without `Actual` is not reviewable, which is why the audit requires
     all three rather than folding them into Summary.
   - **Sub-Issues** (`epic`): the decomposition — one line per child, as
     `- [ ] <owner>/<repo>#<n> — <title>` once created, or as titled bullets
     when the epic body is written before Phase 3 creates them.
   - **Recommendations** (`spike`): the fenced `yaml recommendations` block
     required by [docs/SPIKE_CONTRACT.md](../../../docs/SPIKE_CONTRACT.md).
     Phase 2.X scaffolds this automatically.
   - **Verification** (every type): how a reviewer holding the tree and the
     command tells pass from fail without asking the author — one bullet per
     acceptance criterion naming the test file or package to add or extend
     (or the command for a manual check), the input, and the expected
     observable output. Name real paths; mark to-be-created files as such.
     Where a test pins behavior, say what edit makes it go red (a test that
     cannot fail proves nothing). "Add tests" and "verify it works" are not
     verification. For `epic`, the epic-level observable once every child
     has merged; for `chore`, the command whose output proves the chore is
     done (a `grep` that returns zero hits, a `go vet` that passes); for
     `bug`, the regression test that reproduces `Actual` today and `Expected`
     after the fix. Issue-audit warns (`WEAK_VERIFICATION`) when the section
     names no test, command, or file.

4. For implementation issues:
   - acceptance criteria MUST describe shipped or observable behavior
   - when upstream CLI or API behavior may have changed, include a guardrail
     such as: "If observed behavior differs from this assumption, stop and
     document findings before continuing implementation."
5. For spike issues:
   - title SHOULD start with `spike:`
   - acceptance criteria MUST end in an explicit recommendation such as
     `adopt`, `defer`, or `skip`
   - deliverables SHOULD be a short findings summary and the next issue to
     create if adoption is recommended
   - do NOT mix broad implementation work into the same issue
   - The deliverable artifact at `docs/spikes/<N>-*.md` MUST contain a single
     fenced `yaml recommendations` block per
     [docs/SPIKE_CONTRACT.md](../../../docs/SPIKE_CONTRACT.md). The block lists
     each follow-up with stable kebab-case `id`, `action`
     (`adopt`/`defer`/`skip`), `title`, `type`, `priority`, `size`, optional
     `labels`, optional `body`, and optional `depends_on`. The post-merge
     `spike-materialize` stage parses this block and files the follow-up
     issues automatically — `feature-validate` blocks the merge if the block
     is missing or fails schema validation.
   - **Phase 2.X** (below) auto-declares the artifact path and scaffolds a
     placeholder `yaml recommendations` block in the issue body — every spike
     issue created by this skill is contract-conformant by default.
6. When the issue creates new services, exports, or data producers, acceptance
   criteria SHOULD include integration requirements:
   - "X is consumed by Y" not just "X exists"
   - "End-to-end: [trigger] → [new component] → [consumer action] verified"
7. When creating epic sub-issues, flag sub-issues that create producers without
   corresponding consumer sub-issues. Epic acceptance criteria should include:
   "All new services have at least one consumer wired and verified"
8. Determine labels:
   - `type:*` from intent (label)
   - `component:*` if applicable (label)
   - Priority and Size are set as project board fields after issue creation, NOT
     as labels. The `nightgauge project add` command adds to the board;
     Priority and Size are set via separate GraphQL field mutations in Phase 4.
   - Status is set as a project board field via
     `nightgauge project sync-status`. Do NOT create or manipulate
     `status:*` labels.
   - **`owner-action` when the deliverable is not producible by an agent**
     (label). Ask one question before filing: _could an agent with full
     repository access and unlimited time produce this artifact?_ When the
     answer is no, the issue is a handoff to a human, and the label is what
     records that — it is the sole default entry of
     `autonomous.exclude_labels`, so the scheduler skips it and raises an
     owner-action card instead of dispatching a pipeline at it.

     Apply it to: sign-offs reserved to a licensed professional (legal,
     financial, medical, safety); acts only an operator can perform — rotating
     or issuing a credential, accepting terms, paying an invoice, registering a
     domain, provisioning hardware, submitting to a store or registrar;
     physical-world steps; and decisions that are the owner's to make and that
     no evidence in the repository can settle.

     The `type:` label is orthogonal and easy to mistake for this judgement. A
     legal review of published policy pages is `type:docs` **and**
     `owner-action`: it touches documentation, and no amount of agent effort
     produces counsel's signature. Labelling it `type:docs` alone is what
     dispatched the specimen issue into a pipeline that could only ever fail it.
     If an issue's acceptance criteria read as a checklist of things a _person_
     must confirm, it wants this label.

     @see Issue #1241

Keep issue text concise and actionable. Avoid placeholder-heavy boilerplate.

#### Epic Refinement Rules

When creating an epic with 3+ sub-issues:

1. Separate scope into:
   - **Execution-ready implementation work**
   - **Decision-oriented spikes**
2. The epic body carries this material under the required headings from item
   3 above — `Goal`, `Scope`, and `Sequencing` are NOT top-level `##`
   headings, because `## Summary` / `## Sub-Issues` / `## Acceptance Criteria`
   are what the audit requires of a `type:epic`:
   - `## Summary` — the goal, and the scope split into implementation work vs
     decision-oriented spikes
   - `## Sub-Issues` — one line per child, grouped or ordered by wave when
     sequencing matters; prerequisites belong here as `Depends on:` notes or
     as the `blockedBy` edges Phase 3.5 sets
   - `## Acceptance Criteria` — epic-level criteria, i.e. what is true once
     every child has merged
3. Do not create a `feat:` or `chore:` sub-issue when the real deliverable is
   still feasibility, verification, or recommendation.
4. If a sub-issue depends on verifying upstream tool behavior first and safe
   implementation boundaries are unclear, default to a spike.
5. Include a capstone docs or validation issue when the epic changes runtime
   behavior or support expectations.

#### Acceptance Criteria Quality Bar

Before finalizing any issue, check:

- Does each acceptance criterion describe a concrete outcome or decision?
- Is discovery work disguised as implementation?
- If external tool behavior matters, is there an explicit verification guard?
- For spike issues, does the issue end with `adopt`, `defer`, or `skip`?
- For epics, are implementation items and spikes clearly separated?
- Does every acceptance criterion have a matching `## Verification` bullet a
  reviewer could run? An AC with no way to observe it is a wish, not a
  criterion — rewrite or drop it.
- Does the ask preserve one path? Under the no-backwards-compat doctrine
  (`AGENTS.md` § Agent Operating Rules) an issue must never request a compat
  shim, a migration fallback, a feature flag that keeps both behaviors, or
  "support the old format too". Consolidate to the single resolved behavior
  and say what gets deleted.

#### Security Constraints (design time)

Security is judged on the **proposed design**, not on the current code, and
it is cheapest here: a hazard caught at creation costs one sentence, the same
hazard caught at `feature-validate` costs a re-plan, and one caught after
merge ships. Whenever the design as written touches any of:

- authentication or authorization (who may call this, on whose behalf)
- secrets, tokens, keys, or credentials (where stored, where logged)
- a network endpoint, webhook, or inbound payload
- retries, fan-out, spawning, or anything unbounded
- file writes or path handling (containment, symlinks, `..`)
- shell or exec from configuration or issue text
- model-authored text reaching a mutating tool or a filed issue

then name the hazard and state its mitigation **as an acceptance criterion**
(a `- [ ]` line), so the pipeline cannot close the issue without it, and
optionally group the reasoning under a `## Security constraints` section.
The bar is `standards/security.md`. Issue-audit warns
(`SECURITY_SURFACE_UNADDRESSED`) when the body names such a surface and
carries no security-shaped criterion. Hazards the 2026-08-29 groom had to
append after the fact — every one avoidable at creation: an endpoint whose
"token" was a guessable topic name anyone could subscribe to; a worker
calling the platform with one shared credential and a self-asserted account
id (confused deputy); a 403 retried forever; a shell string executed from
repo config; an artifact `path` with no containment check; fetched web pages
turned into issue bodies with no injection boundary.

#### Parent Epic Fit

An issue that lands under the wrong epic — or under none — is invisible to
the wave planner and to the next groom. Before Phase 3:

1. List the open epics (`nightgauge forge issue list --label type:epic
--json`) and read each one's `## Acceptance Criteria`.
2. Choose the parent whose criteria this issue **advances**. Link it in
   Phase 3.5 through `addSubIssue` — the sub-issue graph is the membership
   record; "Part of #N" body text is not.
3. If two epics plausibly fit, pick one and name the other in
   `## Related work` with one line on why not, so the next groom does not
   re-litigate it.
4. If no open epic fits and the work is three or more issues, create the epic
   first (Phase 2.9 rules) and file the issues under it. A standalone issue
   is fine when it is genuinely one; a cluster of orphans is not.
5. Never parent to a closed epic, and never parent an issue in one repo to
   an epic in a repository that is archived or unreachable — the groom cannot
   maintain either.

#### Size Prediction from Complexity Model

When `.nightgauge/complexity-model.yaml` exists, use it to determine the
Size field value deterministically instead of guessing. This ensures sizing is
data-driven and improves over time via the feedback loop.

**Steps:**

1. Read `.nightgauge/complexity-model.yaml`
2. Look up `type_adjustments[type].modifier` for the issue type (default `0`)
3. Look up `priority_adjustments[priority].modifier` for the priority (default
   `0`)
4. Sum them to get the base score
5. Scan `patterns.high_complexity`, `patterns.medium_complexity`, and
   `patterns.low_complexity` — for each pattern whose `match` regex matches the
   issue title or description, add `modifier × confidence` to the score
6. Map the final score to a size label:
   - `XS`: score < −1.5
   - `S`: −1.5 ≤ score < −0.5
   - `M`: −0.5 ≤ score < 0.5
   - `L`: 0.5 ≤ score < 1.5
   - `XL`: score ≥ 1.5

#### File-Based Sizing Heuristics

After computing the base score from type/priority/patterns, apply file-based
adjustments from technical notes:

1. **Multi-service detection**: If technical notes reference 3+ files across
   different service directories, add +0.5 to the score. If 5+ files, add +1.0.

2. **Critical file registry**: Read `critical_files.registry` from the
   complexity model. For each file in the registry that appears in the technical
   notes, add `critical_files.per_file_modifier` to the score (capped at
   `critical_files.max_modifier`). Default: +0.5 per file, max +1.5.

   Default critical files:
   - HeadlessOrchestrator.ts
   - PipelineStateService.ts
   - skillRunner.ts
   - AutoModelSelector.ts
   - ProjectBoardService.ts

> **Model routing consequences**: Size directly determines which model runs each
> pipeline stage. `size:S` routes validation to Haiku. `size:L` routes planning
> and dev to Opus. Undersizing causes the AutoModelSelector to assign weaker
> models to complex infrastructure work, increasing failure rates.

**Fallback:** If the complexity model file is missing or unreadable, fall back
to estimating size from expected effort as before.

**Objective size estimate via Go binary:** For an objective size estimate based
on issue metadata (title, body, labels, sub-issue count), call:

```bash
nightgauge size predict <issue-number> --json
```

Use `SizeLabel` from the output as the recommended project board Size field.
This is especially useful for existing issues being re-assessed or when the
complexity model file is absent.
