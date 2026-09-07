import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { StagePhaseSchema } from "../../../src/schemas/pipelineState";
import { PhaseTreeItem, type PhaseStatus } from "../../../src/views/items/PhaseTreeItem";

/**
 * A phase status is declared in four places — Go's PhaseRecord, the zod
 * schema, the service's StagePhase type, and the tree's PhaseStatus — and
 * nothing made them agree.
 *
 * `abandoned` is what that cost. Go has written it since #1009; the zod enum
 * did not accept it, so a state.json carrying one failed to parse; and the
 * tree normalises an unknown status to `unreported`, so the display then said
 * "nothing was ever reported about this phase" about a phase that
 * demonstrably started. It is the status that identifies a stuck stage, so the
 * drift did not just lose a label, it lost the finding (#1558).
 */
const TS_PHASE_STATUSES: PhaseStatus[] = [
  "pending",
  "running",
  "complete",
  "skipped",
  "unreported",
  "failed",
  "abandoned",
];

/**
 * `pending` is the tree's own resting state for a row nothing has reported
 * yet. Go has no reason to persist it, so it is the one value the two sides
 * are allowed to differ on.
 */
const TREE_ONLY = new Set<string>(["pending"]);

function goPhaseStatuses(): string[] {
  const goSource = readFileSync(
    join(__dirname, "..", "..", "..", "..", "..", "internal", "state", "runtime_state.go"),
    "utf8"
  );
  // The vocabulary is the comment on PhaseRecord.Status.
  const line = goSource
    .split("\n")
    .find((l) => l.includes("Status") && l.includes('json:"status"') && l.includes("//"));
  expect(line, "PhaseRecord.Status vocabulary comment not found in runtime_state.go").toBeDefined();
  return [...line!.matchAll(/"([a-z]+)"/g)].map((m) => m[1]).filter((v) => v !== "status");
}

describe("phase status vocabulary parity (#1558)", () => {
  it("the zod schema accepts every status the tree can render", () => {
    for (const status of TS_PHASE_STATUSES) {
      const parsed = StagePhaseSchema.safeParse({ name: "some-phase", status });
      expect(parsed.success, `StagePhaseSchema rejected "${status}"`).toBe(true);
    }
  });

  it("the tree renders every status Go can persist", () => {
    for (const status of goPhaseStatuses()) {
      const item = new PhaseTreeItem("some-phase", status as PhaseStatus);
      // normalizePhaseStatus falls back to `unreported` for anything it does
      // not know, so a status surviving unchanged is the assertion.
      expect(item.getStatus(), `the tree does not know Go's "${status}"`).toBe(status);
    }
  });

  it("the zod schema accepts every status Go can persist", () => {
    for (const status of goPhaseStatuses()) {
      const parsed = StagePhaseSchema.safeParse({ name: "some-phase", status });
      expect(parsed.success, `StagePhaseSchema rejected Go's "${status}"`).toBe(true);
    }
  });

  it("neither side has a value the other has never heard of", () => {
    const go = new Set(goPhaseStatuses());
    const ts = new Set<string>(TS_PHASE_STATUSES);
    expect([...ts].filter((v) => !go.has(v) && !TREE_ONLY.has(v))).toEqual([]);
    expect([...go].filter((v) => !ts.has(v))).toEqual([]);
  });
});
