/**
 * StageModelCalibrationService.concurrentSave.test.ts — concurrent `save()`
 * calls on one calibration path must not destroy each other's temp file
 * (#786).
 *
 * Mirrors CalibrationService.concurrentSave.test.ts: `save()` used to write
 * a fixed `${calibrationPath}.tmp`, then rename it onto `calibrationPath` —
 * the exact race #777 fixed in `TelemetryStore.writeIndex`. With N
 * concurrent writers sharing one temp name, every writer overwrites the SAME
 * temp file and every writer renames it: the first rename moves the file
 * away and every later rename fails `ENOENT: no such file or directory,
 * rename '...stage-model-calibration.json.tmp' ->
 * '...stage-model-calibration.json'`.
 *
 * `PostPipelineAnalyzer` (nightgauge-vscode) calls
 * `StageModelCalibrationService.save` from a `try/catch` that only logs at
 * debug level on failure, so this race was invisible in practice.
 *
 * Deliberately NOT mocking node:fs/promises: the defect lives in the
 * ordering of two real filesystem calls, so a mocked fs cannot race at all.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

import {
  StageModelCalibrationService,
  type StageModelCalibrationTable,
} from "../../services/StageModelCalibrationService.js";

const CONCURRENT_WRITERS = 8;

let dir: string;
let calibrationPath: string;

function table(recordsAnalyzed: number): StageModelCalibrationTable {
  return {
    schema_version: "1",
    updated_at: new Date().toISOString(),
    total_records_analyzed: recordsAnalyzed,
    buckets: {},
  };
}

beforeEach(async () => {
  dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-stage-model-calibration-race-"));
  calibrationPath = path.join(dir, "stage-model-calibration.json");
});

afterEach(async () => {
  await fs.rm(dir, { recursive: true, force: true });
});

describe("StageModelCalibrationService.save — concurrent writers on one path (#786)", () => {
  it("all writes succeed; none loses its temp file to another's rename", async () => {
    const settled = await Promise.allSettled(
      Array.from({ length: CONCURRENT_WRITERS }, (_, i) =>
        StageModelCalibrationService.save(calibrationPath, table(i))
      )
    );

    const rejected = settled.filter((r) => r.status === "rejected") as PromiseRejectedResult[];
    expect(rejected.map((r) => String(r.reason))).toEqual([]);
  });

  it("leaves one readable calibration file and no temp files behind", async () => {
    await Promise.all(
      Array.from({ length: CONCURRENT_WRITERS }, (_, i) =>
        StageModelCalibrationService.save(calibrationPath, table(i))
      )
    );

    const content = await fs.readFile(calibrationPath, "utf-8");
    const parsed = JSON.parse(content) as StageModelCalibrationTable;
    expect(parsed.schema_version).toBe("1");

    const leftoverTmp = (await fs.readdir(dir)).filter((f) => f.includes(".tmp"));
    expect(leftoverTmp).toEqual([]);
  });
});
