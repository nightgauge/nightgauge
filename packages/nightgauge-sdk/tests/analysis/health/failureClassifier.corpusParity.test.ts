/**
 * Terminal-kind classification parity — SDK side (#306).
 *
 * Terminal-kind classification exists three times (Go's `ClassifyTerminalKind`,
 * this SDK mirror, and the extension's `classifyTerminalKindForSignal`), and
 * each copy decides how the fleet reacts to a failure: failure weighting,
 * cascade feeding, lifetime caps, board reverts, backoff length. Before this
 * suite they were held aligned by comments, so a pattern added to one ladder
 * silently diverged the others while both sides stayed individually green.
 *
 * This suite pins THIS side against the shared corpus in
 * `internal/orchestrator/testdata/terminal-kind/corpus.json` — the same file
 * read by `internal/orchestrator/terminal_kind_corpus_parity_test.go` and
 * `packages/nightgauge-vscode/tests/services/terminalKindSignal.corpusParity.test.ts`.
 *
 * The corpus holds GO's answer, because Go writes the run record: a
 * disagreement is by definition this side being wrong about what the pipeline
 * recorded. Rows carrying `known_divergence.sdk` are the deliberate exception —
 * they pin a disagreement that is a taxonomy decision rather than a bug.
 *
 * Sibling of `failureClassifier.parity.test.ts` (#229), which diffs the kind
 * VOCABULARY against Go. Names were in sync while behaviour had drifted; this
 * suite covers behaviour.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  classifyTerminalKind,
  resolveTerminalKind,
  ALL_TERMINAL_FAILURE_KINDS,
  type TerminalFailureKind,
} from "../../../src/analysis/health/failureClassifier.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "../../../../..");
const CORPUS_PATH = path.join(
  REPO_ROOT,
  "internal/orchestrator/testdata/terminal-kind/corpus.json"
);

interface KnownDivergence {
  sdk?: string;
  signal?: string;
  why: string;
  tracked: string;
}

interface CorpusCase {
  id: string;
  input: string;
  expected: string;
  source: "captured" | "synthetic";
  producer?: string;
  rationale: string;
  known_divergence?: KnownDivergence;
}

interface TerminalKindCorpus {
  no_matcher_kinds: string[];
  cases: CorpusCase[];
}

const corpus: TerminalKindCorpus = JSON.parse(readFileSync(CORPUS_PATH, "utf-8"));

/** The corpus encodes an unmatched input as `""`; this classifier returns undefined. */
const expectedFor = (c: CorpusCase): string => c.known_divergence?.sdk ?? c.expected;

describe("terminal-kind corpus parity (SDK)", () => {
  it("classifies every corpus input to the kind the corpus pins", () => {
    const mismatches: string[] = [];

    for (const c of corpus.cases) {
      const got = classifyTerminalKind(c.input) ?? "";
      const want = expectedFor(c);
      if (got !== want) {
        mismatches.push(
          `${c.id}\n    input:    ${JSON.stringify(c.input)}\n` +
            `    got:      ${JSON.stringify(got)}\n` +
            `    expected: ${JSON.stringify(want)}\n` +
            `    why this row exists: ${c.rationale}`
        );
      }
    }

    expect(
      mismatches,
      `The SDK classifier disagrees with the shared corpus on ${mismatches.length} of ` +
        `${corpus.cases.length} inputs. The corpus holds Go's answer because Go writes the run ` +
        `record, so each mismatch is a run the record and the published SDK describe ` +
        `differently:\n\n${mismatches.join("\n\n")}\n`
    ).toEqual([]);
  });

  it("records each declared divergence as an actual, current disagreement", () => {
    // A recorded divergence that has quietly been resolved is worse than none:
    // it documents a gap that no longer exists and hides the next real one.
    for (const c of corpus.cases) {
      const sdk = c.known_divergence?.sdk;
      if (!sdk) continue;
      expect(
        classifyTerminalKind(c.input) ?? "",
        `${c.id} declares an SDK divergence to "${sdk}" (${c.known_divergence?.tracked}); ` +
          `if the classifier no longer produces it, delete the divergence from the corpus.`
      ).toBe(sdk);
    }
  });

  it("prefers a gate-sourced kind over prose classification", () => {
    for (const c of corpus.cases) {
      expect(resolveTerminalKind(false, undefined, c.input) ?? "").toBe(expectedFor(c));
      expect(resolveTerminalKind(true, "abandoned_commit", c.input)).toBe("abandoned_commit");
    }
  });

  it("covers every terminal kind that is derived from error text", () => {
    const exempt = new Set(corpus.no_matcher_kinds);
    const covered = new Set(corpus.cases.map((c) => c.expected).filter(Boolean));

    const missing = ALL_TERMINAL_FAILURE_KINDS.filter(
      (kind) => !covered.has(kind) && !exempt.has(kind)
    );

    expect(
      missing,
      "Terminal kinds with no corpus row. Add a row (with a rationale) to " +
        "internal/orchestrator/testdata/terminal-kind/corpus.json, or list the kind in " +
        "no_matcher_kinds if it is set structurally and never derived from error text."
    ).toEqual([]);
  });

  it("expects only kinds this classifier can actually return", () => {
    const known = new Set<string>(ALL_TERMINAL_FAILURE_KINDS as readonly string[]);
    for (const c of corpus.cases) {
      const want = expectedFor(c);
      if (want === "") continue;
      expect(known.has(want as TerminalFailureKind), `${c.id} expects unknown kind "${want}"`).toBe(
        true
      );
    }
  });
});
