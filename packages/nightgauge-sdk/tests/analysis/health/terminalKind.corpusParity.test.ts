/**
 * terminalKind.corpusParity.test.ts
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
 *      inputs from the table (every clause, every term, both edges of every `~`
 *      term, every ordered rule pair, and every `signal: true` rule composed
 *      with every extension clause) and must reproduce the golden Go generated
 *      from it. The golden is Go's answer and this package cannot regenerate
 *      it, so a TypeScript-only change to clause evaluation or to the order of
 *      the projection's two stages lands here as a diff.
 *   3. PREDICATES — the one term kind that is code rather than a literal, held
 *      to the probes the table declares. Go asserts the same probes.
 *
 * Complements failureClassifier.parity.test.ts (#229), which diffs the kind
 * VOCABULARY. Names were in sync while behaviour had drifted 19 ways; this
 * covers behaviour.
 */

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import {
  classifyTerminalKind,
  signalTerminalKind,
  terminalKindStressInputs,
} from "../../../src/analysis/health/terminalKind.js";
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

/**
 * The kind a DECLARED signal extension gives this input, or "" — the only way
 * the reaction may differ from the record. Evaluated the way the interpreter
 * does: extensions are consulted only when the rule table projects no signal.
 */
function extensionKindFor(input: string): string {
  const t = input.toLowerCase();
  for (const extension of TERMINAL_KIND_TABLE.signal_extensions) {
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
    // Both bounds. A rule declared `signal: true` must produce a signal, and a
    // non-empty signal must equal the recorded kind — UNLESS a declared
    // signal_extension produced it, which is the one legal divergence and is
    // itself data the corpus pins per row.
    const mismatches: string[] = [];
    for (const c of corpus.cases) {
      const got = signalTerminalKind(c.input) ?? "";
      if (got !== c.expected_signal) {
        mismatches.push(
          `${c.id}: signal ${JSON.stringify(got)}, corpus pins ${JSON.stringify(c.expected_signal)}`
        );
      }
      if (got !== "" && got !== c.expected && extensionKindFor(c.input) !== got) {
        mismatches.push(
          `${c.id}: signal ${got} contradicts the record ${c.expected} and no declared ` +
            `signal extension produces it`
        );
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
        `The stress set covers every clause, every term, both edges of every \`~\` term, every ` +
        `ordered rule pair and every signal rule composed with every extension clause, so a ` +
        `divergence in clause evaluation, in the word boundary or in the order of the ` +
        `projection's two stages lands here.`
    ).toEqual([]);
  });

  it("never signals a kind that contradicts the record, outside declared extensions", () => {
    const conflicts: string[] = [];
    for (const c of golden.cases) {
      const signal = signalTerminalKind(c.input);
      if (signal === undefined) continue;
      if (signal === classifyTerminalKind(c.input)) continue;
      if (extensionKindFor(c.input) === signal) continue;
      conflicts.push(`${JSON.stringify(c.input)}: signal=${signal}`);
    }
    expect(conflicts.slice(0, 10)).toEqual([]);
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

describe("the interpreter module", () => {
  // THE GUARD, and the reason terminalKind.ts is a module rather than a region.
  //
  // The corpus and the derived stress set are both built FROM the table's
  // vocabulary, so neither can see a rule invented HERE for a marker the table
  // has never heard of. Round 3 scanned a WINDOW of failureClassifier.ts — from
  // `function matchTerminalKindRule` to `const TERMINAL_KIND_PREDICATES` — and
  // its review walked around that window twice with every suite green: once by
  // putting the branch in `signalTerminalKind` (below the lower edge, and the
  // function whose answer Go's NotifyComplete uses VERBATIM), once by declaring
  // `const DEFERRED_MARKER = "…"` one line above the upper edge.
  //
  // So the scan is the WHOLE FILE against an exact allowlist, in both
  // directions. There is no line above the matcher to hoist to, no entry point
  // below it to move to, and `mentionsRegistryModel` — round 3's finding 6, a
  // one-line insertion that made six clauses fire for an undeclared marker — is
  // inside the same file and therefore inside the same assertion.
  const SOURCE_PATH = path.join(
    REPO_ROOT,
    "packages/nightgauge-sdk/src/analysis/health/terminalKind.ts"
  );
  const source = readFileSync(SOURCE_PATH, "utf-8");
  const code = source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/[^\n]*/g, "");

  /**
   * Every string literal terminalKind.ts is allowed to contain. Not one is a
   * marker: three import specifiers, two diagnostic messages assembled at their
   * throw sites, and the five derived-input strings that mirror stress.go.
   */
  const ALLOWED_LITERALS = [
    '"../../eval/modelRegistry.js"',
    '"./terminalKindTable.generated.js"',
    '"./failureClassifier.js"',
    '"terminal-kind table references a predicate with no TypeScript implementation in terminalKind.ts: "',
    '"terminal-kind predicate declares no probes_true: "',
    '""',
    '"nothing in this sentence resembles a terminal marker"',
    '"exit 1: "',
    '" | "',
    '" "',
    '"s"',
  ];

  it("contains no string literal outside the declared allowlist", () => {
    const literals = code.match(/"[^"]*"|'[^']*'|`[^`]*`/g) ?? [];
    const undeclared = literals.filter((l) => !ALLOWED_LITERALS.includes(l));
    expect(
      undeclared,
      "an undeclared string literal appeared in terminalKind.ts. Markers belong in " +
        "internal/terminalkind/table.json, where Go reads them too; a literal here is a rule " +
        "only this language has, and neither the corpus nor the derived stress set can see it. " +
        "Declaring it above the matcher does not help — this scan is the whole file."
    ).toEqual([]);

    const missing = ALLOWED_LITERALS.filter((l) => !literals.includes(l));
    expect(
      missing,
      "the allowlist declares literals terminalKind.ts no longer contains. Delete them — a " +
        "stale allowlist is a hole waiting for the string to come back somewhere else."
    ).toEqual([]);
  });

  it("contains no regex at all", () => {
    const regexes = code.match(/[=(,[\s]\/(?![/*])(?:\\.|\[[^\]]*\]|[^/\n])+\/[gimsuy]*/g) ?? [];
    expect(regexes, "a regex appeared in the terminal-kind interpreter").toEqual([]);
  });

  it("implements exactly the predicates the table declares — no more", () => {
    // An extra predicate would be a second place to put behaviour the table
    // does not describe.
    const declared = TERMINAL_KIND_TABLE.predicates.map((p) => p.name).sort();
    const block = source.slice(
      source.indexOf("const TERMINAL_KIND_PREDICATES"),
      source.indexOf("Classify the *kind* of terminal failure")
    );
    const implemented = [...block.matchAll(/^\s{2}(\w+):/gm)].map((m) => m[1]).sort();
    expect(implemented).toEqual(declared);
  });

  it("value-imports exactly the declared modules and bindings", () => {
    // THE DEPENDENCY FENCE. The literal allowlist above is a FILE fence, and a
    // file fence has an outside: adding
    //
    //   export const DEFERRED_MARKER = "[baseline-ci-deferred]";
    //
    // to failureClassifier.ts — a module nothing scans, whose specifier was
    // ALREADY on the allowlist because terminalKind.ts imports a type from it —
    // and value-importing it here put a marker branch at the top of
    // `signalTerminalKind` with terminalKind.ts gaining no string literal at
    // all. Every suite stayed green while the fleet reacted a kind the run
    // record did not carry.
    //
    // So the imports themselves are an exact set, in both directions: the
    // specifier, whether it is type-only, and every binding name. Turning
    // `import type { TerminalFailureKind }` into a value import is a different
    // statement and is red; so is one extra binding from either data module.
    //
    // The one value import from an unguarded module is MODEL_REGISTRY, and what
    // it may contribute is bounded separately — terminalKind.predicateFields.
    // test.ts pins which registry FIELDS the predicate reads, from the same
    // fixture Go uses.
    const EXPECTED_IMPORTS = [
      "value ../../eval/modelRegistry.js { MODEL_REGISTRY }",
      "type ./failureClassifier.js { TerminalFailureKind }",
      "value ./terminalKindTable.generated.js { TERMINAL_KIND_PREDICATE_REF, " +
        "TERMINAL_KIND_TABLE, TERMINAL_KIND_WORD_BOUNDARY_REF, type TerminalKindRule }",
    ].sort();

    const statements = code.match(/^[^\S\n]*import\b[\s\S]*?from\s*["'][^"']+["']\s*;?/gm) ?? [];
    const importLines = code.match(/^[^\S\n]*import\b.*$/gm) ?? [];
    expect(
      statements.length,
      "an import statement in terminalKind.ts is not of the form " +
        '`import [type] { … } from "…";` — a side-effect, default or namespace import would ' +
        "slip past the parse below"
    ).toBe(importLines.length);

    const parsed = statements.map((raw) => {
      const m = /^\s*import\s+(type\s+)?\{([\s\S]*?)\}\s*from\s*["']([^"']+)["']/.exec(raw);
      expect(m, `unsupported import form in terminalKind.ts: ${raw}`).not.toBeNull();
      const [, typeOnly, body, specifier] = m as RegExpExecArray;
      const bindings = body
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean)
        .sort();
      return `${typeOnly ? "type" : "value"} ${specifier} { ${bindings.join(", ")} }`;
    });

    expect(
      parsed.sort(),
      "terminalKind.ts's imports are not the declared set. A value import from an unguarded " +
        "module is the hoist target the whole-file literal scan cannot see: the marker lives " +
        "over there and this file only names it."
    ).toEqual(EXPECTED_IMPORTS);
  });

  it("is the ONLY module that reads the generated table", () => {
    // Guarding one file is worth nothing if a second interpreter can be written
    // beside it. The generated module is the only way to reach the rules, so
    // this bounds the whole matching surface of both TypeScript packages to the
    // file the allowlist above covers.
    const roots = ["packages/nightgauge-sdk/src", "packages/nightgauge-vscode/src"];
    const readers: string[] = [];
    const walk = (dir: string): void => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name);
        if (entry.isDirectory()) {
          walk(full);
          continue;
        }
        if (!entry.name.endsWith(".ts")) continue;
        if (entry.name === "terminalKindTable.generated.ts") continue;
        if (readFileSync(full, "utf-8").includes("terminalKindTable.generated")) {
          readers.push(path.relative(REPO_ROOT, full));
        }
      }
    };
    for (const root of roots) walk(path.join(REPO_ROOT, root));
    expect(
      readers.sort(),
      "a source file other than the guarded interpreter reads the generated rule table. " +
        "Every consumer must go through classifyTerminalKind / signalTerminalKind, or the " +
        "no-literals guard fences an empty room."
    ).toEqual(["packages/nightgauge-sdk/src/analysis/health/terminalKind.ts"]);
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
