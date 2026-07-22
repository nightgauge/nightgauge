/**
 * customStageModels.test.ts (Issue #20)
 *
 * Verifies the Custom per-stage model persistence helpers that back the
 * "Custom…" footer-dropdown entry. Selections must round-trip through the SAME
 * config surface the resolver chain reads (`model_routing.mode` +
 * `pipeline.stage_models`), and clear cleanly when a preset is chosen.
 *
 * Pins persist to the LOCAL tier (.nightgauge/config.local.yaml): they
 * are per-operator steering, so flipping models in the UI must never dirty
 * the committed project config. Reads go through the tier-merged view, so a
 * local pin overrides (and coexists with) committed project keys.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { parse } from "yaml";
import {
  readStageModelSelections,
  writeStageModelSelections,
  clearStageModelSelections,
  hasCustomStageOverrides,
  CUSTOM_SELECTABLE_STAGES,
  type StageModelChoice,
} from "../../src/utils/customStageModels";
import type { PipelineStage } from "@nightgauge/sdk";

function allAuto(): Record<PipelineStage, StageModelChoice> {
  const sel = {} as Record<PipelineStage, StageModelChoice>;
  for (const stage of CUSTOM_SELECTABLE_STAGES) sel[stage] = "auto";
  return sel;
}

function configPath(root: string): string {
  return path.join(root, ".nightgauge", "config.yaml");
}

function localConfigPath(root: string): string {
  return path.join(root, ".nightgauge", "config.local.yaml");
}

function writeConfig(root: string, content: string): void {
  const dir = path.join(root, ".nightgauge");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, "config.yaml"), content, "utf-8");
}

describe("customStageModels (Issue #20)", () => {
  let root: string;

  beforeEach(() => {
    root = fs.mkdtempSync(path.join(os.tmpdir(), "custom-stage-models-"));
  });

  afterEach(() => {
    fs.rmSync(root, { recursive: true, force: true });
  });

  it("returns all-auto when no config file exists (fail-open, no throw)", () => {
    const sel = readStageModelSelections(root);
    expect(Object.values(sel).every((c) => c === "auto")).toBe(true);
    expect(hasCustomStageOverrides(root)).toBe(false);
  });

  it("writes hybrid routing mode and per-stage pins to the LOCAL tier, and round-trips", () => {
    const sel = allAuto();
    sel["feature-dev"] = "opus";
    sel["feature-planning"] = "fable";

    writeStageModelSelections(root, sel);

    // Pins land in config.local.yaml — the committed project file is never
    // created or touched by a UI model pick.
    expect(fs.existsSync(configPath(root))).toBe(false);
    const parsed = parse(fs.readFileSync(localConfigPath(root), "utf-8"));
    expect(parsed.model_routing.mode).toBe("hybrid");
    expect(parsed.pipeline.stage_models["feature-dev"]).toBe("opus");
    expect(parsed.pipeline.stage_models["feature-planning"]).toBe("fable");
    // Auto stages must NOT be written as pins.
    expect(parsed.pipeline.stage_models["pr-merge"]).toBeUndefined();

    const readBack = readStageModelSelections(root);
    expect(readBack["feature-dev"]).toBe("opus");
    expect(readBack["feature-planning"]).toBe("fable");
    expect(readBack["pr-merge"]).toBe("auto");
    expect(hasCustomStageOverrides(root)).toBe(true);
  });

  it("leaves the committed project config byte-identical; pins merge in on read", () => {
    const projectBody = [
      "# Acme pipeline config",
      "owner: AcmeCorp",
      "repo: acme-infra",
      "pipeline:",
      "  default_branch: main",
      "  max_concurrent: 4",
      "",
    ].join("\n");
    writeConfig(root, projectBody);

    const sel = allAuto();
    sel["issue-pickup"] = "haiku";
    writeStageModelSelections(root, sel);

    // The committed file is untouched — no dirty working tree from a UI pick.
    expect(fs.readFileSync(configPath(root), "utf-8")).toBe(projectBody);

    // The pin lives in the local tier and wins the merged read.
    const local = parse(fs.readFileSync(localConfigPath(root), "utf-8"));
    expect(local.pipeline.stage_models["issue-pickup"]).toBe("haiku");
    expect(local.model_routing.mode).toBe("hybrid");
    expect(readStageModelSelections(root)["issue-pickup"]).toBe("haiku");
  });

  it("removes a pin when a stage is set back to auto", () => {
    const sel = allAuto();
    sel["feature-dev"] = "opus";
    sel["pr-create"] = "haiku";
    writeStageModelSelections(root, sel);
    expect(readStageModelSelections(root)["feature-dev"]).toBe("opus");

    // Flip feature-dev back to auto, keep pr-create.
    sel["feature-dev"] = "auto";
    writeStageModelSelections(root, sel);

    const parsed = parse(fs.readFileSync(localConfigPath(root), "utf-8"));
    expect(parsed.pipeline.stage_models["feature-dev"]).toBeUndefined();
    expect(parsed.pipeline.stage_models["pr-create"]).toBe("haiku");
  });

  it("drops the stage_models map entirely when all pins are cleared to auto", () => {
    const sel = allAuto();
    sel["feature-dev"] = "opus";
    writeStageModelSelections(root, sel);

    sel["feature-dev"] = "auto";
    writeStageModelSelections(root, sel);

    const parsed = parse(fs.readFileSync(localConfigPath(root), "utf-8"));
    expect(parsed.pipeline?.stage_models).toBeUndefined();
    expect(hasCustomStageOverrides(root)).toBe(false);
  });

  it("does NOT honor pins when routing mode is automatic (reads back auto)", () => {
    writeConfig(
      root,
      [
        "model_routing:",
        "  mode: automatic",
        "pipeline:",
        "  stage_models:",
        "    feature-dev: opus",
        "",
      ].join("\n")
    );
    // Pins exist on disk but automatic mode ignores them, so effective = auto.
    expect(readStageModelSelections(root)["feature-dev"]).toBe("auto");
    expect(hasCustomStageOverrides(root)).toBe(false);
  });

  it("clearStageModelSelections removes pins and resets routing to automatic", () => {
    const sel = allAuto();
    sel["feature-dev"] = "opus";
    writeStageModelSelections(root, sel);
    expect(hasCustomStageOverrides(root)).toBe(true);

    clearStageModelSelections(root);

    const parsed = parse(fs.readFileSync(localConfigPath(root), "utf-8"));
    expect(parsed.pipeline?.stage_models).toBeUndefined();
    expect(parsed.model_routing.mode).toBe("automatic");
    expect(hasCustomStageOverrides(root)).toBe(false);
  });

  it("clearStageModelSelections also clears legacy pins committed in the project config", () => {
    // Pins written by older versions landed in the committed config.yaml. A
    // stale committed pin would silently override any later preset selection,
    // so clear must scrub the project tier too.
    writeConfig(
      root,
      [
        "model_routing:",
        "  mode: hybrid",
        "pipeline:",
        "  stage_models:",
        "    feature-dev: opus",
        "",
      ].join("\n")
    );
    expect(hasCustomStageOverrides(root)).toBe(true);

    clearStageModelSelections(root);

    const parsed = parse(fs.readFileSync(configPath(root), "utf-8"));
    expect(parsed.pipeline?.stage_models).toBeUndefined();
    expect(parsed.model_routing.mode).toBe("automatic");
    expect(hasCustomStageOverrides(root)).toBe(false);
  });

  it("clearStageModelSelections is a no-op when no config exists", () => {
    expect(() => clearStageModelSelections(root)).not.toThrow();
    expect(fs.existsSync(configPath(root))).toBe(false);
    expect(fs.existsSync(localConfigPath(root))).toBe(false);
  });

  it("ignores invalid model values on read", () => {
    writeConfig(
      root,
      [
        "model_routing:",
        "  mode: hybrid",
        "pipeline:",
        "  stage_models:",
        "    feature-dev: gpt-nonsense",
        "    pr-create: haiku",
        "",
      ].join("\n")
    );
    const sel = readStageModelSelections(root);
    expect(sel["feature-dev"]).toBe("auto"); // invalid → auto
    expect(sel["pr-create"]).toBe("haiku");
  });
});
