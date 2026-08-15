/**
 * Map Nightgauge effort vocabulary onto Grok Build CLI `--effort` values, and
 * enforce the model registry's `supported_efforts` before dispatch.
 *
 * Nightgauge {@link EFFORT_LEVELS} are `low|medium|high|xhigh|max`.
 * Grok also accepts `none` and `minimal`. Those extra rungs collapse to
 * `low` for registry / interlock purposes; spawn still forwards the
 * vendor flag when the caller already used Grok vocabulary.
 *
 * The static {@link GROK_CLI_EFFORTS} list is SYNTAX validation only — the
 * vocabulary the CLI documents for `--effort`. Whether the resolved model
 * actually serves a rung is the registry's call (#569): a rung the model does
 * not declare used to sail through this filter and die inside the CLI as
 * #532's signature (`unknown effort level 'xhigh'`, exit 1 in seconds, no
 * work, nothing classified). {@link grokCliEffortFlag} now consults the
 * registry and fails closed BEFORE spawn instead.
 *
 * @see Issue #523 - grok-native rungs collapse to `low`
 * @see Issue #569 - supported_efforts enforced at every dispatch path
 */

import { EFFORT_LEVELS } from "../../eval/modelEvalSchemas.js";
import { getModelDescriptor } from "../../eval/modelRegistry.js";
import { AdapterError } from "./errors.js";

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

const GROK_DOCS_URL = "https://docs.x.ai/build/overview";

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
 *
 * Enforcement (#569), applied AFTER the #523 normalization — the normalized
 * rung must be a member of the RESOLVED model's `supported_efforts`:
 *
 * - `model` defaults to the same env resolution the adapter dispatches with
 *   (`NIGHTGAUGE_GROK_MODEL` → `NIGHTGAUGE_MODEL`); band names resolve to the
 *   registry's xai model, concrete ids to themselves.
 * - A model with NO registry descriptor passes through with a logged warning,
 *   never a hard failure (#336) — there is nothing to validate against.
 * - `supported_efforts: []` is a positive declaration ("no effort axis"), so
 *   ANY explicit effort throws (#336).
 * - A rung the model does not declare throws an {@link AdapterError}
 *   (`CONFIG_INVALID`) naming the model, the requested effort, and the
 *   declared ladder — never a silent pass-through, never a silent downgrade
 *   (#75).
 * - A value outside the CLI vocabulary is dropped as before (returns
 *   undefined), with a logged warning instead of silence.
 */
export function grokCliEffortFlag(
  effort: string | undefined,
  model: string | undefined = process.env.NIGHTGAUGE_GROK_MODEL ?? process.env.NIGHTGAUGE_MODEL
): GrokCliEffort | undefined {
  if (!effort) return undefined;
  const e = effort.trim().toLowerCase();
  if (!(GROK_CLI_EFFORTS as readonly string[]).includes(e)) {
    if (e) {
      console.warn(
        `[grok-adapter] warning: effort '${e}' is not a Grok CLI effort ` +
          `(${GROK_CLI_EFFORTS.join("|")}) — the --effort flag will be omitted ` +
          `and the provider default used`
      );
    }
    return undefined;
  }

  const trimmedModel = model?.trim();
  const descriptor = trimmedModel ? getModelDescriptor(trimmedModel, "xai") : undefined;
  if (!descriptor) {
    console.warn(
      `[grok-adapter] warning: model '${trimmedModel || "(adapter default)"}' has no ` +
        `registry descriptor — cannot verify effort '${e}' against supported_efforts; ` +
        `passing through`
    );
    return e as GrokCliEffort;
  }

  const ladder = descriptor.supported_efforts;
  if (ladder.length === 0) {
    throw new AdapterError(
      `Effort '${e}' is not supported by model '${descriptor.id}': the model declares ` +
        `no effort axis (supported_efforts: []).\n` +
        `Fix: unset NIGHTGAUGE_GROK_EFFORT or route to a model with an effort ladder.`,
      "CONFIG_INVALID",
      "Grok",
      GROK_DOCS_URL
    );
  }

  const normalized = mapGrokEffortToNightgauge(e);
  if (!normalized || !(ladder as readonly string[]).includes(normalized)) {
    const note = normalized && normalized !== e ? ` (normalized to '${normalized}')` : "";
    throw new AdapterError(
      `Effort '${e}'${note} is not supported by model '${descriptor.id}' ` +
        `(supports: ${ladder.join(", ")}).\n` +
        `Fix: set NIGHTGAUGE_GROK_EFFORT to a supported level or route to a model ` +
        `that accepts '${e}'.`,
      "CONFIG_INVALID",
      "Grok",
      GROK_DOCS_URL
    );
  }

  return e as GrokCliEffort;
}
