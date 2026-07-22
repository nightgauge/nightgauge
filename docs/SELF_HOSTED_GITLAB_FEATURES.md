# Self-hosted GitLab — CE vs EE feature matrix

This document is a stub feature-matrix reference for the GitLab adapter
(`internal/gitlab/`). The full operator setup guide — token scopes, instance
URL discovery, auth chain ordering, multi-repo workspace wiring — is tracked
as W6-2 (#3369) and supersedes this skeleton.

The purpose of this page: tell an operator running a self-hosted GitLab
which Nightgauge project-board features will work, what they degrade
to on Community Edition, and what is a hard requirement.

## Field matrix

| Field                     | EE behaviour                                                                                                 | CE behaviour                                                                             |
| ------------------------- | ------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------- |
| **Status**                | Scoped label `Status::<value>`, server-side mutex                                                            | Same — requires scoped labels (free) for mutual exclusion; falls back to last-write-wins |
| **Iteration**             | Native `iteration_id` (group iterations)                                                                     | Project milestone fallback — auto-creates a milestone with the iteration title           |
| **Weight**                | Native `weight` field                                                                                        | Returns `forge.ErrUnsupportedOnEdition` — caller treats as non-fatal warning             |
| **Health**                | Native `health_status` enum (`on_track`, `needs_attention`, `at_risk`)                                       | Scoped label `Health::<value>` fallback                                                  |
| **Generic single-select** | Scoped label `<field>::<option>` (auto-created)                                                              | Same                                                                                     |
| **Estimate / Hours**      | Maps to `weight`                                                                                             | Maps to scoped label `Estimate::<n>`                                                     |
| **Date fields**           | Native `due_date` for "Target date"; scoped label otherwise                                                  | Same                                                                                     |
| **Priority / Size**       | Scoped label `Priority::<value>` / `Size::<value>`; legacy `priority:high` / `size:M` labels also recognised | Same                                                                                     |

## Scoped labels are required for Status

GitLab CE technically supports scoped labels (free feature), but they are
disabled on some self-managed instances by admin policy. When the
`label-status` strategy is configured (the default) and scoped labels are
disabled, Status writes fall back to last-write-wins ordering. The adapter
logs a one-shot warning the first time this is detected at runtime so the
operator can re-enable scoped labels on the project.

## Status strategy: `label-status` vs `state-only`

`forge.Config.BoardStatusStrategy` selects the Status mapping mode:

- **`label-status`** (default): Status writes set a `Status::<value>`
  scoped label on the issue. Multiple statuses are supported; status moves
  are reversible without state churn. **Requires scoped labels.**
- **`state-only`**: Status writes map `Done` to issue close, anything else
  to reopen + clear `Status::*`. Simpler — no scoped label dependency —
  but cannot represent intermediate states (`In progress`, `In review`).
  Writing those statuses returns an error.

Pick `state-only` when:

- The instance has scoped labels disabled and you don't need an
  intermediate-state board.
- The team's existing workflow already maps Done = closed.

Otherwise stay on `label-status`.

## Edition detection

The adapter probes `GET /api/v4/license` on the first call requiring an
edition decision. The result is cached on the client via `sync.Once`:

- **200** → `EditionEE`
- **403 / 404** → `EditionCE`
- **401** → `EditionUnknown` (likely token missing the `api` scope —
  re-auth and retry)
- **5xx / network** → propagates to the caller

The license body itself is not retained — only the edition classification
is cached.

## Sub-issues and blocking relationships

Parent-child sub-issue relationships and blocking links work on **both CE
and EE** via the REST `/api/v4/projects/:id/issues/:iid/links` endpoint. No
Premium subscription required.

| Operation                | Implementation                                                     | Edition |
| ------------------------ | ------------------------------------------------------------------ | ------- |
| Add / remove sub-issue   | `link_type=relates_to` on the parent's `/links` endpoint           | CE + EE |
| Add / remove blocked-by  | `link_type=is_blocked_by` on the blocked issue's `/links`          | CE + EE |
| Inverse `blocks` link    | Materialised automatically by GitLab on the linked issue           | CE + EE |
| Native `parent_id` write | Best-effort secondary write — failure swallowed, link is canonical | EE only |

**CE-only limitation — `relates_to` is symmetric and untyped.** GitLab CE
does not distinguish a "merely related" link from a sub-issue link on the
wire — both surface as `link_type=relates_to`. The adapter's convention is
that **outgoing `relates_to` links from issue X are X's children** (mirrors
the way `AddSubIssue(parent, child)` POSTs to the parent's `/links`). On EE
installs that have a native `parent_id` field, the adapter writes it as a
best-effort secondary action so the link is unambiguous; the `relates_to`
link remains the canonical readback path on every edition.

**Closed-blocker filtering.** The `BoardItem.BlockedBy` enrichment drops
links whose linked issue's `state` is anything other than `opened` so the
VSCode `isBlocked()` predicate matches the GitHub adapter's semantics — a
closed blocker does not count as blocking. See
`internal/gitlab/links.go:classifyLinks`.

**`ListItems` is intentionally not enriched.** Per-item link enrichment on
`BoardService.ListItems` would be N+1 over hundreds of board entries.
Enrichment is applied only on `GetIssue` and `BoardService.GetItem` (single
extra GET per item). Bulk-fetch enrichment via GitLab GraphQL is tracked as
a follow-up.

## Wave planning parity

`EpicService.PlanWaves` delegates the wave-assignment computation to the
shared `internal/intelligence/teams.PlanWavesFromIssues` helper used by the
GitHub adapter. Identical dependency graphs produce identical wave
assignments regardless of which forge backed the fetch — pinned by
`TestParityContract_PlanWaves` in `internal/forge/parity_test.go`.

## Token scopes (preview)

The full operator setup guide at W6-2 (#3369) covers token-scope
requirements. For the field-mapping operations covered by this document,
the token must hold the `api` scope. PAT, OAuth, CI job token, and deploy
token chains are tracked under W2-2 (#3354).

## Cross-references

- `internal/gitlab/edition.go` — edition probe implementation
- `internal/gitlab/project.go` — per-field dispatch table
- `internal/gitlab/board.go` — read-side mapping (`rawIssueToBoardItem`)
- `internal/forge/types/project.go` — `Iteration`, `HealthStatus` types
- `internal/forge/parity_test.go` — cross-forge round-trip parity contract
- `docs/decisions/006-forge-abstraction.md` — adapter contract rationale
- W6-2 (#3369) — full operator setup guide (canonical)
