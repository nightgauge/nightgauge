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
 * ## Full is the default, and off is one switch away
 *
 * Reporting is opt-out (#738): with no configuration this returns a `full`
 * report. That default is defensible here for a reason worth stating plainly —
 * an adapter usage report goes to the operator's **own account dashboard**, so
 * they can see their allowance across the machines they run. It is the
 * multi-machine view of their own data, not product analytics sent to a vendor.
 *
 * Four switches can each veto a report:
 *
 * - `telemetry.telemetryLevel` — VSCode's own kill switch. A platform contract,
 *   not a Nightgauge preference: an operator who set it to `"off"` has told
 *   VSCode that no extension sends anything, and honouring it is unconditional.
 * - `nightgauge.telemetry.enabled` — the VSCode-side consent, written by the
 *   first-run notice and the Telemetry Settings panel.
 * - `platform.telemetry.enabled` — the YAML-side consent, which is what the CLI
 *   and the Go scheduler read.
 * - `platform.telemetry.usage_reporting` — the tier, default `full`.
 *
 * The first two are resolved by `TelemetryConsentService.isEnabled()` and
 * arrive here as a single boolean; the last two are read from the merged
 * config. That split is deliberate — one component owns what consent means, and
 * this one owns what a report contains.
 *
 * The two consent stores are separate keys and neither can speak for the other,
 * so both are checked. Reading only the YAML one produced an off switch that
 * did not switch anything off: the notice's "Turn off" writes the VSCode
 * setting, and usage would have kept flowing.
 *
 * Note the asymmetry: absent config means yes, but an explicit `false` always
 * means no. Changing a default is not the same act as overriding an answer, and
 * this module never does the second.
 *
 * ## The tiers
 *
 * | Tier                 | What is sent                                       |
 * | -------------------- | -------------------------------------------------- |
 * | `off`                | Nothing. No body at all.                           |
 * | `minimal`            | Allowance windows only — no monetary figure leaves. |
 * | `full` (**default**) | Every window, including per-adapter spend.          |
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
 * @see Issue #736 - Tiered adapter usage reporting
 * @see Issue #738 - Telemetry is opt-out
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
  /** `null` when the window exists but nothing has been observed for it (#808). */
  used: number | null;
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
    // Reported as an explicit null, never coerced to 0: the wire must carry
    // "not observed" as distinctly as the local model does (#808).
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
 * `null` is returned for `off` — the case where the operator, or their editor,
 * has said no. It is deliberately **not** returned for a snapshot with no
 * windows: an operator who turned reporting on and whose
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

/** The tier applied when nothing has been configured. */
export const DEFAULT_USAGE_REPORTING_LEVEL: UsageReportingLevel = "full";

/**
 * Resolve the effective tier from the two consents and the tier itself.
 *
 * `extensionConsent` is already-resolved: `TelemetryConsentService.isEnabled()`
 * answers it, having folded in VSCode's own `telemetry.telemetryLevel` kill
 * switch and the `nightgauge.telemetry.enabled` setting. It arrives here as a
 * plain boolean rather than being re-derived, so there is exactly one place
 * that decides what "the operator consented" means — and this module is not it.
 *
 * `platformConsent` is the YAML-side `platform.telemetry.enabled`, which is
 * what the CLI and the Go scheduler read. It is a separate store from the
 * extension setting and neither can speak for the other, so both are checked.
 * Only an explicit `false` disables: `undefined` (never configured) and `null`
 * (the legacy prompt-pending state) both permit reporting, because the default
 * is on. An operator who wrote `false` keeps it — that is an answer, not a
 * default, and changing a default must never override one.
 */
export function resolveUsageReportingLevel(
  configured: UsageReportingLevel | undefined,
  platformConsent: boolean | null | undefined,
  extensionConsent: boolean
): UsageReportingLevel {
  if (!extensionConsent) {
    return "off";
  }
  if (platformConsent === false) {
    return "off";
  }
  return configured ?? DEFAULT_USAGE_REPORTING_LEVEL;
}

/**
 * The tier currently in force.
 *
 * `extensionConsent` is supplied by the caller — in practice
 * `TelemetryConsentService.isEnabled()` — rather than read here. Reading the
 * VSCode setting directly from this module would put a second, independent
 * answer to "has the operator consented?" in the codebase, and the two would
 * drift; it would also reach around the 6-tier config system that
 * `tests/integration/configRegressionGuard.test.ts` exists to protect.
 *
 * One uncertainty still fails closed: an uninitialised ConfigBridge returns
 * `off`, because at that point we cannot see an operator's explicit `false` and
 * must not report over the top of one we simply have not read yet. Every other
 * absence — no `platform` block, no `telemetry` block, no tier — resolves to
 * the default, which is the point of an opt-out default.
 */
export function getUsageReportingLevel(extensionConsent: boolean): UsageReportingLevel {
  if (!extensionConsent) {
    return "off";
  }
  const configBridge = ConfigBridge.getInstance();
  if (!configBridge.isInitialized()) {
    return "off";
  }
  const telemetry = configBridge.getPlatform()?.telemetry;
  return resolveUsageReportingLevel(telemetry?.usage_reporting, telemetry?.enabled, true);
}
