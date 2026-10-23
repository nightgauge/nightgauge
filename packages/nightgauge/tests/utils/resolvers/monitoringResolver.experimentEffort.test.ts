/**
 * monitoringResolver.experimentEffort.test.ts
 *
 * `getExperimentConfig` validated `model_routing.experiment.*.effort` against a
 * hand-listed array that stopped at `high` — the fifth surviving copy of the
 * effort vocabulary #394 collapses into `EFFORT_LEVELS`. A config declaring
 * `xhigh` or `max` passed the (now-widened) experiment schema and then had its
 * effort silently dropped to `undefined` on the way out of the resolver.
 *
 * These tests walk every member of `EFFORT_LEVELS` through the real config
 * entry point and pin that a non-member is still rejected.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { EFFORT_LEVELS } from "@nightgauge/sdk";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: undefined },
}));

import { getExperimentConfig } from "../../../src/utils/resolvers/monitoringResolver";

function writeExperimentConfig(root: string, controlEffort: string, treatmentEffort: string): void {
  const yaml = `model_routing:
  experiment:
    name: effort-vocabulary
    active: true
    control:
      model: sonnet
      effort: ${controlEffort}
    treatment:
      model: opus
      effort: ${treatmentEffort}
`;
  fs.writeFileSync(path.join(root, ".nightgauge", "config.yaml"), yaml);
}

describe("getExperimentConfig — effort vocabulary (#394)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "experiment-effort-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it.each([...EFFORT_LEVELS])("resolves '%s' on both arms rather than dropping it", (level) => {
    writeExperimentConfig(tmpRoot, level, level);

    const result = getExperimentConfig(tmpRoot);

    expect(result).not.toBeNull();
    expect(result?.control.effort).toBe(level);
    expect(result?.treatment.effort).toBe(level);
  });

  it("resolves a treatment arm set to 'xhigh' (the level the hand-list dropped)", () => {
    writeExperimentConfig(tmpRoot, "low", "xhigh");

    const result = getExperimentConfig(tmpRoot);

    expect(result?.treatment.effort).toBe("xhigh");
  });

  it("resolves a treatment arm set to 'max' (the level the hand-list dropped)", () => {
    writeExperimentConfig(tmpRoot, "low", "max");

    const result = getExperimentConfig(tmpRoot);

    expect(result?.treatment.effort).toBe("max");
  });

  it("still drops a value that is not an effort level at all", () => {
    writeExperimentConfig(tmpRoot, "low", "turbo");

    const result = getExperimentConfig(tmpRoot);

    expect(result?.control.effort).toBe("low");
    expect(result?.treatment.effort).toBeUndefined();
  });
});
