# Codex Adapter Feature Parity Audit

**Date:** 2026-04-08 **Author:** nightgauge **Status:** Decided
**Issue:** #2587
**Epic:** #2583 — Wave 1 Codex CLI hardening

---

## Executive Summary

This spike audited the Codex CLI adapter (`CodexAdapter.ts`) against the latest
Codex CLI (0.111.0+) and the Claude SDK adapter (gold standard). Six capability
gaps were identified. Two HIGH-priority findings required immediate code fixes.
Four items were deferred pending further investigation or separate follow-up
issues.

**Immediate fixes in this issue:**

1. `nativeTokenTracking` corrected from `true` → `false` in `CodexAdapter.ts`
2. Parity matrix updated to reflect session resume adoption (Issue #1659)
3. Minimum version requirement updated from `0.98.0` → `0.111.0` in parity matrix

---

## Findings and Decisions

### Finding 1: `nativeTokenTracking: true` Claim Was Incorrect

**Severity:** HIGH

**Evidence:**

- `summarizeCodexJsonOutput()` in `adapterQuery.ts` extracts no token fields
  from Codex JSONL output
- `CodexJsonSummary` interface has no `usage` field (unlike `GeminiStreamJsonUsage`)
- The `CODEX_CLI_PARITY_MATRIX.md` already documented `nativeTokenTracking: false`
  in the "Investigated But Not Adopted" table
- The comment in `CodexAdapter.ts` referenced "Verified against codex-cli 0.98.0"
  but provided no actual token field names or JSONL example

**Decision: ADOPT — corrected immediately.**

`nativeTokenTracking` set to `false` with an accurate comment explaining the
reality: Codex JSONL output (`thread.started`, `item.completed`, `turn.completed`)
does not include token usage fields. External estimation is required for
Codex pipelines.

---

### Finding 2: Parity Matrix Inconsistent with Session Resume Implementation

**Severity:** HIGH (documentation debt)

**Evidence:**

- Parity matrix listed `exec resume` as "Not adopted; `sessionResume: false`"
- But `CodexAdapter.ts` correctly had `sessionResume: true`
- Issue #1659 integrated session resume in `cliQueryHelper.ts` (lines 140–196)
- Session resume works via `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`

**Decision: ADOPT — parity matrix updated.**

Moved `exec resume` from "Investigated But Not Adopted" to a new "Adopted After
Initial Parity Matrix" section. Added accurate notes on how it works.

---

### Finding 3: Minimum Version in Parity Matrix Stale

**Severity:** LOW

**Evidence:**

- `CodexAdapter.ts` uses `MIN_KNOWN_VERSION = "0.111.0"` (line 70)
- Parity matrix stated `0.98.0` in the "Adopted Adapter Behavior" table
- These had been out of sync since the `0.111.0` bump

**Decision: ADOPT — parity matrix updated to `0.111.0`.**

---

### Finding 4: Go Adapter Uses Different Execution Path

**Severity:** MEDIUM (informational — tracked separately)

**Evidence:**

- `internal/execution/adapters/codex.go` uses `--approval-mode full-auto`
  and `--prompt-file` (different from TypeScript adapter's `exec --full-auto --json`)
- No session resume, ephemeral, or sandbox flags in the Go adapter
- Investigation confirmed these are intentionally different execution paths:
  - Go path: skill runner auto mode (scheduler-driven)
  - TypeScript path: VSCode extension IPC mode

**Decision: DEFER — filed as Follow-Up Issue #2589.**

The Go adapter is a different execution path; parity is desirable but separate.
Issue #2589 will align Go adapter with TypeScript capabilities.

---

### Finding 5: Tool Restrictions Not Supported

**Severity:** MEDIUM

**Evidence:**

- Claude adapter passes `--allowedTools` from SKILL.md frontmatter (via Go adapter)
- Codex adapter has no tool restriction support
- Codex CLI documentation not reviewed for tool restriction flags

**Decision: DEFER.**

Contingent on Codex CLI capability confirmation. If Codex CLI supports tool
restrictions, a follow-up issue should add `allowedTools?: string[]` to
`QueryFunctionOptions` and implement in `CodexAdapter.createQueryFunction()`.

---

### Finding 6: Cost/Token Budget Limits Not Supported

**Severity:** MEDIUM

**Evidence:**

- Claude adapter supports `--max-budget-usd`, `--max-tokens`, `--max-turns`
  (Go adapter, `claude.go` lines 49–61)
- Codex adapter has no budget/limit flags
- Codex CLI documentation not reviewed for these flags

**Decision: DEFER.**

Contingent on Codex CLI capability confirmation. If Codex CLI supports budget
limits, a follow-up issue should add `costBudget?: number`, `maxTokens?: number`,
and `maxTurns?: number` to `QueryFunctionOptions`.

---

## Capability Parity Summary (Post-Spike)

| Capability            | Claude SDK | Codex (TypeScript) | Codex (Go)  | Status        |
| --------------------- | ---------- | ------------------ | ----------- | ------------- |
| Interactive mode      | ✓          | ✗                  | ✗           | By design     |
| Session resume        | ✓          | ✓ (opt-in)         | ✗           | Gap (#2589)   |
| Stream JSON output    | ✓          | ✓                  | ✗           | Gap (#2589)   |
| Native token tracking | ✓          | ✗                  | ✗           | Not available |
| Ephemeral mode        | N/A        | ✓                  | ✗           | Gap (#2589)   |
| Model selection       | ✓          | ✓                  | ✓           | Parity        |
| Tool restrictions     | ✓          | ✗                  | ✗           | Deferred      |
| Cost budget limits    | ✓          | ✗                  | ✗           | Deferred      |
| Sandbox mode          | N/A        | ✓                  | ✗ (partial) | Gap (#2589)   |

---

## Follow-Up Issues

| Issue | Title                                         | Priority |
| ----- | --------------------------------------------- | -------- |
| #2588 | Add Session Resume Integration Test for Codex | HIGH     |
| #2589 | Sync Go Codex Adapter with TypeScript Adapter | HIGH     |

---

## Files Changed

- `packages/nightgauge-sdk/src/cli/adapters/CodexAdapter.ts` — Fixed `nativeTokenTracking: false`
- `docs/strategy/codex/CODEX_CLI_PARITY_MATRIX.md` — Corrected session resume status, minimum version
- `docs/decisions/003-codex-adapter-feature-parity.md` — This document
- `packages/nightgauge-sdk/src/__tests__/cli/adapters/CodexAdapter.test.ts` — Added unit tests
