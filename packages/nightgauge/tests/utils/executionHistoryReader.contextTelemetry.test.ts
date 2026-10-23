/**
 * Context-window telemetry through the production read path (#1653).
 *
 * Go writes `stages[<name>].context_window_utilization` and its siblings.
 * Every production caller of the SDK's `flattenRunRecords` gets its records
 * from `ExecutionHistoryReader`, whose zod stage schema strips undeclared
 * keys, so the value reaches the analyzer only if that schema declares it.
 * This drives a real run record (the Go outcome-gap fixture) through the
 * reader's own JSONL parse, then through the feeder.
 */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { flattenRunRecords } from "@nightgauge/sdk";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";

const GO_RUN_RECORD = path.resolve(
  __dirname,
  "../../../../internal/ipc/testdata/outcome-gap/run-record.json"
);

describe("ExecutionHistoryReader → flattenRunRecords: context telemetry (#1653)", () => {
  let dir: string;

  beforeEach(() => {
    ExecutionHistoryReader.clearCache();
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-1653-"));
  });

  afterEach(() => {
    fs.rmSync(dir, { recursive: true, force: true });
  });

  it("keeps the stage's context fields and maps utilization to the analyzer record", async () => {
    const record = JSON.parse(fs.readFileSync(GO_RUN_RECORD, "utf8"));
    Object.assign(record.stages["feature-dev"], {
      peak_step_input_tokens: 121_897,
      context_window_tokens: 131_072,
      context_window_utilization: 0.93,
      compaction_count: 2,
    });
    const file = path.join(dir, "2026-07-28.jsonl");
    fs.writeFileSync(file, JSON.stringify(record) + "\n");

    const parsed = await ExecutionHistoryReader.parseJsonlFile(file);
    expect(parsed).toHaveLength(1);
    const stage = (parsed[0] as { stages: Record<string, Record<string, unknown>> }).stages[
      "feature-dev"
    ];
    expect(stage.peak_step_input_tokens).toBe(121_897);
    expect(stage.context_window_tokens).toBe(131_072);
    expect(stage.compaction_count).toBe(2);

    const flat = flattenRunRecords(parsed as Parameters<typeof flattenRunRecords>[0]);
    const dev = flat.find((r) => r.stage === "feature-dev");
    expect(dev?.contextWindowUtilization).toBe(0.93);
  });
});
