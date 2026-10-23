/**
 * The GitHub API spend meter (Issue #1347).
 *
 * The status bar already showed GraphQL points REMAINING, read from the
 * response headers of whatever board call happened last. That number says how
 * close the workspace is to the cliff and nothing at all about what is driving
 * it towards it — so an operator watching it drain had no way to act, and
 * every exhaustion this workspace has hit was reconstructed after the fact
 * from a code audit rather than from a measurement.
 *
 * The missing half is the RATE and its attribution, and both are already
 * computed by the Go binary from the request ledger (#843), which #1347 turns
 * on by default. This service reads that answer rather than reimplementing it:
 * a second aggregator in TypeScript would drift from the Go one, and the two
 * surfaces would then disagree about what the workspace is spending — which is
 * the failure the ledger exists to end, reintroduced one layer up.
 *
 * Cost: one local file read every five minutes. No GitHub API traffic, which
 * matters more than usual here — a meter that spent quota to report on quota
 * would be its own top caller.
 */

import { execFile } from "child_process";
import { promisify } from "util";

const execFileAsync = promisify(execFile);

/** How far back the meter aggregates. One hour is GitHub's own budget period. */
export const API_BUDGET_WINDOW = "1h";

/**
 * How often the meter refreshes. Five minutes: the underlying quota window is
 * an hour, so a tighter cadence spends process spawns to redraw a number that
 * has barely moved.
 */
export const API_BUDGET_REFRESH_MS = 5 * 60 * 1000;

/** GitHub's hourly GraphQL point budget for a user token. */
export const GRAPHQL_HOURLY_LIMIT = 5000;

export interface ApiBudgetReading {
  /** GraphQL points spent in the last hour. */
  points: number;
  /** Requests made in the last hour. */
  calls: number;
  /** The caller with the largest share, or null when nothing was attributed. */
  topCaller: string | null;
  /** The top caller's points. */
  topCallerPoints: number;
}

interface ApiUsageJson {
  records?: number;
  points?: number;
  by?: string;
  groups?: Array<{ key?: string; points?: number; calls?: number }>;
}

export interface ApiBudgetDeps {
  /** Absolute path to the nightgauge binary. */
  binaryPath: string;
  /** Workspace root — the ledger is per-workspace, so this is the cwd. */
  cwd: string;
  /** Seam for tests. */
  run?: (binaryPath: string, args: string[], cwd: string) => Promise<{ stdout: string }>;
}

async function defaultRun(binaryPath: string, args: string[], cwd: string) {
  return execFileAsync(binaryPath, args, {
    cwd,
    timeout: 10_000,
    maxBuffer: 4 * 1024 * 1024,
  });
}

/**
 * Read the last hour of GitHub API spend.
 *
 * Returns null for every "cannot tell" case rather than throwing or
 * substituting zero. Zero would be a lie an operator acts on — it renders as a
 * quiet workspace, which is exactly the reading a broken meter must never
 * produce. A null hides the meter instead, so an absent number is visibly
 * absent.
 */
export async function readApiBudget(deps: ApiBudgetDeps): Promise<ApiBudgetReading | null> {
  const run = deps.run ?? defaultRun;
  let stdout: string;
  try {
    ({ stdout } = await run(
      deps.binaryPath,
      [
        "api-usage",
        "--since",
        API_BUDGET_WINDOW,
        "--by",
        "caller",
        // GraphQL only. The pools have separate hourly quotas, so the
        // unfiltered total is not a budget — a live window measured 70 GraphQL
        // points and reported 2216 once REST traffic on `core` was added in,
        // most of it another process's spending that derived cost cannot tell
        // apart from ours. Rendering that against the 5000-point GraphQL quota
        // would put a number 30x too large in the status bar.
        "--resource",
        "graphql",
        "--json",
      ],
      deps.cwd
    ));
  } catch {
    // No binary, no ledger, an unreadable workspace — all "cannot tell".
    return null;
  }

  let parsed: ApiUsageJson;
  try {
    parsed = JSON.parse(stdout) as ApiUsageJson;
  } catch {
    // `api-usage` prints a plain-text line when the window is empty, which is
    // a legitimate state and not a parse failure worth reporting: zero spend
    // in the last hour is a real, reportable reading.
    if (stdout.includes("no ledger records")) {
      return { points: 0, calls: 0, topCaller: null, topCallerPoints: 0 };
    }
    return null;
  }

  if (typeof parsed.points !== "number") {
    return null;
  }
  const groups = Array.isArray(parsed.groups) ? parsed.groups : [];
  // Groups arrive most-expensive first, but a caller that cost nothing is not
  // a spender — naming it would put the one row that cannot be the answer in
  // front of the operator.
  const top = groups.find((g) => (g.points ?? 0) > 0);
  return {
    points: parsed.points,
    calls: groups.reduce((n, g) => n + (g.calls ?? 0), 0),
    topCaller: top?.key ?? null,
    topCallerPoints: top?.points ?? 0,
  };
}
