/**
 * platformResult.ts — Shared discriminated result type for platform IPC calls.
 *
 * All six Platform*Service classes (PlatformAnalyticsHealthService,
 * PlatformComplianceService, PlatformCostService, PlatformRunsService,
 * PlatformTrendsService, PlatformQuotaService) used to swallow their IPC
 * errors with a bare `catch {}` and return `null` (or a silently stale
 * cache) — the caller could not tell a 401 from a 500, an unreachable Go
 * backend, a missing platform credential, or a genuinely empty account.
 *
 * `PlatformResult<T>` replaces that: services return this instead of
 * `T | null`, callers never need try/catch (the no-throw contract is
 * preserved), and the failure branch carries everything the Go layer told us
 * — kind, HTTP status where one exists, the endpoint, and the raw message.
 *
 * @see Issue 743 — six platform services discard the real error
 */

import type { Logger } from "../utils/logger";

/** The five failure buckets a platform IPC call can land in. */
export type PlatformFailureKind =
  | "unauthorized" // HTTP 401 — credential rejected
  | "forbidden" // HTTP 403 — credential valid, access denied
  | "server_error" // any other bad HTTP response, or an unclassified error
  | "offline" // the Go backend / network path is unreachable
  | "not_configured"; // no platform credential/service wired up at all

/** A failed platform IPC call. Never thrown — always returned. */
export interface PlatformFailure {
  ok: false;
  kind: PlatformFailureKind;
  /** HTTP status code, when the Go layer's error text carried one. */
  status?: number;
  /** The IPC method that failed, e.g. "platform.getAnalyticsHealth". */
  endpoint: string;
  /** The raw error text (Go error string, or the local IPC failure). */
  message: string;
}

/** A successful platform IPC call. */
export interface PlatformSuccess<T> {
  ok: true;
  value: T;
}

/** What every Platform*Service `fetchAndCache`-style method returns. */
export type PlatformResult<T> = PlatformSuccess<T> | PlatformFailure;

/** Wrap a value as a successful PlatformResult. */
export function platformOk<T>(value: T): PlatformSuccess<T> {
  return { ok: true, value };
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------
//
// The Go layer is the only source of truth we get over IPC (see
// internal/platform/analytics.go, internal/platform/compliance.go,
// internal/ipc/server.go). It returns text like:
//   "get analytics health: server returned 401"
//   "analytics service unavailable"          (no platform credential)
//   "platform client not configured"         (usage summary, same cause)
// and the local TS IPC transport (IpcClientBase) rejects with text like:
//   "IPC error <code>: <go message>"
//   "Go backend exited with code 1"
//   "IPC request platform.getCostAnalytics timed out after 30000ms"
//   "IPC client disposed"
// This module parses that text into a PlatformFailureKind instead of
// dropping it.

// "server returned 401" (cost/health/runs/trends/compliance) and
// "unexpected response 401" (usage summary — GetAnalyticsDashboard's own
// phrasing, see internal/platform/analytics.go GetUsageSummary) are the two
// status-carrying shapes the Go layer emits for these six endpoints.
const STATUS_RE = /(?:server returned|unexpected (?:response|status))\s*(\d+)/i;

const NOT_CONFIGURED_RE = /not configured|service unavailable/i;

const OFFLINE_RE =
  /timed out|dial tcp|econnrefused|enotfound|etimedout|network is unreachable|go backend exited|failed to write to go backend|ipc client disposed/i;

/**
 * Classify a caught IPC/HTTP error into a PlatformFailure for `endpoint`.
 * Never throws — an error that matches nothing recognizable still becomes a
 * `server_error` PlatformFailure rather than propagating.
 */
export function classifyPlatformError(err: unknown, endpoint: string): PlatformFailure {
  const message = err instanceof Error ? err.message : String(err);

  const statusMatch = message.match(STATUS_RE);
  const status = statusMatch ? Number(statusMatch[1]) : undefined;

  let kind: PlatformFailureKind;
  if (status === 401) {
    kind = "unauthorized";
  } else if (status === 403) {
    kind = "forbidden";
  } else if (status !== undefined) {
    kind = "server_error";
  } else if (NOT_CONFIGURED_RE.test(message)) {
    kind = "not_configured";
  } else if (OFFLINE_RE.test(message)) {
    kind = "offline";
  } else {
    kind = "server_error";
  }

  return { ok: false, kind, status, endpoint, message };
}

/**
 * Log a platform failure exactly once at the service boundary — with
 * endpoint and status, per the acceptance criteria — then return it
 * unchanged so callers can chain this straight into a `catch` block:
 *
 *   } catch (err) {
 *     return reportPlatformFailure(this.logger, err, "platform.getX");
 *   }
 */
export function reportPlatformFailure(
  logger: Pick<Logger, "warn">,
  err: unknown,
  endpoint: string
): PlatformFailure {
  const failure = classifyPlatformError(err, endpoint);
  logger.warn(`platform request failed: ${endpoint}`, {
    kind: failure.kind,
    status: failure.status,
    message: failure.message,
  });
  return failure;
}
