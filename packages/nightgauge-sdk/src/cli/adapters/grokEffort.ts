/**
 * Map Nightgauge effort vocabulary onto Grok Build CLI `--effort` values.
 *
 * Nightgauge {@link EFFORT_LEVELS} are `low|medium|high|xhigh|max`.
 * Grok also accepts `none` and `minimal`. Those extra rungs collapse to
 * `low` for registry / interlock purposes; spawn still forwards the
 * vendor flag when the caller already used Grok vocabulary.
 *
 * @see Issue #523
 */

import { EFFORT_LEVELS } from "../../eval/modelEvalSchemas.js";

/** Effort flags the Grok CLI documents for `--effort` / `--reasoning-effort`. */
export const GROK_CLI_EFFORTS = [
  "none",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
] as const;
export type GrokCliEffort = (typeof GROK_CLI_EFFORTS)[number];

/**
 * Collapse a Grok or Nightgauge effort string onto Nightgauge's vocabulary.
 * `none` and `minimal` become `low`. Unknown values return undefined.
 */
export function mapGrokEffortToNightgauge(effort: string | undefined): string | undefined {
  if (!effort) return undefined;
  const e = effort.trim().toLowerCase();
  if (e === "none" || e === "minimal") return "low";
  if ((EFFORT_LEVELS as readonly string[]).includes(e)) return e;
  return undefined;
}

/**
 * The `--effort` value to pass the Grok CLI. Nightgauge rungs pass through;
 * `none`/`minimal` stay as Grok-native flags.
 */
export function grokCliEffortFlag(effort: string | undefined): GrokCliEffort | undefined {
  if (!effort) return undefined;
  const e = effort.trim().toLowerCase();
  if ((GROK_CLI_EFFORTS as readonly string[]).includes(e)) {
    return e as GrokCliEffort;
  }
  return undefined;
}
