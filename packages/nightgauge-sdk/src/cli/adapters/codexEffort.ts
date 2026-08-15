/**
 * Validate the Codex reasoning effort against the CLI vocabulary AND the
 * model registry's `supported_efforts` before dispatch (#569).
 *
 * The static {@link CODEX_REASONING_EFFORTS} list is SYNTAX validation only —
 * the vocabulary the Codex CLI accepts for `model_reasoning_effort`. Whether
 * the resolved model actually serves a rung is the registry's call (#569): a
 * rung the model does not declare used to sail through this filter and reach
 * the CLI unchecked — the same silent pass-through class as #532's grok
 * signature. {@link codexReasoningEffortFlag} now consults the registry and
 * fails closed BEFORE spawn instead, mirroring grokEffort.ts.
 *
 * Codex's sub-`low` rung (`none`) collapses to `low` for the registry
 * membership check — the same rule the extension gate and the grok path apply
 * to vendor-native sub-`low` rungs (#523) — while the dispatched value is
 * forwarded untouched. (Deriving a codex-specific ladder is #435, out of
 * scope.)
 *
 * @see Issue #569 - supported_efforts enforced at every dispatch path
 * @see Issue #336 - supported_efforts semantics ([] = no effort axis)
 */

import { getModelDescriptor } from "../../eval/modelRegistry.js";
import { AdapterError } from "./errors.js";

/** Effort values the Codex CLI accepts for `model_reasoning_effort`. */
export const CODEX_REASONING_EFFORTS = ["none", "low", "medium", "high", "xhigh", "max"] as const;
export type CodexReasoningEffort = (typeof CODEX_REASONING_EFFORTS)[number];

const CODEX_DOCS_URL = "https://developers.openai.com/codex";

/**
 * The `model_reasoning_effort` value to pass the Codex CLI, or undefined when
 * no effort is requested.
 *
 * Enforcement (#569) — the requested rung must be a member of the RESOLVED
 * model's `supported_efforts`:
 *
 * - A value outside the CLI vocabulary throws an {@link AdapterError}
 *   (`CONFIG_INVALID`) — the Codex CLI would reject it at spawn anyway, so
 *   fail fast with the vocabulary named (preserves the pre-#569 contract).
 * - `model` is the resolved codex model the adapter dispatches with; band
 *   names resolve to the registry's openai model, concrete ids to themselves.
 * - A model with NO registry descriptor passes through with a logged warning,
 *   never a hard failure (#336) — there is nothing to validate against.
 * - `supported_efforts: []` is a positive declaration ("no effort axis"), so
 *   ANY explicit effort throws (#336).
 * - A rung the model does not declare throws an {@link AdapterError}
 *   (`CONFIG_INVALID`) naming the model, the requested effort, and the
 *   declared ladder — never a silent pass-through, never a silent downgrade
 *   (#75).
 */
export function codexReasoningEffortFlag(
  effort: string | undefined,
  model: string | undefined = process.env.NIGHTGAUGE_CODEX_MODEL
): CodexReasoningEffort | undefined {
  if (!effort) return undefined;
  const e = effort.trim().toLowerCase();
  if (!e) return undefined;
  if (!(CODEX_REASONING_EFFORTS as readonly string[]).includes(e)) {
    throw new AdapterError(
      `Invalid NIGHTGAUGE_CODEX_REASONING_EFFORT '${e}'. ` +
        `Expected one of: ${CODEX_REASONING_EFFORTS.join(", ")}.`,
      "CONFIG_INVALID",
      "Codex",
      CODEX_DOCS_URL
    );
  }

  const trimmedModel = model?.trim();
  const descriptor = trimmedModel ? getModelDescriptor(trimmedModel, "openai") : undefined;
  if (!descriptor) {
    console.warn(
      `[codex-adapter] warning: model '${trimmedModel || "(adapter default)"}' has no ` +
        `registry descriptor — cannot verify effort '${e}' against supported_efforts; ` +
        `passing through`
    );
    return e as CodexReasoningEffort;
  }

  const ladder = descriptor.supported_efforts;
  if (ladder.length === 0) {
    throw new AdapterError(
      `Effort '${e}' is not supported by model '${descriptor.id}': the model declares ` +
        `no effort axis (supported_efforts: []).\n` +
        `Fix: unset NIGHTGAUGE_CODEX_REASONING_EFFORT or route to a model with an effort ladder.`,
      "CONFIG_INVALID",
      "Codex",
      CODEX_DOCS_URL
    );
  }

  // Codex-native sub-`low` rung: `none` collapses to `low` for the registry
  // membership check (#523 rule); the dispatched value stays `none`.
  const normalized = e === "none" ? "low" : e;
  if (!(ladder as readonly string[]).includes(normalized)) {
    const note = normalized !== e ? ` (normalized to '${normalized}')` : "";
    throw new AdapterError(
      `Effort '${e}'${note} is not supported by model '${descriptor.id}' ` +
        `(supports: ${ladder.join(", ")}).\n` +
        `Fix: set NIGHTGAUGE_CODEX_REASONING_EFFORT to a supported level or route to a model ` +
        `that accepts '${e}'.`,
      "CONFIG_INVALID",
      "Codex",
      CODEX_DOCS_URL
    );
  }

  return e as CodexReasoningEffort;
}
