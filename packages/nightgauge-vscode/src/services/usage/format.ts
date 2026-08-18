/**
 * Unit-aware formatting for `UsageWindow` figures — shared by every usage
 * surface (Issue #658 / #659 / #661).
 *
 * Lives beside the usage model rather than inside one of its consumers so the
 * status-bar meter and the dashboard panel print the same figure the same way.
 * It was originally private to `utils/statusBar.ts`; the dashboard panel
 * needed it too, and importing the status-bar module into a webview HTML
 * builder would have dragged that module's `vscode.ThemeColor` construction
 * along with it.
 *
 * Deliberately free of any `vscode` import — a pure string function, callable
 * from anywhere.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import type { UsageUnit } from "./types";

function trimTrailingZero(value: number): string {
  return value % 1 === 0 ? value.toFixed(0) : value.toFixed(1);
}

/** Format very large token counts compactly: "812k tokens" / "1.2m tokens". */
function formatTokenCount(value: number): string {
  const abs = Math.abs(value);
  if (abs >= 1_000_000) {
    return `${trimTrailingZero(value / 1_000_000)}m tokens`;
  }
  if (abs >= 1_000) {
    return `${trimTrailingZero(value / 1_000)}k tokens`;
  }
  return `${Math.round(value)} tokens`;
}

/**
 * Format a raw `used`/`limit` figure per its `UsageUnit`.
 *
 * `percent` is the vendor-reported-percentage case reserved by
 * docs/decisions/018-adapter-usage-quota-model.md (`rate_limit_event`'s
 * `utilization`) — no producer emits it yet, but the formatter honours it so
 * that provider needs no change here when it lands.
 */
export function formatUsageValue(value: number, unit: UsageUnit): string {
  switch (unit) {
    case "usd":
      return `$${value.toFixed(2)}`;
    case "percent":
      return `${Math.round(value)}%`;
    case "tokens":
      return formatTokenCount(value);
    case "requests": {
      const rounded = Math.round(value);
      return `${rounded.toLocaleString()} request${rounded === 1 ? "" : "s"}`;
    }
  }
}
