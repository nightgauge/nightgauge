/**
 * prStagePhaseStream.test.ts — the extension's half of #1397.
 *
 * The Go side is pinned by internal/orchestrator/phase_recorder_test.go,
 * including a cross-language guard on the sentinel prefix. This suite covers
 * what only the reader can get wrong: chunk boundaries, malformed lines, and
 * telling phase events apart from ordinary log output.
 */
import { describe, it, expect, vi } from "vitest";
import {
  PHASE_STREAM_PREFIX,
  parsePhaseStreamLine,
  createLineSplitter,
  type PrStagePhaseTransition,
} from "../../src/services/prStagePhaseStream";

const line = (t: Partial<PrStagePhaseTransition>): string =>
  PHASE_STREAM_PREFIX +
  JSON.stringify({
    stage: "pr-merge",
    name: "ci-gate",
    index: 3,
    total: 14,
    status: "running",
    ...t,
  });

describe("parsePhaseStreamLine", () => {
  it("parses a well-formed transition", () => {
    expect(parsePhaseStreamLine(line({}))).toEqual({
      stage: "pr-merge",
      name: "ci-gate",
      index: 3,
      total: 14,
      status: "running",
    });
  });

  it("ignores ordinary log output", () => {
    // The CLI's stderr carries real logging too; treating any of it as a phase
    // event would put junk rows in the tree.
    expect(parsePhaseStreamLine("2026/09/04 05:00:00 gh: waiting for checks")).toBeUndefined();
    expect(parsePhaseStreamLine("")).toBeUndefined();
  });

  it("returns undefined rather than throwing on malformed input", () => {
    // This runs on a progress channel. A bad line must never take down the
    // merge that was reporting progress.
    expect(parsePhaseStreamLine(`${PHASE_STREAM_PREFIX}{not json`)).toBeUndefined();
    expect(parsePhaseStreamLine(`${PHASE_STREAM_PREFIX}"a string"`)).toBeUndefined();
    expect(parsePhaseStreamLine(`${PHASE_STREAM_PREFIX}null`)).toBeUndefined();
  });

  it("rejects a transition missing its registry position", () => {
    // index/total are what the tree renders "n/14" from; a row without them
    // would silently report the wrong position.
    const bad =
      PHASE_STREAM_PREFIX +
      JSON.stringify({ stage: "pr-merge", name: "ci-gate", status: "running" });
    expect(parsePhaseStreamLine(bad)).toBeUndefined();
  });
});

describe("createLineSplitter", () => {
  it("reassembles a transition split across chunk boundaries", () => {
    // The real failure this prevents: a chunk boundary falls mid-line, the
    // transition is dropped, and the count stalls — intermittently, and
    // looking exactly like the bug #1397 fixes.
    const seen: string[] = [];
    const splitter = createLineSplitter((l) => seen.push(l));

    const whole = line({ name: "merge", status: "complete" });
    splitter.push(whole.slice(0, 20));
    expect(seen, "emitted a line before it was complete").toEqual([]);
    splitter.push(whole.slice(20) + "\n");

    expect(seen).toEqual([whole]);
    expect(parsePhaseStreamLine(seen[0])?.name).toBe("merge");
  });

  it("emits multiple lines arriving in one chunk", () => {
    const seen: string[] = [];
    const splitter = createLineSplitter((l) => seen.push(l));
    splitter.push(`${line({ name: "a" })}\n${line({ name: "b" })}\n`);
    expect(seen.map((l) => parsePhaseStreamLine(l)?.name)).toEqual(["a", "b"]);
  });

  it("flushes a trailing line with no newline", () => {
    // A process that exits without a final newline must not lose its last
    // transition — which would be the terminal one.
    const seen: string[] = [];
    const splitter = createLineSplitter((l) => seen.push(l));
    splitter.push(line({ name: "merge", status: "complete" }));
    expect(seen).toEqual([]);
    splitter.flush();
    expect(parsePhaseStreamLine(seen[0])?.status).toBe("complete");
  });

  it("flushes only once", () => {
    const onLine = vi.fn();
    const splitter = createLineSplitter(onLine);
    splitter.push("partial");
    splitter.flush();
    splitter.flush();
    expect(onLine).toHaveBeenCalledTimes(1);
  });
});
