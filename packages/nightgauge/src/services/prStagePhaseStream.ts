/**
 * prStagePhaseStream.ts — read the deterministic pr-stage CLI's live phase
 * transitions (#1397).
 *
 * WHY THIS EXISTS. #1247 gave the deterministic pr-merge / pr-create runners
 * real phase reporting, but only the Go scheduler attaches a reporter.
 * HeadlessOrchestrator reaches the SAME runners a second way — it shells out to
 * `nightgauge pr-stage create|merge --json` in a separate process — and that
 * route reported nothing, so it showed 0/14 for the whole stage. Two routes to
 * one stage, one instrumented and one not, and the uninstrumented one is the
 * route an ordinary VS Code user takes.
 *
 * WHY A STREAM AND NOT JUST THE RESULT ARRAY. The CLI's JSON result carries the
 * full `phases` array, which is enough to rebuild the durable record — but not
 * to move a counter. The deterministic pr-merge waits out in-flight CI on a
 * 30s x 30 budget, so a consumer that only reads the result sits at 0/14 for up
 * to fifteen minutes and then jumps to the end. That is the reported symptom,
 * not a fix for it. So the CLI also writes each transition to stderr the moment
 * it happens, and this module turns those lines back into events.
 *
 * WHY STDERR. stdout carries exactly one thing — the stage's JSON result — and
 * that contract predates this and is what the caller's parser depends on. The
 * live channel therefore goes to stderr, sentinel-prefixed so it is separable
 * from ordinary log output.
 */
import { spawn } from "child_process";

/**
 * Marks a live phase-transition line on the CLI's stderr.
 *
 * MUST equal `PhaseStreamPrefix` in internal/orchestrator/phase_recorder.go.
 * That is a literal in two languages, which is the dual-path-drift class this
 * repo keeps getting bitten by, so it is pinned: TestPhaseStreamPrefixParity
 * WithTypeScript reads this file and fails on drift.
 */
export const PHASE_STREAM_PREFIX = "@@nightgauge-phase@@";

/** One transition, matching orchestrator.PhaseTransition's JSON tags. */
export interface PrStagePhaseTransition {
  stage: string;
  name: string;
  index: number;
  total: number;
  /** "running", then one of "complete" | "failed" | "skipped". */
  status: string;
}

/**
 * Parse one stderr line into a transition, or `undefined` when the line is not
 * a phase event (ordinary log output) or is malformed.
 *
 * Malformed input returns `undefined` rather than throwing: this runs on a
 * progress channel, and a bad line must never take down the merge that was
 * reporting progress.
 */
export function parsePhaseStreamLine(line: string): PrStagePhaseTransition | undefined {
  const trimmed = line.trim();
  if (!trimmed.startsWith(PHASE_STREAM_PREFIX)) return undefined;

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed.slice(PHASE_STREAM_PREFIX.length));
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null) return undefined;

  const t = parsed as Record<string, unknown>;
  if (
    typeof t.stage !== "string" ||
    typeof t.name !== "string" ||
    typeof t.status !== "string" ||
    typeof t.index !== "number" ||
    typeof t.total !== "number"
  ) {
    return undefined;
  }
  return { stage: t.stage, name: t.name, index: t.index, total: t.total, status: t.status };
}

/**
 * Split a stream of chunks into complete lines, holding any partial trailing
 * line until the rest of it arrives.
 *
 * A chunk boundary can fall mid-line, so parsing each chunk independently drops
 * transitions at random — the failure would be intermittent and look like the
 * bug this module fixes.
 */
export function createLineSplitter(onLine: (line: string) => void): {
  push(chunk: string): void;
  flush(): void;
} {
  let buffer = "";
  return {
    push(chunk: string) {
      buffer += chunk;
      let nl = buffer.indexOf("\n");
      while (nl !== -1) {
        onLine(buffer.slice(0, nl));
        buffer = buffer.slice(nl + 1);
        nl = buffer.indexOf("\n");
      }
    },
    flush() {
      if (buffer.length > 0) {
        onLine(buffer);
        buffer = "";
      }
    },
  };
}

export interface PhaseStreamRunOptions {
  cwd: string;
  timeoutMs: number;
  /** Called for each transition as it arrives — this is the live half. */
  onPhase: (t: PrStagePhaseTransition) => void;
  /** Called for stderr lines that are not phase events. */
  onLog?: (line: string) => void;
}

export interface PhaseStreamRunResult {
  stdout: string;
}

/**
 * Run the CLI, returning its stdout while delivering phase transitions live.
 *
 * Rejects on a non-zero exit, a spawn failure, or the timeout — matching what
 * `promisify(execFile)` did before, so the caller's existing "runner errored →
 * fall through to the LLM path" branch is unchanged.
 */
export function runWithPhaseStream(
  binary: string,
  args: string[],
  opts: PhaseStreamRunOptions
): Promise<PhaseStreamRunResult> {
  return new Promise<PhaseStreamRunResult>((resolve, reject) => {
    const child = spawn(binary, args, { cwd: opts.cwd });

    let stdout = "";
    let settled = false;

    const stderrLines = createLineSplitter((line) => {
      const transition = parsePhaseStreamLine(line);
      if (transition) {
        // A throwing consumer must not kill the run it is reporting on.
        try {
          opts.onPhase(transition);
        } catch {
          /* progress is best-effort */
        }
        return;
      }
      if (line.trim().length > 0) opts.onLog?.(line);
    });

    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      child.kill("SIGKILL");
      reject(new Error(`pr-stage timed out after ${opts.timeoutMs}ms`));
    }, opts.timeoutMs);

    const finish = (err?: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      stderrLines.flush();
      if (err) reject(err);
      else resolve({ stdout });
    };

    child.stdout?.setEncoding("utf-8");
    child.stdout?.on("data", (c: string) => {
      stdout += c;
    });
    child.stderr?.setEncoding("utf-8");
    child.stderr?.on("data", (c: string) => stderrLines.push(c));

    child.on("error", (err) => finish(err));
    child.on("close", (code) => {
      if (code === 0) finish();
      else finish(new Error(`pr-stage exited with code ${code}`));
    });
  });
}
