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
 *
 * It also diffs the matcher LITERALS, in order, against Go's — see the second
 * describe block. The corpus pins behaviour on the inputs it contains; the
 * literal diff pins the ladder itself, so a pattern alternative added or
 * removed on one side is red even before anyone thinks to write a row for it.
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

/**
 * The corpus pins BEHAVIOUR on the inputs it happens to contain. This block
 * pins the LADDER: every matcher literal, in order, must be the same on both
 * sides. Adding or deleting a pattern alternative introduces no new kind, so
 * the kind-coverage assertions above cannot see it — which is how a stale
 * mirror re-forms.
 *
 * Direction of authority is unchanged: Go is the source, this file is the
 * mirror. Go's own suite additionally requires every one of these literals to
 * be exercised by a corpus input
 * (`TestTerminalKindCorpus_ExercisesEveryMatcherLiteral`); with the sequences
 * proven identical here, that coverage carries over to this side for free.
 *
 * NOT covered: the extension's `classifyTerminalKindForSignal`, which is a
 * deliberately partial regex ladder rather than a mirror. Its corpus suite
 * pins it by outcome instead.
 */
const GO_CLASSIFIER_PATH = path.join(REPO_ROOT, "internal/orchestrator/failure_handler.go");
const SDK_CLASSIFIER_PATH = path.join(
  REPO_ROOT,
  "packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts"
);

const GO_LITERAL_RE = /strings\.Contains\(t,\s*"((?:[^"\\]|\\.)*)"\)/g;
const SDK_LITERAL_RE = /t\.includes\(\s*"((?:[^"\\]|\\.)*)"\s*\)/g;

/**
 * Go carries one alternative this side deliberately does not: the
 * `exitSignalSource` + `runaway-progress` conjunction. Go lowercases its input
 * before matching and that literal carries capitals, so the branch cannot fire
 * for any input — mirroring dead code would make both sides agree on nothing.
 * Pinned as a contiguous run so that deleting it from Go (the right eventual
 * fix, #305/#370) fails here and forces this exception to be deleted with it.
 */
const GO_ONLY_DEAD_BRANCH = ["exitSignalSource", "runaway-progress"];

/** Brace-balanced body of a top-level function, located by its signature. */
function functionBody(source: string, signature: string): string {
  const start = source.indexOf(signature);
  if (start < 0) {
    throw new Error(`${signature} not found — the classifier was renamed and this guard is blind`);
  }
  const open = source.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < source.length; i++) {
    if (source[i] === "{") depth++;
    else if (source[i] === "}" && --depth === 0) return source.slice(open, i + 1);
  }
  throw new Error(`${signature} body is unbalanced`);
}

function matcherLiterals(body: string, pattern: RegExp): string[] {
  return [...body.matchAll(pattern)].map((m) => m[1]);
}

describe("terminal-kind matcher parity (#306)", () => {
  const goSource = readFileSync(GO_CLASSIFIER_PATH, "utf-8");
  const sdkSource = readFileSync(SDK_CLASSIFIER_PATH, "utf-8");

  const goLiterals = matcherLiterals(
    functionBody(goSource, "func ClassifyTerminalKind(") +
      functionBody(goSource, "func isModelUnavailableText("),
    GO_LITERAL_RE
  );
  const sdkLiterals = matcherLiterals(
    functionBody(sdkSource, "export function classifyTerminalKind(") +
      functionBody(sdkSource, "function isModelUnavailableText("),
    SDK_LITERAL_RE
  );

  it("extracted a non-trivial literal ladder from both sides (sanity check on the regexes)", () => {
    expect(goLiterals.length).toBeGreaterThan(100);
    expect(sdkLiterals.length).toBeGreaterThan(100);
  });

  it("mirrors Go's matcher literals exactly, in order", () => {
    const at = goLiterals.indexOf(GO_ONLY_DEAD_BRANCH[0]);
    expect(
      at,
      `Go no longer carries the documented dead branch ${JSON.stringify(GO_ONLY_DEAD_BRANCH)}. ` +
        `If it was deleted, delete GO_ONLY_DEAD_BRANCH here too (and the corpus row ` +
        `runaway-progress-exit-signal-source-dead-branch).`
    ).toBeGreaterThanOrEqual(0);
    expect(
      goLiterals.slice(at, at + GO_ONLY_DEAD_BRANCH.length),
      "the dead branch is no longer a contiguous run in Go; re-check the exception before trusting it"
    ).toEqual(GO_ONLY_DEAD_BRANCH);

    const goWithoutDeadBranch = [
      ...goLiterals.slice(0, at),
      ...goLiterals.slice(at + GO_ONLY_DEAD_BRANCH.length),
    ];

    expect(
      goWithoutDeadBranch,
      "The SDK mirror and Go's ClassifyTerminalKind no longer match literal for literal. Order " +
        "is the contract — many real failure strings match two blocks and the earlier one wins " +
        "— so a reordered or missing alternative is a run the record and the published SDK " +
        "describe differently, even when every corpus row still passes. Mirror the change (and " +
        "add a corpus row exercising the new literal: Go's " +
        "TestTerminalKindCorpus_ExercisesEveryMatcherLiteral requires one)."
    ).toEqual(sdkLiterals);
  });
});
