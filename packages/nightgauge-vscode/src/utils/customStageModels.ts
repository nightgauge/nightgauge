/**
 * Custom per-stage model selection (Issue #20)
 *
 * Backs the "Custom…" entry in the footer performance-mode dropdown. A user can
 * pin an explicit model per pipeline stage, or leave a stage on "Auto" (defer to
 * the adaptive router). Selections persist to the SAME config surface the
 * resolver chain already reads — no new orchestration:
 *
 *   - `model_routing.mode: hybrid`   (top-level; read by getModelRoutingMode)
 *   - `pipeline.stage_models.<stage>: <tier>`   (read by getStageModel)
 *
 * In `hybrid` mode, a pinned stage uses its pin and an unpinned ("Auto") stage
 * returns undefined from `getStageModel`, deferring to `AutoModelSelector`
 * (stageResolver.ts). Selecting a preset mode elsewhere calls
 * `clearStageModelSelections` so presets behave as advertised rather than being
 * silently overridden by stale pins.
 *
 * Writes go through the `yaml` Document API so surrounding comments and
 * unrelated keys in config.yaml are preserved.
 *
 * @see docs/decisions/012-performance-mode-envelopes.md — Proposal B
 * @see packages/nightgauge-vscode/src/utils/resolvers/stageResolver.ts — getStageModel
 * @see packages/nightgauge-vscode/src/utils/resolvers/modelResolver.ts — getModelRoutingMode
 */

import * as fs from "fs";
import * as path from "path";
import { parseDocument } from "yaml";
import type { PipelineStage } from "@nightgauge/sdk";
import type { DefaultModel } from "./incrediConfig";
import { resolveConfigPathSync, getConfigPaths } from "./configPathResolver";
import { readEffectiveConfigTextSync } from "./mergedConfigReader";

/**
 * The six pipeline stages, in execution order, that a user can pin a model for.
 * Matches the stage set the resolver chain honors in `getStageModel`.
 */
export const CUSTOM_SELECTABLE_STAGES: readonly PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

/** "auto" = defer to the adaptive router (no pin written). */
export type StageModelChoice = DefaultModel | "auto";

/** Selectable choices, in ascending capability order (auto first). */
export const STAGE_MODEL_CHOICES: readonly StageModelChoice[] = [
  "auto",
  "haiku",
  "sonnet",
  "opus",
  "fable",
] as const;

const VALID_MODELS: readonly DefaultModel[] = ["haiku", "sonnet", "opus", "fable"] as const;

/** Routing modes under which `pipeline.stage_models` pins are actually honored. */
const PIN_HONORING_MODES = new Set(["manual", "hybrid"]);

function isValidModel(value: unknown): value is DefaultModel {
  return typeof value === "string" && VALID_MODELS.includes(value as DefaultModel);
}

/**
 * Read the current per-stage selections from config.yaml.
 *
 * Every stage is present in the result; a stage with no valid pin (or one that
 * would not be honored because routing mode is `automatic`) reads back as
 * `"auto"`. This is the initial state the picker renders.
 */
export function readStageModelSelections(
  workspaceRoot: string
): Record<PipelineStage, StageModelChoice> {
  const result = {} as Record<PipelineStage, StageModelChoice>;
  for (const stage of CUSTOM_SELECTABLE_STAGES) result[stage] = "auto";

  try {
    // No exists-gate on the project file: pins live in the local tier, so the
    // merged read must run even when only config.local.yaml is present.
    const pathResult = resolveConfigPathSync(workspaceRoot);
    const parsed = parseDocument(readEffectiveConfigTextSync(pathResult)).toJS() as Record<
      string,
      unknown
    > | null;
    if (!parsed) return result;

    // Pins are only honored when routing mode allows them.
    const routingMode = (parsed.model_routing as Record<string, unknown> | undefined)?.mode;
    if (typeof routingMode === "string" && !PIN_HONORING_MODES.has(routingMode)) {
      return result;
    }

    const stageModels = (parsed.pipeline as Record<string, unknown> | undefined)?.stage_models as
      Record<string, unknown> | undefined;
    if (!stageModels) return result;

    for (const stage of CUSTOM_SELECTABLE_STAGES) {
      const value = stageModels[stage];
      if (isValidModel(value)) result[stage] = value;
    }
  } catch {
    // Non-critical — fall back to all-auto.
  }
  return result;
}

/**
 * True when at least one stage is pinned to a concrete model AND the routing
 * mode honors pins. This is the condition under which the footer badge shows
 * "Custom" instead of the preset mode name.
 */
export function hasCustomStageOverrides(workspaceRoot: string): boolean {
  const selections = readStageModelSelections(workspaceRoot);
  return Object.values(selections).some((choice) => choice !== "auto");
}

/**
 * Persist per-stage selections. Sets `model_routing.mode: hybrid` so `Auto`
 * stages defer to the router, writes a pin for each concrete choice, and removes
 * the pin for any stage set back to `Auto`. Preserves comments and unrelated
 * keys via the yaml Document API. Creates config.yaml if it does not yet exist.
 */
export function writeStageModelSelections(
  workspaceRoot: string,
  selections: Record<PipelineStage, StageModelChoice>
): void {
  // Pins are a per-operator steering knob, not team policy — they land in the
  // gitignored local tier (config.local.yaml) so flipping models in the UI
  // never dirties the working tree. Local wins the tier merge, so a local pin
  // also overrides any legacy committed pin.
  const targetPath = getConfigPaths(workspaceRoot).local;

  let existing = "";
  try {
    existing = fs.readFileSync(targetPath, "utf-8");
  } catch {
    // Local config does not exist yet — created below.
  }
  const doc = parseDocument(existing);

  doc.setIn(["model_routing", "mode"], "hybrid");

  for (const stage of CUSTOM_SELECTABLE_STAGES) {
    const choice = selections[stage];
    if (choice === "auto") {
      // deleteIn throws if an intermediate node is absent — guard with hasIn.
      if (doc.hasIn(["pipeline", "stage_models", stage])) {
        doc.deleteIn(["pipeline", "stage_models", stage]);
      }
    } else {
      doc.setIn(["pipeline", "stage_models", stage], choice);
    }
  }

  // Drop an emptied `stage_models` map so the file does not accrue dead keys.
  const stageModels = doc.getIn(["pipeline", "stage_models"]) as { items?: unknown[] } | undefined;
  if (stageModels && Array.isArray(stageModels.items) && stageModels.items.length === 0) {
    doc.deleteIn(["pipeline", "stage_models"]);
  }

  fs.mkdirSync(path.dirname(targetPath), { recursive: true });
  fs.writeFileSync(targetPath, doc.toString(), "utf-8");
}

/**
 * Remove all custom per-stage pins and reset routing to `automatic` so a
 * subsequently-selected preset mode (efficiency/elevated/maximum/frontier)
 * governs routing without stale pins overriding it. No-op when config is absent.
 */
export function clearStageModelSelections(workspaceRoot: string): void {
  // Pins may live in the local tier (current write target) or in the project
  // tier (legacy committed pins). Clear both — a stale pin in either file
  // would silently override a subsequently-selected preset mode.
  const candidates = [getConfigPaths(workspaceRoot).local];
  const pathResult = resolveConfigPathSync(workspaceRoot);
  if (pathResult.exists) {
    candidates.push(pathResult.path);
  }

  for (const filePath of candidates) {
    let existing: string;
    try {
      existing = fs.readFileSync(filePath, "utf-8");
    } catch {
      continue;
    }
    const doc = parseDocument(existing);

    // Only touch keys the Custom flow manages, and only write when the file
    // actually contains one of them (avoids no-op rewrites of committed files).
    const hasPins = doc.hasIn(["pipeline", "stage_models"]);
    const hasMode = doc.hasIn(["model_routing", "mode"]);
    if (!hasPins && !hasMode) continue;

    if (hasPins) {
      doc.deleteIn(["pipeline", "stage_models"]);
    }
    if (hasMode) {
      doc.setIn(["model_routing", "mode"], "automatic");
    }

    fs.writeFileSync(filePath, doc.toString(), "utf-8");
  }
}
