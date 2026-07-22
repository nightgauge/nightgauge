/**
 * Tests for EvalRecorder: JSONL round-trip, regression diff, schema validation
 * (Issue #3814). Mock-mode only — no live model calls.
 */

import { describe, it, expect } from "vitest";
import {
  EvalRecorder,
  serializeReport,
  parseRecords,
  reportToRecords,
} from "../../src/eval/EvalRecorder.js";
import { EvalScenarioSchema, type EvalRunReport } from "../../src/eval/schemas.js";

const TS = "2026-05-30T19:40:00.000Z";

function makeReport(overrides: Partial<EvalRunReport> = {}): EvalRunReport {
  return {
    schema_version: "1",
    timestamp: TS,
    mode: "mock",
    skills: ["pr-create"],
    models: ["haiku", "sonnet", "opus"],
    cells: [
      {
        scenario_id: "s1",
        skill: "pr-create",
        model: "haiku",
        model_version_label: "Haiku 4.5",
        verdict: "pass",
        failures: [],
        exit_code: 0,
      },
      {
        scenario_id: "s1",
        skill: "pr-create",
        model: "opus",
        model_version_label: "Opus 4.8",
        verdict: "pass",
        failures: [],
        exit_code: 0,
      },
    ],
    summary: { total: 2, passed: 2, failed: 0, errored: 0 },
    ...overrides,
  };
}

describe("serialize / parse round-trip", () => {
  it("flattens a report into one record per cell", () => {
    const records = reportToRecords(makeReport());
    expect(records).toHaveLength(2);
    expect(records[0].schema_version).toBe("1");
    expect(records[0].timestamp).toBe(TS);
    expect(records[0].mode).toBe("mock");
  });

  it("serializes to JSONL and parses back to validated records", () => {
    const jsonl = serializeReport(makeReport());
    expect(jsonl.split("\n").filter(Boolean)).toHaveLength(2);
    const parsed = parseRecords(jsonl);
    expect(parsed).toHaveLength(2);
    expect(parsed[0].scenario_id).toBe("s1");
  });

  it("throws on a malformed JSONL line", () => {
    expect(() => parseRecords("not json\n")).toThrow(/invalid JSON/);
  });

  it("emits empty string for a report with no cells", () => {
    expect(
      serializeReport(
        makeReport({ cells: [], summary: { total: 0, passed: 0, failed: 0, errored: 0 } })
      )
    ).toBe("");
  });
});

describe("EvalRecorder.record — injected writer", () => {
  it("writes a JSONL file named by single skill + sanitized timestamp", async () => {
    const writes: Array<{ path: string; contents: string }> = [];
    const recorder = new EvalRecorder({
      dir: ".tmp/evals",
      writer: async (path, contents) => {
        writes.push({ path, contents });
      },
    });
    const filePath = await recorder.record(makeReport());
    expect(filePath).toBe(".tmp/evals/pr-create-2026-05-30T19-40-00-000Z.jsonl");
    expect(writes).toHaveLength(1);
    expect(writes[0].contents).toContain('"scenario_id":"s1"');
  });

  it("names the file 'multi' when several skills are covered", async () => {
    const recorder = new EvalRecorder({ dir: "d", writer: async () => {} });
    const filePath = await recorder.record(makeReport({ skills: ["pr-create", "pr-merge"] }));
    expect(filePath).toContain("/multi-");
  });
});

describe("EvalRecorder.diffAgainstBaseline", () => {
  const recorder = new EvalRecorder({ writer: async () => {} });

  it("flags a pass → fail flip as a regression", () => {
    const baseline = makeReport();
    const report = makeReport();
    report.cells[1] = { ...report.cells[1], verdict: "fail" }; // opus regressed
    const diff = recorder.diffAgainstBaseline(report, baseline);
    expect(diff.regressions).toHaveLength(1);
    expect(diff.regressions[0]).toMatchObject({
      scenario_id: "s1",
      model: "opus",
      from: "pass",
      to: "fail",
    });
    expect(diff.fixes).toHaveLength(0);
  });

  it("flags a fail → pass flip as a fix", () => {
    const baseline = makeReport();
    baseline.cells[0] = { ...baseline.cells[0], verdict: "fail" };
    const report = makeReport(); // haiku back to pass
    const diff = recorder.diffAgainstBaseline(report, baseline);
    expect(diff.fixes).toHaveLength(1);
    expect(diff.fixes[0].from).toBe("fail");
    expect(diff.fixes[0].to).toBe("pass");
    expect(diff.regressions).toHaveLength(0);
  });

  it("reports a cell with no baseline counterpart as added, not a regression", () => {
    const baseline = makeReport({
      cells: [],
      summary: { total: 0, passed: 0, failed: 0, errored: 0 },
    });
    const report = makeReport();
    const diff = recorder.diffAgainstBaseline(report, baseline);
    expect(diff.added).toHaveLength(2);
    expect(diff.regressions).toHaveLength(0);
  });

  it("does not treat a fail → error flip as a regression (was already failing)", () => {
    const baseline = makeReport();
    baseline.cells[1] = { ...baseline.cells[1], verdict: "fail" };
    const report = makeReport();
    report.cells[1] = { ...report.cells[1], verdict: "error" };
    const diff = recorder.diffAgainstBaseline(report, baseline);
    expect(diff.regressions).toHaveLength(0);
    expect(diff.fixes).toHaveLength(0);
  });

  it("treats a pass → error flip as a regression", () => {
    const baseline = makeReport();
    const report = makeReport();
    report.cells[0] = { ...report.cells[0], verdict: "error" };
    const diff = recorder.diffAgainstBaseline(report, baseline);
    expect(diff.regressions).toHaveLength(1);
    expect(diff.regressions[0].to).toBe("error");
  });
});

describe("schema validation rejects malformed scenarios", () => {
  it("rejects a non-kebab-case id", () => {
    const result = EvalScenarioSchema.safeParse({
      id: "Not Kebab",
      skill: "pr-create",
      description: "d",
      failure_mode: "f",
      prompt: "p",
      assertions: [{ type: "contains", value: "x" }],
    });
    expect(result.success).toBe(false);
  });

  it("rejects an empty assertions array", () => {
    const result = EvalScenarioSchema.safeParse({
      id: "ok-id",
      skill: "pr-create",
      description: "d",
      failure_mode: "f",
      prompt: "p",
      assertions: [],
    });
    expect(result.success).toBe(false);
  });

  it("rejects an unknown skill", () => {
    const result = EvalScenarioSchema.safeParse({
      id: "ok-id",
      skill: "not-a-skill",
      description: "d",
      failure_mode: "f",
      prompt: "p",
      assertions: [{ type: "contains", value: "x" }],
    });
    expect(result.success).toBe(false);
  });

  it("rejects an unknown assertion type", () => {
    const result = EvalScenarioSchema.safeParse({
      id: "ok-id",
      skill: "pr-create",
      description: "d",
      failure_mode: "f",
      prompt: "p",
      assertions: [{ type: "semantic_similarity", value: "x" }],
    });
    expect(result.success).toBe(false);
  });
});
