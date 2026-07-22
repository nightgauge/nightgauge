# Go / TypeScript Parity

The Nightgauge pipeline engine runs on two languages with a deliberate
split: **determinism lives in Go, UX lives in TypeScript.**

## The split

### Go binary (`cmd/nightgauge/`, `internal/*`)

- Project board sync, state file updates, JSON validation
- Hook evaluation (pre-commit, pre-push gates)
- GitHub API calls that must be fast and reproducible
- Epic / sub-issue graph traversal
- Knowledge base scaffolding primitives (this file was produced by one)

### TypeScript (`packages/nightgauge-sdk`, `packages/nightgauge-vscode`)

- Agent orchestration and the prompt surface to Claude
- VSCode UI, webviews, tree views, dashboards
- Per-stage context handoff logic
- Long-running async flows (SSE consumers, progressive rendering)

## Invariants

1. **Anything deterministic (fixed input → fixed output, zero LLM tokens)
   belongs in Go.** Zero tolerance for creeping AI calls into `internal/`.
2. **Anything user-facing or model-facing belongs in TypeScript.**
3. **Shared logic gets a Go primitive + a TypeScript consumer.** The TS
   layer calls the Go binary via subprocess; the Go binary never calls the
   TS layer.
4. **Tests in both layers** for features that cross the boundary (e.g.,
   wiki-link resolution has matching Go and TS unit tests).

## When a new feature lands

Decide early: is the behavior deterministic? If yes, prototype it in Go
and expose it as a CLI subcommand. If no, it probably belongs in the SDK
or the extension.

---

Edit this file to reflect current ecosystem state — this was scaffolded by
`nightgauge knowledge workspace-init`.
