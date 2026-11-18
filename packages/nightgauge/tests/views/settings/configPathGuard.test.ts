/**
 * configPathGuard.test.ts
 *
 * Settings paths are accepted only when the config schema defines them.
 */

import { describe, it, expect } from "vitest";
import { isConfigSchemaPath } from "../../../src/views/settings/configPathGuard";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { NightgaugeConfig } from "../../../src/views/settings/types";

describe("isConfigSchemaPath", () => {
  it.each([
    "pipeline.max_concurrent",
    "pipeline.stage_adapters.feature-dev",
    "pipeline.stage_models.issue-pickup",
    "ui.core.adapter",
  ])("accepts schema path %s", (path) => {
    expect(isConfigSchemaPath(path)).toBe(true);
  });

  it.each([
    "not_a_section.anything",
    "pipeline.not_a_field",
    "pipeline.max_concurrent.deeper",
    "__proto__.polluted",
    "pipeline.constructor",
    "pipeline..max_concurrent",
    "",
    "pipeline.max concurrent",
  ])("rejects %j", (path) => {
    expect(isConfigSchemaPath(path)).toBe(false);
  });

  it("rejects non-string paths", () => {
    expect(isConfigSchemaPath(undefined)).toBe(false);
    expect(isConfigSchemaPath(42)).toBe(false);
  });

  it("accepts every field path the settings panel renders", () => {
    const html = getSettingsHtml(
      { cspSource: "test" } as never,
      getDefaultConfig() as NightgaugeConfig
    );
    const paths = [
      ...new Set([...html.matchAll(/data-path="([A-Za-z0-9_.-]+)"/g)].map((m) => m[1])),
    ];
    expect(paths.length).toBeGreaterThan(20);
    expect(paths.filter((p) => !isConfigSchemaPath(p))).toEqual([]);
  });
});
