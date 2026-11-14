/**
 * Window diagnostics for the VSCode host smoke launcher (#2747).
 *
 * The demo window failed twice under load: once when the extension host exited
 * at startup (`Canceled`), and once when the dashboard never answered
 * `activateTab`. Neither failure could be diagnosed. The gate kept no VS Code
 * logs and recorded no load, and every run left its scratch directory behind.
 * These helpers close those gaps. They run in plain Node, beside
 * `acquireVSCode.ts`, so vitest can cover them (`tests/vscode-host/` holds no
 * `*.test.ts`).
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

/**
 * Launch attempts per window. Only a launch fault is retried: a window that
 * never reached the in-host module has run no test. The same principle is
 * settled in `acquireVSCode.ts`.
 */
export const LAUNCH_ATTEMPTS = 2;

/**
 * What became of one window launch.
 * - `passed`: the transcript exists and runTests resolved.
 * - `test-failed`: the transcript exists, so the in-host module ran, but a test failed.
 * - `launch-fault`: no transcript, so VS Code never reached the in-host module.
 */
export type WindowOutcome = "passed" | "test-failed" | "launch-fault";

/** The machine's load average and core count, printed before each launch. */
export function loadAverageLine(
  loadavg: () => number[] = os.loadavg,
  cores: () => number = () => os.cpus().length
): string {
  const [one, five, fifteen] = loadavg().map((n) => n.toFixed(2));
  return `load avg ${one} ${five} ${fifteen} (1/5/15 min) on ${cores()} cores`;
}

/**
 * The transcript decides the outcome. If it is absent, the in-host module
 * never ran, whatever runTests said. A rejection after the module ran is a
 * failed test.
 */
export function classifyWindowOutcome(input: {
  transcriptExists: boolean;
  runTestsError: unknown;
}): WindowOutcome {
  if (!input.transcriptExists) return "launch-fault";
  return input.runTestsError === undefined ? "passed" : "test-failed";
}

/**
 * Retry only a launch fault, and only before the last attempt. A failed test
 * is never retried, because a retry would turn a red assertion into a green run.
 * `attempt` is 1-based.
 */
export function shouldRetryLaunch(outcome: WindowOutcome, attempt: number): boolean {
  return outcome === "launch-fault" && attempt < LAUNCH_ATTEMPTS;
}

/**
 * Where a failed window's logs go. Under `scripts/ci-local.sh`, `run_step`
 * exports the step's log path as `CI_LOCAL_STEP_LOG`. The logs then land
 * beside it, at `<step>.vscode-logs/`, which the gate keeps. When the variable
 * is unset (ci.yml, or a bare `npm run test:host`), they stay in scratch,
 * which is kept on a failure.
 */
export function diagnosticsDir(env: NodeJS.ProcessEnv, scratch: string): string {
  const stepLog = env.CI_LOCAL_STEP_LOG;
  if (stepLog) {
    return `${stepLog.replace(/\.log$/, "")}.vscode-logs`;
  }
  return path.join(scratch, "vscode-logs");
}

/**
 * Copy `<userDataDir>/logs` to `<destDir>/<label>`. Returns the destination,
 * or `undefined` when VS Code wrote no logs (it died before creating them).
 */
export function copyWindowLogs(
  userDataDir: string,
  label: string,
  destDir: string
): string | undefined {
  const source = path.join(userDataDir, "logs");
  if (!fs.existsSync(source)) return undefined;
  const dest = path.join(destDir, label);
  // A previous run's logs under the same label would read as this run's.
  fs.rmSync(dest, { recursive: true, force: true });
  fs.mkdirSync(dest, { recursive: true });
  fs.cpSync(source, dest, { recursive: true });
  return dest;
}

/** Scratch is evidence only when something failed. A passing run removes it. */
export function shouldRemoveScratch(exitCode: number): boolean {
  return exitCode === 0;
}
