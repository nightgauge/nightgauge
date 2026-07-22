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
3. Produce issue title and body with:
   - Problem statement
   - Business/user value
   - Acceptance criteria
   - Technical notes (if known)
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

Keep issue text concise and actionable. Avoid placeholder-heavy boilerplate.

#### Epic Refinement Rules

When creating an epic with 3+ sub-issues:

1. Separate scope into:
   - **Execution-ready implementation work**
   - **Decision-oriented spikes**
2. The epic body SHOULD include:
   - Goal
   - Scope grouped by implementation vs spike
   - Sequencing or prerequisites
   - Epic-level acceptance criteria
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
