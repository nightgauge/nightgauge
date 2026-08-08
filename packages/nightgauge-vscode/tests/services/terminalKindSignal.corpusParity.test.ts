/**
 * terminalKindSignal.corpusParity.test.ts
 *
 * Terminal-kind SIGNAL — extension side (#306).
 *
 * `classifyTerminalKindForSignal` is the classifier with the most authority per
 * line: its answer is sent to Go with `autonomousComplete`, and Go's
 * NotifyComplete only re-classifies when it received an EMPTY kind. A non-empty
 * answer is used VERBATIM.
 *
 * It is no longer a ladder, so the interesting assertions are structural rather
 * than sampled. The signal runs the canonical table and projects the WINNING
 * rule only when that rule is declared `signal: true`, which bounds it from both
 * sides — it can never name a kind other than the one the record will carry, and
 * it must answer whenever the winner is in the subset. Round 2's version could
 * only assert a lower bound ("these eight kinds appear somewhere"), which is why
 * a new rule invented for this ladder passed with every suite green.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { classifyTerminalKindForSignal } from "../../src/services/terminalKindSignal";
import { ARCHITECTURE_APPROVAL_REQUIRED_MARKER } from "../../src/utils/failureComment";
import { classifyTerminalKind, terminalKindStressInputs } from "@nightgauge/sdk";

const REPO_ROOT = path.resolve(__dirname, "../../../..");
const CORPUS_PATH = path.join(REPO_ROOT, "internal/terminalkind/testdata/corpus.json");
const TABLE_PATH = path.join(REPO_ROOT, "internal/terminalkind/table.json");
const GOLDEN_PATH = path.join(REPO_ROOT, "internal/terminalkind/testdata/stress-golden.json");

interface CorpusCase {
  id: string;
  input: string;
  expected: string;
  expected_signal: string;
  rationale: string;
}

interface TableRule {
  id: string;
  kind: string;
  signal: boolean;
  clauses: string[][];
}

interface TableSignalExtension {
  id: string;
  kind: string;
  clauses: string[][];
}

const corpus: { cases: CorpusCase[] } = JSON.parse(readFileSync(CORPUS_PATH, "utf-8"));
const table: { rules: TableRule[]; signal_extensions: TableSignalExtension[] } = JSON.parse(
  readFileSync(TABLE_PATH, "utf-8")
);

/**
 * The kind a DECLARED signal extension gives this input, or "". Extensions are
 * the ONE place the fleet's reaction may name a kind the record does not, they
 * are data in table.json rather than code here, and they are consulted only
 * when the rule table projects no signal of its own.
 */
function extensionKindFor(input: string): string {
  const t = input.toLowerCase();
  for (const extension of table.signal_extensions) {
    for (const clause of extension.clauses) {
      if (clause.every((term) => termHolds(t, term))) return extension.kind;
    }
  }
  return "";
}

/** Literal containment, or word-bounded containment for a `~` term. */
function termHolds(lowered: string, term: string): boolean {
  if (!term.startsWith("~")) return lowered.includes(term);
  const lit = term.slice(1).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?<![0-9a-z_])${lit}(?![0-9a-z_])`).test(lowered);
}
const golden: { cases: { input: string; kind: string; signal: string }[] } = JSON.parse(
  readFileSync(GOLDEN_PATH, "utf-8")
);

describe("terminal-kind signal parity (extension)", () => {
  it("signals exactly what the corpus pins, row by row", () => {
    // Both bounds in one assertion. Flipping `signal` on any table rule turns
    // every row that rule wins red, in this suite and in the Go and SDK ones —
    // the subset cannot be widened or narrowed from any single side.
    const mismatches: string[] = [];
    for (const c of corpus.cases) {
      const got = classifyTerminalKindForSignal(c.input) ?? "";
      if (got !== c.expected_signal) {
        mismatches.push(
          `${c.id}\n    input:     ${JSON.stringify(c.input)}\n` +
            `    signalled: ${JSON.stringify(got)}\n` +
            `    corpus:    ${JSON.stringify(c.expected_signal)}\n` +
            `    why this row exists: ${c.rationale}`
        );
      }
    }
    expect(
      mismatches,
      `The signal projection disagrees with the corpus on ${mismatches.length} input(s). ` +
        `Because a non-empty signal is used VERBATIM by Go's NotifyComplete, each one is a run ` +
        `whose record and whose fleet reaction could disagree:\n\n${mismatches.join("\n\n")}\n`
    ).toEqual([]);
  });

  it("never signals a kind that disagrees with the record, outside declared extensions", () => {
    // Over the derived stress set, not a sample: every clause, every term, every
    // ordered rule pair, and every signal-extension clause.
    const conflicts: string[] = [];
    for (const input of terminalKindStressInputs()) {
      const signal = classifyTerminalKindForSignal(input);
      if (signal === undefined) continue;
      const record = classifyTerminalKind(input);
      if (signal === record) continue;
      if (extensionKindFor(input) === signal) continue;
      conflicts.push(`${JSON.stringify(input)}: signal=${signal} record=${record}`);
    }
    expect(conflicts.slice(0, 10)).toEqual([]);
  });

  it("declares every divergence it produces, and produces every one it declares", () => {
    // Both directions over the derived set: a signal that differs from the
    // record must come from a declared extension, and every declared extension
    // must actually produce such a divergence somewhere — otherwise it is dead
    // data that could be deleted with nothing going red.
    const produced = new Set<string>();
    for (const input of terminalKindStressInputs()) {
      const signal = classifyTerminalKindForSignal(input);
      if (signal === undefined || signal === classifyTerminalKind(input)) continue;
      produced.add(extensionKindFor(input));
    }
    expect(
      [...produced].sort(),
      "the divergences this side actually produces must be exactly the declared extensions"
    ).toEqual([...new Set(table.signal_extensions.map((e) => e.kind))].sort());
  });

  it("matches Go's own signal projection for every derived input", () => {
    const diffs: string[] = [];
    for (const c of golden.cases) {
      const got = classifyTerminalKindForSignal(c.input) ?? "";
      if (got !== c.signal) {
        diffs.push(`${JSON.stringify(c.input)}: extension=${got} Go=${c.signal}`);
      }
    }
    expect(
      diffs.slice(0, 10),
      `The extension's signal projection differs from Go's on ${diffs.length} of ` +
        `${golden.cases.length} derived inputs.`
    ).toEqual([]);
  });

  it("signals nothing for an empty error text", () => {
    // The caller passes `error?.message ?? ""`, so this is a real input, and a
    // kind derived from no evidence at all would be the worst possible signal.
    expect(classifyTerminalKindForSignal("")).toBeUndefined();
  });

  it("keeps the architecture-approval marker and the table in step", () => {
    // The sentinel is a human-readable constant that failureComment.ts and
    // ConcurrentPipelineManager.ts also key on, so it lives in TypeScript and is
    // referenced by the table as a lowercased literal. Nothing else connects the
    // two — this does.
    const rule = table.rules.find((r) => r.id === "architecture-approval-required");
    expect(rule, "the architecture-approval rule is gone from the table").toBeDefined();
    expect(
      rule?.clauses.some((clause) =>
        clause.includes(ARCHITECTURE_APPROVAL_REQUIRED_MARKER.toLowerCase())
      ),
      `ARCHITECTURE_APPROVAL_REQUIRED_MARKER is "${ARCHITECTURE_APPROVAL_REQUIRED_MARKER}" but no ` +
        `clause of the architecture-approval rule matches its lowercased form. Changing the marker ` +
        `without changing internal/terminalkind/table.json makes a deliberate human gate look ` +
        `like a subagent crash.`
    ).toBe(true);
    expect(
      classifyTerminalKindForSignal(`Run halted: ${ARCHITECTURE_APPROVAL_REQUIRED_MARKER}`)
    ).toBe("architecture_approval_required");
  });

  it("is a pure delegation — no matching of its own", () => {
    // The corpus and the derived stress set are both built FROM the table's
    // vocabulary, so neither can see a rule invented HERE for a marker the table
    // has never heard of. Round 2's mutant was exactly that: inserting
    // `if (/\[baseline-ci-deferred\]/i.test(errorText)) return "stall_kill";`
    // at the top of this ladder left every suite green while Go recorded
    // subagent_crash — a record-vs-reaction split on the side Go trusts
    // verbatim. There is no ladder to insert into any more, and this asserts it
    // stays that way.
    const src = readFileSync(
      path.resolve(__dirname, "../../src/services/terminalKindSignal.ts"),
      "utf-8"
    )
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/\/\/[^\n]*/g, "");
    expect(src).toContain("signalTerminalKind(errorText)");
    expect(src.match(/\.test\(|\.includes\(|\.match\(/g) ?? []).toEqual([]);
    // The only string literal permitted is the SDK import specifier.
    expect(src.match(/"[^"]*"|'[^']*'|`[^`]*`/g) ?? []).toEqual(['"@nightgauge/sdk"']);
    // And the only thing imported is the classifier itself. A second binding
    // would be a marker declared in a module nothing here scans and merely
    // NAMED in this one — the file/package fence's blind spot, closed on the
    // SDK side by terminalKind.corpusParity's import assertion and here by this.
    expect(
      (src.match(/^[^\S\n]*import\b.*$/gm) ?? []).map((l) => l.trim()),
      "terminalKindSignal.ts imports something other than the guarded classifier"
    ).toEqual(['import { signalTerminalKind } from "@nightgauge/sdk";']);
  });

  it("is the only producer of the kind services.ts sends to Go, and sends it unconditionally", () => {
    // ROUND 3's FINDING 5. The guard above fences terminalKindSignal.ts, but the
    // value that actually crosses the IPC boundary is assembled in
    // bootstrap/services.ts and handed to IpcClient.autonomousComplete, which Go's
    // NotifyComplete uses VERBATIM when non-empty. Nothing asserted anything
    // about that line: changing it to
    //
    //   let terminalFailureKind = classifyTerminalKindForSignal(errMsg);
    //   if (/\[baseline-ci-deferred\]/i.test(errMsg)) terminalFailureKind = "stall_kill";
    //
    // left the Go suite, this suite and the halt-policy suite green while the
    // record said subagent_crash and the fleet reacted stall_kill.
    //
    // So the call site is asserted to be a BARE delegation: declared const (not
    // let), assigned once from the guarded function, and never touched again
    // except as the argument it is forwarded as.
    const src = readFileSync(path.resolve(__dirname, "../../src/bootstrap/services.ts"), "utf-8")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/\/\/[^\n]*/g, "");

    const declarations = src.match(/(?:const|let|var)\s+terminalFailureKind\b[^;\n]*/g) ?? [];
    expect(
      declarations,
      "the terminal kind forwarded to Go must be declared exactly once, as a const, straight " +
        "from the guarded classifier — a `let` is the first half of a reassignment"
    ).toEqual(["const terminalFailureKind = classifyTerminalKindForSignal(errMsg)"]);

    const assignments = src.match(/terminalFailureKind\s*(?:=[^=]|\+=|\?\?=|\|\|=|&&=)/g) ?? [];
    expect(
      assignments.length,
      "terminalFailureKind is assigned more than once in services.ts. Every extra assignment is " +
        "a matcher outside the table, on the value Go trusts verbatim."
    ).toBe(1);

    const calls = src.match(/classifyTerminalKindForSignal\s*\(/g) ?? [];
    expect(
      calls.length,
      "classifyTerminalKindForSignal must be called exactly once in services.ts"
    ).toBe(1);

    // And the argument really is what reaches the wire.
    expect(src).toMatch(/isConflictRestart,\s*terminalFailureKind,\s*failureDetail/);
  });

  it("covers the environmental kinds it exists to catch — no more, no less", () => {
    // Exact set equality, both directions. The declared subset is in the table;
    // the corpus has to exercise all of it and nothing beyond it.
    const declared = new Set([
      ...table.rules.filter((r) => r.signal).map((r) => r.kind),
      ...table.signal_extensions.map((e) => e.kind),
    ]);
    const observed = new Set(
      corpus.cases.map((c) => classifyTerminalKindForSignal(c.input)).filter(Boolean)
    );
    expect(
      [...observed].sort(),
      "the kinds the corpus makes this side signal must be exactly the declared subset"
    ).toEqual([...declared].sort());
  });
});
