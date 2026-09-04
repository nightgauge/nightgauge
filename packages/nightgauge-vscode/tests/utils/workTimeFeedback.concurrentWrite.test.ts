/**
 * workTimeFeedback.concurrentWrite.test.ts — two `appendObservationToYAML`
 * calls on one workspace must not destroy each other's temp file (#786).
 *
 * `appendObservationToYAML` wrote a fixed `${yamlPath}.tmp`, then renamed it
 * onto `yamlPath` (the exact shape #777 fixed in `TelemetryStore.writeIndex`).
 * With N concurrent writers sharing one temp name, every writer overwrites the
 * SAME temp file and every writer renames it: the first rename moves the file
 * away and every later rename fails `ENOENT: no such file or directory,
 * rename '...complexity-model.yaml.tmp' -> '...complexity-model.yaml'`.
 *
 * Deliberately NOT mocking node:fs/promises: the defect lives in the ordering
 * of two real filesystem calls, so a mocked fs cannot race at all. Two
 * concurrent writes is the ordinary case here — merge-time feedback recording
 * can overlap a second pipeline stage's own observation write.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as fsSync from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import * as yaml from "js-yaml";

import {
  appendObservationToYAML,
  type WorkTimeObservation,
} from "../../src/utils/workTimeFeedback";

const CONCURRENT_WRITERS = 8;

let dir: string;
let yamlPath: string;

function observation(issueNumber: number): WorkTimeObservation {
  return {
    issue_number: issueNumber,
    size: "S",
    priority: null,
    task_type: null,
    actual_work_minutes: 12,
    estimated_minutes: 10,
    routing: "standard",
    stages_completed: ["feature-dev"],
    timestamp: new Date(Date.UTC(2026, 7, 12, 10, issueNumber)).toISOString(),
  };
}

beforeEach(async () => {
  dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-worktime-race-"));
  yamlPath = path.join(dir, "complexity-model.yaml");
});

afterEach(async () => {
  await fs.rm(dir, { recursive: true, force: true });
});

describe("appendObservationToYAML — concurrent writers on one workspace (#786)", () => {
  it("all writes succeed; none loses its temp file to another's rename", async () => {
    const settled = await Promise.allSettled(
      Array.from({ length: CONCURRENT_WRITERS }, (_, i) =>
        appendObservationToYAML(observation(800 + i), yamlPath)
      )
    );

    // Report the actual rejection rather than a bare count — an ENOENT on
    // rename is the specific failure this test exists to keep out.
    const rejected = settled.filter((r) => r.status === "rejected") as PromiseRejectedResult[];
    expect(rejected.map((r) => String(r.reason))).toEqual([]);
  });

  it("leaves one readable YAML file and no temp files behind", async () => {
    await Promise.all(
      Array.from({ length: CONCURRENT_WRITERS }, (_, i) =>
        appendObservationToYAML(observation(800 + i), yamlPath)
      )
    );

    const content = await fs.readFile(yamlPath, "utf-8");
    const parsed = yaml.load(content) as { work_time_feedback: { observations: unknown[] } };
    expect(Array.isArray(parsed.work_time_feedback.observations)).toBe(true);

    const leftoverTmp = fsSync.readdirSync(dir).filter((f) => f.endsWith(".tmp"));
    expect(leftoverTmp).toEqual([]);
  });
});
