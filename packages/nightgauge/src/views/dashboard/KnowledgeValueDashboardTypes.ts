/**
 * KnowledgeValueDashboardTypes — UI-side types for the Knowledge Value
 * dashboard (#3600). Mirrors the IPC `KnowledgeMetricsResult` payload and adds
 * delta-vs-prior-window state computed TS-side (ADR-002).
 */

import type { KnowledgeMetricsResult } from "../../services/IpcClientBase";

export type WindowDays = 7 | 30 | 90;

export interface DeltaTotals {
  writes: number;
  reads: number;
  recalls: number;
  recall_hits: number;
  graduations: number;
}

export interface KnowledgeValueState {
  windowDays: WindowDays;
  current: KnowledgeMetricsResult | null;
  prior: KnowledgeMetricsResult | null;
  delta: DeltaTotals | null;
  loadedAt: number; // epoch ms; powers the 60 s cache TTL debounce
  loading: boolean;
  error: string | null;
}

/**
 * computeDelta returns the absolute delta between two windows. Returns null
 * when either side is missing — the UI renders the cell as "—".
 */
export function computeDelta(
  current: KnowledgeMetricsResult | null,
  prior: KnowledgeMetricsResult | null
): DeltaTotals | null {
  if (!current || !prior) return null;
  return {
    writes: current.totals.writes - prior.totals.writes,
    reads: current.totals.reads - prior.totals.reads,
    recalls: current.totals.recalls - prior.totals.recalls,
    recall_hits: current.totals.recall_hits - prior.totals.recall_hits,
    graduations: current.totals.graduations - prior.totals.graduations,
  };
}

/**
 * formatDelta renders a signed integer with the matching "▲"/"▼"/"·" glyph.
 * Used by the HTML module to keep all rendering logic out of the dashboard
 * provider.
 */
export function formatDelta(d: number | null | undefined): string {
  if (d === null || d === undefined) return "—";
  if (d > 0) return `▲ ${d}`;
  if (d < 0) return `▼ ${Math.abs(d)}`;
  return `· 0`;
}

/**
 * hitRateBand classifies a hit rate into a color band per AC.
 *   > 50% → green
 *   20–50% → yellow
 *   < 20% → red
 *   null/no recalls → neutral (rendered as "—")
 */
export type HitRateBand = "green" | "yellow" | "red" | "neutral";

export function hitRateBand(hitRate: number | null | undefined): HitRateBand {
  if (hitRate === null || hitRate === undefined) return "neutral";
  if (hitRate > 0.5) return "green";
  if (hitRate >= 0.2) return "yellow";
  return "red";
}
