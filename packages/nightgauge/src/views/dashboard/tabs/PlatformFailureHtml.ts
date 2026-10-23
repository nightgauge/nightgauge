/**
 * PlatformFailureHtml — Shared rendering for a classified `PlatformFailure` (#748).
 *
 * Every platform-backed tab (Health, Trends, Cost, Compliance, Runs, plus the
 * Audit tab's retention/integrity panel) used to collapse *any* failed fetch
 * into one hardcoded, kind-specific message — most damagingly, Compliance
 * telling a rejected-credential failure that it needed to "Contact your team
 * owner to request access, or upgrade your plan." That is a claim the code
 * never verified.
 *
 * This module renders copy strictly from the `PlatformFailureKind` the
 * service actually reported:
 *   - `unauthorized` → the credential was rejected; offer sign-in/reconnect.
 *   - `forbidden`    → the ONLY kind allowed to mention a role or plan, and
 *                       only by quoting what the platform actually said.
 *   - `server_error` → the platform errored; retry is meaningful.
 *   - `offline`      → the backend/network path is unreachable.
 *   - `not_configured` → no platform account is wired up at all.
 *   - anything else  → a neutral message naming the endpoint and status.
 *                       Never invent a reason (this is also the fallback for
 *                       a `PlatformFailure` this module doesn't recognize —
 *                       kept as a real runtime branch, not just a type-level
 *                       exhaustiveness check, so a future kind degrades
 *                       safely instead of throwing).
 *
 * @see Issue #748 — accurate platform error states
 * @see ../../../services/platformResult.ts — the classified failure this renders
 */

import { escapeHtml } from "../DashboardComponents";
import type { PlatformFailure } from "../../../services/platformResult";

export interface RenderedPlatformFailure {
  icon: string;
  title: string;
  /** Already HTML-escaped — safe to interpolate directly. */
  hintHtml: string;
  showSignIn: boolean;
  showRetry: boolean;
}

function statusLabel(status: number | undefined): string {
  return status !== undefined ? `HTTP ${status}` : "an error";
}

/**
 * Render the copy for a classified platform failure. Every branch derives
 * its hint from `failure.status` / `failure.message` — nothing here asserts
 * a cause the failure object doesn't carry.
 */
export function renderPlatformFailure(failure: PlatformFailure): RenderedPlatformFailure {
  switch (failure.kind) {
    case "unauthorized":
      return {
        icon: "🔑",
        title: "Sign-in required",
        hintHtml: `Your platform credential was rejected (${escapeHtml(
          statusLabel(failure.status)
        )}). Sign in again to reconnect your account.`,
        showSignIn: true,
        showRetry: false,
      };
    case "forbidden":
      // The only kind allowed to mention a role or plan — and only because
      // the platform's own response is what we're quoting.
      return {
        icon: "🚫",
        title: "Access denied",
        hintHtml: `The platform denied this request (${escapeHtml(
          statusLabel(failure.status)
        )}): ${escapeHtml(failure.message)}. This may require a different role or plan — contact your team owner if you believe this is wrong.`,
        showSignIn: false,
        showRetry: true,
      };
    case "bad_request":
      // Deliberately offers no retry: the request itself is malformed, so the
      // same button would fail identically every time. Naming the endpoint and
      // quoting the platform's own message is what actually gets this fixed.
      return {
        icon: "⚠️",
        title: "Request rejected",
        hintHtml: `The platform rejected this request (${escapeHtml(
          statusLabel(failure.status)
        )}) for <code>${escapeHtml(failure.endpoint)}</code>: ${escapeHtml(
          failure.message
        )}. This is a bug in Nightgauge rather than a temporary outage — retrying will not help. Please report it with the endpoint above.`,
        showSignIn: false,
        showRetry: false,
      };
    case "server_error":
      return {
        icon: "⚠️",
        title: "Platform error",
        hintHtml: `The platform returned an error (${escapeHtml(
          statusLabel(failure.status)
        )}) for <code>${escapeHtml(failure.endpoint)}</code>. This may be transient — retry.`,
        showSignIn: false,
        showRetry: true,
      };
    case "offline":
      return {
        icon: "📡",
        title: "Platform unreachable",
        hintHtml:
          "Nightgauge could not reach the platform backend. Check your connection and retry.",
        showSignIn: false,
        showRetry: true,
      };
    case "not_configured":
      return {
        icon: "🔌",
        title: "Not connected",
        hintHtml: "No platform account is connected. Sign in to enable this feature.",
        showSignIn: true,
        showRetry: false,
      };
    default:
      // Unrecognized kind — never invent a reason. Name what we know
      // (endpoint + status) and stop there.
      return {
        icon: "❓",
        title: "Unable to load data",
        hintHtml: `<code>${escapeHtml(failure.endpoint)}</code> failed${
          failure.status !== undefined ? ` (HTTP ${failure.status})` : ""
        }. ${escapeHtml(failure.message)}`,
        showSignIn: false,
        showRetry: true,
      };
  }
}

/**
 * A retry button that re-posts `retryMessage` to the extension host (e.g. the
 * tab's own `{ type: "runsRefresh" }`) via the shared delegated listener from
 * `getPlatformFailureScript()`. Reuses each tab's existing refresh message —
 * this does not add a new retry mechanism, only a button that reaches the one
 * that already exists.
 */
export function getPlatformRetryButtonHtml(
  id: string,
  retryMessage: Record<string, unknown>,
  label = "Retry"
): string {
  const messageJson = escapeHtml(JSON.stringify(retryMessage));
  return `<button class="action-btn" id="${escapeHtml(id)}" data-action="platform-retry" data-retry-message="${messageJson}">${escapeHtml(label)}</button>`;
}

/** A sign-in button that posts the existing `signInWithPlatform` message (already handled in Dashboard.ts). */
export function getPlatformSignInButtonHtml(id: string, label = "Sign in to Nightgauge"): string {
  return `<button class="action-btn" id="${escapeHtml(id)}" data-action="platform-sign-in">${escapeHtml(label)}</button>`;
}

/**
 * Delegated click handler for `data-action="platform-retry"` /
 * `data-action="platform-sign-in"` buttons rendered by this module. Each tab
 * script includes this once inside its own panel's click listener.
 */
export function getPlatformFailureScript(): string {
  return `
        var platformRetryBtn = e.target.closest('[data-action="platform-retry"]');
        if (platformRetryBtn) {
          var retryMessage = platformRetryBtn.getAttribute('data-retry-message');
          if (retryMessage) {
            try { vscode.postMessage(JSON.parse(retryMessage)); } catch (err) { /* malformed — no-op */ }
          }
          return;
        }
        var platformSignInBtn = e.target.closest('[data-action="platform-sign-in"]');
        if (platformSignInBtn) {
          vscode.postMessage({ type: 'signInWithPlatform' });
          return;
        }
  `;
}
