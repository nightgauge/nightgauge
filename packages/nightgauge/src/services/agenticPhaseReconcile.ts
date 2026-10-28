/**
 * agenticPhaseReconcile.ts — replay the agentic-stage evidence reconciler on
 * the extension's legacy route (#1887).
 *
 * WHY THIS EXISTS. feature-planning, feature-dev and feature-validate report
 * phases only when the model narrates a marker, which it does in a minority of
 * runs. The Go scheduler fixes that after the stage passes its gate: it maps
 * each registry phase to the artifact that proves it and upgrades the
 * `unreported` back-fill (`scheduler.reconcileAgenticPhases`). The extension
 * also runs these stages WITHOUT the scheduler, through
 * `HeadlessOrchestrator.runPipeline`, and that route kept showing about 4/14.
 * Two routes to one stage, one instrumented and one not, is #1397's drift.
 *
 * WHY A CLI VERB AND NOT A PORT. The claim rules live once, in Go
 * (`internal/orchestrator/agentic_phases.go`). This module only runs
 * `nightgauge phase reconcile`, validates its `PhaseTransition[]` output and
 * hands each verdict to the state service, which upgrades only an `unreported`
 * record. A TypeScript copy of the rules would be a second implementation of a
 * classification rule.
 *
 * FAILS SOFT. Phase reporting must never fail the stage it reports on: a
 * missing binary, a verb error or malformed output is logged and leaves the
 * back-fill exactly as it was.
 */
import { execFile } from "child_process";
import { promisify } from "util";
import type { PrStagePhaseTransition } from "./prStagePhaseStream";

const execFileAsync = promisify(execFile);

/** The stages `nightgauge phase reconcile` has rules for. */
export const AGENTIC_RECONCILE_STAGES: readonly string[] = [
  "feature-planning",
  "feature-dev",
  "feature-validate",
];

export function isAgenticReconcileStage(stage: string): boolean {
  return AGENTIC_RECONCILE_STAGES.includes(stage);
}

export interface AgenticReconcileRequest {
  stage: string;
  issueNumber: number;
  /** When the stage started. Artifacts older than this do not count. */
  since: Date;
  /** The stage's exit code. */
  exitCode: number;
  /** Whether the stage's post-condition gate positively passed. */
  gatePassed: boolean;
  /** The stage workspace: the same directory its gate verified. */
  workdir: string;
}

export interface AgenticReconcileDeps {
  resolveBinary: () => Promise<string | null>;
  /** Record one proven outcome. Resolves true when it changed the record. */
  observe: (t: PrStagePhaseTransition & { status: "complete" | "skipped" }) => Promise<boolean>;
  warn: (message: string, meta?: Record<string, unknown>) => void;
  /** Run the binary and return its stdout. Defaults to execFile. */
  run?: (binary: string, args: string[], cwd: string) => Promise<string>;
}

export function buildPhaseReconcileArgs(req: AgenticReconcileRequest): string[] {
  return [
    "phase",
    "reconcile",
    "--stage",
    req.stage,
    "--issue",
    String(req.issueNumber),
    "--since",
    req.since.toISOString(),
    "--exit-code",
    String(req.exitCode),
    `--gate-passed=${req.gatePassed}`,
    "--workdir",
    req.workdir,
  ];
}

/**
 * Parse the verb's stdout, or `undefined` when it is not a `PhaseTransition[]`.
 * One malformed element rejects the whole output: a contract change is not a
 * state to guess at.
 */
export function parsePhaseReconcileOutput(stdout: string): PrStagePhaseTransition[] | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(stdout);
  } catch {
    return undefined;
  }
  if (!Array.isArray(parsed)) return undefined;
  const out: PrStagePhaseTransition[] = [];
  for (const item of parsed) {
    if (typeof item !== "object" || item === null) return undefined;
    const t = item as Record<string, unknown>;
    if (
      typeof t.stage !== "string" ||
      typeof t.name !== "string" ||
      typeof t.status !== "string" ||
      typeof t.index !== "number" ||
      typeof t.total !== "number"
    ) {
      return undefined;
    }
    out.push({ stage: t.stage, name: t.name, index: t.index, total: t.total, status: t.status });
  }
  return out;
}

async function defaultRun(binary: string, args: string[], cwd: string): Promise<string> {
  const { stdout } = await execFileAsync(binary, args, {
    encoding: "utf-8",
    cwd,
    timeout: 60_000,
  });
  return stdout;
}

/**
 * Run the reconciler for one agentic stage and record its verdicts. Returns
 * how many phases it changed.
 *
 * It does not spawn unless the stage exited 0 and its gate passed: the verb
 * claims nothing otherwise, so the spawn would only cost time.
 */
export async function reconcileAgenticStagePhases(
  req: AgenticReconcileRequest,
  deps: AgenticReconcileDeps
): Promise<number> {
  if (!isAgenticReconcileStage(req.stage) || req.exitCode !== 0 || !req.gatePassed) {
    return 0;
  }
  const meta = { stage: req.stage, issueNumber: req.issueNumber };

  let binary: string | null;
  try {
    binary = await deps.resolveBinary();
  } catch (err) {
    deps.warn("Phase reconcile: binary unresolved — phases stay as reported", {
      ...meta,
      err: err instanceof Error ? err.message : String(err),
    });
    return 0;
  }
  if (!binary) {
    deps.warn("Phase reconcile: binary unresolved — phases stay as reported", meta);
    return 0;
  }

  let stdout: string;
  try {
    stdout = await (deps.run ?? defaultRun)(binary, buildPhaseReconcileArgs(req), req.workdir);
  } catch (err) {
    deps.warn("Phase reconcile: verb failed — phases stay as reported", {
      ...meta,
      err: err instanceof Error ? err.message : String(err),
    });
    return 0;
  }

  const transitions = parsePhaseReconcileOutput(stdout);
  if (!transitions) {
    deps.warn("Phase reconcile: output is not a PhaseTransition[] — phases stay as reported", meta);
    return 0;
  }

  let changed = 0;
  for (const t of transitions) {
    if (t.stage !== req.stage) continue;
    if (t.status !== "complete" && t.status !== "skipped") {
      deps.warn("Phase reconcile reported an unknown phase status", { ...meta, t });
      continue;
    }
    try {
      if (await deps.observe({ ...t, status: t.status })) changed++;
    } catch (err) {
      deps.warn("Phase reconcile: failed to record a phase", {
        ...meta,
        phase: t.name,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
  return changed;
}
