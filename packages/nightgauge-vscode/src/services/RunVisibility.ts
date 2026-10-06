/**
 * RunVisibility — the private-run choice (#2400).
 *
 * When this window is signed in to the hosted service, a member's runs are
 * readable there by their team by default. Every entry point that starts a
 * run asks here whether to keep that run private: readable on the hosted
 * service by the member alone. The choice is off by default for every run
 * and is never remembered, so it cannot carry over from an earlier run.
 *
 * Signed out, nothing reaches the hosted service, so no choice is offered and
 * every run is team-visible.
 *
 * The hosted service enforces the rule. The window marks a run private only
 * when the service confirms it ({@link confirmPrivateRun}); otherwise it says
 * so and shows no private badge.
 *
 * @see docs/TELEMETRY_PRIVACY.md § Private runs
 */

import * as vscode from "vscode";
import { IpcClient } from "./IpcClient";

/** Who reads a run on the hosted service. `team` is the default. */
export type RunVisibility = "team" | "private";

/** The outcome of confirming a private run with the hosted service. */
export type PrivateConfirmation = "confirmed" | "unconfirmed";

/** What private hides, what it does not, and what owners and admins see. */
export const PRIVATE_RUN_DETAIL =
  "Only you can read this run on the hosted service. Owners and admins still see " +
  "that it exists and what it cost, never its content. Work on GitHub (branches, " +
  "pull requests, issues, comments, board moves) follows the repository's permissions.";

const TEAM_RUN_DETAIL = "Your team can read this run on the hosted service.";

interface VisibilityPickItem extends vscode.QuickPickItem {
  visibility: RunVisibility;
}

/** True when this window holds a hosted-service session. */
export async function isSignedInToHostedService(): Promise<boolean> {
  // Loaded on demand: the run services import this module, and the token
  // store's secret-storage graph belongs only to the sign-in check.
  const { TokenStorage } = await import("../platform/TokenStorage");
  const storage = TokenStorage.getInstance();
  if (!storage) return false;
  try {
    return (await storage.retrieve("accessToken")) !== null;
  } catch {
    return false;
  }
}

/**
 * Ask who reads the run about to start. Resolves `"team"` without asking when
 * the window is signed out, the chosen visibility otherwise, and `undefined`
 * when the member dismissed the choice, which cancels the start.
 *
 * Team is listed first and is the active item, so the choice defaults to off;
 * nothing is stored, so the next run asks again from the same default.
 */
export async function chooseRunVisibility(
  signedIn: () => Promise<boolean> = isSignedInToHostedService
): Promise<RunVisibility | undefined> {
  if (!(await signedIn())) return "team";
  const items: VisibilityPickItem[] = [
    {
      label: "$(organization) Team",
      description: "default",
      detail: TEAM_RUN_DETAIL,
      visibility: "team",
    },
    {
      label: "$(lock) Private",
      detail: PRIVATE_RUN_DETAIL,
      visibility: "private",
    },
  ];
  const picked = await vscode.window.showQuickPick(items, {
    title: "Nightgauge: Who can read this run?",
    placeHolder: "Team (default) or private",
    ignoreFocusOut: false,
  });
  return picked?.visibility;
}

/** The `visibility` a queue request carries: only private names one. */
export function visibilityOption(visibility: RunVisibility | undefined): {
  visibility?: "private";
} {
  return visibility === "private" ? { visibility: "private" } : {};
}

/**
 * Enqueue options with the run's visibility added (#2400): `options`
 * unchanged for a team run, so a team start sends exactly what it sent
 * before; a private run adds `visibility: "private"`.
 */
export function withVisibility<T extends object>(
  options: T | undefined,
  visibility: RunVisibility | undefined
): (T & { visibility?: "private" }) | undefined {
  return visibility === "private" ? { ...(options ?? ({} as T)), visibility: "private" } : options;
}

/** The read {@link confirmPrivateRun} asks of the daemon. */
export type RunVisibilityReader = (
  issueNumber: number,
  runId: string
) => Promise<{ found: boolean; visibility?: string }>;

/**
 * How long to wait before each read. The run's first stage event reaches the
 * service a few seconds after the slot opens, and a fresh worktree can take
 * a minute or more to prepare, so the reads are spread over several minutes.
 */
export const CONFIRM_DELAYS_MS: readonly number[] = [
  5_000, 10_000, 20_000, 30_000, 60_000, 120_000,
];

/**
 * Confirm with the hosted service that run `runId` of issue `issueNumber` is
 * private. Resolves "confirmed" only when the service answers for that run
 * with `visibility: "private"`. An answer for the run with any other value is
 * "unconfirmed" at once; no answer for the run (it has not reached the
 * service yet, or a read failed) is retried after each delay, and
 * "unconfirmed" when the delays run out. `cancelled` stops early, also
 * "unconfirmed".
 */
export async function confirmPrivateRun(
  read: RunVisibilityReader,
  issueNumber: number,
  runId: string,
  options: {
    delaysMs?: readonly number[];
    sleep?: (ms: number) => Promise<void>;
    cancelled?: () => boolean;
  } = {}
): Promise<PrivateConfirmation> {
  const delays = options.delaysMs ?? CONFIRM_DELAYS_MS;
  const sleep = options.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  for (const delay of delays) {
    await sleep(delay);
    if (options.cancelled?.()) return "unconfirmed";
    let answer: { found: boolean; visibility?: string };
    try {
      answer = await read(issueNumber, runId);
    } catch {
      continue;
    }
    if (answer.found) {
      return answer.visibility === "private" ? "confirmed" : "unconfirmed";
    }
  }
  return "unconfirmed";
}

/** Tell the member a run they started private is not marked private. */
export function showPrivateNotConfirmedNotice(issueNumber: number, repo?: string): void {
  const name = repo ? `${repo}#${issueNumber}` : `#${issueNumber}`;
  void vscode.window.showWarningMessage(
    `Nightgauge: the hosted service did not confirm run ${name} as private, so it is not ` +
      "marked private. Treat it as visible to your team there."
  );
}

/**
 * Confirm a run started private and act on the answer (#2400): record it on
 * the run's state, which shows the private badge only on "confirmed", and
 * tell the member when the service did not confirm it.
 */
export async function confirmAndReportPrivateRun(
  target: { setPrivateConfirmation(outcome: PrivateConfirmation): void },
  issueNumber: number,
  runId: string,
  repo?: string,
  read: RunVisibilityReader = (issue, id) =>
    IpcClient.getInstance().platformGetRunVisibility(issue, id),
  options?: Parameters<typeof confirmPrivateRun>[3]
): Promise<PrivateConfirmation> {
  const outcome = await confirmPrivateRun(read, issueNumber, runId, options);
  target.setPrivateConfirmation(outcome);
  if (outcome !== "confirmed") showPrivateNotConfirmedNotice(issueNumber, repo);
  return outcome;
}
