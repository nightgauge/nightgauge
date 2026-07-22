/**
 * Network-outage circuit breaker — aborts active LLM pipeline stages when
 * GitHub becomes unreachable for an extended period.
 *
 * Background (#3296):
 *   When `api.github.com` becomes unreachable mid-pipeline (DNS failure, ISP
 *   outage, GitHub status incident), an active LLM stage keeps consuming
 *   tokens for hours — retrying gh API calls inside the stage and waiting
 *   on Anthropic stream responses — until Anthropic's stream-idle-timeout
 *   fires. Issue #3216 burned $20.87 on a single pr-merge stage in a 2.5-hour
 *   DNS outage on a PR that was already created and CI-green (#3295).
 *
 *   The autonomous stall watchdog already detects these outages within 60s
 *   (consecutiveFailures climbing on board.list calls), but never aborts the
 *   active stage subprocess. This helper closes that gap:
 *
 *   1. Classify each watchdog error as `connectivity` (DNS/ECONNREFUSED/
 *      ENOTFOUND/ETIMEDOUT/network unreachable) vs other failure modes —
 *      distinct from rate-limit, which is its own breaker.
 *   2. Track consecutive *connectivity* failures separately from the broader
 *      stall-watchdog failure counter.
 *   3. Once threshold is crossed (default 3 consecutive — about 1.5–2 minutes
 *      given the watchdog's existing back-off curve), call the Go IPC method
 *      `pipeline.cancelActiveForNetworkOutage`. The Go scheduler walks every
 *      active stage context and cancels it with cause ErrNetworkUnavailable,
 *      causing the Claude CLI subprocess to exit immediately. The pipeline's
 *      failure handler classifies as terminal_failure_kind="network_unavailable"
 *      and skips auto-retro / calibration update.
 *   4. Reset the counter on the first successful watchdog sweep so the breaker
 *      auto-arms for the next outage.
 *
 *   Best-effort: a transient IPC failure must NEVER block the original error
 *   path. Pipeline error handling continues to drive the underlying state.
 */

import { IpcClient } from "../services/IpcClient";
import type { Logger } from "./logger";

/**
 * Error patterns that indicate transport-level connectivity loss (vs rate
 * limit, vs auth, vs other). Matches messages observed in production logs:
 *   - "dial tcp: lookup api.github.com: no such host"
 *   - "fetch board items (filtered): Post ... dial tcp: ... no such host"
 *   - "getaddrinfo ENOTFOUND"
 *   - "fetch failed" (undici TypeError when DNS fails inside Node)
 *   - "ECONNREFUSED", "ECONNRESET", "ETIMEDOUT"
 *   - "network is unreachable", "no internet", "offline"
 *   - "request board.list timed out after 30000ms" (IPC timeout when backend
 *     itself can't reach api.github.com)
 *   - "error connecting to api.github.com" (gh CLI message)
 */
const CONNECTIVITY_PATTERNS: RegExp[] = [
  /no such host/i,
  /\bENOTFOUND\b/,
  /\bgetaddrinfo\b/i,
  /\bECONNREFUSED\b/,
  /\bECONNRESET\b/,
  /\bETIMEDOUT\b/,
  /\bENETUNREACH\b/,
  /\bENETDOWN\b/,
  /\bEHOSTUNREACH\b/,
  /network\s+is\s+unreachable/i,
  /error\s+connecting\s+to\s+api\.github\.com/i,
  /\bfetch\s+failed\b/i,
  /timed\s+out\s+after\s+\d+ms/i,
];

/** Default threshold — 3 consecutive failures ≈ 1.5–2 min on the watchdog cadence. */
export const DEFAULT_CONNECTIVITY_THRESHOLD = 3;

let consecutiveConnectivityFailures = 0;
let breakerTripped = false;
let lastTripAt = 0;
let lastCancelledIssues: number[] = [];

/** Heuristic detector — true when the error message indicates transport-level failure. */
export function isConnectivityError(input: unknown): boolean {
  if (!input) return false;
  const text = input instanceof Error ? `${input.message}\n${input.stack ?? ""}` : String(input);
  for (const pattern of CONNECTIVITY_PATTERNS) {
    if (pattern.test(text)) return true;
  }
  return false;
}

/**
 * Record one watchdog observation. Pass the error if the sweep failed; pass
 * `null` if it succeeded. The function distinguishes connectivity failures
 * from other errors and trips the breaker when consecutive connectivity
 * failures cross the threshold.
 *
 * Returns `{ tripped: true }` when this call caused the breaker to trip
 * (so callers can log and surface). Subsequent calls during the same outage
 * return `{ tripped: false, alreadyTripped: true }`.
 */
export async function observeWatchdogResult(
  err: unknown,
  logger: Logger,
  options: { threshold?: number; source: string } = { source: "autonomous stall watchdog" }
): Promise<{
  tripped: boolean;
  alreadyTripped?: boolean;
  classified?: "connectivity" | "other";
  consecutiveFailures?: number;
}> {
  const threshold = options.threshold ?? DEFAULT_CONNECTIVITY_THRESHOLD;

  // Sweep succeeded — reset the counter and clear the breaker.
  if (err === null) {
    if (consecutiveConnectivityFailures > 0 || breakerTripped) {
      logger.info("Network connectivity restored — clearing outage breaker", {
        source: options.source,
        priorConsecutiveFailures: consecutiveConnectivityFailures,
        wasTripped: breakerTripped,
      });
    }
    consecutiveConnectivityFailures = 0;
    breakerTripped = false;
    return { tripped: false };
  }

  // Sweep failed — is it connectivity or something else?
  if (!isConnectivityError(err)) {
    // Non-connectivity error (rate limit, auth, schema, etc.) — leave the
    // connectivity counter unchanged. This intentionally does NOT reset:
    // a transient classification miss in the middle of an outage shouldn't
    // un-stick the counter. The first true success will reset it.
    return { tripped: false, classified: "other" };
  }

  consecutiveConnectivityFailures += 1;

  if (consecutiveConnectivityFailures < threshold) {
    return {
      tripped: false,
      classified: "connectivity",
      consecutiveFailures: consecutiveConnectivityFailures,
    };
  }

  if (breakerTripped) {
    return {
      tripped: false,
      alreadyTripped: true,
      classified: "connectivity",
      consecutiveFailures: consecutiveConnectivityFailures,
    };
  }

  // Cross the threshold — trip the breaker.
  breakerTripped = true;
  lastTripAt = Date.now();

  logger.warn("Network outage circuit breaker tripping — aborting active LLM stages", {
    source: options.source,
    consecutiveFailures: consecutiveConnectivityFailures,
    threshold,
  });

  try {
    const ipc = IpcClient.getInstance();
    const result = await ipc.pipelineCancelActiveForNetworkOutage();
    lastCancelledIssues = result?.cancelledIssues ?? [];
    if (lastCancelledIssues.length > 0) {
      logger.warn("Network outage breaker: cancelled active pipeline stages", {
        cancelledIssues: lastCancelledIssues,
      });
    } else {
      logger.info("Network outage breaker: no active stages to cancel (pipelines may be idle)");
    }
  } catch (cancelErr) {
    logger.warn("Network outage breaker: IPC cancel call failed (best-effort)", {
      error: cancelErr instanceof Error ? cancelErr.message : String(cancelErr),
    });
  }

  return {
    tripped: true,
    classified: "connectivity",
    consecutiveFailures: consecutiveConnectivityFailures,
  };
}

/** Last trip timestamp (epoch ms), or 0 if never. */
export function getLastTripAt(): number {
  return lastTripAt;
}

/** Issues whose stages were cancelled on the most recent trip. */
export function getLastCancelledIssues(): number[] {
  return [...lastCancelledIssues];
}

/** Test-only — reset module state between tests. */
export function _resetForTests(): void {
  consecutiveConnectivityFailures = 0;
  breakerTripped = false;
  lastTripAt = 0;
  lastCancelledIssues = [];
}

/** Test-only — observe internal state. */
export function _stateForTests(): {
  consecutiveFailures: number;
  tripped: boolean;
} {
  return {
    consecutiveFailures: consecutiveConnectivityFailures,
    tripped: breakerTripped,
  };
}
