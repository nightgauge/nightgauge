/**
 * What, if anything, leaves this machine about the operator's AI-provider
 * allowance (Issue #736).
 *
 * The footer and the dashboard webview read `UsageSnapshot` locally and it
 * never travels. Some operators want the same picture in the hosted Nightgauge
 * dashboard, across every machine they run. This module is the single place
 * that decides what a report contains, so "what does Nightgauge send?" has one
 * answer that can be read in one file.
 *
 * ## Off is the default, and it is a real off
 *
 * `docs/ECOSYSTEM.md`: telemetry is disabled unless explicitly enabled. There
 * is no implicit reporting, no sampling, and no "anonymous" middle state. With
 * no configuration the return value here is `null` and `AgentHeartbeatService`
 * sends the bodiless PUT it always sent.
 *
 * Two independent switches must both permit a report:
 *
 * - `platform.telemetry.enabled` — the account-wide consent. An operator who
 *   opted out of telemetry has opted out of this too, whatever the tier says.
 *   Anything else would make a global "no" mean "no except this".
 * - `platform.telemetry.usage_reporting` — the tier, default `off`.
 *
 * ## The tiers
 *
 * | Tier      | What is sent                                                  |
 * | --------- | ------------------------------------------------------------- |
 * | `off`     | Nothing. No body at all.                                      |
 * | `minimal` | Allowance windows only — no monetary figure ever leaves.      |
 * | `full`    | Every window, including locally-derived per-adapter spend.     |
 *
 * `minimal` is defined by what it withholds — **money** — rather than by which
 * provider produced a window, because that is the property an operator is
 * actually deciding about. Today the split is exact: `usd` windows come from
 * `LocalTelemetryUsageProvider`'s rate-card reduction of this workspace's own
 * pipeline history, and `percent` windows are Claude's own statement of the
 * account's allowance. A future vendor-reported token allowance would be
 * included in `minimal`, which is correct — it is the vendor describing its own
 * ceiling, not a figure about what this operator has spent.
 *
 * ## The tier travels with the payload
 *
 * `level` is part of the report and is stored server-side. Without it a reader
 * cannot tell "this operator has no dollar spend" from "this operator does not
 * send dollar spend", and a dashboard that renders the first when it means the
 * second states something false. This is the same rule ADR 018 enforces
 * locally — absence must never be presented as zero.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #736 - Opt-in adapter usage reporting
 */

import { ConfigBridge } from "../ConfigBridge";
import type { UsageSnapshot, UsageWindow } from "./types";

/** Reporting tiers, in increasing order of what they disclose. */
export type UsageReportingLevel = "off" | "minimal" | "full";

/**
 * Server-side cap on `windows` (see the platform's `AdapterUsageSchema`).
 * Applied here too so an over-long report is trimmed with intent rather than
 * rejected wholesale by validation — a snapshot can only exceed this if a
 * future provider emits an unusual number of windows, and losing the tail is
 * better than losing the report.
 */
const MAX_REPORTED_WINDOWS = 20;

/** One window as it travels. Snake_case: this is the platform's vocabulary. */
export interface ReportedUsageWindow {
  id: string;
  label: string;
  scope: UsageWindow["scope"];
  used: number;
  limit: number | null;
  unit: UsageWindow["unit"];
  resets_at: string | null;
  confidence: UsageWindow["confidence"];
  observed_at: string | null;
}

/** The full report body, or nothing. */
export interface ReportedUsage {
  level: Exclude<UsageReportingLevel, "off">;
  adapter: string;
  plan: UsageSnapshot["plan"]["kind"];
  captured_at: string;
  windows: ReportedUsageWindow[];
}

/**
 * True when a window carries a monetary figure, and is therefore withheld at
 * the `minimal` tier.
 */
function isMonetary(window: UsageWindow): boolean {
  return window.unit === "usd";
}

function toReportedWindow(window: UsageWindow): ReportedUsageWindow {
  return {
    id: window.id,
    label: window.label,
    scope: window.scope,
    used: window.used,
    limit: window.limit,
    unit: window.unit,
    resets_at: window.resetsAt?.toISOString() ?? null,
    confidence: window.confidence,
    // Absent locally means "observed as the snapshot was derived"; the wire
    // has no absent, so that becomes an explicit null rather than a copy of
    // `captured_at` — which would claim an as-of the provider never stated.
    observed_at: window.observedAt?.toISOString() ?? null,
  };
}

/**
 * Build the report for a snapshot at a given tier, or `null` when nothing
 * should be sent.
 *
 * `null` is returned for `off` and for a disabled telemetry consent — the two
 * cases where the operator has said no. It is deliberately **not** returned for
 * a snapshot with no windows: an operator who turned reporting on and whose
 * adapter cannot be described is telling the dashboard something real ("this
 * machine is reporting, and there is nothing to report"), which a silent
 * absence could not distinguish from reporting being off.
 */
export function buildUsageReport(
  snapshot: UsageSnapshot,
  level: UsageReportingLevel
): ReportedUsage | null {
  if (level === "off") {
    return null;
  }
  const windows = (
    level === "minimal" ? snapshot.windows.filter((w) => !isMonetary(w)) : snapshot.windows
  )
    .slice(0, MAX_REPORTED_WINDOWS)
    .map(toReportedWindow);

  return {
    level,
    adapter: snapshot.adapter,
    plan: snapshot.plan.kind,
    captured_at: snapshot.capturedAt.toISOString(),
    windows,
  };
}

/**
 * Resolve the effective tier from the two switches.
 *
 * `telemetryEnabled` is the account-wide consent and is authoritative: `false`
 * forces `off`. `null` means the one-time consent prompt has not been answered
 * yet, which is not consent, so it also forces `off` — an unanswered question
 * must never be read as a yes.
 */
export function resolveUsageReportingLevel(
  configured: UsageReportingLevel | undefined,
  telemetryEnabled: boolean | null | undefined
): UsageReportingLevel {
  if (telemetryEnabled !== true) {
    return "off";
  }
  return configured ?? "off";
}

/**
 * The tier currently in force, read from the 6-tier merged config.
 *
 * Fails closed on every uncertainty: an uninitialised ConfigBridge, an absent
 * `platform.telemetry` block, and an unanswered consent prompt all resolve to
 * `off`. Reporting starts only when the configuration positively says so.
 */
export function getUsageReportingLevel(): UsageReportingLevel {
  const configBridge = ConfigBridge.getInstance();
  if (!configBridge.isInitialized()) {
    return "off";
  }
  const telemetry = configBridge.getPlatform()?.telemetry;
  return resolveUsageReportingLevel(telemetry?.usage_reporting, telemetry?.enabled);
}
