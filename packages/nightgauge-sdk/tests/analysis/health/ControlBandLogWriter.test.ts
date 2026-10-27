/**
 * ControlBandLogWriter (#1286): every tier persists; `none` and failures do not throw.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { ControlBandLogWriter } from "../../../src/analysis/health/ControlBandLogWriter.js";
import type { ControlBandEvaluation } from "../../../src/analysis/health/controlBands.js";
import { cloneLayoutFor, setCloneLayout } from "../../../src/context/cloneLayout.js";

const evaluation = (tier: ControlBandEvaluation["tier"]): ControlBandEvaluation => ({
  metric: "cost_per_run_usd",
  tier,
  action: tier === "none" ? null : "record",
  reason: tier,
  value: 12,
  z: 1.5,
  bounds: {},
  baseline: null,
});

let root: string;
beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "cbw-"));
  setCloneLayout(root, cloneLayoutFor(root, path.join(root, ".git")));
});
afterEach(() => fs.rmSync(root, { recursive: true, force: true }));

const entry = (tier: ControlBandEvaluation["tier"]) => ({
  timestamp: "2026-01-15T10:00:00Z",
  run_id: "r1",
  issue_number: 1,
  evaluation: evaluation(tier),
});

describe("ControlBandLogWriter", () => {
  it("persists an innermost excursion beside the health series", async () => {
    await ControlBandLogWriter.append(root, entry("innermost"));
    const file = ControlBandLogWriter.getFilePath(root);
    expect(path.basename(file)).toBe("control-bands.jsonl");
    expect(await ControlBandLogWriter.read(root)).toEqual([entry("innermost")]);
  });

  it("skips a `none` evaluation", async () => {
    await ControlBandLogWriter.append(root, entry("none"));
    expect(fs.existsSync(ControlBandLogWriter.getFilePath(root))).toBe(false);
  });

  it("never throws on an unusable root", async () => {
    await expect(
      ControlBandLogWriter.append("relative", entry("outermost"))
    ).resolves.toBeUndefined();
    expect(await ControlBandLogWriter.read("relative")).toEqual([]);
  });
});
