/**
 * AutoRetroService.terminalKindCoverage.test.ts
 *
 * THE DRIFT GUARD for the run record's authoritative terminal kind (#1448).
 *
 * `terminal_failure_kind` is a field the run record CARRIES, not a guess — the
 * kind was decided by `internal/terminalkind/table.json`, the single definition
 * of how a failure's error text becomes a terminal kind. So when the retro
 * classifier sees that field it must DECIDE, and the one thing it may never do
 * is drop the kind and go fishing in prose for a keyword: that is how a
 * credential fault got written up as `state-management` with the remedy
 * "re-run the failed stage after verifying context" (#878), a remedy that
 * cannot work for a kind the record had already named exactly.
 *
 * The map used to be five hand-listed kinds against a vocabulary of 39, and
 * nothing failed when the vocabulary grew. This suite reads the canonical kind
 * list out of table.json — the source of truth itself, not a copy of it — and
 * fails when any kind in it does not reach a category through the record
 * extractor. A kind added to table.json therefore lands here as a red test
 * rather than as a silent fall-through in production.
 *
 * The compiler is the other half of the guard: the map in AutoRetroService.ts
 * is typed `Record<TerminalFailureKind, RetroFailureCategory>`, and that union
 * is pinned to Go's `TerminalKind*` constants by
 * failureClassifier.parity.test.ts (#229), which table.json's kinds are in turn
 * pinned to by TestTable_EveryKindHasCorpusCoverage. A new kind cannot reach
 * table.json without a Go constant, cannot get a Go constant without a union
 * member, and cannot get a union member without an entry in the map. This test
 * is what makes that chain observable at the retro end of it.
 *
 * @see Issue #1448
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { AutoRetroService } from "../../src/services/AutoRetroService";
import type { RetroFailureCategory } from "../../src/services/AutoRetroService";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, "../../../..");
const TABLE_PATH = path.join(REPO_ROOT, "internal/terminalkind/table.json");

interface TerminalKindTableJson {
  rules: Array<{ kind: string }>;
  kinds_without_rules: string[];
}

/**
 * Every kind table.json can produce: the kinds its rules derive from error
 * text, plus the kinds it declares are set structurally and so have no rule.
 *
 * Read from the file rather than from the generated TypeScript view of it,
 * because terminalKind.corpusParity.test.ts fences the generated module to a
 * single reader — the interpreter — and a second reader in `src/` would be red
 * there. A test reading the JSON is outside that fence and still measures
 * against the same bytes Go embeds.
 */
function canonicalKinds(): string[] {
  const table = JSON.parse(readFileSync(TABLE_PATH, "utf-8")) as TerminalKindTableJson;
  const kinds = new Set<string>(table.rules.map((r) => r.kind));
  for (const kind of table.kinds_without_rules) kinds.add(kind);
  return [...kinds].sort();
}

/** A minimal V3 run record carrying exactly one terminal kind and nothing else. */
function classifyRecord(kind: string) {
  return AutoRetroService.classifyFailure(
    {
      text: `{"schema_version":"3","issue_number":1448,"outcome":"failed","terminal_failure_kind":"${kind}","total_duration_ms":1000}`,
      sourcesAnalyzed: ["execution_history"],
    },
    "feature-dev"
  );
}

describe("AutoRetroService: terminal-kind coverage (#1448)", () => {
  const kinds = canonicalKinds();

  it("read a non-trivial canonical kind list from table.json (sanity check)", () => {
    // Guards the reader itself: an empty list would make every assertion below
    // pass vacuously — an absence of unmapped kinds is not the presence of a map.
    expect(kinds.length).toBeGreaterThan(30);
    expect(kinds).toContain("stall_kill");
    expect(kinds).toContain("orchestrator_crash");
  });

  it("decides every canonical terminal kind from the record instead of falling through", () => {
    const undecided: string[] = [];
    for (const kind of kinds) {
      const findings = classifyRecord(kind);
      const primary = findings[0];
      const fromRecord = primary.evidence.some((line) =>
        line.includes(`Run record terminal_failure_kind: ${kind}`)
      );
      if (!fromRecord || primary.category === "unknown") {
        undecided.push(`${kind} -> ${primary.category}`);
      }
    }
    expect(
      undecided,
      "a terminal kind defined in internal/terminalkind/table.json produced no category from " +
        "the run-record extractor in AutoRetroService.ts, so the run fell through to prose " +
        "keyword guessing (or `unknown`) while the record already named the cause exactly. " +
        "Add the kind to TERMINAL_KIND_CATEGORY with a category whose recommendation is a " +
        "remedy for THAT kind."
    ).toEqual([]);
  });

  it("assigns no canonical kind a severity the category dictionaries cannot supply", () => {
    // buildFinding reads summary/recommendation/severity from three dictionaries
    // keyed by category. A category added to the union but missed in one of them
    // is a compile error; this checks the runtime result is populated too, so a
    // finding can never reach an operator with an empty remedy.
    for (const kind of kinds) {
      const primary = classifyRecord(kind)[0];
      expect(primary.summary, `empty summary for ${kind}`).not.toBe("");
      expect(primary.recommendation, `empty recommendation for ${kind}`).not.toBe("");
      expect(["low", "medium", "high"], `bad severity for ${kind}`).toContain(primary.severity);
    }
  });

  describe("the categories the record maps to are the intended ones", () => {
    // Pinned so a later edit cannot quietly re-point a kind at a category whose
    // remedy does not apply to it. These are the kinds #1448 names as evidence
    // plus the five that predate it (whose categories must not change).
    const PINNED: Array<[string, RetroFailureCategory]> = [
      // Pre-#1448 entries — unchanged.
      ["stall_kill", "stall-kill"],
      ["budget_exceeded", "budget-exceeded"],
      ["validation_error", "validation-failure"],
      ["subagent_crash", "stall-kill"],
      ["orchestrator_crash", "state-management"],
      // Added by #1448.
      ["adapter_auth_failed", "adapter-unavailable"],
      ["permission_denied", "permission-denied"],
      ["branch_forked", "work-stranded"],
      ["commit_orphaned", "work-stranded"],
      ["abandoned_commit", "work-stranded"],
      ["dev_tests_failed", "validation-failure"],
      ["validation_inconclusive", "validation-inconclusive"],
      ["model_unavailable", "model-unavailable"],
      ["rate_limit_quota_exhausted", "quota-exhausted"],
      ["github_rate_limited", "quota-exhausted"],
      ["containment_breach", "containment-breach"],
      ["blocked_dependency", "dependency-blocked"],
      ["architecture_approval_required", "human-decision-required"],
      ["issue_closed", "no-work-required"],
      ["pr_merge_unmerged", "skill-no-op"],
      ["stage_context_unreadable", "state-management"],
      ["api_overloaded", "infrastructure-outage"],
    ];

    it.each(PINNED)("%s classifies as %s", (kind, expected) => {
      expect(kinds, `${kind} is no longer a canonical kind`).toContain(kind);
      expect(classifyRecord(kind)[0].category).toBe(expected);
    });
  });
});
