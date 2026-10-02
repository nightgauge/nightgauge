/**
 * WorkspaceThrottle — the platform's workspace concurrency throttle, as this
 * window applies it (#2337).
 *
 * An owner or admin caps how many runs a workspace executes at once,
 * optionally until `resumeAt`. The platform enforces the cap on the triggers
 * it dispatches, keeps it on the workspace, and publishes a `throttle`
 * command to the agent the workspace is linked to whenever it is set or
 * cleared.
 *
 * The command does not name the workspace, and every window of the machine
 * shares one agent while each serves its own workspace. So a window never
 * applies a command's payload: the command, a (re)connected command stream
 * and every authenticated session event make it read the throttle of its own
 * workspace, by the manifest's slug, from the platform's workspace list
 * (WorkspaceThrottleSync). A window whose manifest names no workspace has no
 * throttle.
 *
 * The window applies the cap to its own dispatch
 * (ConcurrentPipelineManager.setWorkspaceThrottle): no new slot opens above
 * min(configured max_concurrent, maxConcurrent) until the throttle lifts, and
 * a slot already running is never stopped. WorkspaceThrottleState keeps the
 * applied cap in the window's workspace state, so a reload holds dispatch at
 * once, before the first read; the cap is kept and applied only while a
 * platform session exists, and signing out lifts and forgets it.
 *
 * @see WorkspaceThrottleSync — reads the throttle and follows the session
 * @see ThrottleCommandHandler — answers the `throttle` command
 */

import type * as vscode from "vscode";
import type { ConcurrentPipelineManager } from "./ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";

/** A cap on concurrent runs, lifted at `resumeAt`; `null` there holds it until cleared. */
export interface WorkspaceThrottle {
  maxConcurrent: number;
  /** ISO-8601 time the throttle lifts, or null for a throttle with no end. */
  resumeAt: string | null;
}

/** A throttle command's payload: the throttle it announces (null clears), or why it is invalid. */
export type ParsedThrottleCommand = { throttle: WorkspaceThrottle | null } | { invalid: string };

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Validate a cap and its end; returns the reason it is invalid, or null. */
function invalidThrottle(maxConcurrent: unknown, resumeAt: unknown): string | null {
  if (typeof maxConcurrent !== "number" || !Number.isInteger(maxConcurrent) || maxConcurrent < 0) {
    return "maxConcurrent must be a non-negative integer";
  }
  if (resumeAt !== null && resumeAt !== undefined) {
    if (typeof resumeAt !== "string" || !Number.isFinite(Date.parse(resumeAt))) {
      return "resumeAt must be an ISO-8601 time or null";
    }
  }
  return null;
}

/** Parse the payload of a platform `throttle` command. */
export function parseThrottleCommand(payload: unknown): ParsedThrottleCommand {
  if (!isObject(payload)) return { invalid: "payload must be an object" };
  if (payload.action === "cleared") return { throttle: null };
  if (payload.action !== "set") return { invalid: 'action must be "set" or "cleared"' };
  const invalid = invalidThrottle(payload.maxConcurrent, payload.resumeAt);
  if (invalid) return { invalid };
  return {
    throttle: {
      maxConcurrent: payload.maxConcurrent as number,
      resumeAt: (payload.resumeAt as string | null | undefined) ?? null,
    },
  };
}

/**
 * Parse a reported or stored throttle object (`{maxConcurrent, resumeAt}`, as
 * the platform's workspace list and the kept state carry it). Returns null
 * for an explicit null, and undefined when the value is absent or is not a
 * valid throttle, so a caller never mistakes a malformed value for "no
 * throttle".
 */
export function parseThrottleValue(value: unknown): WorkspaceThrottle | null | undefined {
  if (value === null) return null;
  if (!isObject(value) || invalidThrottle(value.maxConcurrent, value.resumeAt) !== null) {
    return undefined;
  }
  return {
    maxConcurrent: value.maxConcurrent as number,
    resumeAt: (value.resumeAt as string | null | undefined) ?? null,
  };
}

/** Whether a throttle is still in force at `now` (ms since the epoch). */
export function throttleInForce(throttle: WorkspaceThrottle, now: number): boolean {
  return throttle.resumeAt === null || now < Date.parse(throttle.resumeAt);
}

/**
 * The throttle in words, for the queue view and the Resume Queue message:
 * "1 run at once until <local time>", or "... until it is cleared".
 */
export function describeWorkspaceThrottle(throttle: WorkspaceThrottle): string {
  const runs = `${throttle.maxConcurrent} ${throttle.maxConcurrent === 1 ? "run" : "runs"} at once`;
  return throttle.resumeAt === null
    ? `${runs} until it is cleared`
    : `${runs} until ${new Date(throttle.resumeAt).toLocaleString()}`;
}

/** The workspace-state key the applied throttle is kept under. */
export const WORKSPACE_THROTTLE_STATE_KEY = "nightgauge.workspaceThrottle";

/** The kept value: the throttle, and the workspace (manifest slug) it belongs to. */
interface KeptThrottle extends WorkspaceThrottle {
  slug: string;
}

/**
 * The throttle this window applies, kept across reloads (#2337). It is kept
 * in the window's workspace state, with the slug of the platform workspace
 * it belongs to, and restored only for that slug: a window serving another
 * workspace, or the same folder after its manifest names another workspace,
 * never starts from it.
 */
export class WorkspaceThrottleState {
  constructor(
    private readonly target: Pick<ConcurrentPipelineManager, "setWorkspaceThrottle">,
    private readonly memento: Pick<vscode.Memento, "get" | "update">,
    private readonly logger: Logger,
    private readonly now: () => number = Date.now
  ) {}

  /**
   * Apply the throttle an earlier session kept for workspace `slug`, unless
   * it has lifted since. A lifted, unreadable or other workspace's one is
   * dropped.
   */
  restore(slug: string | null): void {
    const stored = this.memento.get<unknown>(WORKSPACE_THROTTLE_STATE_KEY);
    if (stored === undefined) return;
    const throttle = parseThrottleValue(stored);
    const keptFor = isObject(stored) && typeof stored.slug === "string" ? stored.slug : null;
    if (
      !throttle ||
      keptFor === null ||
      keptFor !== slug ||
      !throttleInForce(throttle, this.now())
    ) {
      void this.keep(undefined);
      return;
    }
    this.logger.info("WorkspaceThrottleState: restoring the workspace throttle", {
      slug,
      ...throttle,
    });
    this.target.setWorkspaceThrottle(throttle);
  }

  /**
   * Apply workspace `slug`'s throttle to dispatch and keep it; null clears
   * both. Dispatch has the throttle once this returns; keeping it is best
   * effort, and a failure to keep it is logged.
   */
  async apply(slug: string | null, throttle: WorkspaceThrottle | null): Promise<void> {
    this.target.setWorkspaceThrottle(throttle);
    if (slug === null || throttle === null || !throttleInForce(throttle, this.now())) {
      await this.keep(undefined);
      return;
    }
    await this.keep({ slug, maxConcurrent: throttle.maxConcurrent, resumeAt: throttle.resumeAt });
  }

  /** Lift the cap and forget the kept throttle: the window has no platform session. */
  async clear(): Promise<void> {
    this.target.setWorkspaceThrottle(null);
    await this.keep(undefined);
  }

  private async keep(value: KeptThrottle | undefined): Promise<void> {
    try {
      await this.memento.update(WORKSPACE_THROTTLE_STATE_KEY, value);
    } catch (err) {
      this.logger.warn("WorkspaceThrottleState: could not keep the workspace throttle", {
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
}
