/**
 * terminalKindSignal.corpusParity.test.ts
 *
 * Terminal-kind classification parity — extension SIGNAL side (#306).
 *
 * `classifyTerminalKindForSignal` is the third terminal-kind classifier, and
 * the one with the most authority per line: its answer is sent to Go with
 * `autonomousComplete`, and Go's NotifyComplete only re-classifies when it
 * received an EMPTY kind. A non-empty answer here is used VERBATIM — so a
 * wrong answer overrides the authoritative classifier for the fleet's
 * reaction, while the run RECORD is still written from Go's own classification
 * of the same failure.
 *
 * This suite pins that ladder against the same corpus that pins Go and the SDK
 * (`internal/orchestrator/testdata/terminal-kind/corpus.json`):
 *
 *   - Returning `undefined` is always legal. It is safe by design: the caller
 *     forwards the raw failure text as `failureDetail` unconditionally (#3442)
 *     and Go re-classifies from it. A miss costs nothing.
 *   - Returning a KIND is only legal when it is the kind the corpus pins —
 *     unless the corpus records that disagreement explicitly, in which case
 *     the ladder must produce exactly the recorded kind. A divergence that is
 *     written down is a known gap; one that is not is drift.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { classifyTerminalKindForSignal } from "../../src/services/terminalKindSignal";

const REPO_ROOT = path.resolve(__dirname, "../../../..");
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

describe("terminal-kind corpus parity (extension signal ladder)", () => {
  it("never signals a kind that disagrees with the authoritative classifier", () => {
    const conflicts: string[] = [];

    for (const c of corpus.cases) {
      const got = classifyTerminalKindForSignal(c.input);
      if (got === undefined) continue; // a miss defers to Go — always safe

      const allowed = c.known_divergence?.signal ?? c.expected;
      if (got !== allowed) {
        conflicts.push(
          `${c.id}\n    input:    ${JSON.stringify(c.input)}\n` +
            `    signalled: ${JSON.stringify(got)}\n` +
            `    Go records: ${JSON.stringify(c.expected)}\n` +
            `    why this row exists: ${c.rationale}`
        );
      }
    }

    expect(
      conflicts,
      `The extension signal ladder would tell the Go scheduler a different kind than the one ` +
        `Go writes into the run record, for ${conflicts.length} input(s). Because a non-empty ` +
        `signal is used VERBATIM, each of these is a run whose record and whose fleet reaction ` +
        `disagree. Either fix the ladder, or — if the disagreement is a deliberate taxonomy ` +
        `decision — record it in the corpus as known_divergence.signal with a tracking ` +
        `reference:\n\n${conflicts.join("\n\n")}\n`
    ).toEqual([]);
  });

  it("still produces every divergence the corpus records", () => {
    // A recorded divergence that has quietly been resolved is worse than none:
    // it documents a gap that no longer exists and hides the next real one.
    for (const c of corpus.cases) {
      const signal = c.known_divergence?.signal;
      if (!signal) continue;
      expect(
        classifyTerminalKindForSignal(c.input),
        `${c.id} records a signal divergence to "${signal}" (${c.known_divergence?.tracked}). ` +
          `If the ladder no longer produces it, delete the divergence from the corpus.`
      ).toBe(signal);
    }
  });

  it("signals nothing for an empty error text", () => {
    // The caller passes `error?.message ?? ""`, so this is a real input, and a
    // kind derived from no evidence at all would be the worst possible signal.
    expect(classifyTerminalKindForSignal("")).toBeUndefined();
  });

  it("covers the environmental kinds it exists to catch", () => {
    // The ladder is deliberately partial — it catches what the TS layer sees
    // first, not the whole taxonomy. This pins that intent so a future
    // reader neither expands it into a second full classifier by accident nor
    // erodes it to nothing.
    const signalled = new Set(
      corpus.cases.map((c) => classifyTerminalKindForSignal(c.input)).filter(Boolean)
    );

    for (const kind of [
      "stream_idle_timeout",
      "github_quota_low",
      "rate_limit_quota_exhausted",
      "api_overloaded",
      "github_network_outage",
      "api_connection_lost",
      "adapter_auth_failed",
      "architecture_approval_required",
    ]) {
      expect(signalled, `the corpus no longer exercises the ${kind} signal rule`).toContain(kind);
    }
  });
});
