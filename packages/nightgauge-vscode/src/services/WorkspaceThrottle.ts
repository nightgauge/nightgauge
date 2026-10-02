/**
 * WorkspaceThrottle — the platform's workspace concurrency throttle, as this
 * agent applies it (#2337).
 *
 * An owner or admin caps how many runs a workspace executes at once,
 * optionally until `resumeAt`. The platform enforces the cap on the triggers
 * it dispatches, and tells the workspace's agent two ways:
 *   - a `throttle` command on the agent's command stream whenever the cap is
 *     set (`{action: "set", maxConcurrent, resumeAt}`) or cleared
 *     (`{action: "cleared", maxConcurrent: null, resumeAt: null}`);
 *   - the `throttle` field of every registration response, the strictest
 *     cap in force for the agent, or null, so that an agent that restarts
 *     re-learns a cap it had already acknowledged.
 *
 * Every window applies the cap to its own dispatch
 * (ConcurrentPipelineManager.setWorkspaceThrottle): no new slot opens above
 * min(configured max_concurrent, maxConcurrent) until the throttle lifts, and
 * a slot already running is never stopped. WorkspaceThrottleState keeps the
 * applied cap in the extension's global state, so a reload, which reuses the
 * stored registration, does not forget it; a persisted cap makes the next
 * activation register again to refresh it.
 *
 * @see ThrottleCommandHandler — applies and acknowledges the command
 */

import type * as vscode from "vscode";
import type { AgentRegistrationService } from "./AgentRegistrationService";
import type { ConcurrentPipelineManager } from "./ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";

/** A cap on concurrent runs, lifted at `resumeAt`; `null` there holds it until cleared. */
export interface WorkspaceThrottle {
  maxConcurrent: number;
  /** ISO-8601 time the throttle lifts, or null for a throttle with no end. */
  resumeAt: string | null;
}

/** A throttle command's payload: the throttle to apply (null clears), or why it is invalid. */
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
 * Parse a stored or reported throttle object (`{maxConcurrent, resumeAt}`,
 * as the registration response and the persisted state carry it). Returns
 * null for an explicit null, and undefined when the value is absent or is not
 * a valid throttle, so a caller never mistakes a malformed value for "no
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

/** The global-state key the applied throttle is kept under. */
export const WORKSPACE_THROTTLE_STATE_KEY = "nightgauge.workspaceThrottle";

/**
 * The throttle this window applies, kept across reloads (#2337). The agent
 * id is per machine, and every window of the machine receives the same
 * throttle, so the extension's global state is the right scope.
 */
export class WorkspaceThrottleState {
  constructor(
    private readonly target: Pick<ConcurrentPipelineManager, "setWorkspaceThrottle">,
    private readonly memento: Pick<vscode.Memento, "get" | "update">,
    private readonly logger: Logger,
    private readonly now: () => number = Date.now
  ) {}

  /**
   * Apply the throttle an earlier session kept, unless it has lifted since;
   * a lifted or unreadable one is dropped.
   */
  restore(): void {
    const stored = this.memento.get<unknown>(WORKSPACE_THROTTLE_STATE_KEY);
    if (stored === undefined) return;
    const throttle = parseThrottleValue(stored);
    if (!throttle || !throttleInForce(throttle, this.now())) {
      void this.forget();
      return;
    }
    this.logger.info("WorkspaceThrottleState: restoring the workspace throttle", { ...throttle });
    this.target.setWorkspaceThrottle(throttle);
  }

  /** Whether a throttle is kept: a reload should register again to refresh it. */
  hasPersisted(): boolean {
    return this.memento.get<unknown>(WORKSPACE_THROTTLE_STATE_KEY) !== undefined;
  }

  /**
   * Apply a throttle to dispatch and keep it; null clears both. Dispatch has
   * the throttle once this returns; keeping it is best effort, and a failure
   * to keep it is logged.
   */
  async apply(throttle: WorkspaceThrottle | null): Promise<void> {
    this.target.setWorkspaceThrottle(throttle);
    if (throttle === null || !throttleInForce(throttle, this.now())) {
      await this.forget();
      return;
    }
    await this.keep({ maxConcurrent: throttle.maxConcurrent, resumeAt: throttle.resumeAt });
  }

  private forget(): Promise<void> {
    return this.keep(undefined);
  }

  private async keep(value: WorkspaceThrottle | undefined): Promise<void> {
    try {
      await this.memento.update(WORKSPACE_THROTTLE_STATE_KEY, value);
    } catch (err) {
      this.logger.warn("WorkspaceThrottleState: could not keep the workspace throttle", {
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
}

/**
 * Apply the throttle a successful registration reported (#2337). The
 * registering agent becomes the one its workspace is linked to, so the
 * response is the cap in force for that workspace now, and null clears one
 * kept from before. A response with no valid throttle changes nothing.
 */
export async function applyRegistrationThrottle(
  registration: Pick<AgentRegistrationService, "getLastThrottle"> | null | undefined,
  state: Pick<WorkspaceThrottleState, "apply"> | null | undefined
): Promise<void> {
  const throttle = registration?.getLastThrottle();
  if (throttle === undefined || !state) return;
  await state.apply(throttle);
}
