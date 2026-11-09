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

/**
 * `error_summary` prefix of the row an interrupted run writes when it stopped
 * before it returned, so the gate cannot read missing judges as passed.
 */
export const JUDGES_INTERRUPTED_MARKER = "[judges-interrupted]";

/**
 * `error_summary` prefix of the passing row a complete attempt with no judge
 * verdict writes over an earlier judges row, closing that attempt so an earlier
 * interrupted marker no longer reads as the latest attempt. It never closes
 * over a real judge failure.
 */
export const JUDGES_NONE_MARKER = "[judges-none]";

/**
 * `error_summary` prefix of the failing row a dropped judge attempt writes: a
 * verdict record failed validation, or a complete run whose spec declared
 * judges recorded no verdict. Without it the gate would read the attempt as
 * one that never had judges. Mirrors `state.JudgesDroppedMarker` (#2944).
 */
export const JUDGES_DROPPED_MARKER = "[judges-dropped]";

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
 * earlier failing attempt. Every interrupted run also writes one failing
 * {@link JUDGES_INTERRUPTED_MARKER} row, so judges that never ran are not read
 * as passed.
 * Its `attempt_id` is `options.attemptId` when the caller knows the attempt,
 * else the sole distinct `runId` among the stream's `run` nodes, so a later
 * complete attempt can supersede it; with none or several it is omitted and the
 * row trips the gate for good, which fails closed.
 *
 * A dropped attempt writes one failing {@link JUDGES_DROPPED_MARKER} row and
 * no verdict row: when a verdict record is invalid, and when a complete run
 * whose spec declared `declaredJudges > 0` judges recorded no verdict (in place
 * of the `[judges-none]` close). With `declaredJudges` absent or `0`, nothing
 * changes (#2944).
 *
 * @param cwd         Any path inside the checkout whose gate-metrics to write.
 * @param issueNumber Issue the run belongs to.
 * @param nodes       The run's node stream, `run` and `phase` nodes included.
 */
export async function appendJudgeVerdicts(
  cwd: string,
  issueNumber: number,
  nodes: ReadonlyArray<WorkflowNode | { kind: string }>,
  options: { complete?: boolean; attemptId?: string; declaredJudges?: number } = {}
): Promise<void> {
  try {
    const stream = nodes as ReadonlyArray<WorkflowNode>;
    // An interrupted run's judges the abort cut off carry status "failed"
    // without having judged anything; the marker already records them, and as
    // attempt-less fail rows they would trip the gate for good (#2880).
    const verdicts = stream
      .filter(isJudgeVerdict)
      .filter((judge) => options.complete !== false || judge.status !== "failed")
      .sort((a, b) => Number(a.verdict === "pass") - Number(b.verdict === "pass"));
    // An interrupted run's judges are a partial set: whatever finished, the
    // marker says the rest never ran, so its passes cannot pass the gate.
    if (options.complete === false) {
      await appendMarker(
        cwd,
        issueNumber,
        stream,
        options.attemptId,
        `${JUDGES_INTERRUPTED_MARKER} feature-validate workflow stopped before every judge returned a verdict`
      );
    }
    if (verdicts.length === 0) {
      const declared = options.declaredJudges ?? 0;
      if (options.complete !== false && declared > 0) {
        await appendMarker(
          cwd,
          issueNumber,
          stream,
          options.attemptId,
          `${JUDGES_DROPPED_MARKER} feature-validate workflow declared ${declared} judge(s) but recorded no verdict`
        );
        return;
      }
      if (options.complete !== false && options.attemptId) {
        await appendNoJudgesRow(cwd, issueNumber, options.attemptId);
      }
      return;
    }

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
        // No verdict row, but the drop itself is recorded so the gate cannot
        // read the attempt as one that never had judges (#2944).
        const issue = validation.error.issues[0];
        const detail = issue ? `${issue.path.join(".") || "record"}: ${issue.message}` : "invalid";
        await appendMarker(
          cwd,
          issueNumber,
          stream,
          attemptId,
          `${JUDGES_DROPPED_MARKER} feature-validate invalid judge verdict record: ${detail.slice(0, 300)}`
        );
        return;
      }
      lines.push(JSON.stringify(record) + "\n");
    }

    await appendLines(cwd, lines);
  } catch (error) {
    console.warn(`[Nightgauge] Failed to write judge verdicts: ${error}`);
  }
}

/**
 * Write one failing marker row (interrupted or dropped attempt). Its
 * `attempt_id` is the known attempt, else the stream's sole run id, else none.
 */
async function appendMarker(
  cwd: string,
  issueNumber: number,
  stream: ReadonlyArray<WorkflowNode>,
  knownAttemptId: string | undefined,
  errorSummary: string
): Promise<void> {
  // A run node is emitted once per state change, so count distinct ids.
  const runIds = new Set(stream.filter(isWorkflowRun).map((run) => run.runId));
  const [onlyRunId] = runIds;
  const inferred = runIds.size === 1 && onlyRunId ? onlyRunId : undefined;
  const record: JudgeGateRecord = {
    schema_version: "1",
    timestamp: new Date().toISOString(),
    issue_number: issueNumber,
    gate_name: JUDGE_GATE_NAME,
    result: "fail",
    error_summary: errorSummary,
    attempt_id: knownAttemptId || inferred,
  };
  const validation = JudgeGateRecordSchema.safeParse(record);
  if (!validation.success) {
    console.warn(
      `[Nightgauge] Invalid judges marker record, not written: ${validation.error.message}`
    );
    return;
  }
  await appendLines(cwd, [JSON.stringify(record) + "\n"]);
}

/**
 * Close a complete attempt that judged nothing, but only over an earlier judges
 * row for the issue: an issue never judged needs no row, and the gate already
 * reads its absence as "no judges". It never closes over a real failure: when
 * the latest judges attempt (as the Go gate reads it) holds a failing row other
 * than an interrupted or dropped marker, that failure stands and nothing is written.
 */
async function appendNoJudgesRow(
  cwd: string,
  issueNumber: number,
  attemptId: string
): Promise<void> {
  let existing: string;
  try {
    existing = await fs.readFile(gateMetricsFilePath(cwd), "utf-8");
  } catch {
    return;
  }
  const rows = existing.split("\n").flatMap((line) => {
    try {
      const row = JSON.parse(line) as Partial<JudgeGateRecord>;
      return row.gate_name === JUDGE_GATE_NAME && row.issue_number === issueNumber ? [row] : [];
    } catch {
      return [];
    }
  });
  if (rows.length === 0) return;
  // The latest attempt that actually judged: markers are skipped, so a marker
  // that followed a real failing attempt cannot hide it. That attempt, plus
  // every attempt-less row (with no attempt id, every row), must hold no real
  // failure.
  const isMarker = (row: Partial<JudgeGateRecord>): boolean =>
    (row.error_summary ?? "").startsWith(JUDGES_INTERRUPTED_MARKER) ||
    (row.error_summary ?? "").startsWith(JUDGES_DROPPED_MARKER);
  const judgedRows = rows.filter((row) => !isMarker(row));
  const latest = judgedRows[judgedRows.length - 1]?.attempt_id;
  const considered = latest
    ? judgedRows.filter((row) => !row.attempt_id || row.attempt_id === latest)
    : judgedRows;
  const realFailure = considered.some((row) => row.result !== "pass");
  if (realFailure) return;
  const record: JudgeGateRecord = {
    schema_version: "1",
    timestamp: new Date().toISOString(),
    issue_number: issueNumber,
    gate_name: JUDGE_GATE_NAME,
    result: "pass",
    error_summary: `${JUDGES_NONE_MARKER} feature-validate attempt completed with no judge verdict`,
    attempt_id: attemptId,
  };
  if (!JudgeGateRecordSchema.safeParse(record).success) return;
  await appendLines(cwd, [JSON.stringify(record) + "\n"]);
}

/** Append `lines` to the checkout's gate-metrics file in one write. */
async function appendLines(cwd: string, lines: string[]): Promise<void> {
  const filePath = gateMetricsFilePath(cwd);
  await fs.mkdir(path.dirname(filePath), { recursive: true });
  await fs.appendFile(filePath, lines.join(""), "utf-8");
}
