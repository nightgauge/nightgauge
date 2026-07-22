/**
 * Auto-resume autonomous when a self-clearing pause condition recovers.
 *
 * Background (#3307):
 *   The rate-limit circuit breaker pauses autonomous when GitHub returns 429.
 *   The pause is correct — burning $ on doomed calls is worse — but it is also
 *   a deadlock without a recovery path: paused autonomous never starts new
 *   pipelines, so `preCheckAuth` (the only call that fires `noteRateLimitOk`)
 *   never runs, so the breaker never re-arms and autonomous stays paused even
 *   after GitHub's quota resets.
 *
 *   The stall watchdog DOES keep polling while autonomous is paused (it is
 *   driven by its own `setTimeout`, not the dispatch loop). When its sweeps
 *   start succeeding again, it knows the underlying outage has cleared. That
 *   is the right moment to:
 *     1. Re-arm the rate-limit breaker so the next 429 trips it again.
 *     2. Resume autonomous IF the pause was triggered by a self-clearing
 *        reason (rate-limit / network-outage). User pauses and lifetime-
 *        failure-cap pauses must NEVER auto-resume — those require manual
 *        triage.
 *
 *   Best-effort: IPC errors are swallowed; the watchdog must continue cycling
 *   regardless.
 */

import { IpcClient } from "../services/IpcClient";
import type { Logger } from "./logger";

/**
 * Pause reasons that indicate a transient external condition the system can
 * detect as recovered automatically. ANY other `pauseTriggeredBy` value means
 * the pause requires manual intervention and must NOT auto-resume.
 */
const SELF_CLEARING_PAUSE_REASONS = new Set<string>([
  "rate-limit-circuit-breaker",
  "network-outage-circuit-breaker",
]);

/**
 * If autonomous is paused with a self-clearing reason, resume it. Returns true
 * iff a resume was actually issued. Never throws.
 */
export async function autoResumeAfterRecovery(logger: Logger): Promise<boolean> {
  try {
    const ipc = IpcClient.getInstance();
    const status = await ipc.autonomousStatus();
    if (status.status !== "paused") return false;
    const triggeredBy = status.pauseTriggeredBy ?? "";
    if (!SELF_CLEARING_PAUSE_REASONS.has(triggeredBy)) return false;

    logger.info("Auto-resuming autonomous after transient pause cleared", {
      pauseReason: status.pauseReason,
      pauseTriggeredBy: triggeredBy,
    });
    await ipc.autonomousResume();
    return true;
  } catch (err) {
    logger.warn("Auto-resume after recovery failed (best-effort)", {
      error: err instanceof Error ? err.message : String(err),
    });
    return false;
  }
}

/** Test-only — expose the allowlist so tests can assert which reasons qualify. */
export function _selfClearingReasonsForTests(): ReadonlySet<string> {
  return SELF_CLEARING_PAUSE_REASONS;
}
