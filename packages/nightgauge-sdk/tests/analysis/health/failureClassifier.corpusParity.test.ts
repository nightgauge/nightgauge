/**
 * failureClassifier.corpusParity.test.ts
 *
 * Terminal-kind classification — SDK side (#306).
 *
 * There is nothing here to keep "aligned" with Go any more: both languages
 * interpret ONE ordered rule table (internal/terminalkind/table.json, generated
 * into terminalKindTable.generated.ts with a byte-equality drift check). What
 * this suite proves is that the interpretation is the same, on three axes:
 *
 *   1. BEHAVIOUR — the shared corpus, whose `expected` is Go's answer because
 *      Go writes the run record.
 *   2. EQUIVALENCE — the derived stress set. Both languages derive the same
 *      inputs from the table (every clause, every term, every ordered rule
 *      pair) and must reproduce the golden Go generated from it.
 *   3. PREDICATES — the one term kind that is code rather than a literal, held
 *      to the probes the table declares. Go asserts the same probes.
 *
 * Complements failureClassifier.parity.test.ts (#229), which diffs the kind
 * VOCABULARY. Names were in sync while behaviour had drifted 19 ways; this
 * covers behaviour.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import {
  classifyTerminalKind,
  signalTerminalKind,
  terminalKindStressInputs,
} from "../../../src/analysis/health/failureClassifier.js";
import { TERMINAL_KIND_TABLE } from "../../../src/analysis/health/terminalKindTable.generated.js";

const REPO_ROOT = path.resolve(__dirname, "../../../../..");
const CORPUS_PATH = path.join(REPO_ROOT, "internal/terminalkind/testdata/corpus.json");
const GOLDEN_PATH = path.join(REPO_ROOT, "internal/terminalkind/testdata/stress-golden.json");

interface CorpusCase {
  id: string;
  input: string;
  expected: string;
  expected_signal: string;
  source: "captured" | "synthetic";
  producer?: string;
  rationale: string;
}

const corpus: { cases: CorpusCase[] } = JSON.parse(readFileSync(CORPUS_PATH, "utf-8"));
const golden: { cases: { input: string; kind: string; signal: string }[] } = JSON.parse(
  readFileSync(GOLDEN_PATH, "utf-8")
);

describe("terminal-kind corpus parity (SDK)", () => {
  it("classifies every corpus input the way the run record will", () => {
    const mismatches: string[] = [];

    for (const c of corpus.cases) {
      const got = classifyTerminalKind(c.input) ?? "";
      if (got !== c.expected) {
        mismatches.push(
          `${c.id}\n    input:    ${JSON.stringify(c.input)}\n` +
            `    got:      ${JSON.stringify(got)}\n` +
            `    expected: ${JSON.stringify(c.expected)}\n` +
            `    why this row exists: ${c.rationale}`
        );
      }
    }

    expect(
      mismatches,
      `The SDK classifier disagrees with the shared corpus on ${mismatches.length} of ` +
        `${corpus.cases.length} inputs. Both sides interpret the same table, so a mismatch here ` +
        `is an interpreter bug, not drift — check clause evaluation and rule order before ` +
        `touching the fixture:\n\n${mismatches.join("\n\n")}\n`
    ).toEqual([]);
  });

  it("projects the signal subset exactly as the corpus pins it", () => {
    // Both bounds. A non-empty signal must equal the recorded kind (it is the
    // same winning rule), and a rule declared `signal: true` must produce one.
    const mismatches: string[] = [];
    for (const c of corpus.cases) {
      const got = signalTerminalKind(c.input) ?? "";
      if (got !== c.expected_signal) {
        mismatches.push(
          `${c.id}: signal ${JSON.stringify(got)}, corpus pins ${JSON.stringify(c.expected_signal)}`
        );
      }
      if (got !== "" && got !== c.expected) {
        mismatches.push(`${c.id}: signal ${got} contradicts the record ${c.expected}`);
      }
    }
    expect(mismatches).toEqual([]);
  });
});

describe("interpreter equivalence (SDK vs Go, over the derived stress set)", () => {
  const inputs = terminalKindStressInputs();

  it("derives exactly the inputs Go derived", () => {
    // If this fails the two derivations have diverged, which would quietly
    // shrink what the equivalence check below covers.
    expect(inputs.length).toBe(golden.cases.length);
    const mismatched = inputs.filter((s, i) => s !== golden.cases[i].input).slice(0, 5);
    expect(
      mismatched,
      "terminalKindStressInputs no longer matches StressInputs in internal/terminalkind/stress.go"
    ).toEqual([]);
  });

  it("answers exactly what Go answered, for every derived input", () => {
    const diffs: string[] = [];
    for (const c of golden.cases) {
      const kind = classifyTerminalKind(c.input) ?? "";
      const signal = signalTerminalKind(c.input) ?? "";
      if (kind !== c.kind || signal !== c.signal) {
        diffs.push(
          `${JSON.stringify(c.input)}\n    SDK: kind=${JSON.stringify(kind)} ` +
            `signal=${JSON.stringify(signal)}\n    Go:  kind=${JSON.stringify(c.kind)} ` +
            `signal=${JSON.stringify(c.signal)}`
        );
      }
    }
    expect(
      diffs.slice(0, 10),
      `The two interpreters disagree on ${diffs.length} of ${golden.cases.length} derived inputs. ` +
        `The stress set covers every clause, every term and every ordered rule pair, so any ` +
        `divergence in clause evaluation or precedence lands here.`
    ).toEqual([]);
  });

  it("never signals a kind that contradicts the record", () => {
    for (const c of golden.cases) {
      const signal = signalTerminalKind(c.input);
      if (signal !== undefined) {
        expect(signal, `signal for ${JSON.stringify(c.input)}`).toBe(classifyTerminalKind(c.input));
      }
    }
  });
});

describe("named predicates", () => {
  // The table's one predicate is the only part of a rule that is code rather
  // than a literal, so it is the only remaining way the two languages could
  // answer differently. Go asserts these same probes in TestPredicateProbes.
  it("accepts every declared true-probe and rejects every false-probe", () => {
    for (const p of TERMINAL_KIND_TABLE.predicates) {
      expect(p.probes_true.length, `${p.name} declares no true-probes`).toBeGreaterThan(0);
      expect(p.probes_false.length, `${p.name} declares no false-probes`).toBeGreaterThan(0);

      for (const probe of p.probes_true) {
        // Exercised through the classifier rather than by reaching into the
        // predicate: what matters is the rule it gates.
        expect(
          classifyTerminalKind(`usage limit reached for ${probe}`),
          `${p.name} should accept ${JSON.stringify(probe)}`
        ).toBe("model_unavailable");
      }
      for (const probe of p.probes_false) {
        expect(
          classifyTerminalKind(`usage limit reached ${probe}`),
          `${p.name} should reject ${JSON.stringify(probe)}`
        ).not.toBe("model_unavailable");
      }
    }
  });
});

describe("the interpreter itself", () => {
  // THE LAST HOLE, closed structurally. The corpus and the derived stress set
  // are both built FROM the table's vocabulary, so neither can see a rule
  // invented HERE for a marker the table has never heard of — insert
  // `if (t.includes("[baseline-ci-deferred]")) return …` at the top of
  // matchTerminalKindRule and every behavioural assertion above stays green.
  // (That is round 2's finding 1/8 in its new home; the marker is not invented
  // either, it is live in classifyFailureCategory.)
  //
  // So the matcher is asserted to contain NO string literal and NO regex at
  // all: every marker it compares against must come out of the generated
  // table. That is checkable at a glance on twelve lines, which is exactly why
  // the ladder had to become data first — the same assertion over a 33-block
  // hand-written ladder would be meaningless.
  const SOURCE_PATH = path.join(
    REPO_ROOT,
    "packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts"
  );
  const source = readFileSync(SOURCE_PATH, "utf-8");

  function matcherSource(): string {
    const start = source.indexOf("function matchTerminalKindRule");
    const end = source.indexOf("const TERMINAL_KIND_PREDICATES");
    expect(start, "matchTerminalKindRule is gone — this guard now checks nothing").toBeGreaterThan(
      0
    );
    expect(end, "TERMINAL_KIND_PREDICATES is gone — this guard now checks nothing").toBeGreaterThan(
      start
    );
    // Strip comments; prose is allowed to contain anything.
    return source
      .slice(start, end)
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/\/\/[^\n]*/g, "");
  }

  it("contains no string literal and no regex — every marker comes from the table", () => {
    const body = matcherSource();
    const literals = body.match(/"[^"]*"|'[^']*'|`[^`]*`/g) ?? [];
    expect(
      literals,
      "a string literal appeared inside the terminal-kind matcher. Markers belong in " +
        "internal/terminalkind/table.json, where Go reads them too; a literal here is a rule " +
        "only this language has, and neither the corpus nor the derived stress set can see it."
    ).toEqual([]);
    const regexes = body.match(/[=(,[\s]\/(?![/*])(?:\\.|\[[^\]]*\]|[^/\n])+\/[gimsuy]*/g) ?? [];
    expect(regexes, "a regex appeared inside the terminal-kind matcher").toEqual([]);
  });

  it("implements exactly the predicates the table declares — no more", () => {
    // An extra predicate would be a second place to put behaviour that the
    // table does not describe.
    const declared = TERMINAL_KIND_TABLE.predicates.map((p) => p.name).sort();
    const block = source.slice(
      source.indexOf("const TERMINAL_KIND_PREDICATES"),
      source.indexOf("Classify the *kind* of terminal failure")
    );
    const implemented = [...block.matchAll(/^\s{2}(\w+):/gm)].map((m) => m[1]).sort();
    expect(implemented).toEqual(declared);
  });
});

describe("the table itself", () => {
  it("references no predicate this language cannot evaluate", () => {
    // A missing implementation must throw rather than silently evaluate false,
    // which would disable a rule with no visible symptom.
    for (const rule of TERMINAL_KIND_TABLE.rules) {
      for (const clause of rule.clauses) {
        for (const term of clause) {
          if (!term.startsWith("@")) continue;
          const name = term.slice(1);
          expect(
            TERMINAL_KIND_TABLE.predicates.some((p) => p.name === name),
            `rule ${rule.id} references undeclared predicate ${name}`
          ).toBe(true);
          expect(() => classifyTerminalKind("probe")).not.toThrow();
        }
      }
    }
  });
});
