/**
 * DashboardState.modelRoutingMetrics.test.ts
 *
 * #466: `getModelRoutingMetrics` and `getStageModelInfo` scanned history JSONL
 * behind an `Array.isArray(record.stages)` guard, but `stages` is an object map
 * keyed by stage name in every schema version — so neither block had ever
 * executed and the Model Routing widget was permanently empty.
 *
 * These tests read REAL JSONL from a real temp directory, in the on-disk shape
 * the Go writer produces, so they fail if the guard regresses to `Array.isArray`
 * or if the field plumbing goes back to reading `stage.stage` / `stage.tokens`
 * (neither of which exists on a stage detail).
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { createMockMemento } from "../../mocks/memento";

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn().mockReturnValue(undefined) })),
  },
  EventEmitter: class EventEmitter {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import { DashboardState } from "../../../src/views/dashboard/DashboardState";

const tempRoots: string[] = [];

afterEach(async () => {
  for (const dir of tempRoots.splice(0)) {
    await fs.rm(dir, { recursive: true, force: true });
  }
});

/**
 * A run record in the exact on-disk shape: `stages` is an object map keyed by
 * stage name, the stage detail carries NO `stage` field and NO `tokens`, and
 * per-stage token totals hang off the run record's `tokens.per_stage`.
 */
function runRecord(issueNumber: number) {
  return {
    record_type: "run",
    schema_version: "2",
    issue_number: issueNumber,
    started_at: "2026-03-01T00:00:00.000Z",
    stages: {
      "feature-planning": {
        status: "complete",
        duration_ms: 1000,
        started_at: "2026-03-01T00:00:00.000Z",
        model_selection: {
          model: "sonnet",
          source: "auto",
          mode: "automatic",
          confidence: 0.7,
          complexity: "S",
        },
      },
      "feature-dev": {
        status: "failed",
        duration_ms: 2000,
        started_at: "2026-03-01T00:30:00.000Z",
        model_selection: {
          model: "opus",
          source: "auto",
          mode: "automatic",
          confidence: 0.9,
          complexity: "L",
        },
      },
      "pr-create": {
        status: "complete",
        duration_ms: 500,
        model_selection: { model: "haiku", source: "stage-default" },
      },
    },
    tokens: {
      per_stage: {
        "feature-planning": { input: 22, output: 16748, cost_usd: 1.25 },
        "feature-dev": { input: 100, output: 200, cost_usd: 4.75 },
        "pr-create": { input: 10, output: 20, cost_usd: 0.5 },
      },
    },
  };
}

async function makeWorkspace(records: unknown[]): Promise<string> {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ng-466-"));
  tempRoots.push(root);
  const historyDir = path.join(root, ".nightgauge", "pipeline", "history");
  await fs.mkdir(historyDir, { recursive: true });
  await fs.writeFile(
    path.join(historyDir, "2026-03-01.jsonl"),
    records.map((r) => JSON.stringify(r)).join("\n") + "\n",
    "utf-8"
  );
  return root;
}

function createState(root: string): DashboardState {
  return new DashboardState(createMockMemento(new Map()), root);
}

describe("getModelRoutingMetrics (#466)", () => {
  it("extracts auto-selected stages from the object-map `stages` shape", async () => {
    const state = createState(await makeWorkspace([runRecord(42)]));

    const metrics = await state.getModelRoutingMetrics();

    // Before #466 this was null on every workspace, because the Array.isArray
    // guard meant `records` was always empty.
    expect(metrics).not.toBeNull();
    // Only the two `source: "auto"` stages count; "stage-default" is filtered.
    expect(metrics!.totalAutoSelectedRuns).toBe(2);
    expect(metrics!.modelUsage).toEqual({ sonnet: 1, opus: 1 });
  });

  it("names each stage from the map KEY — the stage detail has no `stage` field", async () => {
    const state = createState(await makeWorkspace([runRecord(42)]));

    const metrics = await state.getModelRoutingMetrics();

    const stages = metrics!.perStage.map((s) => s.stage).sort();
    expect(stages).toEqual(["feature-dev", "feature-planning"]);
    // A regression that reads `stage.stage` yields undefined here.
    expect(stages).not.toContain(undefined);
  });

  it("takes token cost from run.tokens.per_stage, not from the stage detail", async () => {
    const state = createState(await makeWorkspace([runRecord(42)]));

    const metrics = await state.getModelRoutingMetrics();

    // 1.25 (feature-planning) + 4.75 (feature-dev); pr-create is not auto.
    expect(metrics!.totalCostUsd).toBeCloseTo(6.0, 5);
  });

  it("carries per-stage success through from the detail's status", async () => {
    const state = createState(await makeWorkspace([runRecord(42)]));

    const metrics = await state.getModelRoutingMetrics();

    // one complete + one failed
    expect(metrics!.overallSuccessRate).toBeCloseTo(0.5, 5);
  });

  it("returns null when no stage was auto-selected", async () => {
    const record = runRecord(42);
    for (const detail of Object.values(record.stages)) {
      detail.model_selection.source = "stage-default";
    }
    const state = createState(await makeWorkspace([record]));

    expect(await state.getModelRoutingMetrics()).toBeNull();
  });

  it("skips malformed lines instead of failing the whole scan", async () => {
    const root = await makeWorkspace([runRecord(42)]);
    const file = path.join(root, ".nightgauge", "pipeline", "history", "2026-03-01.jsonl");
    await fs.writeFile(file, "{not json\n" + (await fs.readFile(file, "utf-8")), "utf-8");

    const metrics = await createState(root).getModelRoutingMetrics();

    expect(metrics!.totalAutoSelectedRuns).toBe(2);
  });
});
