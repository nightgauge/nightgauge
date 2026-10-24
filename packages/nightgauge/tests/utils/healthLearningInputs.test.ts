/**
 * healthLearningInputs — the files the learning-effectiveness and skill-drift
 * dimensions read (#1900). Absent files read as null, never as empty data.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { readLearningSignals, readSkillEffectiveness } from "../../src/utils/healthLearningInputs";
import { mkFakeCloneLayout } from "../helpers/cloneLayout";

let root: string;
let pipeline: string;
let checkout: string;
const filter = {
  startDate: new Date("2026-03-01T00:00:00Z"),
  endDate: new Date("2026-03-31T23:59:59Z"),
};

beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "ng-learning-inputs-"));
  const layout = mkFakeCloneLayout(root);
  pipeline = layout.pipeline;
  checkout = layout.checkout;
});

afterEach(() => fs.rmSync(root, { recursive: true, force: true }));

describe("readLearningSignals", () => {
  it("is null when outcomes.jsonl is absent", async () => {
    expect(await readLearningSignals(root, filter)).toBeNull();
  });

  it("reads in-period outcome pairs and calibration updated_at", async () => {
    fs.mkdirSync(path.join(pipeline, "history"), { recursive: true });
    const rows = [
      { completedAt: "2026-02-01T00:00:00Z", predictedModel: "opus", actualModel: "opus" },
      {
        schema_version: "2",
        completedAt: "2026-03-10T00:00:00-06:00",
        predictedModel: "sonnet",
        actualModel: "",
        predictedSize: "small",
        actualSize: "small",
        issueNumber: 1,
      },
    ];
    fs.writeFileSync(
      path.join(pipeline, "history", "outcomes.jsonl"),
      rows.map((r) => JSON.stringify(r)).join("\n") + "\n{broken\n"
    );
    fs.writeFileSync(
      path.join(pipeline, "calibration.json"),
      JSON.stringify({ updated_at: "2026-03-09T00:00:00Z" })
    );

    expect(await readLearningSignals(root, filter)).toEqual({
      outcomes: [
        {
          completedAt: "2026-03-10T00:00:00-06:00",
          predictedModel: "sonnet",
          actualModel: "",
          predictedSize: "small",
          actualSize: "small",
        },
      ],
      calibrationUpdatedAt: "2026-03-09T00:00:00Z",
    });
  });

  it("reports calibration as null when calibration.json is absent", async () => {
    fs.mkdirSync(path.join(pipeline, "history"), { recursive: true });
    fs.writeFileSync(path.join(pipeline, "history", "outcomes.jsonl"), "");
    expect(await readLearningSignals(root, filter)).toEqual({
      outcomes: [],
      calibrationUpdatedAt: null,
    });
  });
});

describe("readSkillEffectiveness", () => {
  it("is null when skill-effectiveness.jsonl is absent", async () => {
    expect(await readSkillEffectiveness(root, filter)).toBeNull();
  });

  it("returns the latest in-period record per skill change", async () => {
    fs.mkdirSync(path.join(checkout, "health"), { recursive: true });
    const base = {
      schema_version: "1",
      skill_file: "skills/x/SKILL.md",
      stage: "x",
      commit_hash: "abc",
      changed_at: "2026-03-05T00:00:00Z",
      before_sample_count: 10,
      before_success_rate: 0.9,
      after_sample_count: 5,
      after_success_rate: 0.9,
      delta: 0,
      confidence: "low",
    };
    const lines = [
      { ...base, classification: "neutral", analyzed_at: "2026-03-06T00:00:00Z" },
      { ...base, classification: "regression", analyzed_at: "2026-03-07T00:00:00Z" },
      {
        ...base,
        commit_hash: "old",
        changed_at: "2026-01-01T00:00:00Z",
        classification: "neutral",
        analyzed_at: "2026-03-07T00:00:00Z",
      },
    ];
    fs.writeFileSync(
      path.join(checkout, "health", "skill-effectiveness.jsonl"),
      lines.map((l) => JSON.stringify(l)).join("\n") + "\n"
    );
    const result = await readSkillEffectiveness(root, filter);
    expect(result).toHaveLength(1);
    expect(result![0].classification).toBe("regression");
  });
});
