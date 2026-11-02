/**
 * gateMetricsWriter.test.ts
 *
 * Unit tests for GateMetricsWriter utility class.
 *
 * @see Issue #1412 - Quality gate hit-rate metrics
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  type JudgeVerdict,
  type WorkflowJudgeVerdict,
} from "@nightgauge/sdk";
import { GateMetricsWriter } from "../../src/utils/gateMetricsWriter";
import { GateMetricRecordSchema, type GateMetricRecord } from "../../src/schemas/gateMetrics";
import { fakeCloneLayout } from "../helpers/cloneLayout";

// Mock node:fs/promises
vi.mock("node:fs/promises");

/** Build a JudgeVerdict node for the appendJudgeVerdicts() tests. */
const JUDGE_PHASE = "phase:run-1:0";

function judgeNode(
  verdict: WorkflowJudgeVerdict,
  overrides: Partial<JudgeVerdict> = {}
): JudgeVerdict {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "judge",
    nodeId: "judge:run-1:0:0",
    parentId: JUDGE_PHASE,
    seq: 3,
    ts: "2026-02-28T10:05:00.000Z",
    status: "succeeded",
    judgeId: "refuter",
    provider: "claude",
    target: "agent:run-1:0:0",
    verdict,
    usage: zeroUsage(),
    ...overrides,
  };
}

/** A bare stream node, carrying only what the writer reads. */
function node(fields: { kind: string } & Record<string, unknown>): { kind: string } {
  return fields;
}

/** A run and its phase, which owns the judges `judgeNode` builds. */
function runNodes(runId: string): Array<Record<string, unknown> & { kind: string }> {
  return [
    { kind: "run", nodeId: `run:${runId}`, parentId: null, runId },
    { kind: "phase", nodeId: JUDGE_PHASE, parentId: `run:${runId}` },
  ];
}

/** Every row handed to fs.appendFile, across calls. */
function writtenRows(): Array<Record<string, unknown>> {
  return vi
    .mocked(fs.appendFile)
    .mock.calls.flatMap((c) => (c[1] as string).trimEnd().split("\n"))
    .map((line) => JSON.parse(line));
}

describe("GateMetricsWriter", () => {
  const workspaceRoot = "/test/workspace";
  // Per checkout, in CHECKOUT (ADR-024 § 7).
  const expectedFilePath = "/test/workspace/.git/nightgauge-worktree/health/gate-metrics.jsonl";

  const validRecord: GateMetricRecord = {
    schema_version: "1",
    timestamp: "2026-02-28T10:00:00.000Z",
    issue_number: 1412,
    gate_name: "build",
    result: "pass",
    issue_type: "feature",
    complexity_label: "M",
    duration_ms: 12000,
    error_summary: null,
  };

  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout(workspaceRoot);
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("getFilePath()", () => {
    it("returns the expected path under the checkout's CHECKOUT", () => {
      expect(GateMetricsWriter.getFilePath(workspaceRoot)).toBe(expectedFilePath);
    });

    it("gives a linked worktree its own file", () => {
      fakeCloneLayout("/test/wt", "/test/workspace/.git", "/test/workspace/.git/worktrees/wt");
      expect(GateMetricsWriter.getFilePath("/test/wt")).toBe(
        "/test/workspace/.git/worktrees/wt/nightgauge-worktree/health/gate-metrics.jsonl"
      );
    });
  });

  describe("appendRecord()", () => {
    it("writes a valid record as a JSONL line", async () => {
      await GateMetricsWriter.appendRecord(workspaceRoot, validRecord);

      expect(fs.mkdir).toHaveBeenCalledWith(expect.stringContaining("nightgauge-worktree/health"), {
        recursive: true,
      });
      expect(fs.appendFile).toHaveBeenCalledWith(
        expectedFilePath,
        expect.stringMatching(/^\{.*\}\n$/),
        "utf-8"
      );
    });

    it("written line contains the record fields", async () => {
      await GateMetricsWriter.appendRecord(workspaceRoot, validRecord);

      const writtenLine = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(writtenLine.trim());
      expect(parsed.gate_name).toBe("build");
      expect(parsed.result).toBe("pass");
      expect(parsed.issue_number).toBe(1412);
    });

    it("silently skips invalid records without throwing", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      const invalidRecord = {
        schema_version: "1",
        // missing required fields: timestamp, issue_number, gate_name, result
      } as unknown as GateMetricRecord;

      await expect(
        GateMetricsWriter.appendRecord(workspaceRoot, invalidRecord)
      ).resolves.toBeUndefined();

      expect(fs.appendFile).not.toHaveBeenCalled();
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("Invalid gate metric record"));

      warnSpy.mockRestore();
    });

    it("creates parent directory if missing", async () => {
      await GateMetricsWriter.appendRecord(workspaceRoot, validRecord);

      expect(fs.mkdir).toHaveBeenCalledWith(expect.stringContaining("nightgauge-worktree/health"), {
        recursive: true,
      });
    });

    it("handles fs errors gracefully without throwing", async () => {
      vi.mocked(fs.mkdir).mockRejectedValue(new Error("EACCES: permission denied"));
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      await expect(
        GateMetricsWriter.appendRecord(workspaceRoot, validRecord)
      ).resolves.toBeUndefined();

      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining("Failed to write gate metric record")
      );

      warnSpy.mockRestore();
    });

    it('records a "catch" result with an error_summary', async () => {
      const catchRecord: GateMetricRecord = {
        ...validRecord,
        gate_name: "unit-tests",
        result: "catch",
        error_summary: "Expected 3 received 0",
      };

      await GateMetricsWriter.appendRecord(workspaceRoot, catchRecord);

      const writtenLine = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(writtenLine.trim());
      expect(parsed.result).toBe("catch");
      expect(parsed.error_summary).toBe("Expected 3 received 0");
    });
  });

  describe("appendJudgeVerdicts()", () => {
    it('writes a {gate_name:"judges", result:"pass"} record for a pass verdict', async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [judgeNode("pass")]);

      expect(fs.appendFile).toHaveBeenCalledTimes(1);
      const writtenLine = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(writtenLine.trim());
      expect(parsed.gate_name).toBe("judges");
      expect(parsed.result).toBe("pass");
      expect(parsed.issue_number).toBe(3918);
      expect(parsed.schema_version).toBe("1");
      expect(parsed.timestamp).toBe("2026-02-28T10:05:00.000Z");
    });

    it('writes result:"fail" for a fail verdict and carries the rationale as error_summary', async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        judgeNode("fail", { rationale: "claimed tests pass but suite was never run\nmore detail" }),
      ]);

      const writtenLine = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(writtenLine.trim());
      expect(parsed.result).toBe("fail");
      // Only the first rationale line is carried.
      expect(parsed.error_summary).toBe("claimed tests pass but suite was never run");
    });

    it('maps an "uncertain" verdict to result:"fail" (fail-closed)', async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [judgeNode("uncertain")]);

      const writtenLine = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(writtenLine.trim());
      expect(parsed.result).toBe("fail");
    });

    it("the written record passes the gate-metrics Zod schema", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [judgeNode("fail")]);

      const writtenLine = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(writtenLine.trim());
      expect(GateMetricRecordSchema.safeParse(parsed).success).toBe(true);
    });

    it("writes one record per judge verdict and ignores non-judge nodes", async () => {
      const nonJudge = { kind: "agent" } as unknown as JudgeVerdict;
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        judgeNode("pass", { nodeId: "judge:run-1:0:0" }),
        nonJudge,
        judgeNode("fail", { nodeId: "judge:run-1:0:1" }),
      ]);

      expect(writtenRows().map((r) => r.result)).toEqual(["fail", "pass"]);
    });

    it("two folds of one run share an attempt, so the first's failure stands (#2778)", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        ...runNodes("run-1"),
        judgeNode("fail"),
      ]);
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        ...runNodes("run-1"),
        judgeNode("pass"),
      ]);

      expect(writtenRows().map((r) => [r.attempt_id, r.result])).toEqual([
        ["run-1", "fail"],
        ["run-1", "pass"],
      ]);
    });

    it("gives different runs different attempts, so a retry can replace one", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        ...runNodes("run-1"),
        judgeNode("fail"),
      ]);
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        ...runNodes("run-2"),
        judgeNode("pass"),
      ]);

      expect(writtenRows().map((r) => r.attempt_id)).toEqual(["run-1", "run-2"]);
    });

    it("omits attempt_id when the stream has no run node", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [judgeNode("pass")]);

      expect(writtenRows()[0]).not.toHaveProperty("attempt_id");
    });

    it("takes the judges' own run when the stream also carries the pipeline's", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        node({ kind: "run", nodeId: "run:3918", parentId: null, runId: "run:3918" }),
        ...runNodes("wf-3918-feature-validate-a"),
        judgeNode("pass"),
      ]);

      expect(writtenRows()[0].attempt_id).toBe("wf-3918-feature-validate-a");
    });

    it("omits attempt_id when the judges belong to two runs", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        node({ kind: "run", nodeId: "run:a", parentId: null, runId: "a" }),
        node({ kind: "run", nodeId: "run:b", parentId: null, runId: "b" }),
        node({ kind: "phase", nodeId: "phase:a", parentId: "run:a" }),
        node({ kind: "phase", nodeId: "phase:b", parentId: "run:b" }),
        judgeNode("pass", { parentId: "phase:a" }),
        judgeNode("pass", { parentId: "phase:b" }),
      ]);

      expect(writtenRows()[0]).not.toHaveProperty("attempt_id");
    });

    it("omits attempt_id when the run's id is empty", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        ...runNodes(""),
        judgeNode("pass"),
      ]);

      expect(writtenRows()[0]).not.toHaveProperty("attempt_id");
    });

    it("writes an attempt in exactly one append", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        ...runNodes("run-1"),
        judgeNode("pass", { nodeId: "judge:run-1:0:0" }),
        judgeNode("fail", { nodeId: "judge:run-1:0:1" }),
        judgeNode("pass", { nodeId: "judge:run-1:0:2" }),
      ]);

      expect(fs.appendFile).toHaveBeenCalledTimes(1);
      // Failures lead, so a write torn anywhere cannot keep a pass and lose a fail.
      expect(writtenRows().map((r) => r.result)).toEqual(["fail", "pass", "pass"]);
    });

    it("a failed append warns, does not throw, and is not retried row by row", async () => {
      vi.mocked(fs.appendFile).mockRejectedValue(new Error("ENOSPC"));
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      await expect(
        GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
          ...runNodes("run-1"),
          judgeNode("pass", { nodeId: "judge:run-1:0:0" }),
          judgeNode("fail", { nodeId: "judge:run-1:0:1" }),
        ])
      ).resolves.toBeUndefined();

      expect(fs.appendFile).toHaveBeenCalledTimes(1);
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("Failed to write judge"));
    });

    it("one invalid failing record writes nothing, not the passing rest", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        ...runNodes("run-1"),
        judgeNode("pass", { nodeId: "judge:run-1:0:0" }),
        judgeNode("fail", { nodeId: "judge:run-1:0:1", ts: 0 as unknown as string }),
      ]);

      expect(fs.appendFile).not.toHaveBeenCalled();
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("dropping the attempt"));
    });

    it("writes nothing when there are no judge verdicts", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, runNodes("run-1"));
      expect(fs.appendFile).not.toHaveBeenCalled();
      expect(fs.mkdir).not.toHaveBeenCalled();
    });

    it("omits error_summary on a passing verdict even when a rationale is present", async () => {
      await GateMetricsWriter.appendJudgeVerdicts(workspaceRoot, 3918, [
        judgeNode("pass", { rationale: "all claims independently confirmed" }),
      ]);

      const writtenLine = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(writtenLine.trim());
      expect(parsed.error_summary).toBeUndefined();
    });
  });

  describe("readAll()", () => {
    it("returns an empty array when file does not exist", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const records = await GateMetricsWriter.readAll(workspaceRoot);
      expect(records).toEqual([]);
    });

    it("parses multiple valid JSONL lines", async () => {
      const line1 = JSON.stringify(validRecord);
      const line2 = JSON.stringify({
        ...validRecord,
        gate_name: "lint",
        result: "catch",
      });
      vi.mocked(fs.readFile).mockResolvedValue(`${line1}\n${line2}\n` as any);

      const records = await GateMetricsWriter.readAll(workspaceRoot);
      expect(records).toHaveLength(2);
      expect(records[0].gate_name).toBe("build");
      expect(records[1].gate_name).toBe("lint");
    });

    it("skips malformed JSON lines silently", async () => {
      const validLine = JSON.stringify(validRecord);
      vi.mocked(fs.readFile).mockResolvedValue(`${validLine}\nnot-valid-json\n` as any);

      const records = await GateMetricsWriter.readAll(workspaceRoot);
      expect(records).toHaveLength(1);
    });

    it("skips lines that fail Zod validation", async () => {
      const invalidRecord = { schema_version: "1", gate_name: "unknown-gate" };
      const validLine = JSON.stringify(validRecord);
      const invalidLine = JSON.stringify(invalidRecord);
      vi.mocked(fs.readFile).mockResolvedValue(`${validLine}\n${invalidLine}\n` as any);

      const records = await GateMetricsWriter.readAll(workspaceRoot);
      expect(records).toHaveLength(1);
    });

    it("returns all valid records across all gate types", async () => {
      const gateNames = ["build", "unit-tests", "integration-tests", "type-check", "lint"] as const;
      const lines = gateNames.map((g) => JSON.stringify({ ...validRecord, gate_name: g }));
      vi.mocked(fs.readFile).mockResolvedValue((lines.join("\n") + "\n") as any);

      const records = await GateMetricsWriter.readAll(workspaceRoot);
      expect(records).toHaveLength(5);
      expect(records.map((r) => r.gate_name)).toEqual(gateNames);
    });
  });

  describe("pruneOldEntries()", () => {
    it("removes entries older than retention period", async () => {
      const oldTimestamp = new Date(Date.now() - 100 * 24 * 60 * 60 * 1000).toISOString();
      const recentTimestamp = new Date().toISOString();

      const oldRecord = JSON.stringify({
        ...validRecord,
        timestamp: oldTimestamp,
      });
      const recentRecord = JSON.stringify({
        ...validRecord,
        timestamp: recentTimestamp,
      });

      vi.mocked(fs.readFile).mockResolvedValue(`${oldRecord}\n${recentRecord}\n` as any);
      vi.mocked(fs.writeFile).mockResolvedValue();

      await GateMetricsWriter.pruneOldEntries(workspaceRoot, 90);

      const writtenContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      const lines = writtenContent
        .split("\n")
        .filter((l) => l.trim())
        .map((l) => JSON.parse(l));
      expect(lines).toHaveLength(1);
      expect(lines[0].timestamp).toBe(recentTimestamp);
    });

    it("handles missing file gracefully", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      await expect(GateMetricsWriter.pruneOldEntries(workspaceRoot, 90)).resolves.toBeUndefined();

      expect(fs.writeFile).not.toHaveBeenCalled();
    });
  });
});
