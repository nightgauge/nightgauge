/**
 * Tests for the SDK judge-verdict fold into gate-metrics (#3918, #2778, #2880).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  type JudgeVerdict,
  type WorkflowJudgeVerdict,
} from "../../cli/workflow/index.js";
import { checkoutPath } from "../../context/cloneLayout.js";
import {
  appendJudgeVerdicts,
  gateMetricsFilePath,
  JUDGES_INTERRUPTED_MARKER,
  JUDGES_NONE_MARKER,
} from "../../orchestrator/judgeGateMetrics.js";
import { initCloneRepo } from "../helpers/gitRepo.js";

// Pass-through mock: real writes, with appendFile observable (ESM namespaces
// cannot be spied on directly).
vi.mock("node:fs/promises", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:fs/promises")>();
  return { ...actual, appendFile: vi.fn(actual.appendFile) };
});

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

/** A bare stream node, carrying only what the fold reads. */
function node(fields: { kind: string } & Record<string, unknown>): { kind: string } {
  return fields;
}

/** A run and its phase, which owns the judges `judgeNode` builds. */
function runNodes(runId: string): Array<{ kind: string }> {
  return [
    node({ kind: "run", nodeId: `run:${runId}`, parentId: null, runId }),
    node({ kind: "phase", nodeId: JUDGE_PHASE, parentId: `run:${runId}` }),
  ];
}

describe("appendJudgeVerdicts", () => {
  let dir: string;

  beforeEach(async () => {
    dir = await fs.mkdtemp(path.join(os.tmpdir(), "judge-gate-"));
    initCloneRepo(dir);
    vi.mocked(fs.appendFile).mockClear();
  });
  afterEach(async () => {
    vi.restoreAllMocks();
    await fs.rm(dir, { recursive: true, force: true });
  });

  async function rows(): Promise<Array<Record<string, unknown>>> {
    let content: string;
    try {
      content = await fs.readFile(gateMetricsFilePath(dir), "utf-8");
    } catch {
      return [];
    }
    return content
      .split("\n")
      .filter(Boolean)
      .map((l) => JSON.parse(l) as Record<string, unknown>);
  }

  it("writes to the checkout's health/gate-metrics.jsonl", () => {
    expect(gateMetricsFilePath(dir)).toBe(
      path.join(checkoutPath("health", dir), "gate-metrics.jsonl")
    );
  });

  it('writes a {gate_name:"judges", result:"pass"} record for a pass verdict', async () => {
    await appendJudgeVerdicts(dir, 3918, [judgeNode("pass")]);
    expect(await rows()).toEqual([
      {
        schema_version: "1",
        timestamp: "2026-02-28T10:05:00.000Z",
        issue_number: 3918,
        gate_name: "judges",
        result: "pass",
      },
    ]);
  });

  it("maps fail to result:fail with the first rationale line as error_summary", async () => {
    await appendJudgeVerdicts(dir, 3918, [
      judgeNode("fail", { rationale: "claimed tests pass but suite was never run\nmore" }),
    ]);
    const [row] = await rows();
    expect(row.result).toBe("fail");
    expect(row.error_summary).toBe("claimed tests pass but suite was never run");
  });

  it('maps an "uncertain" verdict to result:"fail" (fail-closed)', async () => {
    await appendJudgeVerdicts(dir, 3918, [judgeNode("uncertain")]);
    expect((await rows())[0].result).toBe("fail");
  });

  it("writes one record per judge, failures first, ignoring other nodes", async () => {
    await appendJudgeVerdicts(dir, 3918, [
      judgeNode("pass", { nodeId: "judge:run-1:0:0" }),
      node({ kind: "agent", nodeId: "agent:x" }),
      judgeNode("fail", { nodeId: "judge:run-1:0:1" }),
    ]);
    expect((await rows()).map((r) => r.result)).toEqual(["fail", "pass"]);
  });

  it("two folds of one run share an attempt (#2778)", async () => {
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-1"), judgeNode("fail")]);
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-1"), judgeNode("pass")]);
    expect((await rows()).map((r) => [r.attempt_id, r.result])).toEqual([
      ["run-1", "fail"],
      ["run-1", "pass"],
    ]);
  });

  it("gives different runs different attempts", async () => {
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-1"), judgeNode("fail")]);
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-2"), judgeNode("pass")]);
    expect((await rows()).map((r) => r.attempt_id)).toEqual(["run-1", "run-2"]);
  });

  it("omits attempt_id for an interrupted run, so it never supersedes a failure", async () => {
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-1"), judgeNode("fail")]);
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-2"), judgeNode("pass")], {
      complete: false,
    });
    const written = await rows();
    expect(written[0].attempt_id).toBe("run-1");
    // run-2's marker fails under its own id; its partial pass carries none.
    expect(written[1]).toMatchObject({ result: "fail", attempt_id: "run-2" });
    expect(written[2]).toMatchObject({ result: "pass" });
    expect(written[2]).not.toHaveProperty("attempt_id");
  });

  it("omits attempt_id when the stream has no run node", async () => {
    await appendJudgeVerdicts(dir, 3918, [judgeNode("pass")]);
    expect((await rows())[0]).not.toHaveProperty("attempt_id");
  });

  it("takes the judges' own run when the stream also carries the pipeline's", async () => {
    await appendJudgeVerdicts(dir, 3918, [
      node({ kind: "run", nodeId: "run:3918", parentId: null, runId: "run:3918" }),
      ...runNodes("wf-3918-feature-validate-a"),
      judgeNode("pass"),
    ]);
    expect((await rows())[0].attempt_id).toBe("wf-3918-feature-validate-a");
  });

  it("omits attempt_id when the judges belong to two runs", async () => {
    await appendJudgeVerdicts(dir, 3918, [
      node({ kind: "run", nodeId: "run:a", parentId: null, runId: "a" }),
      node({ kind: "run", nodeId: "run:b", parentId: null, runId: "b" }),
      node({ kind: "phase", nodeId: "phase:a", parentId: "run:a" }),
      node({ kind: "phase", nodeId: "phase:b", parentId: "run:b" }),
      judgeNode("pass", { parentId: "phase:a" }),
      judgeNode("pass", { parentId: "phase:b" }),
    ]);
    expect((await rows())[0]).not.toHaveProperty("attempt_id");
  });

  it("omits attempt_id when the run's id is empty", async () => {
    await appendJudgeVerdicts(dir, 3918, [...runNodes(""), judgeNode("pass")]);
    expect((await rows())[0]).not.toHaveProperty("attempt_id");
  });

  it("writes an attempt in exactly one append, failures first", async () => {
    await appendJudgeVerdicts(dir, 3918, [
      ...runNodes("run-1"),
      judgeNode("pass", { nodeId: "judge:run-1:0:0" }),
      judgeNode("fail", { nodeId: "judge:run-1:0:1" }),
      judgeNode("pass", { nodeId: "judge:run-1:0:2" }),
    ]);
    expect(fs.appendFile).toHaveBeenCalledTimes(1);
    expect((await rows()).map((r) => r.result)).toEqual(["fail", "pass", "pass"]);
  });

  it("a failed append warns and does not throw", async () => {
    vi.mocked(fs.appendFile).mockRejectedValueOnce(new Error("ENOSPC"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(
      appendJudgeVerdicts(dir, 3918, [...runNodes("run-1"), judgeNode("fail")])
    ).resolves.toBeUndefined();
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("Failed to write judge"));
  });

  it("one invalid failing record writes nothing, not the passing rest", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await appendJudgeVerdicts(dir, 3918, [
      ...runNodes("run-1"),
      judgeNode("pass", { nodeId: "judge:run-1:0:0" }),
      judgeNode("fail", { nodeId: "judge:run-1:0:1", ts: 0 as unknown as string }),
    ]);
    expect(await rows()).toEqual([]);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("dropping the attempt"));
  });

  it("writes nothing, and creates no file, when there are no judges", async () => {
    await appendJudgeVerdicts(dir, 3918, runNodes("run-1"));
    await expect(fs.stat(gateMetricsFilePath(dir))).rejects.toThrow();
  });

  it("an interrupted run with no verdict writes one failing marker under its run", async () => {
    await appendJudgeVerdicts(dir, 3918, runNodes("run-1"), { complete: false });
    const written = await rows();
    expect(written).toHaveLength(1);
    expect(written[0]).toMatchObject({
      schema_version: "1",
      gate_name: "judges",
      issue_number: 3918,
      result: "fail",
      attempt_id: "run-1",
    });
    expect(String(written[0].error_summary).startsWith(JUDGES_INTERRUPTED_MARKER)).toBe(true);
  });

  it("an interrupted run with no run node writes the marker without attempt_id", async () => {
    await appendJudgeVerdicts(dir, 3918, [], { complete: false });
    const written = await rows();
    expect(written).toHaveLength(1);
    expect(written[0]).toMatchObject({ gate_name: "judges", result: "fail" });
    expect(written[0]).not.toHaveProperty("attempt_id");
  });

  it("a complete attempt with no verdict closes an earlier interrupted marker", async () => {
    await appendJudgeVerdicts(dir, 3918, [], { complete: false, attemptId: "wf-1" });
    await appendJudgeVerdicts(dir, 3918, [], { complete: true, attemptId: "sa-2" });
    const written = await rows();
    expect(written).toHaveLength(2);
    expect(written[1]).toMatchObject({ gate_name: "judges", result: "pass", attempt_id: "sa-2" });
    expect(String(written[1].error_summary).startsWith(JUDGES_NONE_MARKER)).toBe(true);
  });

  it("a complete attempt with no verdict never closes a real failing judges attempt", async () => {
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-1"), judgeNode("fail")]);
    await appendJudgeVerdicts(dir, 3918, [], { complete: true, attemptId: "sa-2" });
    const written = await rows();
    expect(written).toHaveLength(1);
    expect(written[0]).toMatchObject({ result: "fail", attempt_id: "run-1" });
  });

  it("a marker after a real failing attempt cannot let a no-verdict attempt close it", async () => {
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-0"), judgeNode("fail")]);
    await appendJudgeVerdicts(dir, 3918, [], { complete: false, attemptId: "run-1" });
    await appendJudgeVerdicts(dir, 3918, [], { complete: true, attemptId: "sa-2" });
    expect(await rows()).toHaveLength(2);
  });

  it("an interrupted run drops judges the abort cut off, writing only the marker", async () => {
    await appendJudgeVerdicts(
      dir,
      3918,
      [...runNodes("run-1"), judgeNode("uncertain", { status: "failed" })],
      { complete: false }
    );
    const written = await rows();
    expect(written).toHaveLength(1);
    expect(String(written[0].error_summary).startsWith(JUDGES_INTERRUPTED_MARKER)).toBe(true);
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-2"), judgeNode("pass")]);
    const after = await rows();
    expect(after[1]).toMatchObject({ result: "pass", attempt_id: "run-2" });
  });

  it("a complete attempt with no verdict writes nothing for an issue never judged", async () => {
    await appendJudgeVerdicts(dir, 3918, [], { complete: true, attemptId: "sa-1" });
    expect(await rows()).toEqual([]);
  });

  it("an interrupted run stamps the marker with a caller-supplied attempt id", async () => {
    await appendJudgeVerdicts(dir, 3918, [], { complete: false, attemptId: "wf-x" });
    expect((await rows())[0].attempt_id).toBe("wf-x");
  });

  it("counts one run emitted twice (running, then terminal) as a sole run", async () => {
    await appendJudgeVerdicts(
      dir,
      3918,
      [
        node({ kind: "run", nodeId: "run:a", parentId: null, runId: "a" }),
        node({ kind: "run", nodeId: "run:a", parentId: null, runId: "a" }),
      ],
      { complete: false }
    );
    expect((await rows())[0].attempt_id).toBe("a");
  });

  it("an interrupted stream with two run nodes writes the marker without attempt_id", async () => {
    await appendJudgeVerdicts(
      dir,
      3918,
      [
        node({ kind: "run", nodeId: "run:a", parentId: null, runId: "a" }),
        node({ kind: "run", nodeId: "run:b", parentId: null, runId: "b" }),
      ],
      { complete: false }
    );
    const written = await rows();
    expect(written).toHaveLength(1);
    expect(written[0]).not.toHaveProperty("attempt_id");
  });

  it("an interrupted run with a passing verdict still writes the failing marker", async () => {
    await appendJudgeVerdicts(dir, 3918, [...runNodes("run-1"), judgeNode("pass")], {
      complete: false,
    });
    const written = await rows();
    expect(written.map((r) => r.result)).toEqual(["fail", "pass"]);
    expect(String(written[0].error_summary).startsWith(JUDGES_INTERRUPTED_MARKER)).toBe(true);
    expect(written[0].attempt_id).toBe("run-1");
    expect(written[1]).not.toHaveProperty("attempt_id");
  });

  it("omits error_summary on a passing verdict with a rationale", async () => {
    await appendJudgeVerdicts(dir, 3918, [judgeNode("pass", { rationale: "all confirmed" })]);
    expect((await rows())[0].error_summary).toBeUndefined();
  });
});
