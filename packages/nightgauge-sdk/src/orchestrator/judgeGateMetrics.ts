/**
 * judgeGateMetrics — folds an orchestrated feature-validate run's adversarial
 * judge verdicts into the checkout's gate-metrics file.
 *
 * Lives next to the only producer of judge nodes (`runStageWorkflow` in
 * {@link PipelineOrchestrator}), so the rows reach the Go `FeatureValidateGate`
 * whenever feature-validate fans out (#2880). The record shape is the
 * extension's `GateMetricRecordSchema` and the Go reader's
 * (`internal/state/gate_metrics_reader.go`).
 *
 * @see Issue #2778 - one attempt per run, written whole or not at all
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { z } from "zod";

import { checkoutPath } from "../context/cloneLayout.js";
import { isJudgeVerdict, isWorkflowRun, type WorkflowNode } from "../cli/workflow/WorkflowEvent.js";

/** The gate metrics file's name inside the checkout's `health/` entry. */
export const GATE_METRICS_FILE = "gate-metrics.jsonl";

/**
 * Canonical gate_name for judge verdicts. Mirrors `state.JudgeGateName`, which
 * reserves the name in the Go writer (#2778).
 */
export const JUDGE_GATE_NAME = "judges" as const;

/** One judge row, as the gate-metrics readers accept it. */
const JudgeGateRecordSchema = z.object({
  schema_version: z.literal("1"),
  timestamp: z.string(),
  issue_number: z.number().int().positive(),
  gate_name: z.literal(JUDGE_GATE_NAME),
  result: z.enum(["pass", "fail"]),
  error_summary: z.string().optional(),
  attempt_id: z.string().min(1).optional(),
});

type JudgeGateRecord = z.infer<typeof JudgeGateRecordSchema>;

/** The gate-metrics file for the checkout that owns `cwd`. */
export function gateMetricsFilePath(cwd: string): string {
  return path.join(checkoutPath("health", cwd), GATE_METRICS_FILE);
}

/**
 * Append the judge verdicts of `nodes` as `{ gate_name: "judges", result }`
 * records.
 *
 * Fail-closed: only an explicit `"pass"` verdict yields `result: "pass"`;
 * `"fail"` and `"uncertain"` yield `"fail"`, so the Go gate (which trips on any
 * `result != "pass"` in the latest attempt) cannot be passed by an unconfirmed
 * claim.
 *
 * `attempt_id` is the `runId` of the run that owns the judges, reached through
 * each judge's phase, because the shared stream also carries the pipeline's
 * own `run` node. With no owning run, or judges of several runs, the field is
 * omitted: such rows are never superseded, which fails closed (#2746, #2778).
 *
 * One invalid record writes nothing; the rest go out in a single append with
 * failures first, so neither a dropped row nor a torn write can leave a passing
 * remainder without its failures. A failing record carries the first line of
 * the judge's rationale as `error_summary`. Never throws.
 *
 * Pass `complete: false` when the run was interrupted (aborted, timed out or
 * threw) before it returned. Its judges are then written without `attempt_id`:
 * a partial set whose finished judges all passed must never supersede an
 * earlier failing attempt.
 *
 * @param cwd         Any path inside the checkout whose gate-metrics to write.
 * @param issueNumber Issue the run belongs to.
 * @param nodes       The run's node stream, `run` and `phase` nodes included.
 */
export async function appendJudgeVerdicts(
  cwd: string,
  issueNumber: number,
  nodes: ReadonlyArray<WorkflowNode | { kind: string }>,
  options: { complete?: boolean } = {}
): Promise<void> {
  try {
    const stream = nodes as ReadonlyArray<WorkflowNode>;
    const verdicts = stream
      .filter(isJudgeVerdict)
      .sort((a, b) => Number(a.verdict === "pass") - Number(b.verdict === "pass"));
    if (verdicts.length === 0) return;

    const byId = new Map(stream.map((node) => [node.nodeId, node]));
    const runIds = new Set(
      verdicts.map((judge) => {
        const phase = byId.get(judge.parentId ?? "");
        const run = phase && byId.get(phase.parentId ?? "");
        return run && isWorkflowRun(run) ? run.runId : undefined;
      })
    );
    const [runId] = runIds;
    const attemptId = options.complete !== false && runIds.size === 1 && runId ? runId : undefined;

    const lines: string[] = [];
    for (const judge of verdicts) {
      const passed = judge.verdict === "pass";
      const record: JudgeGateRecord = {
        schema_version: "1",
        timestamp: judge.ts,
        issue_number: issueNumber,
        gate_name: JUDGE_GATE_NAME,
        result: passed ? "pass" : "fail",
        error_summary:
          !passed && judge.rationale ? judge.rationale.split("\n")[0].slice(0, 500) : undefined,
        attempt_id: attemptId,
      };
      const validation = JudgeGateRecordSchema.safeParse(record);
      if (!validation.success) {
        console.warn(
          `[Nightgauge] Invalid judge verdict record, dropping the attempt: ${validation.error.message}`
        );
        return;
      }
      lines.push(JSON.stringify(record) + "\n");
    }

    const filePath = gateMetricsFilePath(cwd);
    await fs.mkdir(path.dirname(filePath), { recursive: true });
    await fs.appendFile(filePath, lines.join(""), "utf-8");
  } catch (error) {
    console.warn(`[Nightgauge] Failed to write judge verdicts: ${error}`);
  }
}
